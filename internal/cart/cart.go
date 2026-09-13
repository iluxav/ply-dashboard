// Package cart assembles a deployment from one or more services (a "cart"),
// serializes it to the exact TOML a ply deployment file uses, and parses one
// back. A cart of many services becomes a `[package]` + `[[service]]`
// composition; a cart of one collapses to a flat single-source order, so a
// trivial deploy stays a trivial file. The in-progress cart lives as a draft
// file the dashboard promotes atomically on deploy.
package cart

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Kind is how one card's source is defined — mirrors ply's member sources.
const (
	KindRepo     = "repo"     // a git repo built on the host   (run = "git+<ref>")
	KindRegistry = "registry" // a registry ref                 (run = "<name>@<ver>")
	KindImage    = "image"    // an image file or URL           (run/image = "<ref>")
)

// Card is one service in the cart.
type Card struct {
	Name    string
	Kind    string
	Ref     string // repo URL, registry ref (name@ver), or image path/URL
	Build   string // repo only
	Runtime string // repo only
	Publish []string
	Domain  []string
	Env     []string // "KEY=VALUE"
	After   []string // member names this one starts after
	Volume  []string
}

// Cart is the whole deployment being assembled.
type Cart struct {
	Name  string
	Cards []Card
}

var nameSafe = regexp.MustCompile(`[^a-z0-9-]+`)

// SafeName lowercases and strips a name to a filesystem- and ply-safe stem.
func SafeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nameSafe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// tq quotes a value as a TOML basic string.
func tq(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(v) + `"`
}

// tarr renders a string slice as a TOML array of quoted strings.
func tarr(vs []string) string {
	parts := make([]string, 0, len(vs))
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			parts = append(parts, tq(v))
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func nonEmpty(vs []string) []string {
	out := vs[:0:0]
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

// runValue is the `run =` a composition member carries for this card.
func (c Card) runValue() string {
	switch c.Kind {
	case KindRepo:
		return "git+" + c.Ref
	default: // registry ref or image ref run as-is
		return c.Ref
	}
}

// member renders one `[[service]]` block.
func (c Card) member() string {
	var b strings.Builder
	b.WriteString("\n[[service]]\n")
	fmt.Fprintf(&b, "run = %s\n", tq(c.runValue()))
	fmt.Fprintf(&b, "name = %s\n", tq(c.Name))
	if c.Kind == KindRepo {
		if s := strings.TrimSpace(c.Build); s != "" {
			fmt.Fprintf(&b, "build = %s\n", tq(s))
		}
		if s := strings.TrimSpace(c.Runtime); s != "" {
			fmt.Fprintf(&b, "runtime = %s\n", tq(s))
		}
	}
	if v := nonEmpty(c.After); len(v) > 0 {
		fmt.Fprintf(&b, "after = %s\n", tarr(v))
	}
	if v := nonEmpty(c.Publish); len(v) > 0 {
		fmt.Fprintf(&b, "publish = %s\n", tarr(v))
	}
	if v := nonEmpty(c.Domain); len(v) > 0 {
		fmt.Fprintf(&b, "domain = %s\n", tarr(v))
	}
	if v := nonEmpty(c.Volume); len(v) > 0 {
		fmt.Fprintf(&b, "volume = %s\n", tarr(v))
	}
	if v := nonEmpty(c.Env); len(v) > 0 {
		fmt.Fprintf(&b, "env = %s\n", tarr(v)) // member env is the array form
	}
	return b.String()
}

// composition renders `[package]` + one `[[service]]` per card. This is the
// round-trippable draft form (FromTOML restores it exactly), used for state.
func (c Cart) composition() string {
	name := c.Name
	if name == "" {
		name = "draft"
	}
	var b strings.Builder
	b.WriteString("[package]\n")
	fmt.Fprintf(&b, "name = %s\n", tq(name))
	b.WriteString("version = \"0.1.0\"\n")
	for _, card := range c.Cards {
		b.WriteString(card.member())
	}
	return b.String()
}

// flat renders a single-source order for a one-card cart, so a trivial deploy
// stays a trivial, diff-clean file (repo=/app=/image= + the run overrides).
func (c Cart) flat() string {
	card := c.Cards[0]
	var b strings.Builder
	switch card.Kind {
	case KindRepo:
		fmt.Fprintf(&b, "repo = %s\n", tq(card.Ref))
		if s := strings.TrimSpace(card.Build); s != "" {
			fmt.Fprintf(&b, "build = %s\n", tq(s))
		}
		if s := strings.TrimSpace(card.Runtime); s != "" {
			fmt.Fprintf(&b, "runtime = %s\n", tq(s))
		}
	case KindImage:
		fmt.Fprintf(&b, "image = %s\n", tq(card.Ref))
	default: // registry: split name@version
		name, ver, _ := strings.Cut(card.Ref, "@")
		fmt.Fprintf(&b, "app = %s\n", tq(name))
		if ver != "" {
			fmt.Fprintf(&b, "version = %s\n", tq(ver))
		}
	}
	if v := nonEmpty(card.Publish); len(v) > 0 {
		fmt.Fprintf(&b, "publish = %s\n", tarr(v))
	}
	if v := nonEmpty(card.Domain); len(v) > 0 {
		fmt.Fprintf(&b, "domain = %s\n", tarr(v))
	}
	if v := nonEmpty(card.Volume); len(v) > 0 {
		fmt.Fprintf(&b, "volume = %s\n", tarr(v))
	}
	if env := nonEmpty(card.Env); len(env) > 0 {
		b.WriteString("\n[env]\n")
		for _, kv := range env {
			k, val, _ := strings.Cut(kv, "=")
			if k = strings.TrimSpace(k); k != "" {
				fmt.Fprintf(&b, "%s = %s\n", k, tq(val))
			}
		}
	}
	return b.String()
}

// ToTOML is the FINAL deployment file: one card collapses to a flat order,
// many become a composition. Empty cart yields an empty string.
func (c Cart) ToTOML() string {
	switch len(c.Cards) {
	case 0:
		return ""
	case 1:
		return c.flat()
	default:
		return c.composition()
	}
}

// DraftTOML is the state form — always a composition, so a partial cart (even
// one card) round-trips through FromTOML without losing the name or any field.
func (c Cart) DraftTOML() string { return c.composition() }

type rawMember struct {
	Run     string   `toml:"run"`
	Name    string   `toml:"name"`
	Build   string   `toml:"build"`
	Runtime string   `toml:"runtime"`
	Publish []string `toml:"publish"`
	Domain  []string `toml:"domain"`
	Env     []string `toml:"env"`
	After   []string `toml:"after"`
	Volume  []string `toml:"volume"`
}

type rawSpec struct {
	Package struct {
		Name string `toml:"name"`
	} `toml:"package"`
	Service []rawMember `toml:"service"`
	// flat single-source order
	Repo    string            `toml:"repo"`
	App     string            `toml:"app"`
	Image   string            `toml:"image"`
	Build   string            `toml:"build"`
	Runtime string            `toml:"runtime"`
	Version string            `toml:"version"`
	Publish []string          `toml:"publish"`
	Domain  []string          `toml:"domain"`
	Volume  []string          `toml:"volume"`
	Env     map[string]string `toml:"env"`
}

func kindOfRun(run string) (kind, ref string) {
	switch {
	case strings.HasPrefix(run, "git+"):
		return KindRepo, strings.TrimPrefix(run, "git+")
	case strings.HasSuffix(run, ".img"), strings.HasPrefix(run, "http"):
		return KindImage, run
	default:
		return KindRegistry, run
	}
}

// FromTOML parses a deployment file back into a cart: a `[[service]]`
// composition yields N cards (and the package name); a flat order yields one.
// The caller may override Name (for a flat file, it comes from the filename).
func FromTOML(spec string) (Cart, error) {
	var raw rawSpec
	if _, err := toml.Decode(spec, &raw); err != nil {
		return Cart{}, err
	}
	// composition
	if len(raw.Service) > 0 {
		c := Cart{Name: raw.Package.Name}
		for _, m := range raw.Service {
			kind, ref := kindOfRun(m.Run)
			name := m.Name
			if name == "" {
				name = deriveName(ref)
			}
			c.Cards = append(c.Cards, Card{
				Name: name, Kind: kind, Ref: ref,
				Build: m.Build, Runtime: m.Runtime,
				Publish: m.Publish, Domain: m.Domain, Env: m.Env,
				After: m.After, Volume: m.Volume,
			})
		}
		return c, nil
	}
	// flat single-source order
	card := Card{Publish: raw.Publish, Domain: raw.Domain, Volume: raw.Volume}
	switch {
	case raw.Repo != "":
		card.Kind, card.Ref, card.Build, card.Runtime = KindRepo, raw.Repo, raw.Build, raw.Runtime
	case raw.Image != "":
		card.Kind, card.Ref = KindImage, raw.Image
	case raw.App != "":
		card.Kind = KindRegistry
		card.Ref = raw.App
		if raw.Version != "" {
			card.Ref = raw.App + "@" + raw.Version
		}
	default:
		return Cart{Name: raw.Package.Name}, nil // empty/unknown — no cards
	}
	for _, k := range sortedKeys(raw.Env) {
		card.Env = append(card.Env, k+"="+raw.Env[k])
	}
	card.Name = deriveName(card.Ref)
	return Cart{Name: raw.Package.Name, Cards: []Card{card}}, nil
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// DeriveName guesses a service name from a source ref (repo/image basename, or
// a registry name without its version) — for prefilling a new card.
func DeriveName(ref string) string { return deriveName(ref) }

// deriveName guesses a member name from a source ref (repo/image basename, or
// a registry name without its version).
func deriveName(ref string) string {
	ref = strings.TrimSuffix(ref, ".git")
	if name, _, ok := strings.Cut(ref, "@"); ok && !strings.Contains(ref, "/") {
		return name // registry name@ver
	}
	ref = strings.TrimSuffix(ref, "/")
	if i := strings.LastIndexAny(ref, "/:"); i >= 0 {
		ref = ref[i+1:]
	}
	ref = strings.TrimSuffix(ref, ".img")
	if name, _, ok := strings.Cut(ref, "@"); ok {
		ref = name
	}
	return SafeName(ref)
}

// --- draft store -------------------------------------------------------------
// A draft lives in <deployments>/.drafts/<id>.toml — a dot-dir reconcile
// ignores, exactly like .status/ — so a half-built stack never deploys.

func draftsDir(depDir string) string { return filepath.Join(depDir, ".drafts") }
func draftPath(depDir, id string) string {
	return filepath.Join(draftsDir(depDir), SafeName(id)+".toml")
}

// WriteDraft saves the cart's round-trippable state under a draft id.
func WriteDraft(depDir, id string, c Cart) error {
	if err := os.MkdirAll(draftsDir(depDir), 0o755); err != nil {
		return err
	}
	return os.WriteFile(draftPath(depDir, id), []byte(c.DraftTOML()), 0o644)
}

// ReadDraft loads a draft cart; a missing draft is an empty cart, not an error.
func ReadDraft(depDir, id string) (Cart, error) {
	b, err := os.ReadFile(draftPath(depDir, id))
	if os.IsNotExist(err) {
		return Cart{}, nil
	}
	if err != nil {
		return Cart{}, err
	}
	return FromTOML(string(b))
}

// DeleteDraft removes a draft (best-effort).
func DeleteDraft(depDir, id string) { _ = os.Remove(draftPath(depDir, id)) }

// Deploy writes the cart's FINAL TOML to <deployments>/<name>.toml atomically
// (temp inside the ignored .drafts dir, then rename into place so reconcile
// sees a complete file appear), then clears the draft.
func Deploy(depDir, id, name string, c Cart) error {
	name = SafeName(name)
	if name == "" {
		return fmt.Errorf("name the deployment before deploying")
	}
	if len(c.Cards) == 0 {
		return fmt.Errorf("add at least one service before deploying")
	}
	if err := os.MkdirAll(draftsDir(depDir), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(draftsDir(depDir), name+".final.tmp")
	if err := os.WriteFile(tmp, []byte(c.ToTOML()), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(depDir, name+".toml")); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	DeleteDraft(depDir, id)
	return nil
}

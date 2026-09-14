// Package cart assembles a deployment from one or more services (a "cart"),
// serializes it to the exact TOML a ply deployment file uses, and parses one
// back. A cart of many services becomes a `[package]` + `[[service]]`
// composition; a cart of one collapses to a flat single-source order, so a
// trivial deploy stays a trivial file. The in-progress cart lives as a draft
// file the dashboard promotes atomically on deploy.
package cart

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Kind is how one card's source is defined — mirrors ply's member sources.
const (
	KindRepo     = "repo"     // a git repo built on the host   (run = "git+<ref>")
	KindRegistry = "registry" // a registry ref                 (run = "<name>@<ver>")
	KindImage    = "image"    // an image file or URL           (run/image = "<ref>")
	KindDocker   = "docker"   // an OCI image imported on host   (run = "docker://<ref>", or docker=)
)

// KV is one un-modeled deployment key preserved verbatim across a round-trip
// (its value already rendered as a TOML literal). This is what keeps
// grant_links / env_file / scale / a member's egress / params — anything the
// cart model doesn't carry — from being silently dropped on edit or save.
type KV struct {
	Key      string
	Rendered string
}

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
	Extra   []KV // per-member keys the model doesn't carry (scale, egress, …)
}

// Cart is the whole deployment being assembled.
type Cart struct {
	Name         string
	Version      string // [package] version (default 0.1.0); preserved
	Cards        []Card
	Extra        []KV // top-level/order keys not modeled (grant_links, env_file, …)
	PackageExtra []KV // [package] keys beyond name/version (rare)
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

// renderTOMLValue turns a value decoded into a generic map back into a TOML
// value literal, so a preserved un-modeled key re-emits equivalently. A table
// becomes an INLINE table (`{ k = v }`) — that keeps a member's
// `egress = { mode = "enforce" }` a single line inside its `[[service]]`.
func renderTOMLValue(v any) string {
	switch x := v.(type) {
	case nil:
		return `""`
	case string:
		return tq(x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case time.Time:
		return x.Format(time.RFC3339)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			return "{}"
		}
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+" = "+renderTOMLValue(x[k]))
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	}
	// arrays (of primitives or of tables) via reflect
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		parts := make([]string, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			parts = append(parts, renderTOMLValue(rv.Index(i).Interface()))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return tq(fmt.Sprintf("%v", v)) // last resort — never lose the value
}

// extras renders the keys of a decoded table that the model does NOT carry,
// sorted for determinism. `modeled` is the set of keys already emitted.
func extras(m map[string]any, modeled map[string]bool) []KV {
	keys := make([]string, 0, len(m))
	for k := range m {
		if !modeled[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]KV, 0, len(keys))
	for _, k := range keys {
		out = append(out, KV{Key: k, Rendered: renderTOMLValue(m[k])})
	}
	return out
}

// serviceMaps normalizes the generic `service` value (an array of tables) into
// []map[string]any regardless of how the toml decoder typed it.
func serviceMaps(v any) []map[string]any {
	switch s := v.(type) {
	case []map[string]any:
		return s
	case []any:
		out := make([]map[string]any, 0, len(s))
		for _, e := range s {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

func writeExtras(b *strings.Builder, kvs []KV) {
	for _, kv := range kvs {
		fmt.Fprintf(b, "%s = %s\n", kv.Key, kv.Rendered)
	}
}

// the keys the model already emits — everything else is preserved as an extra.
var (
	memberModeled = set("run", "name", "build", "runtime", "publish", "domain", "env", "after", "volume")
	flatModeled   = set("repo", "app", "image", "docker", "build", "runtime", "version",
		"publish", "domain", "volume", "env", "package", "service")
	packageModeled = set("name", "version")
	compTopModeled = set("package", "service")
)

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
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
	writeExtras(&b, c.Extra) // scale / egress / params / … stay in this block
	return b.String()
}

// composition renders `[package]` + one `[[service]]` per card. This is the
// round-trippable draft form (FromTOML restores it exactly), used for state.
func (c Cart) composition() string {
	name := c.Name
	if name == "" {
		name = "draft"
	}
	ver := c.Version
	if ver == "" {
		ver = "0.1.0"
	}
	var b strings.Builder
	writeExtras(&b, c.Extra) // top-level keys must precede any [table] section
	b.WriteString("[package]\n")
	fmt.Fprintf(&b, "name = %s\n", tq(name))
	fmt.Fprintf(&b, "version = %s\n", tq(ver))
	writeExtras(&b, c.PackageExtra)
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
	case KindDocker:
		fmt.Fprintf(&b, "docker = %s\n", tq(card.Ref))
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
	// preserved top-level keys (grant_links, env_file, scale, …) MUST come
	// before the [env] table, or TOML folds them into it.
	writeExtras(&b, c.Extra)
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
		Name    string `toml:"name"`
		Version string `toml:"version"`
	} `toml:"package"`
	Service []rawMember `toml:"service"`
	// flat single-source order
	Repo    string            `toml:"repo"`
	App     string            `toml:"app"`
	Image   string            `toml:"image"`
	Docker  string            `toml:"docker"`
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
	case strings.HasPrefix(run, "docker://"):
		return KindDocker, run
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
	// A second decode into a generic map lets us read the VALUES of any keys
	// the model doesn't carry, so a round-trip preserves them (per-member
	// extras keep their index here — Undecoded() collapses array-of-tables).
	var generic map[string]any
	_, _ = toml.Decode(spec, &generic)
	// composition
	if len(raw.Service) > 0 {
		c := Cart{Name: raw.Package.Name, Version: raw.Package.Version}
		c.Extra = extras(generic, compTopModeled) // top-level keys (rare here)
		if pkg, ok := generic["package"].(map[string]any); ok {
			c.PackageExtra = extras(pkg, packageModeled)
		}
		svc := serviceMaps(generic["service"])
		for i, m := range raw.Service {
			kind, ref := kindOfRun(m.Run)
			name := m.Name
			if name == "" {
				name = deriveName(ref)
			}
			card := Card{
				Name: name, Kind: kind, Ref: ref,
				Build: m.Build, Runtime: m.Runtime,
				Publish: m.Publish, Domain: m.Domain, Env: m.Env,
				After: m.After, Volume: m.Volume,
			}
			if i < len(svc) {
				card.Extra = extras(svc[i], memberModeled)
			}
			c.Cards = append(c.Cards, card)
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
	case raw.Docker != "":
		card.Kind, card.Ref = KindDocker, raw.Docker
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
	return Cart{
		Name:    raw.Package.Name,
		Version: raw.Package.Version,
		Cards:   []Card{card},
		// grant_links / env_file / token_file / scale / … — preserved so the
		// edit builder and the domain editor never silently drop them.
		Extra: extras(generic, flatModeled),
	}, nil
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
	if r, ok := strings.CutPrefix(ref, "docker://"); ok {
		// docker://ghcr.io/org/postgres:17 → postgres: drop a digest, take the
		// last path segment, then its tag (the tag's `:` would otherwise win).
		if i := strings.Index(r, "@"); i >= 0 {
			r = r[:i]
		}
		if i := strings.LastIndex(r, "/"); i >= 0 {
			r = r[i+1:]
		}
		if i := strings.Index(r, ":"); i >= 0 {
			r = r[:i]
		}
		return SafeName(r)
	}
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

// DeleteDraft removes a draft and its meta sidecar (best-effort).
func DeleteDraft(depDir, id string) {
	_ = os.Remove(draftPath(depDir, id))
	_ = os.Remove(metaPath(depDir, id))
}

// --- draft meta (UI sidecar, NOT part of the deployed TOML) ------------------
// Expected env vars (from a repo's .env.example) are wizard metadata, keyed by
// the source ref (stable across rename/reorder, unlike the card index). They
// live beside the draft as .drafts/<id>.meta.json — the stack parser rejects
// unknown member keys, so they must never enter the deployed file — and retire
// with the draft on promote.

// DraftMeta maps a card's source ref → the env-var keys its .env.example lists.
type DraftMeta map[string][]string

func metaPath(depDir, id string) string {
	return filepath.Join(draftsDir(depDir), SafeName(id)+".meta.json")
}

// ReadMeta loads the expected-env sidecar; a missing file is an empty map.
func ReadMeta(depDir, id string) DraftMeta {
	b, err := os.ReadFile(metaPath(depDir, id))
	if err != nil {
		return DraftMeta{}
	}
	var m DraftMeta
	if json.Unmarshal(b, &m) != nil || m == nil {
		return DraftMeta{}
	}
	return m
}

// WriteMeta saves the expected-env sidecar atomically (temp + rename).
func WriteMeta(depDir, id string, m DraftMeta) error {
	if err := os.MkdirAll(draftsDir(depDir), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := metaPath(depDir, id) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, metaPath(depDir, id))
}

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

package plystate

// Stack files, dashboard-side: parse a [[app]] stack toml into a view the
// deploy page can render as member cards, surface its $VAR holes as
// required inputs, and land the collected values in the env file the stack
// references. Parsing is read-only — the pasted text stays the truth and
// goes to disk verbatim; the form never regenerates TOML.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type StackView struct {
	Name    string // [stack] name, or "" when unnamed
	EnvFile string // [stack] env_file as written, or ""
	Members []StackMember
	Holes   []EnvHole // unique $VARs across member envs, declaration order
}

type StackMember struct {
	Run     string
	Name    string // explicit name, or derived from run like ply does
	E       []string
	Publish []string
	Domain  []string
	After   []string
	Volume  []string
	Scale   int
}

// EnvHole: one $VAR the stack needs at launch; Value carries the current
// content of the referenced env file when it already exists on this host.
type EnvHole struct {
	Key   string
	Value string
}

var holePattern = regexp.MustCompile(`\$([A-Z_][A-Z0-9_]*)`)

// ParseStack reads a stack toml. Returns nil (no error) when the text has
// no [[app]] array — it's not a stack, someone else's problem.
func ParseStack(p Paths, text string) (*StackView, error) {
	// Loose probe first: `app = "name"` is the single-app deployment lane
	// and must fall through as not-a-stack, not die on a type mismatch.
	var probe map[string]any
	if _, err := toml.Decode(text, &probe); err != nil {
		return nil, fmt.Errorf("stack toml: %w", err)
	}
	if _, ok := probe["app"].([]map[string]any); !ok {
		return nil, nil
	}
	var doc struct {
		Stack struct {
			Name    string `toml:"name"`
			EnvFile string `toml:"env_file"`
		} `toml:"stack"`
		App []struct {
			Run     string   `toml:"run"`
			Name    string   `toml:"name"`
			E       []string `toml:"e"`
			Publish []string `toml:"publish"`
			Domain  []string `toml:"domain"`
			After   any      `toml:"after"` // ply accepts a string or a list
			Volume  []string `toml:"volume"`
			Scale   int      `toml:"scale"`
		} `toml:"app"`
	}
	if _, err := toml.Decode(text, &doc); err != nil {
		return nil, fmt.Errorf("stack toml: %w", err)
	}
	if len(doc.App) == 0 {
		return nil, nil
	}
	view := &StackView{Name: doc.Stack.Name, EnvFile: doc.Stack.EnvFile}
	seen := map[string]bool{}
	for _, a := range doc.App {
		m := StackMember{
			Run:     a.Run,
			Name:    a.Name,
			E:       a.E,
			Publish: a.Publish,
			Domain:  a.Domain,
			After:   anyToList(a.After),
			Volume:  a.Volume,
			Scale:   a.Scale,
		}
		if m.Name == "" {
			m.Name = memberName(a.Run)
		}
		view.Members = append(view.Members, m)
		for _, kv := range a.E {
			for _, hit := range holePattern.FindAllStringSubmatch(kv, -1) {
				if !seen[hit[1]] {
					seen[hit[1]] = true
					view.Holes = append(view.Holes, EnvHole{Key: hit[1]})
				}
			}
		}
	}
	view.prefillHoles(p)
	return view, nil
}

func anyToList(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// memberName mirrors ply's default: the image basename up to the first
// dash-digit boundary, or the ref name before any @version.
func memberName(run string) string {
	base := run
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".img")
	if i := strings.Index(base, "@"); i >= 0 {
		base = base[:i]
	}
	for i := 0; i+1 < len(base); i++ {
		if base[i] == '-' && base[i+1] >= '0' && base[i+1] <= '9' {
			return base[:i]
		}
	}
	return base
}

// envFilePath resolves the stack's env_file the way ply does: absolute as
// written, relative against the deployments dir. Empty when the stack
// names none.
func (v *StackView) envFilePath(p Paths) string {
	switch {
	case v.EnvFile == "":
		return ""
	case filepath.IsAbs(v.EnvFile):
		return v.EnvFile
	default:
		return filepath.Join(p.Deployments, v.EnvFile)
	}
}

// prefillHoles fills each hole's Value from the referenced env file when it
// already exists on this host — re-deploying a stack shouldn't demand every
// secret be retyped.
func (v *StackView) prefillHoles(p Paths) {
	path := v.envFilePath(p)
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	values := parseEnvLines(string(raw))
	for i := range v.Holes {
		v.Holes[i].Value = values[v.Holes[i].Key]
	}
}

func parseEnvLines(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, val, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(val)
		}
	}
	return out
}

// DeployStack lands a pasted stack: merge the collected hole values into
// the env file the stack references (creating deployments/.env/<name>.env
// and injecting the reference when the stack names none), then write the
// spec verbatim as a new deployment. Values only ever ADD or UPDATE keys —
// an existing env file keeps keys the form didn't carry.
func DeployStack(p Paths, name, spec string, values map[string]string) error {
	if !deployName.MatchString(name) {
		return fmt.Errorf("bad deployment name")
	}
	view, err := ParseStack(p, spec)
	if err != nil {
		return err
	}
	if view == nil {
		return fmt.Errorf("that spec has no [[app]] blocks — paste it in the from-a-spec form instead")
	}
	if len(values) > 0 {
		if view.EnvFile == "" {
			view.EnvFile = ".env/" + name + ".env"
			spec = injectEnvFile(spec, view.EnvFile)
		}
		path := view.envFilePath(p)
		if err := mergeEnvFile(path, values); err != nil {
			return err
		}
	}
	return CreateRawDeployment(p, name, spec)
}

// injectEnvFile adds `env_file = "…"` to the [stack] section, creating the
// section when the file has none. Text surgery on purpose: the pasted spec
// must reach disk otherwise byte-identical.
func injectEnvFile(spec, ref string) string {
	line := fmt.Sprintf("env_file = %q\n", ref)
	if i := strings.Index(spec, "[stack]"); i >= 0 {
		nl := strings.Index(spec[i:], "\n")
		if nl < 0 {
			return spec + "\n" + line
		}
		at := i + nl + 1
		return spec[:at] + line + spec[at:]
	}
	return "[stack]\n" + line + "\n" + spec
}

func mergeEnvFile(path string, values map[string]string) error {
	existing := map[string]string{}
	if raw, err := os.ReadFile(path); err == nil {
		existing = parseEnvLines(string(raw))
	}
	for k, v := range values {
		existing[k] = v
	}
	keys := make([]string, 0, len(existing))
	for k := range existing {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, existing[k])
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

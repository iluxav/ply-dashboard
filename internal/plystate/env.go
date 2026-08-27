package plystate

// Env files: shared secrets under deployments/.env/ — the .keys pattern
// widened to whole environment files. Specs reference them relatively
// (`env_file = ".env/<name>.env"`); ply resolves the path against the
// deployments dir, so a public fleet repo carries references, never values.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var envName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
var envRef = regexp.MustCompile(`^\.env/[a-z0-9][a-z0-9-]*\.env$`)
var envFileLine = regexp.MustCompile(`(?m)^\s*env_file\s*=\s*"([^"]+)"`)

type EnvFile struct {
	Name    string   // "plybox" → .env/plybox.env
	Refs    []string // deployments whose spec references it
	Missing bool     // referenced by a spec but the file doesn't exist
}

// Ref renders the spec-side reference for this file.
func (e EnvFile) Ref() string { return ".env/" + e.Name + ".env" }

// ExternalEnvRef: a spec pointing at an absolute env_file path — outside
// the dashboard's reach (it only holds the deployments grant), listed so
// the migration path is visible, not editable.
type ExternalEnvRef struct {
	App  string
	Path string
}

func envDir(p Paths) string { return filepath.Join(p.Deployments, ".env") }

func envPath(p Paths, name string) (string, error) {
	if !envName.MatchString(name) {
		return "", fmt.Errorf("env file name must be [a-z0-9-]")
	}
	return filepath.Join(envDir(p), name+".env"), nil
}

// EnvFiles lists managed env files (plus referenced-but-missing ones,
// flagged) and every absolute env_file reference found in the specs.
func EnvFiles(p Paths) ([]EnvFile, []ExternalEnvRef) {
	refs := map[string][]string{}
	var external []ExternalEnvRef
	for _, d := range Deployments(p) {
		m := envFileLine.FindStringSubmatch(d.Spec)
		if m == nil {
			continue
		}
		val := m[1]
		name, ok := strings.CutSuffix(strings.TrimPrefix(val, ".env/"), ".env")
		if strings.HasPrefix(val, ".env/") && ok && envName.MatchString(name) {
			refs[name] = append(refs[name], d.Name)
		} else {
			external = append(external, ExternalEnvRef{App: d.Name, Path: val})
		}
	}
	names := map[string]bool{}
	if entries, err := os.ReadDir(envDir(p)); err == nil {
		for _, e := range entries {
			if n, ok := strings.CutSuffix(e.Name(), ".env"); ok && envName.MatchString(n) {
				names[n] = true
			}
		}
	}
	var out []EnvFile
	for n := range names {
		out = append(out, EnvFile{Name: n, Refs: refs[n]})
	}
	for n, r := range refs {
		if !names[n] {
			out = append(out, EnvFile{Name: n, Refs: r, Missing: true})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	sort.Slice(external, func(a, b int) bool { return external[a].App < external[b].App })
	return out, external
}

func ReadEnvFile(p Paths, name string) (string, error) {
	path, err := envPath(p, name)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil // a new or referenced-but-missing file edits from blank
	}
	return string(raw), err
}

func WriteEnvFile(p Paths, name, content string) error {
	path, err := envPath(p, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(envDir(p), 0o700); err != nil {
		return err
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n" // textarea paste loses the trailing newline
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// DeleteEnvFile refuses while any spec still references the file — retire
// the references first, then the secrets.
func DeleteEnvFile(p Paths, name string) error {
	path, err := envPath(p, name)
	if err != nil {
		return err
	}
	files, _ := EnvFiles(p)
	for _, f := range files {
		if f.Name == name && len(f.Refs) > 0 {
			return fmt.Errorf("%s is referenced by %s — edit those specs first", f.Ref(), strings.Join(f.Refs, ", "))
		}
	}
	return os.Remove(path)
}

// TouchEnvRefs touches every spec referencing the file — touch is the
// approval gesture, so reconcile restarts each app onto the new values.
func TouchEnvRefs(p Paths, name string) ([]string, error) {
	files, _ := EnvFiles(p)
	for _, f := range files {
		if f.Name != name {
			continue
		}
		for _, ref := range f.Refs {
			if err := TouchDeployment(p, ref); err != nil {
				return nil, fmt.Errorf("touch %s: %w", ref, err)
			}
		}
		return f.Refs, nil
	}
	return nil, nil
}

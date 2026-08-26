package plystate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Deployments: a deployment is a file in the granted deployments dir.
// The dashboard writes/removes specs; `ply reconcile` (fired by systemd's
// inotify watch) converges units to them and writes `.status` files back.

type Deployment struct {
	Name   string
	Spec   string // raw TOML, shown verbatim — the file IS the truth
	Status *DeployStatus
}

type DeployStatus struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	TS     int64  `json:"ts"`
}

func (s DeployStatus) String() string {
	if s.OK {
		return "ok: " + s.Detail
	}
	return "failed: " + s.Detail
}

func DeploymentsAvailable(p Paths) bool {
	if p.Deployments == "" || !grantMounted(p.Deployments) {
		return false
	}
	if err := os.MkdirAll(p.Deployments, 0o755); err != nil {
		return false
	}
	probe := filepath.Join(p.Deployments, ".probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

func Deployments(p Paths) []Deployment {
	entries, err := os.ReadDir(p.Deployments)
	if err != nil {
		return nil
	}
	var out []Deployment
	for _, e := range entries {
		name, found := strings.CutSuffix(e.Name(), ".toml")
		if !found || strings.HasPrefix(name, ".") {
			continue
		}
		spec, _ := os.ReadFile(filepath.Join(p.Deployments, e.Name()))
		d := Deployment{Name: name, Spec: string(spec)}
		if raw, err := os.ReadFile(filepath.Join(p.Deployments, name+".status")); err == nil {
			var st DeployStatus
			if json.Unmarshal(raw, &st) == nil {
				d.Status = &st
			}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

var deployName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// WriteDeployment renders and atomically lands a spec file.
// env is KEY=VALUE lines; blank values are dropped (unfilled form rows).
func WriteDeployment(p Paths, name, app, version, publish, domain, envLines string) error {
	if !deployName.MatchString(name) {
		return fmt.Errorf("deployment name must be [a-z0-9-], got %q", name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "app = %q\n", app)
	if version != "" {
		fmt.Fprintf(&b, "version = %q\n", version)
	}
	if publish != "" {
		fmt.Fprintf(&b, "publish = [%q]\n", publish)
	}
	if domain != "" {
		fmt.Fprintf(&b, "domain = [%q]\n", domain)
	}
	env := map[string]string{}
	var keys []string
	for _, line := range strings.Split(envLines, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !found || key == "" || value == "" {
			continue
		}
		if _, dup := env[key]; !dup {
			env[key] = value
			keys = append(keys, key)
		}
	}
	if len(keys) > 0 {
		b.WriteString("\n[env]\n")
		for _, key := range keys {
			fmt.Fprintf(&b, "%s = %q\n", key, env[key])
		}
	}

	tmp := filepath.Join(p.Deployments, "."+name+".toml.tmp")
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(p.Deployments, name+".toml"))
}

func DeleteDeployment(p Paths, name string) error {
	if !deployName.MatchString(name) {
		return fmt.Errorf("bad deployment name")
	}
	return os.Remove(filepath.Join(p.Deployments, name+".toml"))
}

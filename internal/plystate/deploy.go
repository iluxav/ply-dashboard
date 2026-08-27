package plystate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	// Probe inside .status/ — NOT the watched root: a probe file there
	// fires the systemd.path watch, and "viewing the deploy page runs
	// reconcile" is exactly the kind of spooky action this app must not
	// have. Same mount, same writability answer, zero side effects.
	dir := filepath.Join(p.Deployments, ".status")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".probe")
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
		if raw, err := os.ReadFile(filepath.Join(p.Deployments, ".status", name+".status")); err == nil {
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

// OneDeployment reads a single deployment (spec + status) by name.
func OneDeployment(p Paths, name string) (Deployment, bool) {
	if !deployName.MatchString(name) {
		return Deployment{}, false
	}
	raw, err := os.ReadFile(filepath.Join(p.Deployments, name+".toml"))
	if err != nil {
		return Deployment{}, false
	}
	d := Deployment{Name: name, Spec: string(raw)}
	if st, err := os.ReadFile(filepath.Join(p.Deployments, ".status", name+".status")); err == nil {
		var s DeployStatus
		if json.Unmarshal(st, &s) == nil {
			d.Status = &s
		}
	}
	return d, true
}

// RewriteDeployment saves an edited spec verbatim. Validation is minimal
// on purpose — the file is the truth and `ply reconcile` is the real
// validator (its verdict lands in the status line); we only refuse specs
// that could never mean anything.
func RewriteDeployment(p Paths, name, spec string) error {
	if !deployName.MatchString(name) {
		return fmt.Errorf("bad deployment name")
	}
	if _, err := os.Stat(filepath.Join(p.Deployments, name+".toml")); err != nil {
		return fmt.Errorf("deployment %q does not exist", name)
	}
	if strings.TrimSpace(spec) == "" {
		return fmt.Errorf("empty spec — use delete if that's what you mean")
	}
	if !regexp.MustCompile(`(?m)^\s*(app|image|github|repo)\s*=`).MatchString(spec) {
		return fmt.Errorf("spec needs one of app/image/github/repo — nothing to deploy otherwise")
	}
	if !strings.HasSuffix(spec, "\n") {
		spec += "\n"
	}
	tmp := filepath.Join(p.Deployments, "."+name+".toml.tmp")
	if err := os.WriteFile(tmp, []byte(spec), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(p.Deployments, name+".toml"))
}

// renderEnv appends an [env] table from KEY=VALUE lines; blank values and
// comments are dropped (unfilled form rows), first duplicate wins.
func renderEnv(b *strings.Builder, envLines string) {
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
			fmt.Fprintf(b, "%s = %q\n", key, env[key])
		}
	}
}

// WriteDeployment renders and atomically lands a spec file.
// env is KEY=VALUE lines; blank values are dropped (unfilled form rows).
func WriteDeployment(p Paths, name, app, version, publish, domain, envLines string, grantLinks bool) error {
	if !deployName.MatchString(name) {
		return fmt.Errorf("deployment name must be [a-z0-9-], got %q", name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "app = %q\n", app)
	if grantLinks {
		b.WriteString("grant_links = true\n")
	}
	if version != "" {
		fmt.Fprintf(&b, "version = %q\n", version)
	}
	if publish != "" {
		fmt.Fprintf(&b, "publish = [%q]\n", publish)
	}
	if domain != "" {
		fmt.Fprintf(&b, "domain = [%q]\n", domain)
	}
	renderEnv(&b, envLines)

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

// --- freshness plumbing ------------------------------------------------------

// Field pulls one string value out of the raw spec TOML — enough for the
// freshness checker to learn repo/github/ref without a TOML dependency.
func (d Deployment) Field(key string) string {
	pattern := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `\s*=\s*"([^"]*)"`)
	if m := pattern.FindStringSubmatch(d.Spec); m != nil {
		return m[1]
	}
	return ""
}

var (
	deployedCommit  = regexp.MustCompile(`@ ([0-9a-f]{7,40})\b`)
	deployedVersion = regexp.MustCompile(`-([0-9]+\.[0-9]+\.[0-9][^ /-]*)-linux-(?:x64|arm64)\.img`)
)

// DeployedCommit reads the `@ <commit>` the reconcile status reports for
// repo-lane builds; empty when unknown.
func (d Deployment) DeployedCommit() string {
	if d.Status == nil {
		return ""
	}
	if m := deployedCommit.FindStringSubmatch(d.Status.Detail); m != nil {
		return m[1]
	}
	return ""
}

// DeployedVersion reads the image version out of the status detail's
// `<name>-<ver>-linux-<arch>.img`; empty when unknown.
func (d Deployment) DeployedVersion() string {
	if d.Status == nil {
		return ""
	}
	if m := deployedVersion.FindStringSubmatch(d.Status.Detail); m != nil {
		return m[1]
	}
	return ""
}

// TouchDeployment rewrites the spec unchanged (write-temp, rename) so the
// systemd.path watch fires and `ply reconcile` re-resolves the source —
// the deploy-now button is just a touch.
func TouchDeployment(p Paths, name string) error {
	if !deployName.MatchString(name) {
		return fmt.Errorf("bad deployment name")
	}
	path := filepath.Join(p.Deployments, name+".toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	tmp := filepath.Join(p.Deployments, "."+name+".toml.tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- the repo lane (from-source wizard) --------------------------------------

// SourceSpec holds the wizard form as typed: strings in, validation and
// TOML shaping here, so the preview and the written file cannot diverge.
type SourceSpec struct {
	Name       string
	Repo       string
	Ref        string
	Build      string
	Runtime    string
	DeployKey  string // path reference (".keys/<name>"), not the key itself
	TokenFile  string // path reference (".keys/<name>.token") — PAT for private https
	Entrypoint string // whitespace-split into the array form
	Include    string // comma-split
	Port       string
	Publish    string
	Domain     string
	Env        string // KEY=VALUE lines
	Manual     bool   // render auto = false: converge only on touch/deploy-now
}

// Render validates and returns the exact TOML the deployment file will
// hold — the preview shows this string, the write writes it.
func (s SourceSpec) Render() (string, error) {
	if !deployName.MatchString(s.Name) {
		return "", fmt.Errorf("deployment name must be [a-z0-9-], got %q", s.Name)
	}
	repo := strings.TrimSpace(s.Repo)
	if repo == "" {
		return "", fmt.Errorf("repo URL is required")
	}
	if strings.ContainsAny(repo, " \t\"'") {
		return "", fmt.Errorf("repo URL %q contains spaces or quotes", repo)
	}
	var port int
	if v := strings.TrimSpace(s.Port); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("port must be 1-65535, got %q", v)
		}
		port = n
	}
	if strings.TrimSpace(s.Build) == "" && strings.TrimSpace(s.Entrypoint) == "" {
		return "", fmt.Errorf("need a build command, an entrypoint, or both — an empty spec builds nothing")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "repo = %q\n", repo)
	writeOpt := func(key, value string) {
		if v := strings.TrimSpace(value); v != "" {
			fmt.Fprintf(&b, "%s = %q\n", key, v)
		}
	}
	if s.Manual {
		b.WriteString("auto = false\n")
	}
	writeOpt("ref", s.Ref)
	writeOpt("deploy_key", s.DeployKey)
	writeOpt("token_file", s.TokenFile)
	writeOpt("build", s.Build)
	writeOpt("runtime", s.Runtime)
	if fields := strings.Fields(s.Entrypoint); len(fields) > 0 {
		b.WriteString("entrypoint = [")
		for i, f := range fields {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", f)
		}
		b.WriteString("]\n")
	}
	if include := splitList(s.Include); len(include) > 0 {
		b.WriteString("include = [")
		for i, f := range include {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", f)
		}
		b.WriteString("]\n")
	}
	if port > 0 {
		fmt.Fprintf(&b, "port = %d\n", port)
	}
	if v := strings.TrimSpace(s.Publish); v != "" {
		fmt.Fprintf(&b, "publish = [%q]\n", v)
	}
	if v := strings.TrimSpace(s.Domain); v != "" {
		fmt.Fprintf(&b, "domain = [%q]\n", v)
	}
	renderEnv(&b, s.Env)
	return b.String(), nil
}

func splitList(csv string) []string {
	var out []string
	for _, part := range strings.Split(csv, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func WriteSourceDeployment(p Paths, s SourceSpec) error {
	text, err := s.Render()
	if err != nil {
		return err
	}
	tmp := filepath.Join(p.Deployments, "."+s.Name+".toml.tmp")
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(p.Deployments, s.Name+".toml"))
}

// GithubSpec is the CI-image lane as typed: the repo's releases carry the
// .img, the droplet pulls it. Same contract as SourceSpec: strings in,
// validation and TOML here, preview == file.
type GithubSpec struct {
	Name      string
	Repo      string // org/repo
	Asset     string // app name in <asset>-<ver>-linux-<arch>.img; blank = deployment name
	Version   string // exact x.y.z pins; prefix follows; blank follows latest
	TokenFile string // path reference for private repos
	Publish   string
	Domain    string
	Env       string
	Manual    bool // render auto = false: converge only on touch/deploy-now
}

var orgRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (s GithubSpec) Render() (string, error) {
	if !deployName.MatchString(s.Name) {
		return "", fmt.Errorf("deployment name must be [a-z0-9-], got %q", s.Name)
	}
	repo := strings.TrimSpace(s.Repo)
	if !orgRepo.MatchString(repo) {
		return "", fmt.Errorf("github repo must be org/repo, got %q", repo)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "github = %q\n", repo)
	writeOpt := func(key, value string) {
		if v := strings.TrimSpace(value); v != "" {
			fmt.Fprintf(&b, "%s = %q\n", key, v)
		}
	}
	if s.Manual {
		b.WriteString("auto = false\n")
	}
	if strings.TrimSpace(s.Asset) != s.Name {
		writeOpt("asset", s.Asset)
	}
	writeOpt("version", s.Version)
	writeOpt("token_file", s.TokenFile)
	if v := strings.TrimSpace(s.Publish); v != "" {
		fmt.Fprintf(&b, "publish = [%q]\n", v)
	}
	if v := strings.TrimSpace(s.Domain); v != "" {
		fmt.Fprintf(&b, "domain = [%q]\n", v)
	}
	renderEnv(&b, s.Env)
	return b.String(), nil
}

func WriteGithubDeployment(p Paths, s GithubSpec) error {
	text, err := s.Render()
	if err != nil {
		return err
	}
	tmp := filepath.Join(p.Deployments, "."+s.Name+".toml.tmp")
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(p.Deployments, s.Name+".toml"))
}

// WriteToken stores a pasted fine-grained PAT beside the deploy keys and
// returns the relative reference the spec carries. Same trust story as
// keys: root-owned, 0600, resolved by reconcile against the same dir.
func WriteToken(p Paths, name, token string) (string, error) {
	if !deployName.MatchString(name) {
		return "", fmt.Errorf("bad deployment name")
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \n\t") || len(token) < 20 {
		return "", fmt.Errorf("that doesn't look like a GitHub token (one line, 20+ chars)")
	}
	dir := filepath.Join(p.Deployments, ".keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, name+".token"), []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return ".keys/" + name + ".token", nil
}

// WriteDeployKey stores a pasted read-only deploy key under the
// deployments dir (root-owned, 0600) and returns the relative reference
// the spec should carry — `ply reconcile` resolves it against the same
// dir, so neither side has to know the other's mount point.
func WriteDeployKey(p Paths, name, key string) (string, error) {
	if !deployName.MatchString(name) {
		return "", fmt.Errorf("bad deployment name")
	}
	key = strings.TrimSpace(key)
	if !strings.Contains(key, "PRIVATE KEY") {
		return "", fmt.Errorf("that doesn't look like a private key (expected an OpenSSH `PRIVATE KEY` block)")
	}
	dir := filepath.Join(p.Deployments, ".keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// ssh rejects a key file without a trailing newline; textarea paste loses it
	if err := os.WriteFile(filepath.Join(dir, name), []byte(key+"\n"), 0o600); err != nil {
		return "", err
	}
	return ".keys/" + name, nil
}

// PinDeployment upserts one `key = "value"` line in a spec — the rollback
// mechanism: pin a version or ref, and the write itself is the touch that
// makes reconcile converge (and roll) to it. Removing the pin by editing
// the spec resumes follow-latest.
func PinDeployment(p Paths, name, key, value string) error {
	if key != "version" && key != "ref" {
		return fmt.Errorf("can only pin version or ref")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(value) {
		return fmt.Errorf("bad pin value %q", value)
	}
	d, ok := OneDeployment(p, name)
	if !ok {
		return fmt.Errorf("deployment %q does not exist", name)
	}
	line := fmt.Sprintf("%s = %q", key, value)
	pattern := regexp.MustCompile(`(?m)^\s*` + key + `\s*=.*$`)
	var spec string
	if pattern.MatchString(d.Spec) {
		spec = pattern.ReplaceAllString(d.Spec, line)
	} else {
		// after the source line, so the file reads top-down sensibly
		src := regexp.MustCompile(`(?m)^\s*(app|image|github|repo)\s*=.*$`)
		loc := src.FindStringIndex(d.Spec)
		if loc == nil {
			return fmt.Errorf("spec has no source line")
		}
		spec = d.Spec[:loc[1]] + "\n" + line + d.Spec[loc[1]:]
	}
	return RewriteDeployment(p, name, spec)
}

// History: this deployment's past deploys, newest first, deduplicated by
// rollback target — the menu the rollback buttons render from.
func History(p Paths, name string, limit int) []Event {
	var out []Event
	seen := map[string]bool{}
	for _, e := range Events(p, name, 200) {
		kind, value := e.RollbackTarget()
		if kind == "" || seen[kind+value] {
			continue
		}
		seen[kind+value] = true
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// Package github inspects a repository before the "from source" wizard
// writes a deployment: does it exist, is it private, what does it look
// like? Public repos need no token — the public API and raw file probes
// are enough to pick a framework preset with confidence.
package github

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var client = &http.Client{Timeout: 8 * time.Second}

type Inspection struct {
	Repo          string // "org/repo"; empty when the URL isn't GitHub
	CloneURL      string // what the spec's repo field should carry
	SSHURL        string // the deploy-key form of the same repo
	Found         bool   // the API confirmed it exists
	Private       bool   // API says private, or 404 (private and invisible look identical)
	DefaultBranch string
	Markers       []string // what the probe saw, for the "detected" line
	Framework     string   // ply | nextjs | node | go | rust | unknown
	Note          string   // one human sentence about what that means
	Release       *Release // latest release carrying a .img for this arch, if any
	FleetHosts    []string // hosts/<name>/ dirs — the repo is a fleet, not an app
}

// Release: the CI-image lane's offer — the latest release ships a ply
// image for this host's arch, so pulling beats building.
type Release struct {
	Version string
	Asset   string // app name parsed from <app>-<ver>-linux-<arch>.img
}

var repoPattern = regexp.MustCompile(
	`^(?:https?://github\.com/|git@github\.com:|github\.com/)([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

// ParseRepo extracts "org/repo" from the URL shapes people actually paste;
// empty string means not-GitHub (still deployable, just not inspectable).
func ParseRepo(raw string) string {
	raw = strings.TrimSpace(raw)
	if m := repoPattern.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	// bare "org/repo"
	if m := regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).FindStringSubmatch(raw); m != nil {
		return m[0]
	}
	return ""
}

// Inspect learns what a repo is. token (a fine-grained PAT, optional)
// unlocks private repos: with it, private gets the same full inspection
// as public — meta, file probes, releases.
func Inspect(rawURL, token string) (Inspection, error) {
	rawURL = strings.TrimSpace(rawURL)
	repo := ParseRepo(rawURL)
	if repo == "" {
		return Inspection{
			CloneURL:  rawURL,
			Framework: "unknown",
			Note:      "not a GitHub URL — no inspection possible; pick a preset and fill the form yourself",
		}, nil
	}
	insp := Inspection{
		Repo:     repo,
		CloneURL: "https://github.com/" + repo,
		SSHURL:   "git@github.com:" + repo + ".git",
	}

	status, body, err := getAuth("https://api.github.com/repos/"+repo, token)
	switch {
	case err != nil:
		return insp, err
	case status == http.StatusNotFound:
		insp.Private = true
		insp.Framework = "unknown"
		if token == "" {
			insp.Note = "GitHub says 404 — a private repo looks exactly like a missing one; if it's yours, paste a fine-grained token (Contents: read) to inspect and deploy it"
		} else {
			insp.Note = "still 404 with that token — wrong repo, or the token lacks access to it"
		}
		return insp, nil
	case status != http.StatusOK:
		return insp, fmt.Errorf("GitHub API answered %d for %s", status, repo)
	}
	var meta struct {
		Private       bool   `json:"private"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return insp, err
	}
	insp.Found = true
	insp.Private = meta.Private
	insp.DefaultBranch = meta.DefaultBranch
	if insp.Private && token == "" {
		insp.Framework = "unknown"
		insp.Note = "private repo — paste a fine-grained token (Contents: read) to inspect and deploy it"
		return insp, nil
	}

	insp.probe(token)
	insp.Release = latestImageRelease(repo, token)
	insp.FleetHosts = fleetHosts(repo, token)
	if len(insp.FleetHosts) > 0 {
		insp.Note = "this is a FLEET repo — enroll this host and its apps sync from git every reconcile beat"
	}
	return insp, nil
}

// fleetHosts: a repo with hosts/<name>/ dirs is an infra repo — offer
// enrollment instead of an app deployment.
func fleetHosts(repo, token string) []string {
	status, body, err := getAuth("https://api.github.com/repos/"+repo+"/contents/hosts", token)
	if err != nil || status != http.StatusOK {
		return nil
	}
	var entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if json.Unmarshal(body, &entries) != nil {
		return nil
	}
	var hosts []string
	for _, e := range entries {
		if e.Type == "dir" {
			hosts = append(hosts, e.Name)
		}
	}
	return hosts
}

// probe sniffs well-known files and decides the framework. Public repos
// read raw.githubusercontent.com; a token switches to the API's raw view
// so private repos probe identically.
func (i *Inspection) probe(token string) {
	raw := func(path string) (int, []byte) {
		url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", i.Repo, i.DefaultBranch, path)
		if token != "" {
			url = fmt.Sprintf("https://api.github.com/repos/%s/contents/%s?ref=%s", i.Repo, path, i.DefaultBranch)
		}
		status, body, err := getRaw(url, token)
		if err != nil {
			return 0, nil
		}
		return status, body
	}
	hasPly := false
	if status, _ := raw("ply.toml"); status == http.StatusOK {
		hasPly = true
		i.Markers = append(i.Markers, "ply.toml")
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	hasPkg, hasNext := false, false
	if status, body := raw("package.json"); status == http.StatusOK {
		hasPkg = true
		i.Markers = append(i.Markers, "package.json")
		if json.Unmarshal(body, &pkg) == nil {
			_, dep := pkg.Dependencies["next"]
			_, dev := pkg.DevDependencies["next"]
			if hasNext = dep || dev; hasNext {
				i.Markers = append(i.Markers, "next dependency")
			}
		}
	}
	hasGo, hasRust := false, false
	if status, _ := raw("go.mod"); status == http.StatusOK {
		hasGo = true
		i.Markers = append(i.Markers, "go.mod")
	}
	if status, _ := raw("Cargo.toml"); status == http.StatusOK {
		hasRust = true
		i.Markers = append(i.Markers, "Cargo.toml")
	}

	switch {
	case hasPly:
		i.Framework = "ply"
		i.Note = "the repo carries its own ply.toml — entrypoint/include come from it; add a build command only if artifacts must be compiled first"
	case hasNext:
		i.Framework = "nextjs"
		i.Note = `Next.js — the preset needs output: "standalone" in next.config, and keeps the static-assets copy step (without it /_next/static 404s)`
	case hasPkg:
		i.Framework = "node"
		i.Note = "Node — the preset assumes `npm run build` emits dist/; adjust entrypoint/include to the repo's layout"
	case hasGo:
		i.Framework = "go"
		i.Note = "Go — no go toolchain keg yet, so a droplet build will fail; the CI lane (GitHub release assets) is the steer for now"
	case hasRust:
		i.Framework = "rust"
		i.Note = "Rust — droplet builds are too heavy for small hosts and no rust keg ships yet; use the CI lane (GitHub release assets)"
	default:
		i.Framework = "unknown"
		i.Note = "no framework markers found — pick a preset or fill the form yourself"
	}
}

// Preset is the known-good prefill per framework — validated commands from
// live droplet runs, not guesses.
type Preset struct {
	Build, Runtime, Entrypoint, Include, Port string
}

func PresetFor(framework string) Preset {
	switch framework {
	case "nextjs":
		return Preset{
			Build:      `npm ci && npm run build && rm -rf .next/standalone/.next/static .next/standalone/public && cp -r .next/static .next/standalone/.next/static && cp -r public .next/standalone/public`,
			Runtime:    "node@24",
			Entrypoint: "node .next/standalone/server.js",
			Include:    ".next/standalone/",
			Port:       "3000",
		}
	case "node":
		return Preset{
			Build:      "npm ci && npm run build",
			Runtime:    "node@24",
			Entrypoint: "node dist/index.js",
			Include:    "dist/, node_modules/, package.json",
			Port:       "3000",
		}
	case "go":
		return Preset{
			Build:      "go build -o server .",
			Entrypoint: "./server",
			Include:    "server",
			Port:       "8080",
		}
	case "rust":
		return Preset{
			Build:      "cargo build --release",
			Entrypoint: "target/release/server",
			Include:    "target/release/server",
			Port:       "8080",
		}
	default:
		return Preset{}
	}
}

// Frameworks lists the preset choices in form order.
func Frameworks() []string { return []string{"nextjs", "node", "go", "rust", "ply", "unknown"} }

// --- freshness (no API, no rate limits) --------------------------------------

var noRedirect = &http.Client{
	Timeout:       8 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// LatestRelease resolves the newest release version. Tokenless, the
// `releases/latest/download/…` redirect names the tag in its Location —
// one HEAD request, no API, no rate limit. With a token (private repos),
// the API answers instead.
func LatestRelease(repo, token string) (string, error) {
	if token != "" {
		status, body, err := getAuth("https://api.github.com/repos/"+repo+"/releases/latest", token)
		if err != nil {
			return "", err
		}
		if status != http.StatusOK {
			return "", fmt.Errorf("%s: releases API answered %d", repo, status)
		}
		var rel struct {
			TagName string `json:"tag_name"`
		}
		if err := json.Unmarshal(body, &rel); err != nil || rel.TagName == "" {
			return "", fmt.Errorf("%s: no tag in release", repo)
		}
		return strings.TrimPrefix(rel.TagName, "v"), nil
	}
	url := "https://github.com/" + repo + "/releases/latest/download/probe"
	req, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ply-dashboard")
	resp, err := noRedirect.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("%s: no releases (or private)", repo)
	}
	return VersionFromLocation(loc)
}

// latestImageRelease asks the API whether the newest release carries a ply
// image for this host's arch — the signal that the CI lane is available.
// Best-effort: nil on any miss (no releases, rate limit, wrong arch).
func latestImageRelease(repo, token string) *Release {
	status, body, err := getAuth("https://api.github.com/repos/"+repo+"/releases/latest", token)
	if err != nil || status != http.StatusOK {
		return nil
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if json.Unmarshal(body, &rel) != nil || rel.TagName == "" {
		return nil
	}
	version := strings.TrimPrefix(rel.TagName, "v")
	suffix := fmt.Sprintf("-%s-linux-%s.img", version, hostArch())
	for _, a := range rel.Assets {
		if app, found := strings.CutSuffix(a.Name, suffix); found && app != "" {
			return &Release{Version: version, Asset: app}
		}
	}
	return nil
}

func hostArch() string {
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "x64"
}

// VersionFromLocation: `…/releases/download/v0.1.3/probe` → `0.1.3`.
func VersionFromLocation(loc string) (string, error) {
	const marker = "/releases/download/"
	idx := strings.Index(loc, marker)
	if idx < 0 {
		return "", fmt.Errorf("no version in redirect %q", loc)
	}
	tag := strings.SplitN(loc[idx+len(marker):], "/", 2)[0]
	tag = strings.TrimPrefix(tag, "v")
	if tag == "" {
		return "", fmt.Errorf("no version in redirect %q", loc)
	}
	return tag, nil
}

// LsRemote reads the tip commit of a branch (or the default HEAD) over
// git's smart-HTTP advertisement — what `git ls-remote` does, minus git.
// Works for any public https remote, GitHub or not; a token (basic auth,
// the PAT-as-password form git itself uses) unlocks private ones.
func LsRemote(cloneURL, ref, token string) (string, error) {
	base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(cloneURL), "/"), ".git")
	status, body, err := getBasic(base+".git/info/refs?service=git-upload-pack", token)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("%s: refs advertisement answered %d (private?)", base, status)
	}
	refs := parseAdvertisement(body)
	if ref == "" {
		if sha, ok := refs["HEAD"]; ok {
			return sha, nil
		}
		return "", fmt.Errorf("%s: no HEAD advertised", base)
	}
	for _, name := range []string{"refs/heads/" + ref, "refs/tags/" + ref} {
		if sha, ok := refs[name]; ok {
			return sha, nil
		}
	}
	return "", fmt.Errorf("%s: ref %q not found", base, ref)
}

// parseAdvertisement decodes pkt-lines: 4 hex length chars, payload
// `<sha> <refname>[\0capabilities]`, `0000` flush markers, `#` comments.
func parseAdvertisement(b []byte) map[string]string {
	out := map[string]string{}
	for len(b) >= 4 {
		n, err := strconv.ParseUint(string(b[:4]), 16, 32)
		if err != nil {
			break
		}
		if n == 0 {
			b = b[4:]
			continue
		}
		if int(n) > len(b) || n < 4 {
			break
		}
		line := strings.TrimSuffix(string(b[4:n]), "\n")
		b = b[n:]
		if strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.IndexByte(line, 0); i >= 0 {
			line = line[:i]
		}
		sha, name, ok := strings.Cut(line, " ")
		if ok && len(sha) == 40 {
			out[name] = sha
		}
	}
	return out
}

func get(url string) (int, []byte, error) { return request(url, nil) }

// getAuth: Bearer-authenticated API request (no-op without a token).
func getAuth(url, token string) (int, []byte, error) {
	h := map[string]string{}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	return request(url, h)
}

// getRaw: file content — raw.githubusercontent tokenless, the contents
// API's raw view with a token.
func getRaw(url, token string) (int, []byte, error) {
	h := map[string]string{}
	if token != "" {
		h["Authorization"] = "Bearer " + token
		h["Accept"] = "application/vnd.github.raw+json"
	}
	return request(url, h)
}

// getBasic: git smart-HTTP auth — basic with the PAT as password, exactly
// what `git clone https://…` sends.
func getBasic(url, token string) (int, []byte, error) {
	h := map[string]string{}
	if token != "" {
		h["Authorization"] = "Basic " +
			base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
	}
	return request(url, h)
}

func request(url string, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", "ply-dashboard")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for len(body) < 256*1024 {
		n, err := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	return resp.StatusCode, body, nil
}

// Package github inspects a repository before the "from source" wizard
// writes a deployment: does it exist, is it private, what does it look
// like? Public repos need no token — the public API and raw file probes
// are enough to pick a framework preset with confidence.
package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var client = &http.Client{Timeout: 8 * time.Second}

type Inspection struct {
	Repo          string // "org/repo"; empty when the URL isn't GitHub
	CloneURL      string // what the spec's repo field should carry
	SSHURL        string // the deploy-key form of the same repo
	Found         bool   // the public API confirmed it exists
	Private       bool   // API says private, or 404 (private and invisible look identical)
	DefaultBranch string
	Markers       []string // what the probe saw, for the "detected" line
	Framework     string   // ply | nextjs | node | go | rust | unknown
	Note          string   // one human sentence about what that means
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

func Inspect(rawURL string) (Inspection, error) {
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

	status, body, err := get("https://api.github.com/repos/" + repo)
	switch {
	case err != nil:
		return insp, err
	case status == http.StatusNotFound:
		insp.Private = true
		insp.Framework = "unknown"
		insp.Note = "GitHub says 404 — a private repo looks exactly like a missing one; if it's yours, paste a read-only deploy key below and pick a preset"
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
	if insp.Private {
		insp.Framework = "unknown"
		insp.Note = "private repo — contents not inspectable without credentials; paste a read-only deploy key and pick a preset"
		return insp, nil
	}

	insp.probe()
	return insp, nil
}

// probe sniffs well-known files off raw.githubusercontent.com and decides
// the framework. Detection is a suggestion the form lets you override.
func (i *Inspection) probe() {
	raw := func(path string) (int, []byte) {
		status, body, err := get(fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", i.Repo, i.DefaultBranch, path))
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

// LatestRelease resolves the newest release version the way ply does: the
// `releases/latest/download/…` redirect names the tag in its Location —
// one HEAD request, no API, no rate limit.
func LatestRelease(repo string) (string, error) {
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
// Works for any public https remote, GitHub or not.
func LsRemote(cloneURL, ref string) (string, error) {
	base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(cloneURL), "/"), ".git")
	status, body, err := get(base + ".git/info/refs?service=git-upload-pack")
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

func get(url string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", "ply-dashboard")
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

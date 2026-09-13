package github

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestParseRepo(t *testing.T) {
	cases := map[string]string{
		"https://github.com/iluxav/next-dummy":     "iluxav/next-dummy",
		"https://github.com/iluxav/next-dummy.git": "iluxav/next-dummy",
		"http://github.com/a/b/":                   "a/b",
		"git@github.com:iluxav/next-dummy.git":     "iluxav/next-dummy",
		"github.com/org/repo":                      "org/repo",
		"org/repo":                                 "org/repo",
		" https://github.com/a/b ":                 "a/b",
		"https://gitlab.com/a/b":                   "",
		"not a url":                                "",
		"https://github.com/onlyorg":               "",
	}
	for in, want := range cases {
		if got := ParseRepo(in); got != want {
			t.Errorf("ParseRepo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPresetsCoverEveryFramework(t *testing.T) {
	for _, fw := range Frameworks() {
		p := PresetFor(fw)
		switch fw {
		case "ply", "unknown":
			if p.Build != "" {
				t.Errorf("%s preset should be empty", fw)
			}
		case "nextjs":
			// The host auto-detects a no-ply.toml Next.js repo: it fills
			// entrypoint/include/port on its own. The preset only prefills the
			// Build command — no entrypoint to prompt for.
			if p.Build == "" {
				t.Errorf("nextjs preset must prefill a build command: %+v", p)
			}
			if p.Entrypoint != "" {
				t.Errorf("nextjs entrypoint is host-auto-detected, must stay empty: %+v", p)
			}
		default:
			if p.Build == "" || p.Entrypoint == "" {
				t.Errorf("%s preset incomplete: %+v", fw, p)
			}
		}
	}
	nb := PresetFor("nextjs").Build
	if !strings.Contains(nb, "cp -r .next/static") {
		t.Error("nextjs preset lost the mandatory static-assets copy step")
	}
	// The npm-ci trap: `npm ci` needs a committed lockfile; a repo without one
	// (a fresh `create-next-app`) fails. The host uses `npm install`; the
	// dashboard must prefill the same, never nudge toward `npm ci`.
	if !strings.Contains(nb, "npm install") || strings.Contains(nb, "npm ci") {
		t.Errorf("nextjs build must use `npm install`, not `npm ci`: %q", nb)
	}
	if nb != NextjsBuild {
		t.Errorf("nextjs preset build diverged from the host's shared const:\n got %q\nwant %q", nb, NextjsBuild)
	}
}

func TestFirstPort(t *testing.T) {
	cases := map[string]string{
		// inline table — the grouped [run] / flat authoring form
		"[run]\nentrypoint = [\"node\", \"server.js\"]\nports = { web = 3000 }\n": "3000",
		"ports = { web = 8000, admin = 9000 }":                                    "8000",
		// [ports] section table
		"[package]\nname = \"x\"\n\n[ports]\nweb = 8080\n": "8080",
		// a composition/no-port manifest yields nothing
		"[package]\nname = \"x\"\n": "",
		// a later section's number must not leak in as a port
		"[ports]\nweb = 5000\n\n[health]\nport = 9999\n": "5000",
	}
	for body, want := range cases {
		if got := firstPort(body); got != want {
			t.Errorf("firstPort(%q) = %q, want %q", body, got, want)
		}
	}
}

// / Network test — run explicitly: go test ./internal/github -run Live -live
func TestLiveInspectPublicRepo(t *testing.T) {
	if os.Getenv("PLY_LIVE_TESTS") == "" {
		t.Skip("network — set PLY_LIVE_TESTS=1 to run (needs GitHub API access)")
	}
	insp, err := Inspect("https://github.com/iluxav/next-dummy", "")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !insp.Found || insp.Private {
		t.Fatalf("next-dummy should be public: %+v", insp)
	}
	if insp.Framework != "nextjs" {
		t.Errorf("want nextjs, got %q (markers %v)", insp.Framework, insp.Markers)
	}
	if insp.DefaultBranch == "" {
		t.Error("default branch missing")
	}
}

func TestVersionFromLocation(t *testing.T) {
	v, err := VersionFromLocation("https://github.com/iluxav/ply-dashboard/releases/download/v0.1.3/probe")
	if err != nil || v != "0.1.3" {
		t.Errorf("got %q, %v", v, err)
	}
	if _, err := VersionFromLocation("https://github.com/x/y"); err == nil {
		t.Error("should fail without a download path")
	}
}

func TestParseAdvertisement(t *testing.T) {
	head := "9051086fee0b8b041a42e5658b60a0eff1f2c04a HEAD\x00multi_ack symref=HEAD:refs/heads/main\n"
	main := "9051086fee0b8b041a42e5658b60a0eff1f2c04a refs/heads/main\n"
	tag := "aaaa086fee0b8b041a42e5658b60a0eff1f2c04a refs/tags/v1.0\n"
	body := fmt.Sprintf("001e# service=git-upload-pack\n0000%04x%s%04x%s%04x%s0000",
		len(head)+4, head, len(main)+4, main, len(tag)+4, tag)
	refs := parseAdvertisement([]byte(body))
	if refs["HEAD"] != "9051086fee0b8b041a42e5658b60a0eff1f2c04a" {
		t.Errorf("HEAD = %q", refs["HEAD"])
	}
	if refs["refs/heads/main"] == "" || refs["refs/tags/v1.0"] == "" {
		t.Errorf("refs missing: %v", refs)
	}
}

// / Network test: real refs advertisement against next-dummy.
func TestLiveLsRemote(t *testing.T) {
	if os.Getenv("PLY_LIVE_TESTS") == "" {
		t.Skip("network — set PLY_LIVE_TESTS=1 to run (needs GitHub API access)")
	}
	sha, err := LsRemote("https://github.com/iluxav/next-dummy", "", "")
	if err != nil || len(sha) != 40 {
		t.Fatalf("sha %q err %v", sha, err)
	}
	byBranch, err := LsRemote("https://github.com/iluxav/next-dummy", "main", "")
	if err != nil || byBranch != sha {
		t.Errorf("main %q vs HEAD %q (err %v)", byBranch, sha, err)
	}
}

// / Network test: the dashboard repo's latest release carries a ply image.
func TestLiveImageRelease(t *testing.T) {
	if os.Getenv("PLY_LIVE_TESTS") == "" {
		t.Skip("network — set PLY_LIVE_TESTS=1 to run (needs GitHub API access)")
	}
	rel := latestImageRelease("iluxav/ply-dashboard", "")
	if rel == nil {
		t.Fatal("ply-dashboard releases should carry images")
	}
	if rel.Asset != "dashboard" {
		t.Errorf("asset app = %q, want dashboard", rel.Asset)
	}
	if rel.Version == "" {
		t.Error("version empty")
	}
}

func TestParseEnvKeys(t *testing.T) {
	// comments, blanks, `export `, an empty value, a spaced key, a no-`=` junk
	// line, and a duplicate — order-preserving, deduped.
	body := "# a comment\n\nexport FOO=1\nBAR=\n  BAZ = qux \nnot a key line\nFOO=2\n#DB=x\nEMPTY_OK=\n"
	got := parseEnvKeys(body)
	want := []string{"FOO", "BAR", "BAZ", "EMPTY_OK"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("parseEnvKeys = %v, want %v", got, want)
	}
	if len(parseEnvKeys("")) != 0 {
		t.Fatal("empty body should yield no keys")
	}
}

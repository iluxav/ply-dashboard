package github

import (
	"fmt"
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
		default:
			if p.Build == "" || p.Entrypoint == "" {
				t.Errorf("%s preset incomplete: %+v", fw, p)
			}
		}
	}
	if !strings.Contains(PresetFor("nextjs").Build, "cp -r .next/static") {
		t.Error("nextjs preset lost the mandatory static-assets copy step")
	}
}

// / Network test — run explicitly: go test ./internal/github -run Live -live
func TestLiveInspectPublicRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("network")
	}
	insp, err := Inspect("https://github.com/iluxav/next-dummy")
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
	if testing.Short() {
		t.Skip("network")
	}
	sha, err := LsRemote("https://github.com/iluxav/next-dummy", "")
	if err != nil || len(sha) != 40 {
		t.Fatalf("sha %q err %v", sha, err)
	}
	byBranch, err := LsRemote("https://github.com/iluxav/next-dummy", "main")
	if err != nil || byBranch != sha {
		t.Errorf("main %q vs HEAD %q (err %v)", byBranch, sha, err)
	}
}

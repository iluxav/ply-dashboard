package plystate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iluxav/ply-dashboard/internal/cart"
)

func writeDep(t *testing.T, p Paths, name, spec string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(p.Deployments, name+".toml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cardDomains(t *testing.T, p Paths, dep, member string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(p.Deployments, dep+".toml"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := cart.FromTOML(string(raw))
	if err != nil {
		t.Fatalf("re-parse %s: %v", dep, err)
	}
	for _, card := range c.Cards {
		if card.Name == member || len(c.Cards) == 1 {
			return card.Domain
		}
	}
	return nil
}

func TestAddDomainSingleApp(t *testing.T) {
	p := Paths{Deployments: t.TempDir()}
	writeDep(t, p, "redis", "app = \"redis\"\npublish = [\"internal:6379\"]\n")
	if err := AddDomain(p, "redis", "Cache.Example.com"); err != nil {
		t.Fatal(err)
	}
	got := cardDomains(t, p, "redis", "redis")
	if len(got) != 1 || got[0] != "cache.example.com" { // lowercased
		t.Fatalf("domain = %v, want [cache.example.com]", got)
	}
	// idempotent
	if err := AddDomain(p, "redis", "cache.example.com"); err != nil {
		t.Fatal(err)
	}
	if got := cardDomains(t, p, "redis", "redis"); len(got) != 1 {
		t.Fatalf("duplicate domain added: %v", got)
	}
}

func TestAddDomainStackMemberOnly(t *testing.T) {
	p := Paths{Deployments: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(p.Deployments, ".status"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(p.Deployments, ".status", "shop.members"), []byte("web\ndb\n"), 0o644)
	writeDep(t, p, "shop", `[package]
name = "shop"
version = "0.1.0"

[[service]]
run = "git+https://github.com/you/web"
name = "web"
publish = ["8080:3000"]

[[service]]
run = "postgres@17"
name = "db"
publish = ["internal:5432"]
`)
	if err := AddDomain(p, "web", "shop.example.com"); err != nil {
		t.Fatal(err)
	}
	if got := cardDomains(t, p, "shop", "web"); len(got) != 1 || got[0] != "shop.example.com" {
		t.Fatalf("web domain = %v, want [shop.example.com]", got)
	}
	if got := cardDomains(t, p, "shop", "db"); len(got) != 0 {
		t.Fatalf("db should have no domain, got %v", got)
	}
	// remove it again
	if err := RemoveDomain(p, "web", "shop.example.com"); err != nil {
		t.Fatal(err)
	}
	if got := cardDomains(t, p, "shop", "web"); len(got) != 0 {
		t.Fatalf("domain not removed: %v", got)
	}
}

func TestBadDomainRejected(t *testing.T) {
	p := Paths{Deployments: t.TempDir()}
	writeDep(t, p, "redis", "app = \"redis\"\n")
	for _, bad := range []string{"http://x.com", "nodot", "has space.com", "a.com/path", ""} {
		if err := AddDomain(p, "redis", bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

func TestDeploymentOf(t *testing.T) {
	p := Paths{Deployments: t.TempDir()}
	os.MkdirAll(filepath.Join(p.Deployments, ".status"), 0o755)
	os.WriteFile(filepath.Join(p.Deployments, ".status", "shop.members"), []byte("web\ndb\n"), 0o644)
	writeDep(t, p, "shop", "[package]\nname=\"shop\"\n[[service]]\nrun=\"postgres@17\"\nname=\"db\"\n")
	writeDep(t, p, "solo", "app = \"solo\"\n")
	if dep, ok := DeploymentOf(p, "db"); !ok || dep != "shop" {
		t.Fatalf("DeploymentOf(db) = %q,%v want shop", dep, ok)
	}
	if dep, ok := DeploymentOf(p, "solo"); !ok || dep != "solo" {
		t.Fatalf("DeploymentOf(solo) = %q,%v want solo", dep, ok)
	}
	if _, ok := DeploymentOf(p, "ghost"); ok {
		t.Fatal("DeploymentOf(ghost) should be false")
	}
}

func TestAddDomainPreservesGrantLinks(t *testing.T) {
	p := Paths{Deployments: t.TempDir()}
	// the dashboard's-own shape: previously REFUSED; now must succeed + keep it.
	writeDep(t, p, "dashboard",
		"app = \"dashboard\"\ngrant_links = true\nenv_file = \".env/x.env\"\npublish = [\"internal:7070\"]\n")
	if err := AddDomain(p, "dashboard", "dash.example.com"); err != nil {
		t.Fatalf("AddDomain refused/failed: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(p.Deployments, "dashboard.toml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "dash.example.com") {
		t.Fatalf("domain not added:\n%s", got)
	}
	if !strings.Contains(got, "grant_links = true") {
		t.Fatalf("grant_links dropped by the domain edit:\n%s", got)
	}
	if !strings.Contains(got, "env_file") {
		t.Fatalf("env_file dropped by the domain edit:\n%s", got)
	}
}

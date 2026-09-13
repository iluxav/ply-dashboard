package cart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOneRepoCardIsAFlatOrder(t *testing.T) {
	c := Cart{Name: "site", Cards: []Card{{
		Name: "site", Kind: KindRepo, Ref: "https://github.com/you/site",
		Build: "npm install", Publish: []string{"8080:3000"}, Domain: []string{"site.example.com"},
		Env: []string{"NODE_ENV=production"},
	}}}
	got := c.ToTOML()
	for _, want := range []string{
		`repo = "https://github.com/you/site"`,
		`build = "npm install"`,
		`publish = ["8080:3000"]`,
		`domain = ["site.example.com"]`,
		"[env]",
		`NODE_ENV = "production"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("flat order missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[[service]]") {
		t.Fatalf("one card must NOT be a composition:\n%s", got)
	}
}

func TestOneRegistryCardIsAppPlusVersion(t *testing.T) {
	c := Cart{Cards: []Card{{Name: "db", Kind: KindRegistry, Ref: "postgres@17", Publish: []string{"internal:5432"}}}}
	got := c.ToTOML()
	if !strings.Contains(got, `app = "postgres"`) || !strings.Contains(got, `version = "17"`) {
		t.Fatalf("registry flat order wrong:\n%s", got)
	}
}

func TestThreeCardsAreAComposition(t *testing.T) {
	c := Cart{Name: "shop", Cards: []Card{
		{Name: "db", Kind: KindRegistry, Ref: "postgres@17", Publish: []string{"internal:5432"},
			Volume: []string{"pgdata:/var/lib/postgresql/data"}, Env: []string{"POSTGRES_PASSWORD=secret"}},
		{Name: "server", Kind: KindRepo, Ref: "https://github.com/you/server", Build: "npm install",
			After: []string{"db"}, Publish: []string{"internal:3001"}, Env: []string{"POSTGRES_PASSWORD=secret"}},
		{Name: "web", Kind: KindRepo, Ref: "https://github.com/you/web", After: []string{"server"}, Publish: []string{"8080:3000"}},
	}}
	got := c.ToTOML()
	for _, want := range []string{
		"[package]", `name = "shop"`,
		`run = "postgres@17"`, `name = "db"`, `volume = ["pgdata:/var/lib/postgresql/data"]`,
		`run = "git+https://github.com/you/server"`, `build = "npm install"`, `after = ["db"]`,
		`env = ["POSTGRES_PASSWORD=secret"]`, // member env is the array form
		`run = "git+https://github.com/you/web"`, `after = ["server"]`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("composition missing %q in:\n%s", want, got)
		}
	}
	if strings.Count(got, "[[service]]") != 3 {
		t.Fatalf("want 3 members, got:\n%s", got)
	}
}

func TestCompositionRoundTrips(t *testing.T) {
	orig := Cart{Name: "shop", Cards: []Card{
		{Name: "db", Kind: KindRegistry, Ref: "postgres@17", Publish: []string{"internal:5432"}, Env: []string{"POSTGRES_PASSWORD=x"}},
		{Name: "server", Kind: KindRepo, Ref: "https://github.com/you/server", Build: "npm install", After: []string{"db"}, Publish: []string{"internal:3001"}},
		{Name: "web", Kind: KindRepo, Ref: "https://github.com/you/web", After: []string{"server"}, Publish: []string{"8080:3000"}},
	}}
	back, err := FromTOML(orig.DraftTOML())
	if err != nil {
		t.Fatal(err)
	}
	if back.Name != "shop" || len(back.Cards) != 3 {
		t.Fatalf("round-trip lost shape: %+v", back)
	}
	if back.Cards[1].Kind != KindRepo || back.Cards[1].Ref != "https://github.com/you/server" || back.Cards[1].Build != "npm install" {
		t.Fatalf("repo member not restored: %+v", back.Cards[1])
	}
	if back.Cards[0].Kind != KindRegistry || back.Cards[0].Ref != "postgres@17" {
		t.Fatalf("registry member not restored: %+v", back.Cards[0])
	}
	if len(back.Cards[1].After) != 1 || back.Cards[1].After[0] != "db" {
		t.Fatalf("after not restored: %+v", back.Cards[1].After)
	}
}

func TestFlatOrderRoundTripsToOneCard(t *testing.T) {
	back, err := FromTOML("repo = \"https://github.com/you/site\"\nbuild = \"npm install\"\npublish = [\"8080:3000\"]\n\n[env]\nNODE_ENV = \"production\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Cards) != 1 {
		t.Fatalf("flat should be one card: %+v", back)
	}
	c := back.Cards[0]
	if c.Kind != KindRepo || c.Ref != "https://github.com/you/site" || c.Build != "npm install" {
		t.Fatalf("flat repo not restored: %+v", c)
	}
	if len(c.Env) != 1 || c.Env[0] != "NODE_ENV=production" {
		t.Fatalf("flat env not restored: %+v", c.Env)
	}
}

func TestKindOfRunClassifies(t *testing.T) {
	cases := map[string]string{
		"git+https://github.com/o/r":    KindRepo,
		"postgres@17":                   KindRegistry,
		"ply/plybox-web":                KindRegistry,
		"https://cdn.example.com/a.img": KindImage,
	}
	for run, want := range cases {
		if k, _ := kindOfRun(run); k != want {
			t.Errorf("%q → %q, want %q", run, k, want)
		}
	}
}

func TestDraftWriteReadPromote(t *testing.T) {
	dep := t.TempDir()
	c := Cart{Name: "shop", Cards: []Card{
		{Name: "db", Kind: KindRegistry, Ref: "postgres@17"},
		{Name: "web", Kind: KindRepo, Ref: "https://github.com/you/web", Build: "npm install"},
	}}
	if err := WriteDraft(dep, "d1", c); err != nil {
		t.Fatal(err)
	}
	// the draft lives in the ignored .drafts dir, NOT the deployments root
	if _, err := os.Stat(filepath.Join(dep, ".drafts", "d1.toml")); err != nil {
		t.Fatalf("draft not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dep, "shop.toml")); !os.IsNotExist(err) {
		t.Fatalf("nothing should be in the deployments root yet")
	}
	back, err := ReadDraft(dep, "d1")
	if err != nil || len(back.Cards) != 2 || back.Name != "shop" {
		t.Fatalf("read draft wrong: %+v (%v)", back, err)
	}
	if err := Deploy(dep, "d1", "shop", back); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dep, "shop.toml")); err != nil {
		t.Fatalf("promote did not create the deployment file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dep, ".drafts", "d1.toml")); !os.IsNotExist(err) {
		t.Fatalf("draft should be cleared after deploy")
	}
}

func TestReadMissingDraftIsEmpty(t *testing.T) {
	c, err := ReadDraft(t.TempDir(), "nope")
	if err != nil || len(c.Cards) != 0 {
		t.Fatalf("missing draft should be empty cart, got %+v (%v)", c, err)
	}
}

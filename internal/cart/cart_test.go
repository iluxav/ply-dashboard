package cart

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// assertTOMLEqual decodes two TOML docs into generic maps and asserts they're
// value-equal — the round-trip needn't be byte-identical, only lossless.
func assertTOMLEqual(t *testing.T, want, got string) {
	t.Helper()
	var mw, mg map[string]any
	if _, err := toml.Decode(want, &mw); err != nil {
		t.Fatalf("decode want: %v", err)
	}
	if _, err := toml.Decode(got, &mg); err != nil {
		t.Fatalf("decode got: %v\n%s", err, got)
	}
	if !reflect.DeepEqual(mw, mg) {
		t.Fatalf("round-trip lost/changed a field:\nwant=%#v\ngot =%#v\n--- got TOML ---\n%s", mw, mg, got)
	}
}

func TestFlatOrderPreservesUnmodeledFields(t *testing.T) {
	// the dashboard's-own-deployment shape — grant_links MUST survive.
	spec := `app = "dashboard"
version = "0.1.43"
grant_links = true
env_file = ".env/dash.env"
token_file = "t.tok"
scale = 3
publish = ["internal:7070"]
domain = ["qa.plybox.sh"]

[env]
NODE_ENV = "production"
`
	c, err := FromTOML(spec)
	if err != nil {
		t.Fatal(err)
	}
	out := c.ToTOML()
	assertTOMLEqual(t, spec, out)
	if !strings.Contains(out, "grant_links = true") {
		t.Fatalf("grant_links dropped:\n%s", out)
	}
}

func TestCompositionMemberPreservesExtras(t *testing.T) {
	spec := `[package]
name = "shop"
version = "0.1.0"

[[service]]
run = "postgres@17"
name = "db"
publish = ["internal:5432"]
scale = 2
egress = { mode = "enforce" }
params = { region = "eu" }

[[service]]
run = "git+https://github.com/you/web"
name = "web"
build = "npm install"
after = ["db"]
`
	c, err := FromTOML(spec)
	if err != nil {
		t.Fatal(err)
	}
	var db, web Card
	for _, cd := range c.Cards {
		switch cd.Name {
		case "db":
			db = cd
		case "web":
			web = cd
		}
	}
	if len(web.Extra) != 0 {
		t.Fatalf("web should carry no extras, got %v", web.Extra)
	}
	if len(db.Extra) != 3 { // scale, egress, params — on db only
		t.Fatalf("db should carry scale/egress/params, got %v", db.Extra)
	}
	assertTOMLEqual(t, spec, c.ToTOML())
}

func TestCompositionPreservesPackageVersion(t *testing.T) {
	// two services so it stays a composition (a 1-card cart collapses to flat).
	spec := `[package]
name = "shop"
version = "2.3.4"

[[service]]
run = "redis@8"
name = "cache"

[[service]]
run = "postgres@17"
name = "db"
`
	c, err := FromTOML(spec)
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != "2.3.4" {
		t.Fatalf("version = %q, want 2.3.4", c.Version)
	}
	assertTOMLEqual(t, spec, c.ToTOML())
}

func TestRenderTOMLValue(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"a b", `"a b"`},
		{true, "true"},
		{false, "false"},
		{int64(7), "7"},
		{3.5, "3.5"},
		{[]any{"x", int64(2)}, `["x", 2]`},
		{map[string]any{"mode": "enforce", "n": int64(1)}, `{ mode = "enforce", n = 1 }`},
	}
	for _, c := range cases {
		if got := renderTOMLValue(c.in); got != c.want {
			t.Errorf("renderTOMLValue(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

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

func TestDraftMetaRoundTripAndDeploy(t *testing.T) {
	dep := t.TempDir()
	ref := "https://github.com/you/server"
	// missing → empty map, never nil-panicking
	if m := ReadMeta(dep, "d1"); len(m) != 0 {
		t.Fatalf("missing meta should be empty, got %v", m)
	}
	if err := WriteMeta(dep, "d1", DraftMeta{ref: {"DATABASE_URL", "PORT"}}); err != nil {
		t.Fatal(err)
	}
	got := ReadMeta(dep, "d1")
	if fmt.Sprint(got[ref]) != fmt.Sprint([]string{"DATABASE_URL", "PORT"}) {
		t.Fatalf("meta round-trip wrong: %v", got)
	}
	// meta must NOT sit in the deployments root and must retire on deploy
	if _, err := os.Stat(filepath.Join(dep, ".drafts", "d1.meta.json")); err != nil {
		t.Fatalf("meta not in .drafts: %v", err)
	}
	c := Cart{Name: "app", Cards: []Card{{Name: "server", Kind: KindRepo, Ref: ref}}}
	if err := Deploy(dep, "d1", "app", c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dep, ".drafts", "d1.meta.json")); !os.IsNotExist(err) {
		t.Fatalf("meta should be removed after deploy")
	}
}

// GET /deploy/{name}/edit reads a deployment file, parses it into cards, and
// seeds a draft keyed by the deployment name — so deploying overwrites in
// place. This covers that load path end-to-end (minus the thin HTTP layer).
func TestEditLoadCompositionSeedsDraftWithNCards(t *testing.T) {
	dir := t.TempDir()
	spec := `[package]
name = "shop"
version = "0.1.0"

[[service]]
run = "postgres@17"
name = "db"

[[service]]
run = "git+https://github.com/you/server"
name = "server"
build = "npm install"
after = ["db"]

[[service]]
run = "git+https://github.com/you/web"
name = "web"
after = ["server"]
`
	c, err := FromTOML(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Cards) != 3 {
		t.Fatalf("want 3 cards, got %d", len(c.Cards))
	}
	if err := WriteDraft(dir, "shop", c); err != nil {
		t.Fatal(err)
	}
	got, err := ReadDraft(dir, "shop")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"db", "server", "web"}
	if len(got.Cards) != len(want) {
		t.Fatalf("draft round-trip lost cards: %d", len(got.Cards))
	}
	for i, w := range want {
		if got.Cards[i].Name != w {
			t.Fatalf("card %d name = %q want %q", i, got.Cards[i].Name, w)
		}
	}
	if got.Cards[1].Kind != KindRepo || len(got.Cards[1].After) != 1 || got.Cards[1].After[0] != "db" {
		t.Fatalf("server card lost kind/after: %+v", got.Cards[1])
	}
}

func TestDockerCardFlatOrder(t *testing.T) {
	// a single docker card → a flat `docker =` order
	c := Cart{Name: "pg", Cards: []Card{
		{Name: "pg", Kind: KindDocker, Ref: "docker://postgres:17", Publish: []string{"internal:5432"}},
	}}
	out := c.ToTOML()
	if !strings.Contains(out, `docker = "docker://postgres:17"`) {
		t.Fatalf("flat docker order missing docker= line:\n%s", out)
	}
	back, err := FromTOML(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Cards) != 1 || back.Cards[0].Kind != KindDocker || back.Cards[0].Ref != "docker://postgres:17" {
		t.Fatalf("flat docker did not round-trip: %+v", back.Cards)
	}
}

func TestDockerMemberRoundTrips(t *testing.T) {
	c := Cart{Name: "stack", Cards: []Card{
		{Name: "web", Kind: KindRepo, Ref: "https://github.com/you/web", Build: "npm install"},
		{Name: "db", Kind: KindDocker, Ref: "docker://postgres:17", Publish: []string{"internal:5432"}, Env: []string{"POSTGRES_PASSWORD=x"}},
	}}
	out := c.ToTOML()
	if !strings.Contains(out, `run = "docker://postgres:17"`) {
		t.Fatalf("docker member missing run line:\n%s", out)
	}
	back, err := FromTOML(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Cards) != 2 || back.Cards[1].Kind != KindDocker || back.Cards[1].Ref != "docker://postgres:17" {
		t.Fatalf("docker member did not round-trip: %+v", back.Cards)
	}
}

func TestDeriveNameDocker(t *testing.T) {
	for ref, want := range map[string]string{
		"docker://postgres:17":             "postgres",
		"docker://ghcr.io/org/postgres:17": "postgres",
		"docker://redis":                   "redis",
		"docker://mongo@sha256:abc":        "mongo",
	} {
		if got := DeriveName(ref); got != want {
			t.Errorf("DeriveName(%q) = %q, want %q", ref, got, want)
		}
	}
}

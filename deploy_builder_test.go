package main

import (
	"html/template"
	"strings"
	"testing"

	"github.com/iluxav/ply-dashboard/internal/cart"
	"github.com/iluxav/ply-dashboard/internal/github"
)

func builderTemplate(t *testing.T, files ...string) *template.Template {
	t.Helper()
	funcs := template.FuncMap{
		"srcicon": srcIconSVG, "srcbadge": srcBadge, "inc": inc, "has": hasStr,
	}
	return template.Must(template.New("base.html").Funcs(funcs).ParseFS(webFS, files...))
}

func TestCartRendersServicesWithWiring(t *testing.T) {
	tmpl := builderTemplate(t, "web/templates/cart.html")
	data := pageData{DraftID: "d1", Cards: cardViews([]cart.Card{
		{Name: "db", Kind: cart.KindRegistry, Ref: "postgres@17", Publish: []string{"internal:5432"}},
		{Name: "web", Kind: cart.KindRepo, Ref: "https://github.com/you/web", Build: "npm install", After: []string{"db"}},
	}, nil)}
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "cart", data); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	// "git&#43;" is html/template escaping the badge's "+"; it renders as "git+"
	for _, want := range []string{"in this deployment", "postgres@17", "git", "npm install", "starts after"} {
		if !strings.Contains(out, want) {
			t.Fatalf("cart missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, `value="db" checked`) {
		t.Fatalf("the web card's after=db box should be checked:\n%s", out)
	}
}

func TestCartEmptyHint(t *testing.T) {
	tmpl := builderTemplate(t, "web/templates/cart.html")
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "cart", pageData{DraftID: "d1"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "add your first service") {
		t.Fatal("empty cart hint missing")
	}
}

func TestDetectedRepoPreview(t *testing.T) {
	tmpl := builderTemplate(t, "web/templates/detected.html")
	d := &detectedView{Kind: "repo", Note: "Next.js detected", Card: cart.Card{
		Kind: cart.KindRepo, Ref: "https://github.com/you/web", Name: "web", Build: "npm install"}}
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "detected", pageData{DraftID: "d1", Detected: d}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"add to deployment", "Next.js detected", `value="npm install"`, `value="https://github.com/you/web"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("detected repo preview missing %q in:\n%s", want, out)
		}
	}
}

func TestDetectedRegistryMatches(t *testing.T) {
	tmpl := builderTemplate(t, "web/templates/detected.html")
	d := &detectedView{Kind: "registry", Matches: []registryMatch{{Ref: "postgres", Versions: []string{"17", "16"}, Description: "SQL database"}}}
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "detected", pageData{DraftID: "d1", Detected: d}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, `name="reftmpl" value="postgres"`) || !strings.Contains(out, "<option>17</option>") {
		t.Fatalf("registry matches render wrong:\n%s", out)
	}
}

func TestRecipeShowsTOML(t *testing.T) {
	tmpl := builderTemplate(t, "web/templates/recipe.html")
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "recipe", pageData{RecipeTOML: "[package]\nname = \"x\"\n"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "[package]") {
		t.Fatal("recipe TOML not shown")
	}
}

// The full builder page must parse and execute with base.html + every partial
// it embeds — the real proof parseTemplates won't panic at boot.
func TestDeployNewPageRenders(t *testing.T) {
	tmpl := builderTemplate(t,
		"web/templates/base.html", "web/templates/deploy_new.html",
		"web/templates/cart.html", "web/templates/detected.html", "web/templates/recipe.html")
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "base.html", pageData{Authed: true, DeployAvailable: true, DraftID: "d1"}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"add a service", "new deployment", "deploy · reconcile applies it"} {
		if !strings.Contains(out, want) {
			t.Fatalf("builder page missing %q", want)
		}
	}
}

func TestAddEnvRef(t *testing.T) {
	c := cart.Card{Name: "server"}
	addEnvRef(&c, "postgres", "url", "DATABASE_URL")
	if len(c.Env) != 1 || c.Env[0] != "DATABASE_URL={postgres.url}" {
		t.Fatalf("addEnvRef env = %v", c.Env)
	}
	addEnvRef(&c, "postgres", "host", "  ") // blank key → no-op
	addEnvRef(&c, "", "url", "X")           // blank service → no-op
	if len(c.Env) != 1 {
		t.Fatalf("blank inputs should be skipped: %v", c.Env)
	}
}

func TestUseDatabase(t *testing.T) {
	c := cart.Card{Name: "server"}
	useDatabase(&c, "db")
	if len(c.Env) != 1 || c.Env[0] != "DATABASE_URL={db.url}" {
		t.Fatalf("useDatabase env = %v", c.Env)
	}
	if !hasStr(c.After, "db") {
		t.Fatalf("useDatabase should set after: %v", c.After)
	}
	useDatabase(&c, "db") // after stays unique
	n := 0
	for _, a := range c.After {
		if a == "db" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("db should appear once in after: %v", c.After)
	}
}

func TestIsDatabase(t *testing.T) {
	for _, s := range []string{"postgres@17", "ply/postgres", "mysql", "mariadb@11", "redis:7", "valkey", "mongodb", "mongo@6"} {
		if !isDatabase(s) {
			t.Errorf("isDatabase(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"qa-server", "web", "https://github.com/you/api", "nginx"} {
		if isDatabase(s) {
			t.Errorf("isDatabase(%q) = true, want false", s)
		}
	}
}

func TestInjectPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"qa-server": "QA_SERVER", "db": "DB", "web.api": "WEB_API", "postgres": "POSTGRES",
	} {
		if got := injectPrefix(in); got != want {
			t.Errorf("injectPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCartShowsNeedFirstWiring(t *testing.T) {
	tmpl := builderTemplate(t, "web/templates/cart.html")
	// server reads DATABASE_URL + PORT (from .env.example); DATABASE_URL is
	// already mapped to postgres.url, PORT is not yet mapped.
	data := pageData{DraftID: "d1", Cards: cardViews([]cart.Card{
		{Name: "postgres", Kind: cart.KindRegistry, Ref: "postgres@17"},
		{Name: "server", Kind: cart.KindRepo, Ref: "https://github.com/you/server",
			After: []string{"postgres"}, Env: []string{"DATABASE_URL={postgres.url}"}},
	}, cart.DraftMeta{"https://github.com/you/server": {"DATABASE_URL", "PORT"}})}
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "cart", data); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"where does each value come from?", // the need-first header
		"DATABASE_URL", "PORT",             // a row per needed var
		"{postgres.url}",                    // the mapped row shows its value
		`"op":"unmap","key":"DATABASE_URL"`, // …and a clear (×)
		`name="op" value="map"`,             // the unmapped row's from-control
		`name="service"`, `name="field"`, "— a fixed value —",
		"+ map another env var",
		"POSTGRES_HOST", "POSTGRES_PORT", // the after-injects hint stays
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("need-first wiring missing %q in:\n%s", want, out)
		}
	}
	// the source-first picker is gone
	for _, gone := range []string{"+ connect a service", "it exposes", "+ DATABASE_URL"} {
		if strings.Contains(out, gone) {
			t.Fatalf("old source-first affordance %q should be gone", gone)
		}
	}
}

func TestWireKeysUnionAndEnvMap(t *testing.T) {
	// Expects order first, then Env-only keys; deduped.
	got := wireKeys([]string{"DATABASE_URL", "PORT"}, []string{"DATABASE_URL={postgres.url}", "EXTRA=1"})
	want := []string{"DATABASE_URL", "PORT", "EXTRA"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("wireKeys = %v, want %v", got, want)
	}
	m := envMap([]string{"DATABASE_URL={postgres.url}", "PORT=3000"})
	if m["DATABASE_URL"] != "{postgres.url}" || m["PORT"] != "3000" {
		t.Fatalf("envMap = %v", m)
	}
}

func TestRemoveEnvKey(t *testing.T) {
	got := removeEnvKey([]string{"A=1", "DATABASE_URL={db.url}", "B=2"}, "DATABASE_URL")
	if strings.Join(got, ",") != "A=1,B=2" {
		t.Fatalf("removeEnvKey = %v", got)
	}
}

func TestClassifySource(t *testing.T) {
	cases := map[string]string{
		"":                            "empty",
		"https://github.com/you/web":  "repo",
		"git@github.com:you/web.git":  "repo",
		"gitlab.com/you/web":          "repo",
		"postgres":                    "registry",
		"postgres@17":                 "registry",
		"ply/plybox-web":              "registry",
		"app.img":                     "image",
		"https://cdn.example/app.img": "image",
		"docker://redis:7":            "docker",
	}
	for in, want := range cases {
		if got := classifySource(in); got != want {
			t.Errorf("classifySource(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvRefsAreIdempotent(t *testing.T) {
	c := &cart.Card{Name: "web"}
	// same KEY twice → one line, updated in place, never duplicated
	useDatabase(c, "postgres")
	useDatabase(c, "postgres")
	addEnvRef(c, "postgres", "url", "DATABASE_URL")
	n := 0
	for _, e := range c.Env {
		if strings.HasPrefix(e, "DATABASE_URL=") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("DATABASE_URL appears %d times, want 1: %v", n, c.Env)
	}
	// a different KEY for the same ref still adds its own line
	addEnvRef(c, "postgres", "host", "PGHOST")
	if !hasStr(c.Env, "PGHOST={postgres.host}") {
		t.Fatalf("PGHOST not added: %v", c.Env)
	}
}

func TestPlyRepoWithPackageJSONPrefillsNpmInstall(t *testing.T) {
	// a ply.toml Node app: build must be prefilled so node_modules exists at pack
	c := cardFromInspection(github.Inspection{
		Framework: "ply", CloneURL: "https://github.com/you/api", HasPackageJSON: true, AppPort: "3001",
	}, "")
	if c.Build != "npm install" {
		t.Fatalf("build = %q, want %q", c.Build, "npm install")
	}
	// a ply.toml repo with no package.json (e.g. Go/Rust) stays buildless
	c2 := cardFromInspection(github.Inspection{Framework: "ply", CloneURL: "https://github.com/you/svc"}, "")
	if c2.Build != "" {
		t.Fatalf("build = %q, want empty", c2.Build)
	}
}

func TestCartDockerCardShowsWarning(t *testing.T) {
	tmpl := builderTemplate(t, "web/templates/cart.html")
	data := pageData{DraftID: "d1", Cards: cardViews([]cart.Card{
		{Name: "db", Kind: cart.KindDocker, Ref: "docker://postgres:17", Publish: []string{"internal:5432"}},
	}, nil)}
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "cart", data); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"docker · docker://postgres:17", "Docker image — larger than a native package", "ply/&lt;name&gt;"} {
		if !strings.Contains(out, want) {
			t.Fatalf("docker card missing %q in:\n%s", want, out)
		}
	}
}

func TestClassifyDocker(t *testing.T) {
	if classifySource("docker://postgres:17") != "docker" {
		t.Fatal("docker:// should classify as docker")
	}
}

func TestDockerDatabaseOffersNoParams(t *testing.T) {
	// a docker "postgres" exposes only built-in facts — the picker must NOT
	// flag it IsDB (which would offer url/password that don't resolve).
	v := cardViews([]cart.Card{
		{Name: "postgres", Kind: cart.KindDocker, Ref: "docker://postgres:17"},
		{Name: "app", Kind: cart.KindRepo, Ref: "https://github.com/you/app"},
	}, nil)
	for _, o := range v[1].Others {
		if o.Name == "postgres" && o.IsDB {
			t.Fatal("a docker database must not be flagged IsDB (no params to offer)")
		}
	}
	// a REGISTRY postgres (native package) does declare them → IsDB
	v2 := cardViews([]cart.Card{
		{Name: "pg", Kind: cart.KindRegistry, Ref: "postgres@17"},
		{Name: "app", Kind: cart.KindRepo, Ref: "https://github.com/you/app"},
	}, nil)
	ok := false
	for _, o := range v2[1].Others {
		if o.Name == "pg" && o.IsDB {
			ok = true
		}
	}
	if !ok {
		t.Fatal("a registry database should be flagged IsDB")
	}
}

func TestParseWireRow(t *testing.T) {
	// a {service.field} reference
	r := parseWireRow("SERVER_HOST", "{qa-server.host}")
	if !r.Mapped || !r.IsRef || r.Service != "qa-server" || r.Field != "host" {
		t.Fatalf("ref row = %+v", r)
	}
	// a fixed literal value
	r = parseWireRow("PORT", "3000")
	if !r.Mapped || r.IsRef || r.Service != "" || r.Value != "3000" {
		t.Fatalf("literal row = %+v", r)
	}
	// unmapped
	r = parseWireRow("SECRET", "")
	if r.Mapped || r.IsRef {
		t.Fatalf("unmapped row = %+v", r)
	}
	// a db param field survives (dot-free field, greedy still fine)
	r = parseWireRow("DATABASE_URL", "{postgres.url}")
	if !r.IsRef || r.Field != "url" {
		t.Fatalf("db param row = %+v", r)
	}
}

func TestCartMappedRowPickerPreselected(t *testing.T) {
	tmpl := builderTemplate(t, "web/templates/cart.html")
	// qa-web's SERVER_HOST is mapped to qa-server.host; PORT is a fixed value.
	data := pageData{DraftID: "d1", Cards: cardViews([]cart.Card{
		{Name: "qa-server", Kind: cart.KindRepo, Ref: "https://github.com/you/qa-server"},
		{Name: "web", Kind: cart.KindRepo, Ref: "https://github.com/you/qa-web",
			Env: []string{"SERVER_HOST={qa-server.host}", "PORT=3000"}},
	}, cart.DraftMeta{"https://github.com/you/qa-web": {"SERVER_HOST", "PORT"}})}
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "cart", data); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		`value="qa-server" data-db="" selected`,       // the ref row: service pre-selected
		`<option value="host" selected>host</option>`, // …and its field pre-selected
		`data-cur="host"`,                             // field survives fillFields rebuild
		"remap",                                       // a mapped row's button reads "remap"
		`"op":"unmap","key":"SERVER_HOST"`,            // …with a × to clear it
		`<option value="__value__" selected>`,         // the literal row: fixed-value pre-selected
		`value="3000"`,                                // …with the literal prefilled
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("mapped-row picker missing %q in:\n%s", want, out)
		}
	}
}

func TestMapOpRemapsExistingKey(t *testing.T) {
	// the rtrtrtr fix: re-mapping SERVER_HOST from postgres to qa-server
	// REPLACES the line in place rather than adding a duplicate.
	c := cart.Card{Name: "web", Env: []string{"SERVER_HOST={postgres.host}"}}
	addEnvRef(&c, "qa-server", "host", "SERVER_HOST")
	if len(c.Env) != 1 || c.Env[0] != "SERVER_HOST={qa-server.host}" {
		t.Fatalf("remap env = %v, want [SERVER_HOST={qa-server.host}]", c.Env)
	}
	// re-mapping to a fixed value likewise replaces in place
	c.Env = setEnvKey(c.Env, "SERVER_HOST", "db.internal")
	if len(c.Env) != 1 || c.Env[0] != "SERVER_HOST=db.internal" {
		t.Fatalf("remap-to-value env = %v", c.Env)
	}
}

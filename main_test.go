package main

import (
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/iluxav/ply-dashboard/internal/plystate"
)

// stubFresh stands in for the freshness view the real freshOf returns; a nil
// pointer reads as "no info", which is what the templates handle.
type stubFresh struct {
	Building        bool
	UpdateAvailable bool
	Latest          string
}

func testTemplate(t *testing.T, files ...string) *template.Template {
	t.Helper()
	funcs := template.FuncMap{
		"isBuilder": func(name string) bool { return strings.HasSuffix(name, "-builder") },
		"freshOf":   func(string) *stubFresh { return nil },
		"historyOf": func(string) []any { return nil },
	}
	return template.Must(template.New("p").Funcs(funcs).ParseFS(webFS, files...))
}

func oneInstance(app string) []plystate.Instance {
	return []plystate.Instance{{App: app, N: 1, Started: time.Now().Unix()}}
}

// A stack's members render under a single "stack · <name>" header and carry
// the accent; a standalone app (dashboard) gets neither. Exercises the
// $prev change-tracker in apps_table.html.
func TestAppsTableGroupsStackMembers(t *testing.T) {
	tmpl := testTemplate(t, "web/templates/apps_table.html")
	apps := []plystate.App{
		{Name: "dashboard", Instances: oneInstance("dashboard")},
		{Name: "db", Stack: "qa", Instances: oneInstance("db")},
		{Name: "server", Stack: "qa", Instances: oneInstance("server")},
		{Name: "web", Stack: "qa", Instances: oneInstance("web")},
	}
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "apps_table", pageData{Apps: apps}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if n := strings.Count(out, "stack · qa"); n != 1 {
		t.Fatalf("want exactly one 'stack · qa' header, got %d\n%s", n, out)
	}
	if !strings.Contains(out, "border-l-2 border-accent/30") {
		t.Fatal("expected stack member rows to carry the left accent")
	}
}

// No stacks at all → no header rows, no accent (regression: ungrouped hosts
// render exactly as before).
func TestAppsTableWithoutStacksIsUnchanged(t *testing.T) {
	tmpl := testTemplate(t, "web/templates/apps_table.html")
	apps := []plystate.App{{Name: "solo", Instances: oneInstance("solo")}}
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "apps_table", pageData{Apps: apps}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, "stack · ") {
		t.Fatal("did not expect any stack header for a standalone app")
	}
	if strings.Contains(out, "border-l-2 border-accent/30") {
		t.Fatal("did not expect a stack accent for a standalone app")
	}
}

// A deployment with a Recipe shows the read-only composition panel with the
// TOML content; one without shows nothing extra.
func TestDeploymentsShowReadOnlyRecipe(t *testing.T) {
	tmpl := testTemplate(t, "web/templates/deployments.html")
	groups := plystate.GroupDeployments([]plystate.Deployment{
		{Name: "qa", Spec: `repo = "https://github.com/iluxav/qa-stack"`,
			Recipe: "[package]\nname = \"qa-stack\"\n\n[[service]]\nname = \"db\"\n"},
		{Name: "plain", Spec: `app = "redis"`},
	})
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "deployments", pageData{Groups: groups}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if n := strings.Count(out, "composition · recipe (read-only)"); n != 1 {
		t.Fatalf("want exactly one read-only recipe panel, got %d", n)
	}
	if !strings.Contains(out, "qa-stack") {
		t.Fatal("expected the recipe TOML to be rendered")
	}
}

// The reclaimable-data section shows an orphan row with its size and a reclaim
// button posting the app name; with no orphans the section is absent.
func TestDeployPageShowsReclaimableOrphans(t *testing.T) {
	tmpl := testTemplate(t, "web/templates/deploy.html", "web/templates/deployments.html")
	data := pageData{
		DeployAvailable: true,
		Orphans:         []plystate.Volume{{App: "db", Name: "data.1", Status: "orphaned", Bytes: 40632320, HasBytes: true}},
	}
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "content", data); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"reclaimable data", "/deploy/volume/reclaim", `name="app" value="db"`, "reclaim", "38.8 MiB"} {
		if !strings.Contains(out, want) {
			t.Fatalf("orphan section missing %q in:\n%s", want, out)
		}
	}
}

func TestDeployPageNoOrphansNoSection(t *testing.T) {
	tmpl := testTemplate(t, "web/templates/deploy.html", "web/templates/deployments.html")
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "content", pageData{DeployAvailable: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "reclaimable data") {
		t.Fatal("no orphans → no reclaimable-data section")
	}
}

// The safe "delete" stays, and a secondary "delete + data" (with_data=1) is
// added — both to the same delete route.
func TestDeploymentsDeleteWithData(t *testing.T) {
	tmpl := testTemplate(t, "web/templates/deployments.html")
	groups := plystate.GroupDeployments([]plystate.Deployment{{Name: "xcf", Spec: `repo = "x"`}})
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "deployments", pageData{Groups: groups}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "delete + data") || !strings.Contains(out, `name="with_data" value="1"`) {
		t.Fatalf("expected a delete + data action:\n%s", out)
	}
	if strings.Count(out, `action="/deploy/xcf/delete"`) != 2 {
		t.Fatal("expected both delete and delete+data to post the same route")
	}
}

package plystate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const plyboxStack = `[stack]
name = "plybox"
env_file = "/etc/ply/plybox.env"

[[app]]
run  = "postgres@17"
name = "plybox-db"
e    = ["POSTGRES_PASSWORD=$POSTGRES_PASSWORD", "POSTGRES_DB=$POSTGRES_DB"]

[[app]]
run     = "https://github.com/iluxav/ply-web/releases/download/v0.4.9/plybox-web-0.4.9-linux-x64.img"
after   = ["plybox-db"]
publish = ["internal:3000"]
e = ["POSTGRES_PASSWORD=$POSTGRES_PASSWORD", "GITHUB_CLIENT_ID=$GITHUB_CLIENT_ID"]
`

func TestParseStackMembersAndHoles(t *testing.T) {
	p := Paths{Deployments: t.TempDir()}
	view, err := ParseStack(p, plyboxStack)
	if err != nil || view == nil {
		t.Fatalf("parse: %v (view=%v)", err, view)
	}
	if view.Name != "plybox" || view.EnvFile != "/etc/ply/plybox.env" {
		t.Fatalf("meta: %+v", view)
	}
	if len(view.Members) != 2 {
		t.Fatalf("members: %+v", view.Members)
	}
	// explicit name kept; URL member derives from the image basename
	if view.Members[0].Name != "plybox-db" || view.Members[1].Name != "plybox-web" {
		t.Fatalf("names: %q %q", view.Members[0].Name, view.Members[1].Name)
	}
	// unique holes in declaration order
	keys := []string{}
	for _, h := range view.Holes {
		keys = append(keys, h.Key)
	}
	if strings.Join(keys, ",") != "POSTGRES_PASSWORD,POSTGRES_DB,GITHUB_CLIENT_ID" {
		t.Fatalf("holes: %v", keys)
	}
}

func TestParseStackNotAStack(t *testing.T) {
	view, err := ParseStack(Paths{}, "app = \"dashboard\"\n")
	if err != nil || view != nil {
		t.Fatalf("plain app spec must parse to nil, got %v / %v", view, err)
	}
}

func TestDeployStackWritesEnvAndSpec(t *testing.T) {
	dir := t.TempDir()
	p := Paths{Deployments: dir}
	// no env_file in the stack: one is created and wired in
	spec := "[[app]]\nrun = \"postgres@17\"\ne = [\"POSTGRES_PASSWORD=$PW\"]\n"
	err := DeployStack(p, "mystack", spec, map[string]string{"PW": "s3cret"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(dir, "mystack.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "env_file = \".env/mystack.env\"") {
		t.Fatalf("env_file not wired in:\n%s", written)
	}
	env, err := os.ReadFile(filepath.Join(dir, ".env", "mystack.env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(env) != "PW=s3cret\n" {
		t.Fatalf("env file: %q", env)
	}
	err = DeployStack(p, "mystack2", spec, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// untouched paste reaches disk verbatim
	verbatim, _ := os.ReadFile(filepath.Join(dir, "mystack2.toml"))
	if string(verbatim) != spec {
		t.Fatalf("expected verbatim spec, got:\n%s", verbatim)
	}
}

func TestDeployStackAppliesMemberOverrides(t *testing.T) {
	dir := t.TempDir()
	p := Paths{Deployments: dir}
	err := DeployStack(p, "plybox", plyboxStack, nil, map[int]MemberOverride{
		1: {Domain: []string{"plybox.sh"}, Publish: []string{"internal:3000"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	written, _ := os.ReadFile(filepath.Join(dir, "plybox.toml"))
	text := string(written)
	if !strings.Contains(text, "domain = [\"plybox.sh\"]") {
		t.Fatalf("domain override missing:\n%s", text)
	}
	// the re-render keeps everything else: env file ref, member wiring
	if !strings.Contains(text, "env_file = \"/etc/ply/plybox.env\"") ||
		!strings.Contains(text, "after = [\"plybox-db\"]") {
		t.Fatalf("re-render lost fields:\n%s", text)
	}
	// unchanged values submitted back do NOT force a re-render
	err = DeployStack(p, "plybox2", plyboxStack, nil, map[int]MemberOverride{
		0: {Publish: []string{}, Domain: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	verbatim, _ := os.ReadFile(filepath.Join(dir, "plybox2.toml"))
	if string(verbatim) != plyboxStack {
		t.Fatalf("expected verbatim spec:\n%s", verbatim)
	}
}

// The new form: [package] identity + [[service]] members.
func TestParsePackageComposition(t *testing.T) {
	p := Paths{Deployments: t.TempDir()}
	const spec = `[package]
name = "todos"
version = "0.1.0"

[[service]]
run = "postgres@17"
name = "db"

[[service]]
run  = "git+https://github.com/iluxav/rm-server"
name = "server"
after = ["db"]
`
	view, err := ParseStack(p, spec)
	if err != nil || view == nil {
		t.Fatalf("parse: %v (view=%v)", err, view)
	}
	if view.Name != "todos" || view.Version != "0.1.0" {
		t.Fatalf("identity from [package]: %+v", view)
	}
	if len(view.Members) != 2 || view.Members[0].Name != "db" || view.Members[1].Name != "server" {
		t.Fatalf("members: %+v", view.Members)
	}
}

// A file must not carry both [[service]] and [[app]].
func TestParseStackBothArraysError(t *testing.T) {
	_, err := ParseStack(Paths{}, "[[service]]\nrun = \"redis\"\n\n[[app]]\nrun = \"postgres@17\"\n")
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("expected a both-arrays error, got %v", err)
	}
}

// Render (used only when the form edits a member) emits the new spelling.
func TestRenderEmitsPackageAndService(t *testing.T) {
	v := &StackView{Name: "todos", Members: []StackMember{{Run: "postgres@17", Name: "db"}}}
	out := v.Render()
	if !strings.Contains(out, "[package]") || !strings.Contains(out, "[[service]]") {
		t.Fatalf("want [package] + [[service]], got:\n%s", out)
	}
	if strings.Contains(out, "[stack]") || strings.Contains(out, "[[app]]") {
		t.Fatalf("must not emit legacy headers:\n%s", out)
	}
}

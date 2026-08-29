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
	err := DeployStack(p, "mystack", spec, map[string]string{"PW": "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(dir, "mystack.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "env_file = \".env/mystack.env\"") {
		t.Fatalf("env_file not injected:\n%s", written)
	}
	env, err := os.ReadFile(filepath.Join(dir, ".env", "mystack.env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(env) != "PW=s3cret\n" {
		t.Fatalf("env file: %q", env)
	}
	// merge keeps existing keys
	err = DeployStack(p, "mystack2", spec, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestInjectEnvFileIntoExistingStackSection(t *testing.T) {
	out := injectEnvFile("[stack]\nname = \"x\"\n\n[[app]]\nrun = \"redis@8\"\n", ".env/x.env")
	if !strings.Contains(out, "[stack]\nenv_file = \".env/x.env\"\nname = \"x\"") {
		t.Fatalf("inject: %q", out)
	}
}

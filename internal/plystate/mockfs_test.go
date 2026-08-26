package plystate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMockWorldRendersEveryState(t *testing.T) {
	w := newMockWorld(t.TempDir())

	instances, err := List(w.p)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(instances) != 7 {
		t.Fatalf("want 7 instances, got %d", len(instances))
	}
	byHealth := map[string]string{}
	for _, a := range Apps(instances) {
		byHealth[a.Name] = a.Health()
	}
	if byHealth["worker"] != "degraded" {
		t.Errorf("worker should be the unhealthy one, got %q", byHealth["worker"])
	}
	for _, app := range []string{"nextapp", "redis", "postgres", "dashboard"} {
		if byHealth[app] != "ok" {
			t.Errorf("%s should be ok, got %q", app, byHealth[app])
		}
	}

	deployments := Deployments(w.p)
	if len(deployments) != 5 {
		t.Fatalf("want 5 deployments, got %d", len(deployments))
	}
	states := map[string]bool{} // ok / failed / waiting all present?
	for _, d := range deployments {
		switch {
		case d.Status == nil:
			states["waiting"] = true
		case d.Status.OK:
			states["ok"] = true
		default:
			states["failed"] = true
		}
	}
	for _, want := range []string{"ok", "failed", "waiting"} {
		if !states[want] {
			t.Errorf("deployment state %q missing from fixtures", want)
		}
	}

	if lines := LogTail(w.p, "nextapp", 1, 10); len(lines) == 0 {
		t.Error("nextapp.1 log ring is empty")
	}
}

func TestMockPuppeteerAnswersControl(t *testing.T) {
	w := newMockWorld(t.TempDir())
	if err := SubmitControl(w.p, "redis", "scale", "3"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	w.answerControls()
	instances, _ := List(w.p)
	n := 0
	for _, inst := range instances {
		if inst.App == "redis" {
			n++
		}
	}
	if n != 3 {
		t.Errorf("scale to 3: got %d redis instances", n)
	}
	r := LastResult(w.p, "redis")
	if r == nil || !r.OK || r.Command != "scale" {
		t.Errorf("last-result not written: %+v", r)
	}
}

func TestMockReconcilerAnswersNewDeployment(t *testing.T) {
	w := newMockWorld(t.TempDir())
	if err := WriteDeployment(w.p, "shop", "shop", "", "internal:8080", "", ""); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.playReconciler() // first pass: notices
	w.firstSeen["shop"] = time.Now().Add(-4 * time.Second)
	w.playReconciler() // second pass: answers
	for _, d := range Deployments(w.p) {
		if d.Name == "shop" {
			if d.Status == nil || !d.Status.OK {
				t.Fatalf("shop status not written: %+v", d.Status)
			}
			return
		}
	}
	t.Fatal("shop deployment not listed")
}

func TestSourceSpecRender(t *testing.T) {
	spec := SourceSpec{
		Name:       "shop",
		Repo:       "git@github.com:acme/shop.git",
		Ref:        "main",
		Build:      "npm ci && npm run build",
		Runtime:    "node@24",
		DeployKey:  ".keys/shop",
		Entrypoint: "node dist/index.js",
		Include:    "dist/, package.json",
		Port:       "3000",
		Publish:    "internal:3000",
		Domain:     "shop.example.com",
		Env:        "NODE_ENV=production\n\nEMPTY=\n",
	}
	text, err := spec.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		`repo = "git@github.com:acme/shop.git"`,
		`deploy_key = ".keys/shop"`,
		`entrypoint = ["node", "dist/index.js"]`,
		`include = ["dist/", "package.json"]`,
		"port = 3000",
		`publish = ["internal:3000"]`,
		`NODE_ENV = "production"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered spec missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "EMPTY") {
		t.Error("blank env value should be dropped")
	}

	for _, bad := range []SourceSpec{
		{Name: "Bad Name", Repo: "x", Build: "b"},
		{Name: "ok", Repo: "", Build: "b"},
		{Name: "ok", Repo: "has space", Build: "b"},
		{Name: "ok", Repo: "x", Port: "99999", Build: "b"},
		{Name: "ok", Repo: "x"}, // no build, no entrypoint
	} {
		if _, err := bad.Render(); err == nil {
			t.Errorf("should reject %+v", bad)
		}
	}
}

func TestWriteDeployKey(t *testing.T) {
	w := newMockWorld(t.TempDir())
	if _, err := WriteDeployKey(w.p, "shop", "not a key"); err == nil {
		t.Error("junk should be rejected")
	}
	ref, err := WriteDeployKey(w.p, "shop", "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if ref != ".keys/shop" {
		t.Errorf("ref = %q", ref)
	}
	raw, err := os.ReadFile(filepath.Join(w.p.Deployments, ".keys", "shop"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.HasSuffix(string(raw), "-----END OPENSSH PRIVATE KEY-----\n") {
		t.Error("key must keep its trailing newline (ssh refuses it otherwise)")
	}
	info, _ := os.Stat(filepath.Join(w.p.Deployments, ".keys", "shop"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestFreshnessPlumbing(t *testing.T) {
	d := Deployment{
		Name:   "x",
		Spec:   "repo = \"https://github.com/iluxav/next-dummy\"\nref = \"main\"\n",
		Status: &DeployStatus{OK: true, Detail: "deployed nextapp-0.1.0-linux-x64.img @ 6bb7edf31153"},
	}
	if got := d.Field("repo"); got != "https://github.com/iluxav/next-dummy" {
		t.Errorf("Field(repo) = %q", got)
	}
	if got := d.Field("ref"); got != "main" {
		t.Errorf("Field(ref) = %q", got)
	}
	if got := d.Field("github"); got != "" {
		t.Errorf("Field(github) should be empty, got %q", got)
	}
	if got := d.DeployedCommit(); got != "6bb7edf31153" {
		t.Errorf("DeployedCommit = %q", got)
	}
	if got := d.DeployedVersion(); got != "0.1.0" {
		t.Errorf("DeployedVersion = %q", got)
	}
	if got := (Deployment{}).DeployedCommit(); got != "" {
		t.Errorf("no status should mean no commit, got %q", got)
	}
}

func TestTouchDeployment(t *testing.T) {
	w := newMockWorld(t.TempDir())
	path := filepath.Join(w.p.Deployments, "redis.toml")
	before, _ := os.ReadFile(path)
	old, _ := os.Stat(path)
	time.Sleep(10 * time.Millisecond)
	if err := TouchDeployment(w.p, "redis"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("touch must not change content")
	}
	if info, _ := os.Stat(path); !info.ModTime().After(old.ModTime()) {
		t.Error("touch must bump mtime (that's the whole point)")
	}
	if err := TouchDeployment(w.p, "nope"); err == nil {
		t.Error("missing deployment should error")
	}
}

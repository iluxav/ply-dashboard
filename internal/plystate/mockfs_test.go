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

func TestEventsJournal(t *testing.T) {
	w := newMockWorld(t.TempDir())
	all := Events(w.p, "", 25)
	if len(all) < 6 {
		t.Fatalf("want seeded events, got %d", len(all))
	}
	if all[0].TS < all[len(all)-1].TS {
		t.Error("events must be newest first")
	}
	worker := Events(w.p, "worker", 25)
	for _, e := range worker {
		if e.App != "worker" {
			t.Errorf("filter leaked %q", e.App)
		}
	}
	if len(worker) != 2 {
		t.Errorf("worker should have 2 seeded events, got %d", len(worker))
	}
	kinds := map[string]bool{}
	for _, e := range all {
		kinds[e.Event] = true
	}
	for _, want := range []string{"deploy", "deploy-failed", "scale", "instance-restart"} {
		if !kinds[want] {
			t.Errorf("event kind %q missing from seeds", want)
		}
	}
	if all[0].Age() == "-" {
		t.Errorf("Age broken: %+v", all[0])
	}
}

func TestGithubSpecRender(t *testing.T) {
	gh := GithubSpec{
		Name:      "dash",
		Repo:      "iluxav/ply-dashboard",
		Asset:     "dashboard",
		TokenFile: ".keys/dash.token",
		Publish:   "internal:7070",
		Domain:    "dash.example.com",
		Env:       "LOG=debug",
	}
	text, err := gh.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		`github = "iluxav/ply-dashboard"`,
		`asset = "dashboard"`,
		`token_file = ".keys/dash.token"`,
		`publish = ["internal:7070"]`,
		`LOG = "debug"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "version") {
		t.Error("blank version must be omitted (follow latest)")
	}
	// asset == name is the default; the spec should not repeat it
	same := GithubSpec{Name: "dashboard", Repo: "a/b", Asset: "dashboard"}
	text, _ = same.Render()
	if strings.Contains(text, "asset") {
		t.Error("asset equal to name should be omitted")
	}
	for _, bad := range []GithubSpec{
		{Name: "x", Repo: "not-org-repo"},
		{Name: "Bad Name", Repo: "a/b"},
	} {
		if _, err := bad.Render(); err == nil {
			t.Errorf("should reject %+v", bad)
		}
	}
}

func TestWriteToken(t *testing.T) {
	w := newMockWorld(t.TempDir())
	if _, err := WriteToken(w.p, "dash", "short"); err == nil {
		t.Error("junk token should be rejected")
	}
	if _, err := WriteToken(w.p, "dash", "has spaces in it which is wrong"); err == nil {
		t.Error("multiword token should be rejected")
	}
	ref, err := WriteToken(w.p, "dash", "github_pat_11ABCDEF0123456789abcdef")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if ref != ".keys/dash.token" {
		t.Errorf("ref = %q", ref)
	}
	info, err := os.Stat(filepath.Join(w.p.Deployments, ".keys", "dash.token"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("token file: %v mode %v", err, info.Mode().Perm())
	}
}

func TestOneDeploymentAndRewrite(t *testing.T) {
	w := newMockWorld(t.TempDir())
	d, ok := OneDeployment(w.p, "redis")
	if !ok || d.Status == nil || !strings.Contains(d.Spec, `app = "redis"`) {
		t.Fatalf("OneDeployment redis: ok=%v %+v", ok, d)
	}
	if _, ok := OneDeployment(w.p, "nope"); ok {
		t.Error("missing deployment should not resolve")
	}
	if err := RewriteDeployment(w.p, "redis", "app = \"redis\"\nversion = \"8.1\"\n"); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	d, _ = OneDeployment(w.p, "redis")
	if !strings.Contains(d.Spec, `version = "8.1"`) {
		t.Error("edit not persisted")
	}
	for _, bad := range []string{"", "   ", "just = \"noise\"\n"} {
		if err := RewriteDeployment(w.p, "redis", bad); err == nil {
			t.Errorf("should reject %q", bad)
		}
	}
	if err := RewriteDeployment(w.p, "ghost-app", "app = \"x\"\n"); err == nil {
		t.Error("editing a nonexistent deployment should fail")
	}
}

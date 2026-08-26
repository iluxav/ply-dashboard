package plystate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListToleratesUnknownAndBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	proc := filepath.Join(dir, "proc")
	os.MkdirAll(state, 0o755)
	os.MkdirAll(filepath.Join(proc, "4242"), 0o755) // pid 4242 "alive"

	// a healthy record with an unknown future field
	os.WriteFile(filepath.Join(state, "web.1.json"), []byte(`{
		"app":"web","n":1,"pid":4242,"ip":"10.77.0.2","ports":{"http":3000},
		"image":"/srv/web/web-1.2.3-linux-x64.img","started":1000,"restarts":2,
		"domains":["web.example.com"],"some_future_field":{"x":1}}`), 0o644)
	// a dead one
	os.WriteFile(filepath.Join(state, "web.2.json"), []byte(`{
		"app":"web","n":2,"pid":99999999,"image":"/srv/web/web-1.2.3-linux-x64.img"}`), 0o644)
	// garbage must be skipped, not fatal
	os.WriteFile(filepath.Join(state, "junk.json"), []byte("{nope"), 0o644)
	os.WriteFile(filepath.Join(state, "notjson.txt"), []byte("hi"), 0o644)

	got, err := List(Paths{State: state, Proc: proc})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 instances, got %d", len(got))
	}
	if !got[0].Alive || got[1].Alive {
		t.Fatalf("aliveness wrong: %+v", got)
	}
	if got[0].ImageVersion() != "1.2.3" {
		t.Fatalf("version: %s", got[0].ImageVersion())
	}

	apps := Apps(got)
	if len(apps) != 1 || apps[0].Health() != "degraded" || apps[0].Live() != 1 {
		t.Fatalf("apps: %+v", apps)
	}
	if apps[0].Restarts() != 2 {
		t.Fatalf("restarts: %d", apps[0].Restarts())
	}
	if apps[0].Domains()[0] != "web.example.com" {
		t.Fatalf("domains: %v", apps[0].Domains())
	}
}

func TestMissingStateDirIsEmptyNotError(t *testing.T) {
	got, err := List(Paths{State: "/nonexistent/state", Proc: "/proc"})
	if err != nil || got != nil {
		t.Fatalf("want empty+nil, got %v %v", got, err)
	}
}

func TestSparkShapes(t *testing.T) {
	if s := spark([]float64{0, 50, 100}, 100); s != "▁▄█" {
		t.Fatalf("spark: %q", s)
	}
	if s := spark(nil, 0); s != "" {
		t.Fatalf("empty: %q", s)
	}
	if HumanBytes(1536) != "1.5 KiB" {
		t.Fatal(HumanBytes(1536))
	}
}

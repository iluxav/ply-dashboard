package plystate

import (
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

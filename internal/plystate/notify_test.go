package plystate

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNotifyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := Paths{Config: dir}
	if !NotifyWritable(p) {
		t.Fatal("temp dir should be writable")
	}
	// Empty to start.
	if c := LoadNotify(p); len(c.On) != 0 || len(c.To) != 0 {
		t.Fatalf("fresh config not empty: %+v", c)
	}
	// Compose from the form's three inputs, save, reload.
	to := ComposeTo("123:ABC", "@chan", "discord:https://d/x\nhttps://hook\n\n  enc:v1:z  ")
	want := []string{"telegram:123:ABC:@chan", "discord:https://d/x", "https://hook", "enc:v1:z"}
	if len(to) != len(want) {
		t.Fatalf("ComposeTo = %v", to)
	}
	for i := range want {
		if to[i] != want[i] {
			t.Fatalf("ComposeTo[%d] = %q, want %q", i, to[i], want[i])
		}
	}
	if err := SaveNotify(p, NotifyConfig{On: []string{"deploy-failed", "restart-loop"}, To: to}); err != nil {
		t.Fatal(err)
	}
	got := LoadNotify(p)
	if !got.Has("deploy-failed") || !got.Has("restart-loop") || got.Has("scale") {
		t.Fatalf("On round-trip: %v", got.On)
	}
	// The form-split helpers reverse the compose.
	token, chat := got.Telegram()
	if token != "123:ABC" || chat != "@chan" {
		t.Fatalf("Telegram() = %q %q", token, chat)
	}
	if got.Others() != "discord:https://d/x\nhttps://hook\nenc:v1:z" {
		t.Fatalf("Others() = %q", got.Others())
	}
}

func TestTestDeliver(t *testing.T) {
	var gotBody string
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		w.WriteHeader(200)
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer bad.Close()

	if err := TestDeliver(ok.URL, "hi"); err != nil {
		t.Fatalf("webhook ok: %v", err)
	}
	if gotBody != `"hi"` {
		t.Fatalf("webhook body = %s", gotBody)
	}
	if err := TestDeliver("discord:"+ok.URL, "hi"); err != nil {
		t.Fatalf("discord ok: %v", err)
	}
	if gotBody != `{"content":"hi"}` {
		t.Fatalf("discord body = %s", gotBody)
	}
	if err := TestDeliver(bad.URL, "hi"); err == nil {
		t.Fatal("500 should be an error")
	}
	// command: and sealed are refused from the dashboard, with a hint.
	if err := TestDeliver("command:/bin/true", "hi"); err == nil {
		t.Fatal("command should be refused here")
	}
	if err := TestDeliver("enc:v1:abc", "hi"); err == nil {
		t.Fatal("sealed should be refused here")
	}
}

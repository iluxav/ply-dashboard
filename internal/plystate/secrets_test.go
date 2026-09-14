package plystate

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestSecretStoreRoundTrip(t *testing.T) {
	p := Paths{Deployments: t.TempDir()}
	if err := SetSecret(p, "rtrtrtr", "api", "STRIPE_KEY", "sk_live_x"); err != nil {
		t.Fatal(err)
	}
	// frozen contract: <Deployments>/.secrets/<dep>/<member>.<KEY>, 0600, value+"\n"
	path := filepath.Join(p.Deployments, ".secrets", "rtrtrtr", "api.STRIPE_KEY")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "sk_live_x\n" {
		t.Fatalf("content = %q, want %q", b, "sk_live_x\n")
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o, want 700", di.Mode().Perm())
	}
	// no temp file left behind
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("expected exactly one file, got %d", len(entries))
	}

	if !HasSecret(p, "rtrtrtr", "api", "STRIPE_KEY") {
		t.Fatal("HasSecret false after set")
	}

	// SecretNames lists member.KEY, skipping the env/ subdir reconcile owns
	if err := os.MkdirAll(filepath.Join(p.Deployments, ".secrets", "rtrtrtr", "env"), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(p.Deployments, ".secrets", "rtrtrtr", "env", "api.env"), []byte("x"), 0o600)
	if err := SetSecret(p, "rtrtrtr", "web", "SENTRY_DSN", "https://x"); err != nil {
		t.Fatal(err)
	}
	names, err := SecretNames(p, "rtrtrtr")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "api.STRIPE_KEY" || names[1] != "web.SENTRY_DSN" {
		t.Fatalf("SecretNames = %#v (env/ subdir must be skipped)", names)
	}

	// RemoveSecret deletes + idempotent
	if err := RemoveSecret(p, "rtrtrtr", "api", "STRIPE_KEY"); err != nil {
		t.Fatal(err)
	}
	if HasSecret(p, "rtrtrtr", "api", "STRIPE_KEY") {
		t.Fatal("HasSecret true after remove")
	}
	if err := RemoveSecret(p, "rtrtrtr", "api", "STRIPE_KEY"); err != nil {
		t.Fatalf("remove of missing should be nil, got %v", err)
	}
}

func TestSecretNamesMissingDir(t *testing.T) {
	p := Paths{Deployments: t.TempDir()}
	names, err := SecretNames(p, "nope")
	if err != nil || len(names) != 0 {
		t.Fatalf("missing dir → empty, no error; got %#v, %v", names, err)
	}
}

func TestSetSecretRejectsBadNames(t *testing.T) {
	p := Paths{Deployments: t.TempDir()}
	for _, tc := range []struct{ dep, member, key string }{
		{"../evil", "api", "K"},
		{"dep", "..", "K"},
		{"dep", "api", "BAD-KEY"},  // hyphen not allowed in env name
		{"dep", "api", "9LEADING"}, // leading digit
		{"dep", "api", "a/b"},      // traversal
		{"dep", "api", ""},         // empty
	} {
		if err := SetSecret(p, tc.dep, tc.member, tc.key, "v"); err == nil {
			t.Errorf("SetSecret(%q,%q,%q) should be rejected", tc.dep, tc.member, tc.key)
		}
	}
}

func TestRemoveDeploymentSecrets(t *testing.T) {
	p := Paths{Deployments: t.TempDir()}
	SetSecret(p, "one", "api", "K", "v")
	SetSecret(p, "two", "api", "K", "v")
	if err := RemoveDeploymentSecrets(p, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.Deployments, ".secrets", "one")); !os.IsNotExist(err) {
		t.Fatal("one/.secrets should be gone")
	}
	if !HasSecret(p, "two", "api", "K") {
		t.Fatal("deleting one must not touch two")
	}
	// idempotent
	if err := RemoveDeploymentSecrets(p, "one"); err != nil {
		t.Fatal(err)
	}
}

func TestRenameMemberSecrets(t *testing.T) {
	p := Paths{Deployments: t.TempDir()}
	SetSecret(p, "dep", "old", "STRIPE_KEY", "sk")
	SetSecret(p, "dep", "old", "SENTRY_DSN", "dsn")
	SetSecret(p, "dep", "other", "K", "v")
	if err := RenameMemberSecrets(p, "dep", "old", "new"); err != nil {
		t.Fatal(err)
	}
	if HasSecret(p, "dep", "old", "STRIPE_KEY") {
		t.Fatal("old.STRIPE_KEY should be gone")
	}
	if !HasSecret(p, "dep", "new", "STRIPE_KEY") || !HasSecret(p, "dep", "new", "SENTRY_DSN") {
		t.Fatal("secrets should have moved to new.*")
	}
	if !HasSecret(p, "dep", "other", "K") {
		t.Fatal("unrelated member untouched")
	}
}

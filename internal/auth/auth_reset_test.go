package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Deleting auth.json must reset to setup on the NEXT request, no restart.
func TestDeleteFileResetsWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	a, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !a.NeedsSetup() {
		t.Fatal("fresh dir should need setup")
	}
	if err := a.Setup(a.setupToken, "admin", "hunter2!"); err != nil {
		t.Fatal(err)
	}
	if a.NeedsSetup() {
		t.Fatal("configured now")
	}
	cookie, err := a.Login("1.2.3.4:1", "admin", "hunter2!")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	if !a.Valid(r) {
		t.Fatal("fresh session should be valid")
	}

	// Same live Auth (no restart): delete the file.
	if err := os.Remove(filepath.Join(dir, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if !a.NeedsSetup() {
		t.Fatal("deleting the file should reset to setup without a restart")
	}
	if a.setupToken == "" {
		t.Fatal("a fresh setup token should be minted")
	}
	if a.Valid(r) {
		t.Fatal("the old session must not survive a reset")
	}
	// A new account can be created with the new token.
	if err := a.Setup(a.setupToken, "admin2", "newpass9!"); err != nil {
		t.Fatalf("re-setup: %v", err)
	}
}

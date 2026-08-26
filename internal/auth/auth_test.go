package auth

import (
	"net/http"
	"testing"
)

func TestSetupTokenGatesAccountCreation(t *testing.T) {
	a, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !a.NeedsSetup() {
		t.Fatal("fresh dir must need setup")
	}
	if err := a.Setup("wrong-token", "iluxa", "password123"); err == nil {
		t.Fatal("wrong token must be rejected")
	}
	if err := a.Setup(a.setupToken, "iluxa", "short"); err == nil {
		t.Fatal("short password must be rejected")
	}
	if err := a.Setup(a.setupToken, "iluxa", "password123"); err != nil {
		t.Fatal(err)
	}
	if a.NeedsSetup() {
		t.Fatal("configured now")
	}
}

func TestLoginSessionsAndPersistence(t *testing.T) {
	dir := t.TempDir()
	a, _ := Load(dir)
	a.Setup(a.setupToken, "iluxa", "password123")

	if _, err := a.Login("1.2.3.4:5", "iluxa", "nope-nope"); err == nil {
		t.Fatal("wrong password must fail")
	}
	cookie, err := a.Login("1.2.3.4:5", "iluxa", "password123")
	if err != nil {
		t.Fatal(err)
	}

	r, _ := http.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	if !a.Valid(r) {
		t.Fatal("fresh session must validate")
	}
	tampered, _ := http.NewRequest("GET", "/", nil)
	tampered.AddCookie(&http.Cookie{Name: sessionCookie, Value: "9999999999.forged"})
	if a.Valid(tampered) {
		t.Fatal("forged session must fail")
	}

	// the auth file survives a restart (deploy roll)
	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if b.NeedsSetup() {
		t.Fatal("reloaded auth must keep the account")
	}
	if !b.Valid(r) {
		t.Fatal("session key persisted — old session still valid")
	}
}

func TestLoginRateLimit(t *testing.T) {
	a, _ := Load(t.TempDir())
	a.Setup(a.setupToken, "iluxa", "password123")
	for i := 0; i < 5; i++ {
		a.Login("7.7.7.7:1", "iluxa", "wrong-wrong")
	}
	if _, err := a.Login("7.7.7.7:1", "iluxa", "password123"); err == nil {
		t.Fatal("6th attempt in a minute must be limited, even with the right password")
	}
	// a different IP is unaffected
	if _, err := a.Login("8.8.8.8:1", "iluxa", "password123"); err != nil {
		t.Fatal("other IPs must not be limited")
	}
}

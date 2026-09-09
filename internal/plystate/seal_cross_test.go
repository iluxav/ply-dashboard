package plystate

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"testing"

	"golang.org/x/crypto/curve25519"
)

// TestSealForRust seals a known value for a host public key given in the
// environment and writes the blob to a file, so a Rust `ply` on the host
// can prove it opens it — the cross-language check the two formats agree.
// A no-op unless PLY_HOST_PUB is set, so `go test` stays hermetic.
func TestSealForRust(t *testing.T) {
	pub := os.Getenv("PLY_HOST_PUB")
	out := os.Getenv("PLY_SEAL_OUT")
	if pub == "" || out == "" {
		t.Skip("set PLY_HOST_PUB and PLY_SEAL_OUT to emit a cross-language fixture")
	}
	blob, err := Seal("GREETING", "sealed-by-go", pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte(blob), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSealShape checks the local invariants without a host's private key —
// seal with a genuine public key (not a low-order point, which X25519
// rejects), and confirm the shape and that a non-ply key is refused.
func TestSealShape(t *testing.T) {
	var sk [32]byte
	_, _ = rand.Read(sk[:])
	pub, err := curve25519.X25519(sk[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	key := "ply-host-" + base64.StdEncoding.EncodeToString(pub)
	blob, err := Seal("K", "v", key)
	if err != nil {
		t.Fatalf("seal with a valid pub: %v", err)
	}
	if !IsSealed(blob) || blob[:7] != "enc:v1:" {
		t.Fatalf("bad shape: %q", blob)
	}
	// Two seals differ (fresh ephemeral each time).
	if b2, _ := Seal("K", "v", key); b2 == blob {
		t.Fatal("seals must not repeat")
	}
	if _, err := Seal("K", "v", "age1nope"); err == nil {
		t.Fatal("a non-ply key should be refused")
	}
}

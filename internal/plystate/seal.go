package plystate

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// Sealing, the same construction as ply-core/src/sealed.rs, so a value
// sealed here opens on the host at launch. It uses ONLY the host's public
// key — the private key never touches the dashboard, which is what makes a
// web-facing seal safe. The dashboard can create sealed values; it can
// never read one back.
//
//	enc:v1:base64( eph_pub(32) || nonce(12) || chacha20poly1305(ct) )
//	key   = HKDF-SHA256(shared, salt = eph_pub||recipient, info)
//	shared= X25519(eph, recipient) ; AAD = the variable name
//
// The formats are a tiny, fixed contract; the cross-language test is a
// value sealed here and opened by `ply run` on the host.

const (
	sealPrefix   = "enc:v1:"
	pubPrefix    = "ply-host-"
	sealInfo     = "ply sealed env v1"
	sealNonceLen = 12
)

// HostPub reads the host public key the dashboard seals for, written by
// `ply setup` into the granted config dir. "" if absent.
func HostPub(p Paths) string {
	if p.Config == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(p.Config, "host.pub"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// SealAvailable reports whether a host public key is present to seal with.
func SealAvailable(p Paths) bool { return HostPub(p) != "" }

func parsePub(pub string) ([]byte, error) {
	body, ok := strings.CutPrefix(strings.TrimSpace(pub), pubPrefix)
	if !ok {
		return nil, fmt.Errorf("not a host key (expected %s…)", pubPrefix)
	}
	b, err := base64.StdEncoding.DecodeString(body)
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("host key is not 32 bytes of base64")
	}
	return b, nil
}

// Seal encrypts value under name for the given host public key, producing
// an enc:v1: string. `name` binds the value to the variable it will be —
// it is the AEAD's associated data, so a sealed value opens only under the
// name it was sealed for, exactly as the Rust side requires.
func Seal(name, value, hostPub string) (string, error) {
	recipient, err := parsePub(hostPub)
	if err != nil {
		return "", err
	}
	var eph [32]byte
	if _, err := io.ReadFull(rand.Reader, eph[:]); err != nil {
		return "", err
	}
	ephPub, err := curve25519.X25519(eph[:], curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	shared, err := curve25519.X25519(eph[:], recipient)
	if err != nil {
		return "", err
	}
	salt := append(append([]byte{}, ephPub...), recipient...)
	okm := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, salt, []byte(sealInfo)), okm); err != nil {
		return "", err
	}
	aead, err := chacha20poly1305.New(okm)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, sealNonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := aead.Seal(nil, nonce, []byte(value), []byte(name))
	out := append(append(append([]byte{}, ephPub...), nonce...), ct...)
	return sealPrefix + base64.StdEncoding.EncodeToString(out), nil
}

// IsSealed reports whether a value is already a sealed blob.
func IsSealed(v string) bool { return strings.HasPrefix(v, "enc:") }

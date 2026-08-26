// Package auth: no database, race-closed first boot.
//
// First boot (no auth file): a random setup token is printed to stdout —
// visible via `ply logs` / journalctl only to someone who already controls
// the host — and the create-account page requires it. This closes the
// first-visitor-owns-the-box race a bare setup page would open.
//
// The account lives in one JSON file (argon2id hash + HMAC session key) in
// the app's data volume, so it survives deploys. Sessions are signed
// cookies; deleting the file is the documented password reset — the
// filesystem is the admin API.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	sessionCookie = "ply_dash_session"
	sessionTTL    = 7 * 24 * time.Hour
)

type record struct {
	User       string `json:"user"`
	Salt       string `json:"salt"`
	Hash       string `json:"argon2id_hash"`
	SessionKey string `json:"session_key"`
}

type Auth struct {
	path string

	mu         sync.Mutex
	rec        *record
	setupToken string
	attempts   map[string][]time.Time // login rate limit per IP
}

// Load reads the auth file; when absent, mints the setup token and prints it.
func Load(dir string) (*Auth, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	a := &Auth{path: filepath.Join(dir, "auth.json"), attempts: map[string][]time.Time{}}
	raw, err := os.ReadFile(a.path)
	switch {
	case err == nil:
		var rec record
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, fmt.Errorf("%s: %w (delete it to reset)", a.path, err)
		}
		a.rec = &rec
	case os.IsNotExist(err):
		a.setupToken = randomToken(16)
		fmt.Printf("ply-dashboard: first boot — create the account with setup token: %s\n", a.setupToken)
	default:
		return nil, err
	}
	return a, nil
}

func (a *Auth) NeedsSetup() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rec == nil
}

// Setup creates the single account; the printed token gates it.
func (a *Auth) Setup(token, user, password string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rec != nil {
		return fmt.Errorf("already configured")
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(a.setupToken)) != 1 {
		return fmt.Errorf("wrong setup token — it was printed to the dashboard's log on first boot")
	}
	if len(user) < 1 || len(password) < 8 {
		return fmt.Errorf("username required; password must be at least 8 characters")
	}
	salt := randomBytes(16)
	rec := record{
		User:       user,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Hash:       base64.StdEncoding.EncodeToString(hash(password, salt)),
		SessionKey: randomToken(32),
	}
	raw, err := json.MarshalIndent(rec, "", " ")
	if err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.path); err != nil {
		return err
	}
	a.rec = &rec
	return nil
}

// Seed writes the account file directly — mock/dev bootstrap, not a login
// path. No-op when an account already exists, so sessions survive restarts.
func Seed(dir, user, password string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "auth.json")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	salt := randomBytes(16)
	rec := record{
		User:       user,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Hash:       base64.StdEncoding.EncodeToString(hash(password, salt)),
		SessionKey: randomToken(32),
	}
	raw, err := json.MarshalIndent(rec, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// Login checks credentials (rate-limited) and returns a session cookie value.
func (a *Auth) Login(remoteAddr, user, password string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ip := ipOf(remoteAddr)
	now := time.Now()
	recent := a.attempts[ip][:0]
	for _, t := range a.attempts[ip] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 5 {
		a.attempts[ip] = recent
		return "", fmt.Errorf("too many attempts — wait a minute")
	}
	a.attempts[ip] = append(recent, now)

	if a.rec == nil {
		return "", fmt.Errorf("not configured")
	}
	salt, _ := base64.StdEncoding.DecodeString(a.rec.Salt)
	want, _ := base64.StdEncoding.DecodeString(a.rec.Hash)
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(a.rec.User)) == 1
	passOK := subtle.ConstantTimeCompare(hash(password, salt), want) == 1
	if !userOK || !passOK {
		return "", fmt.Errorf("wrong username or password")
	}
	return a.sign(now.Add(sessionTTL)), nil
}

// Valid reports whether the request carries a live session.
func (a *Auth) Valid(r *http.Request) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rec == nil {
		return false
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(parts[1]), []byte(a.mac(parts[0])))
}

func (a *Auth) SetCookie(w http.ResponseWriter, value string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *Auth) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
}

func (a *Auth) sign(expiry time.Time) string {
	exp := strconv.FormatInt(expiry.Unix(), 10)
	return exp + "." + a.mac(exp)
}

func (a *Auth) mac(msg string) string {
	m := hmac.New(sha256.New, []byte(a.rec.SessionKey))
	m.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func hash(password string, salt []byte) []byte {
	// argon2id, OWASP-recommended interactive parameters
	return argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // no entropy = nothing sane to do
	}
	return b
}

func randomToken(n int) string {
	return base64.RawURLEncoding.EncodeToString(randomBytes(n))
}

func ipOf(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

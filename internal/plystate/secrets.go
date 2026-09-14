package plystate

// Operator secrets: a service's runtime secret values, kept host-only. A
// deployment/member declares the KEYS via `secret_env = ["KEY", …]` in its
// spec; the VALUES live here, in `<deployments>/.secrets/<dep>/<member>.<KEY>`
// — a 0600 file in a systemd-watch-invisible dir, never in the spec, the unit,
// or git. This is the exact on-disk contract ply-core's SecretStore::set
// writes and `ply reconcile` reads: value + one trailing newline, 0600 file /
// 0700 dir, atomic temp+rename. The dashboard writes these files directly (it
// already manages the deployments dir daemonlessly) and never shells `ply`.
//
// The dashboard can SET, LIST NAMES, and REMOVE secrets. It never reads a
// value back to the browser.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/iluxav/ply-dashboard/internal/cart"
)

// storeName is a deployment or member name: the same [a-z0-9-] shape ply uses,
// so a name can never escape the store dir.
var storeName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// envKey is an environment-variable name: letters/digits/underscore, not
// starting with a digit — matches ply-core's is_env_name.
var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// secretsDir is the per-deployment secret store dir. `env/` beneath it is
// reconcile's own subdir (the rendered member env files) and is NOT part of
// the store namespace.
func secretsDir(p Paths, dep string) string {
	return filepath.Join(p.Deployments, ".secrets", dep)
}

func secretPath(p Paths, dep, member, key string) (string, error) {
	if !storeName.MatchString(dep) {
		return "", fmt.Errorf("bad deployment name %q", dep)
	}
	if !storeName.MatchString(member) {
		return "", fmt.Errorf("bad service name %q", member)
	}
	if !envKey.MatchString(key) {
		return "", fmt.Errorf("%q is not an environment variable name (letters, digits, underscore; not starting with a digit)", key)
	}
	return filepath.Join(secretsDir(p, dep), member+"."+key), nil
}

// SetSecret writes a secret value for <dep>/<member>.<key> per the frozen
// contract: 0700 dir, 0600 file, value + one trailing "\n", atomic
// temp+rename. The value is never logged.
func SetSecret(p Paths, dep, member, key, value string) error {
	path, err := secretPath(p, dep, member, key)
	if err != nil {
		return err
	}
	if value == "" {
		return fmt.Errorf("a secret value is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// tighten in case an ancestor beat us to a laxer mode.
	_ = os.Chmod(dir, 0o700)
	tmp := filepath.Join(dir, "."+member+"."+key+".tmp")
	if err := os.WriteFile(tmp, []byte(value+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// HasSecret reports whether a value is stored for <dep>/<member>.<key>.
func HasSecret(p Paths, dep, member, key string) bool {
	path, err := secretPath(p, dep, member, key)
	if err != nil {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// SecretNames lists the stored secrets for a deployment as "member.KEY",
// sorted implicitly by the dir order (callers sort if needed). The `env/`
// subdirectory (reconcile's rendered member env files) is skipped, and only
// regular files whose name is a valid <member>.<KEY> are returned. A missing
// store dir is empty, not an error.
func SecretNames(p Paths, dep string) ([]string, error) {
	if !storeName.MatchString(dep) {
		return nil, fmt.Errorf("bad deployment name %q", dep)
	}
	entries, err := os.ReadDir(secretsDir(p, dep))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue // skips the env/ subdir
		}
		n := e.Name()
		member, key, ok := strings.Cut(n, ".")
		if !ok || !storeName.MatchString(member) || !envKey.MatchString(key) {
			continue
		}
		names = append(names, n)
	}
	return names, nil
}

// MemberSecretKeys returns just the KEYs stored for one member of a deployment.
func MemberSecretKeys(p Paths, dep, member string) []string {
	names, _ := SecretNames(p, dep)
	var keys []string
	for _, n := range names {
		m, k, ok := strings.Cut(n, ".")
		if ok && m == member {
			keys = append(keys, k)
		}
	}
	return keys
}

// RemoveSecret deletes a stored secret. Missing is success (idempotent).
func RemoveSecret(p Paths, dep, member, key string) error {
	path, err := secretPath(p, dep, member, key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RemoveDeploymentSecrets removes a deployment's entire secret store — the
// "delete + data" companion. Missing is success; it never touches another
// deployment's dir.
func RemoveDeploymentSecrets(p Paths, dep string) error {
	if !storeName.MatchString(dep) {
		return fmt.Errorf("bad deployment name %q", dep)
	}
	return os.RemoveAll(secretsDir(p, dep))
}

// RenameMemberSecrets moves a member's stored secrets when a card is renamed
// (`<old>.KEY` → `<new>.KEY`), so they don't orphan. Best-effort per file: it
// returns the first real IO error but tries every file.
func RenameMemberSecrets(p Paths, dep, oldMember, newMember string) error {
	if oldMember == newMember {
		return nil
	}
	if !storeName.MatchString(dep) || !storeName.MatchString(oldMember) || !storeName.MatchString(newMember) {
		return fmt.Errorf("bad name in rename %q/%q→%q", dep, oldMember, newMember)
	}
	var firstErr error
	for _, key := range MemberSecretKeys(p, dep, oldMember) {
		from, e1 := secretPath(p, dep, oldMember, key)
		to, e2 := secretPath(p, dep, newMember, key)
		if e1 != nil || e2 != nil {
			continue
		}
		if err := os.Rename(from, to); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// --- app-page glue: edit a deployment's secret_env + its store together ------
// The app page (post-deploy) manages secrets for one running service. These
// resolve the deployment that owns `app`, edit the right card's `secret_env`
// (via a lossless cart round-trip, like the domain editor), and write/remove
// the store value with a fresh mtime so reconcile re-converges. The store key
// member matches what ply-core reconcile uses: a composition member is keyed
// by its own name, a single-app deployment by the deployment name.

// appCardIndex finds the card in `c` that IS `app` (single-card → 0), and the
// store member name ply-core reconcile uses for it.
func appCardIndex(c cart.Cart, dep, app string) (idx int, member string, ok bool) {
	if len(c.Cards) == 1 {
		return 0, dep, true // single-app: reconcile keys the store by deployment name
	}
	for i := range c.Cards {
		if c.Cards[i].Name == app {
			return i, app, true // composition member: keyed by its own name
		}
	}
	return 0, "", false
}

func loadDeploymentCart(p Paths, app string) (dep string, c cart.Cart, idx int, member string, err error) {
	dep, ok := DeploymentOf(p, app)
	if !ok {
		return "", cart.Cart{}, 0, "", fmt.Errorf("no deployment owns app %q", app)
	}
	raw, err := os.ReadFile(filepath.Join(p.Deployments, dep+".toml"))
	if err != nil {
		return "", cart.Cart{}, 0, "", err
	}
	c, err = cart.FromTOML(string(raw))
	if err != nil {
		return "", cart.Cart{}, 0, "", fmt.Errorf("parsing deployment %q: %w", dep, err)
	}
	idx, member, ok = appCardIndex(c, dep, app)
	if !ok {
		return "", cart.Cart{}, 0, "", fmt.Errorf("service %q not found in deployment %q", app, dep)
	}
	return dep, c, idx, member, nil
}

// AttachedSecretKeys lists the secret env KEYS the DEPLOYMENT declares for an
// app — the source of truth for the panel (like AttachedDomains). Sorted.
func AttachedSecretKeys(p Paths, app string) []string {
	_, c, idx, _, err := loadDeploymentCart(p, app)
	if err != nil {
		return nil
	}
	keys := append([]string(nil), c.Cards[idx].SecretEnv...)
	sort.Strings(keys)
	return keys
}

// SecretBacked reports whether the app's stored value for `key` exists — a
// declared secret_env key with no store value would fail the next reconcile.
func SecretBacked(p Paths, app, key string) bool {
	dep, _, _, member, err := loadDeploymentCart(p, app)
	if err != nil {
		return false
	}
	return HasSecret(p, dep, member, key)
}

// AddSecretForApp stores a value and declares its key in the app's card
// `secret_env`, writing the deployment back losslessly. A key already used as
// a plain env value is refused (a value can't be public and secret).
func AddSecretForApp(p Paths, app, key, value string) error {
	if !envKey.MatchString(key) {
		return fmt.Errorf("%q is not an environment variable name (letters, digits, underscore; not starting with a digit)", key)
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("a secret value is required")
	}
	dep, c, idx, member, err := loadDeploymentCart(p, app)
	if err != nil {
		return err
	}
	for _, e := range c.Cards[idx].Env {
		if k, _, _ := strings.Cut(e, "="); k == key {
			return fmt.Errorf("%s is already a plain env value — a value can't be public and secret; remove it from env first", key)
		}
	}
	if err := SetSecret(p, dep, member, key, value); err != nil {
		return err
	}
	if !containsStr(c.Cards[idx].SecretEnv, key) {
		c.Cards[idx].SecretEnv = append(c.Cards[idx].SecretEnv, key)
	}
	return writeSpec(p, dep, c.ToTOML())
}

// RemoveSecretForApp drops the key from the app's card `secret_env` and
// removes its stored value.
func RemoveSecretForApp(p Paths, app, key string) error {
	dep, c, idx, member, err := loadDeploymentCart(p, app)
	if err != nil {
		return err
	}
	c.Cards[idx].SecretEnv = withoutStr(c.Cards[idx].SecretEnv, key)
	if err := writeSpec(p, dep, c.ToTOML()); err != nil {
		return err
	}
	return RemoveSecret(p, dep, member, key)
}

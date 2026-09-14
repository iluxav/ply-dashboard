package plystate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/iluxav/ply-dashboard/internal/cart"
)

// A hostname: labels of letters/digits/hyphens, at least one dot, no scheme,
// port, path or whitespace. Deliberately strict — a bad domain is a Caddy
// vhost that never issues a cert.
var hostname = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// DeploymentOf returns the deployment file that owns `app`: a composition
// member (via the .members map), a deployment named after the app, or a
// single-app deployment whose one card resolves to that name.
func DeploymentOf(p Paths, app string) (string, bool) {
	if app == "" {
		return "", false
	}
	if stack, ok := StackMembers(p)[app]; ok {
		return stack, true
	}
	if _, err := os.Stat(filepath.Join(p.Deployments, app+".toml")); err == nil {
		return app, true
	}
	for _, d := range Deployments(p) {
		c, err := cart.FromTOML(d.Spec)
		if err == nil && len(c.Cards) == 1 && c.Cards[0].Name == app {
			return d.Name, true
		}
	}
	return "", false
}

// AddDomain / RemoveDomain attach or detach a domain on the deployment that
// owns `app`, writing the change back with a fresh mtime so reconcile
// re-converges immediately. The edge (ply-proxy) renders the Caddy vhost and
// issues TLS; ply does the rest.
func AddDomain(p Paths, app, domain string) error    { return editDomain(p, app, domain, true) }
func RemoveDomain(p Paths, app, domain string) error { return editDomain(p, app, domain, false) }

// AttachedDomains lists the domains the DEPLOYMENT declares for an app — the
// source of truth for "attached". The running instance only learns a new
// domain after reconcile restarts it, so reading the deployment lets a
// just-attached domain show immediately.
func AttachedDomains(p Paths, app string) []string {
	dep, ok := DeploymentOf(p, app)
	if !ok {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(p.Deployments, dep+".toml"))
	if err != nil {
		return nil
	}
	c, err := cart.FromTOML(string(raw))
	if err != nil {
		return nil
	}
	if len(c.Cards) == 1 {
		return c.Cards[0].Domain
	}
	for _, card := range c.Cards {
		if card.Name == app {
			return card.Domain
		}
	}
	return nil
}

func editDomain(p Paths, app, domain string, add bool) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return fmt.Errorf("no domain given")
	}
	if add && !hostname.MatchString(domain) {
		return fmt.Errorf("%q is not a valid hostname — e.g. app.example.com (no scheme, port or path)", domain)
	}
	dep, ok := DeploymentOf(p, app)
	if !ok {
		return fmt.Errorf("no deployment owns app %q", app)
	}
	raw, err := os.ReadFile(filepath.Join(p.Deployments, dep+".toml"))
	if err != nil {
		return err
	}
	// The cart round-trip preserves un-modeled fields (grant_links, env_file,
	// scale, a member's egress/params, …), so editing a domain never drops
	// anything — no need to refuse.
	c, err := cart.FromTOML(string(raw))
	if err != nil {
		return fmt.Errorf("parsing deployment %q: %w", dep, err)
	}
	idx := -1
	if len(c.Cards) == 1 {
		idx = 0
	} else {
		for i := range c.Cards {
			if c.Cards[i].Name == app {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return fmt.Errorf("service %q not found in deployment %q", app, dep)
	}
	if add {
		if !containsStr(c.Cards[idx].Domain, domain) {
			c.Cards[idx].Domain = append(c.Cards[idx].Domain, domain)
		}
	} else {
		c.Cards[idx].Domain = withoutStr(c.Cards[idx].Domain, domain)
	}
	return writeSpec(p, dep, c.ToTOML())
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func withoutStr(list []string, s string) []string {
	out := make([]string, 0, len(list))
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

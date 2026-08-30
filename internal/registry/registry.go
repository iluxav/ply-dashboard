// Package registry reads the ply registry's catalog — the same state.json
// `ply search` reads. The ROOT catalog, so a user's own published apps are
// installable from their dashboard, not just the official `apps` shelf.
package registry

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

const rootStateURL = "https://registry.plybox.sh/state.json"

// The official shelf of runnable apps. Its packages are referenced bare
// (`postgres`), which is also what ply resolves a bare name to.
const officialNS = "apps"

type App struct {
	Owner     string
	Namespace string
	// Ref is what a deployment's `app =` must say: bare for the official
	// shelf, `<namespace>/<name>` for everything else. ply resolves a bare
	// name against apps/ only, so an unqualified user package 404s.
	Ref         string
	Name        string
	Type        string
	Description string
	Contract    string   // env lines to prefill on deploy
	Publish     string   // suggested --publish
	GrantLinks  bool     // the app needs its [requests] links granted
	Origin      string   // phantom packages: where the bytes actually live
	Versions    []string // newest first, deduped across arches
}

type state struct {
	Packages []struct {
		Owner       string `json:"owner"`
		Namespace   string `json:"namespace"`
		Name        string `json:"name"`
		Type        string `json:"type"`
		Description string `json:"description"`
		Contract    string `json:"contract"`
		Publish     string `json:"publish"`
		GrantLinks  bool   `json:"grant_links"`
		Origin      string `json:"origin"`
		Versions    []struct {
			Version string `json:"version"`
		} `json:"versions"`
	} `json:"packages"`
}

// Client caches the catalog for five minutes — the dashboard must not
// hammer the registry on every page load, and stale-by-minutes is fine.
type Client struct {
	url string

	mu      sync.Mutex
	apps    []App
	fetched time.Time
	lastErr error
}

func NewClient() *Client { return &Client{url: rootStateURL} }

func (c *Client) Apps() ([]App, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.fetched) < 5*time.Minute && (c.apps != nil || c.lastErr != nil) {
		return c.apps, c.lastErr
	}
	c.apps, c.lastErr = fetch(c.url)
	c.fetched = time.Now()
	return c.apps, c.lastErr
}

func fetch(url string) ([]App, error) {
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var s state
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, err
	}
	var apps []App
	for _, p := range s.Packages {
		seen := map[string]bool{}
		var versions []string
		for _, v := range p.Versions {
			if !seen[v.Version] {
				seen[v.Version] = true
				versions = append(versions, v.Version)
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(versions)))
		if p.Type != "" && p.Type != "app" {
			continue // layers and stacks are not one-click installs (yet)
		}
		ref := p.Name
		if p.Namespace != "" && p.Namespace != officialNS {
			ref = p.Namespace + "/" + p.Name
		}
		app := App{
			Owner:       p.Owner,
			Namespace:   p.Namespace,
			Ref:         ref,
			Name:        p.Name,
			Type:        p.Type,
			Description: p.Description,
			Contract:    p.Contract,
			Publish:     p.Publish,
			GrantLinks:  p.GrantLinks,
			Origin:      p.Origin,
			Versions:    versions,
		}
		// catalog wins; the compiled-in table is only a fallback for
		// registries that predate metadata v2
		if app.Contract == "" {
			app.Contract = Contract(p.Name)
		}
		if app.Publish == "" {
			app.Publish = DefaultPublish(p.Name)
		}
		apps = append(apps, app)
	}
	// Official apps lead — they are the ones most installs want, and a
	// stranger's package must not outrank postgres on alphabetical luck.
	sort.Slice(apps, func(a, b int) bool {
		x, y := apps[a], apps[b]
		if (x.Namespace == officialNS) != (y.Namespace == officialNS) {
			return x.Namespace == officialNS
		}
		if x.Namespace != y.Namespace {
			return x.Namespace < y.Namespace
		}
		return x.Name < y.Name
	})
	return apps, nil
}

// Contract: the env vars each known service understands, prefilled in the
// deploy form so nobody has to remember them.
func Contract(app string) string {
	switch app {
	case "postgres":
		return "POSTGRES_PASSWORD=\nPOSTGRES_DB=\nPGPORT=5432"
	case "redis":
		return "REDIS_PASSWORD=\nREDIS_PORT=6379"
	case "notify":
		// needs grant_links = true in the spec (it reads the events journal)
		return "WEBHOOK_URL=\nNOTIFY_EVENTS=deploy,deploy-failed,instance-restart"
	default:
		return ""
	}
}

// DefaultPublish suggests the conventional internal port.
func DefaultPublish(app string) string {
	switch app {
	case "postgres":
		return "internal:5432"
	case "redis":
		return "internal:6379"
	default:
		return "internal:8080"
	}
}

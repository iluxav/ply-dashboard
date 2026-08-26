// Package registry reads the ply registry's apps catalog — the same
// state.json `ply search` reads, scoped to the runnable-apps namespace.
package registry

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

const appsStateURL = "https://registry.plybox.sh/apps/state.json"

type App struct {
	Name        string
	Description string
	Versions    []string // newest first, deduped across arches
}

type state struct {
	Packages []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
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

func NewClient() *Client { return &Client{url: appsStateURL} }

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
		apps = append(apps, App{Name: p.Name, Description: p.Description, Versions: versions})
	}
	sort.Slice(apps, func(a, b int) bool { return apps[a].Name < apps[b].Name })
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

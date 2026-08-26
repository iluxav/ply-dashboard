// Package plystate reads ply's on-disk runtime state — the same files the
// ply CLI reads. The state-file schema is a semi-public, additive-only
// contract: unknown fields are ignored, missing fields default, so the
// dashboard tolerates both older and newer ply versions.
package plystate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Paths locates the four read surfaces. In a container they are the
// explicit --link grants under /ply/host; on a bare host (dev) they are
// probed from ply's own conventions.
type Paths struct {
	State       string // instance state JSONs
	Logs        string // per-instance log rings (<app>.<n>.log)
	Apps        string // app dirs (deploy pointers, control/)
	Deployments string // declarative deployments (a deployment is a file)
	Cgroup      string // cgroup v2 root
	Proc        string // host procfs (aliveness, rootless stats fallback)
}

// Resolve prefers the container grants; on a bare host, `PLY_STATE_DIR`
// wins, else whichever of rootful/rootless has the most recent state file —
// a dev box often has a stale rootful dir from old sudo runs beside the
// live rootless one.
func Resolve() Paths {
	if dirExists("/ply/host/run/state") {
		return Paths{
			State:       "/ply/host/run/state",
			Logs:        "/ply/host/run/logs",
			Apps:        "/ply/host/apps",
			Deployments: "/ply/host/deployments",
			Cgroup:      "/ply/host/cgroup",
			Proc:        "/ply/host/proc",
		}
	}
	rootful := Paths{
		State:       "/run/ply/state",
		Logs:        "/run/ply/logs",
		Apps:        "/var/lib/ply/apps",
		Deployments: "/var/lib/ply/deployments",
		Cgroup:      "/sys/fs/cgroup",
		Proc:        "/proc",
	}
	run := os.Getenv("XDG_RUNTIME_DIR")
	if run == "" {
		run = fmt.Sprintf("/tmp/ply-%d", os.Getuid())
	} else {
		run = filepath.Join(run, "ply")
	}
	home, _ := os.UserHomeDir()
	rootless := Paths{
		State:       filepath.Join(run, "state"),
		Logs:        filepath.Join(run, "logs"),
		Apps:        filepath.Join(home, ".local/share/ply/apps"),
		Deployments: filepath.Join(home, ".local/share/ply/deployments"),
		Cgroup:      "/sys/fs/cgroup",
		Proc:        "/proc",
	}
	if dir := os.Getenv("PLY_STATE_DIR"); dir != "" {
		p := rootful
		p.State = dir
		p.Logs = filepath.Join(filepath.Dir(dir), "logs")
		return p
	}
	if newestState(rootful.State).After(newestState(rootless.State)) {
		return rootful
	}
	if dirExists(rootless.State) || !dirExists(rootful.State) {
		return rootless
	}
	return rootful
}

func newestState(dir string) time.Time {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}
	}
	var newest time.Time
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// Instance mirrors ply's InstanceState. Additive-only contract: decode
// ignores unknown fields, absent fields keep zero values.
type Instance struct {
	App           string            `json:"app"`
	N             uint32            `json:"n"`
	PID           int               `json:"pid"`
	IP            string            `json:"ip"`
	Ports         map[string]uint16 `json:"ports"`
	Image         string            `json:"image"`
	Started       int64             `json:"started"`
	Restarts      uint32            `json:"restarts"`
	HealthPort    *uint16           `json:"health_port"`
	PublishedPort *uint16           `json:"published_port"`
	PublishedAddr string            `json:"published_addr"`
	Domains       []string          `json:"domains"`

	Alive bool `json:"-"`
}

func (i Instance) Name() string { return fmt.Sprintf("%s.%d", i.App, i.N) }

func (i Instance) Uptime() time.Duration {
	if i.Started == 0 {
		return 0
	}
	return time.Since(time.Unix(i.Started, 0)).Truncate(time.Second)
}

// UptimeShort renders `ply ps`-style: 90s, 12m, 3h, 2d.
func (i Instance) UptimeShort() string {
	d := i.Uptime()
	switch {
	case d <= 0:
		return "-"
	case d < 2*time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < 2*time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// ImageVersion extracts "0.1.0" from ".../name-0.1.0-linux-x64.img".
func (i Instance) ImageVersion() string {
	base := filepath.Base(i.Image)
	base = strings.TrimSuffix(base, ".img")
	for _, arch := range []string{"-linux-x64", "-linux-arm64"} {
		base = strings.TrimSuffix(base, arch)
	}
	prefix := i.App + "-"
	if strings.HasPrefix(base, prefix) {
		return strings.TrimPrefix(base, prefix)
	}
	// image name may differ from app name (docker imports); best effort
	if idx := strings.LastIndex(base, "-"); idx > 0 {
		return base[idx+1:]
	}
	return base
}

// List reads every state file, marking aliveness via host procfs.
func List(p Paths) ([]Instance, error) {
	entries, err := os.ReadDir(p.State)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no apps ever run: empty, not broken
		}
		return nil, err
	}
	var out []Instance
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(p.State, e.Name()))
		if err != nil {
			continue
		}
		var inst Instance
		if err := json.Unmarshal(raw, &inst); err != nil || inst.App == "" {
			continue
		}
		inst.Alive = dirExists(filepath.Join(p.Proc, fmt.Sprint(inst.PID)))
		out = append(out, inst)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].App != out[b].App {
			return out[a].App < out[b].App
		}
		return out[a].N < out[b].N
	})
	return out, nil
}

// App is the overview row: one app, its live instances aggregated.
type App struct {
	Name      string
	Instances []Instance
}

func (a App) Live() int {
	n := 0
	for _, i := range a.Instances {
		if i.Alive {
			n++
		}
	}
	return n
}

func (a App) Restarts() uint32 {
	var n uint32
	for _, i := range a.Instances {
		n += i.Restarts
	}
	return n
}

func (a App) Oldest() Instance {
	oldest := a.Instances[0]
	for _, i := range a.Instances[1:] {
		if i.Started < oldest.Started {
			oldest = i
		}
	}
	return oldest
}

// Health: green = all alive, amber = some, red = none.
func (a App) Health() string {
	live := a.Live()
	switch {
	case live == len(a.Instances) && live > 0:
		return "ok"
	case live > 0:
		return "degraded"
	default:
		return "down"
	}
}

func (a App) PublishedAddr() string {
	for _, i := range a.Instances {
		if i.PublishedAddr != "" {
			return i.PublishedAddr
		}
	}
	return ""
}

func (a App) Domains() []string {
	for _, i := range a.Instances {
		if len(i.Domains) > 0 {
			return i.Domains
		}
	}
	return nil
}

// Apps groups instances (dead ones with a live sibling included, fully-dead
// apps only if their state files linger — honest about what ply ps shows).
func Apps(instances []Instance) []App {
	byName := map[string][]Instance{}
	for _, i := range instances {
		byName[i.App] = append(byName[i.App], i)
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]App, 0, len(names))
	for _, n := range names {
		out = append(out, App{Name: n, Instances: byName[n]})
	}
	return out
}

// LogTail returns the last `lines` lines of an instance's ring
// (rotation-aware: `.1` first, then the live file).
func LogTail(p Paths, app string, n uint32, lines int) []string {
	live := filepath.Join(p.Logs, fmt.Sprintf("%s.%d.log", app, n))
	var text strings.Builder
	if prev, err := os.ReadFile(live + ".1"); err == nil {
		text.Write(prev)
	}
	if cur, err := os.ReadFile(live); err == nil {
		text.Write(cur)
	}
	all := strings.Split(strings.TrimRight(text.String(), "\n"), "\n")
	if len(all) == 1 && all[0] == "" {
		return nil
	}
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return all
}

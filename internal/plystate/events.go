package plystate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Events: the append-only journal ply's actors write (reconcile, run
// parents) — deploys, scales, restarts, crash respawns. One JSON line per
// event in <apps>/events.log, ring-rotated with a `.1` sibling.

type Event struct {
	TS     int64  `json:"ts"`
	App    string `json:"app"`
	Event  string `json:"event"`
	Detail string `json:"detail"`
}

// Age renders `ply ps`-style relative time: 45s, 12m, 3h, 2d.
func (e Event) Age() string {
	d := time.Since(time.Unix(e.TS, 0)).Truncate(time.Second)
	switch {
	case d < 0:
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

// Events returns the newest events first, optionally filtered to one app.
// Rotation-aware: `.1` holds the older half.
func Events(p Paths, app string, limit int) []Event {
	base := filepath.Join(p.Apps, "events.log")
	var out []Event
	for _, path := range []string{base + ".1", base} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if line == "" {
				continue
			}
			var e Event
			if json.Unmarshal([]byte(line), &e) != nil || e.Event == "" {
				continue
			}
			if app != "" && e.App != app {
				continue
			}
			out = append(out, e)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	// newest first for the panel
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// LogTarget names the app whose log ring explains this event — failures
// that happened inside a builder point at the builder's ring, which
// outlives the builder itself (the post-mortem is a file).
func (e Event) LogTarget() string {
	if strings.Contains(e.Detail, e.App+"-builder") {
		return e.App + "-builder"
	}
	return e.App
}

// RollbackTarget extracts what a past deploy event can be pinned back to:
// the commit for repo-lane builds, the image version otherwise.
// (kind, value); empty kind = not a rollback candidate.
func (e Event) RollbackTarget() (string, string) {
	if e.Event != "deploy" {
		return "", ""
	}
	if m := deployedCommit.FindStringSubmatch(e.Detail); m != nil {
		return "ref", m[1]
	}
	if m := deployedVersion.FindStringSubmatch(e.Detail); m != nil {
		return "version", m[1]
	}
	return "", ""
}

// Template-friendly halves of RollbackTarget (templates want one value).
func (e Event) RollbackKey() string   { k, _ := e.RollbackTarget(); return k }
func (e Event) RollbackValue() string { _, v := e.RollbackTarget(); return v }

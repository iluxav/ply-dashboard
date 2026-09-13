package plystate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Volume is one row of ply's volume inventory. The dashboard is not granted
// the volumes dir, so reconcile publishes this to
// `deployments/.status/volumes.json` each beat; the deploy page reads it to
// surface orphaned data an operator can reclaim.
type Volume struct {
	App      string
	Name     string // "<name>.<slot>", e.g. "data.1"
	Status   string // "in use" | "idle" | "orphaned"
	Bytes    int64
	HasBytes bool // false when ply couldn't size it (rootless subuid) — shown "?"
}

// Size renders the human byte size, or "?" when ply couldn't read it.
func (v Volume) Size() string {
	if !v.HasBytes {
		return "?"
	}
	return HumanBytes(uint64(v.Bytes))
}

// ShowName reports whether the volume name is worth showing beside the app —
// the default single volume is `data.<slot>`, which adds nothing.
func (v Volume) ShowName() bool { return !strings.HasPrefix(v.Name, "data.") }

// Volumes reads the inventory reconcile publishes. Missing/empty/garbled →
// nil, never an error (an older ply writes no file; the page just shows none).
func Volumes(p Paths) []Volume {
	raw, err := os.ReadFile(filepath.Join(p.Deployments, ".status", "volumes.json"))
	if err != nil {
		return nil
	}
	var rows []struct {
		App    string `json:"app"`
		Volume string `json:"volume"`
		Status string `json:"status"`
		Bytes  *int64 `json:"bytes"`
	}
	if json.Unmarshal(raw, &rows) != nil {
		return nil
	}
	out := make([]Volume, 0, len(rows))
	for _, r := range rows {
		v := Volume{App: r.App, Name: r.Volume, Status: r.Status}
		if r.Bytes != nil {
			v.Bytes, v.HasBytes = *r.Bytes, true
		}
		out = append(out, v)
	}
	return out
}

// OrphanVolumes are the reclaim candidates: volumes no installed app claims.
func OrphanVolumes(p Paths) []Volume {
	var out []Volume
	for _, v := range Volumes(p) {
		if v.Status == "orphaned" {
			out = append(out, v)
		}
	}
	return out
}

// RequestReap appends app names to `deployments/.status/reap` — the opt-in
// reclaim signal reconcile consumes (it removes a stopped app's volumes,
// refuses a live one, and clears processed lines). Deduped against what is
// already queued; never overwrites existing lines. ply does the deletion, not
// the dashboard (it cannot see the volumes dir).
func RequestReap(p Paths, apps ...string) error {
	dir := filepath.Join(p.Deployments, ".status")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "reap")
	queued := map[string]bool{}
	if raw, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if a := strings.TrimSpace(line); a != "" {
				queued[a] = true
			}
		}
	}
	var add []string
	for _, a := range apps {
		if a = strings.TrimSpace(a); a != "" && !queued[a] {
			queued[a] = true
			add = append(add, a)
		}
	}
	if len(add) == 0 {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(strings.Join(add, "\n") + "\n")
	return err
}

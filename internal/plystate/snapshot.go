package plystate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Snapshot mirrors the sidecar JSON ply's `snapshot take` writes under
// <apps>/<app>/snapshots/<name>.json — a small index so the dashboard can
// list an app's snapshots from the apps-dir grant it already holds, without
// opening a squashfs. The image itself stays in ply's store.
type Snapshot struct {
	Name    string            `json:"name"`
	Bytes   int64             `json:"bytes"`
	App     string            `json:"app"`
	Slot    uint32            `json:"slot"`
	Taken   string            `json:"taken"`
	Image   string            `json:"image"`
	Volumes map[string]string `json:"volumes"`
}

// Size is a human-readable byte count.
func (s Snapshot) Size() string {
	const k = 1024.0
	b := float64(s.Bytes)
	switch {
	case b < k:
		return fmt.Sprintf("%d B", s.Bytes)
	case b < k*k:
		return fmt.Sprintf("%.0f KiB", b/k)
	case b < k*k*k:
		return fmt.Sprintf("%.1f MiB", b/k/k)
	default:
		return fmt.Sprintf("%.2f GiB", b/k/k/k)
	}
}

// VolumeNames is the sorted list of volume names in the snapshot.
func (s Snapshot) VolumeNames() []string {
	names := make([]string, 0, len(s.Volumes))
	for n := range s.Volumes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func snapshotDir(p Paths, app string) string {
	return filepath.Join(p.Apps, app, "snapshots")
}

// Snapshots lists an app's snapshots, newest first.
func Snapshots(p Paths, app string) []Snapshot {
	entries, err := os.ReadDir(snapshotDir(p, app))
	if err != nil {
		return nil
	}
	var out []Snapshot
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" || e.Name()[0] == '.' {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(snapshotDir(p, app), e.Name()))
		if err != nil {
			continue
		}
		var s Snapshot
		if json.Unmarshal(raw, &s) != nil || s.Name == "" {
			continue
		}
		out = append(out, s)
	}
	// `taken` is an RFC3339 UTC string, so lexical sort is chronological.
	sort.Slice(out, func(i, j int) bool { return out[i].Taken > out[j].Taken })
	return out
}

// SubmitSnapshot asks the app's run parent to take a snapshot now.
func SubmitSnapshot(p Paths, app string) error {
	return SubmitControl(p, app, "snapshot", "")
}

// SubmitRestore asks the run parent to roll a slot back onto a snapshot.
func SubmitRestore(p Paths, app, name string) error {
	return SubmitControl(p, app, "restore", name)
}

package plystate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Control: commands as files in <apps>/<app>/control/, consumed by the
// app's run parent within ~2s. Whether the buttons work is decided by the
// operator's grant alone: an apps dir mounted read-write enables them, a
// read-only (or absent) grant leaves the dashboard observe-only. The
// permission IS the ACL — no roles, no tokens, no API.

func controlDir(p Paths, app string) string {
	return filepath.Join(p.Apps, app, "control")
}

// ControlWritable probes the grant by creating the control dir.
func ControlWritable(p Paths, app string) bool {
	if err := os.MkdirAll(controlDir(p, app), 0o755); err != nil {
		return false
	}
	probe := filepath.Join(controlDir(p, app), ".probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

// SubmitControl files a command the daemontools way: write-temp, rename.
func SubmitControl(p Paths, app, name, content string) error {
	dir := controlDir(p, app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

// CommandResult mirrors the parent's last-result line.
type CommandResult struct {
	Command string `json:"command"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail"`
	TS      int64  `json:"ts"`
}

func (r CommandResult) String() string {
	mark := "ok"
	if !r.OK {
		mark = "failed"
	}
	return fmt.Sprintf("%s: %s (%s)", r.Command, mark, r.Detail)
}

func LastResult(p Paths, app string) *CommandResult {
	raw, err := os.ReadFile(filepath.Join(controlDir(p, app), "last-result"))
	if err != nil {
		return nil
	}
	var r CommandResult
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	return &r
}

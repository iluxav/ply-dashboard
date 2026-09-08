package plystate

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// NotifyConfig is <config>/notify.toml — the host-global notification
// settings ply's reconcile beat reads. The dashboard edits it through the
// granted config dir; ply owns delivery.
type NotifyConfig struct {
	On []string `toml:"on"`
	To []string `toml:"to"`
}

// The events a host can subscribe to, in the order the UI lists them. Kept
// in step with ply-core/src/notify.rs and the events the journal carries.
var NotifyEvents = []string{
	"deploy-failed",
	"restart-loop",
	"instance-restart",
	"snapshot-failed",
	"snapshot",
	"restore",
	"deploy",
	"egress-blocked",
	"disk-high",
}

func notifyPath(p Paths) string {
	return filepath.Join(p.Config, "notify.toml")
}

// NotifyWritable reports whether the config dir is a writable grant — the
// same "permission IS the ACL" rule as the control dir.
func NotifyWritable(p Paths) bool {
	if p.Config == "" || !grantMounted(p.Config) {
		return false
	}
	probe := filepath.Join(p.Config, ".probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

// LoadNotify reads the config; a missing file is an empty config, not an
// error (notifications simply off).
func LoadNotify(p Paths) NotifyConfig {
	var c NotifyConfig
	raw, err := os.ReadFile(notifyPath(p))
	if err != nil {
		return c
	}
	_ = toml.Unmarshal(raw, &c)
	return c
}

// SaveNotify writes the config atomically (temp + rename), creating the
// dir if the grant allows.
func SaveNotify(p Paths, c NotifyConfig) error {
	if p.Config == "" {
		return fmt.Errorf("no config dir granted")
	}
	if err := os.MkdirAll(p.Config, 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	tmp := filepath.Join(p.Config, ".notify.toml.tmp")
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, notifyPath(p))
}

// Has reports whether an event is subscribed.
func (c NotifyConfig) Has(event string) bool {
	for _, e := range c.On {
		if e == event {
			return true
		}
	}
	return false
}

// Telegram splits out the first telegram: destination into (token, chat)
// for the two-field form; the rest go in Others verbatim.
func (c NotifyConfig) Telegram() (token, chat string) {
	for _, d := range c.To {
		if rest, ok := strings.CutPrefix(d, "telegram:"); ok {
			if i := strings.LastIndex(rest, ":"); i > 0 {
				return rest[:i], rest[i+1:]
			}
		}
	}
	return "", ""
}

// Others is every destination that is not the telegram one, one per line,
// for the free-text box (discord:, https://, command:, enc:v1:…).
func (c NotifyConfig) Others() string {
	var out []string
	for _, d := range c.To {
		if !strings.HasPrefix(d, "telegram:") {
			out = append(out, d)
		}
	}
	return strings.Join(out, "\n")
}

// ComposeTo builds the `to` list from the form: an optional telegram pair
// plus the free-text lines.
func ComposeTo(token, chat, others string) []string {
	var to []string
	token, chat = strings.TrimSpace(token), strings.TrimSpace(chat)
	if token != "" && chat != "" {
		to = append(to, fmt.Sprintf("telegram:%s:%s", token, chat))
	}
	for _, line := range strings.Split(others, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			to = append(to, line)
		}
	}
	return to
}

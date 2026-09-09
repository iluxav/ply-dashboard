package plystate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TestDeliver sends one message to a destination, for the dashboard's
// "send test" button — instant feedback for telegram/discord/webhook. It
// mirrors ply's own delivery format (ply-core/src/notify.rs); the formats
// are a tiny, stable HTTP contract. A `command:` destination is refused
// here, because the dashboard is not the host and must not run its
// commands — those are tested with `ply notify --test`.
func TestDeliver(dest, text string) error {
	switch {
	case strings.HasPrefix(dest, "telegram:"):
		rest := strings.TrimPrefix(dest, "telegram:")
		i := strings.LastIndex(rest, ":")
		if i <= 0 {
			return fmt.Errorf("telegram: expected telegram:<token>:<chat-id>")
		}
		token, chat := rest[:i], rest[i+1:]
		body, _ := json.Marshal(map[string]string{"chat_id": chat, "text": text})
		return postJSON("https://api.telegram.org/bot"+token+"/sendMessage", body)
	case strings.HasPrefix(dest, "discord:"):
		body, _ := json.Marshal(map[string]string{"content": text})
		return postJSON(strings.TrimPrefix(dest, "discord:"), body)
	case strings.HasPrefix(dest, "command:"):
		return fmt.Errorf("command destinations are tested on the host with `ply notify --test`")
	default:
		url := strings.TrimPrefix(dest, "webhook:")
		if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
			body, _ := json.Marshal(text)
			return postJSON(url, body)
		}
		if strings.HasPrefix(dest, "enc:") {
			return fmt.Errorf("sealed — save it and test with `ply notify --test` on the host")
		}
		return fmt.Errorf("unknown destination")
	}
}

func postJSON(url string, body []byte) error {
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// Surface the service's own reason — Telegram/Discord return a JSON
		// `description` that says exactly what is wrong ("chat not found",
		// "bot can't initiate conversation with a user"). A bare status code
		// turned a wrong chat id into a long hunt.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if desc := jsonField(raw, "description"); desc != "" {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, desc)
		}
		if msg := jsonField(raw, "message"); msg != "" {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// jsonField pulls one string field out of a response body, or "" if the
// body is not JSON or lacks it.
func jsonField(raw []byte, field string) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	if v, ok := m[field].(string); ok {
		return v
	}
	return ""
}

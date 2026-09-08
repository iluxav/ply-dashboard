package plystate

import (
	"bytes"
	"encoding/json"
	"fmt"
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
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

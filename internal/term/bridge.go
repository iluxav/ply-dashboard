// Package term bridges a browser WebSocket to the PTY socket a run parent
// serves under the app's control dir. The bridge is deliberately dumb:
// browser binary messages become data frames, browser text messages become
// resize frames, and inbound data frames become binary messages. All
// protocol knowledge stays server-side; xterm.js sees only bytes.
package term

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// Bridge upgrades the request and pumps until either side hangs up.
func Bridge(w http.ResponseWriter, r *http.Request, socketPath string) error {
	conn, err := net.DialTimeout("unix", socketPath, 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	ctx := context.WithoutCancel(r.Context())

	go func() { // browser -> pty
		defer conn.Close()
		for {
			kind, payload, err := ws.Read(ctx)
			if err != nil {
				return
			}
			frame := make([]byte, 3+len(payload))
			if kind == websocket.MessageText {
				frame[0] = 1 // resize JSON
			}
			binary.BigEndian.PutUint16(frame[1:3], uint16(len(payload)))
			copy(frame[3:], payload)
			if _, err := conn.Write(frame); err != nil {
				return
			}
		}
	}()

	// pty -> browser
	header := make([]byte, 3)
	for {
		if _, err := io.ReadFull(conn, header); err != nil {
			ws.Close(websocket.StatusNormalClosure, "shell exited")
			return nil
		}
		payload := make([]byte, binary.BigEndian.Uint16(header[1:3]))
		if _, err := io.ReadFull(conn, payload); err != nil {
			ws.Close(websocket.StatusNormalClosure, "shell exited")
			return nil
		}
		if header[0] == 0 {
			if err := ws.Write(ctx, websocket.MessageBinary, payload); err != nil {
				return nil
			}
		}
	}
}

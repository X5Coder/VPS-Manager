package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	vpsssh "github.com/x5coder/vps-rooms/internal/ssh"
)

var sshTermUpgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

type sshTermMsg struct {
	Type string `json:"type"` // input | resize
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// handleSSHTerminalWS opens a live root shell (PTY) over WebSocket.
// The browser auto-connects when the SSH page opens — ready to type.
func (s *Server) handleSSHTerminalWS(w http.ResponseWriter, r *http.Request) {
	conn, err := sshTermUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	sh, err := vpsssh.StartShell(80, 24)
	if err != nil {
		_ = conn.WriteJSON(map[string]string{"type": "output", "data": "error: cannot open shell: " + err.Error() + "\r\n"})
		return
	}
	defer sh.Close()

	var wmu sync.Mutex
	writeOut := func(data string) {
		wmu.Lock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_ = conn.WriteJSON(map[string]string{"type": "output", "data": data})
		wmu.Unlock()
	}

	done := make(chan struct{})
	// PTY -> browser
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := sh.Master.Read(buf)
			if n > 0 {
				writeOut(string(buf[:n]))
			}
			if err != nil {
				writeOut("\r\n[shell closed]\r\n")
				return
			}
		}
	}()

	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Minute))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Minute))
		return nil
	})
	go func() {
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				wmu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				_ = conn.WriteMessage(websocket.PingMessage, nil)
				wmu.Unlock()
			}
		}
	}()

	// browser -> PTY
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var m sshTermMsg
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		switch m.Type {
		case "input":
			if m.Data != "" {
				_, _ = sh.Master.Write([]byte(m.Data))
			}
		case "resize":
			_ = sh.Resize(m.Cols, m.Rows)
		}
	}
}

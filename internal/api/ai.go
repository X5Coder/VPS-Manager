package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/x5coder/vps-rooms/internal/ai"
)

func (s *Server) handleRoomAI(w http.ResponseWriter, r *http.Request, roomID string) {
	_, room := s.roomAccess(w, r, roomID)
	if room == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Messages []ai.Message `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if len(body.Messages) == 0 {
		writeErr(w, 400, "messages required")
		return
	}
	st := s.storageInfo()
	avail := asInt64(st["quota_available"]) + room.QuotaBytes
	if avail < room.QuotaBytes {
		avail = room.QuotaBytes
	}
	maxGB := float64(avail) / (1024 * 1024 * 1024)
	curGB := float64(room.QuotaBytes) / (1024 * 1024 * 1024)
	freeGB := float64(asInt64(st["quota_available"])) / (1024 * 1024 * 1024)
	hist := append([]ai.Message{{
		Role: "system-note",
		Text: fmt.Sprintf(
			"Room name: %s. Docker image tag: %s:latest. Current room quota: %.1f GB. Free disk still available on the VPS: %.1f GB. Maximum the quota slider may be set to: %.1f GB. Ask the user how many GB they want (from that available amount) before clone/build/host, then set quota_gb.",
			room.Name, slugTag(room.Name), curGB, freeGB, maxGB,
		),
	}}, body.Messages...)
	rep, raw, err := ai.Turn(hist)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	if ai.Dangerous(rep.Command) {
		rep.Say = strings.TrimSpace(rep.Say + " That command is not allowed.")
		rep.Command = ""
		rep.Done = true
	}
	if rep.QuotaGB > 0 {
		if rep.QuotaGB > maxGB {
			rep.QuotaGB = maxGB
		}
		if rep.QuotaGB < 0.1 {
			rep.QuotaGB = 0.1
		}
		rep.QuotaGB = float64(int(rep.QuotaGB*10+0.5)) / 10
	}
	writeJSON(w, 200, map[string]any{
		"say":      rep.Say,
		"command":  rep.Command,
		"ask":      rep.Ask,
		"quota_gb": rep.QuotaGB,
		"done":     rep.Done,
		"raw":      raw,
	})
}

func slugTag(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r == '_' || r == ' ' {
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "room"
	}
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}

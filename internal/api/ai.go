package api

import (
	"encoding/json"
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
	hist := append([]ai.Message{{
		Role: "system-note",
		Text: "This room is named " + room.Name + ". Tag docker images as " + slugTag(room.Name) + ":latest when you build.",
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
	writeJSON(w, 200, map[string]any{
		"say":     rep.Say,
		"command": rep.Command,
		"ask":     rep.Ask,
		"done":    rep.Done,
		"raw":     raw,
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

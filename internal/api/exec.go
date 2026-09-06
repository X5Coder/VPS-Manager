package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleDeployExec(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Command    string `json:"command"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Command) == "" {
		writeErr(w, 400, "command required")
		return
	}
	if commandDangerous(body.Command) {
		writeErr(w, 400, "command not allowed")
		return
	}
	timeout := 120 * time.Second
	if body.TimeoutSec > 0 {
		timeout = time.Duration(body.TimeoutSec) * time.Second
	}
	if timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}
	j := s.startDeployExecJob(body.Command, timeout)
	writeJSON(w, 200, map[string]any{"ok": true, "job_id": j.ID, "status": j.Status, "scope": "deploy"})
}

func (s *Server) handleHostExec(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	// GET polls latest job for host (so refresh keeps spinner)
	if r.Method == http.MethodGet {
		s.handleExecLatest(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Command    string `json:"command"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Command) == "" {
		writeErr(w, 400, "command required")
		return
	}
	if commandDangerous(body.Command) {
		writeErr(w, 400, "command not allowed")
		return
	}
	timeout := 120 * time.Second
	if body.TimeoutSec > 0 {
		timeout = time.Duration(body.TimeoutSec) * time.Second
	}
	if timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}
	j := s.startHostExecJob(body.Command, timeout)
	writeJSON(w, 200, map[string]any{"ok": true, "job_id": j.ID, "status": j.Status, "scope": "host"})
}

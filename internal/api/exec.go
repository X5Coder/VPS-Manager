package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	dir := filepath.Join(s.Cfg.RuntimeDir, "_deploy")
	_ = os.MkdirAll(dir, 0o750)
	cmd := exec.CommandContext(ctx, "sh", "-lc", body.Command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	res := map[string]any{"output": string(out), "where": "deploy"}
	if err != nil {
		res["error"] = err.Error()
		res["exit"] = 1
	} else {
		res["exit"] = 0
	}
	writeJSON(w, 200, res)
}

func (s *Server) handleHostExec(w http.ResponseWriter, r *http.Request) {
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
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	dir := "/root"
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		dir = s.Cfg.DataDir
	}
	cmd := exec.CommandContext(ctx, "sh", "-lc", body.Command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	res := map[string]any{"output": string(out), "where": "vps-host"}
	if err != nil {
		res["error"] = err.Error()
		res["exit"] = 1
	} else {
		res["exit"] = 0
	}
	_ = appendLog(s.Cfg.DataDir, "host", "$ "+body.Command+"\n"+string(out))
	writeJSON(w, 200, res)
}

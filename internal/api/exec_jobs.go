package api

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Background exec jobs — survive page refresh, polled by the button spinner.

type execJob struct {
	ID         string     `json:"id"`
	Scope      string     `json:"scope"` // host | deploy | room
	RoomID     string     `json:"room_id,omitempty"`
	Command    string     `json:"command"`
	Where      string     `json:"where,omitempty"`
	Status     string     `json:"status"` // running | done | error
	Output     string     `json:"output,omitempty"`
	Error      string     `json:"error,omitempty"`
	Exit       int        `json:"exit"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

var (
	execMu      sync.Mutex
	execJobs    = map[string]*execJob{}
	execLatest  = map[string]string{} // scopeKey -> jobID
)

func execScopeKey(scope, roomID string) string {
	if scope == "room" && roomID != "" {
		return "room:" + roomID
	}
	return scope
}

func execCreate(scope, roomID, command string) *execJob {
	id := uuid.NewString()
	j := &execJob{
		ID:        id,
		Scope:     scope,
		RoomID:    roomID,
		Command:   command,
		Status:    "running",
		StartedAt: time.Now().UTC(),
	}
	execMu.Lock()
	execJobs[id] = j
	execLatest[execScopeKey(scope, roomID)] = id
	execMu.Unlock()
	return j
}

func execUpdateDone(id, where, output string, runErr error) {
	execMu.Lock()
	defer execMu.Unlock()
	j, ok := execJobs[id]
	if !ok {
		return
	}
	j.Where = where
	j.Output = output
	if runErr != nil {
		j.Status = "error"
		j.Error = runErr.Error()
		j.Exit = 1
	} else {
		j.Status = "done"
		j.Exit = 0
	}
	now := time.Now().UTC()
	j.FinishedAt = &now
	// Keep only last 50 jobs to avoid unbounded memory
	if len(execJobs) > 50 {
		// Remove oldest done jobs
		var oldest string
		var oldestTime time.Time
		for k, v := range execJobs {
			if v.Status != "running" {
				if oldest == "" || v.StartedAt.Before(oldestTime) {
					oldest = k
					oldestTime = v.StartedAt
				}
			}
		}
		if oldest != "" {
			delete(execJobs, oldest)
		}
	}
}

func (s *Server) routesExecJobs() {
	s.Mux.HandleFunc("/api/exec/status", s.withGate(s.handleExecStatus))
	s.Mux.HandleFunc("/api/exec/latest", s.withGate(s.handleExecLatest))
}

func (s *Server) handleExecStatus(w http.ResponseWriter, r *http.Request) {
	if s.requireSession(w, r) == nil {
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("job_id"))
	if id == "" {
		id = strings.TrimSpace(r.URL.Query().Get("id"))
	}
	if id == "" {
		writeErr(w, 400, "job_id required")
		return
	}
	execMu.Lock()
	j, ok := execJobs[id]
	execMu.Unlock()
	if !ok || j == nil {
		writeErr(w, 404, "job not found")
		return
	}
	cp := *j
	writeJSON(w, 200, cp)
}

func (s *Server) handleExecLatest(w http.ResponseWriter, r *http.Request) {
	if s.requireSession(w, r) == nil {
		return
	}
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scope == "" {
		scope = "host"
	}
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	if roomID == "" {
		roomID = strings.TrimSpace(r.URL.Query().Get("room"))
	}
	key := execScopeKey(scope, roomID)
	execMu.Lock()
	id := execLatest[key]
	j := execJobs[id]
	execMu.Unlock()
	if j == nil {
		writeJSON(w, 200, map[string]any{"status": "idle"})
		return
	}
	cp := *j
	writeJSON(w, 200, cp)
}



// New async handlers — called from exec.go and manage.go wrappers

func (s *Server) startHostExecJob(command string, timeout time.Duration) *execJob {
	j := execCreate("host", "", command)
	go func(jobID string) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		dir := "/root"
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			dir = s.Cfg.DataDir
		}
		_ = os.MkdirAll(dir, 0o750)
		cmd := exec.CommandContext(ctx, "sh", "-lc", command)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		outStr := string(out)
		_ = appendLog(s.Cfg.DataDir, "host", "$ "+command+"\n"+outStr)
		execUpdateDone(jobID, "vps-host", outStr, err)
	}(j.ID)
	return j
}

func (s *Server) startDeployExecJob(command string, timeout time.Duration) *execJob {
	j := execCreate("deploy", "", command)
	go func(jobID string) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		dir := filepath.Join(s.Cfg.DataDir, "_deploy")
		_ = os.MkdirAll(dir, 0o750)
		cmd := exec.CommandContext(ctx, "sh", "-lc", command)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		execUpdateDone(jobID, "deploy", string(out), err)
	}(j.ID)
	return j
}

func (s *Server) startRoomExecJob(roomID, command, projectID, containerID string, isHost bool, timeout time.Duration) *execJob {
	j := execCreate("room", roomID, command)
	go func(jobID string) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		_ = s.Rooms.EnsureUnlocked(roomID)

		runHost := func() (string, error) {
			dir := s.Rooms.RoomWorkDir(roomID)
			if projectID != "" {
				pdir := s.Rooms.ProjectDir(roomID, projectID)
				if st, err := os.Stat(pdir); err == nil && st.IsDir() {
					dir = pdir
				}
			}
			_ = os.MkdirAll(dir, 0o750)
			cmd := exec.CommandContext(ctx, "sh", "-lc", command)
			cmd.Dir = dir
			b, err := cmd.CombinedOutput()
			return string(b), err
		}

		hostCLI := isHostShellCommand(command)
		dockerID := ""
		if !isHost && !hostCLI && s.Docker != nil {
			ct := s.resolveRoomContainer(roomID, containerID)
			if ct != nil {
				dockerID = ct.DockerID
			}
			if dockerID == "" && projectID != "" {
				p, _ := s.Store.GetProject(projectID)
				if p != nil && p.RoomID == roomID {
					dockerID = p.ContainerID
				}
			}
		}
		where := "room-host"
		out := ""
		var runErr error
		if dockerID != "" {
			st, _ := s.Docker.InspectStatus(dockerID)
			if st == "running" {
				cmd := exec.CommandContext(ctx, "docker", "exec", dockerID, "sh", "-lc", command)
				b, err := cmd.CombinedOutput()
				out, runErr = string(b), err
				where = "container"
				if runErr != nil && (strings.Contains(out, "OCI runtime") || strings.Contains(out, "procReady") || strings.Contains(runErr.Error(), "OCI runtime")) {
					out2, err2 := runHost()
					out, runErr, where = out2, err2, "room-host"
				}
			} else {
				out, runErr = runHost()
			}
		} else {
			out, runErr = runHost()
		}
		_ = appendLog(s.Cfg.DataDir, "room-"+roomID[:8], "$ "+command+"\n"+out)
		_ = rotateLog(logPath(s.Cfg.DataDir, "room-"+roomID[:8]), 256*1024)
		execUpdateDone(jobID, where, out, runErr)
	}(j.ID)
	return j
}

// handleExecStatusByRoom is for legacy per-room polling path used by UI
func (s *Server) handleRoomExecStatus(w http.ResponseWriter, r *http.Request, roomID string) {
	if _, room := s.roomAccess(w, r, roomID); room == nil {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("job_id"))
	if id == "" {
		id = strings.TrimSpace(r.URL.Query().Get("id"))
	}
	if id == "" {
		// return latest for this room
		key := execScopeKey("room", roomID)
		execMu.Lock()
		id = execLatest[key]
		j := execJobs[id]
		execMu.Unlock()
		if j == nil {
			writeJSON(w, 200, map[string]any{"status": "idle"})
			return
		}
		writeJSON(w, 200, *j)
		return
	}
	execMu.Lock()
	j, ok := execJobs[id]
	execMu.Unlock()
	if !ok || j == nil || j.RoomID != roomID {
		writeErr(w, 404, "job not found")
		return
	}
	writeJSON(w, 200, *j)
}



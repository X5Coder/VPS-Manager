package api

import (
	"encoding/json"
	"net/http"
	"strings"

	vpsssh "github.com/x5coder/vps-rooms/internal/ssh"
)

func (s *Server) routesSSH() {
	s.Mux.HandleFunc("/api/ssh/terminal/ws", s.withGateWS(s.handleSSHTerminalWS))
	s.Mux.HandleFunc("/api/vps/paths", s.withGate(s.handleVPSPaths))
}

func (s *Server) sshConnection(r *http.Request) string {
	port := vpsssh.ReadPort()
	host := s.publicHost(r)
	return vpsssh.ConnectionString(host, port)
}

func (s *Server) handleSSHStatus(w http.ResponseWriter, r *http.Request) {
	if s.requireSession(w, r) == nil {
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method")
		return
	}
	port := vpsssh.ReadPort()
	writeJSON(w, 200, map[string]any{
		"port":        port,
		"active":      vpsssh.Active(),
		"root_login":  vpsssh.RootLogin(),
		"connect":     s.sshConnection(r),
		"base_dir":    s.Cfg.BaseDir,
		"single_dir":  s.Cfg.SingleDir,
		"multi_dir":   s.Cfg.MultiDir,
		"data_dir":    s.Cfg.DataDir,
		"proxy_dir":   s.Cfg.ProxyDir,
		"bin_dir":     s.Cfg.BinDir,
		"agent_dir":   s.Cfg.AgentDir,
		"backup_dir":  s.Cfg.GlobalBackupDir(),
		"panel_port":  9090,
		"vps_path":    s.Cfg.BaseDir,
		"layout":      []string{"bin/", "data/{database.sqlite,sessions/,logs/}", "proxy/", "x5coder-agent/", "backup/vps-manager.zip", "single/<room_id>/{project/,container/,volumes/,config/,logs/,backup/<room_id>.zip}", "multi/<room_id>/{project/,stack/docker-compose.yml,containers/,volumes/,config/,logs/,backup/<room_id>.zip}"},
	})
}

func (s *Server) handleSSHKeys(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, map[string]any{"keys": vpsssh.AuthorizedKeys(), "connect": s.sshConnection(r)})
	case http.MethodPost:
		var body struct {
			PublicKey string `json:"public_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.PublicKey) == "" {
			writeErr(w, 400, "public_key required")
			return
		}
		if err := vpsssh.AddKey(body.PublicKey); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"ok": "1"})
	default:
		writeErr(w, 405, "method")
	}
}

// handleSSHRootPassword reveals the saved VPS root password (owner only).
// It is masked in the UI until the owner presses Show.
func (s *Server) handleSSHRootPassword(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method")
		return
	}
	pass := vpsssh.SavedRootPassword(s.Cfg.DataDir)
	writeJSON(w, 200, map[string]any{
		"stored":   strings.TrimSpace(pass) != "",
		"password": pass,
		"connect":  s.sshConnection(r),
		"port":     vpsssh.ReadPort(),
	})
}

// handleVPSPaths returns the canonical breadcrumb path for any room (or the VPS root).
// Every web page shows this at the top so the user always knows where they are in the VPS.
func (s *Server) handleVPSPaths(w http.ResponseWriter, r *http.Request) {
	if s.requireSession(w, r) == nil {
		return
	}
	if r.URL.Query().Get("all") == "1" {
		rooms, _ := s.Store.ListRooms()
		out := make([]map[string]any, 0, len(rooms))
		for _, rm := range rooms {
			kind := "single"
			if strings.ToLower(strings.TrimSpace(rm.Kind)) == "multi" {
				kind = "multi"
			}
			out = append(out, map[string]any{
				"room_id":  rm.ID,
				"name":     rm.Name,
				"kind":     kind,
				"vps_path": s.Rooms.VPSPath(rm.ID),
				"work_dir": s.Rooms.RoomWorkDir(rm.ID),
				"volumes":  s.Rooms.RoomVolumesDir(rm.ID),
				"config":   s.Rooms.RoomConfigDir(rm.ID),
				"env_path": s.Rooms.RoomEnvPath(rm.ID),
			})
		}
		writeJSON(w, 200, map[string]any{
			"rooms":     out,
			"connect":   s.sshConnection(r),
			"base_dir":  s.Cfg.BaseDir,
			"backupDir": s.Cfg.GlobalBackupDir(),
			"backup_dir": s.Cfg.GlobalBackupDir(),
		})
		return
	}
	roomID := strings.TrimSpace(r.URL.Query().Get("room_id"))
	if roomID == "" {
		writeJSON(w, 200, map[string]any{
			"vps_path": s.Cfg.BaseDir,
			"connect":  s.sshConnection(r),
			"crumbs": []map[string]string{
				{"label": "/vps-manager", "path": s.Cfg.BaseDir},
			},
		})
		return
	}
	room, _ := s.Store.GetRoom(roomID)
	kind := "single"
	if room != nil && strings.ToLower(strings.TrimSpace(room.Kind)) == "multi" {
		kind = "multi"
	}
	rp := s.Rooms.VPSPath(roomID)
	work := s.Rooms.RoomWorkDir(roomID)
	writeJSON(w, 200, map[string]any{
		"room_id":    roomID,
		"kind":       kind,
		"vps_path":   rp,
		"work_dir":   work,
		"volumes":    s.Rooms.RoomVolumesDir(roomID),
		"config":     s.Rooms.RoomConfigDir(roomID),
		"backup":     s.Rooms.RoomBackupDir(roomID),
		"backup_path": s.Rooms.RoomBackupPath(roomID),
		"env_path":   s.Rooms.RoomEnvPath(roomID),
		"connect":    s.sshConnection(r),
		"crumbs": []map[string]string{
			{"label": "/vps-manager", "path": s.Cfg.BaseDir},
			{"label": kind, "path": kind},
			{"label": roomID, "path": rp},
		},
		"_": kind,
	})
}

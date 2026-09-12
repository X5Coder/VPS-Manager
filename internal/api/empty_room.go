package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/x5coder/vps-rooms/internal/rooms"
	"github.com/x5coder/vps-rooms/internal/store"
)

type roomPending struct {
	ContainerPort int `json:"container_port"`
	HostPort      int `json:"host_port"`
}

func (s *Server) pendingPath(roomID string) string {
	return filepath.Join(s.Rooms.VPSPath(roomID), "pending.json")
}

func (s *Server) writeRoomPending(roomID string, cPort, hPort int) {
	if cPort <= 0 {
		cPort = 8080
	}
	_ = os.MkdirAll(s.Rooms.VPSPath(roomID), 0o700)
	b, _ := json.Marshal(roomPending{ContainerPort: cPort, HostPort: hPort})
	_ = os.WriteFile(s.pendingPath(roomID), b, 0o600)
}

func (s *Server) readRoomPending(roomID string) (cPort, hPort int) {
	cPort = 8080
	b, err := os.ReadFile(s.pendingPath(roomID))
	if err != nil {
		return cPort, 0
	}
	var p roomPending
	if json.Unmarshal(b, &p) != nil {
		return cPort, 0
	}
	if p.ContainerPort > 0 {
		cPort = p.ContainerPort
	}
	return cPort, p.HostPort
}

func (s *Server) createEmptyRoom(name string, quotaGB float64, cPort, hPort int, password, kind, domain string, ssl bool, sshCert string) (*store.Room, string, error) {
	if strings.TrimSpace(name) == "" {
		return nil, "", fmt.Errorf("room name is required")
	}
	if err := emptyRoomErr(quotaGB); err != nil {
		return nil, "", err
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "" && kind != store.KindSingle && kind != store.KindMulti {
		return nil, "", fmt.Errorf("kind must be %q or %q", store.KindSingle, store.KindMulti)
	}
	roomName := sanitizeRoomName(name)
	if existing, _ := s.Store.GetRoomByName(roomName); existing != nil {
		return nil, "", fmt.Errorf("room name already in use")
	}
	quota, err := s.allocateQuota(quotaGB, 0)
	if err != nil {
		return nil, "", err
	}
	pass := strings.TrimSpace(password)
	if pass != "" && len(pass) < 6 {
		return nil, "", fmt.Errorf("password must be at least 6 characters")
	}
	if pass == "" {
		pass = randomPass(10)
	}
	rm, err := s.Rooms.Create(rooms.CreateInput{Name: roomName, Password: pass, QuotaBytes: quota, Kind: kind})
	if err != nil {
		return nil, "", err
	}
	if cPort <= 0 {
		cPort = 8080
	}
	s.writeRoomPending(rm.ID, cPort, hPort)
	if strings.TrimSpace(domain) != "" || ssl {
		rm.Domain = strings.TrimSpace(domain)
		rm.SSL = ssl
		_ = s.Store.UpdateRoom(*rm)
	}
	if strings.TrimSpace(sshCert) != "" {
		p := filepath.Join(s.Rooms.RoomConfigDir(rm.ID), "ssh.crt")
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		_ = os.WriteFile(p, []byte(sshCert), 0o600)
	}
	return rm, pass, nil
}

func emptyRoomErr(quotaGB float64) error {
	if quotaGB <= 0 {
		return fmt.Errorf("quota_gb is required and must be > 0")
	}
	return nil
}

// handleRoomUpload accepts a project/stack ZIP for one room and stores it:
// single rooms extract into project/, multi rooms into stack/ (persistent
// data/volumes and config/.env are never touched). It uses the same core as
// the agent deploy/update tools.
func (s *Server) handleRoomUpload(w http.ResponseWriter, r *http.Request, id string) {
	_, room := s.canControlRoom(w, r, id)
	if room == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeErr(w, 400, "project ZIP file required (max 64MB)")
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "field 'file' with the project ZIP is required")
		return
	}
	defer f.Close()
	tmp, err := os.CreateTemp("", "room-upload-*.zip")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.CopyN(tmp, f, agentMaxArchiveBytes+1); err != nil && err != io.EOF {
		_ = tmp.Close()
		writeErr(w, 400, err.Error())
		return
	}
	_ = tmp.Close()
	if st, err := os.Stat(tmpName); err != nil || st.Size() == 0 {
		writeErr(w, 400, "empty file")
		return
	}
	if st, _ := os.Stat(tmpName); st != nil && st.Size() > agentMaxArchiveBytes {
		writeErr(w, 400, "file too large (max 48MB)")
		return
	}
	format, err := agentSniffFile(tmpName)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	port, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("internal_port")))
	res, err := s.deployRoomArchive(room, true, tmpName, format, port)
	if err != nil {
		_ = appendLog(s.Cfg.DataDir, "deploy", "UPLOAD FAIL room="+id+" err="+err.Error())
		writeErr(w, 400, err.Error())
		return
	}
	if m, ok := res.(map[string]any); ok {
		m["kind"] = room.Kind
		_ = appendLog(s.Cfg.DataDir, "deploy", "UPLOAD OK room="+id)
		writeJSON(w, 200, m)
		return
	}
	writeJSON(w, 200, res)
}

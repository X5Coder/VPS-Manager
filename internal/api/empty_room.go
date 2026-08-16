package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/x5coder/vps-rooms/internal/rooms"
	"github.com/x5coder/vps-rooms/internal/store"
)

type roomPending struct {
	ContainerPort int `json:"container_port"`
	HostPort      int `json:"host_port"`
}

func (s *Server) tokenIsOwner(tok *store.APIToken) bool {
	return tok != nil
}

func (s *Server) pendingPath(roomID string) string {
	return filepath.Join(s.Cfg.RuntimeDir, roomID, "pending.json")
}

func (s *Server) writeRoomPending(roomID string, cPort, hPort int) {
	if cPort <= 0 {
		cPort = 8080
	}
	_ = os.MkdirAll(filepath.Join(s.Cfg.RuntimeDir, roomID), 0o700)
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
		p := filepath.Join(s.Cfg.RuntimeDir, rm.ID, "ssh.crt")
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

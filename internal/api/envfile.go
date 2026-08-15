package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (s *Server) roomEnvPath(roomID string) string {
	return filepath.Join(s.Cfg.RuntimeDir, roomID, ".env")
}

func parseEnvMap(text string) [][2]string {
	var rows [][2]string
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		rows = append(rows, [2]string{k, strings.TrimRight(v, "\r")})
	}
	return rows
}

func envMapFromRows(rows [][2]string) map[string]string {
	m := map[string]string{}
	for _, r := range rows {
		m[r[0]] = r[1]
	}
	return m
}

func renderEnvRows(rows [][2]string) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r[0])
		b.WriteByte('=')
		b.WriteString(r[1])
		b.WriteByte('\n')
	}
	return b.String()
}

func (s *Server) readRoomEnv(roomID string) (string, error) {
	_ = s.Rooms.EnsureUnlocked(roomID)
	path := s.roomEnvPath(roomID)
	b, err := os.ReadFile(path)
	if err == nil {
		return string(b), nil
	}
	projs, _ := s.Store.ListProjects(roomID)
	if len(projs) > 0 {
		return s.Projects.ReadEnv(projs[0].ID)
	}
	if os.IsNotExist(err) {
		return "", nil
	}
	return "", err
}

func (s *Server) writeRoomEnv(roomID, text string) error {
	_ = s.Rooms.EnsureUnlocked(roomID)
	path := s.roomEnvPath(roomID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return err
	}
	projs, _ := s.Store.ListProjects(roomID)
	if len(projs) > 0 {
		_ = s.Projects.WriteEnv(projs[0].ID, text)
	}
	return nil
}

func (s *Server) envSetKeys(roomID string, pairs [][2]string, replace bool) error {
	text, err := s.readRoomEnv(roomID)
	if err != nil {
		return err
	}
	rows := parseEnvMap(text)
	idx := map[string]int{}
	for i, r := range rows {
		idx[r[0]] = i
	}
	for _, p := range pairs {
		k := strings.TrimSpace(p[0])
		if k == "" {
			return fmt.Errorf("key required")
		}
		if i, ok := idx[k]; ok {
			rows[i][1] = p[1]
		} else {
			if replace {
				return fmt.Errorf("key not found: %s", k)
			}
			idx[k] = len(rows)
			rows = append(rows, [2]string{k, p[1]})
		}
	}
	return s.writeRoomEnv(roomID, renderEnvRows(rows))
}

func (s *Server) envDeleteKey(roomID, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("key required")
	}
	text, err := s.readRoomEnv(roomID)
	if err != nil {
		return err
	}
	rows := parseEnvMap(text)
	var out [][2]string
	found := false
	for _, r := range rows {
		if r[0] == key {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		return fmt.Errorf("key not found: %s", key)
	}
	return s.writeRoomEnv(roomID, renderEnvRows(out))
}

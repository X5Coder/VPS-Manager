package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/x5coder/vps-rooms/internal/proxy"
	"github.com/x5coder/vps-rooms/internal/store"
)

func (s *Server) domainBindJSON(w http.ResponseWriter, r *http.Request, p *store.Project) {
	if p == nil {
		writeErr(w, 404, "not found")
		return
	}
	out := map[string]any{
		"ok":             true,
		"domain":         p.Domain,
		"domain_enabled": p.DomainEnabled,
		"ssl_status":     p.SSLStatus,
		"host_port":      p.HostPort,
		"project_id":     p.ID,
		"links":          s.projectLinks(r, p),
	}
	if p.Domain != "" {
		out["nginx"] = proxy.InspectNginx(p.Domain, p.HostPort)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleV1Port(w http.ResponseWriter, r *http.Request, roomID string) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	_, p, err := s.resolveRoomProject(roomID)
	if err != nil || p == nil {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "not found", "code": "not_found"})
		return
	}
	var body struct {
		HostPort int  `json:"host_port"`
		Clear    bool `json:"clear"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "invalid request", "code": "invalid_request"})
		return
	}
	if body.Clear || body.HostPort == 0 {
		if err := s.Projects.ClearPort(p.ID); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "code": "port_failed"})
			return
		}
		p.HostPort = 0
		_ = s.syncProxy()
		s.domainBindJSON(w, r, p)
		return
	}
	if err := s.Projects.SetPort(p.ID, body.HostPort); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "code": "port_failed"})
		return
	}
	p2, _ := s.Projects.Get(p.ID)
	if p2 != nil {
		p = p2
	}
	_ = s.syncProxy()
	s.domainBindJSON(w, r, p)
}

func (s *Server) handleV1Domain(w http.ResponseWriter, r *http.Request, roomID string) {
	if r.Method == http.MethodGet {
		_, p, err := s.resolveRoomProject(roomID)
		if err != nil || p == nil {
			writeJSON(w, 404, map[string]any{"ok": false, "error": "not found", "code": "not_found"})
			return
		}
		s.domainBindJSON(w, r, p)
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	_, p, err := s.resolveRoomProject(roomID)
	if err != nil || p == nil {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "not found", "code": "not_found"})
		return
	}
	var body struct {
		Domain   string `json:"domain"`
		Enabled  *bool  `json:"enabled"`
		HostPort *int   `json:"host_port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "invalid request", "code": "invalid_request"})
		return
	}
	if body.HostPort != nil && *body.HostPort > 0 {
		if err := s.Projects.SetPort(p.ID, *body.HostPort); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "code": "port_failed"})
			return
		}
		p2, _ := s.Projects.Get(p.ID)
		if p2 != nil {
			p = p2
		}
	}
	en := true
	if body.Enabled != nil {
		en = *body.Enabled
	}
	if strings.TrimSpace(body.Domain) == "" {
		en = false
	}
	if err := s.applyDomain(p, body.Domain, en); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "code": "domain_bind_failed"})
		return
	}
	s.domainBindJSON(w, r, p)
}

func (s *Server) handleV1WipeData(w http.ResponseWriter, r *http.Request, roomID string) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	_, p, err := s.resolveRoomProject(roomID)
	if err != nil || p == nil {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "not found", "code": "not_found"})
		return
	}
	if err := s.Projects.WipeDataVolume(p); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "code": "wipe_failed"})
		return
	}
	p2, _ := s.Projects.Get(p.ID)
	if p2 != nil {
		p = p2
	}
	writeJSON(w, 200, map[string]any{
		"ok":         true,
		"wiped":      true,
		"project_id": p.ID,
		"room_id":    roomID,
		"status":     p.Status,
	})
}

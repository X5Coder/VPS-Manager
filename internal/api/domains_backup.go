package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/x5coder/vps-rooms/internal/proxy"
	"github.com/x5coder/vps-rooms/internal/store"
)

func (s *Server) routesBackupDomain() {
	s.Mux.HandleFunc("/api/backup/status", s.withGate(s.handleBackupStatus))
	s.Mux.HandleFunc("/api/backup/token", s.withGate(s.handleBackupToken))
	s.Mux.HandleFunc("/api/backup/enable", s.withGate(s.handleBackupEnable))
	s.Mux.HandleFunc("/api/backup/now", s.withGate(s.handleBackupNow))
	s.Mux.HandleFunc("/api/backup/stop", s.withGate(s.handleBackupStop))
	s.Mux.HandleFunc("/api/backup/schedule", s.withGate(s.handleBackupSchedule))
	s.Mux.HandleFunc("/api/backup/inspect", s.withGate(s.handleBackupInspect))
	s.Mux.HandleFunc("/api/backup/restore", s.withGate(s.handleBackupRestore))
	s.Mux.HandleFunc("/api/proxy/status", s.withGate(s.handleProxyStatus))
	s.Mux.HandleFunc("/api/proxy/sync", s.withGate(s.handleProxySync))
}

func (s *Server) publicHost(r *http.Request) string {
	if h, _, _ := s.Store.GetMeta("public_host"); h != "" {
		return h
	}
	host := r.Host
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	if host == "" || host == "127.0.0.1" || host == "localhost" {
		// try outbound IP
		if ip := detectPublicIP(); ip != "" {
			return ip
		}
	}
	return host
}

func detectPublicIP() string {
	conn, err := net.DialTimeout("udp", "8.8.8.8:80", 2*time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP.String()
}

func (s *Server) projectLinks(r *http.Request, p *store.Project) []map[string]string {
	host := s.publicHost(r)
	var links []map[string]string
	if p.Domain != "" && p.DomainEnabled {
		scheme := "https"
		if p.SSLStatus == "http-only" {
			scheme = "http"
		}
		links = append(links, map[string]string{
			"label": "Domain",
			"url":   scheme + "://" + p.Domain,
			"kind":  "domain",
		})
	}
	// Port link only when no public domain (many apps bind 127.0.0.1 behind nginx).
	if p.HostPort > 0 && (p.Domain == "" || !p.DomainEnabled) {
		links = append(links, map[string]string{
			"label": "App (port)",
			"url":   fmt.Sprintf("http://%s:%d", host, p.HostPort),
			"kind":  "port",
		})
	}
	if p.ExternalURL != "" {
		links = append(links, map[string]string{
			"label": "Dashboard / Studio",
			"url":   p.ExternalURL,
			"kind":  "external",
		})
	}
	return links
}

func (s *Server) applyDomain(p *store.Project, domain string, enabled bool) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.Split(domain, "/")[0]
	p.Domain = domain
	p.DomainEnabled = enabled && domain != ""
	if !p.DomainEnabled {
		p.SSLStatus = "disabled"
		_ = s.Store.UpdateProject(*p)
		return s.syncProxy()
	}
	if p.HostPort <= 0 {
		if n := s.upstreamPort(p); n > 0 {
			p.HostPort = n
		}
	}
	if p.HostPort <= 0 {
		return fmt.Errorf("set a host port before binding a domain")
	}
	if err := s.Store.ReleaseDomain(domain, p.ID); err != nil {
		return err
	}
	p.SSLStatus = "pending"
	if err := s.Store.UpdateProject(*p); err != nil {
		return err
	}
	if err := s.syncProxy(); err != nil {
		p.SSLStatus = "error: " + err.Error()
		_ = s.Store.UpdateProject(*p)
		return err
	}
	port := s.upstreamPort(p)
	if proxy.NginxInstalled() && !proxy.VhostPointsTo(domain, port) {
		p.SSLStatus = "error: nginx proxy_pass does not match host_port"
		_ = s.Store.UpdateProject(*p)
		return fmt.Errorf("nginx vhost %s is not proxying to 127.0.0.1:%d", domain, port)
	}
	p.SSLStatus = "active"
	return s.Store.UpdateProject(*p)
}

func (s *Server) upstreamPort(p *store.Project) int {
	if p == nil {
		return 0
	}
	port := p.HostPort
	if s.Docker != nil && p.ContainerID != "" {
		if live := s.Docker.PublishedHostPort(p.ContainerID); live > 0 {
			port = live
		}
	}
	if port > 0 && port != p.HostPort {
		p.HostPort = port
		_ = s.Store.UpdateProject(*p)
	}
	return port
}

func (s *Server) syncProxy() error {
	if s.Proxy == nil && !proxy.NginxInstalled() {
		return fmt.Errorf("proxy not ready")
	}
	projs, err := s.Store.ListAllProjects()
	if err != nil {
		return err
	}
	var sites []proxy.Site
	seen := map[string]struct{}{}
	for i := range projs {
		p := &projs[i]
		if p.Domain == "" || !p.DomainEnabled {
			continue
		}
		port := s.upstreamPort(p)
		if port <= 0 {
			continue
		}
		d := strings.ToLower(strings.TrimSpace(p.Domain))
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		sites = append(sites, proxy.Site{
			Domain: d, Upstream: fmt.Sprintf("127.0.0.1:%d", port), Enabled: true,
		})
	}
	if proxy.NginxInstalled() {
		if err := proxy.SyncNginx(sites); err != nil {
			return err
		}
	}
	if s.Proxy == nil {
		return nil
	}
	return s.Proxy.ReplaceAll(sites)
}

func (s *Server) handleProxyStatus(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	st := map[string]any{}
	if s.Proxy != nil {
		st = s.Proxy.Status()
	}
	writeJSON(w, 200, st)
}

func (s *Server) handleProxySync(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	if err := s.syncProxy(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) handleBackupStatus(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if s.Backup == nil {
		writeJSON(w, 200, map[string]any{"configured": false})
		return
	}
	writeJSON(w, 200, s.Backup.Status())
}

func (s *Server) handleBackupToken(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	switch r.Method {
	case http.MethodPost:
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		tok := strings.TrimSpace(body.Token)
		if tok == "" {
			existing, _, _ := s.Backup.LoadToken()
			if existing != "" {
				writeJSON(w, 200, s.Backup.Status())
				return
			}
			writeErr(w, 400, "GitHub classic PAT with repo and delete_repo is required")
			return
		}
		if err := s.Backup.SaveToken(tok); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, s.Backup.Status())
	case http.MethodDelete:
		_ = s.Backup.ClearToken()
		writeJSON(w, 200, map[string]string{"ok": "1"})
	default:
		writeErr(w, 405, "method")
	}
}

func (s *Server) handleBackupEnable(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Enabled bool   `json:"enabled"`
		Token   string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if body.Enabled {
		tok := strings.TrimSpace(body.Token)
		if tok != "" {
			if err := s.Backup.SaveToken(tok); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
		} else if err := s.Backup.SetEnabled(true); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	} else if err := s.Backup.SetEnabled(false); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, s.Backup.Status())
}

func (s *Server) handleBackupNow(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	job, err := s.Backup.StartBackupAsync(body.Label, body.Description, false)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"ok": "1", "job": job, "message": "Backup running on server — check Restore page for status"})
}

func (s *Server) handleBackupStop(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	if s.Backup == nil {
		writeErr(w, 400, "backup not configured")
		return
	}
	if err := s.Backup.StopJob(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": "1", "message": "Backup paused — press Start to continue", "job": s.Backup.CurrentJob()})
}

func (s *Server) handleBackupSchedule(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Hours int `json:"hours"`
		Days  int `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	hours := body.Hours
	if body.Days > 0 {
		hours = body.Days * 24
	}
	if err := s.Backup.SetIntervalHours(hours); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, s.Backup.Status())
}

func (s *Server) handleBackupInspect(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	token := body.Token
	if token == "" {
		t, _, _ := s.Backup.LoadToken()
		token = t
	}
	rooms, err := s.Backup.InspectRemoteRooms(token)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"rooms": rooms, "format": "VPS-ROOM-SNAP-v1"})
}

func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Token      string   `json:"token"`
		SnapshotID string   `json:"snapshot_id"`
		Repo       string   `json:"repo"`
		Repos      []string `json:"repos"`
		All        bool     `json:"all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	token := strings.TrimSpace(body.Token)
	repos := make([]string, 0, len(body.Repos)+1)
	for _, rpo := range body.Repos {
		if strings.TrimSpace(rpo) != "" {
			repos = append(repos, strings.TrimSpace(rpo))
		}
	}
	one := strings.TrimSpace(body.Repo)
	if one == "" {
		one = strings.TrimSpace(body.SnapshotID)
	}
	if one != "" {
		repos = append(repos, one)
	}
	if body.All {
		list, err := s.Backup.InspectRemoteRooms(token)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		repos = nil
		for _, rm := range list {
			if strings.TrimSpace(rm.Repo) != "" {
				repos = append(repos, rm.Repo)
			}
		}
	}
	job, err := s.Backup.StartRestoreList(token, repos)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"ok": "1", "job": job, "message": "Restore running on server — check Restore page for status"})
}

// helpers used by project handlers
func parseBoolForm(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func parsePortBody(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	default:
		return 0
	}
}

func ensureProxyDir(dataDir string) string {
	base := filepath.Dir(dataDir)
	dir := filepath.Join(base, "proxy")
	_ = os.MkdirAll(dir, 0o750)
	return dir
}

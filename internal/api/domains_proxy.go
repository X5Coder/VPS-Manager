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

func (s *Server) routesProxyDomain() {
	s.Mux.HandleFunc("/api/proxy/status", s.withGate(s.handleProxyStatus))
	s.Mux.HandleFunc("/api/proxy/sync", s.withGate(s.handleProxySync))
	s.Mux.HandleFunc("/api/proxy/test-domain", s.withGate(s.handleTestDomain))
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
	if proxy.NginxInstalled() {
		st := proxy.InspectNginx(domain, port)
		if !st.Matches {
			if st.SkippedCustom != "" {
				p.SSLStatus = "error: custom nginx vhost not overwritten"
				_ = s.Store.UpdateProject(*p)
				return fmt.Errorf("nginx file %q already serves %s with extra hosts or locations; not overwritten (proxy_pass %s). Use a dedicated hostname or update that file", st.SkippedCustom, domain, st.ProxyPass)
			}
			p.SSLStatus = "error: nginx proxy_pass does not match host_port"
			_ = s.Store.UpdateProject(*p)
			return fmt.Errorf("nginx vhost %s is not proxying to 127.0.0.1:%d", domain, port)
		}
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

func (s *Server) handleTestDomain(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	
	var req struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request body")
		return
	}
	
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.Split(domain, "/")[0]
	
	if domain == "" {
		writeErr(w, 400, "domain is required")
		return
	}
	
	writeJSON(w, 200, s.testDomainResult(domain))
}

// testDomainResult runs the real end-to-end domain test, shared by the
// test endpoint and the post-bind check.
func (s *Server) testDomainResult(domain string) map[string]any {
	result := map[string]any{
		"domain":    domain,
		"reachable": false,
		"ip":        "",
		"error":     "",
	}
	// Real end-to-end test (Cloudflare/CDN-aware):
	// 1. DNS resolves at all (edge IPs are fine — no origin match required).
	// 2. Local nginx routing serves the domain.
	// 3. The public URL answers through whatever sits in front.
	// Reachable = local routing works AND the public URL answers.
	local := s.testDomainLocal(domain)
	pubHTTP := s.testDomainPublic("http://" + domain)
	pubHTTPS := s.testDomainPublic("https://" + domain)
	ips := []string{}
	if addrs, err := net.LookupIP(domain); err == nil {
		for _, a := range addrs {
			ips = append(ips, a.String())
		}
	}
	points := false
	if my := detectPublicIP(); my != "" {
		for _, ip := range ips {
			if ip == my {
				points = true
				break
			}
		}
	}
	result["ips"] = ips
	result["points_to_server"] = points
	result["local_http"] = local
	result["public_http"] = pubHTTP
	result["public_https"] = pubHTTPS
	localOK, _ := local["ok"].(bool)
	pubOK := false
	if v, _ := pubHTTP["ok"].(bool); v {
		pubOK = true
	}
	if v, _ := pubHTTPS["ok"].(bool); v {
		pubOK = true
	}
	if localOK && pubOK {
		result["reachable"] = true
		result["message"] = "Domain works end-to-end (nginx + public URL answer)"
	} else if localOK {
		result["reachable"] = false
		result["message"] = "Nginx routing works, but the public URL does not answer yet — check DNS/proxy (Cloudflare orange cloud is fine once it serves)"
		result["error"] = "public URL unreachable"
	} else {
		result["reachable"] = false
		result["message"] = "Nginx on this server does not serve the domain — rebind it"
		result["error"] = "local routing failed"
	}

	return result
}

// testDomainLocal checks nginx routing on this host via Host header.
func (s *Server) testDomainLocal(domain string) map[string]any {
	out := map[string]any{"ok": false}
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	req.Host = domain
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	defer resp.Body.Close()
	out["ok"] = true
	out["code"] = resp.StatusCode
	return out
}

// testDomainPublic checks the public URL (through Cloudflare/CDN if any).
// Any HTTP response counts — even redirects mean the chain works.
func (s *Server) testDomainPublic(rawurl string) map[string]any {
	out := map[string]any{"ok": false}
	client := &http.Client{
		Timeout: 12 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(rawurl)
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	defer resp.Body.Close()
	out["ok"] = true
	out["code"] = resp.StatusCode
	return out
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

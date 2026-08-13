package proxy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Manager writes a Caddyfile and reloads Caddy for HTTPS reverse proxy.
type Manager struct {
	Dir      string // e.g. /opt/vps-rooms/proxy
	mu       sync.Mutex
	sites    map[string]Site // domain -> site
}

type Site struct {
	Domain   string
	Upstream string // 127.0.0.1:port
	Enabled  bool
}

func New(dir string) *Manager {
	_ = os.MkdirAll(dir, 0o750)
	return &Manager{Dir: dir, sites: map[string]Site{}}
}

func (m *Manager) Set(domain, upstream string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return fmt.Errorf("domain required")
	}
	if !enabled {
		delete(m.sites, domain)
	} else {
		if upstream == "" {
			return fmt.Errorf("upstream port required for domain")
		}
		m.sites[domain] = Site{Domain: domain, Upstream: upstream, Enabled: true}
	}
	return m.writeAndReload()
}

func (m *Manager) Remove(domain string) error {
	return m.Set(domain, "", false)
}

func (m *Manager) ReplaceAll(sites []Site) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sites = map[string]Site{}
	for _, s := range sites {
		d := strings.ToLower(strings.TrimSpace(s.Domain))
		if d == "" || !s.Enabled || s.Upstream == "" {
			continue
		}
		m.sites[d] = Site{Domain: d, Upstream: s.Upstream, Enabled: true}
	}
	return m.writeAndReload()
}

func (m *Manager) writeAndReload() error {
	path := filepath.Join(m.Dir, "Caddyfile")
	var b strings.Builder
	b.WriteString("# Managed by VPS MANAGE — do not edit by hand\n")
	b.WriteString("{\n\temail admin@localhost\n}\n\n")
	keys := make([]string, 0, len(m.sites))
	for k := range m.sites {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := m.sites[k]
		fmt.Fprintf(&b, "%s {\n\treverse_proxy %s\n\tencode gzip\n}\n\n", s.Domain, s.Upstream)
	}
	if len(keys) == 0 {
		b.WriteString("# no domains\n:2019 {\n\trespond \"vps-manage-proxy ok\" 200\n}\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	if _, err := exec.LookPath("caddy"); err != nil {
		return fmt.Errorf("caddy not installed — run panel install or: apt install caddy")
	}
	// validate
	cmd := exec.Command("caddy", "validate", "--config", path, "--adapter", "caddyfile")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("caddy validate: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	// try reload via systemd or caddy reload
	if err := exec.Command("systemctl", "reload", "vps-manage-caddy").Run(); err == nil {
		return nil
	}
	if err := exec.Command("caddy", "reload", "--config", path, "--adapter", "caddyfile").Run(); err == nil {
		return nil
	}
	// start in background if nothing listening
	_ = exec.Command("systemctl", "restart", "vps-manage-caddy").Start()
	return nil
}

func (m *Manager) Status() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]Site, 0, len(m.sites))
	for _, s := range m.sites {
		list = append(list, s)
	}
	_, has := exec.LookPath("caddy")
	return map[string]any{"sites": list, "caddy_installed": has == nil, "caddyfile": filepath.Join(m.Dir, "Caddyfile")}
}

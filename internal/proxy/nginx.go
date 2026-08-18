package proxy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const nginxMarker = "# Managed by VPS Manager — do not edit by hand"

var (
	NginxAvailableDir = "/etc/nginx/sites-available"
	NginxEnabledDir   = "/etc/nginx/sites-enabled"
	CertLiveDir       = "/etc/letsencrypt/live"
	SSLOptionsPath    = "/etc/letsencrypt/options-ssl-nginx.conf"
	SSLDHParamPath    = "/etc/letsencrypt/ssl-dhparams.pem"
)

var (
	serverNameRe = regexp.MustCompile(`(?m)^\s*server_name\s+([^;]+);`)
	sslCertRe    = regexp.MustCompile(`(?m)^\s*ssl_certificate\s+([^;]+);`)
	sslKeyRe     = regexp.MustCompile(`(?m)^\s*ssl_certificate_key\s+([^;]+);`)
	proxyPassRe  = regexp.MustCompile(`proxy_pass\s+http://127\.0\.0\.1:(\d+)\s*;`)
	locationRe   = regexp.MustCompile(`(?m)^\s*location\s+([^{;]+)`)
)

func NginxInstalled() bool {
	_, err := exec.LookPath("nginx")
	return err == nil
}

func VhostFile(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	return filepath.Join(NginxAvailableDir, domain)
}

func RenderNginxVhost(domain string, hostPort int, cert, key string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	loc := nginxLocation(hostPort)
	var b strings.Builder
	b.WriteString(nginxMarker + "\n")
	fmt.Fprintf(&b, "server {\n")
	fmt.Fprintf(&b, "    listen 80;\n")
	fmt.Fprintf(&b, "    listen [::]:80;\n")
	fmt.Fprintf(&b, "    server_name %s;\n", domain)
	b.WriteString(loc)
	b.WriteString("}\n")
	if cert != "" && key != "" && fileExists(cert) && fileExists(key) {
		b.WriteString("\nserver {\n")
		b.WriteString("    listen 443 ssl http2;\n")
		b.WriteString("    listen [::]:443 ssl http2;\n")
		fmt.Fprintf(&b, "    server_name %s;\n", domain)
		fmt.Fprintf(&b, "    ssl_certificate %s;\n", cert)
		fmt.Fprintf(&b, "    ssl_certificate_key %s;\n", key)
		if fileExists(SSLOptionsPath) {
			fmt.Fprintf(&b, "    include %s;\n", SSLOptionsPath)
		}
		if fileExists(SSLDHParamPath) {
			fmt.Fprintf(&b, "    ssl_dhparam %s;\n", SSLDHParamPath)
		}
		b.WriteString(loc)
		b.WriteString("}\n")
	}
	return b.String()
}

func nginxLocation(hostPort int) string {
	return fmt.Sprintf(`    location / {
        proxy_pass http://127.0.0.1:%d;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
`, hostPort)
}

func VhostPointsTo(domain string, hostPort int) bool {
	st := InspectNginx(domain, hostPort)
	return st.Matches
}

type NginxStatus struct {
	File          string `json:"file,omitempty"`
	ProxyPass     string `json:"proxy_pass,omitempty"`
	Matches       bool   `json:"matches"`
	SkippedCustom string `json:"skipped_custom,omitempty"`
	ReplacedVhost string `json:"replaced_vhost,omitempty"`
}

func InspectNginx(domain string, hostPort int) NginxStatus {
	domain = strings.ToLower(strings.TrimSpace(domain))
	st := NginxStatus{}
	if domain == "" {
		return st
	}
	if owner := customVhostOwner(domain); owner != "" {
		body, _ := os.ReadFile(filepath.Join(NginxAvailableDir, owner))
		if skipCustomFile(string(body), domain) {
			st.File = owner
			st.SkippedCustom = owner
			st.ProxyPass = firstProxyPass(string(body))
			st.Matches = hostPort > 0 && strings.Contains(string(body), fmt.Sprintf("proxy_pass http://127.0.0.1:%d;", hostPort))
			return st
		}
		st.ReplacedVhost = owner
	}
	for _, name := range nginxSearchFiles(domain) {
		b, err := os.ReadFile(filepath.Join(NginxAvailableDir, name))
		if err != nil {
			continue
		}
		text := string(b)
		if !containsName(parseServerNames(text), domain) && name != domain {
			continue
		}
		st.File = name
		st.ProxyPass = firstProxyPass(text)
		if hostPort > 0 && strings.Contains(text, fmt.Sprintf("proxy_pass http://127.0.0.1:%d;", hostPort)) {
			st.Matches = true
			return st
		}
	}
	return st
}

func firstProxyPass(body string) string {
	m := proxyPassRe.FindStringSubmatch(body)
	if len(m) > 1 {
		return "http://127.0.0.1:" + m[1]
	}
	return ""
}

func nginxSearchFiles(domain string) []string {
	out := []string{domain}
	if owner := customVhostOwner(domain); owner != "" {
		out = append(out, owner)
	}
	return out
}

func resolveCerts(domain, existingBody string) (cert, key string) {
	live := filepath.Join(CertLiveDir, domain)
	full := filepath.Join(live, "fullchain.pem")
	pk := filepath.Join(live, "privkey.pem")
	if fileExists(full) && fileExists(pk) {
		return full, pk
	}
	if m := sslCertRe.FindStringSubmatch(existingBody); len(m) > 1 {
		cert = strings.TrimSpace(m[1])
	}
	if m := sslKeyRe.FindStringSubmatch(existingBody); len(m) > 1 {
		key = strings.TrimSpace(m[1])
	}
	if cert != "" && key != "" && fileExists(cert) && fileExists(key) {
		return cert, key
	}
	return "", ""
}

// SyncNginx overwrites the per-domain vhost every time. Combined custom
// files (several server_name values) are left alone so OAuth splits stay intact.
func SyncNginx(sites []Site) error {
	if !NginxInstalled() {
		return nil
	}
	if err := os.MkdirAll(NginxAvailableDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(NginxEnabledDir, 0o755); err != nil {
		return err
	}
	for _, s := range sites {
		d := strings.ToLower(strings.TrimSpace(s.Domain))
		port := parseUpstreamPort(s.Upstream)
		if d == "" || !s.Enabled || port <= 0 {
			continue
		}
		owner := customVhostOwner(d)
		var old []byte
		if owner != "" {
			ob, err := os.ReadFile(filepath.Join(NginxAvailableDir, owner))
			if err == nil && skipCustomFile(string(ob), d) {
				continue
			}
			old = ob
		}
		avail := filepath.Join(NginxAvailableDir, d)
		if len(old) == 0 {
			old, _ = os.ReadFile(avail)
		}
		cert, key := resolveCerts(d, string(old))
		body := RenderNginxVhost(d, port, cert, key)
		if err := os.WriteFile(avail, []byte(body), 0o644); err != nil {
			return fmt.Errorf("write nginx vhost %s: %w", d, err)
		}
		en := filepath.Join(NginxEnabledDir, d)
		if dest, err := os.Readlink(en); err != nil || dest != avail {
			_ = os.Remove(en)
			if err := os.Symlink(avail, en); err != nil {
				return fmt.Errorf("enable nginx vhost %s: %w", d, err)
			}
		}
		if !VhostPointsTo(d, port) {
			return fmt.Errorf("nginx vhost %s proxy_pass is not 127.0.0.1:%d", d, port)
		}
		_ = disableSingleNameLeftovers(d)
	}
	return nginxReload()
}

func customVhostOwner(domain string) string {
	ents, err := os.ReadDir(NginxAvailableDir)
	if err != nil {
		return ""
	}
	for _, e := range ents {
		if e.IsDir() || e.Name() == domain {
			continue
		}
		b, err := os.ReadFile(filepath.Join(NginxAvailableDir, e.Name()))
		if err != nil {
			continue
		}
		if containsName(parseServerNames(string(b)), domain) {
			return e.Name()
		}
	}
	return ""
}

func skipCombinedCustom(domain string) bool {
	owner := customVhostOwner(domain)
	if owner == "" {
		return false
	}
	b, err := os.ReadFile(filepath.Join(NginxAvailableDir, owner))
	if err != nil {
		return true
	}
	return skipCustomFile(string(b), domain)
}

// skipCustomFile is true for combined hostnames (awn) or extra location
// blocks (/auth/). A leftover single-name reverse proxy (python-hosting)
// is rewritten so Bind Domain can retarget proxy_pass.
func skipCustomFile(body, domain string) bool {
	names := uniqueNames(parseServerNames(body))
	if len(names) > 1 {
		return true
	}
	if len(names) == 1 && names[0] != domain {
		return true
	}
	return hasExtraLocations(body)
}

func hasExtraLocations(body string) bool {
	for _, m := range locationRe.FindAllStringSubmatch(body, -1) {
		if len(m) < 2 {
			continue
		}
		loc := strings.TrimSpace(m[1])
		if loc == "" || loc == "/" {
			continue
		}
		if strings.Contains(loc, ".well-known") {
			continue
		}
		return true
	}
	return false
}

func disableSingleNameLeftovers(domain string) error {
	ents, err := os.ReadDir(NginxEnabledDir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.IsDir() || e.Name() == domain {
			continue
		}
		path := filepath.Join(NginxEnabledDir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		names := uniqueNames(parseServerNames(string(b)))
		if len(names) == 1 && names[0] == domain {
			_ = os.Remove(path)
		}
	}
	return nil
}

func parseServerNames(body string) []string {
	var out []string
	for _, m := range serverNameRe.FindAllStringSubmatch(body, -1) {
		if len(m) < 2 {
			continue
		}
		for _, n := range strings.Fields(m[1]) {
			n = strings.ToLower(strings.TrimSpace(n))
			if n == "" || n == "_" {
				continue
			}
			out = append(out, n)
		}
	}
	return out
}

func uniqueNames(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, n := range in {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func parseUpstreamPort(up string) int {
	up = strings.TrimSpace(up)
	if i := strings.LastIndex(up, ":"); i >= 0 {
		var n int
		_, _ = fmt.Sscanf(up[i+1:], "%d", &n)
		return n
	}
	return 0
}

func nginxReload() error {
	cmd := exec.Command("nginx", "-t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nginx -t: %s", strings.TrimSpace(string(out)))
	}
	if err := exec.Command("nginx", "-s", "reload").Run(); err != nil {
		if err := exec.Command("systemctl", "reload", "nginx").Run(); err != nil {
			return fmt.Errorf("nginx reload: %w", err)
		}
	}
	return nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

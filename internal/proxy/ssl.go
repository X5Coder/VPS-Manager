package proxy

// Automatic HTTPS for bound domains.
//
// The panel already renders a :443 server block whenever a live certificate
// exists (see RenderNginxVhost + resolveCerts) — the only missing piece was
// obtaining the certificate. EnsureDomainHTTPS closes that gap: it reuses a
// live cert, otherwise issues one with certbot (nginx plugin), so binding
// any domain to any room ends with working HTTPS instead of HTTP-only.

import (
	"context"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// LiveCertPaths returns the cert/key pair when a live certificate exists.
func LiveCertPaths(domain string) (cert, key string, ok bool) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return "", "", false
	}
	cert = filepath.Join(CertLiveDir, domain, "fullchain.pem")
	key = filepath.Join(CertLiveDir, domain, "privkey.pem")
	if fileExists(cert) && fileExists(key) {
		return cert, key, true
	}
	return "", "", false
}

// EnsureDomainHTTPS makes https://domain work end-to-end. It returns
// certOK=false (with a human note, never an error) when HTTPS cannot be
// completed yet — the HTTP binding itself is unaffected.
func EnsureDomainHTTPS(domain string) (certOK bool, note string) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return false, "empty domain"
	}
	if _, _, ok := LiveCertPaths(domain); ok {
		return true, "live certificate already present"
	}
	if _, err := net.DefaultResolver.LookupIPAddr(context.Background(), domain); err != nil {
		return false, "DNS does not resolve yet — add an A record pointing to this server, then retry HTTPS"
	} else {
		// LookupIPAddr succeeded but may return no addresses on some systems.
		if addrs, _ := net.LookupIP(domain); len(addrs) == 0 {
			return false, "DNS does not resolve yet — add an A record pointing to this server, then retry HTTPS"
		}
	}
	bin, err := exec.LookPath("certbot")
	if err != nil {
		return false, "certbot is not installed — HTTPS needs certbot on the VPS"
	}
	args := []string{"--nginx", "-d", domain, "--non-interactive", "--agree-tos", "--redirect"}
	// Register the ACME account without an email only on first use.
	if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(CertLiveDir), "accounts", "*", "*", "*")); len(matches) == 0 {
		args = append(args, "--register-unsafely-without-email")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = "…" + msg[len(msg)-300:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return false, "certificate issuance failed: " + msg
	}
	if _, _, ok := LiveCertPaths(domain); ok {
		return true, "certificate issued"
	}
	return false, "certbot reported success but no live certificate found"
}

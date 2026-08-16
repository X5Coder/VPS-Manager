package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderNginxVhostUsesHostPort(t *testing.T) {
	body := RenderNginxVhost("api.studixzone.com", 11001, "", "")
	if !strings.Contains(body, "proxy_pass http://127.0.0.1:11001;") {
		t.Fatalf("missing host_port proxy_pass:\n%s", body)
	}
	if strings.Contains(body, "proxy_pass http://127.0.0.1:8080;") {
		t.Fatal("must not use container_port 8080 on the host")
	}
	if strings.Contains(body, ":8000") {
		t.Fatal("must not keep a stale 8000 upstream")
	}
	if !strings.Contains(body, "server_name api.studixzone.com;") {
		t.Fatal("missing server_name")
	}
	if strings.Contains(body, "listen 443") {
		t.Fatal("no cert should mean HTTP only")
	}
}

func TestRenderNginxVhostTLS(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "fullchain.pem")
	key := filepath.Join(dir, "privkey.pem")
	_ = os.WriteFile(cert, []byte("x"), 0o644)
	_ = os.WriteFile(key, []byte("y"), 0o644)
	body := RenderNginxVhost("api.studixzone.com", 11001, cert, key)
	if !strings.Contains(body, "listen 443 ssl http2;") {
		t.Fatal("expected TLS server")
	}
	if strings.Count(body, "proxy_pass http://127.0.0.1:11001;") < 2 {
		t.Fatalf("80 and 443 must both proxy to host_port:\n%s", body)
	}
}

func TestParseUpstreamPort(t *testing.T) {
	if parseUpstreamPort("127.0.0.1:11001") != 11001 {
		t.Fatal("parse")
	}
}

func TestSkipCombinedCustom(t *testing.T) {
	en := t.TempDir()
	av := t.TempDir()
	oldEn, oldAv := NginxEnabledDir, NginxAvailableDir
	NginxEnabledDir, NginxAvailableDir = en, av
	t.Cleanup(func() { NginxEnabledDir, NginxAvailableDir = oldEn, oldAv })
	_ = os.WriteFile(filepath.Join(en, "awn"), []byte("server_name api.awnlearn.com;\nserver_name app.awnlearn.com;\n"), 0o644)
	_ = os.WriteFile(filepath.Join(av, "awn"), []byte("server_name api.awnlearn.com;\nserver_name app.awnlearn.com;\n"), 0o644)
	if customVhostOwner("api.awnlearn.com") == "" {
		t.Fatal("should skip combined custom vhost")
	}
	_ = os.WriteFile(filepath.Join(av, "api.studixzone.com"), []byte("server_name api.studixzone.com;\n"), 0o644)
	if customVhostOwner("api.studixzone.com") != "" {
		t.Fatal("dedicated domain file must be rewritten")
	}
	_ = os.WriteFile(filepath.Join(av, "studix"), []byte("server_name app.studixzone.com;\n"), 0o644)
	if customVhostOwner("app.studixzone.com") != "studix" {
		t.Fatal("custom single-name file must not be replaced by a new dedicated vhost")
	}
}

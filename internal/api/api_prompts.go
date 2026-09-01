package api

import (
	"fmt"
	"net/http"
	"strings"
)

func requestBaseURL(r *http.Request) string {
	host := r.Host
	if host == "" {
		host = "YOUR_VPS_IP:9090"
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + host
}

func (s *Server) tokenCopyFields(base, secret string) (api, script, scriptMulti string) {
	script = buildGitHubWorkflowAuto(base, secret)
	scriptMulti = buildGitHubWorkflowAuto(base, secret)
	api = s.buildAPISheet(base, secret)
	return
}

func (s *Server) buildAPISheet(base, secret string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	secret = strings.TrimSpace(secret)
	if secret == "" {
		secret = "YOUR_SECRET"
	}
	return fmt.Sprintf("BASE=%s\nTOKEN=%s\nAuthorization: Bearer %s\n", base, secret, secret)
}

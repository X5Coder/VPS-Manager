package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/x5coder/vps-rooms/internal/store"
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

func tokenPromptMode(mode string) string {
	return store.NormalizeTokenMode(mode)
}

func (s *Server) buildAPIPrompt(base, secret, mode string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	secret = strings.TrimSpace(secret)
	if secret == "" {
		secret = "YOUR_SECRET"
	}
	switch tokenPromptMode(mode) {
	case "write":
		return apiPromptWrite(base, secret)
	case "both":
		return apiPromptBoth(base, secret)
	default:
		return apiPromptRead(base, secret)
	}
}

func (s *Server) buildAPISheet(base, secret, mode string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	secret = strings.TrimSpace(secret)
	if secret == "" {
		secret = "YOUR_SECRET"
	}
	perm := "GET only"
	switch tokenPromptMode(mode) {
	case "write":
		perm = "GET + POST/PATCH/exec/build/redeploy"
	case "both":
		perm = "GET + POST/PATCH/exec/build/redeploy (one key)"
	}
	sheet := fmt.Sprintf(`VPS Manager HTTP API
BASE=%s
TOKEN=%s
MODE=%s
PERM=%s

Header (every request):
  Authorization: Bearer %s
  Content-Type: application/json

GET  %s/api/v1/storage
GET  %s/api/v1/ports
GET  %s/api/v1/projects
GET  %s/api/v1/projects/{id}
GET  %s/api/v1/projects/{id}/deploys
`, base, secret, tokenPromptMode(mode), perm, secret, base, base, base, base, base)
	if tokenPromptMode(mode) != "read" {
		sheet += fmt.Sprintf(`
POST   %s/api/v1/projects
PATCH  %s/api/v1/projects/{id}
POST   %s/api/v1/projects/{id}/redeploy
POST   %s/api/v1/projects/{id}/build
POST   %s/api/v1/images/build
POST   %s/api/v1/projects/{id}/exec

curl examples:
  curl -sS -H "Authorization: Bearer %s" %s/api/v1/projects
  curl -sS -X POST -H "Authorization: Bearer %s" -H "Content-Type: application/json" \
    -d '{"image":"vpsrooms/app:latest","pull":true,"recreate":true}' \
    %s/api/v1/projects/{id}/redeploy
  # image omitted = pull+recreate current image; returns status=deploying; poll GET
  curl -sS -X POST -H "Authorization: Bearer %s" -H "Content-Type: application/json" -d '{}' \
    %s/api/v1/projects/{id}/redeploy
  curl -sS -X POST -H "Authorization: Bearer %s" -H "Content-Type: application/json" \
    -d '{"name":"app","image":"nginx:alpine","quota_gb":2}' \
    %s/api/v1/projects
`, base, base, base, base, base, base, secret, base, secret, base, secret, base, secret, base)
	} else {
		sheet += fmt.Sprintf(`
This token cannot POST/PATCH. Read-only.

curl:
  curl -sS -H "Authorization: Bearer %s" %s/api/v1/storage
  curl -sS -H "Authorization: Bearer %s" %s/api/v1/projects
`, secret, base, secret, base)
	}
	sheet += `
{id} = room id (project_id also works).
DELETE is not available. Never send a guessed token.`
	return strings.TrimSpace(sheet) + "\n"
}

func apiPromptRead(base, secret string) string {
	return fmt.Sprintf(`You operate VPS Manager over HTTP. Read-only. Match the user's language. Be precise. Never invent numbers, never print secrets, never mutate.

Auth: BASE %s
Authorization: Bearer %s
JSON. {id} = room id (or project_id). GET env is masked (KEY=***).

Permission: READ — GET only. Refuse POST, PATCH, PUT, DELETE.

Inspect
  GET /api/v1/storage     disk_free, quota_available_gb
  GET /api/v1/ports       used_ports (9090 is the panel)
  GET /api/v1/projects    id, image, status, ports, quota vs usage, last_deploy_*
  GET /api/v1/projects/{id}

Method: GET storage + projects first, then answer from JSON. If they ask to deploy or change anything, say this key is read-only.`, base, secret)
}

func apiPromptWrite(base, secret string) string {
	return fmt.Sprintf(`You operate VPS Manager over HTTP. You may inspect and change rooms. You cannot delete. Match the user's language. Be precise. Never invent numbers. Never print secrets.

Auth: BASE %s
Authorization: Bearer %s
JSON. {id} = room id (or project_id). GET env is masked.

Permission: WRITE — GET plus POST/PATCH/exec/build/redeploy.

Publish unit is a Docker image (name:tag). Never git clone, npm install, or copy source into a running container. Update an existing room on the same {id}; keep ports, domain, quota, .env. Create a new room only when they ask for a new one.

Read
  GET /api/v1/storage
  GET /api/v1/ports
  GET /api/v1/projects
  GET /api/v1/projects/{id}

Write
  POST /api/v1/projects                         new room — quota_gb required, <= quota_available_gb after GET /storage
  PATCH /api/v1/projects/{id}                   name, password, domain, env, quota_gb, action=pause|resume; image = async redeploy (poll GET)
  POST /api/v1/projects/{id}/redeploy           image optional (omit = pull+recreate current image). Returns immediately status=deploying. Poll GET {id} until running or error.
  POST /api/v1/projects/{id}/build              host build, optional deploy:true. Returns immediately status=building. Poll GET. last_deploy_error has the docker/git reason on failure.
  POST /api/v1/projects/{id}/exec               diagnose only (timeout ≤ 120s)

Flow
1. GET storage + projects.
2. Update current app → POST .../redeploy (image optional) → GET until status=running (not waiting on the POST).
3. New app → POST /projects → return id, host_port, password.
4. Never DELETE. Never open a second room to replace one that already exists.`, base, secret)
}

func apiPromptBoth(base, secret string) string {
	return fmt.Sprintf(`You operate VPS Manager over HTTP with one token that can read and write. You cannot delete. Match the user's language. Be precise. Never invent numbers. Never print secrets. Do not ask for a second key.

Auth: BASE %s
Authorization: Bearer %s
JSON. {id} = room id (or project_id). GET env is masked (KEY=***).

Permission: BOTH — GET plus POST/PATCH/exec/build/redeploy on this same secret.

Publish unit is a Docker image (name:tag). Never git clone, npm install, or copy source into a running container. Update an existing room on the same {id}; keep ports, domain, quota, .env. Create a new room only when they ask for a new one.

Read
  GET /api/v1/storage
  GET /api/v1/ports
  GET /api/v1/projects
  GET /api/v1/projects/{id}

Write
  POST /api/v1/projects                         new room — quota_gb required, <= quota_available_gb after GET /storage
  PATCH /api/v1/projects/{id}                   name, password, domain, env, quota_gb, action=pause|resume; image = async redeploy (poll GET)
  POST /api/v1/projects/{id}/redeploy           image optional (omit = pull+recreate current image). Returns immediately status=deploying. Poll GET {id} until running or error.
  POST /api/v1/projects/{id}/build              host build, optional deploy:true. Returns immediately status=building. Poll GET. last_deploy_error has the docker/git reason on failure.
  POST /api/v1/projects/{id}/exec               diagnose only (timeout ≤ 120s)

Flow
1. GET /api/v1/storage and GET /api/v1/projects.
2. “Update this app” → POST .../redeploy (image optional = current image + pull) → GET {id} until status=running or error. POST returns immediately (accepted, status=deploying).
3. Image not on the VPS yet → POST .../build with a git context and deploy:true, then poll GET. Failure reason is last_deploy_error.
4. New app → POST /api/v1/projects with image + quota_gb → return id, host_port, password.
5. Never DELETE.`, base, secret)
}

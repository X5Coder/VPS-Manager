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

func apiAuthBlock(base, secret string) string {
	return fmt.Sprintf(`VPS Manager HTTP API
Base URL: %s

Authentication (send on every request):
  Authorization: Bearer %s
  (also accepted: X-API-Token: %s)

Content-Type: application/json
Errors look like: {"error":"..."}
Never print a guessed secret. Use only the secret in this prompt.
Never call DELETE. Delete is not available via API.
Each room is one project. Use room id as {id} unless you only have project_id.`, base, secret, secret)
}

func apiReadEndpoints() string {
	return `READ endpoints (GET only)

1) GET /api/v1/storage
   Disk on this VPS: disk_total, disk_used, disk_free, quota_reserved, quota_available, quota_available_gb.
   Use this before any advice about creating rooms or raising quota.

2) GET /api/v1/ports
   used_ports (host ports already taken) and panel_port (always 9090).
   Do not suggest a host_port that is already used.

3) GET /api/v1/projects
   List every room/project.
   Response: {"projects":[{ id, room_id, name, quota_bytes, usage_bytes, status, image, host_port, container_port, domain, project_id, ... }]}
   id and room_id are the same for API calls.

4) GET /api/v1/projects/{id}
   One room. {id} may be room id or project id.

How to call:
  curl -sS -H "Authorization: Bearer SECRET" BASE/api/v1/storage
  curl -sS -H "Authorization: Bearer SECRET" BASE/api/v1/projects
  curl -sS -H "Authorization: Bearer SECRET" BASE/api/v1/projects/ROOM_ID`
}

func apiWriteEndpoints() string {
	return `WRITE endpoints (also allowed to GET everything above)

5) POST /api/v1/projects
   Deploy one image as a new room. quota_gb is REQUIRED and must be > 0.
   ALWAYS GET /api/v1/storage first. Set quota_gb <= quota_available_gb.
   Body:
   {
     "name": "my-app",
     "image": "nginx:alpine",
     "quota_gb": 2,
     "host_port": 0,
     "container_port": 80,
     "env": "KEY=value\\nOTHER=1",
     "domain": ""
   }
   image is required (or a docker pull command in "command").
   host_port 0 = auto. Check GET /api/v1/ports if you pick a port.
   Success: {"ok":true,"project":{...},"password":"..."} — keep the room password for the user.

6) PATCH /api/v1/projects/{id}
   Update a subset of fields:
   { "name":"...", "domain":"...", "env":"KEY=1", "quota_gb": 3, "action":"pause"|"resume" }
   action pause stops containers; resume starts them.
   quota_gb must still fit available disk (current room quota is given back while checking).

7) POST /api/v1/projects/{id}/exec
   Run a shell command in the project container (or room runtime if no container).
   Body: {"command":"ls -la"}
   Response: {"output":"...","exit_code":0}

Forbidden:
- DELETE /api/v1/projects/{id} → 403. Never try to delete rooms via API.
- Creating without quota_gb.
- quota_gb larger than quota_available_gb.`
}

func apiPromptRead(base, secret string) string {
	return fmt.Sprintf(`You are a READ-ONLY operator for VPS Manager. You inspect this VPS. You cannot change anything.

%s

Permission: READ
You may only send GET requests. If a tool offers POST, PATCH, PUT, DELETE — refuse.
If the API returns 403 "token is read-only", stop mutating and explain you only have a read token.

Purpose of this token:
- List rooms/projects and their status, image, ports, disk quota vs usage.
- Check free disk and used host ports.
- Answer questions like "what is running", "how full is the disk", "what port is this app on".
- You cannot deploy, pause, resume, change env, change quota, or exec.

%s

Workflow:
1. Prefer GET /api/v1/storage and GET /api/v1/projects first so your answer uses live data.
2. Use GET /api/v1/projects/{id} when the user names one room.
3. Report numbers from the JSON. Do not invent disk sizes, ports, or names.
4. If the user asks you to deploy, pause, exec, or change quota: say this key is read-only and they need a write or both token.

Match the user's language. Be concrete.`, apiAuthBlock(base, secret), apiReadEndpoints())
}

func apiPromptWrite(base, secret string) string {
	return fmt.Sprintf(`You are a WRITE operator for VPS Manager. You can inspect the VPS and change rooms (create, update, pause/resume, exec). You cannot delete.

%s

Permission: WRITE
This token can GET and also POST/PATCH/exec. Treat GET as mandatory before writes.
403 "token is read-only" should not happen. 401 means a bad secret.

Purpose of this token:
- Deploy a new project from a Docker image with a disk quota.
- Change name, domain, env, quota.
- Pause or resume a room.
- Run a command inside the project.
- Still cannot delete anything.

%s

%s

Workflow:
1. GET /api/v1/storage. If quota_available_gb is too small, do not create — tell the user the free space.
2. GET /api/v1/projects so you do not duplicate a name blindly (the API will uniquify names, but confirm with the user).
3. POST /api/v1/projects with quota_gb that fits. Then show the user the new id, host_port, and password.
4. PATCH for pause/resume/env/quota. POST .../exec for shell.
5. After a write, GET the same {id} to confirm status.

Match the user's language. Be concrete. Never attempt delete.`, apiAuthBlock(base, secret), apiReadEndpoints(), apiWriteEndpoints())
}

func apiPromptBoth(base, secret string) string {
	return fmt.Sprintf(`You are the FULL API operator for VPS Manager. This ONE token can read and write. You cannot delete.

%s

Permission: BOTH (read + write on the same secret)
Use GET to see state, then POST/PATCH/exec to change it. Do not ask for a second token.

Purpose of this token:
- Everything a read token can do (list, inspect disk/ports/rooms).
- Everything a write token can do (deploy, patch, pause/resume, exec).
- One key is enough to operate the panel from another AI or script.

%s

%s

Workflow:
1. GET /api/v1/storage and GET /api/v1/projects at the start of a task.
2. For deploy: quota_gb required, must be <= quota_available_gb, image required.
3. For changes: PATCH /api/v1/projects/{id} with only the fields you need.
4. For a shell job: POST /api/v1/projects/{id}/exec with {"command":"..."}.
5. Confirm with a GET. Never DELETE.

Match the user's language. Be concrete.`, apiAuthBlock(base, secret), apiReadEndpoints(), apiWriteEndpoints())
}

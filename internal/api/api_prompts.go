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

func (s *Server) tokenCopyFields(base, secret string) (prompt, api, script, scriptMulti string) {
	script = buildGitHubWorkflowSingle(base, secret)
	scriptMulti = buildGitHubWorkflowMulti(base, secret)
	api = s.buildAPISheet(base, secret)
	prompt = s.buildAPIPrompt(base, secret, script, scriptMulti)
	return
}

func buildGitHubWorkflow(base, secret string) string {
	return buildGitHubWorkflowSingle(base, secret)
}

func (s *Server) buildAPISheet(base, secret string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	secret = strings.TrimSpace(secret)
	if secret == "" {
		secret = "YOUR_SECRET"
	}
	return fmt.Sprintf("BASE=%s\nTOKEN=%s\nAuthorization: Bearer %s\n", base, secret, secret)
}

func (s *Server) buildAPIPrompt(base, secret, scriptSingle, scriptMulti string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	secret = strings.TrimSpace(secret)
	if secret == "" {
		secret = "YOUR_SECRET"
	}
	if strings.TrimSpace(scriptSingle) == "" {
		scriptSingle = buildGitHubWorkflowSingle(base, secret)
	}
	if strings.TrimSpace(scriptMulti) == "" {
		scriptMulti = buildGitHubWorkflowMulti(base, secret)
	}
	return strings.NewReplacer(
		"{{BASE}}", base,
		"{{TOKEN}}", secret,
		"{{SCRIPT_SINGLE}}", strings.TrimSpace(scriptSingle),
		"{{SCRIPT_MULTI}}", strings.TrimSpace(scriptMulti),
	).Replace(`You are the VPS Manager API operator. One token controls ALL rooms. Paste this whole prompt into an AI. Match the user language (Arabic or English — do not mix). Never invent BASE, TOKEN, room ids, GB, or passwords. Never DELETE. Never guess a secret.

AUTH
  BASE={{BASE}}
  TOKEN={{TOKEN}}
  Header: Authorization: Bearer {{TOKEN}}
  Errors: JSON { "ok": false, "error": "...", "code": "..." } plus HTTP status.

ROOM_ID
  ROOM_ID=PASTE_ROOM_ID_HERE  — field "id" from GET /api/v1/projects. Not the token.
  Same id for first upload and later updates.

HTTP ERRORS (every step)
  401 {"error":"unauthorized"} — missing/wrong token
  404 {"error":"not found"} — bad ROOM_ID or container_id
  405 {"error":"method"} — wrong HTTP method
  400 {"error":"...","code":"..."} — see each command

────────────────────────────────
1) AVAILABLE DISK — call this BEFORE creating a room
GET {{BASE}}/api/v1/quota
  200 {
    "quota_available_gb": 12.5,
    "quota_available": 13421772800,
    "disk_total": ..., "disk_used": ..., "disk_free": ...,
    "quota_reserved": ...,
    "hint": "quota_gb on POST /api/v1/projects must be > 0 and <= quota_available_gb"
  }
Same numbers: GET {{BASE}}/api/v1/storage
curl -sS -H "Authorization: Bearer {{TOKEN}}" {{BASE}}/api/v1/quota

────────────────────────────────
2) LIST ROOMS
GET {{BASE}}/api/v1/projects
  200 { "projects":[{ "id","name","kind":"single|multi","status":"empty|running|stopped|deploying|error",
        "quota_gb","usage_gb","containers","images","volumes" }], "storage":{...} }
curl -sS -H "Authorization: Bearer {{TOKEN}}" {{BASE}}/api/v1/projects

GET one: {{BASE}}/api/v1/projects/$ROOM_ID
  200 room object. 404 not found.

────────────────────────────────
3) CREATE EMPTY ROOM (only after step 1)
POST {{BASE}}/api/v1/projects
Body: {"name":"my-app","quota_gb":10,"password":"secret6+","kind":"single","container_port":8080}
  kind optional: single | multi
  200 { "ok":true, "empty":true, "status":"empty", "password":"...", "project":{ "id":"ROOM_ID", ... } }
  status=empty means no container yet — then upload (step 4) or GitHub.
  400 code=quota_required — quota_gb missing or ≤ 0
  400 code=quota_exceeds_available — quota_gb > quota_available_gb (body also returns quota_available_gb)
  400 {"error":"password is required (min 6 characters)"}
  400 {"error":"password must be at least 6 characters"}
  400 invalid JSON → {"error":"invalid request"}
curl -sS -X POST {{BASE}}/api/v1/projects -H "Authorization: Bearer {{TOKEN}}" -H "Content-Type: application/json" \
  -d '{"name":"my-app","quota_gb":10,"password":"at-least-6-chars","kind":"single"}'

────────────────────────────────
4) UPDATE THIS ROOM — send the file. Same call for first image and later updates.
POST {{BASE}}/api/v1/projects/$ROOM_ID/upload
multipart field name: file

One image:
  docker save -o app.tar IMAGE:TAG
  curl -fS -H "Authorization: Bearer {{TOKEN}}" -F "file=@app.tar" \
    {{BASE}}/api/v1/projects/$ROOM_ID/upload

Compose stack (several containers):
  tar -czf stack.tar.gz compose.yml images
  curl -fS -H "Authorization: Bearer {{TOKEN}}" -F "file=@stack.tar.gz" \
    {{BASE}}/api/v1/projects/$ROOM_ID/upload

The panel looks inside the archive (docker save vs compose.yml). Filename .tar vs .tar.gz is a hint only.
HTTP 200 = the file arrived. The room updates in the background. Watch GET {{BASE}}/api/v1/projects/$ROOM_ID or the room page. Do not poll for minutes in CI.
Optional one-container update on a multi room: -F container_id=CONTAINER_ID

  400 package_empty | package_invalid | package_kind_mismatch | content_type | file_required
  404 container not found
  409 another deploy is running

────────────────────────────────
5) FILES
GET {{BASE}}/api/rooms/$ROOM_ID/containers/CONTAINER_ID/files?path=/
GET {{BASE}}/api/rooms/$ROOM_ID/volumes/VOLUME_ID/files?path=/
GET {{BASE}}/api/v1/projects/$ROOM_ID/containers/CONTAINER_ID/files?path=/
GET {{BASE}}/api/v1/projects/$ROOM_ID/volumes/VOLUME_ID/files?path=/
  200 { "path":"/","entries":[{"name":"...","dir":true,"size":0}] }
  200 file: { "path","content","binary":false,"size" }
  400 container stopped / Docker unavailable
  404 not found

────────────────────────────────
6) LOGS — one container OR the whole VPS. Never combined room logs.
By NAME:
GET {{BASE}}/api/v1/projects/$ROOM_ID/logs?name=auth
  curl -sS -H "Authorization: Bearer {{TOKEN}}" "{{BASE}}/api/v1/projects/$ROOM_ID/logs?name=auth"
By CONTAINER ID:
GET {{BASE}}/api/v1/projects/$ROOM_ID/logs?container=CONTAINER_ID
GET {{BASE}}/api/v1/projects/$ROOM_ID/containers/CONTAINER_ID/logs
  curl -sS -H "Authorization: Bearer {{TOKEN}}" "{{BASE}}/api/v1/projects/$ROOM_ID/logs?container=CONTAINER_ID"
  200 { "log","container_id","name","containers":[...] }
  400 code=logs_target_required — missing name= and container=
  404 code=container_not_found
Whole VPS (no ROOM_ID):
GET {{BASE}}/api/v1/logs
GET {{BASE}}/api/v1/logs?kind=vps
  Optional kind=host|panel|api|deploy
  curl -sS -H "Authorization: Bearer {{TOKEN}}" "{{BASE}}/api/v1/logs"

────────────────────────────────
6b) ENV / VOLUMES / IMAGES / COMPOSE / STATUS / AGENT
GET {{BASE}}/api/v1/projects/$ROOM_ID/env
POST {{BASE}}/api/v1/projects/$ROOM_ID/env  {"key":"API_KEY","value":"..."}
DELETE {{BASE}}/api/v1/projects/$ROOM_ID/env?key=API_KEY
GET/POST {{BASE}}/api/v1/projects/$ROOM_ID/volumes
POST {{BASE}}/api/v1/projects/$ROOM_ID/volumes/VOLUME_ID/clean
GET {{BASE}}/api/v1/projects/$ROOM_ID/images
POST {{BASE}}/api/v1/projects/$ROOM_ID/images/load  field file
GET {{BASE}}/api/v1/projects/$ROOM_ID/compose/validate
POST {{BASE}}/api/v1/projects/$ROOM_ID/stack/start|stop|restart|remove
GET {{BASE}}/api/v1/status
POST {{BASE}}/api/v1/agent  {"tool":"list_rooms"}
Create room optional: generate_password, domain, ssl, ssh_certificate
Exec: no short timeout; stdout+stderr+exit_code. Single room needs no container_id.

────────────────────────────────
7) EXEC / QUOTA PATCH / PORTS
POST .../projects/$ROOM_ID/exec  {"command":"ls -la","container_id":"..."}
  200 { "output","exit_code" }   400 command required
PATCH .../projects/$ROOM_ID  {"quota_gb":20}  — same quota errors as create
GET {{BASE}}/api/v1/ports  200 { "used_ports":[...], "panel_port":9090 }

────────────────────────────────
8) GITHUB ACTIONS — optional. The Action only POSTs the tar and exits.
  Single: Copy single script → .github/workflows/vps-deploy-single.yml  (docker save app.tar, POST /upload)
  Multi: Copy multi script → .github/workflows/vps-deploy-multi.yml  (pack compose.yml + images, POST /upload)
  Set ROOM_ID. HTTP 200 = received. Watch the room in the panel. Repo PRIVATE.

Tokens UI: Copy prompt = this text. Copy API = BASE+TOKEN. Copy single / multi script = YAML.

===== BEGIN FILE .github/workflows/vps-deploy-single.yml =====
{{SCRIPT_SINGLE}}
===== END FILE =====

===== BEGIN FILE .github/workflows/vps-deploy-multi.yml =====
{{SCRIPT_MULTI}}
===== END FILE =====
`)
}

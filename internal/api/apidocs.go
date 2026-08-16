package api

import "strings"

func docsReplace(s, base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = "http://YOUR_VPS_IP:9090"
	}
	s = strings.ReplaceAll(s, "{{BASE}}", base)
	return s
}

func APIDocSection(id, base string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "full" || id == "docs_full" {
		var b strings.Builder
		for _, k := range []string{"overview", "token", "github", "create_room", "update", "list", "logs", "exec"} {
			b.WriteString(APIDocSection(k, base))
			b.WriteString("\n\n")
		}
		return strings.TrimSpace(b.String())
	}
	id = strings.TrimPrefix(id, "docs_")
	text, ok := apiDocSections[id]
	if !ok {
		text = apiDocSections["overview"]
	}
	return docsReplace(text, base)
}

var apiDocSections = map[string]string{
	"overview": `VPS Manager public API (one token = ALL rooms)

Base: {{BASE}}
Auth: Authorization: Bearer YOUR_TOKEN

Errors are JSON: { "ok": false, "error": "message", "code": "error_code" } with HTTP 400/401/404/405/409.

Docs page: Tokens sidebar → Docs. Copy page copies this API brief (no install).`,

	"token": `CREATE AN API TOKEN (panel)

1) Tokens → Create token (name only).
2) Copy API = BASE + TOKEN.
3) Copy single script → .github/workflows/vps-deploy-single.yml
   Copy multi script → .github/workflows/vps-deploy-multi.yml
4) Copy prompt = full AI brief (this documentation + both YAMLs).

401 unauthorized if the token is missing or wrong.`,

	"github": `GITHUB ACTIONS — pick one workflow

SINGLE (one image):
  File: .github/workflows/vps-deploy-single.yml
  Builds Docker, docker save app.tar (MUST be .tar), POST /upload.
  Fails if compose.yml or images/*.tar exist (use multi).

MULTI (stack):
  File: .github/workflows/vps-deploy-multi.yml
  Packs project.vps.tar.gz:
    compose.yml   (any *.yml at repo root)
    images/image-01.tar
    images/image-02.tar
  Fails if .yml or images/*.tar is missing (use single).

Set ROOM_ID. Repo PRIVATE. Optional container_id only on the single workflow.`,

	"create_room": `AVAILABLE SPACE then CREATE ROOM

1) GET {{BASE}}/api/v1/quota
   200: quota_available_gb, disk_free, hint
   Use quota_available_gb as the maximum for quota_gb.

2) POST {{BASE}}/api/v1/projects
{
  "name": "my-app",
  "quota_gb": 10,
  "password": "secret6+",
  "kind": "single",
  "container_port": 8080
}
  200: { ok, empty:true, status:"empty", project.id = ROOM_ID, password }
  400 quota_required — quota_gb missing or ≤ 0
  400 quota_exceeds_available — quota_gb > quota_available_gb (response includes quota_available_gb)
  400 password_required / password_invalid
  400 invalid_request

curl -sS "{{BASE}}/api/v1/quota" -H "Authorization: Bearer YOUR_TOKEN"
curl -sS -X POST "{{BASE}}/api/v1/projects" \
  -H "Authorization: Bearer YOUR_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"my-app","quota_gb":10,"password":"secret6+","kind":"single"}'`,

	"update": `UPLOAD / UPDATE

POST {{BASE}}/api/v1/projects/ROOM_ID/upload
Field name: file

Single: filename *.tar only (docker save).
  -F "file=@app.tar;filename=app.tar"
  Optional: -F container_id=CONTAINER_ID (one container; others stay up)

Multi: filename *.tar.gz
  compose.yml (any .yml name)
  images/image-01.tar …

  -F "file=@project.vps.tar.gz;filename=project.vps.tar.gz"

  400 package_empty — not .tar / .tar.gz (or empty file)
  400 package_invalid — .tar is not docker save (no manifest.json)
  400 package_kind_mismatch — single vs multi swapped, or multi room got a .tar without container_id
  400 content_type — not multipart/form-data
  400 file_required
  404 container not found
  409 deploy already running

Panel: empty room dropzone .tar or .tar.gz. Click a container or volume to browse files.`,

	"list": `LIST / ONE ROOM

GET {{BASE}}/api/v1/projects
  200 projects[] + storage. 401 unauthorized.

GET {{BASE}}/api/v1/projects/ROOM_ID
  200 room (kind, status, quota_gb, containers, images, volumes). 404 not found.

GET {{BASE}}/api/rooms/ROOM_ID/containers/CONTAINER_ID/files?path=/
GET {{BASE}}/api/rooms/ROOM_ID/volumes/VOLUME_ID/files?path=/
GET {{BASE}}/api/v1/projects/ROOM_ID/containers/CONTAINER_ID/files?path=/
GET {{BASE}}/api/v1/projects/ROOM_ID/volumes/VOLUME_ID/files?path=/`,

	"logs": `LOGS — one container, or the whole VPS. Never a combined dump of every container in a room.

By container NAME:
GET {{BASE}}/api/v1/projects/ROOM_ID/logs?name=auth
  200 { log, container_id, name, containers[] }
  400 logs_target_required — missing name= and container=
  404 container_not_found

By container ID (or docker id / service name):
GET {{BASE}}/api/v1/projects/ROOM_ID/logs?container=CONTAINER_ID
GET {{BASE}}/api/v1/projects/ROOM_ID/containers/CONTAINER_ID/logs

Whole VPS (token only, no ROOM_ID):
GET {{BASE}}/api/v1/logs
GET {{BASE}}/api/v1/logs?kind=vps
Optional kind=host|panel|api|deploy

There is no GET .../projects/ROOM_ID/logs without name or container.`,

	"exec": `STORAGE, QUOTA, EXEC, PORTS

GET {{BASE}}/api/v1/quota  (same as /storage + hint)
PATCH {{BASE}}/api/v1/projects/ROOM_ID  {"quota_gb":20}
  400 quota_exceeds_available | quota_required
POST {{BASE}}/api/v1/projects/ROOM_ID/exec
  {"command":"ls -la","container_id":"CONTAINER_ID"}
  200 {output, exit_code}  400 command required
GET {{BASE}}/api/v1/ports
  200 {used_ports, panel_port:9090}

ENV: GET/POST/DELETE {{BASE}}/api/v1/projects/ROOM_ID/env
VOLUMES: GET/POST {{BASE}}/api/v1/projects/ROOM_ID/volumes
IMAGES: GET {{BASE}}/api/v1/projects/ROOM_ID/images  POST .../images/load
COMPOSE: GET .../compose  GET .../compose/validate  POST .../stack/start|stop|restart|remove
STATUS: GET {{BASE}}/api/v1/status  (includes per-room cpu/ram/storage)
AGENT: POST {{BASE}}/api/v1/agent  {"tool":"list_rooms"}
EXEC waits until the command ends. stdout, stderr, exit_code. No 2-minute cap.
Create room: generate_password, domain, ssl, ssh_certificate optional.

Never DELETE rooms via API.`,
}

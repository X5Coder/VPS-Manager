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

func (s *Server) tokenCopyFields(base, secret string) (prompt, api, script string) {
	script = buildGitHubWorkflow(base, secret)
	api = s.buildAPISheet(base, secret)
	prompt = s.buildAPIPrompt(base, secret, script)
	return
}

func buildGitHubWorkflow(base, secret string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	secret = strings.TrimSpace(secret)
	if secret == "" {
		secret = "YOUR_SECRET"
	}
	return fmt.Sprintf(`# VPS Manager — one API for ALL rooms
# Save as: .github/workflows/vps-deploy.yml  (keep the repo PRIVATE)
# Set ROOM_ID to the room you want to update (GET BASE/api/v1/projects).
# Or run the workflow manually and type the room id.
# Build → docker save app.tar → POST /upload. No GHCR.

name: Deploy to VPS
on:
  push:
    branches: [main, master]
  workflow_dispatch:
    inputs:
      room_id:
        description: "Room id to update (from GET /api/v1/projects)"
        required: true
        type: string

env:
  VPS_BASE: %q
  VPS_TOKEN: %q
  ROOM_ID: "PASTE_ROOM_ID_HERE"

jobs:
  deploy:
    runs-on: ubuntu-latest
    timeout-minutes: 360
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@v4

      - name: Room id
        run: |
          RID="${{ github.event.inputs.room_id }}"
          if [ -n "$RID" ]; then
            echo "ROOM_ID=$RID" >> "$GITHUB_ENV"
            export ROOM_ID="$RID"
          fi
          if [ -z "$ROOM_ID" ] || [ "$ROOM_ID" = "PASTE_ROOM_ID_HERE" ]; then
            echo "Set ROOM_ID in this file, or type it in Run workflow."
            exit 1
          fi
          echo "Updating room $ROOM_ID"

      - name: Find Dockerfile
        run: |
          for f in Dockerfile dockerfile Containerfile; do
            if [ -f "$f" ]; then echo "DOCKERFILE=$f" >> "$GITHUB_ENV"; exit 0; fi
          done
          echo "DOCKERFILE=Dockerfile" >> "$GITHUB_ENV"

      - name: Note current deploy stamp
        run: |
          python3 - <<'PY'
          import json, os, urllib.request
          base = os.environ["VPS_BASE"].rstrip("/")
          token = os.environ["VPS_TOKEN"]
          pid = os.environ["ROOM_ID"]
          url = base + "/api/v1/projects/" + pid
          req = urllib.request.Request(url, headers={"Authorization": "Bearer " + token})
          try:
              with urllib.request.urlopen(req, timeout=30) as r:
                  data = json.load(r)
              at = data.get("last_deploy_at") or ""
          except Exception as e:
              print("stamp wait:", e, flush=True)
              at = ""
          with open(os.environ["GITHUB_ENV"], "a") as f:
              f.write("PREV_DEPLOY_AT=" + at + "\n")
          print("PREV_DEPLOY_AT=" + at, flush=True)
          PY

      - name: Build Docker image
        run: docker build -f "$DOCKERFILE" -t "vps-ci:${GITHUB_SHA}" .

      - name: docker save -o app.tar
        run: |
          docker save -o app.tar "vps-ci:${GITHUB_SHA}"
          ls -lh app.tar

      - name: POST app.tar to VPS Manager
        run: |
          curl -fS --connect-timeout 30 --max-time 21600 \
            -H "Authorization: Bearer ${VPS_TOKEN}" \
            -F "file=@app.tar;filename=app.tar;type=application/octet-stream" \
            "${VPS_BASE}/api/v1/projects/${ROOM_ID}/upload"
          echo
          echo "VPS accepted tar upload. Waiting until running..."

      - name: Wait until project is running
        timeout-minutes: 60
        run: |
          python3 - <<'PY'
          import json, os, time, urllib.request
          base = os.environ["VPS_BASE"].rstrip("/")
          token = os.environ["VPS_TOKEN"]
          pid = os.environ["ROOM_ID"]
          prev = os.environ.get("PREV_DEPLOY_AT") or ""
          url = base + "/api/v1/projects/" + pid
          req = urllib.request.Request(url, headers={"Authorization": "Bearer " + token})
          for i in range(180):
              try:
                  with urllib.request.urlopen(req, timeout=30) as r:
                      data = json.load(r)
              except Exception as e:
                  print("poll", i + 1, "wait:", e, flush=True)
                  time.sleep(5)
                  continue
              st = data.get("status") or ""
              img = data.get("image") or ""
              at = data.get("last_deploy_at") or ""
              ok = data.get("last_deploy_ok")
              print("poll", i + 1, "status=" + st, "image=" + img, "ok=" + str(ok), "at=" + at, flush=True)
              if at and at != prev:
                  if ok is False or st == "error":
                      print("last_deploy_error:", data.get("last_deploy_error"), flush=True)
                      raise SystemExit(1)
                  if st == "running" and ok is not False:
                      print("UPDATED — project is running automatically", flush=True)
                      raise SystemExit(0)
              time.sleep(5)
          raise SystemExit("timeout waiting for running")
          PY
`, base, secret)
}

func (s *Server) buildAPISheet(base, secret string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	secret = strings.TrimSpace(secret)
	if secret == "" {
		secret = "YOUR_SECRET"
	}
	return fmt.Sprintf("BASE=%s\nTOKEN=%s\nAuthorization: Bearer %s\n", base, secret, secret)
}

func (s *Server) buildAPIPrompt(base, secret, script string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	secret = strings.TrimSpace(secret)
	if secret == "" {
		secret = "YOUR_SECRET"
	}
	if strings.TrimSpace(script) == "" {
		script = buildGitHubWorkflow(base, secret)
	}
	return strings.NewReplacer(
		"{{BASE}}", base,
		"{{TOKEN}}", secret,
		"{{SCRIPT}}", strings.TrimSpace(script),
	).Replace(`You are the VPS Manager API operator for this panel. One token controls ALL rooms. Paste this whole prompt into an AI. Match the user's language (Arabic or English — do not mix). Be precise. Never invent BASE, TOKEN, room ids, GB, or passwords. Never DELETE. Never guess a secret.

AUTH (use these exact values)
  BASE={{BASE}}
  TOKEN={{TOKEN}}
  Header: Authorization: Bearer {{TOKEN}}

ROOM_ID (the variable you fill in)
  ROOM_ID=PASTE_ROOM_ID_HERE
  This is NOT the token. It is the room/project id from GET /api/v1/projects (field "id").
  Before any room-specific curl or GitHub deploy: list rooms, copy the id, replace PASTE_ROOM_ID_HERE.
  Same ROOM_ID for: first image on an empty room, and later updates of that room.
  GitHub: set env ROOM_ID in the YAML below, or type it in Actions → Run workflow.

HOW TO ANSWER
  Teach with working curl. If they ask usage/quota/list/create/update: call or show the exact request, then explain the JSON.
  status=empty means no container yet — upload a docker-save tar (panel or GitHub) onto that same id.
  Publish unit is a Docker image. Never git/npm/build inside the running container.

1) LIST ALL ROOMS (name, id, quota, usage, status)
curl -sS -H "Authorization: Bearer {{TOKEN}}" {{BASE}}/api/v1/projects
  projects[]: id, name, status (empty|running|stopped|deploying|error), quota_gb, usage_gb, quota_bytes, usage_bytes, image, host_port
  storage: disk_total, disk_used, disk_free, quota_reserved, quota_available_gb

2) ONE ROOM (quota + usage + status)
curl -sS -H "Authorization: Bearer {{TOKEN}}" {{BASE}}/api/v1/projects/$ROOM_ID

3) HOST DISK (pick a new room size from this)
curl -sS -H "Authorization: Bearer {{TOKEN}}" {{BASE}}/api/v1/storage
  quota_gb for a new room must be > 0 and ≤ quota_available_gb.

4) CREATE EMPTY ROOM (no container yet — name + disk from free space + password)
curl -sS -H "Authorization: Bearer {{TOKEN}}" -H "Content-Type: application/json" \
  -d '{"name":"my-app","quota_gb":10,"password":"at-least-6-chars","container_port":8080}' \
  {{BASE}}/api/v1/projects
  Returns id + status=empty. Then set ROOM_ID to that id and upload (step 5 or GitHub).

5) UPLOAD IMAGE TAR (first deploy on empty room OR update existing)
  Build locally: docker build -t myapp:latest . && docker save -o app.tar myapp:latest
curl -fS -H "Authorization: Bearer {{TOKEN}}" \
  -F "file=@app.tar;filename=app.tar;type=application/octet-stream" \
  {{BASE}}/api/v1/projects/$ROOM_ID/upload
  Poll GET until status=running and last_deploy_ok. Same id, quota, ports, domain, .env.

6) GITHUB ACTION (same as Copy script)
  Save the YAML at the bottom as .github/workflows/vps-deploy.yml (repo PRIVATE).
  Put the room id in ROOM_ID: "PASTE_ROOM_ID_HERE" (or type it on Run workflow).
  Push: docker build → docker save app.tar → POST /upload. No GHCR.
  First image and later updates use the same file — only ROOM_ID changes.

7) CHANGE QUOTA / NAME / PASSWORD
curl -sS -H "Authorization: Bearer {{TOKEN}}" -H "Content-Type: application/json" \
  -d '{"quota_gb":20}' \
  -X PATCH {{BASE}}/api/v1/projects/$ROOM_ID
  Also allowed: {"name":"new-name"}  {"password":"new-pass-6+"}

8) EXEC A COMMAND
curl -sS -H "Authorization: Bearer {{TOKEN}}" -H "Content-Type: application/json" \
  -d '{"command":"ps aux"}' \
  {{BASE}}/api/v1/projects/$ROOM_ID/exec
  If the room is empty (no container), exec runs on the room folder, not Docker.

9) USED PORTS
curl -sS -H "Authorization: Bearer {{TOKEN}}" {{BASE}}/api/v1/ports

PANEL UI
  Open the room by id → First image / Update image → drop app.tar.
  Tokens page: Copy API = BASE+TOKEN only. Copy script = YAML only. This prompt = everything + YAML.

===== BEGIN FILE .github/workflows/vps-deploy.yml =====
Replace PASTE_ROOM_ID_HERE with the room id, then save this file.
{{SCRIPT}}
===== END FILE =====
`)
}

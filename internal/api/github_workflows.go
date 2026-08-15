package api

import (
	"fmt"
	"strings"
)

func githubPollPython() string {
	return `python3 - <<'PY'
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
    at = data.get("last_deploy_at") or ""
    ok = data.get("last_deploy_ok")
    print("poll", i + 1, "status=" + st, "ok=" + str(ok), "at=" + at, flush=True)
    if at and at != prev:
        if ok is False or st == "error":
            print("last_deploy_error:", data.get("last_deploy_error"), flush=True)
            raise SystemExit(1)
        if st == "running" and ok is not False:
            print("UPDATED", flush=True)
            raise SystemExit(0)
    time.sleep(5)
raise SystemExit("timeout waiting for running")
PY`
}

func githubStampPython() string {
	return `python3 - <<'PY'
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
PY`
}

func githubRoomIDStep() string {
	return `          RID="${{ github.event.inputs.room_id }}"
          CID="${{ github.event.inputs.container_id }}"
          if [ -n "$RID" ]; then echo "ROOM_ID=$RID" >> "$GITHUB_ENV"; export ROOM_ID="$RID"; fi
          if [ -n "$CID" ]; then echo "CONTAINER_ID=$CID" >> "$GITHUB_ENV"; fi
          if [ -z "$ROOM_ID" ] || [ "$ROOM_ID" = "PASTE_ROOM_ID_HERE" ]; then
            echo "::error::Set ROOM_ID in this file or type it in Run workflow."
            exit 1
          fi
          echo "Updating room $ROOM_ID"`
}

func buildGitHubWorkflowSingle(base, secret string) string {
	base = trimBase(base)
	secret = trimSecret(secret)
	return fmt.Sprintf(`# VPS Manager — SINGLE image
# Save as: .github/workflows/vps-deploy-single.yml  (repo PRIVATE)
# Builds one Docker image, docker save → app.tar, POST /upload.
# Do NOT use this if the repo has compose + images/*.tar (use vps-deploy-multi.yml).

name: Deploy single image
on:
  push:
    branches: [main, master]
  workflow_dispatch:
    inputs:
      room_id:
        description: "Room id (GET /api/v1/projects)"
        required: true
        type: string
      container_id:
        description: "Optional — update only this container in a multi room"
        required: false
        type: string

env:
  VPS_BASE: %q
  VPS_TOKEN: %q
  ROOM_ID: "PASTE_ROOM_ID_HERE"
  CONTAINER_ID: ""

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
%s

      - name: Reject multi repos
        run: |
          if ls compose.yml compose.yaml docker-compose.yml docker-compose.yaml >/dev/null 2>&1; then
            echo "::error::This repo has a compose file. Use vps-deploy-multi.yml and a .tar.gz package."
            exit 1
          fi
          if [ -d images ] && ls images/*.tar >/dev/null 2>&1; then
            echo "::error::images/*.tar found. This is a multi package. Use vps-deploy-multi.yml."
            exit 1
          fi

      - name: Note current deploy stamp
        run: |
%s

      - name: Build image tar
        run: |
          DF=Dockerfile
          [ -f dockerfile ] && DF=dockerfile
          [ -f Containerfile ] && DF=Containerfile
          docker build -f "$DF" -t "vps-ci:${GITHUB_SHA}" .
          docker save -o app.tar "vps-ci:${GITHUB_SHA}"
          test -f app.tar
          case app.tar in *.tar.gz) echo "::error::single must be .tar"; exit 1;; esac
          ls -lh app.tar

      - name: POST app.tar
        run: |
          EXTRA=()
          if [ -n "${CONTAINER_ID}" ]; then EXTRA+=(-F "container_id=${CONTAINER_ID}"); fi
          curl -fS --connect-timeout 30 --max-time 21600 \
            -H "Authorization: Bearer ${VPS_TOKEN}" \
            -F "file=@app.tar;filename=app.tar;type=application/octet-stream" \
            "${EXTRA[@]}" \
            "${VPS_BASE}/api/v1/projects/${ROOM_ID}/upload"

      - name: Wait until running
        timeout-minutes: 60
        run: |
%s
`, base, secret, githubRoomIDStep(), indentRun(githubStampPython()), indentRun(githubPollPython()))
}

func buildGitHubWorkflowMulti(base, secret string) string {
	base = trimBase(base)
	secret = trimSecret(secret)
	return fmt.Sprintf(`# VPS Manager — MULTI stack
# Save as: .github/workflows/vps-deploy-multi.yml  (repo PRIVATE)
# Packs a .tar.gz in this layout, then POST /upload:
#
#   compose.yml          (any *.yml name at repo root is copied to compose.yml)
#   images/image-01.tar
#   images/image-02.tar
#
# Do NOT use this for a single docker save .tar (use vps-deploy-single.yml).

name: Deploy multi stack
on:
  push:
    branches: [main, master]
  workflow_dispatch:
    inputs:
      room_id:
        description: "Room id (GET /api/v1/projects)"
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
          if [ -n "$RID" ]; then echo "ROOM_ID=$RID" >> "$GITHUB_ENV"; export ROOM_ID="$RID"; fi
          if [ -z "$ROOM_ID" ] || [ "$ROOM_ID" = "PASTE_ROOM_ID_HERE" ]; then
            echo "::error::Set ROOM_ID in this file or type it in Run workflow."
            exit 1
          fi

      - name: Reject single-image repos
        run: |
          if [ ! -d images ] || ! ls images/*.tar >/dev/null 2>&1; then
            echo "::error::Multi needs images/*.tar (docker save each service). For one image use vps-deploy-single.yml."
            exit 1
          fi
          FOUND=
          for f in *.yml *.yaml; do
            [ -f "$f" ] || continue
            case "$f" in *override*) continue;; esac
            FOUND=$f
            break
          done
          if [ -z "$FOUND" ]; then
            echo "::error::Multi needs a .yml compose file at repo root."
            exit 1
          fi
          cp -f "$FOUND" compose.yml
          echo "Using compose file $FOUND → compose.yml"

      - name: Note current deploy stamp
        run: |
%s

      - name: Pack project.vps.tar.gz
        run: |
          tar -czf project.vps.tar.gz compose.yml images
          ls -lh project.vps.tar.gz

      - name: POST project.vps.tar.gz
        run: |
          curl -fS --connect-timeout 30 --max-time 21600 \
            -H "Authorization: Bearer ${VPS_TOKEN}" \
            -F "file=@project.vps.tar.gz;filename=project.vps.tar.gz;type=application/octet-stream" \
            "${VPS_BASE}/api/v1/projects/${ROOM_ID}/upload"

      - name: Wait until running
        timeout-minutes: 60
        run: |
%s
`, base, secret, indentRun(githubStampPython()), indentRun(githubPollPython()))
}

func indentRun(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			b.WriteByte('\n')
			continue
		}
		b.WriteString("          ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func trimBase(base string) string {
	return strings.TrimRight(strings.TrimSpace(base), "/")
}

func trimSecret(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "YOUR_SECRET"
	}
	return secret
}

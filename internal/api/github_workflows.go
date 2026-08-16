package api

import (
	"fmt"
	"strings"
)

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
	return fmt.Sprintf(`# VPS Manager — update a SINGLE room
# Save as: .github/workflows/vps-deploy-single.yml  (repo PRIVATE)
# docker save → POST the tar to /upload. HTTP 200 = file received.
# The room updates in the panel. This job does not wait for docker load.

name: Update room (single image)
on:
  push:
    branches: [main, master]
  workflow_dispatch:
    inputs:
      room_id:
        description: "Room id"
        required: true
        type: string
      container_id:
        description: "Optional — one container in a multi room"
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
    timeout-minutes: 30
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@v4

      - name: Room id
        run: |
%s

      - name: Build app.tar
        run: |
          DF=Dockerfile
          [ -f dockerfile ] && DF=dockerfile
          [ -f Containerfile ] && DF=Containerfile
          docker build -f "$DF" -t "vps-ci:${GITHUB_SHA}" .
          docker save -o app.tar "vps-ci:${GITHUB_SHA}"
          ls -lh app.tar

      - name: POST tar to the room API
        run: |
          EXTRA=()
          if [ -n "${CONTAINER_ID}" ]; then EXTRA+=(-F "container_id=${CONTAINER_ID}"); fi
          curl -fS --connect-timeout 30 --max-time 1800 \
            -H "Authorization: Bearer ${VPS_TOKEN}" \
            -F "file=@app.tar;filename=app.tar" \
            "${EXTRA[@]}" \
            "${VPS_BASE}/api/v1/projects/${ROOM_ID}/upload"
          echo "ACCEPTED — file received. Watch the room in VPS Manager."
`, base, secret, githubRoomIDStep())
}

func buildGitHubWorkflowMulti(base, secret string) string {
	base = trimBase(base)
	secret = trimSecret(secret)
	return fmt.Sprintf(`# VPS Manager — update a MULTI room
# Save as: .github/workflows/vps-deploy-multi.yml  (repo PRIVATE)
# Pack compose.yml + images/*.tar → POST /upload. HTTP 200 = file received.
# The room updates in the panel. This job does not wait for docker load.

name: Update room (multi stack)
on:
  push:
    branches: [main, master]
  workflow_dispatch:
    inputs:
      room_id:
        description: "Room id"
        required: true
        type: string

env:
  VPS_BASE: %q
  VPS_TOKEN: %q
  ROOM_ID: "PASTE_ROOM_ID_HERE"

jobs:
  deploy:
    runs-on: ubuntu-latest
    timeout-minutes: 30
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

      - name: Pack project.vps.tar.gz
        run: |
          FOUND=
          for f in *.yml *.yaml; do
            [ -f "$f" ] || continue
            case "$f" in *override*) continue;; esac
            FOUND=$f
            break
          done
          if [ -z "$FOUND" ]; then
            echo "::error::Need a compose .yml at repo root."
            exit 1
          fi
          if [ ! -d images ] || ! ls images/*.tar >/dev/null 2>&1; then
            echo "::error::Need images/*.tar (docker save each service)."
            exit 1
          fi
          cp -f "$FOUND" compose.yml
          tar -czf project.vps.tar.gz compose.yml images
          ls -lh project.vps.tar.gz

      - name: POST tar.gz to the room API
        run: |
          curl -fS --connect-timeout 30 --max-time 1800 \
            -H "Authorization: Bearer ${VPS_TOKEN}" \
            -F "file=@project.vps.tar.gz;filename=project.vps.tar.gz" \
            "${VPS_BASE}/api/v1/projects/${ROOM_ID}/upload"
          echo "ACCEPTED — file received. Watch the room in VPS Manager."
`, base, secret)
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

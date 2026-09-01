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
	return fmt.Sprintf(`# VPS Manager — update a SINGLE room (image + secrets)
# Save as: .github/workflows/vps-deploy-single.yml  (repo PRIVATE)
# docker save → POST the tar to /upload. HTTP 200 = file received.
# The room updates in the panel. This job does not wait for docker load.
#
# SECRETS — a GitHub Action is the single place that uploads all secrets with
# the project, whether they live in GitHub repo Secrets or in a plain file in
# the repo:
#   * GitHub repo Secrets  → add a line in the "env:" block below
#   * Available in repo    → commit a file named secrets.env (or reference .env)
# The resulting .env is sent as the "env" multipart field and becomes the room
# .env, so ${VAR} substitution inside the container/compose never comes up empty.

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
  # GitHub repo Secrets — add one line per secret you want to ship as env.
  BOT_TOKEN: ${{ secrets.BOT_TOKEN }}
  TELEGRAM_BOT_TOKEN: ${{ secrets.TELEGRAM_BOT_TOKEN }}
  DATABASE_URL: ${{ secrets.DATABASE_URL }}
  API_KEY: ${{ secrets.API_KEY }}

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

      - name: Pack secrets (.env)
        run: |
          : > .env
          # 1) Available in the repo — committed file is used verbatim.
          if [ -f secrets.env ]; then cp secrets.env .env; fi
          # 2) GitHub Secrets — values are empty unless set, so unset keys are skipped.
          {
            [ -n "${BOT_TOKEN:-}" ] && printf 'BOT_TOKEN=%%s\n' "$BOT_TOKEN"
            [ -n "${TELEGRAM_BOT_TOKEN:-}" ] && printf 'TELEGRAM_BOT_TOKEN=%%s\n' "$TELEGRAM_BOT_TOKEN"
            [ -n "${DATABASE_URL:-}" ] && printf 'DATABASE_URL=%%s\n' "$DATABASE_URL"
            [ -n "${API_KEY:-}" ] && printf 'API_KEY=%%s\n' "$API_KEY"
          } >> .env
          echo "--- .env keys (values masked) ---"
          sed 's/=.*/=***/' .env

      - name: Build app.tar
        run: |
          DF=Dockerfile
          [ -f dockerfile ] && DF=dockerfile
          [ -f Containerfile ] && DF=Containerfile
          docker build -f "$DF" -t "vps-ci:${GITHUB_SHA}" .
          docker save -o app.tar "vps-ci:${GITHUB_SHA}"
          ls -lh app.tar

      - name: POST tar + secrets to the room API
        run: |
          EXTRA=()
          if [ -n "${CONTAINER_ID}" ]; then EXTRA+=(-F "container_id=${CONTAINER_ID}"); fi
          curl -fS --connect-timeout 30 --max-time 1800 \
            -H "Authorization: Bearer ${VPS_TOKEN}" \
            -F "env=@.env" \
            -F "file=@app.tar;filename=app.tar" \
            "${EXTRA[@]}" \
            "${VPS_BASE}/api/v1/projects/${ROOM_ID}/upload"
          echo "ACCEPTED — file received. Watch the room in VPS Manager."
`, base, secret, githubRoomIDStep())
}

func buildGitHubWorkflowMulti(base, secret string) string {
	base = trimBase(base)
	secret = trimSecret(secret)
	return fmt.Sprintf(`# VPS Manager — update a MULTI room (stack + secrets)
# Save as: .github/workflows/vps-deploy-multi.yml  (repo PRIVATE)
# Pack compose.yml + images/*.tar → POST /upload. HTTP 200 = file received.
# The room updates in the panel. This job does not wait for docker load.
#
# SECRETS — a GitHub Action is the single place that uploads all secrets with
# the project, whether they live in GitHub repo Secrets or in a plain file in
# the repo:
#   * GitHub repo Secrets  → add a line in the "env:" block below
#   * Available in repo    → commit a file named secrets.env (or reference .env)
# The resulting .env is sent as the "env" multipart field and becomes the room
# .env, so ${VAR} substitution inside compose never comes up empty.

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
  # GitHub repo Secrets — add one line per secret you want to ship as env.
  BOT_TOKEN: ${{ secrets.BOT_TOKEN }}
  TELEGRAM_BOT_TOKEN: ${{ secrets.TELEGRAM_BOT_TOKEN }}
  DATABASE_URL: ${{ secrets.DATABASE_URL }}
  API_KEY: ${{ secrets.API_KEY }}

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

      - name: Pack secrets (.env)
        run: |
          : > .env
          # 1) Available in the repo — committed file is used verbatim.
          if [ -f secrets.env ]; then cp secrets.env .env; fi
          # 2) GitHub Secrets — values are empty unless set, so unset keys are skipped.
          {
            [ -n "${BOT_TOKEN:-}" ] && printf 'BOT_TOKEN=%%s\n' "$BOT_TOKEN"
            [ -n "${TELEGRAM_BOT_TOKEN:-}" ] && printf 'TELEGRAM_BOT_TOKEN=%%s\n' "$TELEGRAM_BOT_TOKEN"
            [ -n "${DATABASE_URL:-}" ] && printf 'DATABASE_URL=%%s\n' "$DATABASE_URL"
            [ -n "${API_KEY:-}" ] && printf 'API_KEY=%%s\n' "$API_KEY"
          } >> .env
          echo "--- .env keys (values masked) ---"
          sed 's/=.*/=***/' .env

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

      - name: POST tar.gz + secrets to the room API
        run: |
          curl -fS --connect-timeout 30 --max-time 1800 \
            -H "Authorization: Bearer ${VPS_TOKEN}" \
            -F "env=@.env" \
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

// buildGitHubWorkflowAuto is a single Action that (1) detects whether the repo
// is a compose stack (multi) or a single image, (2) reuses the given room id or
// creates a new room of the right kind and uploads to it, and (3) ships all
// secrets (GitHub Secrets + a repo secrets.env) with the project.
func buildGitHubWorkflowAuto(base, secret string) string {
	base = trimBase(base)
	secret = trimSecret(secret)
	return fmt.Sprintf(`# VPS Manager — deploy (auto single/multi, creates a room if needed)
# Save as: .github/workflows/vps-deploy.yml  (repo PRIVATE)
# The Action decides everything itself:
#   * KIND   → "multi" when compose.yml + images/*.tar exist, else "single" (docker build)
#   * ROOM   → uses ROOM_ID below/at run, or creates a new room of the right kind
#   * SECRETS→ GitHub repo Secrets + a plain secrets.env file; sent as the "env" field
#
#   ROOM_ID: leave empty to create a new room on each run (name + quota_gb used).
#   KIND is detected, so a single room never receives a compose stack and vice versa.

name: Deploy to VPS
on:
  push:
    branches: [main, master]
  workflow_dispatch:
    inputs:
      room_id:
        description: "Room id (empty = create a new room)"
        required: false
        type: string
      name:
        description: "Room name (used when creating)"
        required: false
        type: string
      quota_gb:
        description: "Disk quota in GB (used when creating)"
        required: false
        type: string
        default: "5"

env:
  VPS_BASE: %q
  VPS_TOKEN: %q
  ROOM_ID: ""
  NAME: "app"
  QUOTA_GB: "5"
  # GitHub repo Secrets — add one line per secret you want to ship as env.
  BOT_TOKEN: ${{ secrets.BOT_TOKEN }}
  TELEGRAM_BOT_TOKEN: ${{ secrets.TELEGRAM_BOT_TOKEN }}
  DATABASE_URL: ${{ secrets.DATABASE_URL }}
  API_KEY: ${{ secrets.API_KEY }}

jobs:
  deploy:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@v4

      - name: Detect kind (multi vs single)
        id: kind
        run: |
          MODE=single
          if [ -d images ] && ls images/*.tar* >/dev/null 2>&1 && ls *.yml *.yaml >/dev/null 2>&1; then
            if ls *.yml *.yaml | grep -v -i override >/dev/null 2>&1; then MODE=multi; fi
          fi
          echo "Detected mode: $MODE"
          echo "MODE=$MODE" >> "$GITHUB_ENV"

      - name: Decide ROOM_ID (reuse or create)
        env:
          RID: ${{ github.event.inputs.room_id }}
          NAME_IN: ${{ github.event.inputs.name }}
          QUOTA_IN: ${{ github.event.inputs.quota_gb }}
        run: |
          RID="${RID:-$ROOM_ID}"
          NAME="${NAME_IN:-$NAME}"
          QUOTA="${QUOTA_IN:-$QUOTA_GB}"
          NAME="${NAME:-app}"
          NAME="${NAME}_${GITHUB_SHA:0:8}"
          if [ -n "$RID" ] && [ "$RID" != "PASTE_ROOM_ID_HERE" ]; then
            echo "Using existing room $RID"
            echo "ROOM_ID=$RID" >> "$GITHUB_ENV"
            exit 0
          fi
          PASS="$(python3 -c 'import secrets;print(secrets.token_hex(6))')"
          echo "Creating a new $MODE room …"
          RESP="$(curl -fsS -X POST "${VPS_BASE}/api/v1/projects" \
            -H "Authorization: Bearer ${VPS_TOKEN}" -H "Content-Type: application/json" \
            -d "{\"name\":\"$NAME\",\"kind\":\"$MODE\",\"quota_gb\":${QUOTA},\"password\":\"$PASS\"}")"
          RID="$(echo "$RESP" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d.get("room",d.get("project",{})).get("id",""))')"
          if [ -z "$RID" ] || [ "$RID" = "None" ]; then
            echo "::error::create room failed: $RESP"
            exit 1
          fi
          echo "Created room id=$RID (kind=$MODE) password=$PASS — save the password (room/web)."
          echo "ROOM_ID=$RID" >> "$GITHUB_ENV"

      - name: Pack secrets (.env)
        run: |
          : > .env
          if [ -f secrets.env ]; then cp secrets.env .env; fi
          {
            [ -n "${BOT_TOKEN:-}" ] && echo "BOT_TOKEN=$BOT_TOKEN"
            [ -n "${TELEGRAM_BOT_TOKEN:-}" ] && echo "TELEGRAM_BOT_TOKEN=$TELEGRAM_BOT_TOKEN"
            [ -n "${DATABASE_URL:-}" ] && echo "DATABASE_URL=$DATABASE_URL"
            [ -n "${API_KEY:-}" ] && echo "API_KEY=$API_KEY"
          } >> .env
          echo "--- .env keys (values masked) ---"
          sed 's/=.*/=***/' .env

      - name: Build / pack ${{ env.MODE }}
        run: |
          if [ "$MODE" = "multi" ]; then
            FOUND=""
            for f in *.yml *.yaml; do
              [ -f "$f" ] || continue
              case "$f" in *override*) continue;; esac
              FOUND="$f"; break
            done
            [ -n "$FOUND" ] || { echo "::error::No compose .yml at repo root."; exit 1; }
            cp -f "$FOUND" compose.yml
            tar -czf project.vps.tar.gz compose.yml images
            ls -lh project.vps.tar.gz
          else
            DF=Dockerfile
            [ -f dockerfile ] && DF=dockerfile
            [ -f Containerfile ] && DF=Containerfile
            docker build -f "$DF" -t "vps-ci:${GITHUB_SHA}" .
            docker save -o app.tar "vps-ci:${GITHUB_SHA}"
            ls -lh app.tar
          fi

      - name: Upload to the room (with secrets)
        run: |
          FILE=app.tar
          [ "$MODE" = "multi" ] && FILE=project.vps.tar.gz
          curl -fS --connect-timeout 30 --max-time 1800 \
            -H "Authorization: Bearer ${VPS_TOKEN}" \
            -F "env=@.env" \
            -F "file=@${FILE};filename=${FILE}" \
            "${VPS_BASE}/api/v1/projects/${ROOM_ID}/upload"
          echo "ACCEPTED — file received (kind=${MODE}). Watch room in VPS Manager."
`, base, secret)
}

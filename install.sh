#!/usr/bin/env bash
# VPS MANAGE — install on a new Linux VPS.
# Usage (as root):
#   curl -fsSL https://raw.githubusercontent.com/X5Coder/VPS-Manager/main/install.sh | bash
set -euo pipefail

REPO_URL="${VPS_MANAGER_REPO:-https://github.com/X5Coder/VPS-Manager.git}"
BRANCH="${VPS_MANAGER_BRANCH:-main}"
PANEL_DIR="${PANEL_DIR:-/opt/vps-rooms}"
SRC_DIR="${PANEL_DIR}/src"

if [[ "$(id -u)" -ne 0 ]]; then
  echo "Run as root."
  exit 1
fi

export DEBIAN_FRONTEND=noninteractive

echo "==> VPS MANAGE install"
echo "    dir: ${PANEL_DIR}"
echo "    repo: ${REPO_URL} (${BRANCH})"

if command -v apt-get >/dev/null 2>&1; then
  apt-get update -y
  apt-get install -y ca-certificates curl git openssl
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y ca-certificates curl git openssl
elif command -v yum >/dev/null 2>&1; then
  yum install -y ca-certificates curl git openssl
else
  echo "Need apt, dnf, or yum to install curl/git."
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "==> Installing Docker Engine"
  curl -fsSL https://get.docker.com | sh
fi
systemctl enable --now docker 2>/dev/null || service docker start 2>/dev/null || true

for i in $(seq 1 30); do
  if docker info >/dev/null 2>&1; then
    break
  fi
  if [[ "$i" -eq 30 ]]; then
    echo "Docker did not become ready."
    exit 1
  fi
  sleep 2
done

if ! docker compose version >/dev/null 2>&1; then
  echo "==> Installing Docker Compose plugin"
  if command -v apt-get >/dev/null 2>&1; then
    apt-get install -y docker-compose-plugin || true
  fi
fi
if ! docker compose version >/dev/null 2>&1; then
  echo "docker compose is required."
  exit 1
fi

LOCAL_SRC=""
if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
  maybe="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  if [[ -f "${maybe}/deploy/docker-compose.yml" ]]; then
    LOCAL_SRC="${maybe}"
  fi
fi

mkdir -p "${PANEL_DIR}/data/secrets" "${PANEL_DIR}/rooms" "${PANEL_DIR}/runtime" "${PANEL_DIR}/volumes" "${PANEL_DIR}/proxy"
chmod 700 "${PANEL_DIR}/data/secrets"

if [[ -n "${LOCAL_SRC}" ]]; then
  echo "==> Using local source ${LOCAL_SRC}"
  SRC_DIR="${LOCAL_SRC}"
else
  echo "==> Cloning ${REPO_URL}"
  if [[ -d "${SRC_DIR}/.git" ]]; then
    git -C "${SRC_DIR}" fetch --depth 1 origin "${BRANCH}"
    git -C "${SRC_DIR}" checkout -f "${BRANCH}"
    git -C "${SRC_DIR}" reset --hard "origin/${BRANCH}"
  else
    rm -rf "${SRC_DIR}"
    git clone --depth 1 --branch "${BRANCH}" "${REPO_URL}" "${SRC_DIR}"
  fi
fi

OWNER_ENV="${PANEL_DIR}/data/secrets/owner.env"
CREATED_PASS=""
if [[ ! -f "${OWNER_ENV}" ]] || ! grep -q '^VPS_ROOMS_OWNER_PASS=.' "${OWNER_ENV}" 2>/dev/null; then
  CREATED_PASS="$(openssl rand -base64 24 | tr -dc 'A-Za-z0-9' | head -c 20)"
  umask 077
  printf 'VPS_ROOMS_OWNER_PASS=%s\n' "${CREATED_PASS}" > "${OWNER_ENV}"
  chmod 600 "${OWNER_ENV}"
fi

if command -v ufw >/dev/null 2>&1; then
  ufw allow 22/tcp comment "ssh" || true
  ufw allow 80/tcp comment "vps-manage-http" || true
  ufw allow 443/tcp comment "vps-manage-https" || true
  ufw allow 9090/tcp comment "vps-manage-panel" || true
fi

echo "==> Building and starting panel container"
(cd "${SRC_DIR}" && docker compose -f deploy/docker-compose.yml up -d --build)

ok=0
for i in $(seq 1 40); do
  if curl -fsS --max-time 3 http://127.0.0.1:9090/api/health >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep 2
done

IP="$(curl -fsS --max-time 4 https://api.ipify.org 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}' || echo YOUR_VPS_IP)"

echo
if [[ "${ok}" -eq 1 ]]; then
  echo "Install complete."
else
  echo "Container started; health check still warming up."
  echo "Logs: docker logs vps-manager"
fi
echo "Panel:  http://${IP}:9090"
if [[ -n "${CREATED_PASS}" ]]; then
  echo "Admin password (save this): ${CREATED_PASS}"
else
  echo "Admin password: already set in ${OWNER_ENV}"
fi
echo "Unlock with a Telegram bot token, then sign in and change the admin password in Settings."

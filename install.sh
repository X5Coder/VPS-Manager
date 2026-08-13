#!/usr/bin/env bash
# VPS MANAGE — install on a new Linux VPS.
# Usage (as root):
#   curl -fsSL https://raw.githubusercontent.com/X5Coder/VPS-Manager/main/install.sh | bash
set -euo pipefail

REPO_URL="${VPS_MANAGER_REPO:-https://github.com/X5Coder/VPS-Manager.git}"
BRANCH="${VPS_MANAGER_BRANCH:-main}"
TARBALL_URL="${VPS_MANAGER_TARBALL:-https://github.com/X5Coder/VPS-Manager/archive/refs/heads/${BRANCH}.tar.gz}"
PANEL_DIR="${PANEL_DIR:-/opt/vps-rooms}"
SRC_DIR="${PANEL_DIR}/src"

retry() {
  local max="$1" n=0 delay=3
  shift
  until "$@"; do
    n=$((n + 1))
    if [[ "$n" -ge "$max" ]]; then
      echo "FAILED after ${max} tries: $*"
      return 1
    fi
    echo "download/command failed — retry ${n}/${max} in ${delay}s"
    sleep "$delay"
    delay=$((delay * 2))
    if [[ "$delay" -gt 30 ]]; then delay=30; fi
  done
}

if [[ "$(id -u)" -ne 0 ]]; then
  echo "Run as root:  sudo -i   then paste the install command again."
  exit 1
fi

if [[ -f /etc/os-release ]]; then
  # shellcheck disable=SC1091
  . /etc/os-release
fi
if [[ "${ID:-}" != "ubuntu" ]]; then
  echo "This installer supports Ubuntu 20.04, 22.04, and 24.04 only."
  exit 1
fi
echo "==> Ubuntu ${VERSION_ID:-?} detected"

export DEBIAN_FRONTEND=noninteractive

echo "==> VPS MANAGE install"
echo "    dir:  ${PANEL_DIR}"
echo "    repo: ${REPO_URL} (${BRANCH})"
echo
echo "    After Docker is up this script will stop and ask you to:"
echo "      1) create a panel password     (required, saved once)"
echo "      2) enter your Telegram user id (required, saved once)"
echo "    Only then is the panel URL printed."
echo

if command -v apt-get >/dev/null 2>&1; then
  retry 8 apt-get update -y
  retry 8 apt-get install -y ca-certificates curl git openssl tar
else
  echo "Need apt (Ubuntu 20.04 / 22.04 / 24.04)."
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "==> Installing Docker Engine"
  retry 6 bash -c 'curl -fsSL https://get.docker.com | sh'
fi
systemctl enable --now docker 2>/dev/null || service docker start 2>/dev/null || true

for i in $(seq 1 40); do
  if docker info >/dev/null 2>&1; then
    break
  fi
  if [[ "$i" -eq 40 ]]; then
    echo "Docker did not become ready. Re-run this installer — it will continue from here."
    exit 1
  fi
  sleep 2
done

if ! docker compose version >/dev/null 2>&1; then
  echo "==> Installing Docker Compose plugin"
  if command -v apt-get >/dev/null 2>&1; then
    retry 5 apt-get install -y docker-compose-plugin || true
  elif command -v dnf >/dev/null 2>&1; then
    retry 5 dnf install -y docker-compose-plugin || true
  fi
fi
if ! docker compose version >/dev/null 2>&1; then
  echo "==> Installing Compose CLI plugin from GitHub"
  mkdir -p /usr/local/lib/docker/cli-plugins
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) arch="x86_64" ;;
    aarch64|arm64) arch="aarch64" ;;
  esac
  retry 6 curl -fsSL -o /usr/local/lib/docker/cli-plugins/docker-compose \
    "https://github.com/docker/compose/releases/latest/download/docker-compose-linux-${arch}"
  chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
fi
if ! docker compose version >/dev/null 2>&1; then
  echo "docker compose is required and could not be installed."
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

fetch_source() {
  if [[ -d "${SRC_DIR}/.git" ]]; then
    git -C "${SRC_DIR}" fetch --depth 1 origin "${BRANCH}" \
      && git -C "${SRC_DIR}" checkout -f "${BRANCH}" \
      && git -C "${SRC_DIR}" reset --hard "origin/${BRANCH}"
    return $?
  fi
  rm -rf "${SRC_DIR}"
  git clone --depth 1 --branch "${BRANCH}" "${REPO_URL}" "${SRC_DIR}"
}

fetch_tarball() {
  echo "==> Git clone failed — downloading zip/tarball instead"
  rm -rf "${SRC_DIR}"
  local tmp
  tmp="$(mktemp -d)"
  retry 8 curl -fsSL -o "${tmp}/src.tar.gz" "${TARBALL_URL}"
  tar -xzf "${tmp}/src.tar.gz" -C "${tmp}"
  local unpacked
  unpacked="$(find "${tmp}" -mindepth 1 -maxdepth 1 -type d | head -1)"
  mkdir -p "${PANEL_DIR}"
  mv "${unpacked}" "${SRC_DIR}"
  rm -rf "${tmp}"
  test -f "${SRC_DIR}/deploy/docker-compose.yml"
}

if [[ -n "${LOCAL_SRC}" ]]; then
  echo "==> Using local source ${LOCAL_SRC}"
  SRC_DIR="${LOCAL_SRC}"
else
  echo "==> Downloading source"
  if ! retry 6 fetch_source; then
    fetch_tarball
  fi
fi
test -f "${SRC_DIR}/deploy/docker-compose.yml"

OWNER_ENV="${PANEL_DIR}/data/secrets/owner.env"
# Password and Telegram id are collected after a successful start — never skipped on first run.

if command -v ufw >/dev/null 2>&1; then
  ufw allow 22/tcp comment "ssh" || true
  ufw allow 80/tcp comment "vps-manage-http" || true
  ufw allow 443/tcp comment "vps-manage-https" || true
  ufw allow 9090/tcp comment "vps-manage-panel" || true
fi

echo "==> Building and starting panel container (retries if download fails)"
compose_up() {
  (cd "${SRC_DIR}" && docker compose -f deploy/docker-compose.yml up -d --build)
}
retry 5 compose_up

read_tty() {
  local silent="${1:-0}" prompt="$2" value=""
  if [[ ! -r /dev/tty ]]; then
    echo
    echo "No interactive terminal. Download the script and run it as root:"
    echo "  bash ${SRC_DIR}/install.sh"
    echo "or:  curl -fsSL https://raw.githubusercontent.com/X5Coder/VPS-Manager/main/install.sh | bash"
    echo "from an SSH session (not a pipe without /dev/tty)."
    exit 1
  fi
  if [[ "$silent" == "1" ]]; then
    IFS= read -r -s -p "$prompt" value </dev/tty || true
    echo >/dev/tty
  else
    IFS= read -r -p "$prompt" value </dev/tty || true
  fi
  printf '%s' "$value"
}

need_first_setup() {
  if [[ ! -f "${OWNER_ENV}" ]] || ! grep -q '^VPS_ROOMS_OWNER_PASS=.' "${OWNER_ENV}" 2>/dev/null; then
    return 0
  fi
  if [[ ! -f "${PANEL_DIR}/data/secrets/telegram.env" ]] || ! grep -qE '^TELEGRAM_CHAT_ID=-?[0-9]+' "${PANEL_DIR}/data/secrets/telegram.env" 2>/dev/null; then
    return 0
  fi
  return 1
}

ask_admin_setup() {
  local pass pass2 chat
  echo
  echo "=============================================="
  echo "  First-time setup (required)"
  echo "=============================================="
  echo "  These are saved on this VPS and used to sign in."
  echo

  echo "-- Step 1 of 2 · Panel password --"
  echo "    This is the admin password for the web panel. Minimum 8 characters."
  echo
  while true; do
    pass="$(read_tty 1 "Create panel password: ")"
    if [[ ${#pass} -lt 8 ]]; then
      echo "    Password must be at least 8 characters. Try again."
      continue
    fi
    pass2="$(read_tty 1 "Confirm password: ")"
    if [[ "$pass" != "$pass2" ]]; then
      echo "    Passwords do not match. Try again."
      continue
    fi
    break
  done

  echo
  echo "-- Step 2 of 2 · Telegram user id --"
  echo "    Open Telegram → search @userinfobot → tap Start → copy the Id number."
  echo
  while true; do
    chat="$(read_tty 0 "Telegram user id: ")"
    chat="${chat//[[:space:]]/}"
    if [[ "$chat" =~ ^-?[0-9]+$ ]] && [[ -n "$chat" ]]; then
      break
    fi
    echo "    Required. Use the numeric id from @userinfobot (digits only)."
  done

  umask 077
  printf 'VPS_ROOMS_OWNER_PASS=%s\n' "$pass" > "${OWNER_ENV}"
  chmod 600 "${OWNER_ENV}"
  local tok=""
  if [[ -f "${PANEL_DIR}/data/secrets/telegram.env" ]]; then
    tok="$(grep '^TELEGRAM_BOT_TOKEN=' "${PANEL_DIR}/data/secrets/telegram.env" | head -1 | cut -d= -f2- || true)"
  fi
  cat > "${PANEL_DIR}/data/secrets/telegram.env" <<EOF
# Locked at install. Do not edit.
TELEGRAM_CHAT_ID=${chat}
TELEGRAM_CHAT_LOCKED=1
TELEGRAM_BOT_TOKEN=${tok}
EOF
  chmod 600 "${PANEL_DIR}/data/secrets/telegram.env"
  chmod 700 "${PANEL_DIR}/data/secrets"
  echo
  echo "    Saved. Restarting the panel to load them…"
  docker restart vps-manager >/dev/null 2>&1 || true
  sleep 2
}

ok=0
for i in $(seq 1 40); do
  if curl -fsS --max-time 3 http://127.0.0.1:9090/api/health >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep 2
done

if [[ "${ok}" -ne 1 ]]; then
  echo "Container started; health check still warming up."
  echo "Logs: docker logs vps-manager"
  echo "Re-run the same install command when Docker is ready."
  echo "It will continue and then ask for the panel password and Telegram id."
  exit 1
fi

if need_first_setup; then
  ask_admin_setup
  ok=0
  for i in $(seq 1 20); do
    if curl -fsS --max-time 3 http://127.0.0.1:9090/api/health >/dev/null 2>&1; then
      ok=1
      break
    fi
    sleep 1
  done
else
  echo "Panel password and Telegram id are already saved — keeping them."
fi

IP="$(curl -fsS --max-time 4 https://api.ipify.org 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}' || echo YOUR_VPS_IP)"

echo
echo "=============================================="
echo "  Panel is ready"
echo "=============================================="
echo
echo "  Panel URL:  http://${IP}:9090"
echo
echo "  Open that link."
echo "  Unlock with a Telegram bot token, then sign in with the panel password."
echo

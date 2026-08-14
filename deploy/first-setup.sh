# Shared first-time panel setup (password + Telegram id + URL).
# Used by install.sh. PANEL_DIR must be set.
# Optional: VPS_FIRST_SETUP_RESTART — command after saving (default: docker restart vps-manager)

vps_read_tty() {
  local silent="${1:-0}" prompt="$2" value=""
  if [[ ! -r /dev/tty ]]; then
    echo
    echo "No interactive terminal. Run this from a real SSH/TTY session."
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

vps_need_first_setup() {
  local owner_env="${PANEL_DIR}/data/secrets/owner.env"
  local tg_env="${PANEL_DIR}/data/secrets/telegram.env"
  if [[ ! -f "${owner_env}" ]] || ! grep -q '^VPS_ROOMS_OWNER_PASS=.' "${owner_env}" 2>/dev/null; then
    return 0
  fi
  if [[ ! -f "${tg_env}" ]] || ! grep -qE '^TELEGRAM_CHAT_ID=-?[0-9]+' "${tg_env}" 2>/dev/null; then
    return 0
  fi
  return 1
}

vps_ask_admin_setup() {
  local pass pass2 chat
  local owner_env="${PANEL_DIR}/data/secrets/owner.env"
  local tg_env="${PANEL_DIR}/data/secrets/telegram.env"
  mkdir -p "${PANEL_DIR}/data/secrets"
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
    pass="$(vps_read_tty 1 "Create panel password: ")"
    if [[ ${#pass} -lt 8 ]]; then
      echo "    Password must be at least 8 characters. Try again."
      continue
    fi
    pass2="$(vps_read_tty 1 "Confirm password: ")"
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
    chat="$(vps_read_tty 0 "Telegram user id: ")"
    chat="${chat//[[:space:]]/}"
    if [[ "$chat" =~ ^-?[0-9]+$ ]] && [[ -n "$chat" ]]; then
      break
    fi
    echo "    Required. Use the numeric id from @userinfobot (digits only)."
  done

  umask 077
  printf 'VPS_ROOMS_OWNER_PASS=%s\n' "$pass" > "${owner_env}"
  chmod 600 "${owner_env}"
  local tok=""
  if [[ -f "${tg_env}" ]]; then
    tok="$(grep '^TELEGRAM_BOT_TOKEN=' "${tg_env}" | head -1 | cut -d= -f2- || true)"
  fi
  cat > "${tg_env}" <<EOF
# Locked at install. Do not edit.
TELEGRAM_CHAT_ID=${chat}
TELEGRAM_CHAT_LOCKED=1
TELEGRAM_BOT_TOKEN=${tok}
EOF
  chmod 600 "${tg_env}"
  chmod 700 "${PANEL_DIR}/data/secrets"
  echo
  echo "    Saved. Restarting the panel to load them…"
  if [[ -n "${VPS_FIRST_SETUP_RESTART:-}" ]]; then
    bash -c "${VPS_FIRST_SETUP_RESTART}" >/dev/null 2>&1 || true
  else
    docker restart vps-manager >/dev/null 2>&1 || true
  fi
  sleep 2
}

vps_print_panel_ready() {
  local ip
  ip="$(curl -fsS --max-time 4 https://api.ipify.org 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}' || echo YOUR_VPS_IP)"
  echo
  echo "=============================================="
  echo "  Panel is ready"
  echo "=============================================="
  echo
  echo "  Panel URL:  http://${ip}:9090"
  echo
  echo "  Open that link."
  echo "  Unlock with a Telegram bot token, then sign in with the panel password."
  echo
}

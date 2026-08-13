#!/usr/bin/env bash
# Fast host install: prebuilt binary only. Does NOT touch other projects/containers.
set -euo pipefail

PANEL_DIR="${PANEL_DIR:-/opt/vps-rooms}"
BIN_SRC="${1:-/tmp/vps-rooms-linux}"
AGENT_SRC="${2:-/tmp/vps-rooms-agent.py}"

if [[ "$(id -u)" -ne 0 ]]; then
  echo "need root"; exit 1
fi
if [[ ! -f "$BIN_SRC" ]]; then
  echo "missing binary: $BIN_SRC"; exit 1
fi

mkdir -p "$PANEL_DIR/bin" "$PANEL_DIR/data/secrets" "$PANEL_DIR/rooms"
chmod 700 "$PANEL_DIR/data/secrets"

install -m 0755 "$BIN_SRC" "$PANEL_DIR/bin/vps-rooms"
if [[ -f "$AGENT_SRC" ]]; then
  install -m 0755 "$AGENT_SRC" "$PANEL_DIR/bin/metrics_agent.py"
fi

# Stop ONLY our panel unit — never other services
systemctl stop vps-rooms 2>/dev/null || true
sleep 1

cat > /etc/systemd/system/vps-rooms.service <<EOF
[Unit]
Description=VPS MANAGE panel
After=network.target docker.service
Wants=docker.service

[Service]
Type=simple
Environment=VPS_ROOMS_ADDR=:9090
Environment=VPS_ROOMS_DATA=$PANEL_DIR/data
Environment=VPS_ROOMS_ROOMS=$PANEL_DIR/rooms
Environment=VPS_ROOMS_RUNTIME=$PANEL_DIR/runtime
Environment=VPS_ROOMS_AGENT_SOCK=$PANEL_DIR/data/agent.sock
WorkingDirectory=$PANEL_DIR
ExecStartPre=/bin/mkdir -p $PANEL_DIR/data/secrets $PANEL_DIR/rooms $PANEL_DIR/runtime
ExecStart=/bin/bash -c '$PANEL_DIR/bin/metrics_agent.py & exec $PANEL_DIR/bin/vps-rooms'
Restart=always
RestartSec=3
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now vps-rooms.service

# Preserve/create locked owner password drop-in (never expose via API/UI)
DROP_IN="/etc/systemd/system/vps-rooms.service.d"
mkdir -p "$DROP_IN"
if [[ ! -f "$DROP_IN/owner.conf" ]]; then
  umask 077
  cat > "$DROP_IN/owner.conf" <<'OWN'
[Service]
Environment=VPS_ROOMS_OWNER_PASS=changeme
OWN
  chmod 600 "$DROP_IN/owner.conf"
  echo "WARNING: set VPS_ROOMS_OWNER_PASS in $DROP_IN/owner.conf"
  systemctl daemon-reload
  systemctl restart vps-rooms.service
fi

# Optional HTTPS reverse proxy for project domains (ports 80/443)
PROXY_DIR="$PANEL_DIR/proxy"
mkdir -p "$PROXY_DIR"
if ! command -v caddy >/dev/null 2>&1; then
  echo "Installing Caddy for domain SSL…"
  apt-get update -y >/dev/null 2>&1 || true
  apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl >/dev/null 2>&1 || true
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg 2>/dev/null || true
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null 2>&1 || true
  apt-get update -y >/dev/null 2>&1 || true
  apt-get install -y caddy >/dev/null 2>&1 || true
fi
if command -v caddy >/dev/null 2>&1; then
  if [[ ! -f "$PROXY_DIR/Caddyfile" ]]; then
    cat > "$PROXY_DIR/Caddyfile" <<'CADDY'
# Managed by VPS MANAGE
:2019 {
  respond "vps-manage-proxy ok" 200
}
CADDY
  fi
  cat > /etc/systemd/system/vps-manage-caddy.service <<EOF
[Unit]
Description=VPS MANAGE Caddy (project domains)
After=network.target
[Service]
Type=simple
ExecStart=/usr/bin/caddy run --config $PROXY_DIR/Caddyfile --adapter caddyfile
ExecReload=/usr/bin/caddy reload --config $PROXY_DIR/Caddyfile --adapter caddyfile
Restart=on-failure
LimitNOFILE=65535
[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now vps-manage-caddy.service || true
  # open HTTP/HTTPS for ACME if ufw present
  if command -v ufw >/dev/null 2>&1; then
    ufw allow 80/tcp comment "vps-manage-caddy" || true
    ufw allow 443/tcp comment "vps-manage-caddy" || true
  fi
fi

# git required for GitHub backups
command -v git >/dev/null 2>&1 || apt-get install -y git >/dev/null 2>&1 || true

sleep 2
systemctl --no-pager --full status vps-rooms.service | head -20
curl -s --max-time 3 http://127.0.0.1:9090/api/health || true
echo
echo "Panel URL: http://SERVER_IP:9090"
echo "Only services touched: vps-rooms.service (+ optional vps-manage-caddy)"

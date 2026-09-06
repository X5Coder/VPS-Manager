#!/bin/sh
set -eu
mkdir -p /vps-manager/bin /vps-manager/data /vps-manager/proxy /vps-manager/x5coder-agent /vps-manager/single /vps-manager/multi /vps-manager/backup
python3 /app/agent/metrics_agent.py &
exec /usr/local/bin/vps-rooms

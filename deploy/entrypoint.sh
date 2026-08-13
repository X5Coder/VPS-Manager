#!/bin/sh
set -eu
mkdir -p /opt/vps-rooms/data /opt/vps-rooms/rooms
python3 /app/agent/metrics_agent.py &
exec /usr/local/bin/vps-rooms

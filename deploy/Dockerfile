# syntax=docker/dockerfile:1
FROM golang:1.25-bookworm AS build
WORKDIR /src
ENV GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN i=1; until go mod download; do i=$((i+1)); [ "$i" -le 8 ] || exit 1; echo "go mod download retry $i"; sleep $((i*3)); done
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/vps-rooms .

FROM python:3.12-slim-bookworm
RUN i=1; until apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git gnupg; do i=$((i+1)); [ "$i" -le 8 ] || exit 1; sleep $((i*3)); done \
  && install -m 0755 -d /etc/apt/keyrings \
  && curl -fsSL https://download.docker.com/linux/debian/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg \
  && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/debian bookworm stable" > /etc/apt/sources.list.d/docker.list \
  && i=1; until apt-get update && apt-get install -y --no-install-recommends docker-ce-cli; do i=$((i+1)); [ "$i" -le 8 ] || exit 1; sleep $((i*3)); done \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out/vps-rooms /usr/local/bin/vps-rooms
COPY agent/metrics_agent.py /app/agent/metrics_agent.py
COPY deploy/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh /app/agent/metrics_agent.py
ENV VPS_ROOMS_ADDR=:9090 \
    VPS_ROOMS_DATA=/opt/vps-rooms/data \
    VPS_ROOMS_ROOMS=/opt/vps-rooms/rooms \
    VPS_ROOMS_RUNTIME=/opt/vps-rooms/runtime \
    VPS_ROOMS_AGENT_SOCK=/opt/vps-rooms/data/agent.sock
VOLUME ["/opt/vps-rooms/data", "/opt/vps-rooms/rooms", "/opt/vps-rooms/runtime"]
EXPOSE 9090
ENTRYPOINT ["/entrypoint.sh"]

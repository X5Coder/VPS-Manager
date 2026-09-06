# VPS Manager

Self-hosted panel for a VPS: **rooms**, **Docker (single image or compose stack)**, **full SSH integration**, **per-room & global backups**, **live metrics**. No public API, no manual `.tar` upload.

**Developer:** [X5Coder](https://github.com/X5Coder)  
**Source:** [https://github.com/X5Coder/VPS-Manager](https://github.com/X5Coder/VPS-Manager)  
**License:** MIT — keep credit to **X5Coder**. See [LICENSE](LICENSE).

---

## 1. Requirements

- **OS:** Ubuntu 20.04 / 22.04 / 24.04 (installer checks `ID=ubuntu` and exits otherwise) — `install.sh:38`
- **User:** `root` (`id -u == 0`, otherwise `sudo -i`)
- **Network:** outbound to `get.docker.com`, `github.com`, `api.ipify.org` (for IP detect)
- Docker is **auto-installed** if missing (`get.docker.com` + `docker-compose-plugin`). Needs ~40 retries to become ready.

No other distro is officially supported. For other distros install Docker manually and run `bash install.sh` from a local clone.

## 2. Installation (on the VPS)

SSH as root, then one command:

```bash
ssh root@YOUR_VPS_IP
curl -fsSL https://raw.githubusercontent.com/X5Coder/VPS-Manager/main/install.sh | bash
```

If `curl` is blocked / raw URL fails:

```bash
git clone https://github.com/X5Coder/VPS-Manager.git
cd VPS-Manager
bash install.sh
# installer auto-detects local source via deploy/docker-compose.yml
```

### What the installer does (step-by-step)

1. **Checks Ubuntu + root**, installs `ca-certificates curl git openssl tar`
2. **Installs Docker Engine + compose plugin** if missing (`install.sh:64-104`), waits `docker info`
3. **Creates canonical dirs** `install.sh:114`:
   ```
   /vps-manager/{bin,data/secrets,proxy,x5coder-agent,single,multi,backup}
   ```
4. **Fetches source** to `/vps-manager/src` (`fetch_source` → `git clone --depth 1` or tarball fallback)
5. **Builds & starts** the panel container:
   ```bash
   docker compose -f deploy/docker-compose.yml up -d --build  # container_name: vps-manager, network_mode: host, privileged: true
   ```
6. **Health-check** `http://127.0.0.1:9090/api/health` (40×2s)
7. **First-time setup** — **stops and asks for 2 values only** (`deploy/first-setup.sh`):

   **Step 1 — Panel password:** type + confirm, min 8 chars, saved to `/vps-manager/data/secrets/owner.env` (`VPS_ROOMS_OWNER_PASS`).

   **Step 2 — Telegram user id:** open Telegram → `@userinfobot` → Start → copy numeric `Id` → paste (saved to `/vps-manager/data/secrets/telegram.env` as `TELEGRAM_CHAT_ID`+`TELEGRAM_CHAT_LOCKED=1`).

8. **Restarts container** (`docker restart vps-manager`) and prints:

   ```
   Panel URL:  http://YOUR_VPS_IP:9090
   ```

> **UFW:** if `ufw` exists, installer allows `22/tcp, 80/tcp, 443/tcp, 9090/tcp` (`install.sh:160`).

### Environment overrides (optional)

```bash
VPS_MANAGER_REPO=https://github.com/X5Coder/VPS-Manager.git \
VPS_MANAGER_BRANCH=main \
PANEL_DIR=/vps-manager \
bash install.sh
```

| Variable | Default | Effect |
|---|---|---|
| `VPS_MANAGER_REPO` | `https://github.com/X5Coder/VPS-Manager.git` | git source |
| `VPS_MANAGER_BRANCH` | `main` | branch/tag to checkout |
| `VPS_MANAGER_TARBALL` | `.../archive/refs/heads/${BRANCH}.tar.gz` | fallback tarball |
| `PANEL_DIR` | `/vps-manager` | base dir on host |

Re-running `install.sh` is idempotent — it keeps existing `owner.env` / `telegram.env` and just rebuilds the container.

## 3. Accessing the Panel

1. Open `http://YOUR_VPS_IP:9090` (or `https://` if you put a proxy in front).
2. **Gate** — enter your **Telegram bot token**, a 6-digit code is sent to your private chat (`/api/gate/challenge` → `/api/gate/verify`). This creates a `vr_gate` session.
3. **Admin login** — enter the **panel password** from install (`/api/auth/owner`). Creates `vr_session` + sticky `vr_admin` (7-day).
4. Rooms list appears. Opening any room requires its **room password** (`/api/rooms/<id>/enter`).

Every page shows a breadcrumb `VPS Path` (`/vps-manager` → `single|multi` → `<room_id>`) and an **SSH connect chip** (`root@IP -p PORT`).

### Changing credentials later (SSH)

```bash
# Change Telegram owner (asks panel password then new id)
/vps-manager/bin/vps-rooms set-telegram-id
# Or edit directly:
/vps-manager/data/secrets/telegram.env

# Change panel password via UI: Settings → Owner password
# Or edit: /vps-manager/data/secrets/owner.env  then  docker restart vps-manager
```

## 4. Default VPS Directory Map (الخريطة الافتراضية)

This is the **single source of truth** (`internal/config/config.go:68`, `internal/isolate/isolate.go:59`, `deploy/docker-compose.yml:22-30`). The panel never writes outside `/vps-manager`.

```
/vps-manager/                          # BaseDir (PANEL_DIR)
├── bin/                               # host copy of binary (volume /vps-manager/bin)
├── data/                              # DataDir — all state, mounted into container
│   ├── panel.db                       # SQLite (rooms, projects, containers, images, volumes, sessions)
│   ├── panel.db-shm / -wal            # SQLite WAL (excluded from backups)
│   ├── agent.sock                     # metrics agent Unix socket (VPS_ROOMS_AGENT_SOCK)
│   ├── logs/                          # rotated to ~256 KB each (PruneLogsDir)
│   │   ├── panel.log                  # panel + service journal
│   │   ├── api.log
│   │   ├── deploy.log
│   │   └── host.log
│   └── secrets/                       # 0700, files 0600
│       ├── owner.env                  # VPS_ROOMS_OWNER_PASS=<panel password>
│       ├── telegram.env               # TELEGRAM_CHAT_ID, TELEGRAM_CHAT_LOCKED, TELEGRAM_BOT_TOKEN
│       └── host.env                   # VPS_ROOT_PASS (if changed via panel)
├── proxy/                             # ProxyDir — Caddy/Nginx configs per domain
├── x5coder-agent/                     # AgentDir — metrics collector workspace
│   └── agent/metrics_agent.py
├── backup/                            # GlobalBackupDir — ONE file for whole VPS
│   └── vps-manager.zip                # zip of /vps-manager excluding backup/, vps-backup/, agent.sock
│
├── single/                            # SingleDir — single-container rooms
│   └── <room_id>/                     # uuid v4, e.g. 3f9a8c1d-... (RoomRoot)
│       ├── vault.bin                  # encrypted working tree (vault.SealDir)
│       ├── auth.hash                  # bcrypt of room password
│       ├── NAME                       # human room name
│       ├── LOCKED / README.txt        # notices
│       ├── project/                   # WorkDir (Runtime) — canonical code dir
│       │   ├── .env                   # RoomEnvPath — SINGLE .env file for the room
│       │   ├── mounts.json            # Binds, FilesRoot, ComposeProject, Gateway
│       │   ├── files -> ../volumes/…  # symlink to volumes when app lives there
│       │   └── <app files>            # or directly mounted host files
│       ├── volumes/                   # RoomVolumesDir — persistent data
│       │   ├── <project_id>/          # per-project volume (also /app/data bind)
│       │   ├── app-data/              # legacy single pattern
│       │   └── app-uploads/
│       ├── config/                    # RoomConfigDir — extra settings (optional)
│       └── backup/                    # RoomBackupDir
│           └── <room_id>.zip          # per-room snapshot (project+volumes+config+.env)
│
└── multi/                             # MultiDir — compose / multi-container rooms
    └── <room_id>/
        ├── stack/                     # WorkDir for multi (= compose root)
        │   ├── docker-compose.yml     # or compose.yaml
        │   ├── .env                   # same single-file env
        │   └── <service code>
        ├── volumes/                   # per-service data (db/, storage/, functions/)
        │   ├── db/
        │   └── storage/
        ├── config/
        └── backup/
            └── <room_id>.zip
```

**Key helpers (code):**
- `cfg.RoomRoot(roomID, kind)` → `/vps-manager/single|multi/<id>` — `config.go:75`
- `cfg.RoomWorkDir(roomID, kind)` → `.../project` or `.../stack` — `config.go:83`
- `cfg.RoomEnvPath(...)` → `.../.env` — `config.go:91`
- `cfg.RoomVolumesDir(...)` → `.../volumes` — `config.go:95`
- `cfg.GlobalBackupPath()` → `/vps-manager/backup/vps-manager.zip` — `config.go:116`

### Room types

| Kind | WorkDir | Typical deploy |
|---|---|---|
| `single` (default) | `/vps-manager/single/<id>/project` | `DeployImage` — `docker pull <image>` + `docker run` with bind `project/.env:/app/.env:ro` |
| `multi` | `/vps-manager/multi/<id>/stack` | compose text pasted in UI → written to `stack/docker-compose.yml` + `docker compose up -d` |

A room is an **isolation unit**: own Docker network `vpsrooms_<shortId>`, own quota, own password, own vault.

### Encryption / Vault

Each room dir is sealed with the room password (`isolate.SealRuntime` → `vault.SealDir` encrypts `Runtime` into `vault.bin`). The panel keeps the runtime unlocked at `/vps-manager/single|multi/<id>/project|stack` while running. Deleting a room wipes containers, images `vpsrooms/*`, network, DB rows, and `os.RemoveAll(root)`.

## 5. Panel Architecture

```
Browser  →  :9090  →  Go (embed web/ + api.Mux)  →  SQLite panel.db  →  Docker sock
                    ↘ metrics Hub (/api/ws/metrics)  → python agent/metrics_agent.py
```

- **Binary:** `vps-rooms` built `CGO_ENABLED=0` in `deploy/Dockerfile:8` from `golang:1.25`
- **Runtime image:** `python:3.12-slim` + `docker-ce-cli` + `vps-rooms` binary
- **Container:** `vps-manager` (`network_mode: host`, `pid: host`, `privileged: true`, restart `unless-stopped`)
- **Frontend:** `web/` (embedded via `//go:embed all:web`, served with `no-store` for `/` and `*.js/*.css`)
- **Auth:** Telegram gate (`vr_gate`) → owner session (`vr_session`/`vr_admin`) → room sessions

### Main API groups

| Prefix | Purpose |
|---|---|
| `/api/health`, `/api/gate/*`, `/api/auth/*` | health, Telegram OTP, login/logout/me |
| `/api/rooms`, `/api/rooms/<id>/*` | rooms CRUD, files, exec, logs, env, quota, password/name |
| `/api/projects`, `/api/projects/<id>/*` | image deploy, port/domain, wipe-data, external-url |
| `/api/host`, `/api/host/exec`, `/api/deploy/*` | host info, host shell, one-click deploy |
| `/api/ssh/*`, `/api/vps/paths` | SSH status/keys/terminal WS, breadcrumb paths |
| `/api/backup/*` | full + per-room backup, status, files, download, delete |
| `/api/storage`, `/api/ports`, `/api/metrics`, `/api/ws/metrics` | disk, ports, live metrics |

Manual `.tar` upload was removed (`410` — use image name or compose text).

## 6. SSH Integration

- **Status:** `GET /api/ssh/status` — port, `active`, `root_login`, `connect` string, all base paths, layout array.
- **Keys:** `GET/POST /api/ssh/keys` (owner only) — list/add `authorized_keys`.
- **Root password:** `GET /api/ssh/root-password` (owner only, masked until Show).
- **Terminal:** `WS /api/ssh/terminal/ws` (gate-auth) — PTY via `internal/ssh/pty.go`.
- **Paths:** `GET /api/vps/paths?all=1` — all rooms with `vps_path/work_dir/volumes/config/env_path`; `?room_id=<id>` for one room with crumbs.

The UI **SSH page** shows connect string `ssh root@<publicIP> -p <port>`, root login toggle, keys, VPS layout, and a deploy tip (image vs compose).

## 7. Backup System

| Scope | Path | Created by |
|---|---|---|
| **Global** | `/vps-manager/backup/vps-manager.zip` | `POST /api/backup/full` (owner) |
| **Per-room** | `/vps-manager/single/<id>/backup/<id>.zip` or `/vps-manager/multi/<id>/backup/<id>.zip` | `POST /api/backup/room {room_id}` |
| Legacy | `/vps-manager/vps-backup/*.zip` | read-only fallback |

- Zips are **Store** (no compression) for speed, exclude `backup/`, `vps-backup/`, `agent.sock`, `panel.db-shm/wal`.
- `GET /api/backup/status` → running/ready/error per job; `GET /api/backup/files` → list with size/created/kind.
- `GET /api/backup/download?file=<name>.zip` streams the file; `POST /api/backup/delete` deletes it.

## 8. Deploy & Runtime

- **Single:** `DeployImage` pulls, finds free host port (`11000+`), `docker run` with labels `vps-rooms.room`, `vps-rooms.project`, quota via `StorageBytes`.
- **Multi:** compose file saved under `stack/`, `docker compose -f stack/docker-compose.yml up -d --build`.
- **Env editing:** editing `project/.env` or `stack/.env` triggers `ApplyRoomEnv` → `docker compose up --force-recreate` or container recreate so new env is live.
- **Logs:** `GET /api/rooms/<id>/logs?container=<name>` tails `docker logs` (300 lines) + `GET /api/host/logs?kind=all|panel|api|deploy|host`.

## 9. Operations

```bash
# Restart panel
docker restart vps-manager
# Or rebuild
cd /vps-manager/src && docker compose -f deploy/docker-compose.yml up -d --build

# Logs
docker logs -f vps-manager
cat /vps-manager/data/logs/panel.log
journalctl -u vps-rooms.service -n 100  # if systemd unit exists

# Change Telegram owner
/vps-manager/bin/vps-rooms set-telegram-id

# Inspect a room on disk
ls -R /vps-manager/single/<room_id>/
cat /vps-manager/single/<room_id>/project/.env
```

## 10. Troubleshooting

- **Docker not ready:** `docker info` must succeed; installer loops 40×2s. Re-run `bash install.sh`.
- **Port 9090 already in use:** `ss -tulpn | grep 9090` → free it or set `VPS_ROOMS_ADDR=:9091` before compose up.
- **Telegram OTP not arriving:** check `telegram.env` has `TELEGRAM_BOT_TOKEN`, bot is started, chat id numeric.
- **Room files appear empty after migration:** `SyncRoomFilesVisibility` rebuilds `mounts.json` / `files` symlink from live Docker binds and `/vps-manager/.../volumes/<project_id>`.

---

**Note:** The canonical layout described above is enforced by code (`config.Room*`, `isolate.PathsForKind`, `deploy/docker-compose.yml` volumes). If you move `/vps-manager`, set `VPS_MANAGER_BASE` / `VPS_MANAGER_DATA` etc. before starting the container.

# VPS Manager

Self-hosted control panel for an **Ubuntu** VPS: live host metrics, isolated project rooms, Docker deploys, specialized AI agents, and GitHub backup.

**Developer:** [X5Coder](https://github.com/X5Coder)  
**Source:** [https://github.com/X5Coder/VPS-Manager](https://github.com/X5Coder/VPS-Manager)  
**License:** MIT — you may host, use, modify, and develop this project. Keep credit to **X5Coder** and a link to the original repository. See [LICENSE](LICENSE).

---

## What it is

VPS Manager is a web panel that runs **on your VPS** (port **9090**). You unlock it with a Telegram bot, sign in with an admin password, then manage apps in isolated rooms.

Each room has disk quota, files, env, logs, and a terminal agent. The panel does not replace Docker on the host — it uses the host Docker engine to run project containers.

### What you can do

- Watch CPU, RAM, disk, load, and (if present) GPU in real time
- Deploy one app per room from a Docker image (upload `.tar`, pull, or build)
- Set a disk quota per room and pause / resume without deleting
- Talk to specialized agents (deploy, room, logs, tokens, usage)
- Create API tokens: **read**, **write**, or **both** (one key that can read and write)
- Back up and restore panel state to GitHub

---

## Requirements

| Item | Detail |
| --- | --- |
| OS | **Ubuntu 20.04, 22.04, or 24.04** |
| Access | **root** |
| CPU | x86_64 or ARM64 |
| Network | Port **9090** reachable (and 22 for SSH) |
| Telegram | A bot token + your numeric user id (for the login gate) |

The installer installs Docker if it is missing. Other Linux distros, Windows VPS, and shared hosting without root are out of scope.

---

## Install on a new VPS

The panel is installed **on the server**, not on your laptop. Full notes: [docs/INSTALL.md](docs/INSTALL.md).

### 1. SSH in

Your provider gives you an IP and a root password.

```bash
ssh root@YOUR_VPS_IP
```

### 2. Run the installer

```bash
curl -fsSL https://raw.githubusercontent.com/X5Coder/VPS-Manager/main/install.sh | bash
```

Downloads retry on failure. You can run the same command again; it will not wipe other containers.

If `curl` is blocked:

```bash
git clone https://github.com/X5Coder/VPS-Manager.git
cd VPS-Manager
bash install.sh
```

### 3. Create the panel password (required)

When Docker is ready the script **stops**. Create an admin password (at least 8 characters, then confirm). It is stored on this VPS.

### 4. Enter your Telegram user id (required)

Open Telegram, search `@userinfobot`, tap **Start**, copy the numeric **Id**, paste it into the installer.

### 5. Open the panel

Only after those two values are saved does the script print:

```text
Panel URL:  http://YOUR_VPS_IP:9090
```

1. Open that URL.
2. Unlock with a **Telegram bot token**. A 30-second code is sent to your Telegram account.
3. Sign in with the **panel password** you created.

Default service: systemd unit `vps-rooms.service`, files under `/opt/vps-rooms`.

---

## Optional: run with Docker Compose

The default install is a **host binary**. Compose is optional. It needs the host Docker socket so the panel can start project containers.

```bash
git clone https://github.com/X5Coder/VPS-Manager.git
cd VPS-Manager
docker compose -f deploy/docker-compose.yml up -d --build
```

Then open `http://YOUR_VPS_IP:9090`. Data lives in `/opt/vps-rooms/...` on the host.

---

## Deploy a project

### From your machine (recommended)

```bash
docker build -t myapp:latest .
docker save -o myapp.tar myapp:latest
```

In the panel: **Deploy** → upload `myapp.tar` → set **disk quota** → **Start**.

### Pull an image on the VPS

In **Deploy**, ask the agent to `docker pull nginx:alpine` (or any public image). After a real pull/build/clone it **must** ask you for disk size, then it can start the room.

### Environment variables

The **Env** tab is the project `.env` used when the container starts. If the page was empty, the panel can fill it from the running container. After you save, **Pause** then **Resume** so the app reloads the values.

---

## Rooms (projects)

Each room is one project:

- **Open** — enter the room (password is masked; use Copy)
- **Pause / Resume** — confirm before pause
- **Delete** — confirm; this cannot be undone
- Tabs: overview, files, logs, env, **Ai Agent | Terminal**

The room agent can inspect files, edit them via the terminal, analyze **this** room’s disk, and change quota. It cannot delete the room or clone a new app.

---

## Agents

Agents work in a **loop**: speak → one command (or a typed draft) → read the result → next step, until the job is done. While they work, the send button becomes **Stop**.

If you ask the room/deploy agent to **type a command without sending**, it fills the terminal input only. You press Enter when you want it to run.

| Agent | Job |
| --- | --- |
| Deploy | Find a repo, clone, dockerize, pull, ask quota, start a room |
| Room / Terminal | This project only: files, edits, commands, quota, pause/resume, usage |
| Logs | Panel / API / Deploy / Host logs |
| Tokens | Create and explain API tokens |
| Usage | Host CPU/RAM/disk plus room **names** and each room’s disk vs totals |

Each agent refuses off-topic questions.

---

## HTTP API tokens

Create keys in **Tokens**. Three modes, **one secret each**:

| Mode | Access |
| --- | --- |
| `read` | GET only |
| `write` | GET + create / update / exec |
| `both` | Same token can read **and** write |

Delete via API is never allowed.

```bash
curl -sS -H "Authorization: Bearer YOUR_SECRET" http://YOUR_VPS_IP:9090/api/v1/storage
```

Also accepted: `X-API-Token: YOUR_SECRET`.

Main routes: `GET /api/v1/projects`, `GET /api/v1/projects/{id}`, `POST /api/v1/projects` (write/both), `PATCH /api/v1/projects/{id}` (write/both), `POST /api/v1/projects/{id}/exec` (write/both), `GET /api/v1/storage`, `GET /api/v1/ports`.

---

## Backup

In **Restore**, connect a GitHub classic PAT with `repo` scope. The panel can snapshot panel data and rooms to GitHub and restore later. Do not use this as the only copy of production secrets.

---

## License and source

Copyright **X5Coder**. Official source: [https://github.com/X5Coder/VPS-Manager](https://github.com/X5Coder/VPS-Manager).

You **may** host this panel, use it, change it, and build on it. You **must** keep attribution to X5Coder and a link to that repository (in your docs, about page, or an equivalent notice). Full text: [LICENSE](LICENSE).

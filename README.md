# VPS Manager

Control panel for an **Ubuntu** VPS: live host metrics, isolated project rooms, deploy from a Docker image, AI agents, and GitHub backup.

**Developer:** [X5Coder](https://github.com/X5Coder)  
**Repository:** [https://github.com/X5Coder/VPS-Manager](https://github.com/X5Coder/VPS-Manager)

---

## What it is

VPS Manager is a self-hosted web panel that runs on your own server (port **9090**). You unlock it with a Telegram bot, sign in with an admin password, then manage projects in isolated rooms — each with disk quota, files, logs, env, and a dedicated terminal agent.

It is built for operators who want one place to:

- Watch CPU, RAM, disk, and load in real time
- Deploy apps as a single Docker image (upload `.tar`, pull, or build)
- Keep each project in its own room with a quota
- Talk to specialized AI agents (deploy, room terminal, logs, tokens, usage)
- Back up and restore the full panel state to GitHub

## Supported

**Ubuntu 20.04, 22.04, or 24.04** with **root**, x86_64 or ARM64.

## Install

Full steps: [docs/INSTALL.md](docs/INSTALL.md)

```bash
ssh root@YOUR_VPS_IP
curl -fsSL https://raw.githubusercontent.com/X5Coder/VPS-Manager/main/install.sh | bash
```

The installer waits for a panel password and your Telegram user id, then prints the panel URL (`http://YOUR_VPS_IP:9090`).

## Deploy a project

On your computer:

```bash
docker build -t myapp:latest .
docker save -o myapp.tar myapp:latest
```

In the panel: **Deploy** → upload `myapp.tar` → set disk quota → Start.

## Agents

Each agent stays on its own job and works in a loop (speak → command → result → next step) until the task is done. You can stop the loop from the send button.

| Agent | Job |
| --- | --- |
| Deploy | Find, clone, dockerize, pull, quota, start a room |
| Room / Terminal | This project only: files, commands, edits, quota, pause/resume, usage |
| Logs | Analyze Panel / API / Deploy / Host logs |
| Tokens | Create and explain API tokens |
| Usage | Live server consumption vs rooms |

## Run with Docker

From the repo root (needs the host Docker socket so the panel can manage project containers):

```bash
git clone https://github.com/X5Coder/VPS-Manager.git
cd VPS-Manager
docker compose -f deploy/docker-compose.yml up -d --build
```

Panel: `http://YOUR_VPS_IP:9090`

The default install (`install.sh`) still runs the panel as a **host binary** via systemd. Compose is optional.

## License

MIT · Developed by **X5Coder**

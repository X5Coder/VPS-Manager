# VPS MANAGE

Open-source panel for an **Ubuntu** VPS: live metrics, isolated project rooms, deploy from one Docker image, backup.

**Repo:** [https://github.com/X5Coder/VPS-Manager](https://github.com/X5Coder/VPS-Manager)

## Install

Supported: **Ubuntu 20.04, 22.04, or 24.04** with root. Not Windows. Not other distros.

1. SSH in:

```bash
ssh root@YOUR_VPS_IP
```

2. Run as root (downloads retry on failure):

```bash
curl -fsSL https://raw.githubusercontent.com/X5Coder/VPS-Manager/main/install.sh | bash
```

3. When install succeeds, the script asks for:
   - **Panel password** (min 8 characters)
   - **Telegram user id** (numbers only — open Telegram, search `@userinfobot`, tap Start, copy the Id)
4. It then prints the panel URL, for example `http://YOUR_VPS_IP:9090`
5. In the browser: Telegram bot token → 30-second code → panel password

Full guide: [docs/INSTALL.md](docs/INSTALL.md)

The installer does not stop other containers.

## Deploy a project

On your computer, build **one image file**:

```bash
docker build -t myapp:latest .
docker save -o myapp.tar myapp:latest
```

In the panel: **Deploy** → upload `myapp.tar` → set disk quota → Start. The panel creates the room and runs it. You can also `docker pull` an image on the VPS the same way.

## What you get

- Live host metrics (CPU, RAM, disk, load)
- Isolated project rooms
- Deploy from a `.tar` image, a registry pull, or a Dockerfile
- GitHub full backup and restore

## License

MIT

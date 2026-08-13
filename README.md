# VPS MANAGE

Open-source panel to manage a Linux VPS: live metrics, isolated project rooms, deploy, backup.

**Repo:** [https://github.com/X5Coder/VPS-Manager](https://github.com/X5Coder/VPS-Manager)

## After you buy a VPS

You only need **Linux + root**. Best: **Ubuntu 22.04 or 24.04**.

1. SSH from your computer:

```bash
ssh root@YOUR_VPS_IP
```

2. Paste this as root and wait (retries if a download fails):

```bash
curl -fsSL https://raw.githubusercontent.com/X5Coder/VPS-Manager/main/install.sh | bash
```

3. Open `http://YOUR_VPS_IP:9090`
   - Telegram bot token → 30-second code
   - Admin password printed in the terminal (also in `/opt/vps-rooms/data/secrets/owner.env`)
   - Change that password in Settings

Full guide (English + Arabic): [docs/INSTALL.md](docs/INSTALL.md)

The installer does not stop other containers.

## Supported systems

| Works | Does not work |
| --- | --- |
| Ubuntu 20.04 / 22.04 / 24.04 | Windows VPS |
| Debian 11 / 12 | macOS as the server |
| Fedora, Rocky Linux, AlmaLinux | Shared hosting without root |
| x86_64 (amd64) and ARM64 |  |

Needs: root, ~1 GB RAM, Docker (installed for you).

## What you get

- Live host metrics (CPU, RAM, disk, load)
- Isolated project rooms with passwords
- Deploy from Git / Docker image / Dockerfile
- GitHub full backup and restore
- Optional domain proxy

## Local development

```bash
export VPS_ROOMS_DATA=./data
export VPS_ROOMS_ROOMS=./rooms
export VPS_ROOMS_ADDR=':9090'
export VPS_ROOMS_OWNER_PASS=devpass
go run .
```

## License

MIT

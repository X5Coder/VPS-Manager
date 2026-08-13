# VPS MANAGE

Open-source VPS control panel. One command installs Docker, builds the panel, and starts it on port **9090**.

[https://github.com/X5Coder/VPS-Manager](https://github.com/X5Coder/VPS-Manager)

## Install on a new VPS

As **root**:

```bash
curl -fsSL https://raw.githubusercontent.com/X5Coder/VPS-Manager/main/install.sh | bash
```

Then open `http://YOUR_VPS_IP:9090`.

1. Unlock with a Telegram bot token (a 30-second code is sent to your chat).
2. Sign in with the admin password printed by the installer (also stored in `/opt/vps-rooms/data/secrets/owner.env`).
3. Change that password in Settings.

The installer does not stop other containers.

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

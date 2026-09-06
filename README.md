# VPS Manager

Self-hosted panel for a VPS: rooms, Docker (single image or multi stack), full SSH integration. No public API, no manual .tar upload.

**Developer:** [X5Coder](https://github.com/X5Coder)  
**Source:** [https://github.com/X5Coder/VPS-Manager](https://github.com/X5Coder/VPS-Manager)  
**License:** MIT — keep credit to **X5Coder**. See [LICENSE](LICENSE).

---

## Run (on the VPS)

SSH as root, then install:

```bash
ssh root@YOUR_VPS_IP
curl -fsSL https://raw.githubusercontent.com/X5Coder/VPS-Manager/main/install.sh | bash
```

If `curl` is blocked:

```bash
git clone https://github.com/X5Coder/VPS-Manager.git
cd VPS-Manager
bash install.sh
```

The installer **stops and asks for two values only**:

1. **Panel password** — type it, then confirm (at least 8 characters). This is the web login password. It stays on this VPS.
2. **Telegram user id** — open Telegram, search `@userinfobot`, tap **Start**, copy the numeric **Id**, paste it.

After those two, it prints:

```text
Panel URL:  http://YOUR_VPS_IP:9090
```

Open that URL. Unlock with a **Telegram bot token** (a short code is sent to your Telegram). Then sign in with the **panel password** you typed.

Service: `vps-rooms.service` · files: `/vps-manager` · port **9090**.

Layout (only this shape):

```text
/vps-manager/
├── bin/                        ← VPS Manager binary
├── data/                       ← DB, logs, settings
├── proxy/                      ← proxy settings (nginx/caddy)
├── x5coder-agent/              ← x5coder agent
├── single/                     ← single-container projects
│   └── <room_id>/
│       ├── project/            ← code + .env (single file)
│       ├── volumes/            ← persistent data (app-data/, app-uploads/, ...)
│       └── config/             ← extra settings (optional)
└── multi/                      ← multi-container projects
    └── <room_id>/
        ├── stack/              ← docker-compose.yml + .env (single file)
        ├── volumes/            ← service data (db/, storage/, functions/, ...)
        └── config/             ← extra settings (optional)
```

Every web page shows the VPS path breadcrumb on top (where you are in the VPS) + the SSH connect chip.

Change the Telegram owner later (SSH, then panel password, then new id):

```bash
/vps-manager/bin/vps-rooms set-telegram-id
```

SSH (fully integrated): see the **SSH** page in the panel — connect string, port, root login, authorized keys, VPS layout. Deploy via image name or compose text; manual Docker `.tar` upload was removed.

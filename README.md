# VPS Manager

Self-hosted panel for a VPS: rooms, Docker (single image or multi stack), API tokens, GitHub backup.

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

Service: `vps-rooms.service` · files: `/opt/vps-rooms` · port **9090**.

Change the Telegram owner later (SSH, then panel password, then new id):

```bash
/opt/vps-rooms/bin/vps-rooms set-telegram-id
```

API usage (quota, create room, single `.tar` vs multi `.tar.gz`, GitHub Actions) is in the panel **Docs** page, or [docs/API.md](docs/API.md).

# Install VPS Manager

**Repository:** [https://github.com/X5Coder/VPS-Manager](https://github.com/X5Coder/VPS-Manager)  
**Developer:** X5Coder  
**Docs:** [README.md](../README.md) — full product documentation.  
**License:** MIT with attribution — see [LICENSE](../LICENSE).

## Supported

**Ubuntu 20.04, 22.04, or 24.04** with **root**. x86_64 or ARM64.

## After you buy a VPS

The panel is installed **on the VPS**, not on your laptop.

### 1. SSH in

Your provider gives you an **IP** and a **root password**. On your computer:

```bash
ssh root@YOUR_VPS_IP
```

### 2. Paste this one command

```bash
curl -fsSL https://raw.githubusercontent.com/X5Coder/VPS-Manager/main/install.sh | bash
```

If a download fails, the script retries and continues. You can run the same command again; it will not wipe other containers.

If `curl` is blocked:

```bash
git clone https://github.com/X5Coder/VPS-Manager.git
cd VPS-Manager
bash install.sh
```

### 3. Create a panel password (required)

When Docker and the panel are up, the script **stops and waits**. You must create an admin password (at least 8 characters, then confirm). It is saved on this VPS.

### 4. Enter your Telegram user id (required)

Still in the installer: open Telegram, search `@userinfobot`, tap Start, copy the numeric **Id**, paste it. It is saved on this VPS.

### 5. Open the panel URL

Only after those two values are saved does the script print:

```text
Panel URL:  http://YOUR_VPS_IP:9090
```

Open that URL. Unlock with a **Telegram bot token** (a 30-second code is sent to your account). Sign in with the **panel password** you just created.

## Deploy a project as one image

On your machine:

```bash
docker build -t myapp:latest .
docker save -o myapp.tar myapp:latest
```

In the panel: **Deploy** → upload `myapp.tar` → set disk quota → Start.

The panel creates the room, loads the image, and starts it.

You can also pull a public image on the VPS (`docker pull nginx:alpine`) and set quota the same way.

In a room **Ai Agent | Terminal** you can ask the assistant to inspect this project, run commands, and explain files after it reads them in the terminal.

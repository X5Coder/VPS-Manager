# Install VPS MANAGE

## Supported OS

**Ubuntu 20.04, 22.04, or 24.04** only. You need **root**.

Does not run on Windows VPS, other Linux distributions, or shared hosting without root.

## After you buy a VPS

The panel is installed **on the VPS**, not on your laptop.

1. Your provider gives you an **IP** and a **root password**.
2. On your computer:

```bash
ssh root@YOUR_VPS_IP
```

3. As root, paste and wait:

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

4. When install **succeeds**, the script asks for:
   - **Panel password** (at least 8 characters, then confirm)
   - **Telegram user id** — open Telegram, search `@userinfobot`, tap Start, copy the numeric Id
5. It then prints:

```text
Panel URL:  http://YOUR_VPS_IP:9090
```

6. Open that URL.
7. Unlock with a **Telegram bot token** (a 30-second code is sent to your account).
8. Sign in with the **panel password** you just typed.

## Deploy a project as one image

On your machine:

```bash
docker build -t myapp:latest .
docker save -o myapp.tar myapp:latest
```

In the panel: **Deploy** → upload `myapp.tar` → set disk quota → Start.

The panel creates the room, loads the image, and starts it. No room name is required.

You can also pull a public image on the VPS (`docker pull nginx:alpine`) and set quota the same way.

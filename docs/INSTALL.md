# Install VPS MANAGE

[العربية](#عربي) · [English](#english)

---

## English

### What you do after buying a VPS

The panel is **not** an app on your laptop. You install it **on the VPS**.

1. The seller gives you an **IP** and a **root password**.
2. On your computer, open a terminal and connect:

```bash
ssh root@YOUR_VPS_IP
```

3. You are now inside the VPS. Paste **one** command and wait:

```bash
curl -fsSL https://raw.githubusercontent.com/X5Coder/VPS-Manager/main/install.sh | bash
```

If a download fails, the script **retries and continues**. You can run the same command again; it will not wipe other containers.

If `curl` is blocked:

```bash
git clone https://github.com/X5Coder/VPS-Manager.git
cd VPS-Manager
bash install.sh
```

4. When it prints `Panel: http://IP:9090`, open that URL in a browser.
5. Unlock with a **Telegram bot token** (a 30-second code is sent to your chat).
6. Sign in with the **admin password** printed in the terminal. It is also saved at `/opt/vps-rooms/data/secrets/owner.env`.
7. Change that password in **Settings**.

### Which operating system?

| Works | Does not work |
| --- | --- |
| **Ubuntu 20.04 / 22.04 / 24.04** (recommended) | Windows VPS |
| Debian 11 / 12 | macOS as the server |
| Fedora, Rocky Linux, AlmaLinux | Shared hosting without root |
| CPU: x86_64 (amd64) or ARM64 |  |

You need **root**. About **1 GB RAM** is enough for the panel itself.

---

## عربي

### بعد ما تشتري VPS تعمل إيه؟

اللوحة **مش برنامج على لابتوبك**. بتتركّب **على السيرفر**.

1. الشركة هتديك **IP** و **كلمة مرور root**.
2. من تيرمينال جهازك اكتب (بدّل `YOUR_VPS_IP`):

```bash
ssh root@YOUR_VPS_IP
```

3. لما تدخل السيرفر، الصق الأمر ده كـ root واستنى. لو التحميل فشل هيعيد المحاولة ويكمل:

```bash
curl -fsSL https://raw.githubusercontent.com/X5Coder/VPS-Manager/main/install.sh | bash
```

لو `curl` اتقفل:

```bash
git clone https://github.com/X5Coder/VPS-Manager.git
cd VPS-Manager
bash install.sh
```

4. لما يطبع `Panel: http://IP:9090` افتح الرابط من المتصفح.
5. فك القفل بتوكن **بوت تيليجرام** (هيوصلك كود لمدة 30 ثانية).
6. ادخل بكلمة الأدمن اللي ظهرت في التيرمينال (محفوظة كمان في `/opt/vps-rooms/data/secrets/owner.env`).
7. غيّرها من **Settings** فوراً.

### أنهي نظام تشغيل؟

| يشتغل | لا يشتغل |
| --- | --- |
| **Ubuntu 20.04 / 22.04 / 24.04** (الأفضل) | Windows VPS |
| Debian 11 / 12 | macOS كسيرفر |
| Fedora، Rocky، AlmaLinux | استضافة مشتركة من غير root |
| معالج x86_64 أو ARM64 |  |

لازم **root**. اللوحة نفسها تكفي معاها حوالي **1 GB RAM**.

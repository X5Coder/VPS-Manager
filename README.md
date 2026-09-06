<p align="center">
  <img src="web/favicon.svg" width="88" height="88" alt="VPS Manager" />
</p>

<h1 align="center">VPS Manager</h1>

<p align="center">
  <b>إدارة كاملة للـ VPS — وتنظيم احترافي لكل مشاريعك</b><br/>
  غرف معزولة • Docker بضغطة • SSH متكامل • نسخ احتياطي ذكي
</p>

<p align="center">
  <a href="https://github.com/X5Coder/VPS-Manager"><img src="https://img.shields.io/badge/source-GitHub-black?style=flat-square" /></a>
  <img src="https://img.shields.io/badge/license-MIT-green?style=flat-square" />
  <img src="https://img.shields.io/badge/ubuntu-20.04%20|%2022.04%20|%2024.04-orange?style=flat-square" />
  <img src="https://img.shields.io/badge/port-9090-blue?style=flat-square" />
</p>

<p align="center">
  Developed by <a href="https://github.com/X5Coder"><b>X5Coder</b></a> • <code>curl | bash</code> تثبيت في أمر واحد
</p>

---

### ما هو VPS Manager ؟

لوحة تحكم Self-hosted تتركب مباشرة على الـ VPS وتعطيك **نظام غرف (Rooms)** — كل غرفة معزولة تماماً بكلمة سر، شبكة Docker خاصة، وحصة مساحة خاصة بها. بدل ما تخلط كل المشاريع على السيرفر، كل مشروع/ستاك يعيش داخل غرفته مع `.env` واحد و `volumes` محفوظة و `backup` خاص. الإدارة كلها من المتصفح + SSH مدمج بالكامل.

---

### ✨ المميزات — بنظام

| الفئة | ماذا تقدم |
|---|---|
| **🏠 الغرف المعزولة** | كل غرفة = ID + اسم + باسورد + `vault.bin` مشفر. شبكة `vpsrooms_<id>` منفصلة. Single أو Multi |
| **🐳 نشر Docker** | **Single:** `docker pull <image>` ثم تشغيل تلقائي • **Multi:** لصق `docker-compose.yml` كامل داخل اللوحة |
| **🔐 أمان طبقتين** | بوابة Telegram OTP (كود 6 أرقام على الخاص) + باسورد اللوحة + باسورد كل غرفة |
| **💻 SSH متكامل** | صفحة SSH داخل اللوحة: `ssh root@IP -p PORT` + مفاتيح `authorized_keys` + تيرمنال WebSocket مباشر |
| **💾 نسخ احتياطي** | زر واحد: `backup/vps-manager.zip` (كامل الـ VPS) أو `single|multi/<id>/backup/<id>.zip` لكل غرفة |
| **📊 مراقبة حية** | CPU / RAM / Disk / Network + GPU إن وجد عبر WebSocket و `agent/metrics_agent.py` |
| **🌐 الدومينات** | ربط أي غرفة بدومين عبر `proxy/` (Caddy) مع تفعيل/تعطيل فوري |
| **📁 ملفات و Env** | متصفح ملفات لكل غرفة + تحرير `.env` الوحيد وإعادة تشغيل الحاوية تلقائياً |

---

### ⚡ التثبيت — أمر واحد فقط

على الـ VPS كـ `root`:

```bash
curl -fsSL https://raw.githubusercontent.com/X5Coder/VPS-Manager/main/install.sh | bash
```

> بديل إذا `curl` محجوب:
> ```bash
> git clone https://github.com/X5Coder/VPS-Manager.git && cd VPS-Manager && bash install.sh
> ```

**بعد الأمر مباشرة — سيتوقف ويسألك فقط:**

```
1) باسورد لوحة التحكم  →  اكتبه و أكده (8 أحرف على الأقل)
2) Telegram User ID     →  افتح @userinfobot على تليجرام → Start → انسخ الـ ID الرقمي → الصقه
```

ثم يطبع:

```
Panel URL:  http://YOUR_VPS_IP:9090
```

افتح الرابط → أدخل **توكن بوت تليجرام** (يوصلك كود) → سجل دخول بـ **باسورد اللوحة**.

> **المتطلبات:** Ubuntu 20.04 / 22.04 / 24.04 + `root` فقط. الدوكر يُثبت تلقائياً إن لم يكن موجوداً. المنافذ `22, 80, 443, 9090` تُفتح تلقائياً لو `ufw` موجود.

---

### 🗺️ خريطة المشروع الافتراضية

كل شيء يعيش تحت `/vps-manager` فقط — لا يكتب خارجها أبداً.

```
/vps-manager/
├── data/                 →  قاعدة البيانات + كلمات السر + السجلات
│   ├── panel.db
│   └── secrets/owner.env , telegram.env
├── proxy/                →  إعدادات الدومينات
├── backup/               →  نسخة كاملة  vps-manager.zip
│
├── single/<room_id>/     →  غرفة حاوية واحدة
│   ├── project/.env      →  ملف الـ Env الوحيد
│   ├── volumes/          →  البيانات المحفوظة
│   ├── config/           →  إعدادات إضافية
│   └── backup/<id>.zip   →  نسخة الغرفة
│
└── multi/<room_id>/      →  غرفة Compose متعددة الحاويات
    ├── stack/            →  docker-compose.yml + .env
    ├── volumes/          →  db / storage / functions
    ├── config/
    └── backup/<id>.zip
```

كل صفحة في اللوحة تعرض المسار الحالي أعلى الشاشة `VPS Path` وشريحة اتصال SSH.

---

### 🔧 إدارة سريعة

```bash
docker restart vps-manager          # إعادة تشغيل اللوحة
docker logs -f vps-manager          # السجلات
/vps-manager/bin/vps-rooms set-telegram-id  # تغيير مالك تليجرام
```

**اللوحة تعمل على:** `http://IP:9090` — الحاوية `vps-manager` (`host` + `privileged`)

---

<p align="center"><sub>MIT License — keep credit to <b>X5Coder</b> • <a href="LICENSE">LICENSE</a></sub></p>

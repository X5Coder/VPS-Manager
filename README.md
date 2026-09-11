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

### 🗺️ خريطة المشروع النهائية

كل شيء يعيش تحت `/vps-manager` فقط — لا يكتب خارجها أبداً.

```
/vps-manager/
│
├── bin/                              # تشغيل وأدوات VPS Manager
│
├── data/                             # بيانات VPS Manager
│   ├── database.sqlite               # قاعدة البيانات
│   ├── sessions/                     # جلسات المستخدمين
│   └── logs/                         # Logs الخاصة بالـVPS Manager
│
├── proxy/                            # إعدادات الـReverse Proxy
│
├── x5coder-agent/                    # الـVPS Agent
│
├── backup/                           # Backup كامل لـVPS Manager
│   └── vps-manager.zip
│
├── single/                           # Rooms من نوع Single Container
│   └── <room_id>/
│       ├── project/                  # الـSource الكامل والمفكوك للمشروع
│       ├── container/                # بيانات وإعدادات الـContainer
│       ├── volumes/                  # Volumes الخاصة بالـRoom
│       │   ├── <volume_name>/        # Volume
│       │   └── ...
│       ├── config/                   # إعدادات الـRoom والـENV والـSecrets
│       ├── logs/                     # Logs الخاصة بالـContainer
│       └── backup/                   # Clone كامل للـRoom
│           └── <room_id>.zip
│
└── multi/                            # Rooms من نوع Multi Container
    └── <room_id>/
        ├── project/                  # محجوز (مصدر الـStack يعيش في stack/)
        ├── stack/                    # الـSource الكامل + ملفات وتعريف الـStack
        │   ├── docker-compose.yml    # تعريف الـServices والـNetworks والـVolumes
        │   └── ...
        ├── containers/               # Containers الخاصة بالـRoom
        │   ├── <container_id>/       # بيانات وإعدادات Container
        │   └── ...
        ├── volumes/                  # Volumes الخاصة بالـRoom
        │   ├── <volume_name>/        # Volume
        │   └── ...
        ├── config/                   # إعدادات الـStack والـServices والـENV والـSecrets
        ├── logs/                     # Logs الخاصة بالـContainers
        │   ├── <container_id>/       # Logs الخاصة بـContainer
        │   └── ...
        └── backup/                   # Clone كامل للـRoom
            └── <room_id>.zip
```

#### 🏗️ بنية الغرفة الداخلية

**الغرفة المطلوبة للاكتشاف التلقائي (SSH)**:
```
# single:
/vps-manager/single/<room_id>/
├── auth.hash        # ✅ مطلوب (كلمة المرور المشفرة)
├── NAME             # ❌ اختياري (يُنشأ تلقائياً: room-<id8>)
├── vault.bin        # ❌ اختياري (يُنشأ فارغاً إذا مفقود)
├── project/         # ✅ يُنشأ تلقائياً
├── container/       # ✅ يُنشأ تلقائياً
├── volumes/         # ✅ يُنشأ تلقائياً (<volume_name>/ عند الحاجة)
├── config/          # ✅ يُنشأ تلقائياً (.env هنا)
├── logs/            # ✅ يُنشأ تلقائياً
└── backup/          # ✅ يُنشأ تلقائياً (<room_id>.zip)

# multi:
/vps-manager/multi/<room_id>/
├── auth.hash        # ✅ مطلوب
├── NAME             # ❌ اختياري
├── vault.bin        # ❌ اختياري
├── project/         # ✅ يُنشأ تلقائياً (محجوز — المصدر في stack/)
├── stack/           # ✅ يُنشأ تلقائياً (docker-compose.yml هنا)
├── containers/      # ✅ يُنشأ تلقائياً (<container_id>/ عند التشغيل)
├── volumes/         # ✅ يُنشأ تلقائياً (<volume_name>/ عند الحاجة)
├── config/          # ✅ يُنشأ تلقائياً (.env هنا)
├── logs/            # ✅ يُنشأ تلقائياً (<container_id>/ عند التشغيل)
└── backup/          # ✅ يُنشأ تلقائياً (<room_id>.zip)
```

**إنشاء غرفة عبر SSH**:
```bash
# 1. إنشاء المجلد
mkdir -p /vps-manager/single/my-room/project

# 2. إضافة كلمة المرور المشفرة (مطلوب فقط)
echo "hashed_password_here" > /vps-manager/single/my-room/auth.hash

# خلال 10 ثواني: يُكتشف تلقائياً و يُضاف للوحة
# الاسم الافتراضي: room-my-room
# يمكن تغييره من الواجهة
```

كل صفحة في اللوحة تعرض المسار الحالي أعلى الشاشة `VPS Path` وشريحة اتصال SSH.

---

### 🔧 إدارة سريعة

```bash
docker restart vps-manager          # إعادة تشغيل اللوحة
docker logs -f vps-manager          # السجلات
/vps-manager/bin/vps-rooms set-telegram-id  # تغيير مالك تليجرام
```

### 🚀 أوامر SSH كاملة للغرف

```bash
# الاتصال بالخادم
ssh root@YOUR_VPS_IP

# عرض جميع الغرف
curl -s http://127.0.0.1:9090/api/rooms

# إنشاء غرفة جديدة عبر SSH (اكتشاف تلقائي)
mkdir -p /vps-manager/single/my-room/project
echo "hashed_password" > /vps-manager/single/my-room/auth.hash
# خلال 10 ثواني: تظهر في اللوحة

# تشغيل غرفة
curl -s -X POST http://127.0.0.1:9090/api/rooms/{room_id}/start

# إيقاف غرفة
curl -s -X POST http://127.0.0.1:9090/api/rooms/{room_id}/stop

# إعادة تشغيل غرفة
curl -s -X POST http://127.0.0.1:9090/api/rooms/{room_id}/restart

# حذف غرفة
curl -s -X DELETE http://127.0.0.1:9090/api/rooms/{room_id}

# فحص يدوي للغرف الجديدة
curl -s -X POST http://127.0.0.1:9090/api/rooms/scan
```

**اللوحة تعمل على:** `http://IP:9090` — الحاوية `vps-manager` (`host` + `privileged`)

---

<p align="center"><sub>MIT License — keep credit to <b>X5Coder</b> • <a href="LICENSE">LICENSE</a></sub></p>

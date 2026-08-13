(() => {
  const app = document.getElementById("app");
  const BOT_KEY = "vr_bot_token";
  const state = {
    me: null,
    gated: false,
    view: "server",
    roomId: null,
    roomTab: "overview",
    metrics: null,
    ws: null,
    sidebarOpen: false,
    gateStep: "token",
    expiresIn: 30,
    filePath: ".",
    fileContent: "",
    termLines: [],
    shellBuilt: false,
    busy: false,
    askedRestore: false,
    backupReady: false,
    restoreGateDone: localStorage.getItem("vr_restore_gate") === "1",
    showNetPanel: false,
    jobWasRunning: false,
    _gen: 0,
    cache: {},
  };

  const TERM_HINTS = [
    { cmd: "ls -la", tip: "list files" },
    { cmd: "pwd", tip: "current directory" },
    { cmd: "df -h", tip: "disk usage" },
    { cmd: "free -h", tip: "memory" },
    { cmd: "ps aux | head", tip: "processes" },
    { cmd: "cat .env", tip: "show env file" },
    { cmd: "env | sort", tip: "container env" },
    { cmd: "hostname -I", tip: "IPs" },
  ];


  function quotaSliderHTML({ name, id, maxGB, valueGB, required }) {
    const max = Math.max(0.1, Number(maxGB) || 0.1);
    let val = Number(valueGB);
    if (!(val > 0)) val = Math.min(1, max);
    if (val > max) val = max;
    val = Math.round(val * 10) / 10;
    const step = max >= 5 ? 0.5 : 0.1;
    const req = required ? "required" : "";
    const nid = id || name;
    return `<div class="quota-slider" data-quota-wrap>
      <div class="quota-slider-top">
        <span class="muted">Disk quota</span>
        <strong class="quota-val mono" data-quota-label>${val.toFixed(1)} GB</strong>
      </div>
      <input type="range" min="0.1" max="${max.toFixed(1)}" step="${step}" value="${val}" data-quota-range aria-label="Disk quota" />
      <input type="hidden" name="${name}" id="${nid}" value="${val}" ${req} data-quota-input />
      <div class="quota-slider-ends"><span>0.1 GB</span><span>max ${max.toFixed(1)} GB</span></div>
    </div>`;
  }

  function bindQuotaSliders(root) {
    (root || document).querySelectorAll("[data-quota-wrap]").forEach((wrap) => {
      const range = wrap.querySelector("[data-quota-range]");
      const input = wrap.querySelector("[data-quota-input]");
      const label = wrap.querySelector("[data-quota-label]");
      if (!range || !input) return;
      const sync = () => {
        const v = Number(range.value);
        input.value = String(v);
        if (label) label.textContent = `${v.toFixed(1)} GB`;
      };
      range.addEventListener("input", sync);
      sync();
    });
  }

  const fmtBytes = (n) => {
    if (n == null || Number.isNaN(Number(n))) return "—";
    const u = ["B", "KB", "MB", "GB", "TB"];
    let i = 0, v = Number(n);
    while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
    return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${u[i]}`;
  };
  const fmtDisk = (n) => {
    const v = Number(n);
    if (!Number.isFinite(v) || v < 0) return "—";
    const gb = v / (1024 * 1024 * 1024);
    if (gb >= 1024) return `${(gb / 1024).toFixed(1)} TB`;
    return `${gb.toFixed(1)} GB`;
  };
  const pct = (n) => `${(Number(n) || 0).toFixed(1)}%`;
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const gb = (bytes) => (Number(bytes) || 0) / (1024 * 1024 * 1024);

  async function api(path, opts = {}) {
    const res = await fetch(path, {
      credentials: "same-origin",
      headers: opts.body && !(opts.body instanceof FormData)
        ? { "Content-Type": "application/json", ...(opts.headers || {}) }
        : opts.headers,
      ...opts,
    });
    const ct = res.headers.get("content-type") || "";
    if (ct.includes("application/json")) {
      const data = await res.json();
      if (!res.ok) throw new Error(data.error || "Request failed");
      return data;
    }
    const text = await res.text();
    if (!res.ok) throw new Error(text || "Request failed");
    return text;
  }

  function el(html) {
    const t = document.createElement("template");
    t.innerHTML = html.trim();
    return t.content.firstElementChild;
  }

  function toast(msg) {
    let t = document.getElementById("copy-toast");
    if (!t) {
      t = document.createElement("div");
      t.id = "copy-toast";
      t.className = "copy-toast";
      document.body.appendChild(t);
    }
    t.textContent = msg || "";
    t.classList.add("show");
    clearTimeout(t._tm);
    t._tm = setTimeout(() => t.classList.remove("show"), 1600);
  }

  function copyText(text) {
    text = String(text ?? "");
    const show = (ok) => toast(ok ? "Copied" : "Copy failed");
    const fallback = () => {
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.style.cssText = "position:fixed;left:-9999px;top:0;opacity:0";
      document.body.appendChild(ta);
      ta.focus();
      ta.select();
      ta.setSelectionRange(0, ta.value.length);
      let ok = false;
      try { ok = document.execCommand("copy"); } catch {}
      ta.remove();
      show(ok);
      return ok;
    };
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text).then(() => show(true)).catch(() => fallback());
    }
    return Promise.resolve(fallback());
  }

  function bindCopyables(root = document) {
    root.querySelectorAll("[data-copy]").forEach((n) => {
      n.onclick = (e) => {
        e.preventDefault();
        e.stopPropagation();
        copyText(n.getAttribute("data-copy") || n.dataset.copy || n.textContent);
      };
    });
    root.querySelectorAll("[data-copy-btn]").forEach((n) => {
      n.onclick = (e) => {
        e.preventDefault();
        e.stopPropagation();
        copyText(n.getAttribute("data-copy-btn") || "");
      };
    });
  }

  function navHighlight(view) {
    if (state.me?.kind === "owner" && view === "room") return "rooms";
    return view || state.view;
  }

  function viewPath(view, extra = {}) {
    const roomId = extra.roomId || state.roomId;
    const tab = extra.roomTab != null ? extra.roomTab : (state.roomTab || "overview");
    switch (view) {
      case "rooms": return "/projects";
      case "room":
        if (roomId) {
          const t = tab && tab !== "overview" ? `/${encodeURIComponent(tab)}` : "";
          return `/projects/${encodeURIComponent(roomId)}${t}`;
        }
        return "/projects";
      case "server": return "/server";
      case "deploy": return "/deploy";
      case "restore": return "/restore";
      case "logs": return "/logs";
      case "settings": return "/settings";
      case "docs": return "/docs";
      default: return "/server";
    }
  }

  function parsePath(path) {
    let p = path || "/";
    try { p = decodeURIComponent(p); } catch {}
    p = p.replace(/\/+$/, "") || "/";
    const room = p.match(/^\/projects\/([^/]+)(?:\/([^/]+))?$/);
    if (room) return { view: "room", roomId: room[1], roomTab: room[2] || "overview" };
    if (p === "/projects") return { view: "rooms" };
    if (p === "/docs" || p === "/guide") return { view: "docs" };
    if (p === "/deploy") return { view: "deploy" };
    if (p === "/restore") return { view: "restore" };
    if (p === "/logs") return { view: "logs" };
    if (p === "/settings") return { view: "settings" };
    if (p === "/room") return { view: "room" };
    if (p === "/server" || p === "/" || p === "/owner" || p === "/app") return { view: "server" };
    return null;
  }

  function setView(view, extra = {}) {
    state.view = view;
    Object.assign(state, extra);
    state.sidebarOpen = false;
    state._gen = (state._gen || 0) + 1;
    stopLogLive();
    markNav(view);
    const url = viewPath(view, extra);
    if (location.pathname !== url) {
      history.pushState({ view, roomId: state.roomId, roomTab: state.roomTab }, "", url);
    }
    render();
  }

  function markNav(view) {
    const nav = document.querySelector("#nav");
    if (!nav) return;
    const key = navHighlight(view);
    nav.querySelectorAll("[data-go]").forEach((b) => {
      b.classList.toggle("active", b.dataset.go === key);
    });
  }

  function alive(view, gen) {
    return state.view === view && state._gen === gen;
  }

  function skel(n) {
    return `<div class="skel-wrap">${Array.from({ length: n || 3 }, () => `<div class="skel"></div>`).join("")}</div>`;
  }

  function stopLogLive() {
    if (state._logPoll) {
      clearInterval(state._logPoll);
      state._logPoll = null;
    }
  }

  function pinLogBottom(el) {
    if (!el) return;
    const go = () => { el.scrollTop = el.scrollHeight; };
    go();
    requestAnimationFrame(() => {
      go();
      requestAnimationFrame(go);
    });
    // Catch late layout (fonts / live HTML swap)
    setTimeout(go, 50);
  }

  function startLogLive(tick) {
    stopLogLive();
    state._logPoll = setInterval(() => {
      tick().catch(() => {});
    }, 3000);
  }

  async function loadGate() {
    try { state.gated = !!(await api("/api/gate/status")).unlocked; }
    catch { state.gated = false; }
  }
  async function loadMe() {
    try { state.me = await api("/api/auth/me"); }
    catch { state.me = null; }
  }

  function connectWS() {
    if (state.ws && (state.ws.readyState === WebSocket.OPEN || state.ws.readyState === WebSocket.CONNECTING)) return;
    if (state.ws) { try { state.ws.close(); } catch {} state.ws = null; }
    if (!state.me) return;
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${location.host}/api/ws/metrics`);
    state.ws = ws;
    ws.onmessage = (ev) => {
      try {
        state.metrics = JSON.parse(ev.data);
        updateMetricsDOM();
      } catch {}
    };
    ws.onclose = () => { if (state.ws === ws) state.ws = null; };
  }

  function updateMetricsDOM() {
    const m = state.metrics;
    if (!m) return;
    const set = (k, v) => { const n = document.querySelector(`[data-metric="${k}"]`); if (n) n.textContent = v; };
    const bar = (k, v) => { const n = document.querySelector(`[data-bar="${k}"]`); if (n) n.style.width = `${Math.min(100, Math.max(0, v || 0))}%`; };
    set("cpu", pct(m.cpu_percent));
    set("mem", pct(m.mem_percent));
    set("mem-sub", `${fmtBytes(m.mem_used)} / ${fmtBytes(m.mem_total)}`);
    set("disk-total", fmtDisk(m.disk_total));
    set("free", fmtDisk((m.disk_free != null ? m.disk_free : (m.disk_total || 0) - (m.disk_used || 0))));
    set("disk-sub", `used ${fmtDisk(m.disk_used)} · free ${fmtDisk(m.disk_free != null ? m.disk_free : (m.disk_total || 0) - (m.disk_used || 0))}`);
    set("load", (m.load1 != null ? Number(m.load1).toFixed(2) : "—"));
    set("net", `↓ ${fmtBytes(m.net_rx)} · ↑ ${fmtBytes(m.net_tx)}`);
    set("disk", `${pct(m.disk_percent)} · ${fmtBytes(m.disk_used)} / ${fmtBytes(m.disk_total)}`);
    bar("cpu", m.cpu_percent);
    bar("mem", m.mem_percent);
    bar("disk", m.disk_percent);
    const cores = Math.max(1, Number(m.cpu_cores) || Number(document.querySelector("[data-cores]")?.dataset.cores) || 1);
    bar("load", Math.min(100, (Number(m.load1) || 0) / cores * 100));
  }

  function bindAction(btn, fn) {
    if (!btn) return;
    btn.addEventListener("click", async (e) => {
      e.preventDefault();
      if (btn.disabled || btn.classList.contains("busy")) return;
      btn.classList.add("busy");
      btn.disabled = true;
      btn.setAttribute("aria-busy", "true");
      try {
        await Promise.resolve().then(() => fn(btn));
      } catch (ex) {
        alert(ex.message || String(ex));
      } finally {
        btn.classList.remove("busy");
        btn.removeAttribute("aria-busy");
        if (btn.dataset.lock !== "1") btn.disabled = false;
      }
    });
  }

  function powerToggleHTML(id, status) {
    const running = status === "running";
    if (running) {
      return `<button class="btn sm action accent-pause" data-power="${id}" data-next="pause">Pause</button>`;
    }
    return `<button class="btn sm action accent-resume" data-power="${id}" data-next="resume">Resume</button>`;
  }

  function bindPowerToggles(scope = document) {
    scope.querySelectorAll("[data-power]").forEach((b) => bindAction(b, async () => {
      const id = b.dataset.power;
      const next = b.dataset.next;
      await api(`/api/rooms/${id}/${next}`, { method: "POST" });
      const card = b.closest(".room-card") || b.closest(".topbar") || b.parentElement;
      const badge = card?.querySelector?.("[data-badge]");
      if (next === "pause") {
        if (badge) { badge.textContent = "stopped"; badge.className = "badge stop"; }
        b.dataset.next = "resume";
        b.textContent = "Resume";
        b.className = "btn sm action accent-resume";
      } else {
        if (badge) { badge.textContent = "running"; badge.className = "badge ok"; }
        b.dataset.next = "pause";
        b.textContent = "Pause";
        b.className = "btn sm action accent-pause";
      }
    }));
  }

  function renderGate() {
    state.shellBuilt = false;
    const saved = localStorage.getItem(BOT_KEY) || "";
    app.innerHTML = "";
    if (state.gateStep === "code") {
      const card = el(`<div class="auth-wrap"><div class="auth-card">
        <p class="auth-kicker">VPS MANAGE</p>
        <h1>Enter the code</h1>
        <p class="lead">Sent to your Telegram · valid ${state.expiresIn} seconds</p>
        <form id="f">
          <div class="field"><label>Login code</label>
            <input name="code" required autofocus inputmode="numeric" autocomplete="one-time-code" placeholder="6-digit code" /></div>
          <p class="error" id="err"></p>
          <button class="btn primary" style="width:100%" type="submit">Continue</button>
          <button class="btn ghost" style="width:100%;margin-top:8px" type="button" id="back">Use another bot</button>
        </form></div></div>`);
      app.appendChild(card);
      card.querySelector("#back").onclick = () => { state.gateStep = "token"; renderGate(); };
      card.querySelector("#f").onsubmit = async (e) => {
        e.preventDefault();
        try {
          await api("/api/gate/verify", { method: "POST", body: JSON.stringify({ bot_token: saved, code: new FormData(e.target).get("code") }) });
          state.gated = true; state.me = null; render();
        } catch (ex) { card.querySelector("#err").textContent = ex.message || "Server stopped"; }
      };
      return;
    }
    const card = el(`<div class="auth-wrap"><div class="auth-card">
      <p class="auth-kicker">VPS MANAGE</p>
      <h1>Unlock the panel</h1>
      <p class="lead">Paste your Telegram bot token. A one-time code is sent to the owner chat set at install.</p>
      <form id="f">
        <div class="field"><label>Telegram bot token</label>
          <input name="bot_token" type="password" required value="${esc(saved)}" autocomplete="off" placeholder="123456:ABC…" /></div>
        <p class="error" id="err"></p>
        <button class="btn primary" style="width:100%" type="submit">Send code</button>
      </form></div></div>`);
    app.appendChild(card);
    card.querySelector("#f").onsubmit = async (e) => {
      e.preventDefault();
      const token = String(new FormData(e.target).get("bot_token") || "").trim();
      try {
        localStorage.setItem(BOT_KEY, token);
        const res = await api("/api/gate/challenge", { method: "POST", body: JSON.stringify({ bot_token: token }) });
        state.expiresIn = res.expires_in || 30; state.gateStep = "code"; renderGate();
      } catch (ex) { card.querySelector("#err").textContent = ex.message || "Server stopped"; }
    };
  }

  function renderUnlock() {
    state.shellBuilt = false;
    // Sticky admin cookie — skip password form if already proven this browser.
    (async () => {
      try {
        await api("/api/auth/admin", { method: "POST", body: JSON.stringify({}) });
        await loadMe();
        if (state.me?.kind === "owner") {
          const parsed = parsePath(location.pathname);
          if (parsed) Object.assign(state, parsed);
          else state.view = "server";
          connectWS();
          render();
          return;
        }
      } catch {}
      paintUnlockForm();
    })();
  }

  async function paintUnlockForm() {
    let hasRooms = false;
    try {
      const opt = await api("/api/auth/options");
      hasRooms = !!opt.has_rooms;
    } catch {}
    const roomBlock = hasRooms ? `
      <div class="auth-split">or</div>
      <form id="room">
        <p class="auth-sec">Open a project</p>
        <div class="field"><label>Room password</label>
          <input name="password" type="password" required autocomplete="current-password" placeholder="Password of any room" /></div>
        <p class="error" id="rerr"></p>
        <button class="btn" style="width:100%" type="submit">Open</button>
      </form>` : "";
    const card = el(`<div class="auth-wrap"><div class="auth-card">
      <p class="auth-kicker">VPS MANAGE</p>
      <h1>Sign in</h1>
      <p class="lead">Use the panel password you set during install.</p>
      <form id="own">
        <p class="auth-sec">Admin</p>
        <div class="field"><label>Panel password</label>
          <input name="password" type="password" required autocomplete="current-password" autofocus /></div>
        <p class="error" id="oerr"></p>
        <button class="btn primary" style="width:100%" type="submit">Sign in</button>
      </form>
      ${roomBlock}
    </div></div>`);
    app.innerHTML = "";
    app.appendChild(card);
    card.querySelector("#own").onsubmit = async (e) => {
      e.preventDefault();
      try {
        await api("/api/auth/owner", {
          method: "POST",
          body: JSON.stringify({ password: new FormData(e.target).get("password") }),
        });
        await loadMe();
        const parsed = parsePath(location.pathname);
        if (parsed) Object.assign(state, parsed);
        else state.view = "server";
        connectWS();
        render();
      } catch (ex) {
        card.querySelector("#oerr").textContent = ex.message || "Wrong password";
      }
    };
    card.querySelector("#room")?.addEventListener("submit", async (e) => {
      e.preventDefault();
      try {
        await api("/api/auth/room/login", {
          method: "POST",
          body: JSON.stringify({ password: new FormData(e.target).get("password") }),
        });
        await loadMe();
        state.view = "room";
        render();
      } catch (ex) {
        card.querySelector("#rerr").textContent = ex.message || "Wrong password";
      }
    });
  }

  function ensureShell(active) {
    const isOwner = state.me?.kind === "owner";
    let root = document.querySelector(".shell");
    if (!root || !state.shellBuilt) {
      app.innerHTML = "";
      root = el(`<div class="shell">
        <header class="mobile-bar">
          <button class="mobile-toggle" id="menu" title="Menu" aria-label="Menu"><span></span><span></span><span></span></button>
          <div class="mobile-bar-brand">
            <strong>VPS MANAGE</strong>
            <span id="mobile-role">${isOwner ? "Admin" : `Room · ${esc(state.me?.room?.name || "")}`}</span>
          </div>
        </header>
        <div class="backdrop" id="backdrop"></div>
        <aside class="sidebar" id="sidebar">
          <div class="sidebar-head">
            <div class="brand">
              <span class="brand-mark" aria-hidden="true"></span>
              <div>
                <h1>VPS MANAGE</h1>
                <p id="brand-role">${isOwner ? "Admin" : `Room · ${esc(state.me?.room?.name || "")}`}</p>
              </div>
            </div>
            <nav class="nav" id="nav"></nav>
          </div>
          <div class="sidebar-foot">
            <div class="meta">Panel :9090</div>
            <button class="btn ghost sidebar-out" id="logout">Sign out</button>
          </div>
        </aside>
        <main class="main" id="main"></main>
      </div>`);
      app.appendChild(root);
      state.shellBuilt = true;

      root.querySelector("#logout").onclick = async () => {
        await api("/api/auth/logout", { method: "POST" });
        state.me = null; state.gated = false; state.gateStep = "token"; state.shellBuilt = false;
        state.sidebarOpen = false;
        document.body.classList.remove("nav-open");
        if (state.ws) { try { state.ws.close(); } catch {} state.ws = null; }
        render();
      };
      const syncDrawer = () => {
        root.querySelector("#sidebar")?.classList.toggle("open", state.sidebarOpen);
        root.querySelector("#backdrop")?.classList.toggle("show", state.sidebarOpen);
        document.body.classList.toggle("nav-open", state.sidebarOpen);
      };
      const toggle = () => {
        state.sidebarOpen = !state.sidebarOpen;
        syncDrawer();
      };
      root.querySelector("#menu").onclick = toggle;
      root.querySelector("#backdrop").onclick = toggle;
    }

    const brand = root.querySelector("#brand-role");
    if (brand) brand.textContent = isOwner ? "Admin" : `Room · ${state.me?.room?.name || ""}`;
    const mobileRole = root.querySelector("#mobile-role");
    if (mobileRole) mobileRole.textContent = isOwner ? "Admin" : `Room · ${state.me?.room?.name || ""}`;

    const nav = root.querySelector("#nav");
    const items = isOwner
      ? [["server", "Server"], ["rooms", "Projects"], ["deploy", "Deploy"], ["restore", "Restore"], ["logs", "Logs"], ["docs", "Docs"], ["settings", "Settings"]]
      : [["room", "Room"], ["rooms", "All rooms"]];
    const highlight = navHighlight(active || state.view);
    nav.innerHTML = items.map(([k, label]) => {
      const locked = k === "restore" && !state.backupReady;
      return `<button data-go="${k}" class="${highlight === k ? "active" : ""} ${locked ? "nav-locked" : ""}" ${locked ? "title=\"Add & validate GitHub PAT first\"" : ""}>${label}${locked ? " 🔒" : ""}</button>`;
    }).join("");
    nav.querySelectorAll("[data-go]").forEach((b) => {
      b.onclick = async () => {
        state.sidebarOpen = false;
        root.querySelector("#sidebar")?.classList.remove("open");
        root.querySelector("#backdrop")?.classList.remove("show");
        document.body.classList.remove("nav-open");
        if (b.dataset.go === "restore" && !state.backupReady) {
          alert("Restore is locked until you save and validate a GitHub PAT (classic) with repo scope.");
          setView("settings");
          return;
        }
        if (b.dataset.go === "rooms" && !isOwner) {
          try {
            await unlockOwner();
            setView("rooms");
          } catch (ex) {
            alert(ex.message || "Admin unlock failed");
          }
          return;
        }
        if (b.dataset.go === "room") {
          state.roomId = state.me?.room?.id || state.roomId;
          setView("room", { roomTab: "overview" });
          return;
        }
        setView(b.dataset.go);
      };
    });

    root.querySelector("#sidebar").classList.toggle("open", state.sidebarOpen);
    root.querySelector("#backdrop").classList.toggle("show", state.sidebarOpen);
    document.body.classList.toggle("nav-open", state.sidebarOpen);
    return root.querySelector("#main");
  }

  function shell(content, active) {
    const main = ensureShell(active);
    const prev = state._viewKey;
    const next = active || state.view || "";
    main.innerHTML = content;
    main.classList.remove("view-enter", "view-enter-fast");
    if (prev && prev !== next) {
      void main.offsetWidth;
      main.classList.add("view-enter");
    } else {
      main.classList.add("view-enter-fast");
    }
    state._viewKey = next;
  }

  function storagePanel(st) {
    const avail = st.quota_available_gb ?? gb(st.quota_available);
    return `<div class="panel storage-panel">
      <h3>Disk allocation</h3>
      <div class="storage-grid">
        <div><span class="muted">Free disk</span><strong>${fmtBytes(st.disk_free)}</strong></div>
        <div><span class="muted">Reserved by projects</span><strong>${fmtBytes(st.quota_reserved)}</strong></div>
        <div><span class="muted">Available to allocate</span><strong class="ok-text">${avail.toFixed(2)} GB</strong></div>
      </div>
      <p class="muted" style="margin-top:10px;font-size:0.82rem">Pick a quota from available space. Deploy is blocked without a quota.</p>
    </div>`;
  }

  function portsList(ports) {
    const list = (ports || []).slice().sort((a, b) => a - b);
    return `<div class="ports-box">
      <div class="muted" style="margin-bottom:6px;font-size:0.78rem">Used ports</div>
      <div class="ports-scroll">${list.map((p) => `<span class="port-chip">${p}</span>`).join("") || `<span class="muted">none</span>`}</div>
    </div>`;
  }

  async function renderServer() {
    const gen = state._gen;
    const paint = (host) => {
      if (!host) {
        shell(`<div class="topbar"><div><h2>Server</h2><div class="sub">Loading…</div></div></div>${skel(4)}`, "server");
        return;
      }
      const cores = Math.max(1, Number(host.cpu_cores) || 1);
      const loadPct = Math.min(100, (Number(host.load1) || 0) / cores * 100);
      const pub = host.public_ip || host.primary_ip || "";
      const lan = host.primary_ip && host.primary_ip !== pub ? host.primary_ip : "";
      const dockerLabel = host.docker ? "connected" : "offline";
      const fact = (k, v, copy) => {
        const val = (v == null || v === "") ? "—" : String(v);
        return `<div class="fact-row"><span>${k}</span><strong class="${copy ? "mono copyable" : "mono"}"${copy ? ` data-copy="${esc(val)}"` : ""}>${esc(val)}</strong></div>`;
      };
      const group = (title, rows) => `<section class="fact-group"><h4>${title}</h4>${rows.filter(Boolean).join("")}</section>`;
      shell(`
      <div class="topbar"><div>
        <h2>Server</h2>
        <div class="sub">${esc(host.hostname || "")} · ${esc(host.os || "Linux")} · Docker ${dockerLabel}</div>
      </div></div>
      <div class="grid stats-grid" data-cores="${cores}">
        <div class="stat">
          <div class="label">Total disk</div>
          <div class="value" data-metric="disk-total">${fmtDisk(host.disk_total)}</div>
          <div class="muted" data-metric="disk-sub">used ${fmtDisk(host.disk_used)} · free ${fmtDisk(host.disk_free)}</div>
          <div class="bar"><span data-bar="disk" style="width:${host.disk_percent || 0}%"></span></div>
        </div>
        <div class="stat">
          <div class="label">CPU</div>
          <div class="value" data-metric="cpu">${pct(host.cpu_percent)}</div>
          <div class="muted">${cores} ${cores === 1 ? "core" : "cores"}</div>
          <div class="bar"><span data-bar="cpu" style="width:${host.cpu_percent || 0}%"></span></div>
        </div>
        <div class="stat">
          <div class="label">Memory</div>
          <div class="value" data-metric="mem">${pct(host.mem_percent)}</div>
          <div class="muted" data-metric="mem-sub">${fmtBytes(host.mem_used)} / ${fmtBytes(host.mem_total)}</div>
          <div class="bar"><span data-bar="mem" style="width:${host.mem_percent || 0}%"></span></div>
        </div>
        <div class="stat">
          <div class="label">Load / Net</div>
          <div class="value" data-metric="load">${host.load1 != null ? Number(host.load1).toFixed(2) : "—"}</div>
          <div class="muted" data-metric="net">↓ ${fmtBytes(host.net_rx)} · ↑ ${fmtBytes(host.net_tx)}</div>
          <div class="bar"><span data-bar="load" style="width:${loadPct}%"></span></div>
        </div>
      </div>
      <div class="panel vps-facts">
        <h3>VPS details</h3>
        <div class="facts-wrap">
          ${group("Identity", [
            fact("Hostname", host.hostname, true),
            fact("OS", host.os),
            fact("Kernel", host.kernel),
            fact("Arch", host.arch),
            fact("Uptime", host.uptime),
            fact("Hypervisor", host.virt),
          ])}
          ${group("Network", [
            fact("Public IP", pub, true),
            lan ? fact("Private IP", lan, true) : "",
            fact("SSH", host.ssh_port || 22, true),
          ])}
          ${group("Hardware", [
            fact("CPU", [host.cpu_model, cores ? cores + " cores" : ""].filter(Boolean).join(" · ")),
            fact("Memory", fmtBytes(host.mem_total)),
            fact("Disk", `${fmtDisk(host.disk_total)} · ${fmtDisk(host.disk_used)} used · ${fmtDisk(host.disk_free)} free`),
          ])}
          ${group("Platform", [
            fact("Docker", dockerLabel),
            fact("Panel", "VPS MANAGE · :9090"),
          ])}
        </div>
      </div>`, "server");
      bindCopyables();
      updateMetricsDOM();
    };
    paint(state.cache.host || null);
    try {
      const host = await api("/api/host");
      if (!alive("server", gen)) return;
      state.cache.host = host;
      paint(host);
    } catch (e) {
      if (!alive("server", gen)) return;
      if (!state.cache.host) shell(`<p class="error">${esc(e.message)}</p>`, "server");
    }
  }

  async function renderRooms() {
    const gen = state._gen;
    const paint = (rooms) => {
      if (!rooms) {
        shell(`<div class="topbar"><div><h2>Projects</h2><div class="sub">Admin view · all rooms and passwords</div></div>
          <button class="btn primary action" id="go-deploy">Deploy new</button></div>${skel(4)}`, "rooms");
        document.querySelector("#go-deploy")?.addEventListener("click", () => setView("deploy"));
        return;
      }
      const cards = (rooms || []).map((r) => {
        const st = r.status === "running" ? "ok" : r.status === "stopped" ? "stop" : "miss";
        const used = r.quota_bytes ? `${fmtBytes(r.usage_bytes)} / ${fmtBytes(r.quota_bytes)}` : fmtBytes(r.usage_bytes);
        const pw = r.password || "";
        return `<div class="room-card" data-room="${r.id}">
          <div class="head"><h4>${esc(r.name)}</h4><span class="badge ${st}" data-badge>${esc(r.status)}</span></div>
          <div style="margin-top:10px">
            <div class="muted" style="font-size:0.75rem">Room password</div>
            <div class="mono copyable" data-copy="${esc(pw)}" style="margin-top:4px;font-size:0.95rem;color:#93c5fd">${pw ? esc(pw) : "—"}</div>
          </div>
          <div class="muted" style="margin-top:8px;font-size:0.85rem">disk ${used}</div>
          <div class="row-actions" style="margin-top:14px">
            <button class="btn sm primary action" data-enter="${r.id}" data-name="${esc(r.name)}" data-pass="${esc(pw)}">Open</button>
            ${powerToggleHTML(r.id, r.status)}
            <button class="btn sm danger action" data-del="${r.id}">Delete</button>
          </div>
        </div>`;
      }).join("") || `<div class="panel"><p class="muted">No projects yet — use Deploy and set a disk quota first.</p></div>`;

      shell(`
        <div class="topbar"><div>
          <h2>Projects</h2>
          <div class="sub">Admin view · all rooms and passwords</div>
        </div>
        <button class="btn primary action" id="go-deploy">Deploy new</button>
        </div>
        <div class="rooms-grid">${cards}</div>`, "rooms");

      document.querySelector("#go-deploy")?.addEventListener("click", () => setView("deploy"));
      bindCopyables();
      document.querySelectorAll("[data-enter]").forEach((b) => bindAction(b, async () => {
        const roomId = b.dataset.enter;
        if (state.me?.kind === "owner") {
          state.showNetPanel = false;
          setView("room", { roomId, roomTab: "overview", termLines: [] });
          return;
        }
        let pw = b.dataset.pass || "";
        if (!pw) {
          pw = prompt(`Password for project "${b.dataset.name || ""}"`) || "";
        }
        if (!pw) throw new Error("Room password required");
        await api(`/api/rooms/${roomId}/enter`, {
          method: "POST",
          body: JSON.stringify({ password: pw }),
        });
        await loadMe();
        state.showNetPanel = false;
        setView("room", { roomId, roomTab: "overview", termLines: [] });
      }));
      bindPowerToggles();
      document.querySelectorAll("[data-del]").forEach((b) => bindAction(b, async () => {
        if (!confirm("Delete this project/room?")) return;
        await api(`/api/rooms/${b.dataset.del}`, { method: "DELETE" });
        b.closest(".room-card")?.remove();
      }));
    };
    paint(state.cache.rooms || null);
    try {
      const rooms = await api("/api/rooms");
      if (!alive("rooms", gen)) return;
      state.cache.rooms = rooms;
      paint(rooms);
    } catch (e) {
      if (!alive("rooms", gen)) return;
      if (!state.cache.rooms) shell(`<p class="error">${esc(e.message)}</p>`, "rooms");
    }
  }

  function parsePullCmd(cmd) {
    const raw = String(cmd || "").trim().replace(/^sudo\s+/, "");
    const f = raw.split(/\s+/).filter(Boolean);
    if (f[0] === "docker" && f[1] === "pull" && f[2]) return f[2];
    if (f.length === 1 && (f[0].includes(":") || f[0].includes("/"))) return f[0];
    if (f.length === 1 && /^[a-z0-9._-]+$/i.test(f[0])) return f[0];
    return "";
  }

  function suggestName(image) {
    return String(image || "app")
      .toLowerCase()
      .replace(/[:/]/g, "-")
      .replace(/[^a-z0-9_-]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 30) || "app";
  }

  function parseDeployOK(text) {
    const m = String(text || "").match(/OK room=(\S+) room_id=(\S+) project=(\S+) password=(\S+)/);
    if (!m) return null;
    return { room: m[1], roomId: m[2], project: m[3], password: m[4] };
  }

  function appendTerm(el, s) {
    if (!el) return;
    el.textContent += s;
    el.scrollTop = el.scrollHeight;
  }

  async function streamFetch(path, opts, onChunk) {
    const res = await fetch(path, {
      credentials: "same-origin",
      headers: opts.body && !(opts.body instanceof FormData)
        ? { "Content-Type": "application/json", ...(opts.headers || {}) }
        : (opts.headers || {}),
      ...opts,
    });
    const reader = res.body && res.body.getReader ? res.body.getReader() : null;
    const dec = new TextDecoder();
    let text = "";
    if (!reader) {
      text = await res.text();
      if (onChunk) onChunk(text, text);
      if (!res.ok) throw new Error(text || "Request failed");
      return text;
    }
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      const chunk = dec.decode(value, { stream: true });
      text += chunk;
      if (onChunk) onChunk(chunk, text);
    }
    if (!res.ok) throw new Error(text || "Request failed");
    return text;
  }

  async function renderDeploy() {
    const gen = state._gen;
    shell(`<div class="topbar"><div><h2>Deploy</h2><div class="sub">Quota is required · room is created automatically</div></div></div>${skel(3)}`, "deploy");
    let st = {}, ports = { used_ports: [] };
    try {
      [st, ports] = await Promise.all([api("/api/storage"), api("/api/ports")]);
    } catch (e) {
      if (!alive("deploy", gen)) return;
      shell(`<p class="error">${esc(e.message)}</p>`, "deploy"); return;
    }
    if (!alive("deploy", gen)) return;
    const maxGB = Math.max(0.1, Number(st.quota_available_gb || 0));

    shell(`
      <div class="topbar"><div>
        <h2>Deploy</h2>
        <div class="sub">Upload one Docker image, set disk quota, start</div>
      </div></div>
      <div class="deploy-layout">
        <div class="panel deploy-card">
          <h3>Pull image</h3>
          <div class="term term-deploy">
            <div class="term-out" id="term-out">root@vps-manage:~# ready
Type: docker pull nginx:alpine
</div>
            <form class="term-in" id="term-form">
              <span class="prompt">root@vps-manage:~#</span>
              <input id="term-cmd" autocomplete="off" spellcheck="false" placeholder="docker pull nginx:alpine" />
            </form>
          </div>
          <div class="cmd-hints">
            <button type="button" class="hint" data-hint="docker pull nginx:alpine">docker pull nginx:alpine</button>
            <button type="button" class="hint" data-hint="docker pull redis:alpine">docker pull redis:alpine</button>
          </div>
          <div class="field full" style="margin-top:12px">${quotaSliderHTML({ name: "quota_gb", id: "pull-quota", maxGB, valueGB: Math.min(1, maxGB), required: true })}</div>
          <div id="pull-setup" class="deploy-setup hidden">
            <p class="ok-text" id="pull-ready">Image ready</p>
            <form id="pull-finish" class="form-grid">
              <input type="hidden" id="pull-image" />
              <div class="field"><label>Host port</label><input id="pull-port" type="number" min="1" max="65535" placeholder="auto" /></div>
              <div class="field"><label>Container port</label><input id="pull-cport" type="number" value="80" /></div>
              <div class="full">${portsList(ports.used_ports)}</div>
              <div class="full"><button class="btn primary action" type="submit">Start</button></div>
            </form>
          </div>
          <div id="pull-done" class="deploy-result hidden"></div>
        </div>
        <div class="panel deploy-card">
          <h3>Upload image</h3>
          <form id="up">
            <label class="dropzone" id="dz">
              <input name="file" id="dz-input" type="file" required />
              <div class="dz-icon">Image</div>
              <div class="dz-title">Docker image (.tar) or Dockerfile</div>
              <div class="dz-sub">One file · drop or click · then set quota</div>
              <div class="dz-file hidden" id="dz-name"></div>
            </label>
            <div class="field full" style="margin-top:12px">${quotaSliderHTML({ name: "quota_gb", maxGB, valueGB: Math.min(1, maxGB), required: true })}</div>
            <div id="up-setup" class="deploy-setup hidden">
              <div class="form-grid">
                <div class="field"><label>Host port</label><input name="host_port" type="number" min="1" max="65535" placeholder="auto" /></div>
                <div class="field"><label>Container port</label><input name="container_port" type="number" value="80" /></div>
                <div class="full">${portsList(ports.used_ports)}</div>
                <div class="full"><button class="btn primary action" type="submit">Start</button></div>
              </div>
            </div>
            <p class="error" id="uperr"></p>
            <div class="term hidden" id="up-term"><div class="term-out" id="up-log"></div></div>
            <div id="up-done" class="deploy-result hidden"></div>
          </form>
        </div>
      </div>`, "deploy");

    const termOut = document.querySelector("#term-out");
    const termCmd = document.querySelector("#term-cmd");
    const showResult = (host, parsed) => {
      if (!host || !parsed) return;
      host.classList.remove("hidden");
      host.innerHTML = `<div class="ok-text">Room created</div>
        <div class="fact-row"><span>Name</span><strong class="mono">${esc(parsed.room)}</strong></div>
        <div class="fact-row"><span>Password</span><strong class="mono copyable" data-copy="${esc(parsed.password)}">${esc(parsed.password)}</strong></div>
        <button class="btn primary action" type="button" data-open-room="${esc(parsed.roomId)}">Open room</button>
        <p class="muted" style="margin:8px 0 0;font-size:0.78rem">You can change the name, password, quota, and ports inside the room.</p>`;
      bindCopyables(host);
      host.querySelector("[data-open-room]")?.addEventListener("click", () => {
        state.roomId = parsed.roomId;
        setView("room", { roomTab: "overview" });
      });
    };

    bindQuotaSliders();
    document.querySelectorAll("[data-hint]").forEach((b) => {
      b.onclick = () => { termCmd.value = b.dataset.hint; termCmd.focus(); };
    });

    const dz = document.querySelector("#dz");
    const dzInput = document.querySelector("#dz-input");
    const dzName = document.querySelector("#dz-name");
    const upSetup = document.querySelector("#up-setup");
    const markFile = (file) => {
      if (!file) return;
      dz.classList.add("has-file");
      dzName.textContent = file.name;
      dzName.classList.remove("hidden");
      upSetup.classList.remove("hidden");
    };
    dzInput.addEventListener("change", () => markFile(dzInput.files && dzInput.files[0]));
    ["dragenter", "dragover"].forEach((ev) => dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.add("drag"); }));
    ["dragleave", "drop"].forEach((ev) => dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.remove("drag"); }));
    dz.addEventListener("drop", (e) => {
      const file = e.dataTransfer?.files?.[0];
      if (!file) return;
      const dt = new DataTransfer();
      dt.items.add(file);
      dzInput.files = dt.files;
      markFile(file);
    });

    document.querySelector("#term-form").onsubmit = async (e) => {
      e.preventDefault();
      const cmd = termCmd.value.trim();
      if (!cmd) return;
      const image = parsePullCmd(cmd);
      const quota = Number(document.querySelector("#pull-quota").value || 0);
      if (!(quota > 0)) { appendTerm(termOut, "error: set disk quota first\n"); return; }
      if (quota > maxGB + 0.001) { appendTerm(termOut, `error: quota exceeds available ${maxGB.toFixed(2)} GB\n`); return; }
      if (!image) { appendTerm(termOut, "error: use docker pull IMAGE\n"); return; }
      appendTerm(termOut, `${cmd}\n`);
      termCmd.value = "";
      termCmd.disabled = true;
      try {
        const text = await streamFetch("/api/deploy/pull", {
          method: "POST",
          body: JSON.stringify({ command: cmd, image }),
        }, (chunk) => appendTerm(termOut, chunk));
        const ok = /OK image=(\S+)/.exec(text);
        if (ok) {
          document.querySelector("#pull-image").value = ok[1];
          document.querySelector("#pull-ready").textContent = `Image ready · ${ok[1]}`;
          document.querySelector("#pull-setup").classList.remove("hidden");
        }
      } catch (ex) {
        appendTerm(termOut, `\n${ex.message || ex}\n`);
      } finally {
        termCmd.disabled = false;
        termCmd.focus();
      }
    };

    document.querySelector("#pull-finish").onsubmit = async (e) => {
      e.preventDefault();
      const image = document.querySelector("#pull-image").value.trim();
      const name = suggestName(image);
      const quota = Number(document.querySelector("#pull-quota").value || 0);
      const btn = e.target.querySelector("button[type=submit]");
      if (btn) { btn.classList.add("busy"); btn.disabled = true; }
      appendTerm(termOut, `\nStarting ${name}...\n`);
      try {
        const text = await streamFetch("/api/deploy", {
          method: "POST",
          body: JSON.stringify({
            image,
            name,
            quota_gb: quota,
            host_port: Number(document.querySelector("#pull-port").value || 0) || 0,
            container_port: Number(document.querySelector("#pull-cport").value || 80) || 80,
          }),
        }, (chunk) => appendTerm(termOut, chunk));
        showResult(document.querySelector("#pull-done"), parseDeployOK(text));
      } catch (ex) {
        appendTerm(termOut, `\n${ex.message || ex}\n`);
      } finally {
        if (btn) { btn.classList.remove("busy"); btn.disabled = false; }
      }
    };

    document.querySelector("#up").onsubmit = async (e) => {
      e.preventDefault();
      const fd = new FormData(e.target);
      const q = Number(fd.get("quota_gb") || 0);
      const err = document.querySelector("#uperr");
      const log = document.querySelector("#up-log");
      const term = document.querySelector("#up-term");
      err.textContent = "";
      if (!(q > 0)) { err.textContent = "Set disk quota (GB) before deploy."; return; }
      if (q > maxGB + 0.001) { err.textContent = `Quota exceeds available ${maxGB.toFixed(2)} GB.`; return; }
      const file = fd.get("file");
      if (!file || !file.name) { err.textContent = "Upload a Docker image (.tar) or a Dockerfile."; return; }
      if (!fd.get("name")) fd.set("name", suggestName(file.name.replace(/\.(tar|gz|tgz)$/i, "")));
      term.classList.remove("hidden");
      log.textContent = "Uploading…\n";
      const btn = e.target.querySelector("button[type=submit]");
      if (btn) { btn.classList.add("busy"); btn.disabled = true; }
      try {
        const text = await streamFetch("/api/deploy", { method: "POST", body: fd }, (chunk, all) => {
          log.textContent = all;
          log.scrollTop = log.scrollHeight;
        });
        showResult(document.querySelector("#up-done"), parseDeployOK(text));
      } catch (ex) {
        log.textContent += `\n${ex.message || ex}`;
        err.textContent = ex.message || "Deploy failed";
      } finally {
        if (btn) { btn.classList.remove("busy"); btn.disabled = false; }
      }
    };
  }

  async function renderLogs() {
    const gen = state._gen;
    const kind = state.logKind || "panel";
    shell(`<div class="topbar"><div><h2>Logs</h2><div class="sub">Newest at bottom · auto-scroll · live refresh</div></div></div>${skel(2)}`, "logs");
    let data = { log: "", kinds: ["panel", "api", "deploy", "host"] };
    try { data = await api(`/api/host/logs?kind=${encodeURIComponent(kind)}`); } catch (e) {
      if (!alive("logs", gen)) return;
      shell(`<p class="error">${esc(e.message)}</p>`, "logs"); return;
    }
    if (!alive("logs", gen)) return;
    const raw = data.log || "";
    const kinds = data.kinds || ["panel", "api", "deploy", "host"];
    const labels = { panel: "Panel", api: "API", deploy: "Deploy", host: "Host events" };
    const lineCount = raw.split("\n").filter(Boolean).length;

    shell(`
      <div class="topbar"><div>
        <h2>Logs</h2>
        <div class="sub">Newest at bottom · auto-scroll · live refresh</div>
      </div>
        <div class="row-actions">
          <button class="btn sm action" id="refresh">Refresh</button>
          <button class="btn sm action" id="copy">Copy</button>
          <button class="btn sm danger action" id="clear">Clear this</button>
          <button class="btn sm danger action" id="clear-all">Delete all logs</button>
        </div>
      </div>
      <div class="logs-layout">
        <div class="logs-nav">
          ${kinds.map((k) =>
            `<button data-kind="${esc(k)}" class="${kind === k ? "active" : ""}">${esc(labels[k] || k)}</button>`).join("")}
        </div>
        <div class="logs-viewer">
          <div class="logs-toolbar">
            <div class="logs-meta" id="log-meta">${esc(labels[kind] || kind)} · ${lineCount} lines · live</div>
            <span class="muted mono" style="font-size:0.72rem" id="log-ts">${new Date().toISOString()}</span>
          </div>
          <div class="logs-body" id="logbox">${formatLogHTML(raw)}</div>
        </div>
      </div>`, "logs");

    const box = document.querySelector("#logbox");
    pinLogBottom(box);

    const refreshLive = async () => {
      if (state.view !== "logs") { stopLogLive(); return; }
      const k = state.logKind || "panel";
      try {
        const d = await api(`/api/host/logs?kind=${encodeURIComponent(k)}`);
        const text = d.log || "";
        const el = document.querySelector("#logbox");
        if (!el) return;
        const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
        const html = formatLogHTML(text);
        if (el.innerHTML !== html) {
          el.innerHTML = html;
          if (nearBottom || state._logPinBottom !== false) pinLogBottom(el);
        }
        const meta = document.querySelector("#log-meta");
        const ts = document.querySelector("#log-ts");
        if (meta) meta.textContent = `${labels[k] || k} · ${text.split("\n").filter(Boolean).length} lines · live`;
        if (ts) ts.textContent = new Date().toISOString();
      } catch {}
    };

    box?.addEventListener("scroll", () => {
      const el = document.querySelector("#logbox");
      if (!el) return;
      // Keep pinning only while user stays near the bottom.
      state._logPinBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
    });
    state._logPinBottom = true;
    startLogLive(refreshLive);

    document.querySelectorAll("[data-kind]").forEach((b) => b.onclick = () => {
      state.logKind = b.dataset.kind;
      state._logPinBottom = true;
      render();
    });
    document.querySelector("#refresh").onclick = () => { state._logPinBottom = true; render(); };
    document.querySelector("#copy").onclick = () => copyText(document.querySelector("#logbox").innerText);
    bindAction(document.querySelector("#clear"), async () => {
      await api(`/api/host/logs/clear?kind=${encodeURIComponent(kind)}`, { method: "POST" });
      toast("Log cleared");
      state._logPinBottom = true;
      await renderLogs();
    });
    bindAction(document.querySelector("#clear-all"), async () => {
      if (!confirm("Delete all panel log files?")) return;
      await api("/api/host/logs/clear?all=1", { method: "POST" });
      toast("All logs deleted");
      state.logKind = "panel";
      state._logPinBottom = true;
      await renderLogs();
    });
  }

  function formatLogHTML(text) {
    return String(text || "").split("\n").map((line) => {
      const t = esc(line);
      if (/error|fail|denied|panic/i.test(line)) return `<div class="log-line-err">${t}</div>`;
      if (/^=== /.test(line)) return `<div class="log-line-sec">${t}</div>`;
      if (/ok|success|started|listening|cleared/i.test(line)) return `<div class="log-line-ok">${t}</div>`;
      return `<div>${t}</div>`;
    }).join("");
  }

  function cmdCard(title, code) {
    return `<div class="cmd-card">
      <div class="cmd-card-head"><h4>${esc(title)}</h4>
        <button type="button" class="btn sm action" data-copy-cmd>Copy</button></div>
      <pre class="cmd-pre">${esc(code)}</pre>
    </div>`;
  }

  function bindCmdCopies(root = document) {
    root.querySelectorAll("[data-copy-cmd]").forEach((b) => {
      b.onclick = () => copyText(b.closest(".cmd-card")?.querySelector(".cmd-pre")?.textContent || "");
    });
  }

  async function renderDocs() {
    const ssh = `ssh root@YOUR_VPS_IP`;
    const install = `curl -fsSL https://raw.githubusercontent.com/X5Coder/VPS-Manager/main/install.sh | bash`;
    const alt = `git clone https://github.com/X5Coder/VPS-Manager.git
cd VPS-Manager
bash install.sh`;
    const saveImg = `docker build -t myapp:latest .
docker save -o myapp.tar myapp:latest`;
    const step = (n, title, sub, extra, code) => `<div class="docs-step">
      <div class="docs-n">${n}</div>
      <div class="docs-step-body">
        <h3>${title}</h3>
        <p class="muted">${sub}</p>
        ${extra || ""}
        ${code ? cmdCard("Command", code) : ""}
      </div>
    </div>`;

    shell(`
      <div class="topbar"><div>
        <h2>Docs</h2>
        <div class="sub">Install on a new Ubuntu VPS</div>
      </div></div>
      <p class="docs-lead">Install on the VPS itself. After the script finishes it asks for the panel password and your Telegram user id, then prints the URL.</p>
      <div class="docs-os">
        <div class="panel">
          <h3>Supported</h3>
          <p><strong>Ubuntu 20.04, 22.04, or 24.04</strong> · root access · x86_64 or ARM64.</p>
        </div>
        <div class="panel">
          <h3>Not supported</h3>
          <p>Windows VPS · other Linux distros · shared hosting without root.</p>
        </div>
      </div>
      ${step("1", "SSH into the VPS", "On your computer, replace YOUR_VPS_IP with the address from your provider.", "", ssh)}
      ${step("2", "Run the installer", "As root. Downloads retry on failure. When it succeeds you will be asked for a panel password and your Telegram account id, then the panel URL is shown.", "", install)}
      ${step("3", "Open the URL", "Telegram bot token → 30-second code → panel password.", "<p class=\"muted\">Full guide: <a href=\"https://github.com/X5Coder/VPS-Manager/blob/main/docs/INSTALL.md\" target=\"_blank\" rel=\"noopener\">docs/INSTALL.md</a></p>", "")}
      ${step("4", "Ship a project as one image", "Build on your machine, save one .tar file, upload it in Deploy, set disk quota. The panel creates the room and starts it.", "", saveImg)}
      ${cmdCard("If curl is blocked", alt)}`, "docs");
    bindCmdCopies();
  }

  async function renderSettings() {
    const gen = state._gen;
    shell(`<div class="topbar"><div><h2>Settings</h2><div class="sub">Root SSH · Admin vault · API tokens</div></div></div>${skel(4)}`, "settings");
    let tokens = [], st = {};
    try {
      [tokens, st] = await Promise.all([api("/api/settings/tokens"), api("/api/storage")]);
    } catch (e) {
      if (!alive("settings", gen)) return;
      shell(`<p class="error">${esc(e.message)}</p>`, "settings"); return;
    }
    if (!alive("settings", gen)) return;

    const tokenCards = (tokens || []).map((t) => {
      const secret = t.secret || "";
      const canCopy = !!secret;
      return `<div class="tok-card" data-tok-id="${esc(t.id)}">
        <div class="head-row">
          <div>
            <strong>${esc(t.name)}</strong>
            <span class="badge ${t.mode === "write" ? "ok" : "stop"}" style="margin-left:8px">${esc(t.mode)}</span>
          </div>
          <div class="row-actions">
            <button class="btn sm action" data-copy-btn="${esc(secret)}" ${canCopy ? "" : "disabled"} title="Copy secret">Copy key</button>
            <button class="btn sm action" data-prompt="${esc(t.id)}" ${canCopy ? "" : "disabled"}>Copy prompt</button>
            <button class="btn sm danger action" data-del-tok="${esc(t.id)}">Revoke</button>
          </div>
        </div>
        <div class="field" style="margin-top:10px"><label>Secret</label>
          <input class="mono" readonly value="${esc(secret || "(not stored — revoke and create a new token)")}" /></div>
        <div class="muted" style="font-size:0.75rem;margin-top:6px">created ${esc(t.created_at || "")}${t.last_used_at ? " · last used " + esc(t.last_used_at) : ""}</div>
      </div>`;
    }).join("") || `<p class="muted">No API tokens yet</p>`;

    shell(`
      <div class="topbar"><div>
        <h2>Settings</h2>
        <div class="sub">Root SSH · Admin vault · API tokens</div>
      </div></div>
      <div class="grid-2">
        <div class="panel">
          <h3>Change VPS root password (SSH)</h3>
          <form id="pw-form" class="form-grid">
            <div class="field full"><label>New root password</label><input name="password" type="password" minlength="8" required /></div>
            <div class="full"><button class="btn primary action" type="submit">Update root password</button></div>
          </form>
          <p class="error" id="pwerr"></p>
        </div>
        <div class="panel">
          <h3>Change admin panel password</h3>
          <form id="adminpass" class="form-grid">
            <div class="field full"><label>Current admin password</label><input name="current" type="password" required autocomplete="current-password" /></div>
            <div class="field full"><label>New admin password</label><input name="new_password" type="password" minlength="8" required autocomplete="new-password" /></div>
            <div class="full"><button class="btn primary action" type="submit">Update admin password</button></div>
          </form>
          <p class="error" id="adminerr"></p>
          <p class="ok-text hidden" id="adminok">Admin password updated.</p>
          <p class="muted" style="margin-top:8px">Variable <span class="mono">VPS_ROOMS_OWNER_PASS</span> — never shown. Gate owner chat id is fixed.</p>
        </div>
      </div>
      <div class="panel" style="margin-top:12px">
        <h3>Storage snapshot</h3>
          <div class="storage-grid">
            <div><span class="muted">Free disk</span><strong>${fmtBytes(st.disk_free)}</strong></div>
            <div><span class="muted">Reserved</span><strong>${fmtBytes(st.quota_reserved)}</strong></div>
            <div><span class="muted">Available</span><strong class="ok-text">${(st.quota_available_gb || 0).toFixed(2)} GB</strong></div>
          </div>
        </div>
      <div class="panel" style="margin-top:12px">
        <h3>API access</h3>
        <p class="muted">Secrets stay visible here so you can copy anytime. Scope: all projects. Delete via API is disabled.</p>
        <form id="tok-form" class="form-grid" style="margin-top:12px">
          <div class="field"><label>Token name</label><input name="name" placeholder="Cursor / Claude" required /></div>
          <div class="field"><label>Permission</label>
            <select name="mode">
              <option value="read">Read only</option>
              <option value="write" selected>Read + write</option>
            </select>
          </div>
          <div class="full"><button class="btn primary action" type="submit">Generate token</button></div>
        </form>
        <p class="error" id="tokerr"></p>
        <div id="tok-list" style="margin-top:14px;display:flex;flex-direction:column;gap:10px">${tokenCards}</div>
      </div>
      <div class="panel" style="margin-top:12px">
        <h3>Access alert bot (optional)</h3>
        <p class="muted">Separate Telegram bot for access notifications. Leave empty to disable. Gate owner chat id stays fixed.</p>
        <form id="notify-form" class="form-grid" style="margin-top:12px">
          <div class="field full"><label>Notify bot token</label><input name="bot_token" type="password" placeholder="123456:ABC…" autocomplete="off" /></div>
          <div class="field full"><label>Notify chat id</label><input name="chat_id" placeholder="e.g. 123456789" autocomplete="off" /></div>
          <div class="full" style="display:flex;gap:8px;flex-wrap:wrap">
            <button class="btn primary action" type="submit">Save alert bot</button>
            <button class="btn sm danger action" type="button" id="notify-clear">Disable alerts</button>
          </div>
        </form>
        <p class="error" id="notifyerr"></p>
        <p class="muted" id="notifystatus" style="margin-top:8px"></p>
      </div>
      <div class="panel" style="margin-top:12px">
        <h3>GitHub backup (full)</h3>
        <p class="muted">Saved on this VPS — you only paste the token once. Backup includes panel settings, project files,
          Postgres (Supabase auth/db and any other Postgres), and storage files. Needs a <strong>classic PAT</strong> with
          <span class="mono">repo</span> scope.</p>
        <p class="ok-text" id="ghsaved"></p>
        <form id="gh-form" class="form-grid" style="margin-top:12px">
          <div class="field full"><label>GitHub PAT (classic)</label>
            <input name="token" type="password" placeholder="ghp_…" autocomplete="off" /></div>
          <div class="full" style="display:flex;gap:8px;flex-wrap:wrap">
            <button class="btn primary action" type="submit" id="gh-save">Save token</button>
            <button class="btn action" type="button" id="bak-now-settings">Backup now</button>
          </div>
        </form>
        <p class="error" id="gherr"></p>
        <p class="muted" id="ghstatus" style="margin-top:8px"></p>
        <div id="job-live-settings"></div>
        <button class="btn sm danger action" id="gh-clear" type="button">Remove GitHub token</button>
      </div>`, "settings");

    bindCopyables();

    document.querySelectorAll("[data-prompt]").forEach((b) => bindAction(b, async () => {
      const res = await api(`/api/settings/tokens/${b.dataset.prompt}`);
      if (!res.prompt) throw new Error("No secret stored for this token — create a new one");
      await copyText(res.prompt);
    }));

    // load backup status
    api("/api/backup/status").then((bk) => {
      const el = document.querySelector("#ghstatus");
      const saved = document.querySelector("#ghsaved");
      const inp = document.querySelector("#gh-form [name=token]");
      if (!el) return;
      state.backupReady = !!bk.configured;
      if (bk.configured) {
        if (saved) saved.textContent = `Token saved on this VPS (${bk.token_hint || "••••"}) for @${bk.github_user || "?"} — you do not need to paste it again.`;
        if (inp) inp.placeholder = "Leave blank — already saved. Paste a new token only to replace.";
        el.textContent = `Enabled for @${bk.github_user || "?"} · last ${bk.last_backup_at || "never"} · next ${bk.next_backup_at || "—"}`;
      } else {
        if (saved) saved.textContent = "";
        el.textContent = "Not configured — backups are disabled until a PAT is saved.";
      }
      if (bk.job) paintJob(bk.job, "#job-live-settings");
      if (bk.job && bk.job.status === "running") startJobPoll("settings");
    }).catch(() => {});

    api("/api/settings/notify").then((n) => {
      const el = document.querySelector("#notifystatus");
      if (!el) return;
      const chat = document.querySelector("#notify-form [name=chat_id]");
      if (chat && n.chat_id) chat.value = n.chat_id;
      el.textContent = n.enabled
        ? `Alerts ON · chat ${n.chat_id || "?"} · token ${n.bot_token_hint || "set"} · gate owner id fixed ${n.owner_chat_id || ""}`
        : `Alerts OFF · gate owner chat id fixed: ${n.owner_chat_id || "—"}`;
    }).catch(() => {});

    document.querySelector("#notify-form")?.addEventListener("submit", async (e) => {
      e.preventDefault();
      const err = document.querySelector("#notifyerr");
      if (err) err.textContent = "";
      const fd = new FormData(e.target);
      try {
        const res = await api("/api/settings/notify", {
          method: "POST",
          body: JSON.stringify({ bot_token: fd.get("bot_token"), chat_id: fd.get("chat_id") }),
        });
        document.querySelector("#notifystatus").textContent = res.enabled
          ? `Alerts ON · chat ${res.chat_id || ""}`
          : "Saved but incomplete — both token and chat id are required to enable.";
        e.target.querySelector("[name=bot_token]").value = "";
        toast("Alert bot saved");
      } catch (ex) { if (err) err.textContent = ex.message; }
    });
    bindAction(document.querySelector("#notify-clear"), async () => {
      await api("/api/settings/notify", { method: "DELETE" });
      document.querySelector("#notifystatus").textContent = "Alerts OFF";
      toast("Alerts disabled");
    });

    document.querySelector("#gh-form").onsubmit = async (e) => {
      e.preventDefault();
      const err = document.querySelector("#gherr");
      err.textContent = "";
      try {
        const raw = String(new FormData(e.target).get("token") || "").trim();
        const bk = await api("/api/backup/token", { method: "POST", body: JSON.stringify({ token: raw }) });
        state.backupReady = !!bk.configured;
        const saved = document.querySelector("#ghsaved");
        if (saved) saved.textContent = `Token saved on this VPS (${bk.token_hint || "••••"}) for @${bk.github_user || "?"} — you do not need to paste it again.`;
        document.querySelector("#ghstatus").textContent = `Enabled for @${bk.github_user} · next ${bk.next_backup_at || "24h"}`;
        e.target.querySelector("[name=token]").value = "";
        e.target.querySelector("[name=token]").placeholder = "Leave blank — already saved. Paste a new token only to replace.";
        toast("GitHub token saved on the server");
      } catch (ex) { err.textContent = ex.message; }
    };
    bindAction(document.querySelector("#bak-now-settings"), async () => {
      const err = document.querySelector("#gherr");
      if (err) err.textContent = "";
      try {
        await api("/api/backup/now", {
          method: "POST",
          body: JSON.stringify({ label: "Manual backup", description: "Backup now from Settings" }),
        });
        toast("Backup started");
        startJobPoll("settings");
      } catch (ex) { if (err) err.textContent = ex.message; }
    });
    bindAction(document.querySelector("#gh-clear"), async () => {
      await api("/api/backup/token", { method: "DELETE" });
      state.backupReady = false;
      document.querySelector("#ghstatus").textContent = "Token removed — backups disabled.";
      state.shellBuilt = false;
      render();
    });

    document.querySelector("#pw-form").onsubmit = async (e) => {
      e.preventDefault();
      const box = document.querySelector("#pwerr");
      try {
        await api("/api/host/password", { method: "POST", body: JSON.stringify({ password: new FormData(e.target).get("password") }) });
        box.textContent = "Root password updated.";
        box.style.color = "var(--ok)";
      } catch (ex) {
        box.textContent = ex.message;
        box.style.color = "var(--danger)";
      }
    };

    document.querySelector("#adminpass")?.addEventListener("submit", async (e) => {
      e.preventDefault();
      const err = document.querySelector("#adminerr");
      const ok = document.querySelector("#adminok");
      if (err) err.textContent = "";
      ok?.classList.add("hidden");
      const fd = new FormData(e.target);
      try {
        await api("/api/settings/owner-password", {
          method: "POST",
          body: JSON.stringify({ current: fd.get("current"), new_password: fd.get("new_password") }),
        });
        ok?.classList.remove("hidden");
        toast("Admin password updated");
        e.target.reset();
      } catch (ex) {
        if (err) err.textContent = ex.message;
      }
    });

    document.querySelector("#tok-form").onsubmit = async (e) => {
      e.preventDefault();
      const fd = new FormData(e.target);
      const err = document.querySelector("#tokerr");
      err.textContent = "";
      try {
        await api("/api/settings/tokens", {
          method: "POST",
          body: JSON.stringify({ name: fd.get("name"), mode: fd.get("mode") }),
        });
        e.target.reset();
        render();
      } catch (ex) { err.textContent = ex.message; }
    };

    document.querySelectorAll("[data-del-tok]").forEach((btn) => bindAction(btn, async () => {
      if (!confirm("Revoke this API token?")) return;
      await api(`/api/settings/tokens/${btn.dataset.delTok}`, { method: "DELETE" });
      render();
    }));
  }

  function jobPanelHTML(job) {
    if (!job) return "";
    const pct = Math.max(0, Math.min(100, Number(job.percent) || 0));
    const logs = (job.logs || []).join("\n");
    const err = job.error ? `\nERROR: ${job.error}` : "";
    return `<div class="job-banner ${esc(job.status || "")}" id="job-banner">
      <div class="job-live">
        <div class="job-live-head">
          <div>
            <strong>${esc((job.kind || "job").toUpperCase())} · ${esc(job.status || "")}</strong>
            <div class="muted" style="margin-top:4px">${esc(job.message || "")}</div>
            <div class="mono" style="margin-top:6px;font-size:0.8rem">${esc(job.progress || "")}</div>
          </div>
          <div class="job-pct mono">${pct}%</div>
        </div>
        <div class="job-bar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${pct}"><span style="width:${pct}%"></span></div>
        <pre class="job-log">${esc(logs + err) || "Waiting for log…"}</pre>
      </div>
    </div>`;
  }

  function paintJob(job, selector) {
    const el = document.querySelector(selector || "#job-live");
    if (!el) return;
    el.innerHTML = jobPanelHTML(job);
    const log = el.querySelector(".job-log");
    if (log) log.scrollTop = log.scrollHeight;
    const running = !!(job && job.status === "running");
    document.querySelector(".restore-hero")?.classList.toggle("is-running", running);
  }

  function startJobPoll(where) {
    clearTimeout(state.jobTimer);
    const tick = async () => {
      const onPage = (where === "settings" && state.view === "settings") ||
        (where !== "settings" && state.view === "restore");
      if (!onPage) return;
      try {
        const bk = await api("/api/backup/status");
        const sel = where === "settings" ? "#job-live-settings" : "#job-live";
        paintJob(bk.job, sel);
        const btn = document.querySelector(where === "settings" ? "#bak-now-settings" : "#bak-now");
        if (btn) {
          const running = !!(bk.job && bk.job.status === "running");
          btn.disabled = running;
          btn.dataset.lock = running ? "1" : "0";
        }
        if (bk.job && bk.job.status === "running") {
          state.jobWasRunning = true;
          state.jobTimer = setTimeout(tick, 1000);
        } else if (state.jobWasRunning && bk.job && bk.job.status !== "running") {
          state.jobWasRunning = false;
          if (where !== "settings" && state.view === "restore") {
            state.jobTimer = setTimeout(() => { if (state.view === "restore") renderRestore(); }, 800);
          }
        }
      } catch {
        state.jobTimer = setTimeout(tick, 2000);
      }
    };
    tick();
  }

  async function renderRestore() {
    const gen = state._gen;
    if (!state.backupReady) {
      shell(`<div class="panel"><h3>Restore locked</h3>
        <p class="muted">Save and validate a GitHub classic PAT with <span class="mono">repo</span> scope in Settings first.</p>
        <button class="btn primary action" id="go-set">Open Settings</button></div>`, "restore");
      document.querySelector("#go-set").onclick = () => setView("settings");
      return;
    }
    shell(`
      <div class="topbar restore-hero">
        <div>
          <h2>Restore & Backup</h2>
          <div class="sub restore-sub"><span class="restore-live" aria-hidden="true"></span>Runs on the server — leave anytime and check status here</div>
        </div>
        <button class="btn primary action bak-now-btn" id="bak-now" disabled>
          <span class="bak-now-ring" aria-hidden="true"></span>
          Backup now
        </button>
      </div>${skel(3)}`, "restore");
    let bk = {};
    try { bk = await api("/api/backup/status"); } catch (e) {
      if (!alive("restore", gen)) return;
      shell(`<p class="error">${esc(e.message)}</p>`, "restore"); return;
    }
    if (!alive("restore", gen)) return;
    state.backupReady = !!bk.configured;
    const job = bk.job;
    const snaps = bk.snapshots || [];
    const rows = snaps.map((s) => `<tr>
      <td><strong>${esc(s.label || s.id)}</strong><div class="muted" style="font-size:0.8rem">${esc(s.description || "")}</div></td>
      <td class="muted">${esc(s.created_at || "")}</td>
      <td><span class="badge ${s.status === "ok" ? "ok" : "miss"}">${esc(s.status || "")}</span></td>
      <td><button class="btn sm primary action" data-restore="${esc(s.id)}">Restore</button></td>
    </tr>`).join("") || `<tr><td colspan="4" class="muted">No local snapshots yet — run Backup now or restore from GitHub.</td></tr>`;

    const jobHTML = `<div id="job-live">${job ? jobPanelHTML(job) : ""}</div>`;

    shell(`
      <div class="topbar restore-hero ${job && job.status === "running" ? "is-running" : ""}">
        <div>
          <h2>Restore & Backup</h2>
          <div class="sub restore-sub"><span class="restore-live" aria-hidden="true"></span>Runs on the server — leave anytime and check status here</div>
        </div>
        <button class="btn primary action bak-now-btn" id="bak-now" ${job && job.status === "running" ? "disabled" : ""}>
          <span class="bak-now-ring" aria-hidden="true"></span>
          Backup now
        </button>
      </div>
      ${jobHTML}
      <div class="grid-2">
        <div class="panel">
          <h3>Status</h3>
          <table class="table">
            <tr><th>GitHub user</th><td class="mono">${esc(bk.github_user || "—")}</td></tr>
            <tr><th>Last backup</th><td>${esc(bk.last_backup_at || "never")}</td></tr>
            <tr><th>Next due</th><td>${esc(bk.next_backup_at || "—")}</td></tr>
          </table>
          <p class="error">${esc(bk.last_error || "")}</p>
        </div>
        <div class="panel">
          <h3>Fetch from GitHub</h3>
          <p class="muted">Inspect remote map. Wrong format is refused.</p>
          <form id="inspect-form" class="form-grid" style="margin-top:10px">
            <div class="field full"><label>PAT (optional if saved)</label><input name="token" type="password" placeholder="ghp_…" /></div>
            <div class="full"><button class="btn action" type="submit">Inspect remote backups</button></div>
          </form>
          <div id="remote-box" class="muted" style="margin-top:10px"></div>
        </div>
      </div>
      <div class="panel" style="margin-top:12px">
        <h3>Snapshots</h3>
        <table class="table"><thead><tr><th>Name / description</th><th>When</th><th>Status</th><th></th></tr></thead>
        <tbody>${rows}</tbody></table>
        <p class="error" id="bakerr"></p>
        <p class="ok-text hidden" id="bakok"></p>
      </div>`, "restore");

    if (job && job.status === "running") {
      startJobPoll("restore");
    }

    bindAction(document.querySelector("#bak-now"), async () => {
      const err = document.querySelector("#bakerr");
      const ok = document.querySelector("#bakok");
      err.textContent = ""; ok.classList.add("hidden");
      try {
        const res = await api("/api/backup/now", {
          method: "POST",
          body: JSON.stringify({ label: "Manual backup", description: "Backup now — 24h timer reset from this moment" }),
        });
        ok.textContent = res.message || "Backup started on server.";
        ok.classList.remove("hidden");
        const nowBtn = document.querySelector("#bak-now");
        if (nowBtn) { nowBtn.dataset.lock = "1"; nowBtn.disabled = true; }
        document.querySelector(".restore-hero")?.classList.add("is-running");
        paintJob({
          kind: "backup", status: "running", percent: 1,
          message: "Backup started — this can take several minutes",
          progress: "Starting…", logs: ["Starting…"],
        }, "#job-live");
        startJobPoll("restore");
      } catch (ex) { err.textContent = ex.message; }
    });

    document.querySelector("#inspect-form").onsubmit = async (e) => {
      e.preventDefault();
      const box = document.querySelector("#remote-box");
      const tok = new FormData(e.target).get("token");
      box.textContent = "Checking…";
      try {
        const res = await api("/api/backup/inspect", { method: "POST", body: JSON.stringify({ token: tok }) });
        const list = res.snapshots || [];
        box.innerHTML = `<p class="ok-text">Format OK · latest: ${esc(res.latest?.label || res.latest?.snapshot_id || "")}</p>
          <div class="row-actions" style="margin-top:8px">${list.slice(0, 8).map((s) =>
            `<button class="btn sm action" data-remote="${esc(s.id)}">${esc(s.label || s.id.slice(0, 8))}</button>`).join("")}
            <button class="btn sm primary action" data-remote="latest">Restore latest</button>
          </div>`;
        box.querySelectorAll("[data-remote]").forEach((b) => bindAction(b, async () => {
          if (!confirm("Start restore on the server? You can leave and check status here.")) return;
          const r = await api("/api/backup/restore", { method: "POST", body: JSON.stringify({ token: tok, snapshot_id: b.dataset.remote }) });
          document.querySelector("#bakok").textContent = r.message || "Restore started.";
          document.querySelector("#bakok").classList.remove("hidden");
          setTimeout(() => renderRestore(), 600);
        }));
      } catch (ex) { box.innerHTML = `<p class="error">${esc(ex.message)}</p>`; }
    };

    document.querySelectorAll("[data-restore]").forEach((b) => bindAction(b, async () => {
      if (!confirm("Start restore on the server?")) return;
      try {
        const r = await api("/api/backup/restore", { method: "POST", body: JSON.stringify({ snapshot_id: b.dataset.restore }) });
        document.querySelector("#bakok").textContent = r.message || "Restore started.";
        document.querySelector("#bakok").classList.remove("hidden");
        setTimeout(() => renderRestore(), 600);
      } catch (ex) { document.querySelector("#bakerr").textContent = ex.message; }
    }));
  }

  function showRestorePrompt() {
    if (state.restoreGateDone || state.askedRestore || state.me?.kind !== "owner") return;
    state.askedRestore = true;
    const modal = el(`<div class="modal-back" id="restore-modal">
      <div class="modal-card">
        <h3>Do you have a GitHub backup?</h3>
        <p class="muted">If yes, enter your classic PAT and we validate the account. If no, continue to the panel.</p>
        <div id="rm-step1" class="row-actions" style="margin-top:16px">
          <button class="btn primary action" id="rm-yes">Yes, I have a backup</button>
          <button class="btn ghost action" id="rm-no">No</button>
        </div>
        <form id="rm-pat" class="hidden" style="margin-top:14px">
          <div class="field"><label>GitHub PAT (classic · repo scope)</label>
            <input name="token" type="password" required placeholder="ghp_…" autocomplete="off" /></div>
          <p class="error" id="rm-err"></p>
          <div class="row-actions">
            <button class="btn primary action" type="submit">Validate & unlock Restore</button>
            <button class="btn ghost" type="button" id="rm-back">Back</button>
          </div>
        </form>
      </div>
    </div>`);
    document.body.appendChild(modal);
    const done = () => {
      localStorage.setItem("vr_restore_gate", "1");
      state.restoreGateDone = true;
      modal.remove();
    };
    modal.querySelector("#rm-no").onclick = () => { done(); };
    modal.querySelector("#rm-yes").onclick = () => {
      modal.querySelector("#rm-step1").classList.add("hidden");
      modal.querySelector("#rm-pat").classList.remove("hidden");
    };
    modal.querySelector("#rm-back").onclick = () => {
      modal.querySelector("#rm-pat").classList.add("hidden");
      modal.querySelector("#rm-step1").classList.remove("hidden");
    };
    modal.querySelector("#rm-pat").onsubmit = async (e) => {
      e.preventDefault();
      const err = modal.querySelector("#rm-err");
      err.textContent = "";
      try {
        const bk = await api("/api/backup/token", {
          method: "POST",
          body: JSON.stringify({ token: new FormData(e.target).get("token") }),
        });
        state.backupReady = !!bk.configured;
        done();
        setView("restore");
      } catch (ex) {
        err.textContent = ex.message || "Invalid token";
      }
    };
  }

  function parseEnvForm(text) {
    const rows = [];
    String(text || "").split(/\r?\n/).forEach((line) => {
      const t = line.trim();
      if (!t || t.startsWith("#")) return;
      const i = t.indexOf("=");
      if (i < 0) return;
      rows.push({ key: t.slice(0, i).trim(), value: t.slice(i + 1) });
    });
    if (!rows.length) rows.push({ key: "", value: "" });
    return rows;
  }

  function envToText(form) {
    const lines = [];
    form.querySelectorAll(".env-row").forEach((row) => {
      const k = row.querySelector("[name=key]").value.trim();
      const v = row.querySelector("[name=value]").value;
      if (!k) return;
      lines.push(`${k}=${v}`);
    });
    return lines.join("\n") + (lines.length ? "\n" : "");
  }

  async function renderRoom() {
    const gen = state._gen;
    const id = state.roomId || state.me?.room?.id;
    if (!id) { setView("rooms"); return; }
    shell(`<div class="topbar"><div><h2>Project</h2><div class="sub">Loading…</div></div></div>${skel(4)}`, "room");
    let room;
    try { room = await api(`/api/rooms/${id}`); } catch (e) {
      if (!alive("room", gen)) return;
      shell(`<p class="error">${esc(e.message)}</p>`, "room"); return;
    }
    if (!alive("room", gen)) return;
    state.roomId = id;
    const tab = state.roomTab || "overview";
    const projs = room.projects || [];
    const mainProj = projs[0];
    let st = null;
    try { st = await api("/api/storage"); } catch {}

    let body = "";
    if (tab === "overview") {
      const qgb = room.quota_bytes ? gb(room.quota_bytes).toFixed(2) : "";
      body = `
        <div class="grid grid-3">
          <div class="stat"><div class="label">Project disk</div><div class="value">${fmtBytes(room.usage_bytes)}</div><div class="muted">quota ${room.quota_bytes ? fmtBytes(room.quota_bytes) : "not set"}</div></div>
          <div class="stat"><div class="label">Status</div><div class="value" style="font-size:1.1rem">${esc((projs[0] && projs[0].status) || "—")}</div></div>
          <div class="stat"><div class="label">Password</div><div class="value" style="font-size:1rem">${room.password
            ? `<span class="mono copyable" data-copy="${esc(room.password)}">${esc(room.password)}</span>`
            : `<span class="muted">hidden until unlock</span>`}</div></div>
        </div>
        <div class="grid" style="margin-top:12px">
          <div class="stat"><div class="label">CPU</div><div class="value" data-metric="cpu">—</div><div class="bar"><span data-bar="cpu"></span></div></div>
          <div class="stat"><div class="label">Memory</div><div class="value" data-metric="mem">—</div><div class="bar"><span data-bar="mem"></span></div></div>
          <div class="stat"><div class="label">Disk</div><div class="value" data-metric="disk">—</div><div class="bar"><span data-bar="disk"></span></div></div>
          <div class="stat"><div class="label">Load</div><div class="value" data-metric="load">—</div></div>
        </div>
        ${(function () {
          const links = (mainProj?.links || []).slice();
          if (!links.length) return "";
          return `
        <div class="panel"><h3>Project links</h3>
          <div class="link-list" id="link-list">
            ${links.map((l) => `<a class="link-chip link-chip-blue" href="${esc(l.url)}" target="_blank" rel="noopener">
                  <span class="link-label">${esc(l.label)}</span>
                  <span class="link-url">${esc(l.url)}</span>
                </a>`).join("")}
          </div>
        </div>`;
        })()}
        ${(function () {
          if (!mainProj) return `<div class="panel"><h3>Port & domain</h3><p class="muted">Deploy a container first.</p></div>`;
          const hasPort = Number(mainProj.host_port) > 0;
          const hasDomain = !!(mainProj.domain && String(mainProj.domain).trim());
          const show = hasPort || hasDomain || state.showNetPanel;
          if (!show) {
            return `<div class="panel" style="padding:12px 16px">
              <button class="btn sm action" type="button" id="show-net-panel">Set port / domain</button>
            </div>`;
          }
          return `
        <div class="panel"><h3>Port & domain</h3>
          <form id="port-form" class="form-grid">
            <div class="field"><label>Host port</label><input name="host_port" type="number" min="0" max="65535" value="${mainProj.host_port || ""}" placeholder="e.g. 8000" /></div>
            <div class="field" style="display:flex;align-items:end;gap:8px">
              <button class="btn primary sm action" type="submit">Save port</button>
              <button class="btn sm danger action" type="button" id="clear-port">Disable port</button>
            </div>
          </form>
          <form id="domain-form" class="form-grid" style="margin-top:12px">
            <div class="field full"><label>Domain</label><input name="domain" value="${esc(mainProj.domain || "")}" placeholder="app.example.com" /></div>
            <div class="field"><label>SSL status</label><input readonly value="${esc(mainProj.ssl_status || "—")}" /></div>
            <div class="field" style="display:flex;align-items:end;gap:8px;flex-wrap:wrap">
              <button class="btn primary sm action" type="submit">Bind domain</button>
              <button class="btn sm danger action" type="button" id="clear-domain">Disable domain</button>
            </div>
          </form>
          <p class="error" id="linkerr"></p>
        </div>`;
        })()}
        <div class="panel"><h3>Containers</h3>
          <table class="table"><thead><tr><th>Name</th><th>Status</th><th>Port</th><th>Image</th></tr></thead>
          <tbody id="containers-live">${projs.map((p) => `<tr data-cid="${esc(p.id)}"><td>${esc(p.name)}</td><td><span class="badge ${p.status === "running" ? "ok" : "stop"}" data-cstatus>${esc(p.status)}</span></td><td data-cport>${p.host_port || "—"}</td><td class="mono muted">${esc(p.image)}</td></tr>`).join("") || `<tr><td colspan="4" class="muted">No containers</td></tr>`}</tbody></table>
        </div>
        <div class="panel"><h3>Name, quota & password</h3>
          ${(() => {
            const cur = Number(qgb) || 0.1;
            const maxQ = Math.max(cur, Number(room.quota_max_gb || room.quota_available_gb || st?.quota_available_gb || cur));
            return `<form id="rname" class="row-actions">
              <input name="name" value="${esc(room.name)}" minlength="2" maxlength="40" pattern="[A-Za-z0-9_-]{2,40}" placeholder="Room name" style="flex:1;background:#101010;border:1px solid var(--line);border-radius:8px;padding:10px" />
              <button class="btn sm action" type="submit">Save name</button>
            </form>
            <form id="quota" class="form-grid" style="margin-top:14px">
              <div class="field full">${quotaSliderHTML({ name: "quota_gb", maxGB: maxQ, valueGB: cur, required: true })}</div>
              <p class="muted" style="margin:0">Free to allocate up to <strong>${maxQ.toFixed(1)} GB</strong> (server free disk).</p>
              <div class="full"><button class="btn primary sm action" type="submit">Save quota</button></div>
            </form>`;
          })()}
          <form id="rpass" class="row-actions" style="margin-top:14px"><input name="password" type="text" minlength="6" placeholder="New room password" style="flex:1;background:#101010;border:1px solid var(--line);border-radius:8px;padding:10px" />
          <button class="btn sm action" type="submit">Change room password</button></form>
          <p class="error" id="rerr"></p>
        </div>`;
    } else if (tab === "files") {
      let listing = { entries: [], path: state.filePath || "." };
      try { listing = await api(`/api/rooms/${id}/files?path=${encodeURIComponent(state.filePath || ".")}`); } catch {}
      if (listing.binary) {
        body = `<div class="panel"><div class="head-row"><h3>${esc(listing.path)}</h3>
          <div class="row-actions"><button class="btn sm action" id="backfiles">Back</button></div></div>
          <p class="muted">${esc(listing.note || "Binary file — cannot open in text editor.")}</p>
          <p class="mono muted">size ${fmtBytes(listing.size || 0)}</p></div>`;
      } else if (listing.content != null) {
        body = `<div class="panel"><div class="head-row"><h3>${esc(listing.path)}</h3>
          <div class="row-actions"><button class="btn sm action" id="backfiles">Back</button><button class="btn sm primary action" id="savefile">Save</button><button class="btn sm danger action" id="delfile">Delete</button></div></div>
          <textarea class="file-editor" id="fedit">${esc(listing.content)}</textarea></div>`;
      } else {
        body = `<div class="panel"><div class="head-row"><h3>Files · ${esc(listing.path || ".")}</h3>
          <button class="btn sm action" id="updir">Up</button></div>
          <ul class="file-list">${(listing.entries || []).map((e) => `<li><a href="#" data-path="${esc((listing.path === "." ? "" : listing.path + "/") + e.name)}">${e.dir ? "📁" : "📄"} ${esc(e.name)}</a><span class="muted">${e.dir ? "dir" : fmtBytes(e.size)}</span></li>`).join("") || "<li class='muted'>Empty</li>"}</ul></div>`;
      }
    } else if (tab === "logs") {
      let lg = { log: "" };
      try { lg = await api(`/api/rooms/${id}/logs`); } catch {}
      body = `<div class="panel"><div class="head-row"><h3>Room logs</h3>
        <div class="row-actions"><button class="btn sm action" id="copylog">Copy</button><button class="btn sm action" id="reflog">Refresh</button></div></div>
        <p class="muted" style="margin:0 0 8px;font-size:0.78rem">Fixed height · scroll · newest at bottom · live</p>
        <div class="logs-viewer room-logs-viewer">
          <div class="logs-body" id="rlog">${formatLogHTML(lg.log || "(empty)")}</div>
        </div></div>`;
    } else if (tab === "env") {
      let envMeta = { content: "" };
      try { if (mainProj) envMeta = await api(`/api/projects/${mainProj.id}/env`); } catch {}
      const rows = parseEnvForm(envMeta.content || "");
      body = `<div class="panel">
        <div class="head-row"><h3>.env editor</h3>
          <div class="row-actions">
            <button class="btn sm action" id="env-add">Add row</button>
            <button class="btn sm action" id="env-mode">Raw editor</button>
            <button class="btn sm primary action" id="env-save">Save</button>
          </div>
        </div>
        <p class="muted mono" style="margin-bottom:10px">${esc(envMeta.path || "")}</p>
        <form id="env-form" class="env-form">
          ${rows.map((r) => `<div class="env-row">
            <input name="key" placeholder="KEY" value="${esc(r.key)}" />
            <input name="value" placeholder="value" value="${esc(r.value)}" />
            <button type="button" class="btn sm danger env-del" title="Remove">×</button>
          </div>`).join("")}
        </form>
        <textarea class="file-editor hidden" id="env-raw">${esc(envMeta.content || "")}</textarea>
        <p class="error" id="enverr"></p>
        <p class="ok-text hidden" id="envok">Saved.</p>
      </div>`;
    } else if (tab === "terminal") {
      const lines = (state.termLines || []).join("") || "Linux shell for this room. Commands run in room files or project container.\n";
      body = `<div class="panel"><h3>Terminal</h3>
        <div class="term">
          <div class="term-out" id="tout">${esc(lines)}</div>
          <form class="term-in" id="tform"><span class="prompt">root@${esc(room.name)}:~#</span>
          <input id="tcmd" autocomplete="off" spellcheck="false" /></form>
        </div>
        <div class="cmd-hints">
          ${TERM_HINTS.map((h) => `<button type="button" class="hint" data-hint="${esc(h.cmd)}" title="${esc(h.tip)}">${esc(h.cmd)}</button>`).join("")}
        </div>
      </div>`;
    }

    shell(`
      <div class="topbar"><div>
        <h2>${esc(room.name)}</h2>
        <div class="sub">project control</div>
      </div>
        <div class="row-actions">
          ${powerToggleHTML(id, (projs[0] && projs[0].status) || "stopped")}
          <button class="btn sm danger action" data-act="delete">Delete</button>
          <button class="btn sm primary action" id="backrooms">Projects</button>
        </div>
      </div>
      <div class="tabs">
        <button data-tab="overview" class="${tab === "overview" ? "active" : ""}">Overview</button>
        <button data-tab="files" class="${tab === "files" ? "active" : ""}">Files</button>
        <button data-tab="logs" class="${tab === "logs" ? "active" : ""}">Logs</button>
        <button data-tab="env" class="${tab === "env" ? "active" : ""}">Env</button>
        <button data-tab="terminal" class="${tab === "terminal" ? "active" : ""}">Terminal</button>
      </div>
      ${body}`, "room");

    document.querySelectorAll("[data-tab]").forEach((b) => b.onclick = () => setView("room", { roomTab: b.dataset.tab, filePath: b.dataset.tab === "files" ? (state.filePath || ".") : state.filePath }));
    bindAction(document.querySelector("#backrooms"), async () => {
      if (state.me?.kind !== "owner") {
        await unlockOwner();
      }
      state.showNetPanel = false;
      setView("rooms");
    });
    bindPowerToggles();
    document.querySelectorAll("[data-act]").forEach((b) => bindAction(b, async () => {
      if (b.dataset.act === "delete") {
        if (!confirm("Delete project/room?")) return;
        await api(`/api/rooms/${id}`, { method: "DELETE" });
        await unlockOwner();
        setView("rooms");
      }
    }));
    bindCopyables();

    if (tab === "overview") {
      bindQuotaSliders();
      document.querySelector("#rname")?.addEventListener("submit", async (e) => {
        e.preventDefault();
        try {
          await api(`/api/rooms/${id}/name`, { method: "POST", body: JSON.stringify({ name: new FormData(e.target).get("name") }) });
          render();
        } catch (ex) { document.querySelector("#rerr").textContent = ex.message; }
      });
      document.querySelector("#quota")?.addEventListener("submit", async (e) => {
        e.preventDefault();
        try {
          await api(`/api/rooms/${id}/quota`, { method: "POST", body: JSON.stringify({ quota_gb: Number(new FormData(e.target).get("quota_gb") || 0) }) });
          render();
        } catch (ex) { document.querySelector("#rerr").textContent = ex.message; }
      });
      document.querySelector("#rpass")?.addEventListener("submit", async (e) => {
        e.preventDefault();
        try {
          await api(`/api/rooms/${id}/password`, { method: "POST", body: JSON.stringify({ password: new FormData(e.target).get("password") }) });
          render();
        } catch (ex) { document.querySelector("#rerr").textContent = ex.message; }
      });
      const linkErr = document.querySelector("#linkerr");
      document.querySelector("#show-net-panel")?.addEventListener("click", () => {
        state.showNetPanel = true;
        render();
      });
      document.querySelector("#port-form")?.addEventListener("submit", async (e) => {
        e.preventDefault();
        try {
          await api(`/api/projects/${mainProj.id}/port`, { method: "POST", body: JSON.stringify({ host_port: Number(new FormData(e.target).get("host_port") || 0) }) });
          state.showNetPanel = true;
          render();
        } catch (ex) { if (linkErr) linkErr.textContent = ex.message; }
      });
      bindAction(document.querySelector("#clear-port"), async () => {
        await api(`/api/projects/${mainProj.id}/port`, { method: "POST", body: JSON.stringify({ clear: true }) });
        state.showNetPanel = false;
        render();
      });
      document.querySelector("#domain-form")?.addEventListener("submit", async (e) => {
        e.preventDefault();
        try {
          await api(`/api/projects/${mainProj.id}/domain`, { method: "POST", body: JSON.stringify({ domain: new FormData(e.target).get("domain"), enabled: true }) });
          state.showNetPanel = true;
          render();
        } catch (ex) { if (linkErr) linkErr.textContent = ex.message; }
      });
      bindAction(document.querySelector("#clear-domain"), async () => {
        await api(`/api/projects/${mainProj.id}/domain`, { method: "POST", body: JSON.stringify({ domain: "", enabled: false }) });
        state.showNetPanel = false;
        render();
      });
      updateMetricsDOM();
      if (state._containersPoll) clearInterval(state._containersPoll);
      state._containersPoll = setInterval(async () => {
        if (state.view !== "room" || (state.roomTab || "overview") !== "overview") return;
        try {
          const fresh = await api(`/api/rooms/${id}`);
          const list = fresh.projects || [];
          const tbody = document.querySelector("#containers-live");
          if (!tbody) return;
          list.forEach((p) => {
            const row = tbody.querySelector(`[data-cid="${p.id}"]`);
            if (!row) return;
            const badge = row.querySelector("[data-cstatus]");
            if (badge) {
              badge.textContent = p.status || "—";
              badge.className = `badge ${p.status === "running" ? "ok" : "stop"}`;
            }
            const port = row.querySelector("[data-cport]");
            if (port) port.textContent = p.host_port || "—";
          });
          const topStatus = document.querySelector(".grid-3 .stat .value");
          // also refresh overview status card if present
          const statusVals = document.querySelectorAll(".grid-3 .stat");
          if (statusVals[1]) {
            const v = statusVals[1].querySelector(".value");
            if (v && list[0]) v.textContent = list[0].status || "—";
          }
          const links = list[0]?.links || [];
          const linkBox = document.querySelector("#link-list");
          if (linkBox && links.length) {
            linkBox.innerHTML = links.map((l) => `<a class="link-chip link-chip-blue" href="${esc(l.url)}" target="_blank" rel="noopener">
              <span class="link-label">${esc(l.label)}</span>
              <span class="link-url">${esc(l.url)}</span>
            </a>`).join("");
          }
        } catch {}
      }, 4000);
    } else if (state._containersPoll) {
      clearInterval(state._containersPoll);
      state._containersPoll = null;
    }
    if (tab === "files") {
      document.querySelectorAll("[data-path]").forEach((a) => a.onclick = (e) => {
        e.preventDefault();
        setView("room", { roomTab: "files", filePath: a.dataset.path });
      });
      document.querySelector("#updir")?.addEventListener("click", () => {
        const p = state.filePath || ".";
        const parts = p.split("/").filter(Boolean);
        parts.pop();
        setView("room", { roomTab: "files", filePath: parts.join("/") || "." });
      });
      document.querySelector("#backfiles")?.addEventListener("click", () => {
        const p = state.filePath || ".";
        const parts = p.split("/").filter(Boolean);
        parts.pop();
        setView("room", { roomTab: "files", filePath: parts.join("/") || "." });
      });
      bindAction(document.querySelector("#savefile"), async () => {
        await api(`/api/rooms/${id}/files?path=${encodeURIComponent(state.filePath)}`, {
          method: "PUT", body: JSON.stringify({ content: document.querySelector("#fedit").value }),
        });
      });
      bindAction(document.querySelector("#delfile"), async () => {
        if (!confirm("Delete file?")) return;
        await api(`/api/rooms/${id}/files?path=${encodeURIComponent(state.filePath)}`, { method: "DELETE" });
        const parts = (state.filePath || ".").split("/").filter(Boolean); parts.pop();
        setView("room", { roomTab: "files", filePath: parts.join("/") || "." });
      });
    }
    if (tab === "logs") {
      const rlog = document.querySelector("#rlog");
      pinLogBottom(rlog);
      state._logPinBottom = true;
      rlog?.addEventListener("scroll", () => {
        const el = document.querySelector("#rlog");
        if (!el) return;
        state._logPinBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
      });
      document.querySelector("#copylog")?.addEventListener("click", () => copyText(document.querySelector("#rlog")?.innerText || ""));
      document.querySelector("#reflog")?.addEventListener("click", () => {
        state._logPinBottom = true;
        render();
      });
      startLogLive(async () => {
        if (state.view !== "room" || (state.roomTab || "overview") !== "logs") {
          stopLogLive();
          return;
        }
        try {
          const lg = await api(`/api/rooms/${id}/logs`);
          const el = document.querySelector("#rlog");
          if (!el) return;
          const html = formatLogHTML(lg.log || "(empty)");
          if (el.innerHTML !== html) {
            el.innerHTML = html;
            if (state._logPinBottom !== false) pinLogBottom(el);
          }
        } catch {}
      });
    }
    if (tab === "env" && mainProj) {
      const form = document.querySelector("#env-form");
      const raw = document.querySelector("#env-raw");
      let rawMode = false;
      const bindDel = () => form.querySelectorAll(".env-del").forEach((b) => b.onclick = () => {
        b.closest(".env-row")?.remove();
        if (!form.querySelector(".env-row")) {
          form.insertAdjacentHTML("beforeend", `<div class="env-row"><input name="key" placeholder="KEY" /><input name="value" placeholder="value" /><button type="button" class="btn sm danger env-del">×</button></div>`);
          bindDel();
        }
      });
      bindDel();
      document.querySelector("#env-add").onclick = () => {
        form.insertAdjacentHTML("beforeend", `<div class="env-row"><input name="key" placeholder="KEY" /><input name="value" placeholder="value" /><button type="button" class="btn sm danger env-del">×</button></div>`);
        bindDel();
      };
      document.querySelector("#env-mode").onclick = () => {
        rawMode = !rawMode;
        if (rawMode) {
          raw.value = envToText(form);
          form.classList.add("hidden");
          raw.classList.remove("hidden");
          document.querySelector("#env-mode").textContent = "Form editor";
        } else {
          const rows = parseEnvForm(raw.value);
          form.innerHTML = rows.map((r) => `<div class="env-row">
            <input name="key" placeholder="KEY" value="${esc(r.key)}" />
            <input name="value" placeholder="value" value="${esc(r.value)}" />
            <button type="button" class="btn sm danger env-del">×</button></div>`).join("");
          form.classList.remove("hidden");
          raw.classList.add("hidden");
          document.querySelector("#env-mode").textContent = "Raw editor";
          bindDel();
        }
      };
      bindAction(document.querySelector("#env-save"), async () => {
        const content = rawMode ? raw.value : envToText(form);
        await api(`/api/projects/${mainProj.id}/env`, { method: "PUT", body: JSON.stringify({ content }) });
        document.querySelector("#envok").classList.remove("hidden");
        setTimeout(() => document.querySelector("#envok")?.classList.add("hidden"), 1500);
      });
    }
    if (tab === "terminal") {
      const tout = document.querySelector("#tout");
      document.querySelectorAll("[data-hint]").forEach((b) => b.onclick = () => {
        document.querySelector("#tcmd").value = b.dataset.hint;
        document.querySelector("#tcmd").focus();
      });
      document.querySelector("#tform").onsubmit = async (e) => {
        e.preventDefault();
        const cmd = document.querySelector("#tcmd").value;
        document.querySelector("#tcmd").value = "";
        state.termLines = state.termLines || [];
        state.termLines.push(`root@${room.name}:~# ${cmd}\n`);
        tout.textContent += `root@${room.name}:~# ${cmd}\n`;
        try {
          const res = await api(`/api/rooms/${id}/exec`, {
            method: "POST",
            body: JSON.stringify({ command: cmd, project_id: mainProj?.id || "" }),
          });
          const where = res.where ? `[${res.where}] ` : "";
          const out = where + (res.output || "") + (res.error ? `\n${res.error}` : "");
          state.termLines.push(out + (out.endsWith("\n") ? "" : "\n"));
          tout.textContent += out + (out.endsWith("\n") ? "" : "\n");
          tout.scrollTop = tout.scrollHeight;
        } catch (ex) {
          state.termLines.push(ex.message + "\n");
          tout.textContent += ex.message + "\n";
        }
      };
      document.querySelector("#tcmd")?.focus();
    }
  }

  async function unlockOwner() {
    if (state.me?.kind === "owner") return;
    // Sticky admin session — no password re-prompt while already unlocked this visit.
    try {
      await api("/api/auth/admin", { method: "POST", body: JSON.stringify({}) });
      await loadMe();
      if (state.me?.kind === "owner") return;
    } catch {}
    const password = prompt("Admin password");
    if (password == null || password === "") throw new Error("Admin password required");
    await api("/api/auth/owner", { method: "POST", body: JSON.stringify({ password }) });
    await loadMe();
    if (!state.me || state.me.kind !== "owner") throw new Error("Admin unlock failed");
  }

  async function render() {
    if (!state.gated) { renderGate(); return; }
    if (!state.me) await loadMe();
    if (!state.me) { renderUnlock(); return; }
    if (state.me.kind === "owner") {
      if (state.view === "rooms") return renderRooms();
      if (state.view === "deploy") return renderDeploy();
      if (state.view === "logs") return renderLogs();
      if (state.view === "docs") return renderDocs();
      if (state.view === "settings") return renderSettings();
      if (state.view === "restore") return renderRestore();
      if (state.view === "room") return renderRoom();
      await renderServer();
      showRestorePrompt();
      return;
    }
    return renderRoom();
  }

  const boot = parsePath(location.pathname);
  if (boot) Object.assign(state, boot);
  window.addEventListener("popstate", () => {
    const parsed = parsePath(location.pathname) || { view: "server" };
    state.view = parsed.view;
    if (parsed.roomId) state.roomId = parsed.roomId;
    if (parsed.roomTab) state.roomTab = parsed.roomTab;
    state._gen = (state._gen || 0) + 1;
    stopLogLive();
    markNav(state.view);
    render();
  });

  (async () => {
    await loadGate();
    if (state.gated) {
      await loadMe();
      if (state.me) {
        connectWS();
        try {
          const bk = await api("/api/backup/status");
          state.backupReady = !!bk.configured;
        } catch {}
      }
    }
    await render();
  })();
})();

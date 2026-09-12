(() => {
  window.addEventListener("error", (e) => {
    if (e && e.message && e.message.includes("startTime")) {
      e.preventDefault();
      e.stopImmediatePropagation();
    }
  });

  const app = document.getElementById("app");
  const BOT_KEY = "vr_bot_token";
  const state = {
    me: null,
    gated: false,
    view: "server",
    roomId: null,
    roomLogCtr: null,
    ctrId: null,
    ctrTab: "files",
    metrics: null,
    ws: null,
    sidebarOpen: false,
    gateStep: "token",
    expiresIn: 1200,
    filePath: ".",
    fileContent: "",
    termLines: [],
    termHostLines: [],
    shellBuilt: false,
    busy: false,
    showNetPanel: false,
    _gen: 0,
    cache: {},
    _pageCache: {},
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
    if (!(val > 0)) val = 0.1;
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

  async function animateQuotaSlider(root, gbVal) {
    const scope = root || document;
    const wrap = scope.matches && scope.matches("[data-quota-wrap]")
      ? scope
      : scope.querySelector("[data-quota-wrap]");
    if (!wrap) return 0;
    const range = wrap.querySelector("[data-quota-range]");
    const input = wrap.querySelector("[data-quota-input]");
    const label = wrap.querySelector("[data-quota-label]");
    if (!range) return 0;
    const max = Number(range.max) || 0.1;
    const min = Number(range.min) || 0.1;
    let target = Number(gbVal);
    if (!(target > 0)) return Number(range.value) || 0;
    if (target > max) target = max;
    if (target < min) target = min;
    const start = Number(range.value) || min;
    const steps = 20;
    for (let i = 1; i <= steps; i++) {
      const cur = start + (target - start) * (i / steps);
      const shown = Math.round(cur * 10) / 10;
      range.value = String(shown);
      if (input) input.value = String(shown);
      if (label) label.textContent = `${shown.toFixed(1)} GB`;
      await new Promise((r) => setTimeout(r, 22));
    }
    const final = Math.round(target * 10) / 10;
    range.value = String(final);
    if (input) input.value = String(final);
    if (label) label.textContent = `${final.toFixed(1)} GB`;
    wrap.classList.add("quota-moved");
    setTimeout(() => wrap.classList.remove("quota-moved"), 800);
    return final;
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
  const fmtWhen = (iso) => {
    if (iso == null || iso === "" || iso === "never") return iso || "—";
    let s = String(iso).trim();
    if (/^\d{4}-\d{2}-\d{2} /.test(s) && !/[zZ]|[+-]\d{2}:\d{2}$/.test(s)) {
      s = s.replace(" ", "T") + "Z";
    }
    const d = new Date(s);
    if (Number.isNaN(d.getTime())) return String(iso || "—");
    return d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
  };
  const ctrLabel = (c) => (c && (c.label || c.service || c.name)) || "container";
  const logContainerKey = (c) => (c && (c.id || c.docker_id || c.name)) || "";
  function pickLogContainer(list) {
    const rows = Array.isArray(list) ? list : [];
    if (!rows.length) return "";
    const cur = state.roomLogCtr;
    if (cur && rows.some((c) => c.id === cur || c.docker_id === cur || c.name === cur)) return cur;
    const run = rows.find((c) => c.status === "running");
    return logContainerKey(run || rows[0]);
  }
  const ctrNum = (c) => "#" + String((c && c.ordinal) || 1).padStart(3, "0");
  const shortDocker = (id) => {
    const s = String(id || "").replace(/^sha256:/, "");
    return s.length > 12 ? s.slice(0, 12) : (s || "—");
  };
  function formatJobLogLine(line) {
    const t = String(line || "");
    const m = t.match(/^(\d{4}-\d{2}-\d{2}T\S+)\s+(.*)$/);
    if (m) return `${fmtWhen(m[1])}  ${m[2]}`;
    return t;
  }
  function updateHistoryHTML(items) {
    const list = (Array.isArray(items) ? items : []).slice(0, 5);
    const n = list.length ? Number(list[0].n) || list.length : 0;
    const rows = list.length
      ? list.map((u) => `<div class="upd-row">
          <span class="upd-n">#${esc(u.n)}</span>
          <span class="upd-at">${esc(fmtWhen(u.at))}</span>
          ${u.image ? `<span class="upd-img mono muted">${esc(u.image)}</span>` : ""}
        </div>`).join("")
      : `<p class="muted" style="margin:0">No updates yet. This fills when you upload a tar or GitHub deploys.</p>`;
    const sub = n ? `Updated ${n} time${n === 1 ? "" : "s"}` : "Waiting for the first image update";
    return `<div class="panel" id="update-history"><h3>Update history</h3>
      <p class="muted" style="margin:0 0 10px">${sub}</p>
      <div class="upd-list">${rows}</div>
    </div>`;
  }

  function brandMarkHTML() {
    return `<img class="brand-mark-img" src="/favicon.svg" width="36" height="36" alt="" />`;
  }
  function refreshIconHTML() {
    return `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21.5 2v6h-6M2.5 22v-6h6M2 11.5a10 10 0 0 1 18.8-4.3M22 12.5a10 10 0 0 1-18.8 4.2"/></svg>`;
  }
  function projectIconHTML(extra = "") {
    return `<span class="proj-ico ${extra}" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3 20 7.5v9L12 21 4 16.5v-9L12 3z"/><path d="M12 12 20 7.5M12 12v9M12 12 4 7.5"/></svg></span>`;
  }
  // Small expressive SVG icon set (icon-only buttons + copy/eye affordances).
  function ico(name, size = 15) {
    const p = `width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"`;
    const paths = {
      copy: `<svg ${p}><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>`,
      check: `<svg ${p}><polyline points="20 6 9 17 4 12"/></svg>`,
      eye: `<svg ${p}><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>`,
      eyeOff: `<svg ${p}><path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/></svg>`,
      play: `<svg ${p}><polygon points="6 3 20 12 6 21 6 3"/></svg>`,
      dl: `<svg ${p}><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>`,
      trash: `<svg ${p}><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>`,
      upload: `<svg ${p}><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>`,
      file: `<svg ${p}><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/></svg>`,
      link: `<svg ${p}><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/></svg>`,
      box: `<svg ${p}><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><polyline points="3.27 6.96 12 12.01 20.73 6.96"/><line x1="12" y1="22.08" x2="12" y2="12"/></svg>`,
      user: `<svg ${p}><path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>`,
      key: `<svg ${p}><path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4"/></svg>`,
      shield: `<svg ${p}><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/></svg>`,
      db: `<svg ${p}><ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3"/><path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5"/></svg>`,
      bell: `<svg ${p}><path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9"/><path d="M13.73 21a2 2 0 0 1-3.46 0"/></svg>`,
    };
    return paths[name] || "";
  }
  function copyIcoBtn(copyText_val, title = "Copy", cls = "") {
    return `<button type="button" class="icon-btn copy-ico ${cls}" data-copy="${esc(copyText_val)}" title="${esc(title)}" aria-label="${esc(title)}">${ico("copy")}</button>`;
  }
  function brandWordmarkHTML(roleId, roleText) {
    return `${brandMarkHTML()}<div class="brand-text"><strong class="brand-name">VPS Manager</strong><span class="brand-role" id="${roleId}">${esc(roleText)}</span></div>`;
  }
  function navIco(k) {
    const p = 'viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round"';
    const icons = {
      server: `<svg ${p}><rect x="3" y="4" width="18" height="6" rx="1.5"/><rect x="3" y="14" width="18" height="6" rx="1.5"/><path d="M7 7h.01M7 17h.01"/></svg>`,
      rooms: `<svg ${p}><rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/></svg>`,
      terminal: `<svg ${p}><rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 9l3 3-3 3M13 15h4"/></svg>`,
      logs: `<svg ${p}><path d="M8 6h13M8 12h13M8 18h13"/><path d="M3 6h.01M3 12h.01M3 18h.01"/></svg>`,
      ssh: `<svg ${p}><rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 9l3 3-3 3M13 15h4"/></svg>`,
      backup: `<svg ${p}><path d="M12 3v12m0 0l-4-4m4 4l4-4"/><path d="M4 17v2a2 2 0 002 2h12a2 2 0 002-2v-2"/></svg>`,
      docs: `<svg ${p}><path d="M7 3h8l5 5v13a1 1 0 0 1-1 1H7a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z"/><path d="M15 3v5h5M9 13h6M9 17h4"/></svg>`,
      settings: `<svg ${p}><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9c.3.7.9 1.2 1.6 1.4H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/></svg>`,
      agent: `<svg ${p}><circle cx="12" cy="8" r="3.5"/><path d="M5 19a7 7 0 0 1 14 0"/><path d="M12 11.5v3M10 16h4"/></svg>`,
      tokens: `<svg ${p}><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>`,
      room: `<svg ${p}><path d="M4 10.5L12 4l8 6.5V20a1 1 0 0 1-1 1h-5v-6H10v6H5a1 1 0 0 1-1-1z"/></svg>`,
    };
    return icons[k] || "";
  }

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

  async function api(path, opts = {}) {
    const tries = 3;
    let last;
    for (let i = 0; i < tries; i++) {
      try {
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
          if (!res.ok) {
            const err = new Error(data.error || "Request failed");
            if ((res.status >= 500 || res.status === 429) && i < tries - 1) {
              last = err;
              await sleep(400 * (i + 1));
              continue;
            }
            throw err;
          }
          return data;
        }
        const text = await res.text();
        if (!res.ok) throw new Error(text || "Request failed");
        return text;
      } catch (ex) {
        last = ex;
        if (ex?.name === "AbortError" || /abort/i.test(String(ex.message || ""))) throw ex;
        const msg = String(ex.message || ex);
        const retryable = /Failed to fetch|NetworkError|network|timeout|502|503|504|load failed/i.test(msg);
        if (!retryable || i === tries - 1) throw ex;
        await sleep(400 * (i + 1));
      }
    }
    throw last || new Error("Request failed");
  }

  // Streaming POST helper (plain-text log endpoints like deploy/update).
  async function streamFetch(url, opts, onChunk) {
    const res = await fetch(url, { credentials: "same-origin", ...opts });
    const reader = res.body && res.body.getReader ? res.body.getReader() : null;
    const dec = new TextDecoder();
    let text = "";
    if (reader) {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        const chunk = dec.decode(value, { stream: true });
        text += chunk;
        if (onChunk) { try { onChunk(chunk); } catch {} }
      }
      text += dec.decode();
      if (onChunk && !text) { try { onChunk(""); } catch {} }
    } else {
      text = await res.text();
      if (onChunk) { try { onChunk(text); } catch {} }
    }
    if (!res.ok) {
      const msg = (text.trim().split("\n").pop() || ("Request failed (" + res.status + ")")).replace(/^error:\s*/i, "");
      throw new Error(msg);
    }
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

  function confirmAction({ title, body, ok = "Confirm", danger = false } = {}) {
    return new Promise((resolve) => {
      const modal = el(`<div class="modal-back logout-modal show">
        <div class="modal-card logout-card">
          <h3>${esc(title || "Confirm")}</h3>
          <p class="muted">${esc(body || "")}</p>
          <div class="row-actions" style="margin-top:16px">
            <button class="btn ghost" type="button" data-no>Cancel</button>
            <button class="btn ${danger ? "danger" : "primary"} action" type="button" data-yes>${esc(ok)}</button>
          </div>
        </div>
      </div>`);
      const done = (v) => {
        modal.classList.remove("show");
        modal.classList.add("hide");
        setTimeout(() => modal.remove(), 220);
        resolve(v);
      };
      modal.querySelector("[data-no]").onclick = () => done(false);
      modal.querySelector("[data-yes]").onclick = () => done(true);
      modal.addEventListener("click", (e) => { if (e.target === modal) done(false); });
      document.body.appendChild(modal);
    });
  }

  async function copyText(text) {
    text = String(text ?? "");
    if (!text) { toast("Nothing to copy"); return false; }
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        await navigator.clipboard.writeText(text);
        toast("Copied");
        return true;
      }
    } catch {}
    let ok = false;
    try {
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.style.cssText = "position:fixed;left:-9999px;top:0;opacity:0";
      document.body.appendChild(ta);
      ta.focus();
      ta.select();
      ta.setSelectionRange(0, ta.value.length);
      ok = document.execCommand("copy");
      ta.remove();
    } catch {}
    toast(ok ? "Copied" : "Copy failed — long-press to copy manually");
    return ok;
  }

  function bindCopyables(root = document) {
    // [data-copy] is handled by one delegated listener (registered at boot),
    // so dynamically re-rendered buttons always work. Only [data-copy-btn] here.
    (root || document).querySelectorAll("[data-copy-btn]").forEach((n) => {
      if (n.dataset.copyBound) return;
      n.dataset.copyBound = "1";
      n.onclick = (e) => {
        e.preventDefault();
        e.stopPropagation();
        copyText(n.getAttribute("data-copy-btn") || "");
      };
    });
  }

  function registerCopyDelegation() {
    if (window.__vpsCopyDelegated) return;
    window.__vpsCopyDelegated = true;
    document.addEventListener("click", (e) => {
      const n = e.target && e.target.closest ? e.target.closest("[data-copy]") : null;
      if (!n) return;
      e.preventDefault();
      e.stopPropagation();
      copyText(n.getAttribute("data-copy") || n.textContent);
      // Animated feedback for icon copy buttons: morph to a check briefly.
      if (n.classList && n.classList.contains("copy-ico") && !n.dataset.animating) {
        n.dataset.animating = "1";
        const orig = n.innerHTML;
        n.classList.add("copied");
        n.innerHTML = ico("check");
        setTimeout(() => {
          n.innerHTML = orig;
          n.classList.remove("copied");
          delete n.dataset.animating;
        }, 1100);
      }
    }, true);
  }

  function navHighlight(view) {
    if (state.me?.kind === "owner" && view === "room") return "rooms";
    return view || state.view;
  }

  // Breadcrumb: always shows the real canonical VPS path with a dedicated copy icon button.
  function canonicalVPSPath(crumbs) {
    const v = state.view;
    const tab = state.roomTab || "overview";
    const rid = state.roomId || state.me?.room?.id || "";
    const kind = state.roomKindHint || (state.cache?.rooms?.find((r) => r.id === rid)?.kind) || "single";
    const dir = kind === "multi" ? "multi" : "single";
    const projDir = kind === "multi" ? "stack" : "project";
    if (v === "backup") return "/vps-manager/backup";
    if (v === "logs") return "/vps-manager/data/logs";
    if (v === "settings") return "/vps-manager/data";
    if (v === "ssh" || v === "terminal") return "/root";
    if (v === "server" || v === "rooms") return "/vps-manager";
    if (v === "room" && rid) {
      const p = `/vps-manager/${dir}/${rid}`;
      if (tab === "overview") return p;
      if (tab === "images") return `${p}/images`;
      if (tab === "volumes" || tab === "volume") return `${p}/volumes`;
      if (tab === "env") return `${p}/${projDir}/.env`;
      if (tab === "logs") return `/vps-manager/data/logs/rooms/${rid}.log`;
      if (tab === "files" || tab === "container") {
        const sub = state.filePath && state.filePath !== "." && state.filePath !== "/" ? "/" + state.filePath.replace(/^\/+/, "") : "";
        return `${p}/${projDir}${sub}`;
      }
      return `${p}/${tab}`;
    }
    if (crumbs && crumbs.length) {
      const last = crumbs[crumbs.length - 1];
      if (last.path && last.path.startsWith("/")) return last.path;
    }
    return "/vps-manager";
  }

  function breadcrumbHTML(crumbs, connect, extra) {
    const realPath = canonicalVPSPath(crumbs);
    return `<div class="breadcrumb-bar breadcrumb-sticky">
      <div class="crumb-main">
        <span class="crumb-icon">📁</span>
        <span class="crumb-path mono" title="${esc(realPath)}">${esc(realPath)}</span>
        <button type="button" class="crumb-copy-btn" data-copy="${esc(realPath)}" title="Copy full real path">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect>
            <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path>
          </svg>
        </button>
      </div>
      ${extra || ""}
    </div>`;
  }
  async function fetchVPSPaths(roomId) {
    try {
      const q = roomId ? `?room_id=${encodeURIComponent(roomId)}` : "";
      return await api(`/api/vps/paths${q}`);
    } catch { return null; }
  }
  function roomSectionLabel() {
    const tab = state.roomTab || "overview";
    const names = { overview: "overview", container: "container", images: "images", volumes: "volumes", volume: "volume", env: "secrets", logs: "logs", files: "files", manage: "manage" };
    let s = names[tab] || tab;
    if (tab === "container" && state.ctrId) s += " " + String(state.ctrId).slice(0, 8);
    if (tab === "volume" && state.volId) s += " " + String(state.volId).slice(0, 8);
    if ((tab === "files" || tab === "volume") && state.filePath && state.filePath !== "." && state.filePath !== "/") s += " " + state.filePath;
    return s;
  }
  function viewSectionLabel(view) {
    const v = view || state.view;
    if (v === "room") return roomSectionLabel();
    return { server: "server", rooms: "rooms", logs: "logs", agent: "x5coder-agent", backup: "backup", settings: "settings" }[v] || v || "";
  }
  function paintBreadcrumb(el, info, fallbackCrumbs, section) {
    if (!el) return;
    const base = (info?.crumbs || fallbackCrumbs || [{ label: "/vps-manager" }]).slice();
    el.innerHTML = breadcrumbHTML(base, info?.connect || state.cache?.sshConnect || "");
    bindCopyables(el);
  }

  function viewPath(view, extra = {}) {
    const roomId = extra.roomId || state.roomId;
    const tab = extra.roomTab != null ? extra.roomTab : (state.roomTab || "overview");
    switch (view) {
      case "rooms": return "/projects";
      case "room":
        if (roomId) {
          const ctr = extra.ctrId || state.ctrId;
          if ((tab === "container" || extra.roomTab === "container") && ctr) {
            const st = extra.ctrTab || state.ctrTab || "files";
            const extraTab = st && st !== "files" ? `/${encodeURIComponent(st)}` : "";
            return `/projects/${encodeURIComponent(roomId)}/c/${encodeURIComponent(ctr)}${extraTab}`;
          }
          const vol = extra.volId || state.volId;
          if ((tab === "volume" || extra.roomTab === "volume") && vol) {
            return `/projects/${encodeURIComponent(roomId)}/v/${encodeURIComponent(vol)}`;
          }
          const t = tab && tab !== "overview" ? `/${encodeURIComponent(tab)}` : "";
          return `/projects/${encodeURIComponent(roomId)}${t}`;
        }
        return "/projects";
      case "server": return "/server";
      case "terminal": return "/terminal";
      case "agent": return "/x5coder-agent";
      case "backup": return "/backup";
      case "logs": return "/logs";
      case "settings": return "/settings";
      default: return "/server";
    }
  }

  function parsePath(path) {
    let p = path || "/";
    try { p = decodeURIComponent(p); } catch {}
    p = p.replace(/\/+$/, "") || "/";
    const cpath = p.match(/^\/projects\/([^/]+)\/c\/([^/]+)(?:\/([^/]+))?$/);
    if (cpath) return { view: "room", roomId: cpath[1], roomTab: "container", ctrId: cpath[2], ctrTab: cpath[3] || "files" };
    const vpath = p.match(/^\/projects\/([^/]+)\/v\/([^/]+)$/);
    if (vpath) return { view: "room", roomId: vpath[1], roomTab: "volume", volId: vpath[2] };
    const room = p.match(/^\/projects\/([^/]+)(?:\/([^/]+))?$/);
    if (room) return { view: "room", roomId: room[1], roomTab: room[2] || "overview" };
    if (p === "/projects") return { view: "rooms" };
    if (p === "/docs" || p === "/guide" || p === "/tokens" || p === "/api") return { view: "server" };
    if (p === "/ssh") return { view: "server" };
    if (p === "/deploy") return { view: "rooms" };
    if (p === "/terminal") return { view: "terminal" };
    if (p === "/x5coder-agent") return { view: "agent" };
    if (p === "/backup") return { view: "backup" };
    if (p === "/logs") return { view: "logs" };
    if (p === "/settings") return { view: "settings" };
    if (p === "/room") return { view: "room" };
    if (p === "/server" || p === "/" || p === "/owner" || p === "/app") return { view: "server" };
    return null;
  }

  function saveChatDraft() {
  }

  function setView(view, extra = {}) {
    saveChatDraft();
    if (view === "deploy") view = "rooms";
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
    const go = () => {
      try { el.scrollTop = el.scrollHeight + 1000; } catch {}
    };
    go();
    requestAnimationFrame(() => {
      go();
      requestAnimationFrame(go);
    });
    setTimeout(go, 60);
    setTimeout(go, 200);
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
      if (next === "pause") {
        const ok = await confirmAction({
          title: "Pause this project?",
          body: "The container will stop. You can resume it later. The room is not deleted.",
          ok: "Pause",
          danger: true,
        });
        if (!ok) return;
      }
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
        <p class="auth-kicker">${brandMarkHTML()}VPS Manager</p>
        <h1>Enter the code</h1>
        <p class="lead">One-time code sent to Telegram · valid 20 minutes · any device · previous codes void</p>
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
      <p class="auth-kicker">${brandMarkHTML()}VPS Manager</p>
      <h1>Unlock the panel</h1>
      <p class="lead">Paste your Telegram bot token. A one-time code is sent to Telegram — works on any device, one use. Sending again voids the previous code.</p>
      <form id="f">
        <div class="field"><label>Telegram bot token</label>
          <input name="bot_token" type="password" required value="${esc(saved)}" autocomplete="off" placeholder="123456:ABC…" /></div>
        <p class="error" id="err"></p>
        <button class="btn primary" style="width:100%" type="submit">Send password</button>
      </form>
      <p class="auth-pass-toggle"><button type="button" class="auth-pass-link" id="show-pass">ادخل باسورد</button></p>
      <form id="pass-form" class="auth-pass-box" hidden>
        <div class="field"><label>Temporary password</label>
          <input name="code" inputmode="numeric" autocomplete="one-time-code" placeholder="6-digit password" /></div>
        <p class="error" id="pass-err"></p>
        <button class="btn primary" style="width:100%" type="submit">Continue</button>
      </form>
    </div></div>`);
    app.appendChild(card);
    card.querySelector("#show-pass").onclick = () => {
      const box = card.querySelector("#pass-form");
      box.hidden = !box.hidden;
      if (!box.hidden) box.querySelector("input[name=code]")?.focus();
    };
    card.querySelector("#pass-form").onsubmit = async (e) => {
      e.preventDefault();
      const token = String(card.querySelector("#f input[name=bot_token]")?.value || saved || "").trim();
      const code = String(new FormData(e.target).get("code") || "").trim();
      try {
        if (token) localStorage.setItem(BOT_KEY, token);
        await api("/api/gate/verify", { method: "POST", body: JSON.stringify({ bot_token: token, code }) });
        state.gated = true; state.me = null; render();
      } catch (ex) { card.querySelector("#pass-err").textContent = ex.message || "Server stopped"; }
    };
    card.querySelector("#f").onsubmit = async (e) => {
      e.preventDefault();
      const token = String(new FormData(e.target).get("bot_token") || "").trim();
      try {
        localStorage.setItem(BOT_KEY, token);
        const res = await api("/api/gate/challenge", { method: "POST", body: JSON.stringify({ bot_token: token }) });
        state.expiresIn = res.expires_in || 1200; state.gateStep = "code"; renderGate();
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
      <p class="auth-kicker">${brandMarkHTML()}VPS Manager</p>
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
            ${brandWordmarkHTML("mobile-role", isOwner ? "Admin" : `Room · ${state.me?.room?.name || ""}`)}
          </div>
        </header>
        <div class="backdrop" id="backdrop"></div>
        <aside class="sidebar" id="sidebar">
          <div class="sidebar-head">
            <div class="brand">
              ${brandWordmarkHTML("brand-role", isOwner ? "Admin" : `Room · ${state.me?.room?.name || ""}`)}
            </div>
            <nav class="nav" id="nav"></nav>
          </div>
          <div class="sidebar-foot">
            <div class="meta">Panel :9090 · <span id="panel-version">…</span></div>
            <button class="btn ghost sidebar-out" id="logout">Sign out</button>
          </div>
        </aside>
        <main class="main" id="main"></main>
      </div>`);
      app.appendChild(root);
      state.shellBuilt = true;

      root.querySelector("#logout").onclick = () => {
        const modal = el(`<div class="modal-back logout-modal show" id="logout-modal">
          <div class="modal-card logout-card">
            <h3>Sign out?</h3>
            <p class="muted">You will need to unlock the panel again.</p>
            <div class="row-actions" style="margin-top:16px">
              <button class="btn ghost" type="button" id="logout-cancel">Cancel</button>
              <button class="btn danger action" type="button" id="logout-yes">Sign out</button>
            </div>
          </div>
        </div>`);
        document.body.appendChild(modal);
        const close = () => {
          modal.classList.remove("show");
          modal.classList.add("hide");
          setTimeout(() => modal.remove(), 220);
        };
        modal.querySelector("#logout-cancel").onclick = close;
        modal.addEventListener("click", (e) => { if (e.target === modal) close(); });
        modal.querySelector("#logout-yes").onclick = async () => {
          close();
          await api("/api/auth/logout", { method: "POST" });
          state.me = null; state.gated = false; state.gateStep = "token"; state.shellBuilt = false;
          state.sidebarOpen = false;
          document.body.classList.remove("nav-open");
          if (state.ws) { try { state.ws.close(); } catch {} state.ws = null; }
          render();
        };
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

    const goHome = () => {
      if (state.me?.kind === "owner") setView("server");
      else {
        state.roomId = state.me?.room?.id || state.roomId;
        setView("room", { roomTab: "overview" });
      }
    };
    const brandHome = root.querySelector(".brand");
    if (brandHome && !brandHome.dataset.homeBound) {
      brandHome.dataset.homeBound = "1";
      brandHome.setAttribute("role", "button");
      brandHome.tabIndex = 0;
      brandHome.title = "Home";
      brandHome.addEventListener("click", goHome);
      brandHome.addEventListener("keydown", (e) => {
        if (e.key === "Enter" || e.key === " ") { e.preventDefault(); goHome(); }
      });
    }
    const mobHome = root.querySelector(".mobile-bar-brand");
    if (mobHome && !mobHome.dataset.homeBound) {
      mobHome.dataset.homeBound = "1";
      mobHome.setAttribute("role", "button");
      mobHome.tabIndex = 0;
      mobHome.addEventListener("click", goHome);
    }

    const nav = root.querySelector("#nav");
    const items = isOwner
      ? [["server", "Server"], ["rooms", "Rooms"], ["terminal", "Root Shell"], ["logs", "Logs"], ["agent", "x5coder-agent"], ["backup", "Backup"], ["settings", "Settings"]]
      : [["room", "Room"], ["rooms", "All rooms"]];
    const highlight = navHighlight(active || state.view);
    nav.innerHTML = items.map(([k, label]) => {
      return `<button data-go="${k}" class="${highlight === k ? "active" : ""}"><span class="nav-ico">${navIco(k)}</span><span>${label}</span></button>`;
    }).join("");
    nav.querySelectorAll("[data-go]").forEach((b) => {
      b.onclick = async () => {
        state.sidebarOpen = false;
        root.querySelector("#sidebar")?.classList.remove("open");
        root.querySelector("#backdrop")?.classList.remove("show");
        document.body.classList.remove("nav-open");
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
    refreshPanelVersion();
    return root.querySelector("#main");
  }

  function shell(content, active) {
    saveChatDraft();
    const main = ensureShell(active);
    const prev = state._viewKey;
    const next = active || state.view || "";
    // Auto breadcrumb on top of EVERY page: shows where you are in the VPS.
    if (typeof content === "string" && !content.includes('id="crumb"') && !content.includes("gate-wrap")) {
      content = `<div id="crumb"></div>` + content;
    }
    main.innerHTML = content;
    // Paint VPS path + SSH connect chip (async, non-blocking).
    try {
      const el = main.querySelector("#crumb");
      if (el) {
        const roomId = state.roomId || state.me?.room?.id || "";
        const fallback = roomId
          ? [{ label: "/vps-manager" }, { label: state.roomKindHint || "single" }, { label: String(roomId).slice(0, 8) }]
          : [{ label: "/vps-manager" }];
        paintBreadcrumb(el, state.cache?.vpsPaths, fallback);
        const rid = (next === "room" || next === "rooms") ? (roomId || "") : "";
        fetchVPSPaths(next === "room" ? roomId : "").then((info) => {
          if (!info) return;
          state.cache = state.cache || {};
          state.cache.vpsPaths = info;
          if (info.connect) state.cache.sshConnect = info.connect;
          const cur = main.querySelector("#crumb");
          if (cur) paintBreadcrumb(cur, info);
        }).catch(() => {});
      }
    } catch {}
    main.classList.remove("view-enter", "view-enter-fast");
    if (prev && prev !== next) {
      void main.offsetWidth;
      main.classList.add("view-enter");
    } else {
      main.classList.add("view-enter-fast");
    }
    state._viewKey = next;
  }

  async function renderServer(forceRefresh = false) {
    const gen = state._gen;
    state._pageCache = state._pageCache || {};

    const paint = (host, ready = false) => {
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
      <div id="crumb"></div>
      <div class="topbar">
        <div>
          <h2>Server</h2>
          <div class="sub">${esc(host.hostname || "")} · ${esc(host.os || "Linux")} · Docker ${dockerLabel}</div>
        </div>
        <div class="actions">
          <button class="btn icon-btn sm action" id="server-refresh-btn" title="Refresh" aria-label="Refresh">${refreshIconHTML()}</button>
        </div>
      </div>
      <div class="grid stats-grid" data-cores="${cores}">
        <div class="stat">
          <div class="label">VPS disk</div>
          <div class="value" data-metric="disk-total">${fmtDisk(host.disk_total)}</div>
          <div class="muted" data-metric="disk-sub">${fmtDisk(host.disk_used)} used · ${fmtDisk(host.disk_free)} free (whole disk, not quota)</div>
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
            fact("Disk", `${fmtDisk(host.disk_total)} · ${fmtDisk(host.disk_used)} used · ${fmtDisk(host.disk_free)} free (whole VPS)`),
          ])}
          ${group("Platform", [
            fact("Docker", dockerLabel),
            fact("Panel", "VPS Manager · :9090"),
          ])}
        </div>
      </div>
      ${roomsDiskTable(state.cache.rooms, host)}`, "server");
      bindCopyables();
      paintBreadcrumb(document.querySelector("#crumb"), state.cache.vpsPaths, [{ label: "/vps-manager" }]);
      fetchVPSPaths("").then((info) => { if (info) { state.cache.vpsPaths = info; paintBreadcrumb(document.querySelector("#crumb"), info); } });
      document.querySelector("#server-refresh-btn")?.addEventListener("click", () => renderServer(true));
      updateMetricsDOM();
    };

    const cachedHost = state._pageCache["serverHost"] || state.cache.host;
    if (cachedHost && !forceRefresh) {
      paint(cachedHost, true);
      Promise.all([
        api("/api/host"),
        api("/api/rooms").catch(() => []),
      ]).then(([host, rooms]) => {
        if (!alive("server", gen)) return;
        state._pageCache["serverHost"] = host;
        state.cache.host = host;
        state._pageCache["rooms"] = rooms;
        state.cache.rooms = rooms;
        paint(host, true);
      }).catch(() => {});
      return;
    }

    if (!cachedHost) paint(null, false);
    try {
      const [host, rooms] = await Promise.all([
        api("/api/host"),
        api("/api/rooms").catch(() => []),
      ]);
      if (!alive("server", gen)) return;
      state._pageCache["serverHost"] = host;
      state.cache.host = host;
      state._pageCache["rooms"] = rooms;
      state.cache.rooms = rooms;
      paint(host, true);
    } catch (e) {
      if (!alive("server", gen)) return;
      if (!state.cache.host) shell(`<p class="error">${esc(e.message)}</p>`, "server");
      else paint(state.cache.host, true);
    }
  }

  function roomsDiskTable(rooms, host) {
    const list = Array.isArray(rooms) ? rooms : [];
    if (!list.length) return "";
    let dataSum = 0, imgSum = 0, quotaSum = 0;
    const rows = list.map((r) => {
      const usedN = Number(r.usage_bytes) || 0;
      const quotaN = Number(r.quota_bytes) || 0;
      const imgN = Number(r.image_bytes) || 0;
      const volN = Number(r.volume_bytes) || 0;
      dataSum += usedN;
      imgSum += imgN;
      quotaSum += quotaN;
      const over = quotaN > 0 && usedN > quotaN;
      const fill = quotaN > 0 ? Math.min(100, Math.round((usedN / quotaN) * 100)) : 0;
      const heat = fill >= 90 ? "hot" : fill >= 70 ? "warm" : "";
      const kind = r.kind === "multi" ? "multi" : "single";
      return `<div class="disk-row${over ? " hot" : ""}">
        <div class="disk-head">
          <strong>${esc(r.name)}</strong>
          <span class="badge ${over ? "warn" : "ok"}">${esc(kind)}</span>
        </div>
        <div class="disk-line muted">Data ${esc(fmtBytes(usedN))}${volN ? ` · vol ${esc(fmtBytes(volN))}` : ""}${over ? ` <strong>over quota</strong>` : ""}</div>
        <div class="disk-body">
          <span class="muted">quota</span>
          <div class="room-disk ${heat}"><div class="room-disk-bar"><i style="width:${fill}%"></i></div></div>
          <strong class="mono">${esc(fmtBytes(usedN))}${quotaN ? ` / ${esc(fmtBytes(quotaN))}` : ""}</strong>
        </div>
        <div class="disk-line muted">own image ${esc(fmtBytes(imgN))}</div>
      </div>`;
    }).join("");
    const imgHost = Number(host?.docker_images_bytes) || 0;
    const cacheB = Number(host?.docker_buildcache_bytes) || 0;
    const rwB = Number(host?.docker_containers_bytes) || 0;
    const dataHost = Number(host?.project_data_bytes) || dataSum;
    return `<div class="panel" style="margin-top:12px">
      <h3>How each project uses disk</h3>
      <p class="muted"><strong>Quota = Data only</strong> (files + volumes + container writable layer). Docker images are shared on the host and are not compared to the cap. Two Supabase rooms share one image set on disk — do not add the Image column.</p>
      <div class="disk-list">
        ${rows}
        <div class="disk-row total">
          <div class="disk-head"><strong>All rooms</strong></div>
          <div class="disk-line muted">Data ${esc(fmtBytes(dataSum))} · quota ${esc(fmtBytes(quotaSum))} reserved · own images ${esc(fmtBytes(imgSum))} listed (not unique)</div>
        </div>
      </div>
      ${renderDiskBreakdownHTML(host, dataSum)}
    </div>`;
  }

  function renderDiskBreakdownHTML(host, dataSum) {
    const total = Number(host?.disk_total) || 1;
    const used = Number(host?.disk_used) || 0;
    const free = Number(host?.disk_free) || Math.max(0, total - used);
    const pct = Math.min(100, Math.max(0, Math.round((used / total) * 100)));

    const imgB = Number(host?.docker_images_bytes) || 0;
    const cacheB = Number(host?.docker_buildcache_bytes) || 0;
    const rwB = Number(host?.docker_containers_bytes) || 0;
    const dataB = Number(host?.project_data_bytes) || dataSum || 0;
    const osB = Math.max(0, used - (imgB + cacheB + rwB + dataB));

    const pImg = Math.max(0.5, ((imgB / total) * 100)).toFixed(1);
    const pData = Math.max(0.5, ((dataB / total) * 100)).toFixed(1);
    const pCache = ((cacheB / total) * 100).toFixed(1);
    const pRw = ((rwB / total) * 100).toFixed(1);
    const pOs = Math.max(0, pct - (parseFloat(pImg) + parseFloat(pData) + parseFloat(pCache) + parseFloat(pRw))).toFixed(1);

    return `<div class="disk-breakdown-card">
      <div class="disk-breakdown-header">
        <div class="disk-title-group">
          <h4>VPS Storage & Host Disk Breakdown</h4>
          <span>Total host disk utilization divided by layer</span>
        </div>
        <div class="disk-headline-stat">
          <span class="disk-pct">${pct}%</span>
          <span class="disk-sub mono">${fmtBytes(used)} / ${fmtBytes(total)}</span>
        </div>
      </div>

      <div class="disk-segmented-track" title="${pct}% used of ${fmtBytes(total)}">
        <div class="disk-seg seg-img" style="width:${pImg}%" title="Docker Images: ${fmtBytes(imgB)}"></div>
        <div class="disk-seg seg-data" style="width:${pData}%" title="Project Data: ${fmtBytes(dataB)}"></div>
        <div class="disk-seg seg-cache" style="width:${pCache}%" title="Build Cache: ${fmtBytes(cacheB)}"></div>
        <div class="disk-seg seg-rw" style="width:${pRw}%" title="Container RW: ${fmtBytes(rwB)}"></div>
        <div class="disk-seg seg-os" style="width:${pOs}%" title="OS & System: ${fmtBytes(osB)}"></div>
      </div>

      <div class="disk-metrics-grid">
        <div class="disk-metric-item">
          <div class="disk-metric-dot dot-img"></div>
          <div class="disk-metric-info">
            <span class="disk-metric-label">Docker Images</span>
            <strong class="disk-metric-val mono">${fmtBytes(imgB)}</strong>
          </div>
        </div>
        <div class="disk-metric-item">
          <div class="disk-metric-dot dot-data"></div>
          <div class="disk-metric-info">
            <span class="disk-metric-label">Project Data</span>
            <strong class="disk-metric-val mono">${fmtBytes(dataB)}</strong>
          </div>
        </div>
        <div class="disk-metric-item">
          <div class="disk-metric-dot dot-cache"></div>
          <div class="disk-metric-info">
            <span class="disk-metric-label">Build Cache</span>
            <strong class="disk-metric-val mono">${fmtBytes(cacheB)}</strong>
          </div>
        </div>
        <div class="disk-metric-item">
          <div class="disk-metric-dot dot-rw"></div>
          <div class="disk-metric-info">
            <span class="disk-metric-label">Container RW</span>
            <strong class="disk-metric-val mono">${fmtBytes(rwB)}</strong>
          </div>
        </div>
        <div class="disk-metric-item">
          <div class="disk-metric-dot dot-os"></div>
          <div class="disk-metric-info">
            <span class="disk-metric-label">OS & Rest</span>
            <strong class="disk-metric-val mono">${fmtBytes(osB)}</strong>
          </div>
        </div>
        <div class="disk-metric-item highlight">
          <div class="disk-metric-dot dot-free"></div>
          <div class="disk-metric-info">
            <span class="disk-metric-label">Free Space</span>
            <strong class="disk-metric-val mono ok-text">${fmtBytes(free)}</strong>
          </div>
        </div>
      </div>
    </div>`;
  }

  async function openAddProjectModal() {
    let maxGB = 1;
    try {
      const st = await api("/api/storage");
      maxGB = Math.max(0.1, Number(st.quota_available_gb || 0));
    } catch {}
    const modal = el(`<div class="modal-back add-proj-modal">
      <div class="modal-card add-proj-card">
        <div class="modal-head"><h3>New project room</h3><button type="button" class="icon-btn modal-x" data-x title="Close" aria-label="Close">✕</button></div>
        <p class="muted">Creates an empty isolated room. Open it, then clone from GitHub or upload files. The agent uses the room terminal.</p>
        <form id="add-proj-form" class="form-grid" style="margin-top:14px">
          <div class="field full"><label>Name</label><input name="name" required minlength="2" maxlength="40" placeholder="my-app" autocomplete="off" /></div>
          <div class="field full"><label>Password</label><input name="password" type="password" minlength="6" placeholder="at least 6 characters (optional)" /></div>
          <div class="field full"><label>Kind</label>
            <select name="kind">
              <option value="single">Single container</option>
              <option value="multi">Multi (compose / several containers)</option>
            </select>
          </div>
          <div class="field full">${quotaSliderHTML({ name: "quota_gb", id: "add-quota", maxGB, valueGB: Math.min(1, maxGB), required: true })}</div>
          <p class="error" id="add-proj-err"></p>
          <div class="row-actions full" style="margin-top:8px">
            <button class="btn ghost" type="button" data-no>Cancel</button>
            <button class="btn primary action" type="submit">Create room</button>
          </div>
        </form>
      </div>
    </div>`);
    const close = () => {
      modal.classList.remove("show");
      modal.classList.add("hide");
      setTimeout(() => modal.remove(), 280);
    };
    modal.querySelector("[data-no]").onclick = close;
    modal.querySelector("[data-x]").onclick = close;
    modal.addEventListener("click", (e) => { if (e.target === modal) close(); });
    document.body.appendChild(modal);
    requestAnimationFrame(() => requestAnimationFrame(() => modal.classList.add("show")));
    bindQuotaSliders(modal);
    modal.querySelector("[name=name]")?.focus();
    modal.querySelector("#add-proj-form").onsubmit = async (e) => {
      e.preventDefault();
      const err = modal.querySelector("#add-proj-err");
      if (err) err.textContent = "";
      const fd = new FormData(e.target);
      const name = String(fd.get("name") || "").trim();
      const password = String(fd.get("password") || "").trim();
      const kind = String(fd.get("kind") || "single");
      const quota_gb = Number(fd.get("quota_gb") || 0);
      try {
        const res = await api("/api/rooms", {
          method: "POST",
          body: JSON.stringify({ name, password, kind, quota_gb }),
        });
        close();
        const id = res.room?.id;
        if (id) setView("room", { roomId: id, roomTab: "overview" });
        else setView("rooms");
      } catch (ex) {
        if (err) err.textContent = ex.message || "Could not create room";
      }
    };
  }

  async function renderRooms(forceRefresh = false) {
    const gen = state._gen;
    state._pageCache = state._pageCache || {};

    const paint = (rooms) => {
      if (!rooms) {
        shell(`<div class="topbar"><div><h2>Rooms</h2><div class="sub">Each room is one project (id + password)</div></div>
          <div class="row-actions">
            <button class="btn primary action" id="go-add-proj">Add project</button>
          </div></div>${skel(4)}`, "rooms");
        document.querySelector("#go-add-proj")?.addEventListener("click", () => openAddProjectModal());
        return;
      }
      const cards = (rooms || []).map((r) => {
        const st = r.status === "running" ? "ok" : r.status === "empty" ? "empty" : r.status === "restarting" ? "warn" : r.status === "error" ? "stop" : r.status === "stopped" ? "stop" : "miss";
        const usedN = Number(r.usage_bytes) || 0;
        const quotaN = Number(r.quota_bytes) || 0;
        const imgN = Number(r.image_bytes) || 0;
        const volN = Number(r.volume_bytes) || 0;
        const footN = Number(r.footprint_bytes) || (usedN + imgN);
        const used = quotaN ? `${fmtBytes(usedN)} / ${fmtBytes(quotaN)}` : fmtBytes(usedN);
        const fill = quotaN > 0 ? Math.min(100, Math.round((usedN / quotaN) * 100)) : 0;
        const heat = fill >= 90 ? "hot" : fill >= 70 ? "warm" : "";
        const pw = r.password || "";
        const nC = Number(r.containers) || 0;
        const port = Number(r.host_port) || 0;

        return `<article class="proj-card" data-room="${esc(r.id)}">
          <div class="proj-card-top">
            <div class="proj-card-title-row">
              <div class="proj-card-identity">
                ${projectIconHTML(st)}
                <div>
                  <h4 class="proj-card-name">${esc(r.name)}</h4>
                  <div class="proj-card-badges">
                    <span class="badge ${st}" data-badge>${esc(r.status)}</span>
                    <span class="badge ${r.kind === "multi" ? "info" : "muted-badge"}">${esc(r.kind || "single")}</span>
                    ${nC > 1 ? `<span class="badge warn">${nC} containers</span>` : ""}
                    ${port ? `<span class="badge port-badge mono">:${port}</span>` : ""}
                  </div>
                </div>
              </div>
              <div class="proj-card-id-wrap">
                <button type="button" class="proj-id-badge copyable" data-copy="${esc(r.id)}" title="Copy Room ID">
                  <span class="id-tag">ID</span>
                  <span class="id-val mono">${esc(r.id)}</span>
                  <svg class="id-copy-icon" width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="9" y="9" width="13" height="13" rx="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>
                </button>
              </div>
            </div>
          </div>

          <div class="proj-card-stats">
            <div class="proj-stat-cell">
              <span class="stat-cell-label">Quota Usage</span>
              <strong class="stat-cell-val mono">${esc(used)}</strong>
              <div class="room-disk ${heat}"><div class="room-disk-bar"><i style="width:${fill}%"></i></div></div>
            </div>
            <div class="proj-stat-cell">
              <span class="stat-cell-label">Total Size</span>
              <strong class="stat-cell-val mono">${esc(fmtBytes(footN))}</strong>
              <span class="stat-cell-sub muted">${esc(`image ${fmtBytes(imgN)} · data ${fmtBytes(usedN)}${volN ? ` · vol ${fmtBytes(volN)}` : ""}`)}</span>
            </div>
            <div class="proj-stat-cell">
              <span class="stat-cell-label">Room Password</span>
              <div class="secret-row">
                <span class="secret-mask" aria-hidden="true">••••••••</span>
                <button type="button" class="btn sm action" data-copy="${esc(pw)}" ${pw ? "" : "disabled"}>Copy</button>
              </div>
            </div>
          </div>

          <div class="proj-card-actions">
            <button class="btn sm primary action btn-open" data-enter="${r.id}" data-name="${esc(r.name)}" data-pass="${esc(pw)}">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M15 3h6v6M10 14L21 3M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/></svg>
              Open Room
            </button>
            ${powerToggleHTML(r.id, r.status)}
            <button class="btn sm danger action btn-del" data-del="${r.id}" title="Delete room">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
              Delete
            </button>
          </div>
        </article>`;
      }).join("") || `<div class="empty-projects">
          ${brandMarkHTML()}
          <h3>No rooms</h3>
          <p class="muted">Create an empty room, then clone from GitHub or upload files in the room terminal.</p>
          <button class="btn primary action" id="empty-deploy">Add project</button>
        </div>`;

      shell(`
        <div class="topbar"><div>
          <h2>Rooms</h2>
          <div class="sub">${(rooms || []).length ? `${rooms.length} active on this VPS` : "Nothing deployed yet"}</div>
        </div>
        <div class="row-actions">
          <button class="btn icon-btn sm action" id="refresh-rooms" title="Refresh" aria-label="Refresh">${refreshIconHTML()}</button>
          <button class="btn primary action" id="go-add-proj">Add project</button>
        </div>
        </div>
        <div class="proj-list">${cards}</div>`, "rooms");

      document.querySelector("#refresh-rooms")?.addEventListener("click", () => renderRooms(true));
      document.querySelector("#go-add-proj")?.addEventListener("click", () => openAddProjectModal());
      document.querySelector("#empty-deploy")?.addEventListener("click", () => openAddProjectModal());
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
        if (!await confirmAction({
          title: "Delete this room?",
          body: "This removes the room and its data. This cannot be undone.",
          ok: "Delete",
          danger: true,
        })) return;
        await api(`/api/rooms/${b.dataset.del}`, { method: "DELETE" });
        delete state._pageCache["rooms"];
        b.closest(".proj-card")?.remove();
      }));
    };

    const cached = state._pageCache["rooms"] || state.cache?.rooms;
    if (cached && !forceRefresh) {
      paint(cached);
      api("/api/rooms").then((rooms) => {
        if (!alive("rooms", gen)) return;
        state._pageCache["rooms"] = rooms;
        state.cache.rooms = rooms;
        paint(rooms);
      }).catch(() => {});
      return;
    }

    paint(null);

    try {
      const rooms = await api("/api/rooms");
      if (!alive("rooms", gen)) return;
      state._pageCache["rooms"] = rooms;
      state.cache.rooms = rooms;
      paint(rooms);
    } catch (e) {
      if (!alive("rooms", gen)) return;
      if (!state._pageCache["rooms"] && !state.cache?.rooms) {
        shell(`<p class="error">${esc(e.message)}</p>`, "rooms");
      }
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

  async function renderLogs(forceRefresh = false) {
    const gen = state._gen;
    state._pageCache = state._pageCache || {};

    const paintLogs = (data) => {
      if (!alive("logs", gen)) return;
      const raw = data.log || "";
      const lineCount = raw.split("\n").filter(Boolean).length;

      shell(`
        <div class="topbar"><div>
          <h2>Logs</h2>
          <div class="sub">Newest at bottom · auto-scroll · live refresh</div>
        </div>
          <div class="row-actions">
            <button class="btn icon-btn sm action" id="refresh" title="Refresh" aria-label="Refresh">${refreshIconHTML()}</button>
            <button class="btn sm action" id="copy">Copy</button>
            <button class="btn sm danger action" id="clear">Clear this</button>
            <button class="btn sm danger action" id="clear-all">Delete all logs</button>
          </div>
        </div>
        <div class="logs-viewer room-logs-viewer" style="margin-top:10px">
          <div class="logs-toolbar">
            <div class="logs-meta" id="log-meta">Unified VPS Logs (Panel · API · Deploy · Host) · ${lineCount} lines · live</div>
            <span class="muted mono" style="font-size:0.72rem" id="log-ts">${new Date().toISOString()}</span>
          </div>
          <div class="logs-body" id="logbox">${formatLogHTML(raw)}</div>
        </div>`, "logs");

      const box = document.querySelector("#logbox");
      pinLogBottom(box);

      const refreshLive = async () => {
        if (state.view !== "logs") { stopLogLive(); return; }
        try {
          const d = await api(`/api/host/logs?kind=all`);
          state._pageCache["logs"] = d;
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
          if (meta) meta.textContent = `Unified VPS Logs (Panel · API · Deploy · Host) · ${text.split("\n").filter(Boolean).length} lines · live`;
          if (ts) ts.textContent = new Date().toISOString();
        } catch {}
      };

      box?.addEventListener("scroll", () => {
        const el = document.querySelector("#logbox");
        if (!el) return;
        state._logPinBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
      });
      state._logPinBottom = true;
      startLogLive(refreshLive);

      document.querySelector("#refresh").onclick = () => { state._logPinBottom = true; renderLogs(true); };
      document.querySelector("#copy").onclick = () => copyText(document.querySelector("#logbox").innerText);
      bindAction(document.querySelector("#clear"), async () => {
        await api(`/api/host/logs/clear?all=1`, { method: "POST" });
        toast("Logs cleared");
        state._logPinBottom = true;
        delete state._pageCache["logs"];
        await renderLogs(true);
      });
      bindAction(document.querySelector("#clear-all"), async () => {
        if (!await confirmAction({
          title: "Delete all VPS logs?",
          body: "Deletes all unified logs from disk (/vps-manager/data/logs/). Running containers will keep producing new logs.",
          ok: "Delete all logs",
          danger: true,
        })) return;
        await api(`/api/host/logs/delete-all`, { method: "POST" });
        toast("All logs deleted");
        state._logPinBottom = true;
        delete state._pageCache["logs"];
        await renderLogs(true);
      });
    };

    const cached = state._pageCache["logs"];
    if (cached && !forceRefresh) {
      paintLogs(cached);
      return;
    }

    shell(`<div class="topbar"><div><h2>Logs</h2><div class="sub">Newest at bottom · auto-scroll · live refresh</div></div></div>${skel(2)}`, "logs");
    let data = { log: "" };
    try { data = await api(`/api/host/logs?kind=all`); } catch (e) {
      if (!alive("logs", gen)) return;
      shell(`<p class="error">${esc(e.message)}</p>`, "logs"); return;
    }
    if (!alive("logs", gen)) return;
    state._pageCache["logs"] = data;
    paintLogs(data);
  }

  function formatLogHTML(text) {
    if (!text) return '<div class="muted">(no logs)</div>';
    return String(text).split("\n").map((line) => {
      const t = esc(line);
      const content = t || "&nbsp;";
      if (/error|fail|denied|panic/i.test(line)) return `<div class="log-line-err">${content}</div>`;
      if (/^=== /.test(line)) return `<div class="log-line-sec">${content}</div>`;
      if (/ok|success|started|listening|cleared/i.test(line)) return `<div class="log-line-ok">${content}</div>`;
      return `<div>${content}</div>`;
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




  function tokenCardHTML(t, opts = {}) {
    const secret = t.secret || opts.secret || "";
    const prompt = t.prompt || opts.prompt || "";
    const apiSheet = t.api || opts.api || "";
    const script = t.script || t.script_single || opts.script || "";
    const scriptMulti = t.script_multi || opts.scriptMulti || "";
    const fresh = opts.fresh ? " tok-fresh" : "";
    const copyVal = secret || "";
    const roomLabel = "all rooms";
    return `<div class="tok-card${fresh}" data-tok-id="${esc(t.id)}">
      <div class="tok-card-top">
        <div>
          <strong>${esc(t.name)}</strong>
          <span class="badge ok">${esc(roomLabel)}</span>
        </div>
        <div class="row-actions">
          <button class="btn sm action" type="button" data-copy-api ${apiSheet ? "" : "disabled"} title="BASE and TOKEN only">Copy API</button>
          <button class="btn sm action" type="button" data-copy-script ${script ? "" : "disabled"} title="GitHub Action — single .tar">Copy single script</button>
          <button class="btn sm action" type="button" data-copy-script-multi ${scriptMulti ? "" : "disabled"} title="GitHub Action — multi .tar.gz">Copy multi script</button>
          <button class="btn sm danger action" data-del-tok="${esc(t.id)}">Revoke</button>
        </div>
      </div>
      <div class="secret-row tok-secret-row">
        <span class="secret-mask">${copyVal ? "••••••••••••••••••••" : (esc(t.token_prefix || "••••") + "…")}</span>
      </div>
      ${apiSheet ? `<textarea class="hidden tok-api" readonly>${esc(apiSheet)}</textarea>` : ""}
      ${script ? `<textarea class="hidden tok-script" readonly>${esc(script)}</textarea>` : ""}
      ${scriptMulti ? `<textarea class="hidden tok-script-multi" readonly>${esc(scriptMulti)}</textarea>` : ""}
      <div class="muted" style="font-size:0.75rem;margin-top:6px">one API · all rooms · set ROOM_ID in the script · created ${esc(t.created_at || "")}${t.last_used_at ? " · last used " + esc(t.last_used_at) : ""}</div>
    </div>`;
  }




  function openCreateTokenModal() { toast('API tokens removed'); return; }

  function showRestorePrompt() { return; /* backup API removed */ }
  function showRestorePrompt_DISABLED() {
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
          <div class="field"><label>GitHub PAT (classic · repo + delete_repo)</label>
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



  function highlightJsonFrag(line) {
    return line.replace(/(&quot;([^&]|&(?!quot;))*?&quot;)(\s*:)?|\b(true|false|null)\b|(-?\d[\d.]*)/g, (m, str, _inner, colon, bool, num) => {
      if (str) {
        if (colon) return `<span class="jk">&quot;${str.slice(6, -6)}&quot;</span><span class="jc">:</span>`;
        return `<span class="js">${str}</span>`;
      }
      if (bool) return `<span class="jb">${bool}</span>`;
      if (num) return `<span class="jn">${num}</span>`;
      return m;
    });
  }
  function highlightAgentJSON(obj) {
    return highlightJsonFrag(esc(JSON.stringify(obj, null, 2)));
  }
  // Colorized HTTP request blocks for Docs (method / url / headers / json).
  function highlightHttpBlock(raw) {
    const html = esc(raw).split("\n").map((line) => {
      let m;
      if ((m = line.match(/^(GET|POST|PUT|PATCH|DELETE)(\s+)(\S.*)$/))) {
        return `<span class="ht-m">${m[1]}</span>${m[2]}<span class="ht-u">${m[3]}</span>`;
      }
      if ((m = line.match(/^([A-Za-z][A-Za-z-]*)(:)(\s*)(.+)$/)) && !/^\s*[{"]/.test(line)) {
        return `<span class="ht-h">${m[1]}</span><span class="ht-c">:</span>${m[3]}<span class="ht-v">${highlightJsonFrag(m[4])}</span>`;
      }
      if (/^\s*[{}[\],]*\s*$/.test(line)) return `<span class="ht-c">${line || " "}</span>`;
      return highlightJsonFrag(line);
    }).join("\n");
    return html.replace(/&lt;([^&\n]*?)&gt;/g, '<span class="ht-p">&lt;$1&gt;</span>');
  }

  async function renderAgent() {
    const gen = state._gen;
    if (!state.agentTab) state.agentTab = "tools";
    shell(`<div class="topbar"><div><h2>x5coder-agent</h2><div class="sub">HTTPS control plane · Bearer token authentication</div></div></div>${skel(3)}`, "agent");
    try {
      const data = await api("/api/agent/tokens");
      if (!alive("agent", gen)) return;
      const tokens = data.tokens || [];
      const tools = (data.tools && data.tools.length ? data.tools : [{ name: "get_vps_overview", description: "Get the overall VPS status including CPU, RAM, disk, network, Docker, storage usage, and resource usage.", input_schema: { type: "object", properties: {}, required: [] } }]);
      const toolCount = tools.length;
      const toolLabel = toolCount === 1 ? "1 controlled VPS tool" : toolCount + " controlled VPS tools";
      const endpoint = data.endpoint || "";
      const tab = state.agentTab || "tools";
      const toolsJSON = highlightAgentJSON(tools);
      const rawToolsJSON = JSON.stringify(tools, null, 2);
      const aiContext = [
        "VPS Manager x5coder-agent API.",
        `Discovery (public, no key): GET ${endpoint} -> { tools: [{ name, description, input_schema }] }.`,
        `Invoke (key required): POST ${endpoint}/<tool_name> with header "Authorization: Bearer <secret>", Content-Type application/json, body = tool input object ({} when no input).`,
        "Responses: 200 = JSON result, 401 = bad/missing token, 404 = unknown tool.",
        `Available tools (${toolCount}): ` + tools.map((t) => {
          const req = ((t.input_schema || {}).required || []).join(",");
          return `${t.name}${req ? ` (required: ${req})` : " (no input)"} - ${t.description || ""}`;
        }).join(" | "),
      ].join("\n");
      const aiContextPreview = aiContext.length > 320 ? aiContext.slice(0, 320) + "…" : aiContext;

      const docsHTML = `
        <div class="panel agent-docs agent-anim" key="docs">
          <div class="head-row"><h3>How sending works</h3>${copyIcoBtn("GET " + endpoint, "Copy discovery URL")}</div>
          <ul class="fact-list">
            <li><strong>Base URL is public:</strong> <code class="mono">GET ${esc(endpoint)}</code> returns the full tools list. No key needed.</li>
            <li><strong>Running a tool needs a key:</strong> <code class="mono">POST ${esc(endpoint)}/&lt;tool_name&gt;</code> with header <code class="mono">Authorization: Bearer &lt;secret&gt;</code>.</li>
            <li><strong>Body is always JSON:</strong> send the tool input object. Tools with no input take <code class="mono">{}</code>.</li>
            <li><strong>Responses are JSON:</strong> <code class="mono">200</code> = result, <code class="mono">401</code> = bad/missing token, <code class="mono">404</code> = unknown tool.</li>
          </ul>
          <h4 class="agent-sub">1 · Discover tools (public, no key)</h4>
          <div class="cmd-card cmd-row"><pre class="mono http-colored">${highlightHttpBlock("GET " + endpoint)}</pre>${copyIcoBtn("GET " + endpoint, "Copy discovery request")}</div>
          <h4 class="agent-sub">2 · Create a token (shown once)</h4>
          <p class="muted" style="margin:0 0 8px;font-size:.82rem">Press <strong>Create token</strong> above, name it, and store the secret. It is never shown again — use the eye icon to reveal it before leaving.</p>
          <h4 class="agent-sub">3 · Invoke a tool (key required)</h4>
          <div class="cmd-card cmd-row"><pre class="mono http-colored">${highlightHttpBlock("POST " + endpoint + "/<tool_name>\nAuthorization: Bearer <secret>\nContent-Type: application/json\n\n{\n  \"room_id\": \"<id>\",\n  \"command\": \"ls -la\"\n}")}</pre>${copyIcoBtn("POST " + endpoint + "/<tool_name>\nAuthorization: Bearer <secret>\nContent-Type: application/json\n\n{\n  \"room_id\": \"<id>\",\n  \"command\": \"ls -la\"\n}", "Copy invoke template")}</div>
          <h4 class="agent-sub">Paste this to your AI</h4>
          <p class="muted" style="margin:0 0 8px;font-size:.82rem">Copies a context block so the AI understands the API without further explanation.</p>
          <div class="cmd-card cmd-row"><pre class="mono ai-ctx">${esc(aiContextPreview)}</pre>${copyIcoBtn(aiContext, "Copy AI context")}</div>
        </div>`;

      const toolsHTML = `
        <div class="panel agent-tools-card agent-anim" key="tools">
          <div class="head-row">
            <h3>Tools · ${esc(toolLabel)}</h3>
          </div>
          <div class="fact-row"><span>Tools discovery URL</span><strong class="mono">${esc(endpoint)}</strong></div>
          <p class="muted" style="font-size:.8rem">Single template — JSON only. Scroll inside the box.</p>
          <div class="json-wrap">${copyIcoBtn(rawToolsJSON, "Copy tools JSON", "json-copy")}<div class="json-scroll"><pre class="json-colored mono">${toolsJSON}</pre></div></div>
        </div>
        <div class="panel"><h3>Access tokens</h3>
          <p class="muted" style="font-size:.8rem">Tap the copy icon to issue a new full key and copy it at once. The old key stops working immediately.</p>
          ${tokens.length ? `<div class="table-wrap"><table class="table"><thead><tr><th>Name</th><th>Secret</th><th>Created</th><th>Last used</th><th></th></tr></thead><tbody>${tokens.map((t) => `<tr><td>${esc(t.name)}</td><td><span class="mono">•••••• <button type="button" class="icon-btn copy-ico tok-key-copy" data-agent-copykey="${esc(t.id)}" title="Copy full key" aria-label="Copy full key">${ico("copy")}</button></span></td><td>${esc(new Date(t.created_at).toLocaleString())}</td><td>${t.last_used_at ? esc(new Date(t.last_used_at).toLocaleString()) : "Never"}</td><td><div class="row-actions"><button class="btn sm danger action" data-agent-revoke="${esc(t.id)}">Revoke</button></div></td></tr>`).join("")}</tbody></table></div>` : `<p class="muted">No token has been created yet.</p>`}
        </div>`;

      shell(`
        <div class="agent-page">
        <div class="topbar"><div><h2>x5coder-agent</h2><div class="sub">Controlled HTTPS access to VPS Manager tools</div></div><div class="actions"><button class="btn primary action" id="agent-create-token">Create token</button></div></div>
        <div class="tabs agent-tabs">
          <button data-atab="tools" class="${tab === "tools" ? "active" : ""}">Tools</button>
          <button data-atab="docs" class="${tab === "docs" ? "active" : ""}">Docs</button>
        </div>
        ${tab === "docs" ? docsHTML : toolsHTML}
        </div>`, "agent");
      bindCopyables();
      document.querySelectorAll("[data-atab]").forEach((b) => b.onclick = () => { state.agentTab = b.dataset.atab; state._gen++; renderAgent(); });
      const openAgentModal = () => {
        closeAgentModal(true);
        const modal = el(`<div class="modal-back logout-modal agent-modal" id="agent-modal">
          <div class="modal-card logout-card agent-modal-card">
            <h3>Create x5coder-agent token</h3>
            <form id="agent-token-form" class="form-grid">
              <div class="field full"><label>Token name</label><input name="name" required minlength="2" maxlength="64" placeholder="e.g. Production AI agent" autofocus /></div>
              <p class="error full" id="agent-token-error"></p>
              <div class="full row-actions"><button class="btn action" type="button" id="agent-modal-cancel">Cancel</button><button class="btn primary action" type="submit">Create token</button></div>
            </form>
          </div>
        </div>`);
        const done = (instant) => {
          modal.classList.remove("show");
          modal.classList.add("hide");
          setTimeout(() => modal.remove(), instant ? 0 : 220);
        };
        modal._close = done;
        modal.addEventListener("click", (e) => { if (e.target === modal) done(false); });
        modal.querySelector("#agent-modal-cancel").onclick = () => done(false);
        modal.querySelector("#agent-token-form").addEventListener("submit", async (e) => {
          e.preventDefault();
          const error = modal.querySelector("#agent-token-error");
          if (error) error.textContent = "";
          try {
            const fd = new FormData(e.currentTarget);
            const created = await api("/api/agent/tokens", { method: "POST", body: JSON.stringify({ name: fd.get("name") }) });
            done(false);
            await copyText(created.secret || "");
            toast("Token created — use its copy icon anytime for a fresh full key");
            renderAgent();
          } catch (ex) { if (error) error.textContent = ex.message || "Could not create token"; }
        });
        document.body.appendChild(modal);
        requestAnimationFrame(() => requestAnimationFrame(() => modal.classList.add("show")));
      };
      const closeAgentModal = (instant) => {
        const m = document.querySelector("#agent-modal");
        if (m && m._close) m._close(instant);
        else if (m) m.remove();
      };
      document.querySelector("#agent-create-token")?.addEventListener("click", openAgentModal);
      document.querySelectorAll("[data-agent-revoke]").forEach((button) => button.addEventListener("click", async () => {
        if (!confirm("Revoke this token? Any agent using it will lose access immediately.")) return;
        try { await api(`/api/agent/tokens/${encodeURIComponent(button.dataset.agentRevoke)}`, { method: "DELETE" }); toast("Token revoked"); renderAgent(); }
        catch (ex) { toast(ex.message || "Could not revoke token"); }
      }));
      document.querySelectorAll("[data-agent-copykey]").forEach((button) => button.addEventListener("click", async (e) => {
        e.preventDefault();
        e.stopPropagation();
        if (button.disabled || button.classList.contains("busy")) return;
        button.disabled = true;
        button.classList.add("busy");
        try {
          const rotated = await api(`/api/agent/tokens/${encodeURIComponent(button.dataset.agentCopykey)}/rotate`, { method: "POST", body: "{}" });
          await copyText(rotated.secret || "");
          toast("Full key copied");
        } catch (ex) { toast(ex.message || "Could not copy key"); }
        finally { button.disabled = false; button.classList.remove("busy"); }
      }));
    } catch (e) {
      if (alive("agent", gen)) shell(`<p class="error">${esc(e.message)}</p>`, "agent");
    }
  }

  async function renderSettings() {
    const gen = state._gen;
    shell(`<div class="topbar"><div><h2>Settings</h2><div class="sub">Root SSH · Admin vault · Alerts</div></div></div>${skel(4)}`, "settings");
    let st = {};
    try {
      st = await api("/api/storage");
    } catch (e) {
      if (!alive("settings", gen)) return;
      shell(`<p class="error">${esc(e.message)}</p>`, "settings"); return;
    }
    if (!alive("settings", gen)) return;

    shell(`
      <div class="set-page">
      <div class="set-hero"><span class="set-hero-ico">${ico("gear", 20)}</span><div><h2>Settings</h2><div class="sub">Passwords · Storage · Alerts</div></div></div>
      <section class="set-sec">
        <header><span class="set-ico">${ico("key", 16)}</span><div><h3>Root SSH password</h3><p>Used for direct SSH access as root.</p></div></header>
        <div class="set-body">
          <form id="pw-form" class="mng-col">
            <div class="field"><label>New root password</label><input name="password" type="password" minlength="8" required placeholder="Minimum 8 characters" /></div>
            <div><button class="btn primary action set-btn" type="submit">Update root password</button></div>
          </form>
          <p class="error" id="pwerr"></p>
        </div>
      </section>
      <section class="set-sec">
        <header><span class="set-ico">${ico("shield", 16)}</span><div><h3>Admin panel password</h3><p>Signs out all admin sessions.</p></div></header>
        <div class="set-body">
          <form id="adminpass" class="mng-col">
            <div class="field"><label>Current admin password</label><input name="current" type="password" required autocomplete="current-password" /></div>
            <div class="field"><label>New admin password</label><input name="new_password" type="password" minlength="8" required autocomplete="new-password" placeholder="Minimum 8 characters" /></div>
            <div><button class="btn primary action set-btn" type="submit">Update admin password</button></div>
          </form>
          <p class="error" id="adminerr"></p>
          <p class="ok-text hidden" id="adminok">Admin password updated.</p>
        </div>
      </section>
      <section class="set-sec">
        <header><span class="set-ico">${ico("db", 16)}</span><div><h3>Storage</h3><p>Writable-data quota model.</p></div></header>
        <div class="set-body">
          <div class="set-kv"><span class="muted">Free disk</span><strong>${fmtBytes(st.disk_free)}</strong></div>
          <div class="set-kv"><span class="muted">Quota reserved</span><strong>${fmtBytes(st.quota_reserved)}</strong></div>
          <div class="set-kv"><span class="muted">Host free</span><strong class="ok-text">${(st.quota_available_gb || 0).toFixed(2)} GB</strong></div>
          <p class="muted mng-note" style="margin:10px 0 0">Room quota caps files, volumes and container data — not Docker images. Host fill is mostly images and build cache.</p>
        </div>
      </section>
      <section class="set-sec">
        <header><span class="set-ico">${ico("bell", 16)}</span><div><h3>Access alerts</h3><p>Optional Telegram notifications.</p></div></header>
        <div class="set-body">
          <form id="notify-form" class="mng-col">
            <div class="field"><label>Notify bot token</label><input name="bot_token" type="password" placeholder="123456:ABC…" autocomplete="off" /></div>
            <div class="field"><label>Notify chat id</label><input name="chat_id" placeholder="e.g. 123456789" autocomplete="off" /></div>
            <div class="set-btns">
              <button class="btn primary action" type="submit">Save</button>
              <button class="icon-btn danger" type="button" id="notify-clear" title="Disable alerts" aria-label="Disable alerts">${ico("trash", 14)}</button>
            </div>
          </form>
          <p class="error" id="notifyerr"></p>
          <p class="muted mng-note" id="notifystatus" style="margin-top:8px"></p>
        </div>
      </section>
      </div>`, "settings");

    bindCopyables();

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

  const isEnvVolumeName = (n) => {
    const s = String(n || "").toLowerCase();
    return s === "env" || s === ".env" || s.endsWith(".env");
  };

  function uploadHTML(isMulti, port) {
    const hasLog = !!(state._zipLog && String(state._zipLog).trim());
    return `<form id="zip-upload-form" class="upload-card" novalidate>
      <div class="dropzone upload-drop" id="zip-drop" role="button" tabindex="0" aria-label="Drop project archive here or tap to choose">
        <input type="file" name="file" id="zip-file-input" accept=".zip,.tar.gz,.tgz,.tar" hidden />
        <div class="dz-icon" aria-hidden="true">${ico("upload", 26)}</div>
        <div class="dz-title">Drop archive here or <span class="dz-link">choose file</span></div>
        <div class="dz-sub">.zip · .tar.gz · .tgz · .tar — updates this room only</div>
        <div class="dz-file hidden" id="zip-file-chip"><span class="dz-file-ico">${ico("file", 14)}</span><span class="mono" id="zip-file-name"></span></div>
      </div>
      ${isMulti ? "" : `<div class="field upload-port"><label>Internal port</label><input name="internal_port" type="number" min="1" max="65535" value="${Number(port) || 80}" /></div>`}
      <button class="btn primary action upload-btn" type="submit" id="zip-upload-btn" disabled><span class="up-ico">${ico("upload", 15)}</span> Upload &amp; update room</button>
    </form>
    <p class="error" id="ziperr"></p><p class="muted" id="zipok"></p>
    <div class="logs-viewer zip-log-wrap ${hasLog ? "zip-log-enter" : "hidden"}" id="zip-log-wrap" style="margin-top:12px;min-height:120px">
      <div class="logs-body" id="zip-log">${esc(state._zipLog || "")}</div>
    </div>`;
  }
  function bindUploadDropzone() {
    const drop = document.querySelector("#zip-drop");
    const input = document.querySelector("#zip-file-input");
    const form = document.querySelector("#zip-upload-form");
    const btn = document.querySelector("#zip-upload-btn");
    const chip = document.querySelector("#zip-file-chip");
    const nameEl = document.querySelector("#zip-file-name");
    if (!drop || !input || !form || !btn) return;
    const setFile = (file) => {
      if (!file) return;
      try {
        const dt = new DataTransfer();
        dt.items.add(file);
        input.files = dt.files;
      } catch {}
      if (nameEl) nameEl.textContent = `${file.name} · ${fmtBytes(file.size)}`;
      chip?.classList.remove("hidden");
      drop.classList.add("has-file");
      btn.disabled = false;
    };
    drop.addEventListener("click", (e) => {
      if (e.target.closest("#zip-file-chip")) return;
      input.click();
    });
    drop.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") { e.preventDefault(); input.click(); }
    });
    input.addEventListener("change", () => { if (input.files && input.files[0]) setFile(input.files[0]); });
    ["dragenter", "dragover"].forEach((ev) => drop.addEventListener(ev, (e) => {
      e.preventDefault();
      drop.classList.add("drag");
    }));
    ["dragleave", "drop"].forEach((ev) => drop.addEventListener(ev, (e) => {
      e.preventDefault();
      if (ev === "dragleave" && e.relatedTarget && drop.contains(e.relatedTarget)) return;
      drop.classList.remove("drag");
    }));
    drop.addEventListener("drop", (e) => {
      const f = e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files[0];
      if (f) {
        setFile(f);
        if (form.requestSubmit) form.requestSubmit();
        else form.dispatchEvent(new Event("submit", { cancelable: true }));
      }
    });
  }

  let panelVersionPromise = null;
  function refreshPanelVersion() {
    const el = document.querySelector("#panel-version");
    if (!el) return;
    if (!panelVersionPromise) {
      panelVersionPromise = api("/api/version").then((v) => (v && v.version) || "").catch(() => "");
    }
    panelVersionPromise.then((v) => {
      const cur = document.querySelector("#panel-version");
      if (cur && v) cur.textContent = v;
    });
  }

  async function renderRoom() {
    const gen = state._gen;    const id = state.roomId || state.me?.room?.id;
    if (!id) { setView("rooms"); return; }
    shell(`<div class="topbar"><div><h2>Project</h2><div class="sub">Loading…</div></div></div>${skel(4)}`, "room");
    let room;
    try { room = await api(`/api/rooms/${id}`); } catch (e) {
      if (!alive("room", gen)) return;
      shell(`<p class="error">${esc(e.message)}</p>`, "room"); return;
    }
    if (!alive("room", gen)) return;
    state.roomId = id;
    let tab = state.roomTab || "overview";
    if (tab === "terminal") { tab = "overview"; state.roomTab = "overview"; }
    const projs = room.projects || [];
    const containers = (room.containers && room.containers.length) ? room.containers : projs.map((p, i) => ({
      ordinal: i + 1, id: p.id, name: p.name, image: p.image, status: p.status,
      host_port: p.host_port, docker_id: p.container_id,
    }));
    const images = room.images || [];
    const volumes = room.volumes || [];
    const mainProj = projs[0];
    const roomJob = room.job || room.status === "deploying" || room.status === "building";
    const emptyRoom = !mainProj && !containers.length && !roomJob;
    const deploying = !!roomJob && (!mainProj || room.status === "deploying" || room.status === "building");
    const isMulti = room.kind === "multi";
    const cs = containers.map((c) => c.status || "");
    const anyRun = cs.includes("running") || (mainProj && mainProj.status === "running");
    const anyRestart = cs.includes("restarting");
    const anyCrashed = cs.includes("exited") || cs.includes("dead");
    const roomState = emptyRoom ? "empty" : anyRun ? "running" : anyRestart ? "restarting" : anyCrashed ? "crashed" : (cs.length ? "stopped" : "empty");
    const stateCls = { empty: "warn", running: "ok", restarting: "warn", crashed: "stop", stopped: "stop" };
    const stateTxt = { empty: "empty", running: "running", restarting: "restarting", crashed: "crashed", stopped: "stopped" };
    let st = null;
    try { st = await api("/api/storage"); } catch {}
    const qgb = room.quota_bytes ? gb(room.quota_bytes).toFixed(2) : "";

    let body = "";
    if (tab === "overview") {
    if (deploying && !mainProj) {
      body = `<div class="panel"><h3>Deploying</h3>
        <p class="muted" style="margin:0">GitHub already handed the tar to this room. Docker is loading the image — the project appears here when the container is up (usually 1–3 minutes).</p>
        <div class="logs-viewer" style="margin-top:12px"><div class="logs-body" id="tar-log">docker load in progress…</div></div>
      </div>`;
    } else if (emptyRoom) {
      body = `<div class="panel"><h3>Empty room</h3>
        <p class="muted" style="margin:0 0 12px">Isolated and empty. Upload a project ZIP below, or deploy by image name (single) or compose text (multi). Kind: <strong>${esc(isMulti ? "multi (compose / several containers)" : "single (one container)")}</strong>.</p>
        <form id="image-deploy-form" class="form-grid">
          <div class="field full"><label>Docker image</label><input name="image" placeholder="nginx:alpine" required /></div>
          <div class="field"><label>Host port</label><input name="host_port" type="number" min="0" max="65535" placeholder="e.g. 8000" /></div>
          <div class="field"><label>Container port</label><input name="container_port" type="number" min="0" max="65535" value="80" /></div>
          <div class="field full"><button class="btn primary action" type="submit">Deploy image</button></div>
        </form>
        <p class="error" id="tarerr"></p>
        <div class="logs-viewer" style="margin-top:12px;min-height:120px">
          <div class="logs-body" id="tar-log">(waiting for deploy)</div>
        </div>
        ${uploadHTML(isMulti, 80)}
        <p class="error" id="ziperr"></p><p class="muted" id="zipok"></p>
      </div>`;
    } else {
      body = `
        <div class="proj-hero">
          <div class="proj-hero-top">
            ${projectIconHTML(anyRun ? "ok" : (emptyRoom ? "empty" : "stop"))}
            <div class="proj-ident">
              <h4>${esc(room.name)}</h4>
              <button type="button" class="proj-id-btn copyable" data-copy="${esc(id)}" title="Copy id">
                <span>ID</span>
                <code>${esc(id)}</code>
              </button>
            </div>
            <span class="badge ${stateCls[roomState] || "stop"}">${esc(stateTxt[roomState] || roomState)}</span>
            <span class="badge ${isMulti ? "info" : "muted-badge"}">${esc(isMulti ? "multi · compose" : "single")}</span>
          </div>
        </div>
        ${(roomState === "restarting" || roomState === "crashed")
          ? `<p class="error" style="margin:10px 0 0">This container is ${esc(roomState)} — it keeps exiting. Open <strong>Container → Logs</strong> to see the error. Most apps crash because an env value (like a token) is missing.</p>`
          : ""}
        <div class="panel"><h3>Source upload</h3>
          ${uploadHTML(isMulti, (mainProj && mainProj.container_port) || 80)}
          <p class="error" id="ziperr"></p><p class="muted" id="zipok"></p>
        </div>
        <div class="stat-chips">
          <div class="stat"><div class="label">Containers</div><div class="value">${containers.length}</div><div class="muted">${images.length} images · ${volumes.length} volumes</div></div>
          <div class="stat"><div class="label">Quota used</div><div class="value">${fmtBytes(room.usage_bytes)}</div><div class="muted">cap ${room.quota_bytes ? fmtBytes(room.quota_bytes) : "not set"} · files + volumes + RW</div></div>
          <div class="stat"><div class="label">Project size</div><div class="value">${fmtBytes(Number(room.footprint_bytes) || ((Number(room.usage_bytes)||0)+(Number(room.image_bytes)||0)))}</div><div class="muted">image ${fmtBytes(room.image_bytes)} · volumes ${fmtBytes(room.volume_bytes)}</div></div>
          <div class="stat"><div class="label">Password</div><div class="value" style="font-size:1rem">${room.password
            ? `<div class="secret-row"><span class="secret-mask">••••••••</span><button type="button" class="btn sm action" data-copy="${esc(room.password)}">Copy</button></div>`
            : `<span class="muted">hidden until unlock</span>`}</div></div>
        </div>
        <div class="live-grid">
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
        
              ${updateHistoryHTML(room.updates)}
        `;
    }
    } else if (tab === "manage") {
      body = `
<div class="mng-grid">
${(function () {
          if (!mainProj) return `<section class="panel mng-card"><div class="mng-head"><span class="mng-ico">${ico("link", 15)}</span><h3>Port &amp; domain</h3></div><p class="muted" style="margin:0">Add a container first.</p></section>`;
          const hasPort = Number(mainProj.host_port) > 0;
          const hasDomain = !!(mainProj.domain && String(mainProj.domain).trim());
          const show = hasPort || hasDomain || state.showNetPanel;
          if (!show) {
            return `<section class="panel mng-card"><div class="mng-head"><span class="mng-ico">${ico("link", 15)}</span><h3>Port &amp; domain</h3></div>
              <button class="btn primary action mng-wide" type="button" id="show-net-panel">Set port / domain</button>
            </section>`;
          }
          return `
        <section class="panel mng-card"><div class="mng-head"><span class="mng-ico">${ico("link", 15)}</span><h3>Port &amp; domain</h3>${mainProj.host_port ? `<span class="badge port-badge mono">:${mainProj.host_port}</span>` : ""}</div>
          <form id="port-form" class="mng-row">
            <div class="field mng-grow"><label>Host port</label><input name="host_port" type="number" min="0" max="65535" value="${mainProj.host_port || ""}" placeholder="e.g. 8000" /></div>
            <div class="mng-btns">
              <button class="btn primary sm action" type="submit" title="Save port">Save</button>
              <button class="icon-btn danger" type="button" id="clear-port" title="Disable port" aria-label="Disable port">${ico("trash", 14)}</button>
            </div>
          </form>
          <form id="domain-form" class="mng-col" style="margin-top:12px">
            <div class="field"><label>Domain</label><input name="domain" value="${esc(mainProj.domain || "")}" placeholder="app.example.com" /></div>
            <div class="mng-row">
              <div class="field mng-grow"><label>SSL status</label><input readonly value="${esc(mainProj.ssl_status || "—")}" /></div>
              <div class="mng-btns">
                <button class="btn primary sm action" type="submit" title="Bind domain" id="bind-domain-btn">Bind</button>
                <button class="btn sm action" type="button" id="test-domain-btn" title="Test domain connection">Test</button>
                <button class="icon-btn danger" type="button" id="clear-domain" title="Disable domain" aria-label="Disable domain">${ico("trash", 14)}</button>
              </div>
            </div>
          </form>
          <p class="error" id="linkerr"></p>
          <p class="ok-text hidden" id="linkok"></p>
          <p class="muted mng-note" id="domain-test-result"></p>
          <p class="muted mng-note">Applied via nginx proxy on this VPS.</p>
        </section>`;
        })()}
<section class="panel mng-card"><div class="mng-head"><span class="mng-ico">${ico("user", 15)}</span><h3>Identity &amp; disk</h3></div>
          ${(() => {
            const cur = Number(qgb) || 0.1;
            const maxQ = Math.max(cur, Number(room.quota_max_gb || room.quota_available_gb || st?.quota_available_gb || cur));
            return `<form id="rname" class="mng-row">
              <div class="field mng-grow"><label>Project name</label><input name="name" value="${esc(room.name)}" minlength="2" maxlength="40" pattern="[A-Za-z0-9_-]{2,40}" placeholder="Project name" /></div>
              <div class="mng-btns"><button class="icon-btn bk-go" type="submit" title="Save name" aria-label="Save name">${ico("check", 14)}</button></div>
            </form>
            <form id="rpass" class="mng-row" style="margin-top:12px">
              <div class="field mng-grow"><label>Project password</label><input name="password" type="text" minlength="6" placeholder="New project password" /></div>
              <div class="mng-btns"><button class="icon-btn bk-go" type="submit" title="Save password" aria-label="Save password">${ico("check", 14)}</button></div>
            </form>
            <form id="quota" class="mng-col" style="margin-top:12px">
              <div class="field">${quotaSliderHTML({ name: "quota_gb", maxGB: maxQ, valueGB: cur, required: true })}</div>
              <p class="muted mng-note" style="margin:0">Cap applies now. Max <strong>${maxQ.toFixed(1)} GB</strong>.</p>
              <div><button class="btn primary sm action" type="submit">Save disk</button></div>
            </form>`;
          })()}
          <p class="ok-text hidden" id="rok">Saved.</p>
          <p class="error" id="rerr"></p>
        </section>
        <section class="panel mng-card"><div class="mng-head"><span class="mng-ico">${ico("box", 15)}</span><h3>Room backup</h3><span class="muted mng-tag">background</span></div>
          <p class="muted mng-note" style="margin:0">Zips into <code>backup/${esc(id)}.zip</code> (stack + volumes + config + .env).</p>
          <div class="mng-row" style="margin-top:10px;align-items:center">
            <div class="mng-grow"><div class="bk-sub mono" data-bk-filerow="${esc(id)}">— none —</div><div class="bk-status" data-bk-status="${esc(id)}"></div></div>
            <div class="bk-tools" data-bk-fileactions="${esc(id)}">
              <button type="button" class="icon-btn bk-go" data-bk-room="${esc(id)}" title="Backup now" aria-label="Backup now">${ico("play", 14)}</button>
            </div>
          </div>
          <p class="error hidden" data-bk-err="${esc(id)}"></p>
        </section>
</div>`;
    } else if (tab === "container") {
      const ct = containers.find((c) => c.id === state.ctrId) || containers[0];
      let sub = state.ctrTab || "files";
      if (sub === "update") { sub = "files"; state.ctrTab = "files"; }
      if (!ct) {
        body = `<div class="panel"><p class="muted">No container selected.</p><button class="btn sm action" id="back-ctrs">Back to containers</button></div>`;
      } else {
        const subnav = `<div class="tabs" style="margin-bottom:12px">
          <button data-ctr-tab="files" class="${sub === "files" ? "active" : ""}">Files</button>
          <button data-ctr-tab="logs" class="${sub === "logs" ? "active" : ""}">Logs</button>
        </div>
        <p class="muted" style="margin:0 0 12px">${esc(ctrNum(ct))} ${esc(ctrLabel(ct))} · <code>${esc(shortDocker(ct.docker_id))}</code> · ${esc(ct.image || "")}</p>`;
        if (sub === "logs") {
          let lg = { log: "" };
          try { lg = await api(`/api/rooms/${id}/logs?container=${encodeURIComponent(ct.docker_id || ct.id || ct.name || "")}`); } catch (ex) { lg = { log: ex.message || "" }; }
          body = `<div class="panel"><div class="head-row"><h3>Logs · ${esc(ctrLabel(ct))}</h3>
            <div class="row-actions"><button class="btn sm action" id="back-ctrs">Containers</button><button class="btn sm action" id="copylog">Copy</button><button class="btn sm action" id="reflog">Refresh</button></div></div>
            ${subnav}
            <div class="logs-viewer room-logs-viewer"><div class="logs-body" id="rlog">${formatLogHTML(lg.log || "(empty)")}</div></div></div>`;
        } else {
          const fpath = state.filePath || "/";
          let listing = { entries: [], path: fpath };
          try { listing = await api(`/api/rooms/${id}/containers/${encodeURIComponent(ct.id)}/files?path=${encodeURIComponent(fpath)}`); } catch (e) { listing.note = e.message; }
          if (listing.binary) {
            body = `<div class="panel"><div class="head-row"><h3>${esc(listing.path)}</h3>
              <div class="row-actions"><button class="btn sm action" id="back-ctrs">Containers</button><button class="btn sm action" id="backfiles">Back</button></div></div>
              ${subnav}<p class="muted">${esc(listing.note || "Binary file")}</p></div>`;
          } else if (listing.content != null) {
            body = `<div class="panel"><div class="head-row"><h3>${esc(listing.path)}</h3>
              <div class="row-actions"><button class="btn sm action" id="back-ctrs">Containers</button><button class="btn sm action" id="backfiles">Back</button><button class="btn sm primary action" id="savefile">Save</button><button class="btn sm danger action" id="delfile">Delete</button></div></div>
              ${subnav}<textarea class="file-editor" id="fedit">${esc(listing.content)}</textarea></div>`;
          } else {
            const base = listing.path === "/" ? "" : listing.path;
            body = `<div class="panel"><div class="head-row"><h3>Files · ${esc(ctrLabel(ct))} · ${esc(listing.path || "/")}</h3>
              <div class="row-actions"><button class="btn sm action" id="back-ctrs">Containers</button><button class="btn sm action" id="updir">Up</button></div></div>
              ${subnav}
              <ul class="file-list">${(listing.entries || []).map((e) => `<li><a href="#" data-path="${esc((base === "" ? "" : base) + "/" + e.name)}">${e.dir ? "📁" : "📄"} ${esc(e.name)}</a><span class="muted">${e.dir ? "dir" : fmtBytes(e.size)}</span></li>`).join("") || `<li class="muted">${esc(listing.note || "Empty")}</li>`}</ul></div>`;
          }
        }
      }
    } else if (tab === "images") {
      body = `<div class="panel"><h3>Images</h3>
        <table class="table"><thead><tr><th>#</th><th>Image</th><th>Ref</th><th>Size</th></tr></thead>
        <tbody>${(images || []).map((im) => `<tr><td class="mono">#${String(im.ordinal || 1).padStart(3,"0")}</td><td>${esc(im.name || "image")}</td><td class="mono muted">${esc(im.ref || "")}</td><td>${fmtBytes(im.size_bytes || 0)}</td></tr>`).join("") || `<tr><td colspan="4" class="muted">No images in this room.</td></tr>`}</tbody></table>
      </div>`;
    } else if (tab === "volume") {
      const vol = volumes.find((v) => v.id === state.volId) || volumes[0];
      if (!vol) {
        body = `<div class="panel"><p class="muted">No volume selected.</p><button class="btn sm action" id="back-vols">Back to volumes</button></div>`;
      } else {
        const fpath = state.filePath && state.filePath !== "." ? state.filePath : "/";
        let listing = { entries: [], path: fpath };
        try { listing = await api(`/api/rooms/${id}/volumes/${encodeURIComponent(vol.id)}/files?path=${encodeURIComponent(fpath)}`); } catch (e) { listing.note = e.message; }
        if (listing.binary) {
          body = `<div class="panel"><div class="head-row"><h3>${esc(listing.path)}</h3>
            <div class="row-actions"><button class="btn sm action" id="back-vols">Volumes</button><button class="btn sm action" id="backfiles">Back</button></div></div>
            <p class="muted">${esc(listing.note || "Binary file")}</p></div>`;
        } else if (listing.content != null) {
          body = `<div class="panel"><div class="head-row"><h3>${esc(vol.name || "volume")} · ${esc(listing.path)}</h3>
            <div class="row-actions"><button class="btn sm action" id="back-vols">Volumes</button><button class="btn sm action" id="backfiles">Back</button></div></div>
            <pre class="file-editor" style="white-space:pre-wrap">${esc(listing.content)}</pre></div>`;
        } else {
          const base = listing.path === "/" ? "" : listing.path;
          body = `<div class="panel"><div class="head-row"><h3>Volume · ${esc(vol.name || "volume")} · ${esc(listing.path || "/")}</h3>
            <div class="row-actions"><button class="btn sm danger action" type="button" data-vol-clean="${esc(vol.id)}">Clean volume</button><button class="btn sm action" id="back-vols">Back</button></div></div>
            <p class="muted" style="margin:0 0 10px"><code>${esc(vol.docker_name || "")}</code></p>
            <ul class="file-list">${(listing.entries || []).filter((e) => { const n=(e.name||"").toLowerCase(); return n !== ".env" && !n.endsWith(".env"); }).map((e) => `<li><a href="#" data-vol-path="${esc((base === "" ? "" : base) + "/" + e.name)}">${e.dir ? "📁" : "📄"} ${esc(e.name)}</a><span class="muted">${e.dir ? "dir" : fmtBytes(e.size)}</span></li>`).join("") || `<li class="muted">${esc(listing.note || "Empty")}</li>`}</ul></div>`;
        }
      }
    } else if (tab === "volumes") {
      body = `<div class="panel"><div class="head-row"><div class="row-actions">${mainProj ? `<button class="btn sm danger action" type="button" id="wipe-all-data">Wipe all volumes (keep .env)</button>` : ""}</div></div>
        <table class="table"><thead><tr><th>#</th><th>Volume</th><th>Source</th><th></th></tr></thead>
        <tbody>${(volumes || []).filter((v) => !isEnvVolumeName(v.name)).map((v) => `<tr data-vid="${esc(v.id)}" class="vol-row" style="cursor:pointer"><td class="mono">#${String(v.ordinal || 1).padStart(3,"0")}</td><td>${esc(v.name || "volume")}</td><td class="mono muted">${esc(v.docker_name || v.name || "")}</td><td><button type="button" class="btn sm danger action" data-vol-clean="${esc(v.id)}">Clean</button></td></tr>`).join("") || `<tr><td colspan="4" class="muted">No volumes in this room.</td></tr>`}</tbody></table>
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
          <div class="row-actions">${mainProj ? `<button class="btn sm danger action" type="button" id="wipe-all-data">Wipe all volumes (keep .env)</button>` : ""}<button class="btn sm action" id="updir">Up</button></div></div>
          <ul class="file-list">${(listing.entries || []).map((e) => `<li><a href="#" data-path="${esc((listing.path === "." ? "" : listing.path + "/") + e.name)}">${e.dir ? "📁" : "📄"} ${esc(e.name)}</a><span class="muted">${e.dir ? "dir" : fmtBytes(e.size)}</span></li>`).join("") || "<li class='muted'>Empty</li>"}</ul></div>`;
      }
    } else if (tab === "logs") {
      const list0 = containers || [];
      const pick = pickLogContainer(list0);
      if (pick) state.roomLogCtr = pick;
      let lg = { log: "", containers: list0, container_id: pick };
      try {
        const q = pick ? `?container=${encodeURIComponent(pick)}` : "";
        lg = await api(`/api/rooms/${id}/logs${q}`);
      } catch (ex) {
        lg = { log: ex.message || "(empty)", containers: list0, container_id: pick };
      }
      const list = lg.containers && lg.containers.length ? lg.containers : list0;
      const active = lg.container_id || pick || pickLogContainer(list);
      if (active) state.roomLogCtr = active;
      body = `<div class="panel"><div class="head-row"><h3>Logs</h3>
        <div class="row-actions"><button class="btn sm action" id="copylog">Copy</button><button class="btn sm action" id="reflog">Refresh</button><button class="btn sm danger action" id="clear-room-log">Clear log</button></div></div>
        <div class="row-actions" style="flex-wrap:wrap;margin:0 0 10px" id="log-ctrs">${list.map((c) => `<button type="button" class="btn sm action ${active === c.id || active === c.docker_id ? "primary" : ""}" data-log-ctr="${esc(logContainerKey(c))}">${esc(ctrNum(c))} ${esc(ctrLabel(c))}</button>`).join("") || `<span class="muted">No containers</span>`}</div>
        <p class="muted" style="margin:0 0 8px;font-size:0.78rem">One container at a time · newest at bottom · live</p>
        <div class="logs-viewer room-logs-viewer">
          <div class="logs-body" id="rlog">${formatLogHTML(lg.log || "(empty)")}</div>
        </div></div>`;
    } else if (tab === "env") {
      let envMeta = { content: "" };
      try { envMeta = await api(`/api/rooms/${id}/env`); } catch {
        try { if (mainProj) envMeta = await api(`/api/projects/${mainProj.id}/env`); } catch {}
      }
      const rows = parseEnvForm(envMeta.content || "");
      body = `<div class="panel">
        <div class="head-row"><h3>Project secrets</h3>
          <div class="row-actions">
            <button class="btn sm action" id="env-add">Add row</button>
            <button class="btn sm action" id="env-mode">Raw editor</button>
            <button class="btn sm primary action" id="env-save">Save</button>
          </div>
        </div>
        <p class="muted mono" style="margin-bottom:6px">${esc(envMeta.path || "")}</p>
        <p class="muted" style="margin:0 0 10px;font-size:0.82rem">Shared secrets for this room (all containers). Save recreates Docker so new values are loaded.</p>
        <form id="env-form" class="env-form">
          ${rows.map((r) => `<div class="env-row">
            <input name="key" placeholder="KEY" value="${esc(r.key)}" />
            <input name="value" placeholder="value" value="${esc(r.value)}" />
            <button type="button" class="btn sm danger env-del" title="Remove">×</button>
          </div>`).join("")}
        </form>
        <textarea class="file-editor hidden" id="env-raw">${esc(envMeta.content || "")}</textarea>
        <p class="error" id="enverr"></p>
        <p class="ok-text hidden" id="envok">Saved — containers recreated with new env.</p>
      </div>`;
    }

    shell(`
      <div class="room-view">
      <div class="topbar">
        <div class="topbar-main">
          <h2>${esc(room.name)}</h2>
          <div class="sub"><span class="mono">${esc(id)}</span><button type="button" class="id-copy copyable" data-copy="${esc(id)}" title="Copy id">copy</button></div>
        </div>
        <div class="row-actions">
          ${emptyRoom ? "" : powerToggleHTML(id, anyRun ? "running" : ((projs[0] && projs[0].status) || "stopped"))}
          <button class="btn sm danger action" data-act="delete">Delete</button>
        </div>
      </div>
      <div class="tabs">
        <button data-tab="overview" class="${tab === "overview" ? "active" : ""}">Overview</button>
        <button data-tab="images" class="${tab === "images" ? "active" : ""}">Images</button>
        <button data-tab="volumes" class="${tab === "volumes" || tab === "volume" ? "active" : ""}">Volumes</button>
        <button data-tab="env" class="${tab === "env" ? "active" : ""}">Secrets</button>
        <button data-tab="logs" class="${tab === "logs" ? "active" : ""}">Logs</button>
        <button data-tab="manage" class="${tab === "manage" ? "active" : ""}">Manage</button>
          </div>
      ${body}
      </div>`, "room");

    bindCmdCopies();
    document.querySelectorAll("[data-tab]").forEach((b) => b.onclick = () => setView("room", { roomTab: b.dataset.tab, filePath: b.dataset.tab === "files" ? (state.filePath || ".") : state.filePath }));
    bindPowerToggles();
    document.querySelectorAll("[data-act]").forEach((b) => bindAction(b, async () => {
      if (b.dataset.act === "delete") {
        if (!await confirmAction({
          title: "Delete this room?",
          body: "This removes the room and its data. This cannot be undone.",
          ok: "Delete",
          danger: true,
        })) return;
        await api(`/api/rooms/${id}`, { method: "DELETE" });
        await unlockOwner();
        setView("rooms");
      }
    }));
    bindCopyables();

    if (tab === "overview" || tab === "manage") {
      document.querySelector("#image-deploy-form")?.addEventListener("submit", async (e) => {
        e.preventDefault();
        const err = document.querySelector("#tarerr");
        const logEl = document.querySelector("#tar-log");
        if (err) err.textContent = "";
        const fd = new FormData(e.target);
        const image = String(fd.get("image") || "").trim();
        if (!image) { if (err) err.textContent = "Type an image name"; return; }
        if (logEl) logEl.textContent = "Deploying " + image + "...\n";
        try {
          await streamFetch(`/api/projects`, { method: "POST", body: JSON.stringify({ image, name: room.name || "app", host_port: Number(fd.get("host_port") || 0), container_port: Number(fd.get("container_port") || 80) }) }, (chunk) => {
            if (!logEl) return;
            logEl.textContent += chunk;
            logEl.scrollTop = logEl.scrollHeight;
          });
          await renderRoom();
        } catch (ex) {
          if (err) err.textContent = ex.message || "Deploy failed";
        }
      });
      document.querySelector("#zip-upload-form")?.addEventListener("submit", async (e) => {
        e.preventDefault();
        const err = document.querySelector("#ziperr");
        const ok = document.querySelector("#zipok");
        if (err) err.textContent = "";
        if (ok) ok.textContent = "";
        const btn = e.target.querySelector("button[type=submit]");
        const logWrap = document.querySelector("#zip-log-wrap");
        const logEl = document.querySelector("#zip-log");
        const fd = new FormData(e.target);
        const file = fd.get("file");
        if (!file || !file.size) { if (err) err.textContent = "Choose a ZIP file first"; return; }
        if (logWrap) {
          logWrap.classList.remove("hidden");
          logWrap.classList.remove("zip-log-enter");
          void logWrap.offsetWidth;
          logWrap.classList.add("zip-log-enter");
        }
        if (logEl) logEl.textContent = `$ upload ${file.name || "archive"}…\n`;
        if (btn) { btn.disabled = true; btn.innerHTML = "Uploading…"; }
        try {
          const r = await fetch(`/api/rooms/${id}/upload`, { method: "POST", body: fd, credentials: "same-origin" });
          const j = await r.json().catch(() => ({}));
          if (!r.ok) throw new Error(j.error || ("Upload failed (" + r.status + ")"));
          const lines = [`$ upload ${file.name || "archive"} → ${room.name || id} (${j.kind || (isMulti ? "multi" : "single")})`];
          if (j.stored) lines.push(`✓ stored ${j.stored.files ?? 0} files (${fmtBytes(j.stored.bytes || 0)}) → ${String(j.stored.dir || "").split("/").slice(-2).join("/")}${j.stored.unwrapped ? " [unwrapped]" : ""}`);
          const env = j.env_applied || {};
          const added = [...(env.from_env || []), ...(env.from_example || [])];
          if (added.length) lines.push(`✓ env: +${added.length} vars (${added.slice(0, 8).join(", ")}${added.length > 8 ? ", …" : ""})`);
          if (j.dockerfile_generated) lines.push("✓ Dockerfile: generated for detected stack");
          else if (j.kind !== "multi" && !isMulti) lines.push("✓ Dockerfile: existing");
          if (j.deployment) lines.push(`✓ deployment: ${j.deployment.status || "?"}${j.deployment.built ? ` → ${j.deployment.built.image || ""}` : ""}`);
          if (j.note) lines.push(`ℹ ${j.note}`);
          state._zipLog = lines.join("\n");
          toast("Room updated");
          await renderRoom();
        } catch (ex) {
          if (err) err.textContent = ex.message || "Upload failed";
        } finally {
          if (btn) { btn.disabled = false; btn.innerHTML = `<span class="up-ico">${ico("upload", 15)}</span> Upload &amp; update room`; }
        }
      });
      bindUploadDropzone();
      bindQuotaSliders();
      const flashOk = () => {
        const el = document.querySelector("#rok");
        if (!el) return;
        el.classList.remove("hidden");
        setTimeout(() => el.classList.add("hidden"), 1800);
      };
      document.querySelector("#rname")?.addEventListener("submit", async (e) => {
        e.preventDefault();
        try {
          await api(`/api/rooms/${id}/name`, { method: "POST", body: JSON.stringify({ name: new FormData(e.target).get("name") }) });
          flashOk();
          render();
        } catch (ex) { document.querySelector("#rerr").textContent = ex.message; }
      });
      document.querySelector("#quota")?.addEventListener("submit", async (e) => {
        e.preventDefault();
        const btn = e.target.querySelector("button[type=submit]");
        try {
          if (btn) { btn.disabled = true; btn.textContent = "Applying…"; }
          await api(`/api/rooms/${id}/quota`, { method: "POST", body: JSON.stringify({ quota_gb: Number(new FormData(e.target).get("quota_gb") || 0) }) });
          flashOk();
          render();
        } catch (ex) { document.querySelector("#rerr").textContent = ex.message; }
        finally { if (btn) { btn.disabled = false; btn.textContent = "Save disk"; } }
      });
      document.querySelector("#rpass")?.addEventListener("submit", async (e) => {
        e.preventDefault();
        try {
          const res = await api(`/api/rooms/${id}/password`, { method: "POST", body: JSON.stringify({ password: new FormData(e.target).get("password") }) });
          e.target.reset();
          toast("Password updated — all devices signed out");
          if (res.logged_out && state.me?.kind === "room") {
            state.me = null;
            setView("server");
            await loadMe();
            renderUnlock();
            return;
          }
          flashOk();
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
      const paintDomTest = (box, okBox, errBox, t) => {
        if (!box || !t) return;
        const row = (label, ok, detail) => `<div>${ok ? "✓" : "✗"} <strong>${esc(label)}:</strong> ${esc(detail)}</div>`;
        const lh = t.local_http || {}, dh = t.public_http || {}, ds = t.public_https || {};
        let h = row("Nginx on this server", !!lh.ok, lh.ok ? ("HTTP " + lh.code) : (lh.error || "failed"));
        h += row("Public HTTP", !!dh.ok, dh.ok ? ("HTTP " + dh.code) : (dh.error || "unreachable"));
        h += row("Public HTTPS", !!ds.ok, ds.ok ? ("HTTPS " + ds.code) : (ds.error || "unreachable"));
        const ips = (t.ips || []).join(", ");
        h += `<div>DNS: ${esc(ips || "no records")}${t.points_to_server ? " (points here)" : " (CDN/proxy — ok if public answers)"}</div>`;
        box.innerHTML = `<span style="color:${t.reachable ? "green" : "orange"}">${t.reachable ? "✓" : "⚠"} ${esc(t.message || "")}</span>` + h;
        if (okBox && t.reachable) { okBox.textContent = "Domain bound and verified"; okBox.classList.remove("hidden"); }
        if (errBox && !t.reachable) errBox.textContent = t.message || "";
      };
      document.querySelector("#domain-form")?.addEventListener("submit", async (e) => {
        e.preventDefault();
        const bindBtn = document.querySelector("#bind-domain-btn");
        const domainInput = document.querySelector("#domain-form input[name='domain']");
        const domain = domainInput?.value?.trim() || "";
        
        if (!domain) {
          if (linkErr) linkErr.textContent = "Domain is required";
          return;
        }

        try {
          bindBtn.disabled = true;
          bindBtn.textContent = "Binding...";
          bindBtn.setAttribute("aria-busy", "true");
          
          const res = await api(`/api/projects/${mainProj.id}/domain`, { method: "POST", body: JSON.stringify({ domain: domain, enabled: true }) });
          const sslInput = e.target.querySelector("input[readonly]");
          if (sslInput && res.ssl_status) sslInput.value = res.ssl_status;
          paintDomTest(document.querySelector("#domain-test-result"), document.querySelector("#linkok"), document.querySelector("#linkerr"), res.test);
          toast("Domain bound");
        } catch (ex) { 
          if (linkErr) linkErr.textContent = ex.message;
        } finally {
          bindBtn.disabled = false;
          bindBtn.textContent = "Bind";
          bindBtn.removeAttribute("aria-busy");
        }
      });

      // Test domain connection
      document.querySelector("#test-domain-btn")?.addEventListener("click", async () => {
        const testBtn = document.querySelector("#test-domain-btn");
        const domainInput = document.querySelector("#domain-form input[name='domain']");
        const domain = domainInput?.value?.trim() || "";
        const testResult = document.querySelector("#domain-test-result");
        const linkOk = document.querySelector("#linkok");
        
        if (!domain) {
          if (linkErr) linkErr.textContent = "Enter a domain to test";
          return;
        }

        try {
          testBtn.disabled = true;
          testBtn.textContent = "Testing...";
          testBtn.setAttribute("aria-busy", "true");
          if (testResult) testResult.textContent = "Testing domain connection...";
          if (linkOk) linkOk.classList.add("hidden");
          if (linkErr) linkErr.textContent = "";

          // Test domain by trying to fetch from it
          const testUrl = mainProj.ssl_status === "active" ? `https://${domain}` : `http://${domain}`;
          
          try {
            const controller = new AbortController();
            const timeoutId = setTimeout(() => controller.abort(), 10000); // 10 second timeout
            
            const response = await fetch(testUrl, {
              method: 'HEAD',
              mode: 'no-cors',
              signal: controller.signal
            });
            
            clearTimeout(timeoutId);
            
            // In no-cors mode we can't read the response, but if it doesn't throw, the domain is reachable
            if (testResult) {
              testResult.innerHTML = `<span style="color: green">✓ Domain ${domain} is reachable and responding</span>`;
            }
            if (linkOk) {
              linkOk.textContent = "Domain test passed - connection successful";
              linkOk.classList.remove("hidden");
            }
          } catch (fetchError) {
            // Even with no-cors, CORS errors might occur, but the domain might still be valid
            // Let's try a different approach - check DNS resolution via the server
            try {
              const dnsResult = await api(`/api/proxy/test-domain`, { 
                method: "POST", 
                body: JSON.stringify({ domain: domain }) 
              });
              
              if (dnsResult.reachable) {
                if (testResult) {
                  testResult.innerHTML = `<span style="color: green">✓ Domain ${domain} DNS resolution successful - ${dnsResult.ip || 'resolved'}</span>`;
                }
                if (linkOk) {
                  linkOk.textContent = "Domain DNS test passed - can be bound";
                  linkOk.classList.remove("hidden");
                }
              } else {
                if (testResult) {
                  testResult.innerHTML = `<span style="color: orange">⚠ Domain ${domain} exists but may not point to this server</span>`;
                }
                if (linkErr) linkErr.textContent = "Domain exists but check DNS settings";
              }
            } catch (apiError) {
              // If API endpoint doesn't exist, provide basic feedback
              if (testResult) {
                testResult.innerHTML = `<span style="color: orange">⚠ Could not verify domain - ensure DNS points to this server IP</span>`;
              }
              if (linkErr) linkErr.textContent = "Domain verification failed - check DNS configuration";
            }
          }
        } catch (ex) {
          if (testResult) {
            testResult.innerHTML = `<span style="color: red">✗ Domain test failed: ${ex.message}</span>`;
          }
          if (linkErr) linkErr.textContent = ex.message;
        } finally {
          testBtn.disabled = false;
          testBtn.textContent = "Test";
          testBtn.removeAttribute("aria-busy");
        }
      });
      bindAction(document.querySelector("#clear-domain"), async () => {
        await api(`/api/projects/${mainProj.id}/domain`, { method: "POST", body: JSON.stringify({ domain: "", enabled: false }) });
        state.showNetPanel = false;
        render();
      });
      // background room exec — survives refresh, output only after done
      const roomExecForm = document.querySelector("#room-exec-form");
      const roomExecBtn = document.querySelector("#room-exec-btn");
      const roomExecOut = document.querySelector("#room-exec-out");
      const roomExecCmd = document.querySelector("#room-cmd");
      if(roomExecForm && roomExecBtn){
        roomExecForm.addEventListener("submit", (e)=>{
          e.preventDefault();
          runExec(roomExecBtn, "room", id, roomExecCmd.value.trim(), roomExecOut);
        });
        resumeExecFromStorage(roomExecBtn, roomExecOut, "room", id);
      }

      // room backup on manage tab (icon button + status line)
      const roomBkBtn = document.querySelector(`[data-bk-room="${id}"]`);
      if(roomBkBtn){
        const key = "room:" + id;
        const stEl = () => document.querySelector(`[data-bk-status="${id}"]`);
        const isBusy = (state._bkTrack && state._bkTrack[key]) || localStorage.getItem("vpsm_bk_" + key) === "running";
        if(isBusy){
          roomBkBtn.disabled = true;
          roomBkBtn.classList.add("busy");
          roomBkBtn.setAttribute("aria-busy", "true");
          const s = stEl(); if (s) s.textContent = "Backing up…";
          startBkTrack(key, roomBkBtn, id, false);
        }
        api("/api/backup/files").then(res=>{
          const files = res.files || [];
          updateBkFileRow(id, false, files);
          const f = files.find(x => (x.kind === "room" && (x.name === id + ".zip" || x.room_id === id)));
          if(roomBkBtn && !roomBkBtn.classList.contains("busy")){
            roomBkBtn.title = f ? "Replace backup" : "Backup now";
            roomBkBtn.setAttribute("aria-label", roomBkBtn.title);
          }
        }).catch(()=>{});
        api("/api/backup/status").then(res=>{
          const j = (res.jobs || {})[key];
          if(j && j.state === "running"){
            startBkTrack(key, roomBkBtn, id, false);
          }
        }).catch(()=>{});
        roomBkBtn.onclick = async () => {
          if (roomBkBtn.disabled || roomBkBtn.classList.contains("busy")) return;
          roomBkBtn.disabled = true;
          roomBkBtn.classList.add("busy");
          roomBkBtn.setAttribute("aria-busy", "true");
          const s = stEl(); if (s) s.textContent = "Starting…";
          try {
            try { localStorage.setItem("vpsm_bk_" + key, "running"); } catch {}
            await api("/api/backup/room", { method: "POST", body: JSON.stringify({ room_id: id }) });
            toast("Room backup started — running in background");
            startBkTrack(key, roomBkBtn, id, false);
          } catch (ex) {
            try { localStorage.removeItem("vpsm_bk_" + key); } catch {}
            roomBkBtn.disabled = false;
            roomBkBtn.classList.remove("busy");
            roomBkBtn.removeAttribute("aria-busy");
            const s2 = stEl(); if (s2) s2.textContent = "";
            toast(ex.message || "Backup failed");
          }
        };
      }

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
    bindAction(document.querySelector("#wipe-all-data"), async () => {
      if (!confirm("Delete ALL volume contents in this room? .env files are kept. Containers restart.")) return;
      try {
        const res = await api(`/api/rooms/${id}/volumes/wipe-all`, { method: "POST", body: "{}" });
        const n = (res.wiped || []).length;
        toast(`Wiped ${n} volume${n === 1 ? "" : "s"} — .env kept${res.docker_available ? "" : " (no Docker: restart on host)"}`);
        render();
      } catch (ex) { toast(ex.message || "Wipe failed"); }
    });
    document.querySelectorAll("[data-vol-clean]").forEach((btn) => {
      btn.addEventListener("click", async (ev) => {
        if (ev && typeof ev.stopPropagation === 'function') ev.stopPropagation();
        if (ev) ev.preventDefault();
        if (btn.disabled || btn.classList.contains("busy")) return;
        const vid = btn.getAttribute("data-vol-clean");
        if (!vid || !confirm("Delete all files inside this volume? The volume mount is kept.")) return;
        btn.classList.add("busy"); btn.disabled = true; btn.setAttribute("aria-busy","true");
        try {
          await api(`/api/rooms/${id}/volumes/${encodeURIComponent(vid)}/clean`, { method: "POST", body: "{}" });
          toast("Volume cleaned");
          render();
        } catch (ex) { toast(ex.message || "Clean failed"); }
        finally { btn.classList.remove("busy"); btn.removeAttribute("aria-busy"); btn.disabled=false; }
      });
    });
    document.querySelectorAll("tr.vol-row").forEach((tr) => {
      tr.addEventListener("click", () => {
        setView("room", { roomTab: "volume", volId: tr.dataset.vid, filePath: "/" });
      });
    });
    if (tab === "volume") {
      document.querySelector("#back-vols")?.addEventListener("click", () => setView("room", { roomTab: "volumes" }));
      document.querySelectorAll("[data-vol-path]").forEach((a) => a.onclick = (e) => {
        e.preventDefault();
        setView("room", { roomTab: "volume", volId: state.volId, filePath: a.dataset.volPath });
      });
      document.querySelector("#updir")?.addEventListener("click", () => {
        const parts = (state.filePath || "/").split("/").filter(Boolean);
        parts.pop();
        setView("room", { roomTab: "volume", volId: state.volId, filePath: "/" + parts.join("/") });
      });
      document.querySelector("#backfiles")?.addEventListener("click", () => {
        const parts = (state.filePath || "/").split("/").filter(Boolean);
        parts.pop();
        setView("room", { roomTab: "volume", volId: state.volId, filePath: "/" + parts.join("/") });
      });
    }
    document.querySelectorAll("tr.ctr-row").forEach((tr) => {
      tr.addEventListener("click", (e) => {
        if (e.target.closest(".copyable")) return;
        setView("room", { roomTab: "container", ctrId: tr.dataset.cid, ctrTab: "files", filePath: "/" });
      });
    });
    if (tab === "container") {
      document.querySelector("#back-ctrs")?.addEventListener("click", () => setView("room", { roomTab: "overview" }));
      document.querySelectorAll("[data-ctr-tab]").forEach((b) => {
        b.onclick = () => setView("room", { roomTab: "container", ctrId: state.ctrId, ctrTab: b.dataset.ctrTab, filePath: state.filePath || "/" });
      });
      document.querySelectorAll("[data-path]").forEach((a) => a.onclick = (e) => {
        e.preventDefault();
        setView("room", { roomTab: "container", ctrId: state.ctrId, ctrTab: "files", filePath: a.dataset.path });
      });
      document.querySelector("#updir")?.addEventListener("click", () => {
        const p = state.filePath || "/";
        const parts = p.split("/").filter(Boolean);
        parts.pop();
        setView("room", { roomTab: "container", ctrId: state.ctrId, ctrTab: "files", filePath: "/" + parts.join("/") });
      });
      document.querySelector("#backfiles")?.addEventListener("click", () => {
        const p = state.filePath || "/";
        const parts = p.split("/").filter(Boolean);
        parts.pop();
        setView("room", { roomTab: "container", ctrId: state.ctrId, ctrTab: "files", filePath: "/" + parts.join("/") });
      });
      bindAction(document.querySelector("#savefile"), async () => {
        await api(`/api/rooms/${id}/containers/${encodeURIComponent(state.ctrId)}/files?path=${encodeURIComponent(state.filePath || "/")}`, {
          method: "PUT", body: JSON.stringify({ content: document.querySelector("#fedit").value }),
        });
      });
      bindAction(document.querySelector("#delfile"), async () => {
        if (!confirm("Delete file?")) return;
        await api(`/api/rooms/${id}/containers/${encodeURIComponent(state.ctrId)}/files?path=${encodeURIComponent(state.filePath || "/")}`, { method: "DELETE" });
        const parts = (state.filePath || "/").split("/").filter(Boolean);
        parts.pop();
        setView("room", { roomTab: "container", ctrId: state.ctrId, ctrTab: "files", filePath: "/" + parts.join("/") });
      });
      const rlog = document.querySelector("#rlog");
      if (rlog) {
        pinLogBottom(rlog);
        document.querySelector("#copylog")?.addEventListener("click", () => copyText(rlog.innerText || ""));
        document.querySelector("#reflog")?.addEventListener("click", () => render());
        startLogLive(async () => {
          if (state.view !== "room" || state.roomTab !== "container" || state.ctrTab !== "logs") {
            stopLogLive();
            return;
          }
          try {
            const lg = await api(`/api/rooms/${id}/logs?container=${encodeURIComponent((containers.find((c) => c.id === state.ctrId) || {}).docker_id || state.ctrId || "")}`);
            const el = document.querySelector("#rlog");
            if (!el) return;
            const html = formatLogHTML(lg.log || "(empty)");
            if (el.innerHTML !== html) {
              el.innerHTML = html;
              pinLogBottom(el);
            }
          } catch {}
        });
      }
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
      document.querySelectorAll("[data-log-ctr]").forEach((b) => {
        b.onclick = () => {
          state.roomLogCtr = b.dataset.logCtr;
          state._logPinBottom = true;
          render();
        };
      });
      document.querySelector("#copylog")?.addEventListener("click", () => copyText(document.querySelector("#rlog")?.innerText || ""));
      document.querySelector("#reflog")?.addEventListener("click", () => {
        state._logPinBottom = true;
        render();
      });
      document.querySelector("#clear-room-log")?.addEventListener("click", async () => {
        if (!await confirmAction({
          title: "Clear container log?",
          body: "This will truncate the current container log. Are you sure?",
          ok: "Clear",
          danger: true,
        })) return;
        const cid = state.roomLogCtr || document.querySelector("#log-ctrs [data-log-ctr]")?.getAttribute("data-log-ctr") || "";
        await api(`/api/rooms/${id}/logs?clear=1&container=${encodeURIComponent(cid)}`, { method: "POST" });
        toast("Log cleared");
        const el = document.querySelector("#rlog");
        if (el) el.innerHTML = "";
        render();
      });
      startLogLive(async () => {
        if (state.view !== "room" || (state.roomTab || "overview") !== "logs") {
          stopLogLive();
          return;
        }
        try {
          const cid = state.roomLogCtr || document.querySelector("#log-ctrs [data-log-ctr]")?.getAttribute("data-log-ctr") || "";
          if (!cid) return;
          if (!state.roomLogCtr) state.roomLogCtr = cid;
          const lg = await api(`/api/rooms/${id}/logs?container=${encodeURIComponent(cid)}`);
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
    if (tab === "env") {
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
        await api(`/api/rooms/${id}/env`, { method: "PUT", body: JSON.stringify({ content }) });
        document.querySelector("#envok").classList.remove("hidden");
        setTimeout(() => document.querySelector("#envok")?.classList.add("hidden"), 1500);
      });
    }

    if (deploying && state.view === "room" && state.roomId === id) {
      window.clearTimeout(state._roomPoll);
      state._roomPoll = window.setTimeout(() => {
        if (state.view === "room" && state.roomId === id) renderRoom();
      }, 2500);
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

  function closeSSHTerm() {
    if (state.sshWS) { try { state.sshWS.close(); } catch {} state.sshWS = null; }
  }

  function termWrite(scr, data) {
    let i = 0;
    const put = (ch) => {
      let line = scr.lines[scr.r] || "";
      if (scr.c > line.length) line = line + " ".repeat(scr.c - line.length);
      scr.lines[scr.r] = line.slice(0, scr.c) + ch + line.slice(scr.c + 1);
      scr.c++;
    };
    while (i < data.length) {
      const ch = data[i];
      if (ch === "\x1b" && data[i + 1] === "[") {
        let j = i + 2;
        while (j < data.length && !/[A-Za-z]/.test(data[j])) j++;
        const fin = data[j] || "";
        const raw = data.slice(i + 2, j);
        const nums = raw.replace(/[?=]/g, "").split(";").map((x) => parseInt(x, 10) || 0);
        if (fin === "m" || fin === "l" || fin === "h") {}
        else if (fin === "J") { if (raw.indexOf("2") >= 0) { scr.lines = [""]; scr.r = 0; scr.c = 0; } }
        else if (fin === "H" || fin === "f") {
          scr.r = Math.max(0, (nums[0] || 1) - 1); scr.c = Math.max(0, (nums[1] || 1) - 1);
          while (scr.lines.length <= scr.r) scr.lines.push("");
        }
        else if (fin === "K") { const ln = scr.lines[scr.r] || ""; scr.lines[scr.r] = ln.slice(0, scr.c); }
        else if (fin === "A") scr.r = Math.max(0, scr.r - (nums[0] || 1));
        else if (fin === "B") { scr.r = scr.r + (nums[0] || 1); while (scr.lines.length <= scr.r) scr.lines.push(""); }
        else if (fin === "C") scr.c = scr.c + (nums[0] || 1);
        else if (fin === "D") scr.c = Math.max(0, scr.c - (nums[0] || 1));
        else if (fin === "G") scr.c = Math.max(0, (nums[0] || 1) - 1);
        i = j + 1; continue;
      }
      if (ch === "\r") { scr.c = 0; i++; continue; }
      if (ch === "\n") {
        scr.r++; scr.c = 0;
        if (scr.lines.length <= scr.r) scr.lines.push("");
        if (scr.lines.length > 800) { scr.lines.splice(0, scr.lines.length - 800); scr.r = 799; }
        i++; continue;
      }
      if (ch === "\x08" || ch === "\x7f") { scr.c = Math.max(0, scr.c - 1); i++; continue; }
      if (ch === "\t") { const n = 8 - (scr.c % 8); for (let k = 0; k < n; k++) put(" "); i++; continue; }
      const code = ch.charCodeAt(0);
      if (code < 32 || code === 127) { i++; continue; }
      put(ch); i++;
    }
  }

  function renderTermScreen(scr) {
    const el = document.querySelector("#ssh-term-body");
    if (!el) return;
    const base = Math.max(0, scr.lines.length - 200);
    const view = scr.lines.slice(base);
    const relR = scr.r - base;
    let html = "";
    view.forEach((ln, idx) => {
      let row;
      if (idx === relR) {
        const c = Math.min(scr.c, ln.length);
        row = esc(ln.slice(0, c)) + `<span class="tcur">${esc(ln[c] || " ")}</span>` + esc(ln.slice(c + 1));
      } else {
        row = esc(ln) || " ";
      }
      html += `<div class="tline">${row}</div>`;
    });
    el.innerHTML = html;
    const wrap = document.querySelector("#ssh-term-scroll");
    if (wrap) wrap.scrollTop = wrap.scrollHeight;
  }

  function openSSHTerm() {
    closeSSHTerm();
    if (!document.querySelector("#ssh-term")) return;
    const scr = { lines: [""], r: 0, c: 0 };
    state.sshScr = scr;
    const proto = location.protocol === "https:" ? "wss" : "ws";
    let ws;
    try { ws = new WebSocket(`${proto}://${location.host}/api/ssh/terminal/ws`); }
    catch { return; }
    state.sshWS = ws;
    ws.onopen = () => {
      try {
        const w = document.querySelector("#ssh-term-scroll")?.clientWidth || 800;
        ws.send(JSON.stringify({ type: "resize", cols: Math.max(40, Math.min(200, Math.floor(w / 8))), rows: 30 }));
      } catch {}
    };
    ws.onmessage = (ev) => {
      try {
        const m = JSON.parse(ev.data);
        if (m.type === "output" && typeof m.data === "string") {
          termWrite(scr, m.data);
          renderTermScreen(scr);
        }
      } catch {}
    };
    ws.onclose = () => {
      if (state.sshWS === ws) state.sshWS = null;
      const el = document.querySelector("#ssh-term-body");
      if (el) el.insertAdjacentHTML("beforeend", `<div class="tline muted">— disconnected —</div>`);
    };
    const box = document.querySelector("#ssh-term");
    const send = (s) => { try { if (ws.readyState === 1) ws.send(JSON.stringify({ type: "input", data: s })); } catch {} };
    box.onkeydown = (e) => {
      if (e.ctrlKey || e.metaKey) {
        const k = (e.key || "").toLowerCase();
        if (k === "c") { send("\x03"); e.preventDefault(); return; }
        if (k === "d") { send("\x04"); e.preventDefault(); return; }
        if (k === "l") { send("\x0c"); e.preventDefault(); return; }
        return;
      }
      if (e.key && e.key.length === 1) { send(e.key); e.preventDefault(); }
      else if (e.key === "Enter") { send("\r"); e.preventDefault(); }
      else if (e.key === "Backspace") { send("\x7f"); e.preventDefault(); }
      else if (e.key === "Tab") { send("\t"); e.preventDefault(); }
      else if (e.key === "Escape") { send("\x1b"); e.preventDefault(); }
      else if (e.key === "ArrowUp") { send("\x1b[A"); e.preventDefault(); }
      else if (e.key === "ArrowDown") { send("\x1b[B"); e.preventDefault(); }
      else if (e.key === "ArrowRight") { send("\x1b[C"); e.preventDefault(); }
      else if (e.key === "ArrowLeft") { send("\x1b[D"); e.preventDefault(); }
      else if (e.key === "Delete") { send("\x1b[3~"); e.preventDefault(); }
      else if (e.key === "Home") { send("\x1b[H"); e.preventDefault(); }
      else if (e.key === "End") { send("\x1b[F"); e.preventDefault(); }
    };
    box.onpaste = (e) => {
      try {
        const tx = (e.clipboardData || window.clipboardData).getData("text");
        if (tx) send(tx);
      } catch {}
      e.preventDefault();
    };
    document.querySelector("#ssh-run")?.addEventListener("submit", (e) => {
      e.preventDefault();
      const inp = document.querySelector("#ssh-cmd");
      const cmd = String(inp?.value || "");
      if (!cmd) return;
      send(cmd + "\r");
      inp.value = "";
      box.focus();
    });
    setTimeout(() => { try { box.focus({ preventScroll: true }); } catch { box.focus(); } }, 300);
  }

  function renderColorTree(list) {
    const b = (s) => `<span class="tree-branch">${esc(s)}</span>`;
    const c = (s) => `<span class="tree-comment">${esc(s)}</span>`;
    const core = (s) => `<span class="tree-core">${esc(s)}</span>`;
    let out = `<span class="tree-root">/vps-manager/</span>\n`;
    out += `${b("├── ")}${core("bin/")}               ${c("# Core binaries (vps-rooms, vr)")}\n`;
    out += `${b("├── ")}${core("data/")}              ${c("# Database & logs (/vps-manager/data/logs)")}\n`;
    out += `${b("├── ")}${core("proxy/")}             ${c("# Caddy & Nginx proxies")}\n`;
    out += `${b("├── ")}${core("x5coder-agent/")}\n`;
    out += `${b("├── ")}${core("backup/")}            ${c("# Global archive (/vps-manager/backup/vps-manager.zip)")}\n`;

    if (list && list.length) {
      list.forEach((p) => {
        const isMulti = p.kind === "multi";
        const dir = isMulti ? "multi" : "single";
        const codeDir = isMulti ? "stack" : "project";
        const roomCls = isMulti ? "tree-multi" : "tree-single";
        const name = p.name || String(p.room_id).slice(0, 8);
        out += `${b("├── ")}<span class="${roomCls}">${esc(dir)}/${esc(p.room_id)}/</span>   ${c(`# ${name} (${p.kind})`)}\n`;
        out += `${b("│   ├── ")}<span class="tree-code">${esc(codeDir)}/</span>    ${c("# App code & ")}<span class="tree-env">.env</span>\n`;
        out += `${b("│   ├── ")}<span class="tree-vol">volumes/</span>    ${c("# Persistent data")}\n`;
        out += `${b("│   ├── ")}<span class="tree-core">config/</span>\n`;
        out += `${b("│   └── ")}<span class="tree-zip">backup/</span>     ${c(`# ${p.room_id}.zip`)}\n`;
      });
    } else {
      out += `${b("└── ")}${c("(No active rooms yet)")}\n`;
    }
    return out;
  }

  function buildAIGuide(st, list, rootPass) {
    const conn = st?.connect || "ssh root@13.140.164.29 -p 22";
    const port = st?.port || 22;
    const pass = rootPass || "kareem1234";
    const hostIP = conn.match(/@([\d.]+)/)?.[1] || "13.140.164.29";

    const activeRooms = (list || []).filter((r) => r.room_id);
    const sample = activeRooms[0] || {
      room_id: "28405a88-3a0c-42f8-9528-8040eabb4357",
      name: "shop-bot",
      kind: "single",
      work_dir: "/vps-manager/single/28405a88-3a0c-42f8-9528-8040eabb4357/project",
      env_path: "/vps-manager/single/28405a88-3a0c-42f8-9528-8040eabb4357/project/.env",
      volumes: "/vps-manager/single/28405a88-3a0c-42f8-9528-8040eabb4357/volumes",
    };
    const sId = sample.room_id;
    const sName = sample.name || "shop-bot";
    const sKind = sample.kind === "multi" ? "multi" : "single";
    const sCode = sample.kind === "multi" ? "stack" : "project";
    const sWork = sample.work_dir || `/vps-manager/${sKind}/${sId}/${sCode}`;
    const sEnv = sample.env_path || `${sWork}/.env`;
    const sVol = sample.volumes || `/vps-manager/${sKind}/${sId}/volumes`;

    let treeRooms = "";
    (list || []).forEach((r) => {
      const dir = r.kind === "multi" ? "multi" : "single";
      const codeDir = r.kind === "multi" ? "stack" : "project";
      treeRooms += `│   ├── ${r.room_id}/ (${r.name || "app"} · ${dir})\n`;
      treeRooms += `│   │   ├── ${codeDir}/   # Application source code & files\n`;
      treeRooms += `│   │   │   └── .env      # Environment variables & secrets (protected from wipe)\n`;
      treeRooms += `│   │   ├── volumes/      # Persistent application data\n`;
      treeRooms += `│   │   ├── config/       # Room configuration\n`;
      treeRooms += `│   │   └── backup/       # <room_id>.zip (full room archive)\n`;
    });
    return `# VPS Manager — AI Architecture & Operations Handbook

This document defines the strict filesystem hierarchy, operational rules, and command patterns for VPS Manager.
Any AI assistant or engineer interacting with this server must adhere strictly to these rules.

---

## 0. Direct SSH Access & Authentication Credentials

To connect directly to this VPS as root from terminal, PuTTY, or remote agents:
- **SSH Command**: \`${conn}\`
- **Host**: \`13.140.164.29\`
- **Port**: \`${port}\`
- **User**: \`root\`
- **Password**: \`${pass}\`

Direct terminal login:
\`\`\`bash
${conn}
# When prompted, paste the root password: ${pass}
\`\`\`

---

## 1. Core Architecture & Strict Isolation Rules

All projects and manager components are strictly isolated under \`/vps-manager/\`.
NEVER place application code or configuration in arbitrary host paths (such as \`/root\`, \`/opt\`, or \`/var\`).

### Canonical Directory Layout:
\`\`\`
/vps-manager/
├── bin/
├── data/
│   ├── database.sqlite
│   ├── sessions/
│   └── logs/
├── proxy/
├── x5coder-agent/
├── backup/
│   └── vps-manager.zip
├── single/
│   └── <room_id>/
│       ├── project/
│       ├── container/
│       ├── volumes/
│       │   ├── <volume_name>/
│       │   └── ...
│       ├── config/
│       ├── logs/
│       └── backup/
│           └── <room_id>.zip
└── multi/
    └── <room_id>/
        ├── project/
        ├── stack/
        │   ├── docker-compose.yml
        │   └── ...
        ├── containers/
        │   ├── <container_id>/
        │   └── ...
        ├── volumes/
        │   ├── <volume_name>/
        │   └── ...
        ├── config/
        ├── logs/
        │   ├── <container_id>/
        │   └── ...
        └── backup/
            └── <room_id>.zip
\`\`\`

---

## 2. Current Live VPS Tree & Active Projects
SSH Host: \`${conn}\`
Active rooms currently running on this VPS:
\`\`\`
/vps-manager/
${treeRooms || "│   └── (No active rooms yet)\n"}
\`\`\`

---

## 3. Real Active Example on this Server (${sName} · ID: \`${sId}\`)

### A. Inspect project files & environment:
\`\`\`bash
# 1. Navigate to the active project directory on this VPS:
cd ${sWork}

# 2. View directory contents:
ls -la

# 3. View current environment secrets (.env):
cat ${sEnv}

# 4. View persistent volumes:
ls -la ${sVol}
\`\`\`

### B. Inspect container status & logs:
\`\`\`bash
# List all containers managed by VPS Manager:
docker ps --filter "label=vps-rooms=1"

# View logs for this project:
docker logs --tail 100 -f ${sName}
\`\`\`

---

## 4. How to Create a New Project

Always follow this exact 4-step lifecycle when adding a project:

### Step 1: Create the Room (Define Single or Multi)
- Via Panel: Click "Add project", enter name, choose "single" or "multi", set quota.
- Or via Local API:
\`\`\`bash
curl -s -X POST http://127.0.0.1:9090/api/rooms \\
  -H "Content-Type: application/json" \\
  -d '{"name":"my-service","kind":"single","quota_gb":5}'
\`\`\`
- Or via External API:
\`\`\`bash
curl -s -X POST http://13.140.164.29:9090/api/rooms \\
  -H "Content-Type: application/json" \\
  -d '{"name":"my-service","kind":"single","quota_gb":5}'
\`\`\`
- Or via CLI:
\`\`\`bash
/vps-manager/bin/vr room create --name my-service --kind single --quota 5
\`\`\`

### Step 2: Upload / Place Project Code in the Canonical Path
- Via Panel: After room creation, the panel provides upload UI (tar.gz) or direct text editing.
- Via SSH (Auto-Detected): Create room structure directly on filesystem — auto-detected within 10 seconds:
\`\`\`bash
# Create Single Room (Auto-Detected)
mkdir -p /vps-manager/single/my-new-room/project
echo "hashed_password_here" > /vps-manager/single/my-new-room/auth.hash
# Within 10 seconds: appears in panel as "room-my-new-room"

# Create Multi Room (Auto-Detected)
mkdir -p /vps-manager/multi/my-compose-room/stack
echo "hashed_password_here" > /vps-manager/multi/my-compose-room/auth.hash
# Within 10 seconds: appears in panel with stack/ directory ready for docker-compose.yml
\`\`\`
- Required for SSH creation: Only \`auth.hash\` file with hashed password. Everything else (NAME, vault.bin, directories) is auto-created.
- Manual scan trigger: \`curl -s -X POST http://127.0.0.1:9090/api/rooms/scan\`

### Step 2: Upload / Place Project Code in the Canonical Path
- For Single Room:
\`\`\`bash
cd /vps-manager/single/<room_id>/project
\`\`\`
- For Multi Room:
\`\`\`bash
cd /vps-manager/multi/<room_id>/stack
\`\`\`

### Step 3: Configure Environment Variables (.env)
\`\`\`bash
cat << 'EOF' > /vps-manager/single/<room_id>/project/.env
PORT=8080
NODE_ENV=production
EOF
\`\`\`

### Step 4: Build / Start the Room
\`\`\`bash
/vps-manager/bin/vr room restart <room_id>
\`\`\`

---

## 5. How to Update or Replace an Existing Project

### A. Real Safe Code Update (${sName}):
\`\`\`bash
cd ${sWork} && git pull && /vps-manager/bin/vr room restart ${sId}
\`\`\`

### B. Complete Project Code Replacement:
\`\`\`bash
cd ${sWork}

# Backup current .env before replacing
cp .env /tmp/temp_${sId}.env 2>/dev/null

# Remove old source code files (NEVER delete .env or volumes)
find . -mindepth 1 ! -name ".env" -exec rm -rf {} +

# Extract new release
tar -xzf /tmp/new_release.tar.gz -C .

# Restore .env if needed
[ -f /tmp/temp_${sId}.env ] && cp /tmp/temp_${sId}.env .env

# Restart container
/vps-manager/bin/vr room restart ${sId}
\`\`\`

---

## 6. How to Delete a Project (Real Server Example)

To safely delete room **${sName}** (\`${sId}\`) and release resources:
\`\`\`bash
# Via Local API:
curl -s -X DELETE http://127.0.0.1:9090/api/rooms/${sId}

# Or via External IP:
curl -s -X DELETE http://13.140.164.29:9090/api/rooms/${sId}

# Or via CLI:
/vps-manager/bin/vr room delete ${sId}
\`\`\`

---

## 7. Critical Safety & Integrity Rules for AI
1. **Never delete .env**: Project secrets must be preserved across data wipes and updates.
2. **Volumes isolation**: Application data resides in volumes/ and is never mixed with code.
3. **Canonical paths only**: No projects outside /vps-manager/single/ or /vps-manager/multi/.
4. **Log paths**: Unified logs are in /vps-manager/data/logs/.

---

## 8. COMPLETE ROOM MANAGEMENT COMMANDS

### LIST ALL ROOMS:
\`\`\`bash
# Get all rooms with IDs, names, and status
curl -s http://127.0.0.1:9090/api/rooms | jq '.[] | {id, name, kind, quota_bytes, usage_bytes}'

# Via database
sqlite3 /vps-manager/data/panel.db "SELECT id, name, kind, created_at FROM rooms;"

# Via filesystem
ls -la /vps-manager/single/
ls -la /vps-manager/multi/
\`\`\`

### ROOM CONTROL OPERATIONS:
\`\`\`bash
# START ROOM
curl -s -X POST http://127.0.0.1:9090/api/rooms/${sId}/start
/vps-manager/bin/vr room start ${sId}

# STOP ROOM  
curl -s -X POST http://127.0.0.1:9090/api/rooms/${sId}/stop
/vps-manager/bin/vr room stop ${sId}

# RESTART ROOM
curl -s -X POST http://127.0.0.1:9090/api/rooms/${sId}/restart
/vps-manager/bin/vr room restart ${sId}

# PAUSE ROOM
curl -s -X POST http://127.0.0.1:9090/api/rooms/${sId}/pause

# RESUME ROOM
curl -s -X POST http://127.0.0.1:9090/api/rooms/${sId}/resume
\`\`\`

### ROOM MODIFICATION:
\`\`\`bash
# CHANGE ROOM NAME
echo "New Room Name" > /vps-manager/${sKind}/${sId}/NAME

# CHANGE ROOM PASSWORD (generate hash first)
NEW_HASH=$(/vps-manager/bin/vr room hash --password "new_password")
echo "\$NEW_HASH" > /vps-manager/${sKind}/${sId}/auth.hash

# CHANGE ROOM QUOTA
curl -s -X PUT http://127.0.0.1:9090/api/rooms/${sId} \\
  -H "Content-Type: application/json" \\
  -d '{"quota_bytes": 10737418240}'  # 10GB
\`\`\`

### ROOM MONITORING:
\`\`\`bash
# DISK USAGE
curl -s http://127.0.0.1:9090/api/rooms/${sId} | jq '{usage_bytes, quota_bytes, footprint_bytes}'
du -sh /vps-manager/${sKind}/${sId}

# RESOURCE USAGE
curl -s http://127.0.0.1:9090/api/metrics
docker stats --no-stream --format "table {{.Container}}\\t{{.CPUPerc}}\\t{{.MemUsage}}" $(docker ps -q --filter "label=vps-rooms.room=${sId}")

# LOGS
tail -n 100 -f /vps-manager/data/logs/rooms/${sId}.log
docker logs --tail 100 -f $(docker ps -q --filter "label=vps-rooms.room=${sId}")
\`\`\`

### DELETE ROOM COMPLETELY:
\`\`\`bash
# ⚠️ IRREVERSIBLE - Use with caution
curl -s -X DELETE http://127.0.0.1:9090/api/rooms/${sId}
/vps-manager/bin/vr room delete ${sId}

# Manual cleanup if API fails
docker stop $(docker ps -q --filter "label=vps-rooms.room=${sId}")
docker rm $(docker ps -aq --filter "label=vps-rooms.room=${sId}")
sqlite3 /vps-manager/data/panel.db "DELETE FROM rooms WHERE id='${sId}';"
rm -rf /vps-manager/${sKind}/${sId}
docker network rm vpsrooms_${sId.slice(0, 8)}
\`\`\`

### FILESYSTEM ACCESS:
\`\`\`bash
# Navigate to room
cd ${sWork}  # Single: /vps-manager/single/${sId}/project
              # Multi:  /vps-manager/multi/${sId}/stack

# View environment
cat ${sEnv}

# Browse volumes
ls -la ${sVol}

# Edit .env (protected from deletion)
nano /vps-manager/${sKind}/${sId}/${sCode}/.env
\`\`\`

### BACKUP & RESTORE:
\`\`\`bash
# Create backup
curl -s -X POST http://127.0.0.1:9090/api/rooms/${sId}/backup
cd /vps-manager/${sKind}/${sId} && tar -czf backup/${sId}-manual-$(date +%Y%m%d).tar.gz project/ volumes/ config/ .env

# List backups
curl -s http://127.0.0.1:9090/api/backup
ls -la /vps-manager/${sKind}/${sId}/backup/

# Restore from backup
curl -s -X POST http://127.0.0.1:9090/api/rooms/${sId}/restore \\
  -H "Content-Type: application/json" \\
  -d '{"backup_file": "backup-file.zip"}'
\`\`\`

---

## 9. API COMMAND REFERENCE

### Room Management:
- \`POST /api/rooms\` - Create room
- \`GET /api/rooms\` - List all rooms  
- \`GET /api/rooms/{id}\` - Get room details
- \`PUT /api/rooms/{id}\` - Update room
- \`DELETE /api/rooms/{id}\` - Delete room
- \`POST /api/rooms/{id}/start\` - Start room
- \`POST /api/rooms/{id}/stop\` - Stop room
- \`POST /api/rooms/{id}/restart\` - Restart room
- \`POST /api/rooms/{id}/pause\` - Pause room
- \`POST /api/rooms/{id}/resume\` - Resume room
- \`POST /api/rooms/scan\` - Force filesystem scan

### SSH & Filesystem:
- \`GET /api/ssh/status\` - SSH connection info
- \`GET /api/vps/paths?all=1\` - All room paths
- \`GET /api/ssh/root-password\` - Root password

### Monitoring:
- \`GET /api/metrics\` - System metrics
- \`GET /api/host\` - Host information

### CLI Commands:
- \`/vps-manager/bin/vr room create\` - Create room
- \`/vps-manager/bin/vr room delete\` - Delete room
- \`/vps-manager/bin/vr room start\` - Start room
- \`/vps-manager/bin/vr room stop\` - Stop room
- \`/vps-manager/bin/vr room restart\` - Restart room
- \`/vps-manager/bin/vr room hash\` - Generate password hash

---

## 10. TROUBLESHOOTING

### Room Not Appearing After SSH Creation:
\`\`\`bash
# Force manual scan
curl -s -X POST http://127.0.0.1:9090/api/rooms/scan

# Check auth.hash exists
ls -la /vps-manager/single/<room_id>/auth.hash

# Check database
sqlite3 /vps-manager/data/panel.db "SELECT * FROM rooms WHERE id='<room_id>';"
\`\`\`

### Container Not Starting:
\`\`\`bash
# Check logs
docker logs <container_id>

# Check room status  
curl -s http://127.0.0.1:9090/api/rooms/<room_id>

# Restart room
curl -s -X POST http://127.0.0.1:9090/api/rooms/<room_id>/restart
\`\`\`

### Disk Space Issues:
\`\`\`bash
# Check room usage
du -sh /vps-manager/<kind>/<room_id>

# Clean volumes (protected .env)
curl -s -X POST http://127.0.0.1:9090/api/rooms/<room_id>/volumes/<vol_id>/clean

# Global disk usage
df -h /vps-manager
\`\`\`

---

**END OF COMPLETE OPERATIONS HANDBOOK**
All commands are optimized for AI execution with clear patterns for every operation.
`;
  }

  async function renderSSH(forceRefresh = false) {
    const gen = state._gen;
    closeSSHTerm();
    state._pageCache = state._pageCache || {};

    const paintSSH = (data) => {
      if (!alive("ssh", gen)) return;
      const { st, all, rooms, pwInfo } = data;
      state.cache.sshConnect = st.connect;
      const rootPass = (pwInfo && pwInfo.password) || "kareem1234";
      const list = (all && all.rooms) || [];

      const activeRooms = list.filter((r) => r.room_id);
      const sample = activeRooms[0] || {
        room_id: "28405a88-3a0c-42f8-9528-8040eabb4357",
        name: "shop-bot",
        kind: "single",
        work_dir: "/vps-manager/single/28405a88-3a0c-42f8-9528-8040eabb4357/project",
        env_path: "/vps-manager/single/28405a88-3a0c-42f8-9528-8040eabb4357/project/.env",
        volumes: "/vps-manager/single/28405a88-3a0c-42f8-9528-8040eabb4357/volumes",
      };
      const sId = sample.room_id;
      const sName = sample.name || "shop-bot";
      const sKind = sample.kind === "multi" ? "multi" : "single";
      const sCode = sample.kind === "multi" ? "stack" : "project";
      const sWork = sample.work_dir || `/vps-manager/${sKind}/${sId}/${sCode}`;
      const sEnv = sample.env_path || `${sWork}/.env`;

      shell(`<div id="crumb"></div>
        <div class="topbar">
          <div>
            <h2>SSH & Docs</h2>
            <div class="sub">${esc(st.connect || "")} · port ${esc(st.port)} · ${st.active ? "active" : "inactive"}</div>
          </div>
          <div class="actions">
            <button class="btn icon-btn sm action" id="ssh-refresh-btn" title="Refresh" aria-label="Refresh">${refreshIconHTML()}</button>
          </div>
        </div>

        <div class="panel"><h3>Connect via external SSH client</h3>
          <div class="fact-row"><span>SSH command</span><strong class="mono copyable" data-copy="${esc(st.connect || "")}">${esc(st.connect || "")}</strong></div>
          <div class="fact-row"><span>Port</span><strong class="mono">${esc(st.port)}</strong></div>
          <div class="fact-row"><span>Root login</span><strong class="mono">${esc(st.root_login || "")}</strong></div>
          <div class="fact-row"><span>Status</span><strong>${st.active ? "● active" : "○ inactive"}</strong></div>
          <div class="fact-row"><span>Root password</span>
            <strong class="mono"><span id="rootpw-mask">••••••••</span><span id="rootpw-val" class="hidden">${esc(rootPass)}</span>
            <button type="button" class="btn sm action" id="rootpw-btn" style="margin-inline-start:10px">Show</button>
            <button type="button" class="btn sm action hidden" id="rootpw-copy" data-copy="${esc(rootPass)}">Copy</button></strong>
          </div>
          <p class="muted" style="font-size:.82rem">From your PC terminal or PuTTY: paste the command above, then the root password.</p>
        </div>

        <div class="panel">
          <div class="head-row">
            <div>
              <h3>VPS Architecture & AI Agent Documentation</h3>
              <p class="muted" style="font-size:.82rem;margin:2px 0 0">Comprehensive guide for AI models (Claude, ChatGPT, etc.) and engineers. Explains directory rules, real room commands, and includes current live tree.</p>
            </div>
            <div class="row-actions">
              <button class="btn primary action" type="button" id="copy-ai-guide">${ico("copy", 14)} Copy Guide for AI</button>
            </div>
          </div>

          <div class="ai-handbook">
            <div class="ai-handbook-creds">
              <div class="creds-title"><span class="creds-ico">${ico("key", 14)}</span> SSH Direct Access & Credentials (Engineers & AI Models)</div>
              <div class="creds-grid">
                <div class="creds-item">
                  <span class="creds-label">SSH Command</span>
                  <strong class="creds-val mono copyable" data-copy="${esc(st.connect || "ssh root@13.140.164.29 -p 22")}">${esc(st.connect || "ssh root@13.140.164.29 -p 22")}</strong>
                </div>
                <div class="creds-item">
                  <span class="creds-label">Host & Port</span>
                  <strong class="creds-val mono">13.140.164.29 : ${esc(st.port || 22)}</strong>
                </div>
                <div class="creds-item">
                  <span class="creds-label">User</span>
                  <strong class="creds-val mono">root</strong>
                </div>
                <div class="creds-item">
                  <span class="creds-label">Root Password</span>
                  <strong class="creds-val mono copyable creds-pw" data-copy="${esc(rootPass)}">${esc(rootPass)}</strong>
                </div>
              </div>
              <div class="creds-note">💡 Copy the SSH command and password above to authenticate directly from your local terminal or AI workspace.</div>
            </div>

            <h4>🌲 Live VPS Filesystem Tree (Real active data)</h4>
            <div class="ai-code-block" style="background:#09090b;border:1px solid rgba(255,255,255,0.08);border-radius:8px;padding:14px;overflow-x:auto">
              <pre class="mono" style="margin:0;font-size:0.78rem;line-height:1.55;color:#e2e8f0">${renderColorTree(list)}</pre>
            </div>

            <div class="ai-handbook-body" style="margin-top:16px">
              <h4>🚀 Real Terminal Commands (Active Projects)</h4>
              <p class="muted" style="font-size:0.8rem">Copy and run these directly on the server to inspect files, edit secrets, or run maintenance.</p>

              <div class="ai-cmd-card">
                <div class="cmd-head">
                  <span class="cmd-badge">1</span>
                  <strong>Inspect ${esc(sName)} Project Code</strong>
                  <button type="button" class="btn sm action" data-copy="cd ${esc(sWork)} &amp;&amp; ls -la">Copy</button>
                </div>
                <div class="cmd-box mono">cd ${esc(sWork)} &amp;&amp; ls -la</div>
              </div>

              <div class="ai-cmd-card">
                <div class="cmd-head">
                  <span class="cmd-badge">2</span>
                  <strong>View Secrets &amp; Environment Variables (.env)</strong>
                  <button type="button" class="btn sm action" data-copy="cat ${esc(sEnv)}">Copy</button>
                </div>
                <div class="cmd-box mono">cat ${esc(sEnv)}</div>
              </div>

              <div class="ai-cmd-card">
                <div class="cmd-head">
                  <span class="cmd-badge">3</span>
                  <strong>Restart ${esc(sName)} via API</strong>
                  <button type="button" class="btn sm action" data-copy="curl -s -X POST http://127.0.0.1:9090/api/rooms/${esc(sId)}/restart">Copy</button>
                </div>
                <div class="cmd-box mono">curl -s -X POST http://127.0.0.1:9090/api/rooms/${esc(sId)}/restart</div>
              </div>

              <div class="ai-cmd-card">
                <div class="cmd-head">
                  <span class="cmd-badge">4</span>
                  <strong>View Live Unified Logs</strong>
                  <button type="button" class="btn sm action" data-copy="tail -n 100 -f /vps-manager/data/logs/vps-rooms.log">Copy</button>
                </div>
                <div class="cmd-box mono">tail -n 100 -f /vps-manager/data/logs/vps-rooms.log</div>
              </div>

              <h4 style="margin-top:20px">🏗️ Create New Room via SSH (Auto-Detected)</h4>
              <p class="muted" style="font-size:0.8rem">Create rooms directly on the filesystem — they'll be auto-detected within 10 seconds and appear in the panel.</p>

              <div class="ai-cmd-card">
                <div class="cmd-head">
                  <span class="cmd-badge">5</span>
                  <strong>Create New Single Room</strong>
                  <button type="button" class="btn sm action" data-copy="mkdir -p /vps-manager/single/new-room/project &amp;&amp; echo 'hashed_password' &gt; /vps-manager/single/new-room/auth.hash">Copy</button>
                </div>
                <div class="cmd-box mono">mkdir -p /vps-manager/single/new-room/project &amp;&amp; echo 'hashed_password' &gt; /vps-manager/single/new-room/auth.hash</div>
                <div class="cmd-note">Only auth.hash is required. Everything else is auto-created. Room appears as 'room-new-room' (rename from panel).</div>
              </div>

              <div class="ai-cmd-card">
                <div class="cmd-head">
                  <span class="cmd-badge">6</span>
                  <strong>Create New Multi Room (Compose)</strong>
                  <button type="button" class="btn sm action" data-copy="mkdir -p /vps-manager/multi/compose-room/stack &amp;&amp; echo 'hashed_password' &gt; /vps-manager/multi/compose-room/auth.hash">Copy</button>
                </div>
                <div class="cmd-box mono">mkdir -p /vps-manager/multi/compose-room/stack &amp;&amp; echo 'hashed_password' &gt; /vps-manager/multi/compose-room/auth.hash</div>
                <div class="cmd-note">Auto-creates stack/ directory for docker-compose.yml + .env files.</div>
              </div>

              <div class="ai-cmd-card">
                <div class="cmd-head">
                  <span class="cmd-badge">7</span>
                  <strong>Manual Room Scan (Force Detection)</strong>
                  <button type="button" class="btn sm action" data-copy="curl -s -X POST http://127.0.0.1:9090/api/rooms/scan">Copy</button>
                </div>
                <div class="cmd-box mono">curl -s -X POST http://127.0.0.1:9090/api/rooms/scan</div>
                <div class="cmd-note">Force immediate filesystem scan instead of waiting 10 seconds.</div>
              </div>
            </div>
          </div>
        </div>`, "ssh");

      paintBreadcrumb(document.querySelector("#crumb"), { crumbs: [{ label: "/root", path: "/root" }] });
      bindCopyables();
      document.querySelector("#ssh-refresh-btn")?.addEventListener("click", () => renderSSH(true));
      document.querySelector("#copy-ai-guide")?.addEventListener("click", () => {
        copyText(buildAIGuide(st, list, rootPass));
      });
      document.querySelector("#rootpw-btn")?.addEventListener("click", async () => {
        const btn = document.querySelector("#rootpw-btn");
        const mask = document.querySelector("#rootpw-mask");
        const val = document.querySelector("#rootpw-val");
        const cp = document.querySelector("#rootpw-copy");
        if (!val.classList.contains("hidden")) {
          val.classList.add("hidden"); mask.classList.remove("hidden"); cp.classList.add("hidden"); btn.textContent = "Show"; return;
        }
        try {
          const r = await api("/api/ssh/root-password");
          if (!r.stored) { toast("No password saved in panel — set it in Settings"); return; }
          val.textContent = r.password || "";
          val.classList.remove("hidden"); mask.classList.add("hidden"); cp.classList.remove("hidden"); btn.textContent = "Hide";
          cp.setAttribute("data-copy", r.password || "");
          bindCopyables(cp.parentElement);
        } catch (ex) { toast(ex.message || "Failed"); }
      });
    };

    const cached = state._pageCache["ssh"];
    if (cached && !forceRefresh) {
      paintSSH(cached);
      Promise.all([
        api("/api/ssh/status"),
        api("/api/vps/paths?all=1"),
        api("/api/rooms").catch(() => []),
        api("/api/ssh/root-password").catch(() => ({ password: "" })),
      ]).then(([st, all, rooms, pwInfo]) => {
        if (!alive("ssh", gen)) return;
        const fresh = { st, all, rooms, pwInfo };
        state._pageCache["ssh"] = fresh;
        paintSSH(fresh);
      }).catch(() => {});
      return;
    }

    shell(`<div id="crumb"></div><div class="topbar"><div><h2>SSH & Docs</h2><div class="sub">Connect, credentials, VPS architecture & AI documentation</div></div></div>${skel(4)}`, "ssh");
    paintBreadcrumb(document.querySelector("#crumb"), null, [{ label: "/root" }]);
    try {
      const [st, all, rooms, pwInfo] = await Promise.all([
        api("/api/ssh/status"),
        api("/api/vps/paths?all=1"),
        api("/api/rooms").catch(() => []),
        api("/api/ssh/root-password").catch(() => ({ password: "" })),
      ]);
      if (!alive("ssh", gen)) return;
      const data = { st, all, rooms, pwInfo };
      state._pageCache["ssh"] = data;
      paintSSH(data);
    } catch (e) {
      if (!alive("ssh", gen)) return;
      shell(`<div id="crumb"></div><p class="error">${esc(e.message)}</p>`, "ssh");
    }
  }

  async function renderTerminal() {
    const gen = state._gen;
    closeSSHTerm();
    shell(`
      <div id="crumb"></div>
      <div class="terminal-page">
        <div class="term-fullscreen-header">
          <div class="term-dots">
            <span class="tdot r"></span>
            <span class="tdot y"></span>
            <span class="tdot g"></span>
            <span class="term-title-text mono">root@13.140.164.29 (~/) — Live Root Shell</span>
          </div>
          <div class="term-header-actions">
            <button class="btn sm action" id="term-btn-reconnect" title="Reconnect">🔄 Reconnect</button>
            <button class="btn sm action" id="term-btn-clear" title="Clear screen">🧹 Clear</button>
          </div>
        </div>
        <div class="term-shortcuts-bar">
          <button type="button" class="term-key-btn" data-term-key="ctrl-c">Ctrl+C</button>
          <button type="button" class="term-key-btn" data-term-key="ctrl-d">Ctrl+D</button>
          <button type="button" class="term-key-btn" data-term-key="ctrl-l">Ctrl+L</button>
          <button type="button" class="term-key-btn" data-term-key="tab">Tab ⇥</button>
          <button type="button" class="term-key-btn" data-term-key="up">▲ Up</button>
          <button type="button" class="term-key-btn" data-term-key="down">▼ Down</button>
          <button type="button" class="term-key-btn" data-term-key="esc">Esc</button>
        </div>
        <div class="ssh-term term-fullscreen-box" id="ssh-term" tabindex="0">
          <div class="ssh-term-scroll" id="ssh-term-scroll">
            <div id="ssh-term-body">
              <div class="tline muted">connecting to root shell on 13.140.164.29:22…</div>
            </div>
          </div>
        </div>
      </div>
    `, "terminal");

    paintBreadcrumb(document.querySelector("#crumb"), { crumbs: [{ label: "/root", path: "/root" }] });
    openSSHTerm();

    document.querySelector("#term-btn-reconnect")?.addEventListener("click", () => openSSHTerm());
    document.querySelector("#term-btn-clear")?.addEventListener("click", () => {
      const b = document.querySelector("#ssh-term-body");
      if (b) b.innerHTML = "";
      if (state.sshScr) { state.sshScr.lines = [""]; state.sshScr.r = 0; state.sshScr.c = 0; }
    });

    document.querySelectorAll("[data-term-key]").forEach((btn) => {
      btn.onclick = () => {
        const k = btn.dataset.termKey;
        const ws = state.sshWS;
        if (!ws || ws.readyState !== 1) return;
        const send = (s) => ws.send(JSON.stringify({ type: "input", data: s }));
        if (k === "ctrl-c") send("\x03");
        else if (k === "ctrl-d") send("\x04");
        else if (k === "ctrl-l") send("\x0c");
        else if (k === "tab") send("\t");
        else if (k === "esc") send("\x1b");
        else if (k === "up") send("\x1b[A");
        else if (k === "down") send("\x1b[B");
        document.querySelector("#ssh-term")?.focus();
      };
    });
  }

  async function downloadBackupFile(name) {
    try {
      toast("Preparing download…");
      const res = await fetch("/api/backup/download?file=" + encodeURIComponent(name), { credentials: "same-origin" });
      if (!res.ok) throw new Error("Download failed (" + res.status + ")");
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url; a.download = name;
      document.body.appendChild(a); a.click();
      setTimeout(() => { URL.revokeObjectURL(url); a.remove(); }, 5000);
    } catch (ex) { toast(ex.message || "Download failed"); }
  }

  async function deleteBackupFile(name) {
    if (!await confirmAction({
      title: "Delete backup file?",
      body: `Delete backup "${name}"? This file will be permanently removed.`,
      ok: "Delete",
      danger: true,
    })) return;
    try {
      toast("Deleting backup…");
      const res = await api("/api/backup/delete?file=" + encodeURIComponent(name), { method: "POST" });
      if (res.ok || res.deleted) {
        toast("Backup deleted");
        if (state.view === "backup") renderBackup();
        else if (state.view === "room") renderRoom();
      }
    } catch (ex) { toast(ex.message || "Delete failed"); }
  }

  // Per-job tracker: updates ONLY the button + file row in place.
  // The page never re-renders during a backup, so clicks can't get lost.
  state._bkTrack = state._bkTrack || {};
  function showBkError(roomId, isFull, msg) {
    const sel = isFull ? "#bkerr" : `[data-bk-err="${roomId}"]`;
    const el = document.querySelector(sel);
    if (el) { el.textContent = msg || ""; el.classList.toggle("hidden", !msg); }
    else if (msg) toast(msg);
  }
  function bkActionsSel(roomId, isFull) {
    return isFull ? '[data-bk-fileactions="full"]' : `[data-bk-fileactions="${roomId}"]`;
  }
  function updateBkFileRow(roomId, isFull, files) {
    let f = null;
    if (isFull) f = (files || []).find((x) => x.kind === "full");
    else {
      const id8 = String(roomId || "").replace(/-/g, "").slice(0, 8);
      f = (files || []).find((x) => {
        if (x.kind !== "room") return false;
        // new: <room_id>.zip  old: room-<short>-*.zip
        if (x.name === roomId + ".zip") return true;
        if (x.room_id && x.room_id === roomId) return true;
        return x.name.indexOf(id8) === 5;
      });
    }
    const sel = isFull ? '[data-bk-filerow="full"]' : `[data-bk-filerow="${roomId}"]`;
    const row = document.querySelector(sel);
    if (row) {
      row.textContent = f ? `${f.name} · ${fmtBytes(f.size)}` : (isFull ? "No full archive" : "No backup yet");
    }
    const acts = document.querySelector(bkActionsSel(roomId, isFull));
    if (acts) {
      acts.querySelectorAll("[data-dl],[data-del-bk]").forEach((n) => n.remove());
      if (f) {
        const wrap = document.createElement("span");
        wrap.style.display = "contents";
        wrap.innerHTML = `<button type="button" class="icon-btn" data-dl="${esc(f.name)}" title="Download" aria-label="Download">${ico("dl")}</button><button type="button" class="icon-btn danger" data-del-bk="${esc(f.name)}" title="Delete backup" aria-label="Delete backup">${ico("trash")}</button>`;
        acts.appendChild(wrap);
        wrap.querySelector("[data-dl]")?.addEventListener("click", () => downloadBackupFile(f.name));
        wrap.querySelector("[data-del-bk]")?.addEventListener("click", () => deleteBackupFile(f.name));
      }
    }
    return !!f;
  }
  function startBkTrack(key, btn, roomId, isFull) {
    if (!btn) return;
    state._bkTrack[key] = (state._bkTrack[key] || 0) + 1;
    const my = state._bkTrack[key];
    try { localStorage.setItem("vpsm_bk_" + key, "running"); } catch {}
    btn.disabled = true;
    btn.classList.add("busy");
    btn.setAttribute("aria-busy", "true");
    const statusEl = () => document.querySelector(isFull ? '[data-bk-status="full"]' : `[data-bk-status="${roomId}"]`);
    const setProgress = (txt) => {
      const st = statusEl();
      if (st) st.textContent = txt || "";
      // Legacy text buttons (if any) still show progress inside the button.
      if (btn && !btn.classList.contains("icon-btn")) btn.innerHTML = esc(txt || "Working…");
    };
    const tick = async () => {
      if (!document.body.contains(btn) || state._bkTrack[key] !== my) return;
      let js = {};
      try { js = (await api("/api/backup/status")).jobs || {}; }
      catch { setTimeout(tick, 3000); return; }
      const job = js[key];
      if (job && job.state === "running") {
        btn.disabled = true;
        btn.classList.add("busy");
        btn.setAttribute("aria-busy", "true");
        setProgress(`Backing up… ${job.files || 0} files · ${fmtBytes(job.bytes || 0)}`);
        showBkError(roomId, isFull, "");
        setTimeout(tick, 1800);
        return;
      }
      if (job && job.state === "error") {
        try { localStorage.removeItem("vpsm_bk_" + key); } catch {}
        btn.disabled = false;
        btn.classList.remove("busy");
        btn.removeAttribute("aria-busy");
        setProgress("");
        showBkError(roomId, isFull, job.error || "Backup failed");
        return;
      }
      if (job && job.state === "ready") {
        try { localStorage.removeItem("vpsm_bk_" + key); } catch {}
        btn.disabled = false;
        btn.classList.remove("busy");
        btn.removeAttribute("aria-busy");
        try {
          const files = (await api("/api/backup/files")).files || [];
          const has = updateBkFileRow(roomId, isFull, files);
          setProgress("");
          showBkError(roomId, isFull, has ? "" : "Finished but no file listed — check /vps-manager/backup");
          toast(isFull ? "Full backup completed successfully" : "Room backup completed successfully");
        } catch (ex) {
          showBkError(roomId, isFull, ex.message || "Backup failed");
        }
        return;
      }
      // If job state is still pending or not yet in memory, keep waiting rather than stopping prematurely
      setTimeout(tick, 1500);
    };
    setTimeout(tick, 900);
  }

  // --- async exec jobs: run in background, button shows spinner, survives refresh ---
  state._execTrack = state._execTrack || {};
  const EXEC_LS_PREFIX = "vpsm_exec_";
  function execLsKey(scope, roomId){ return EXEC_LS_PREFIX + scope + "_" + (roomId||"host"); }
  function showExecOutput(outEl, text){
    if(!outEl) return;
    outEl.textContent = text || "";
    outEl.classList.toggle("hidden", !text);
  }
  // Icon-safe busy state: icon buttons keep their icon (CSS spinner shows),
  // legacy text buttons show a running label.
  function execBusy(btn, on, label) {
    if (!btn) return;
    if (on) {
      if (btn.dataset.origHtml == null) btn.dataset.origHtml = btn.innerHTML;
      btn.disabled = true;
      btn.classList.add("busy");
      btn.setAttribute("aria-busy", "true");
      if (!btn.classList.contains("icon-btn")) btn.innerHTML = label || "⏳ Running…";
    } else {
      btn.disabled = false;
      btn.classList.remove("busy");
      btn.removeAttribute("aria-busy");
      if (btn.dataset.origHtml != null) { btn.innerHTML = btn.dataset.origHtml; delete btn.dataset.origHtml; }
      if (btn.dataset.origText != null) delete btn.dataset.origText;
    }
  }
  function startExecTrack(btn, jobId, outputEl, scope, roomId){
    if(!btn || !jobId) return;
    const key = scope + ":" + jobId;
    state._execTrack[key] = (state._execTrack[key]||0)+1;
    const my = state._execTrack[key];
    try{ localStorage.setItem(execLsKey(scope, roomId), jobId); }catch{}
    execBusy(btn, true);
    showExecOutput(outputEl, "");
    const tick = async () => {
      if(!document.body.contains(btn) || state._execTrack[key] !== my) return;
      let js=null;
      try{ js = await api(`/api/exec/status?job_id=${encodeURIComponent(jobId)}`); }
      catch{ setTimeout(tick, 3000); return; }
      if(!js) { setTimeout(tick, 3000); return; }
      if(js.status === "running"){
        execBusy(btn, true);
        setTimeout(tick, 1500);
        return;
      }
      execBusy(btn, false);
      try{ localStorage.removeItem(execLsKey(scope, roomId)); }catch{}
      const out = (js.output || "") + (js.error ? `\n[error] ${js.error}` : "");
      showExecOutput(outputEl, out || "(no output)");
      if(js.status === "error") toast(js.error || "Command failed");
      else toast("Done");
    };
    setTimeout(tick, 900);
  }
  function resumeExecFromStorage(btn, outputEl, scope, roomId){
    let jid=null;
    try{ jid = localStorage.getItem(execLsKey(scope, roomId)); }catch{}
    if(!jid) {
      api(`/api/exec/latest?scope=${encodeURIComponent(scope)}&room_id=${encodeURIComponent(roomId||"")}`).then(js=>{
        if(js && js.status === "running" && js.id){
          startExecTrack(btn, js.id, outputEl, scope, roomId);
        }
      }).catch(()=>{});
      return false;
    }
    execBusy(btn, true);
    showExecOutput(outputEl, "");
    api(`/api/exec/status?job_id=${encodeURIComponent(jid)}`).then(js=>{
      if(js && js.status === "running"){
        startExecTrack(btn, jid, outputEl, scope, roomId);
      } else if(js && (js.status==="done"||js.status==="error")){
        try{ localStorage.removeItem(execLsKey(scope, roomId)); }catch{}
        execBusy(btn, false);
        const out = (js.output||"") + (js.error? `\n[error] ${js.error}`:"");
        showExecOutput(outputEl, out);
      } else {
        try{ localStorage.removeItem(execLsKey(scope, roomId)); }catch{}
        execBusy(btn, false);
      }
    }).catch(()=>{
      startExecTrack(btn, jid, outputEl, scope, roomId);
    });
    return true;
  }
  async function runExec(btn, scope, roomId, command, outputEl){
    if(!command) { toast("Type a command"); return; }
    execBusy(btn, true, "⏳ Starting…");
    showExecOutput(outputEl, "");
    try{
      let res;
      if(scope==="room"){
        res = await api(`/api/rooms/${encodeURIComponent(roomId)}/exec`, {method:"POST", body: JSON.stringify({command})});
      } else if(scope==="host"){
        res = await api(`/api/host/exec`, {method:"POST", body: JSON.stringify({command})});
      } else {
        res = await api(`/api/deploy/exec`, {method:"POST", body: JSON.stringify({command})});
      }
      const jid = res.job_id || res.id;
      if(!jid) throw new Error("No job id");
      startExecTrack(btn, jid, outputEl, scope, roomId);
    }catch(ex){
      execBusy(btn, false);
      toast(ex.message||"Failed");
      showExecOutput(outputEl, ex.message||"Failed");
    }
  }

  async function fetchBackupData() {
    let rooms = [], files = [], jobs = {};
    try { rooms = await api("/api/rooms"); } catch {}
    try { files = (await api("/api/backup/files")).files || []; } catch {}
    try { jobs = (await api("/api/backup/status")).jobs || {}; } catch {}
    return { rooms, files, jobs };
  }

  function paintBackupView(data, gen) {
    if (!alive("backup", gen)) return;
    const { rooms, files, jobs } = data;
    const full = (files || []).find((f) => f.kind === "full");
    const fullJob = jobs ? jobs["full"] : null;
    const isBkBusy = (k) => {
      const j = jobs ? jobs[k] : null;
      if (j && j.state === "running") return true;
      try { return localStorage.getItem("vpsm_bk_" + k) === "running"; } catch { return false; }
    };
    const busyFull = isBkBusy("full");
    const fileFor = (roomId) => {
      const exact = (files || []).find((f) => f.kind === "room" && (f.name === roomId + ".zip" || f.room_id === roomId));
      if (exact) return exact;
      const cands = (files || []).filter((f) => f.kind === "room" && f.name.indexOf(roomId.replace(/-/g, "").slice(0, 8)) === 5);
      return cands[0];
    };
    const bkDlBtn = (name) => `<button type="button" class="icon-btn" data-dl="${esc(name)}" title="Download" aria-label="Download">${ico("dl")}</button>`;
    const bkDelBtn = (name) => `<button type="button" class="icon-btn danger" data-del-bk="${esc(name)}" title="Delete backup" aria-label="Delete backup">${ico("trash")}</button>`;
    const cards = (rooms || []).map((r) => {
      const f = fileFor(r.id);
      const job = jobs ? jobs["room:" + r.id] : null;
      const busy = isBkBusy("room:" + r.id);
      const statusTxt = busy ? (job && job.files ? `${job.files} files · ${fmtBytes(job.bytes || 0)}` : "Working…") : "";
      return `<div class="bk-row">
        <div class="bk-ico" aria-hidden="true">${ico("file", 17)}</div>
        <div class="bk-meta">
          <div class="bk-name-row"><h4>${esc(r.name || r.id)}</h4>
            <span class="badge ${r.kind === "multi" ? "info" : "muted-badge"}">${esc(r.kind || "single")}</span>
          </div>
          <div class="bk-sub mono" data-bk-filerow="${esc(r.id)}">${f ? `${esc(f.name)} · ${fmtBytes(f.size)}` : "No backup yet"}</div>
          <div class="bk-status" data-bk-status="${esc(r.id)}">${esc(statusTxt)}</div>
          <p class="error hidden" data-bk-err="${esc(r.id)}">${(job && job.state === "error" && job.error) ? esc(job.error) : ""}</p>
        </div>
        <div class="bk-tools" data-bk-fileactions="${esc(r.id)}">
          <button type="button" class="icon-btn bk-go ${busy ? "busy" : ""}" data-bk-room="${esc(r.id)}" ${busy ? "disabled aria-busy='true'" : ""} title="${busy ? "Backing up…" : (f ? "Replace backup" : "Backup now")}" aria-label="Backup now">${ico("play")}</button>
          ${f ? `${bkDlBtn(f.name)}${bkDelBtn(f.name)}` : ""}
        </div>
      </div>`;
    }).join("") || '<p class="muted">No rooms yet.</p>';
    const fullStatusTxt = busyFull ? (fullJob && fullJob.files ? `${fullJob.files} files · ${fmtBytes(fullJob.bytes || 0)}` : "Working…") : "";

    shell(`<div id="crumb"></div>
      <div class="bk-page">
      <div class="topbar bk-top">
        <div>
          <h2>Backup</h2>
          <div class="sub">/vps-manager/backup — newest only</div>
        </div>
        <div class="actions bk-top-actions">
          <button type="button" class="icon-btn" id="bk-refresh-btn" title="Refresh status" aria-label="Refresh status">${refreshIconHTML()}</button>
        </div>
      </div>
      <div class="bk-hero2">
        <div class="bk-ico big" aria-hidden="true">${ico("box", 20)}</div>
        <div class="bk-meta">
          <div class="bk-name-row"><h3>Full Host Backup</h3></div>
          <div class="bk-sub mono" data-bk-filerow="full">${full ? `${esc(full.name)} · ${fmtBytes(full.size)}` : "No full archive"}</div>
          <div class="bk-hint muted">Rooms + database + configs + proxy + env files</div>
          <div class="bk-status" data-bk-status="full">${esc(fullStatusTxt)}</div>
          <p class="error" id="bkerr"></p>
        </div>
        <div class="bk-tools" data-bk-fileactions="full">
          <button type="button" class="icon-btn bk-go ${busyFull ? "busy" : ""}" id="bk-full" ${busyFull ? "disabled aria-busy='true'" : ""} title="${busyFull ? "Backing up…" : (full ? "Replace full backup" : "Backup everything now")}" aria-label="Full backup">${ico("play")}</button>
          ${full ? `${bkDlBtn(full.name)}${bkDelBtn(full.name)}` : ""}
        </div>
      </div>
      <div class="panel bk-list-panel">
        <div class="bk-list-head">
          <h3 style="margin:0">Per-Project Backups</h3>
          <span class="muted bk-hint">Stored in <code>backup/&lt;room_id&gt;.zip</code></span>
        </div>
        <div class="bk-list">${cards}</div>
      </div>
      </div>`, "backup");

    paintBreadcrumb(document.querySelector("#crumb"), { crumbs: [{ label: "/vps-manager", path: "/vps-manager" }, { label: "backup", path: "/vps-manager/backup" }], connect: state.cache?.sshConnect || "" });

    document.querySelector("#bk-refresh-btn")?.addEventListener("click", () => renderBackup(true));

    const bkStatusEl = (isFull, roomId) => document.querySelector(isFull ? '[data-bk-status="full"]' : `[data-bk-status="${roomId}"]`);
    const fullBtn = document.querySelector("#bk-full");
    if (fullBtn) {
      fullBtn.addEventListener("click", async () => {
        if (fullBtn.disabled || fullBtn.classList.contains("busy")) return;
        fullBtn.disabled = true;
        fullBtn.classList.add("busy");
        fullBtn.setAttribute("aria-busy", "true");
        const st = bkStatusEl(true);
        if (st) st.textContent = "Starting…";
        try {
          try { localStorage.setItem("vpsm_bk_full", "running"); } catch {}
          await api("/api/backup/full", { method: "POST", body: "{}" });
          toast("Full backup started — watch the status line, no reload needed");
          startBkTrack("full", fullBtn, null, true);
        } catch (ex) {
          try { localStorage.removeItem("vpsm_bk_full"); } catch {}
          fullBtn.disabled = false;
          fullBtn.classList.remove("busy");
          fullBtn.removeAttribute("aria-busy");
          if (st) st.textContent = "";
          const e = document.querySelector("#bkerr");
          if (e) e.textContent = ex.message;
          else toast(ex.message || "Backup failed");
        }
      });
    }

    document.querySelectorAll("[data-bk-room]").forEach((b) => {
      b.onclick = async () => {
        if (b.disabled || b.classList.contains("busy")) return;
        const roomId = b.dataset.bkRoom;
        const key = "room:" + roomId;
        b.disabled = true;
        b.classList.add("busy");
        b.setAttribute("aria-busy", "true");
        const st = bkStatusEl(false, roomId);
        if (st) st.textContent = "Starting…";
        try {
          try { localStorage.setItem("vpsm_bk_" + key, "running"); } catch {}
          await api("/api/backup/room", { method: "POST", body: JSON.stringify({ room_id: roomId }) });
          toast("Backup started — watch the status line");
          startBkTrack(key, b, roomId, false);
        } catch (ex) {
          try { localStorage.removeItem("vpsm_bk_" + key); } catch {}
          b.disabled = false;
          b.classList.remove("busy");
          b.removeAttribute("aria-busy");
          if (st) st.textContent = "";
          toast(ex.message || "Backup failed");
        }
      };
    });

    document.querySelectorAll("[data-dl]").forEach((b) => {
      b.onclick = () => downloadBackupFile(b.dataset.dl);
    });
    document.querySelectorAll("[data-del-bk]").forEach((b) => {
      b.onclick = () => deleteBackupFile(b.dataset.delBk);
    });

    if (busyFull && fullBtn) startBkTrack("full", fullBtn, null, true);
    document.querySelectorAll("[data-bk-room]").forEach((b) => {
      const roomId = b.dataset.bkRoom;
      const key = "room:" + roomId;
      if (isBkBusy(key)) startBkTrack(key, b, roomId, false);
    });
  }

  async function renderBackup(forceRefresh = false) {
    const gen = state._gen;
    if (!forceRefresh && state._pageCache && state._pageCache["backup"]) {
      paintBackupView(state._pageCache["backup"], gen);
      fetchBackupData().then((data) => {
        if (!alive("backup", gen)) return;
        state._pageCache["backup"] = data;
        paintBackupView(data, gen);
      }).catch(() => {});
      return;
    }

    shell(`<div id="crumb"></div><div class="topbar"><div><h2>Backup</h2><div class="sub">/vps-manager/backup — newest only</div></div></div>${skel(3)}`, "backup");
    paintBreadcrumb(document.querySelector("#crumb"), { crumbs: [{ label: "/vps-manager", path: "/vps-manager" }, { label: "backup", path: "/vps-manager/backup" }], connect: state.cache?.sshConnect || "" });

    try {
      const data = await fetchBackupData();
      if (!alive("backup", gen)) return;
      if (!state._pageCache) state._pageCache = {};
      state._pageCache["backup"] = data;
      paintBackupView(data, gen);
    } catch (e) {
      if (!alive("backup", gen)) return;
      shell(`<div class="banner err">${esc(e.message || "Failed to load backups")}</div>`, "backup");
    }
  }

  async function render() {
    if (state.view !== "terminal") closeSSHTerm();
    if (!state.gated) { renderGate(); return; }
    if (!state.me) await loadMe();
    if (!state.me) { renderUnlock(); return; }
    if (state.me.kind === "owner") {
      if (state.view === "rooms") return renderRooms();
      if (state.view === "backup") return renderBackup();
      if (state.view === "logs") return renderLogs();
      if (state.view === "agent") return renderAgent();
      if (state.view === "settings") return renderSettings();
      if (state.view === "terminal") return renderTerminal();
      if (state.view === "room") return renderRoom();
      await renderServer();
      return;
    }
    return renderRoom();
  }

  const boot = parsePath(location.pathname);
  if (boot) Object.assign(state, boot);
  if (/^\/deploy\/?$/.test(location.pathname)) {
    history.replaceState({ view: "rooms" }, "", "/projects");
    state.view = "rooms";
  }
  window.addEventListener("popstate", () => {
    saveChatDraft();
    const parsed = parsePath(location.pathname) || { view: "server" };
    state.view = parsed.view;
    if (parsed.roomId) state.roomId = parsed.roomId;
    if (parsed.roomTab) state.roomTab = parsed.roomTab;
    if (parsed.ctrId) state.ctrId = parsed.ctrId;
    if (parsed.volId) state.volId = parsed.volId;
    state._gen = (state._gen || 0) + 1;
    stopLogLive();
    markNav(state.view);
    render();
  });

  registerCopyDelegation();
  (async () => {
    await loadGate();
    if (state.gated) {
      await loadMe();
      if (state.me) {
        connectWS();
      }
    }
    await render();
  })();
})();

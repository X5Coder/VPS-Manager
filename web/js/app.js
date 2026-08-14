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
    aiByRoom: {},
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

  function brandMarkHTML() {
    return `<img class="brand-mark-img" src="/favicon.svg" width="36" height="36" alt="" />`;
  }
  function projectIconHTML(extra = "") {
    return `<img class="proj-ico ${extra}" src="/project.svg" width="44" height="44" alt="" />`;
  }
  function brandWordmarkHTML(roleId, roleText) {
    return `${brandMarkHTML()}<div class="brand-text"><strong class="brand-name">VPS Manager</strong><span class="brand-role" id="${roleId}">${esc(roleText)}</span></div>`;
  }
  function navIco(k) {
    const p = 'viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round"';
    const icons = {
      server: `<svg ${p}><rect x="3" y="4" width="18" height="6" rx="1.5"/><rect x="3" y="14" width="18" height="6" rx="1.5"/><path d="M7 7h.01M7 17h.01"/></svg>`,
      rooms: `<svg ${p}><rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/></svg>`,
      deploy: `<svg ${p}><path d="M12 3v12"/><path d="M8 11l4 4 4-4"/><path d="M5 19h14"/></svg>`,
      restore: `<svg ${p}><path d="M3 12a9 9 0 1 0 3-6.7"/><path d="M3 4v5h5"/></svg>`,
      logs: `<svg ${p}><path d="M8 6h13M8 12h13M8 18h13"/><path d="M3 6h.01M3 12h.01M3 18h.01"/></svg>`,
      docs: `<svg ${p}><path d="M7 3h8l5 5v13a1 1 0 0 1-1 1H7a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z"/><path d="M15 3v5h5M9 13h6M9 17h4"/></svg>`,
      settings: `<svg ${p}><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9c.3.7.9 1.2 1.6 1.4H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/></svg>`,
      tokens: `<svg ${p}><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>`,
      room: `<svg ${p}><path d="M4 10.5L12 4l8 6.5V20a1 1 0 0 1-1 1h-5v-6H10v6H5a1 1 0 0 1-1-1z"/></svg>`,
    };
    return icons[k] || "";
  }

  function roomAIState(id) {
    if (!state.aiByRoom[id]) state.aiByRoom[id] = { messages: [], bubbles: [], busy: false, run: 0, rtl: false };
    return state.aiByRoom[id];
  }

  function isRTLText(s) {
    let ar = 0, lat = 0;
    for (const ch of String(s || "")) {
      const c = ch.codePointAt(0);
      if (c >= 0x0600 && c <= 0x06FF) ar++;
      else if ((c >= 65 && c <= 90) || (c >= 97 && c <= 122)) lat++;
    }
    if (!ar && !lat) return false;
    return ar >= lat;
  }

  function agentStatus(_pack, key) {
    const en = {
      think: "typing now…",
      read: "typing now…",
      quota: "Setting disk quota…",
      type: "Typing in the terminal…",
      run: "Running…",
      start: "Starting the room…",
    };
    return en[key] || en.think;
  }

  const AGENT_HELLO = "Hello — write me a command and I’ll run it in the terminal.";
  const TOKEN_HELLO = "Hello — I can create an API token with read, write, or both on the same key.";
  const ROOM_HELLO = "Hello — I’m inside this project. Ask me to inspect or edit files, run commands, analyze this room’s usage, change disk, or pause/resume. I won’t delete the project.";
  const LOGS_HELLO = "Hello — I analyze panel logs. Ask me to analyze, then pick which log.";
  const USAGE_HELLO = "Hello — I analyze this server’s live usage: CPU, RAM, disk, load, room names, and each room’s disk vs the host total.";

  function sendIconHTML() {
    return `<button class="ai-send" id="ai-send" type="submit" aria-label="Send">${planeIconSVG()}</button>`;
  }
  function planeIconSVG() {
    return `<svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M3.4 20.6 21 12 3.4 3.4 3 10.2 15 12 3 13.8z"/></svg>`;
  }
  function stopIconSVG() {
    return `<svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><rect x="6" y="6" width="12" height="12" rx="2"/></svg>`;
  }

  function agentDeskHTML({
    title = "Agent",
    prompt = "root@vps-manager:~#",
    termLines = "ready\n",
    showTerm = true,
    placeholder = "Message…",
    hints = "",
    hiddenQuotaId = "",
  } = {}) {
    const p = String(prompt || "root@vps-manager:~#").replace(/\s+$/, "");
    const hiddenQ = hiddenQuotaId
      ? `<input type="hidden" id="${esc(hiddenQuotaId)}" name="quota_gb" value="0" data-quota-input />`
      : "";
    const term = showTerm
      ? `<div class="agent-term">
          <div class="agent-term-head"><span class="agent-dot" aria-hidden="true"></span><span class="mono">${esc(p)}</span></div>
          <div class="term-out" id="term-out">${esc(termLines)}</div>
          <form class="term-in" id="term-form">
            <span class="prompt">${esc(p)}</span>
            <input id="term-cmd" autocomplete="off" spellcheck="false" placeholder="command" />
          </form>
          ${hints ? `<div class="cmd-hints">${hints}</div>` : ""}
        </div>`
      : "";
    return `<div class="agent-desk${showTerm ? "" : " agent-desk-chat"}" dir="ltr">
      ${term}
      <section class="ai-sheet ai-sheet-embed" id="ai-chat">
        <header class="ai-sheet-bar"><span class="ai-live"></span>${esc(title)}</header>
        ${hiddenQ}
        <div class="ai-chat-log" id="ai-log"></div>
        <form class="ai-compose" id="ai-form" dir="ltr">
          <div class="ai-compose-box">
            <textarea id="ai-q" rows="1" maxlength="8000" dir="auto" placeholder="${esc(placeholder)}"></textarea>
            ${sendIconHTML()}
          </div>
        </form>
      </section>
    </div>`;
  }

  function syncAgentComposer(pack) {
    const form = document.querySelector("#ai-form");
    const inp = document.querySelector("#ai-q");
    const btn = document.querySelector("#ai-send");
    if (inp) {
      inp.setAttribute("dir", "auto");
      if (inp.tagName === "TEXTAREA") {
        inp.style.height = "auto";
        inp.style.height = Math.min(140, Math.max(24, inp.scrollHeight)) + "px";
      }
    }
    if (btn) {
      const stop = !!pack.busy;
      btn.disabled = false;
      btn.classList.toggle("is-busy", stop);
      btn.classList.toggle("is-stop", stop);
      btn.setAttribute("aria-label", stop ? "Stop" : "Send");
      btn.innerHTML = stop ? stopIconSVG() : planeIconSVG();
    }
    form?.classList.toggle("is-busy", !!pack.busy);
  }

  function paintAIChat(box, pack) {
    if (!box) return;
    syncAgentComposer(pack);
    const bits = [];
    (pack.bubbles || []).forEach((b, idx) => {
      const enter = b.enter ? (b.role === "user" ? " ai-in ai-send-in" : " ai-in ai-recv-in") : "";
      if (b.enter) b.enter = false;
      const display = b.role === "bot" ? normalizeBotText(b.text) : unescapeChatEscapes(String(b.text || ""));
      const hasCode = b.role === "bot" && /```/.test(display);
      const dir = isRTLText(display) ? "rtl" : "ltr";
      const copy = b.role === "bot" && !hasCode && String(display).length > 90
        ? `<button type="button" class="ai-copy" data-copy-bubble="${idx}" aria-label="Copy message">Copy</button>`
        : "";
      bits.push(`<div class="ai-bubble-wrap ${esc(b.role)}" data-bubble-idx="${idx}">
        <div class="ai-bubble ${esc(b.role)}${enter}${hasCode ? " has-code" : ""}" dir="${dir}">${formatChat(display, idx)}</div>
        ${copy}
      </div>`);
    });
    if (pack.busy && pack.typing !== false && !(pack.pendingAsk && pack.pendingAsk.length)) {
      bits.push(`<div class="ai-bubble bot status ai-typing${pack.typingOut ? " is-leaving" : ""}" dir="ltr"><span class="ai-dots" aria-hidden="true"></span><span>typing now…</span></div>`);
    }
    if (!pack.busy && pack.pendingAsk && pack.pendingAsk.length) {
      const q = pack.pendingAsk[0];
      const qdir = isRTLText(q) ? "rtl" : "ltr";
      const picked = new Set(pack.pendingPicked || []);
      const chips = (pack.pendingChoices || []).map((c) =>
        `<button type="button" class="ai-choice${picked.has(c) ? " is-on" : ""}" data-ai-choice="${esc(c)}" aria-pressed="${picked.has(c) ? "true" : "false"}">${esc(c)}</button>`
      ).join("");
      bits.push(`<div class="ai-qcard" dir="${qdir}">
        <div class="ai-qcard-label">Question</div>
        <div class="ai-qcard-q">${esc(q)}</div>
        ${chips ? `<div class="ai-choices" dir="auto">${chips}</div>` : `<p class="ai-qcard-hint">Type your answer below</p>`}
        ${chips ? `<div class="ai-qcard-actions"><button type="button" class="btn primary sm" data-ai-choose-go ${picked.size ? "" : "disabled"}>Continue</button></div>` : ""}
      </div>`);
    } else if (!pack.busy && pack.pendingChoices && pack.pendingChoices.length) {
      const picked = new Set(pack.pendingPicked || []);
      bits.push(`<div class="ai-qcard" dir="auto">
        <div class="ai-qcard-label">Choose</div>
        <div class="ai-choices" dir="auto">${pack.pendingChoices.map((c) =>
          `<button type="button" class="ai-choice${picked.has(c) ? " is-on" : ""}" data-ai-choice="${esc(c)}" aria-pressed="${picked.has(c) ? "true" : "false"}">${esc(c)}</button>`
        ).join("")}</div>
        <p class="ai-qcard-hint">Select one or more, then continue.</p>
        <div class="ai-qcard-actions">
          <button type="button" class="btn primary sm" data-ai-choose-go ${picked.size ? "" : "disabled"}>Continue</button>
        </div>
      </div>`);
    }
    box.innerHTML = bits.join("");
    colorCodeBlocks(box);
    box.scrollTop = box.scrollHeight;
  }

  function colorCodeBlocks(root) {
    root.querySelectorAll("pre.ai-codeblock code").forEach((el) => {
      if (el.querySelector("[class^='tok-']")) return;
      const lang = el.closest(".ai-codewrap")?.getAttribute("data-lang")
        || el.closest(".ai-codewrap")?.querySelector(".ai-codelang")?.textContent
        || "";
      el.innerHTML = highlightCode(lang, el.textContent || "");
    });
  }

  function extractSayFromRaw(text) {
    const s = String(text || "").trim();
    if (!s.includes('"say"')) return "";
    const m = s.match(/"say"\s*:\s*"/);
    if (!m) {
      const i = s.search(/"say"\s*:\s*/);
      if (i < 0) return "";
      let rest = s.slice(i).replace(/^"say"\s*:\s*/, "");
      if (rest.startsWith('"')) rest = rest.slice(1);
      const stop = rest.search(/"\s*,\s*"(?:ask|says|command|choices|done|create_token|token_name|quota_gb|action|log_kind|image|start)"/);
      let val = stop >= 0 ? rest.slice(0, stop) : rest;
      val = val.replace(/"\s*}\s*$/, "").trim();
      return unescapeChatEscapes(val);
    }
    let i = m.index + m[0].length;
    let out = "";
    let esc = false;
    for (; i < s.length; i++) {
      const c = s[i];
      if (esc) {
        if (c === "n") out += "\n";
        else if (c === "t") out += "\t";
        else if (c === "r") out += "\r";
        else out += c;
        esc = false;
        continue;
      }
      if (c === "\\") { esc = true; continue; }
      if (c === '"') break;
      out += c;
    }
    return unescapeChatEscapes(out.trim());
  }

  function unescapeChatEscapes(text) {
    let s = String(text || "");
    s = s.replace(/\\r\\n/g, "\n").replace(/\\r/g, "\n");
    s = s.replace(/\\n/g, "\n").replace(/\\t/g, "\t");
    s = s.replace(/\\"/g, '"');
    return s;
  }

  function looksLikeCode(text) {
    const t = String(text || "").trim();
    if (!t || t.length < 24) return false;
    const lines = t.split(/\n/).filter((l) => l.trim());
    if (lines.length < 2) return false;
    const hits = [
      /^(import |from |def |class |with |async |await |function |const |let |var |#include|package |fn |pub )/m,
      /:\s*$/m,
      /[{};]\s*$/m,
      /^(print|console\.|curl |docker |git )/m,
      /^\s{2,}\S/m,
    ].filter((re) => re.test(t)).length;
    return hits >= 2;
  }

  function guessCodeLang(text) {
    const t = String(text || "");
    if (/^\s*(import |from |def |with open|print\()/m.test(t)) return "python";
    if (/^\s*(const |let |var |function |import |export )/m.test(t)) return "javascript";
    if (/^\s*(package |func |fmt\.)/m.test(t)) return "go";
    if (/^\s*(#!\/bin\/|curl |docker |git |apt |sudo )/m.test(t)) return "bash";
    return "";
  }

  function normalizeBotText(text) {
    let t = String(text || "").trim();
    const extracted = extractSayFromRaw(t);
    if (extracted) t = extracted;
    t = unescapeChatEscapes(t);
    t = t.replace(/\r\n/g, "\n");
    if (t.startsWith("{") && /"say"\s*:/.test(t)) {
      const again = extractSayFromRaw(t);
      if (again) t = unescapeChatEscapes(again);
    }
    if (!/```/.test(t) && looksLikeCode(t)) {
      const lang = guessCodeLang(t);
      t = "```" + lang + "\n" + t.trim() + "\n```";
    } else if (!/```/.test(t)) {
      const parts = t.split(/\n\n+/);
      if (parts.length >= 2) {
        const last = parts[parts.length - 1];
        if (looksLikeCode(last) || (/^(with open|import |def |function |curl )/m.test(last) && last.includes("\n"))) {
          const head = parts.slice(0, -1).join("\n\n").trim();
          const lang = guessCodeLang(last);
          t = head + "\n\n```" + lang + "\n" + last.trim() + "\n```";
        }
      }
    }
    return t.trim();
  }

  function splitBotBubbles(text) {
    const t = normalizeBotText(text);
    if (!t) return [];
    if (/```/.test(t)) return [t];
    const parts = t.split(/\n{2,}/).map((s) => s.trim()).filter(Boolean);
    if (parts.length <= 1) return [t];
    return parts.slice(0, 6);
  }

  function collectBotMessages(res) {
    const out = [];
    const seen = [];
    const similar = (a, b) => {
      if (!a || !b) return false;
      if (a === b) return true;
      if (a.includes(b) || b.includes(a)) return true;
      const n = Math.min(28, a.length, b.length);
      return n >= 16 && a.slice(0, n) === b.slice(0, n);
    };
    const addOne = (raw) => {
      const t = normalizeBotText(raw);
      if (!t) return;
      if (seen.some((x) => similar(x, t))) return;
      seen.push(t);
      out.push(t);
    };
    addOne(res?.say || "");
    const says = Array.isArray(res?.says) ? res.says : [];
    for (const s of says) {
      addOne(s);
      if (out.length >= 2) break;
    }
    return out;
  }

  const HL_KW = {
    python: "and as assert async await break class continue def del elif else except False finally for from global if import in is lambda None nonlocal not or pass raise return True try while with yield self cls".split(" "),
    javascript: "async await break case catch class const continue debugger default delete do else export extends false finally for from function if import in instanceof let new null of return static super switch this throw true try typeof var void while with yield".split(" "),
    typescript: "async await break case catch class const continue debugger default delete do else export extends false finally for from function if import in instanceof let new null of return static super switch this throw true try typeof var void while with yield type interface enum implements readonly public private protected abstract as satisfies namespace declare any never unknown".split(" "),
    go: "break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var true false nil iota".split(" "),
    bash: "if then else elif fi for in do done while until case esac function return exit export local declare set unset test true false echo cd pwd source alias".split(" "),
    sql: "select from where and or not insert into values update set delete join left right inner outer on group by order limit as create table index distinct having union all null is like in exists".split(" "),
    rust: "as async await break const continue crate dyn else enum extern false fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait true type unsafe use where while".split(" "),
    java: "abstract assert boolean break byte case catch char class const continue default do double else enum extends final finally float for goto if implements import instanceof int interface long native new package private protected public return short static strictfp super switch synchronized this throw throws transient try void volatile while true false null".split(" "),
    php: "and or xor array as break case continue declare default die do echo else elseif empty enddeclare endfor endforeach endif endswitch endwhile eval exit extends for foreach function global if include include_once isset list new print require require_once return static switch unset use var while trait interface implements public protected private abstract final class namespace".split(" "),
    ruby: "BEGIN END alias and begin break case class def defined do else elsif end ensure false for if in module next nil not or redo rescue retry return self super then true undef unless until when while yield".split(" "),
    c: "auto break case char const continue default do double else enum extern float for goto if inline int long register restrict return short signed sizeof static struct switch typedef union unsigned void volatile while".split(" "),
    cpp: "alignas alignof and and_eq asm auto bitand bitor bool break case catch char class compl concept const consteval constexpr constinit continue co_await co_return co_yield decltype default delete do double else enum explicit export extern false float for friend goto if inline int long mutable namespace new noexcept not nullptr operator or private protected public register reinterpret_cast return short signed sizeof static static_assert static_cast struct switch template this thread_local throw true try typedef typeid typename union unsigned using virtual void volatile while xor".split(" "),
    csharp: "abstract as base bool break byte case catch char checked class const continue decimal default delegate do double else enum event explicit extern false finally fixed float for foreach goto if implicit in int interface internal is lock long namespace new null object operator out override params private protected public readonly ref return sbyte sealed short sizeof stackalloc static string struct switch this throw true try typeof uint ulong unchecked unsafe ushort using virtual void volatile while".split(" "),
    kotlin: "as break class continue do else false for fun if in interface is null object package return super this throw true try typealias typeof val var when while by catch constructor delegate dynamic field file finally get import init param property receiver set setparam where actual abstract annotation companion const crossinline data enum expect external final infix inline inner internal lateinit noinline open operator out override private protected public reified sealed suspend tailrec vararg".split(" "),
    swift: "associatedtype class deinit enum extension fileprivate func import init inout internal let open operator private protocol public rethrows static struct subscript typealias var break case continue default defer do else fallthrough for guard if in repeat return switch where while as Any catch false is nil super self Self throw throws true try".split(" "),
    html: "html head body div span script style link meta title h1 h2 h3 h4 h5 h6 p a ul ol li img table tr td th form input button label section article nav header footer main".split(" "),
    css: "important media charset import supports from to".split(" "),
    docker: "FROM RUN CMD LABEL MAINTAINER EXPOSE ENV ADD COPY ENTRYPOINT VOLUME USER WORKDIR ARG ONBUILD STOPSIGNAL HEALTHCHECK SHELL AS".split(" "),
    yaml: "true false null yes no on off".split(" "),
    json: "true false null".split(" "),
  };
  Object.keys(HL_KW).forEach((k) => { HL_KW[k] = new Set(HL_KW[k]); });

  function normLang(lang) {
    const l = String(lang || "").toLowerCase();
    const map = {
      js: "javascript", jsx: "javascript", mjs: "javascript", cjs: "javascript",
      ts: "typescript", tsx: "typescript",
      py: "python", python3: "python",
      sh: "bash", shell: "bash", zsh: "bash", bash: "bash",
      golang: "go",
      yml: "yaml",
      dockerfile: "docker",
      "c++": "cpp", cc: "cpp", cxx: "cpp", hpp: "cpp",
      cs: "csharp", "c#": "csharp",
      rs: "rust", rb: "ruby", kt: "kotlin",
      htm: "html", xml: "html",
      text: "code", txt: "code",
    };
    return map[l] || l || "code";
  }

  function highlightCode(lang, src) {
    const code = String(src || "");
    const L = normLang(lang) || guessCodeLang(code) || "python";
    try {
      if (L === "json") return hlJSON(code);
      if (L === "html") return hlMarkup(code);
      if (L === "css") return hlCSS(code);
      if (L === "yaml") return hlYAML(code);
      if (L === "docker") return hlDocker(code);
      return hlGeneric(code, L === "code" ? (guessCodeLang(code) || "python") : L);
    } catch {
      return esc(code);
    }
  }

  function hlJSON(src) {
    let out = "";
    let i = 0;
    while (i < src.length) {
      const c = src[i];
      if (c === '"' ) {
        let j = i + 1, escb = false;
        while (j < src.length) {
          if (escb) { escb = false; j++; continue; }
          if (src[j] === "\\") { escb = true; j++; continue; }
          if (src[j] === '"') { j++; break; }
          j++;
        }
        const chunk = src.slice(i, j);
        let k = j;
        while (k < src.length && /\s/.test(src[k])) k++;
        const isKey = src[k] === ":";
        out += `<span class="${isKey ? "tok-key" : "tok-str"}">${esc(chunk)}</span>`;
        i = j;
        continue;
      }
      if (c === "/" && src[i + 1] === "/") {
        const end = src.indexOf("\n", i);
        const j = end < 0 ? src.length : end;
        out += `<span class="tok-cmt">${esc(src.slice(i, j))}</span>`;
        i = j;
        continue;
      }
      if (/[0-9\-]/.test(c) && (c !== "-" || /[0-9]/.test(src[i + 1] || ""))) {
        let j = i + 1;
        while (j < src.length && /[0-9.eE+\-]/.test(src[j])) j++;
        out += `<span class="tok-num">${esc(src.slice(i, j))}</span>`;
        i = j;
        continue;
      }
      const word = src.slice(i).match(/^(true|false|null)\b/);
      if (word) {
        out += `<span class="tok-kw">${word[0]}</span>`;
        i += word[0].length;
        continue;
      }
      out += esc(c);
      i++;
    }
    return out;
  }

  function hlGeneric(src, lang) {
    const kw = HL_KW[lang] || new Set();
    const hashCmt = lang === "python" || lang === "bash" || lang === "ruby" || lang === "yaml";
    const slashCmt = lang !== "python" && lang !== "bash" && lang !== "ruby";
    const types = lang === "go"
      ? new Set("string int int64 int32 uint uint64 float64 bool byte error any".split(" "))
      : new Set();
    let out = "";
    let i = 0;
    while (i < src.length) {
      const c = src[i];
      if (c === "#" && hashCmt) {
        const j = src.indexOf("\n", i);
        const end = j < 0 ? src.length : j;
        out += `<span class="tok-cmt">${esc(src.slice(i, end))}</span>`;
        i = end;
        continue;
      }
      if (slashCmt && c === "/" && src[i + 1] === "/") {
        const j = src.indexOf("\n", i);
        const end = j < 0 ? src.length : j;
        out += `<span class="tok-cmt">${esc(src.slice(i, end))}</span>`;
        i = end;
        continue;
      }
      if (slashCmt && c === "/" && src[i + 1] === "*") {
        const j = src.indexOf("*/", i + 2);
        const end = j < 0 ? src.length : j + 2;
        out += `<span class="tok-cmt">${esc(src.slice(i, end))}</span>`;
        i = end;
        continue;
      }
      if ((c === '"' || c === "'" || c === "`") && !(lang === "python" && false)) {
        const q = c;
        let j = i + 1, escb = false;
        if (lang === "python" && src.slice(i, i + 3) === q + q + q) {
          const close = src.indexOf(q + q + q, i + 3);
          const end = close < 0 ? src.length : close + 3;
          out += `<span class="tok-str">${esc(src.slice(i, end))}</span>`;
          i = end;
          continue;
        }
        while (j < src.length) {
          if (escb) { escb = false; j++; continue; }
          if (src[j] === "\\") { escb = true; j++; continue; }
          if (src[j] === q) { j++; break; }
          if (q !== "`" && src[j] === "\n") break;
          j++;
        }
        out += `<span class="tok-str">${esc(src.slice(i, j))}</span>`;
        i = j;
        continue;
      }
      if (c === "/" && (lang === "javascript" || lang === "typescript")) {
        // regex after = ( [ , : ! & |
        const prev = out.replace(/<[^>]+>/g, "").slice(-1);
        if ("=([,:!&|;?".includes(prev) || prev === "") {
          let j = i + 1, escb = false;
          while (j < src.length) {
            if (escb) { escb = false; j++; continue; }
            if (src[j] === "\\") { escb = true; j++; continue; }
            if (src[j] === "\n") break;
            if (src[j] === "/") { j++; while (j < src.length && /[gimsuy]/.test(src[j])) j++; break; }
            j++;
          }
          out += `<span class="tok-str">${esc(src.slice(i, j))}</span>`;
          i = j;
          continue;
        }
      }
      if (/[0-9]/.test(c)) {
        let j = i + 1;
        while (j < src.length && /[0-9xa-fA-F_.]/.test(src[j])) j++;
        out += `<span class="tok-num">${esc(src.slice(i, j))}</span>`;
        i = j;
        continue;
      }
      if (/[A-Za-z_$@]/.test(c)) {
        let j = i + 1;
        while (j < src.length && /[A-Za-z0-9_$@]/.test(src[j])) j++;
        const w = src.slice(i, j);
        let k = j;
        while (k < src.length && /\s/.test(src[k])) k++;
        const call = src[k] === "(";
        if (kw.has(w)) out += `<span class="tok-kw">${esc(w)}</span>`;
        else if (types.has(w) || (/^[A-Z]/.test(w) && lang !== "bash")) out += `<span class="tok-type">${esc(w)}</span>`;
        else if (call) out += `<span class="tok-fn">${esc(w)}</span>`;
        else out += esc(w);
        i = j;
        continue;
      }
      out += esc(c);
      i++;
    }
    return out;
  }

  function hlMarkup(src) {
    let out = "";
    let i = 0;
    while (i < src.length) {
      if (src.startsWith("<!--", i)) {
        const j = src.indexOf("-->", i + 4);
        const end = j < 0 ? src.length : j + 3;
        out += `<span class="tok-cmt">${esc(src.slice(i, end))}</span>`;
        i = end;
        continue;
      }
      if (src[i] === "<") {
        const gt = src.indexOf(">", i + 1);
        if (gt < 0) { out += esc(src.slice(i)); break; }
        const inner = src.slice(i + 1, gt);
        const close = inner.startsWith("/");
        const bang = inner.startsWith("!");
        const body = close ? inner.slice(1) : inner;
        const nm = body.match(/^[A-Za-z][\w:-]*/);
        const name = nm ? nm[0] : "";
        let attrs = name ? body.slice(name.length) : body;
        let aout = "";
        for (let k = 0; k < attrs.length; k++) {
          if (attrs[k] === '"' || attrs[k] === "'") {
            const q = attrs[k];
            let j = k + 1;
            while (j < attrs.length && attrs[j] !== q) j++;
            aout += `<span class="tok-str">${esc(attrs.slice(k, Math.min(attrs.length, j + 1)))}</span>`;
            k = j;
            continue;
          }
          if (attrs[k] === "=") { aout += `<span class="tok-op">=</span>`; continue; }
          aout += esc(attrs[k]);
        }
        const open = close ? "&lt;/" : "&lt;";
        if (bang) out += `<span class="tok-cmt">${esc(src.slice(i, gt + 1))}</span>`;
        else out += `${open}<span class="tok-kw">${esc(name)}</span>${aout}&gt;`;
        i = gt + 1;
        continue;
      }
      out += esc(src[i]);
      i++;
    }
    return out;
  }

  function hlCSS(src) {
    return hlGeneric(src, "css");
  }

  function hlYAML(src) {
    return src.split("\n").map((line) => {
      const cmt = line.match(/^(\s*)(#.*)$/);
      if (cmt) return esc(cmt[1]) + `<span class="tok-cmt">${esc(cmt[2])}</span>`;
      const kv = line.match(/^(\s*)([^:#\n][^:]*)(:)(\s*)(.*)$/);
      if (kv) {
        const rest = kv[5];
        const val = /^(true|false|null|yes|no)\b/i.test(rest)
          ? `<span class="tok-kw">${esc(rest)}</span>`
          : /^["']/.test(rest) ? `<span class="tok-str">${esc(rest)}</span>`
          : /^-?\d/.test(rest) ? `<span class="tok-num">${esc(rest)}</span>`
          : esc(rest);
        return `${esc(kv[1])}<span class="tok-key">${esc(kv[2])}</span><span class="tok-op">${kv[3]}</span>${esc(kv[4])}${val}`;
      }
      return esc(line);
    }).join("\n");
  }

  function hlDocker(src) {
    return src.split("\n").map((line) => {
      if (/^\s*#/.test(line)) return `<span class="tok-cmt">${esc(line)}</span>`;
      const m = line.match(/^(\s*)([A-Z]+)(\s*)([\s\S]*)$/);
      if (m && HL_KW.docker.has(m[2])) {
        return `${esc(m[1])}<span class="tok-kw">${m[2]}</span>${esc(m[3])}<span class="tok-str">${esc(m[4])}</span>`;
      }
      return esc(line);
    }).join("\n");
  }

  function formatChat(text, bubbleIdx = 0) {
    const raw = String(text || "");
    const parts = [];
    const fence = /```([a-zA-Z0-9_+-]*)\n?([\s\S]*?)```/g;
    let last = 0;
    let m;
    let codeN = 0;
    while ((m = fence.exec(raw))) {
      if (m.index > last) parts.push({ type: "text", value: raw.slice(last, m.index).replace(/\s+$/, "") });
      parts.push({ type: "code", lang: m[1] || "", value: m[2].replace(/\n$/, ""), id: codeN++ });
      last = m.index + m[0].length;
    }
    if (last < raw.length) parts.push({ type: "text", value: raw.slice(last) });
    if (!parts.length) parts.push({ type: "text", value: raw });
    return parts.map((p) => {
      if (p.type === "code") {
        const lang = (p.lang || guessCodeLang(p.value) || "code").toLowerCase();
        const label = lang && lang !== "code" ? lang : (guessCodeLang(p.value) || "code");
        const hl = highlightCode(label, p.value);
        return `<div class="ai-codewrap" data-lang="${esc(label)}" data-code-bubble="${bubbleIdx}" data-code-id="${p.id}">
          <div class="ai-codehead">
            <span class="ai-code-dots" aria-hidden="true"><i></i><i></i><i></i></span>
            <span class="ai-codelang">${esc(label)}</span>
            <button type="button" class="ai-codecopy" data-copy-code="${bubbleIdx}:${p.id}" aria-label="Copy code">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
              <span class="ai-codecopy-lab">Copy</span>
            </button>
          </div>
          <pre class="ai-codeblock"><code class="language-${esc(label)}">${hl}</code></pre>
        </div>`;
      }
      let s = esc(p.value);
      s = s.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>");
      s = s.replace(/`([^`\n]+)`/g, "<code class=\"ai-code\">$1</code>");
      s = s.replace(/^[-•] (.+)$/gm, "<span class=\"ai-li\">$1</span>");
      s = s.replace(/\n{3,}/g, "\n\n");
      return `<div class="ai-md">${s}</div>`;
    }).join("");
  }

  function codeFromBubble(pack, bubbleIdx, codeId) {
    const b = pack.bubbles?.[bubbleIdx];
    if (!b) return "";
    const display = normalizeBotText(b.text);
    const fence = /```([a-zA-Z0-9_+-]*)\n?([\s\S]*?)```/g;
    let m;
    let n = 0;
    while ((m = fence.exec(display))) {
      if (n === Number(codeId)) return m[2].replace(/\n$/, "");
      n++;
    }
    return "";
  }

  function parseQuotaAnswer(text) {
    const s = String(text || "").trim().toLowerCase().replace(",", ".");
    const m = s.match(/(\d+(?:\.\d+)?)\s*(gb|g)?\b/);
    if (!m) return 0;
    const n = Number(m[1]);
    return n > 0 ? n : 0;
  }

  async function releaseTyping(pack, aiLog) {
    if (pack.typing === false && !pack.typingOut) return;
    pack.typing = true;
    pack.typingOut = true;
    if (aiLog) paintAIChat(aiLog, pack);
    await sleep(320);
    pack.typing = false;
    pack.typingOut = false;
    if (aiLog) paintAIChat(aiLog, pack);
    await sleep(1000);
  }

  async function pushBot(pack, text, aiLog) {
    const parts = splitBotBubbles(text);
    await releaseTyping(pack, aiLog);
    for (const t of parts) {
      if (!t) continue;
      pack.bubbles.push({ role: "bot", text: t, enter: true });
      if (aiLog) paintAIChat(aiLog, pack);
      await sleep(220);
    }
  }

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

  async function typeCommand(tout, prompt, cmd) {
    if (!tout) return;
    tout.textContent += prompt;
    tout.scrollTop = tout.scrollHeight;
    await sleep(320);
    const s = String(cmd);
    const base = s.length > 140 ? 7 : 18;
    for (let i = 0; i < s.length; i++) {
      tout.textContent += s[i];
      tout.scrollTop = tout.scrollHeight;
      let d = base + Math.random() * (base + 8);
      if (s[i] === " ") d += 40;
      if ("/;|&".includes(s[i])) d += 55;
      await sleep(d);
    }
    await sleep(220);
    tout.textContent += "\n";
    tout.scrollTop = tout.scrollHeight;
  }

  async function typeDraftCommand(cmd, alive) {
    const inp = document.querySelector("#term-cmd");
    if (!inp) return;
    inp.value = "";
    inp.focus();
    const s = String(cmd || "");
    const base = s.length > 80 ? 8 : 16;
    for (let i = 0; i < s.length; i++) {
      if (alive && !alive()) return;
      inp.value += s[i];
      let d = base + Math.random() * (base + 6);
      if (s[i] === " ") d += 30;
      await sleep(d);
    }
  }

  function bindAgentChat({ key, stillHere, aiPath, execFn, quotaRoot, termOut, prompt, onImage, onStart, onToken, onQuota, onTermLine, onAction, hello, logMode, seedContext }) {
    const pack = roomAIState(key);
    if (!pack.welcomed) {
      pack.welcomed = true;
      if (!(pack.bubbles || []).length) {
        pack.bubbles.push({ role: "bot", text: hello || AGENT_HELLO, enter: true });
      }
    }
    if (seedContext && !pack.contextSeeded) {
      pack.contextSeeded = true;
      pack.messages.push({ role: "user", text: seedContext });
    }
    const aiLog = document.querySelector("#ai-log");
    const mapLogKind = (text) => {
      const t = String(text || "").trim().toLowerCase();
      if (t === "panel" || t === "panel events") return "panel";
      if (t === "api") return "api";
      if (t === "deploy") return "deploy";
      if (t === "host" || t === "host events") return "host";
      return "";
    };
    const applyQuotaValue = async (gb) => {
      const n = Number(gb);
      if (!(n > 0)) return 0;
      pack.quotaGB = n;
      const hidden = document.querySelector("#pull-quota") || document.querySelector("[data-quota-input]");
      if (hidden) hidden.value = String(n);
      if (quotaRoot?.matches?.("[data-quota-input]")) quotaRoot.value = String(n);
      else if (quotaRoot?.querySelector) {
        const el = quotaRoot.querySelector("[data-quota-input]");
        if (el) el.value = String(n);
      }
      if (onQuota) await onQuota(n);
      return n;
    };
    const looksLikeFilename = (s) => {
      const t = String(s || "").trim();
      if (!t || t.length > 120 || /\n/.test(t)) return false;
      if (/^(yes|no|ok|read|write|panel|api|deploy|host)$/i.test(t)) return false;
      return /(\.|\/|Dockerfile|Makefile|README|package\.json|requirements|compose|\.py$|\.js$|\.go$|\.env$|\.yml$|\.yaml$|\.json$|\.md$)/i.test(t);
    };
    const wantsFiles = (s) => /اقرا الملفات|اقرأ الملفات|اعرض الملفات|list files|read files|show files|inspect files|الملفات|اقرأ ملف|اقرا ملف/i.test(s)
      || (/(file|files|ملف|ملفات|كود)/i.test(s) && /(list|read|show|inspect|اقرا|اقرأ|اعرض)/i.test(s));
    const sendUserText = async (text) => {
      const t = String(text || "").trim();
      if (!t || pack.busy) return;
      pack.rtl = isRTLText(t);
      const answering = !!(pack.pendingAsk && pack.pendingAsk.length);
      pack.pendingAsk = null;
      pack.pendingChoices = null;
      pack.pendingPicked = [];
      const maybeQ = parseQuotaAnswer(t);
      if (maybeQ > 0) await applyQuotaValue(maybeQ);
      if (logMode) {
        const lk = mapLogKind(t);
        if (lk) pack.attachLogKind = lk;
      }
      if (execFn && answering && looksLikeFilename(t)) pack.awaitFile = t;
      if (execFn && wantsFiles(t)) pack.nudgedLs = false;
      pack.bubbles.push({ role: "user", text: t, enter: true });
      pack.messages.push({ role: answering ? "answers" : "user", text: t });
      paintAIChat(aiLog, pack);
      await runAILoop();
    };
    paintAIChat(aiLog, pack);
    const looksLikeRead = (cmd) => /\b(cat|head|tail|sed\s+-n|awk)\b/.test(String(cmd || ""));
    const runExec = async (cmd, typed) => {
      const head = (prompt || "") + cmd + "\n";
      if (!typed && termOut) {
        termOut.textContent += head;
        termOut.scrollTop = termOut.scrollHeight;
      }
      if (onTermLine) onTermLine(head);
      let lastEx;
      for (let attempt = 0; attempt < 3; attempt++) {
        try {
          if (attempt > 0) await sleep(350 * attempt);
          const res = await execFn(cmd);
          const where = res.where ? `[${res.where}] ` : "";
          let out = where + (res.output || "") + (res.error ? `\n${res.error}` : "");
          const empty = !String(res.output || "").trim() && !String(res.error || "").trim();
          if (empty && looksLikeRead(cmd)) {
            out = `FILE EMPTY: the file has no content at all.`;
          }
          const line = out + (out.endsWith("\n") ? "" : "\n");
          if (termOut) {
            termOut.textContent += (attempt ? `(retry ${attempt}) ` : "") + line;
            termOut.scrollTop = termOut.scrollHeight;
          }
          if (onTermLine) onTermLine(line);
          return { exit: empty && looksLikeRead(cmd) ? 0 : (res.exit ?? (res.error ? 1 : 0)), output: out, empty: empty && looksLikeRead(cmd) };
        } catch (ex) {
          lastEx = ex;
          const msg = String(ex.message || ex);
          if (!/Failed to fetch|NetworkError|network|timeout|502|503|504/i.test(msg) || attempt === 2) {
            break;
          }
        }
      }
      const err = (lastEx?.message || lastEx || "exec failed") + "\n";
      if (termOut) termOut.textContent += err;
      if (onTermLine) onTermLine(err);
      return { exit: 1, output: String(lastEx?.message || lastEx || "exec failed") };
    };
    const stopLoop = async () => {
      if (!pack.busy) return;
      try { pack.abort?.abort(); } catch {}
      pack.run += 1;
      pack.busy = false;
      pack.typing = false;
      pack.typingOut = false;
      pack.status = "";
      pack.abort = null;
      pack.bubbles.push({ role: "bot", text: "Stopped.", enter: true });
      paintAIChat(aiLog, pack);
    };
    const runAILoop = async () => {
      if (pack.busy) return;
      pack.busy = true;
      pack.typing = true;
      pack.pendingAsk = null;
      pack.pendingChoices = null;
      pack.status = agentStatus(pack, "think");
      const run = ++pack.run;
      pack.abort = typeof AbortController !== "undefined" ? new AbortController() : null;
      const alive = () => pack.run === run && pack.busy;
      paintAIChat(aiLog, pack);
      try {
        for (let i = 0; i < 40; i++) {
          if (!alive() || (stillHere && !stillHere())) return;
          if (pack.messages.length > 40) pack.messages = pack.messages.slice(-40);
          pack.typing = true;
          pack.status = agentStatus(pack, i === 0 ? "think" : "read");
          paintAIChat(aiLog, pack);
          const payload = { messages: pack.messages };
          if (logMode && pack.attachLogKind) payload.log_kind = pack.attachLogKind;
          const res = await api(aiPath, {
            method: "POST",
            body: JSON.stringify(payload),
            signal: pack.abort?.signal,
          });
          if (!alive()) return;
          const say = String(res.say || "").trim();
          const cmd = String(res.command || "").trim();
          const img = String(res.image || "").trim();
          const ask = Array.isArray(res.ask) ? res.ask.map((x) => String(x || "").trim()).filter(Boolean).slice(0, 1) : [];
          let choices = Array.isArray(res.choices) ? res.choices.map((x) => String(x || "").trim()).filter(Boolean).slice(0, 8) : [];
          const action = String(res.action || "").trim().toLowerCase();
          const logKind = String(res.log_kind || "").trim().toLowerCase();
          pack.messages.push({
            role: "assistant",
            text: JSON.stringify({
              say, says: res.says || [], command: cmd, type_only: !!res.type_only, ask, choices, quota_gb: Number(res.quota_gb || 0),
              image: img, start: !!res.start, action, log_kind: logKind, done: !!res.done,
            }),
          });
          pack.status = "";
          if (execFn && pack.awaitFile && !cmd) {
            const f = pack.awaitFile;
            pack.awaitFile = "";
            pack.messages.push({
              role: "terminal",
              text: `SYSTEM: User chose file ${f}. You MUST set command to print it with the terminal tool, e.g. head -n 200 -- ${f}   Empty ask. Do not invent contents.`,
            });
            pack.typing = true;
            paintAIChat(aiLog, pack);
            continue;
          }
          if (execFn && !cmd && !ask.length && !choices.length && !pack.nudgedLs) {
            const lastU = [...pack.messages].reverse().find((m) => m.role === "user" || m.role === "answers");
            if (lastU && wantsFiles(lastU.text)) {
              pack.nudgedLs = true;
              pack.messages.push({
                role: "terminal",
                text: "SYSTEM: Use the terminal tool. Set command to list files (ls -la or find . -maxdepth 2 -type f). Next turn put names in choices. Do not invent names or contents.",
              });
              pack.typing = true;
              paintAIChat(aiLog, pack);
              continue;
            }
          }
          if (cmd) pack.awaitFile = "";
          const msgs = collectBotMessages(res).filter((m) => {
            if (!ask.length) return true;
            const q = ask[0].toLowerCase();
            const t = String(m || "").toLowerCase();
            if (!t || t === q) return false;
            const n = Math.min(22, q.length, t.length);
            return n < 12 || !(t.includes(q.slice(0, n)) || q.includes(t.slice(0, n)));
          });
          if (msgs.length || say) await pushBot(pack, msgs.length ? msgs.join("\n\n") : say, aiLog);
          else await releaseTyping(pack, aiLog);
          if (ask.length) {
            const q = ask[0];
            const nameQ = /name|اسم|سمي|سمّ/i.test(q);
            if (nameQ && choices.length && choices.every((c) => /^(read|write|both)$/i.test(c))) {
              choices = [];
            }
            if (!choices.length && logMode && !nameQ) {
              choices = ["Panel", "API", "Deploy", "Host events"];
            }
            pack.pendingAsk = [q];
            pack.pendingChoices = choices.length ? choices : null;
            pack.pendingPicked = [];
            paintAIChat(aiLog, pack);
            break;
          }
          if (!ask.length && choices.length) {
            pack.pendingAsk = [say || "Choose:"];
            pack.pendingChoices = choices;
            pack.pendingPicked = [];
            paintAIChat(aiLog, pack);
            break;
          }
          paintAIChat(aiLog, pack);
          if (logMode && logKind && pack.attachLogKind !== logKind) {
            pack.attachLogKind = logKind;
            pack.messages.push({ role: "answers", text: logKind });
            pack.typing = true;
            continue;
          }
          if (logMode) pack.attachLogKind = "";
          const wantQ = Number(res.quota_gb || 0);
          if (wantQ > 0) {
            pack.status = agentStatus(pack, "quota");
            pack.typing = true;
            paintAIChat(aiLog, pack);
            try {
              const saved = await applyQuotaValue(wantQ);
              pack.messages.push({ role: "terminal", text: `QUOTA set: ${saved.toFixed(1)} GB` });
            } catch (ex) {
              pack.messages.push({ role: "terminal", text: `QUOTA save failed: ${ex.message}` });
              await pushBot(pack, "Could not save disk quota: " + (ex.message || ""), aiLog);
            }
            pack.status = "";
            pack.typing = false;
            paintAIChat(aiLog, pack);
          }
          if ((action === "pause" || action === "resume") && onAction) {
            if (action === "pause") {
              const ok = await confirmAction({
                title: "Pause this project?",
                body: "The container will stop. You can resume it later. The room is not deleted.",
                ok: "Pause",
                danger: true,
              });
              if (!ok) {
                pack.status = "";
                pack.typing = false;
                await pushBot(pack, "Pause cancelled.", aiLog);
                paintAIChat(aiLog, pack);
                continue;
              }
            }
            try {
              pack.status = action === "pause" ? "Pausing…" : "Starting…";
              pack.typing = true;
              paintAIChat(aiLog, pack);
              await onAction(action);
              pack.messages.push({ role: "terminal", text: `ACTION ${action} ok` });
              pack.status = "";
              pack.typing = false;
              await pushBot(pack, action === "pause" ? "Paused." : "Running again.", aiLog);
              paintAIChat(aiLog, pack);
            } catch (ex) {
              pack.status = "";
              pack.typing = false;
              await pushBot(pack, ex.message || "Action failed", aiLog);
              pack.messages.push({ role: "terminal", text: `ACTION failed: ${ex.message || ex}` });
              paintAIChat(aiLog, pack);
              continue;
            }
          }
          if (img && onImage) onImage(img);
          if (res.token && onToken) onToken(res);
          const typeOnly = !!(res.type_only || res.draft);
          const draftCmd = cmd || String(res.draft || "").trim();
          if (typeOnly && draftCmd && execFn) {
            pack.status = agentStatus(pack, "type");
            pack.typing = false;
            paintAIChat(aiLog, pack);
            await typeDraftCommand(draftCmd, alive);
            if (!alive()) return;
            pack.messages.push({ role: "terminal", text: `TYPED (not sent): ${draftCmd}` });
            await pushBot(pack, "Typed in the terminal — send it yourself when you want.", aiLog);
            break;
          }
          if (draftCmd && execFn) {
            pack.status = agentStatus(pack, "type");
            pack.typing = false;
            paintAIChat(aiLog, pack);
            await typeCommand(termOut, prompt, draftCmd);
            if (!alive()) return;
            pack.status = agentStatus(pack, "run");
            paintAIChat(aiLog, pack);
            const execRes = await runExec(draftCmd, true);
            if (!alive()) return;
            let termText = `exit ${execRes.exit}\n${execRes.output}`.slice(0, 12000);
            if (execRes.empty) {
              termText = `FILE EMPTY: the file has no content at all.\nCommand: ${draftCmd}`;
            }
            pack.messages.push({
              role: "terminal",
              text: termText,
            });
            if (onStart && /docker\s+(pull|build)|git\s+clone/i.test(draftCmd) && !(pack.quotaGB > 0)) {
              pack.messages.push({
                role: "terminal",
                text: "SYSTEM: Install command finished. You MUST now ask how many GB for this project using ask + choices (0.5 GB, 1 GB, 2 GB, 5 GB, 10 GB). Empty command. Do not start until quota_gb is set.",
              });
            }
            if (res.done) break;
            pack.typing = true;
            continue;
          }
          if (res.start && onStart) {
            const imgUse = img || String(document.querySelector("#pull-image")?.value || "").trim();
            const q = Number(pack.quotaGB || document.querySelector("#pull-quota")?.value || 0);
            if (!(q > 0)) {
              pack.messages.push({
                role: "terminal",
                text: "SYSTEM: start blocked — quota_gb is missing. Ask the user disk size with ask+choices, wait, then set quota_gb. Do not start yet.",
              });
              pack.typing = true;
              continue;
            }
            if (imgUse) {
              try {
                pack.status = agentStatus(pack, "start");
                pack.typing = true;
                paintAIChat(aiLog, pack);
                await onStart(imgUse, q);
                pack.status = "";
                pack.messages.push({ role: "terminal", text: `START requested for ${imgUse}` });
              } catch (ex) {
                pack.status = "";
                pack.typing = false;
                await pushBot(pack, ex.message || "Start failed", aiLog);
                pack.messages.push({ role: "terminal", text: `START failed: ${ex.message || ex}` });
                paintAIChat(aiLog, pack);
                continue;
              }
            }
            if (!res.done) {
              pack.typing = true;
              continue;
            }
          }
          if (wantQ > 0 && !res.done) {
            pack.typing = true;
            continue;
          }
          if ((action === "pause" || action === "resume") && onAction && !res.done) {
            pack.typing = true;
            continue;
          }
          break;
        }
      } catch (ex) {
        if (!(ex?.name === "AbortError" || /abort/i.test(String(ex.message || "")))) {
          pack.typing = false;
          await pushBot(pack, ex.message || "Agent failed", aiLog);
        }
      } finally {
        if (pack.run === run) {
          pack.busy = false;
          pack.typing = false;
          pack.status = "";
        }
        paintAIChat(aiLog, pack);
      }
    };
    document.querySelector("#ai-form")?.addEventListener("submit", async (e) => {
      e.preventDefault();
      if (pack.busy) {
        await stopLoop();
        return;
      }
      const inp = document.querySelector("#ai-q");
      const text = String(inp?.value || "").trim();
      if (!text) return;
      inp.value = "";
      if (inp.tagName === "TEXTAREA") {
        inp.style.height = "auto";
      }
      const btn = document.querySelector("#ai-send");
      btn?.classList.add("is-press");
      setTimeout(() => btn?.classList.remove("is-press"), 180);
      await sendUserText(text);
    });
    const aiInp = document.querySelector("#ai-q");
    aiInp?.addEventListener("input", () => syncAgentComposer(pack));
    aiInp?.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        document.querySelector("#ai-form")?.requestSubmit();
      }
    });
    aiLog?.addEventListener("click", async (e) => {
      const codeBtn = e.target.closest("[data-copy-code]");
      if (codeBtn) {
        const [bi, ci] = String(codeBtn.dataset.copyCode || "").split(":");
        const code = codeFromBubble(pack, Number(bi), Number(ci));
        if (code) {
          await copyText(code);
          codeBtn.querySelector(".ai-codecopy-lab")?.replaceChildren(document.createTextNode("Copied"));
          if (!codeBtn.querySelector(".ai-codecopy-lab")) codeBtn.textContent = "Copied";
          codeBtn.classList.add("is-copied");
          setTimeout(() => {
            const lab = codeBtn.querySelector(".ai-codecopy-lab");
            if (lab) lab.textContent = "Copy";
            else codeBtn.textContent = "Copy";
            codeBtn.classList.remove("is-copied");
          }, 1200);
        }
        return;
      }
      const copyBtn = e.target.closest("[data-copy-bubble]");
      if (copyBtn) {
        const idx = Number(copyBtn.dataset.copyBubble);
        const bubble = pack.bubbles[idx];
        if (bubble?.text) {
          await copyText(normalizeBotText(bubble.text));
          copyBtn.textContent = "Copied";
          setTimeout(() => { copyBtn.textContent = "Copy"; }, 1200);
        }
        return;
      }
      const choice = e.target.closest("[data-ai-choice]");
      if (choice && !pack.busy) {
        const val = choice.dataset.aiChoice;
        const cur = new Set(pack.pendingPicked || []);
        if (cur.has(val)) cur.delete(val);
        else cur.add(val);
        pack.pendingPicked = [...cur];
        paintAIChat(aiLog, pack);
        return;
      }
      const go = e.target.closest("[data-ai-choose-go]");
      if (go && !pack.busy) {
        const picked = pack.pendingPicked || [];
        if (!picked.length) return;
        let answer = picked.join(", ");
        const low = picked.map((x) => String(x).toLowerCase());
        if (low.includes("both") || (low.includes("read") && low.includes("write"))) {
          answer = "both";
        }
        pack.pendingPicked = [];
        await sendUserText(answer);
      }
    });
    return { run: runAILoop, pack, send: sendUserText, stop: stopLoop };
  }

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
      case "tokens": return "/tokens";
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
    if (p === "/tokens" || p === "/api") return { view: "tokens" };
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
      <p class="auth-kicker">${brandMarkHTML()}VPS Manager</p>
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
            <div class="meta">Panel :9090</div>
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

    const nav = root.querySelector("#nav");
    const items = isOwner
      ? [["server", "Server"], ["rooms", "Projects"], ["deploy", "Deploy"], ["restore", "Backup"], ["logs", "Logs"], ["docs", "Docs"], ["tokens", "Tokens"], ["settings", "Settings"]]
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
            fact("Panel", "VPS Manager · :9090"),
          ])}
        </div>
      </div>
      ${ready ? agentDeskHTML({
        title: "Ai Agent | Usage",
        showTerm: false,
        placeholder: "Ask about CPU, RAM, disk, load…",
      }) : ""}`, "server");
      bindCopyables();
      updateMetricsDOM();
      if (ready) {
        bindAgentChat({
          key: "usage",
          stillHere: () => state.view === "server",
          aiPath: "/api/usage/ai",
          hello: USAGE_HELLO,
        });
      }
    };
    paint(state.cache.host || null, false);
    try {
      const host = await api("/api/host");
      if (!alive("server", gen)) return;
      state.cache.host = host;
      paint(host, true);
    } catch (e) {
      if (!alive("server", gen)) return;
      if (!state.cache.host) shell(`<p class="error">${esc(e.message)}</p>`, "server");
      else paint(state.cache.host, true);
    }
  }

  async function renderRooms() {
    const gen = state._gen;
    const paint = (rooms) => {
      if (!rooms) {
        shell(`<div class="topbar"><div><h2>Projects</h2><div class="sub">Rooms on this VPS</div></div>
          <button class="btn primary action" id="go-deploy">Deploy new</button></div>${skel(4)}`, "rooms");
        document.querySelector("#go-deploy")?.addEventListener("click", () => setView("deploy"));
        return;
      }
      const cards = (rooms || []).map((r) => {
        const st = r.status === "running" ? "ok" : r.status === "stopped" ? "stop" : "miss";
        const usedN = Number(r.usage_bytes) || 0;
        const quotaN = Number(r.quota_bytes) || 0;
        const used = quotaN ? `${fmtBytes(usedN)} / ${fmtBytes(quotaN)}` : fmtBytes(usedN);
        const fill = quotaN > 0 ? Math.min(100, Math.round((usedN / quotaN) * 100)) : 0;
        const heat = fill >= 90 ? "hot" : fill >= 70 ? "warm" : "";
        const pw = r.password || "";
        const img = r.image || "";
        const port = Number(r.host_port) || 0;
        return `<article class="proj-card" data-room="${r.id}">
          <div class="proj-card-head">
            ${projectIconHTML(st)}
            <div class="proj-ident">
              <h4>${esc(r.name)}</h4>
              <p class="proj-meta">${img ? `<span class="mono">${esc(img)}</span>` : "Room"}${port ? ` · :${port}` : ""}</p>
            </div>
            <span class="badge ${st}" data-badge>${esc(r.status)}</span>
          </div>
          <div class="proj-stats">
            <div class="proj-stat">
              <span>Disk</span>
              <strong class="mono">${esc(used)}</strong>
              <div class="room-disk ${heat}"><div class="room-disk-bar"><i style="width:${fill}%"></i></div></div>
            </div>
            <div class="proj-stat">
              <span>Password</span>
              <div class="secret-row">
                <span class="secret-mask" aria-hidden="true">••••••••</span>
                <button type="button" class="btn sm action" data-copy="${esc(pw)}" ${pw ? "" : "disabled"}>Copy</button>
              </div>
            </div>
          </div>
          <div class="proj-actions">
            <button class="btn sm primary action" data-enter="${r.id}" data-name="${esc(r.name)}" data-pass="${esc(pw)}">Open</button>
            ${powerToggleHTML(r.id, r.status)}
            <button class="btn sm danger action" data-del="${r.id}">Delete</button>
          </div>
        </article>`;
      }).join("") || `<div class="empty-projects">
          ${brandMarkHTML()}
          <h3>No projects</h3>
          <p class="muted">Deploy an image, set disk quota, and start a room.</p>
          <button class="btn primary action" id="empty-deploy">Deploy new</button>
        </div>`;

      shell(`
        <div class="topbar"><div>
          <h2>Projects</h2>
          <div class="sub">${(rooms || []).length ? `${rooms.length} on this VPS` : "Nothing deployed yet"}</div>
        </div>
        <button class="btn primary action" id="go-deploy">Deploy new</button>
        </div>
        <div class="rooms-grid">${cards}</div>`, "rooms");

      document.querySelector("#go-deploy")?.addEventListener("click", () => setView("deploy"));
      document.querySelector("#empty-deploy")?.addEventListener("click", () => setView("deploy"));
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
          title: "Delete this project?",
          body: "This removes the room and its data. This cannot be undone.",
          ok: "Delete",
          danger: true,
        })) return;
        await api(`/api/rooms/${b.dataset.del}`, { method: "DELETE" });
        b.closest(".proj-card")?.remove();
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
        <div class="sub">Agent in the terminal · no presets · live disk</div>
      </div></div>
      <div class="deploy-layout">
        <div class="panel deploy-card">
          <h3>Pull image</h3>
          ${agentDeskHTML({
            title: "Agent",
            prompt: "root@vps-manager:~#",
            termLines: "ready\n",
            showTerm: true,
            hiddenQuotaId: "pull-quota",
          })}
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
            <div class="field full" style="margin-top:12px">${quotaSliderHTML({ name: "quota_gb", maxGB, valueGB: 0.1, required: true })}</div>
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
    bindAgentChat({
      key: "deploy",
      stillHere: () => state.view === "deploy",
      aiPath: "/api/deploy/ai",
      execFn: (cmd) => api("/api/deploy/exec", {
        method: "POST",
        body: JSON.stringify({ command: cmd, timeout_sec: 600 }),
      }),
      quotaRoot: document.querySelector("#pull-quota"),
      termOut,
      prompt: "root@vps-manager:~# ",
      onImage: (image) => {
        const el = document.querySelector("#pull-image");
        const ready = document.querySelector("#pull-ready");
        const setup = document.querySelector("#pull-setup");
        if (el) el.value = image;
        if (ready) ready.textContent = `Image ready · ${image}`;
        setup?.classList.remove("hidden");
      },
      onStart: async (image, quota) => {
        const name = suggestName(image);
        appendTerm(termOut, `\nStarting ${name}...\n`);
        const text = await streamFetch("/api/deploy", {
          method: "POST",
          body: JSON.stringify({
            image,
            name,
            quota_gb: quota,
            host_port: Number(document.querySelector("#pull-port")?.value || 0) || 0,
            container_port: Number(document.querySelector("#pull-cport")?.value || 80) || 80,
          }),
        }, (chunk) => appendTerm(termOut, chunk));
        showResult(document.querySelector("#pull-done"), parseDeployOK(text));
      },
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
      </div>
      ${agentDeskHTML({
        title: "Logs agent",
        showTerm: false,
        placeholder: "Ask to analyze logs…",
      })}`, "logs");

    const box = document.querySelector("#logbox");
    pinLogBottom(box);

    bindAgentChat({
      key: "logs",
      stillHere: () => state.view === "logs",
      aiPath: "/api/logs/ai",
      hello: LOGS_HELLO,
      logMode: true,
    });

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
      <p class="docs-lead">Paste one command on the VPS. When Docker is ready the script stops and requires a panel password and your Telegram user id. Only after those are saved does it print the panel URL.</p>
      <p class="docs-repo">Repository: <a href="https://github.com/X5Coder/VPS-Manager" target="_blank" rel="noopener">github.com/X5Coder/VPS-Manager</a> · Developer <strong>X5Coder</strong></p>
      <div class="docs-os">
        <div class="panel">
          <h3>Supported</h3>
          <p><strong>Ubuntu 20.04, 22.04, or 24.04</strong> · root access · x86_64 or ARM64.</p>
        </div>
      </div>
      ${step("1", "SSH into the VPS", "On your computer, replace YOUR_VPS_IP with the address from your provider.", "", ssh)}
      ${step("2", "Paste the installer", "As root. One command installs Docker and the panel. Downloads retry on failure.", "", install)}
      ${step("3", "Create the panel password", "Required on first install. At least 8 characters, then confirm. Saved on the VPS.", "", "")}
      ${step("4", "Enter your Telegram user id", "Required. Open Telegram, search @userinfobot, tap Start, paste the numeric Id. Saved on the VPS.", "", "")}
      ${step("5", "Open the printed URL", "The script prints Panel URL last. Telegram bot token → 30-second code → the password you created.", "<p class=\"muted\">Full guide: <a href=\"https://github.com/X5Coder/VPS-Manager/blob/main/docs/INSTALL.md\" target=\"_blank\" rel=\"noopener\">docs/INSTALL.md</a></p>", "")}
      ${step("6", "Ship a project as one image", "Build on your machine, save one .tar file, upload it in Deploy, set disk quota. Or ask the room terminal assistant to clone a repo and dockerize it.", "", saveImg)}
      ${cmdCard("If curl is blocked", alt)}`, "docs");
    bindCmdCopies();
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
      <div class="topbar"><div>
        <h2>Settings</h2>
        <div class="sub">Root SSH · Admin vault · Alerts</div>
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
      const onPage = state.view === "restore";
      if (!onPage) return;
      try {
        const bk = await api("/api/backup/status");
        const sel = "#job-live";
        paintJob(bk.job, sel);
        const btn = document.querySelector("#bak-now");
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
    shell(`
      <div class="topbar restore-hero">
        <div>
          <h2>Backup</h2>
          <div class="sub restore-sub"><span class="restore-live" aria-hidden="true"></span>Enable with a tested GitHub key · runs on the server</div>
        </div>
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
    const canResume = !!bk.can_resume && (job?.status !== "running");
    const resumeKind = bk.resume_kind || "";
    const bakLabel = canResume && resumeKind === "backup" ? "Resume backup" : "Backup now";
    const resumeNote = canResume
      ? (resumeKind === "backup"
        ? `Interrupted backup — ${bk.resume_rooms || 0} room(s) already uploaded. Click Resume to inspect and continue.`
        : `Interrupted restore — ${bk.resume_rooms || 0} room(s) already applied. Click Resume restore to continue.`)
      : (bk.enabled ? "Backup is on — leave anytime and check status here" : "Backup is off until you test a GitHub PAT with repo scope");
    const rows = snaps.map((s) => {
      const resumeThis = canResume && resumeKind === "restore" && bk.resume_snapshot && s.id === bk.resume_snapshot;
      return `<tr>
      <td><strong>${esc(s.label || s.id)}</strong><div class="muted" style="font-size:0.8rem">${esc(s.description || "")}</div></td>
      <td class="muted">${esc(s.created_at || "")}</td>
      <td><span class="badge ${s.status === "ok" ? "ok" : "miss"}">${esc(s.status || "")}</span></td>
      <td><button class="btn sm primary action" data-restore="${esc(s.id)}" ${bk.configured ? "" : "disabled"}>${resumeThis ? "Resume" : "Restore"}</button></td>
    </tr>`;
    }).join("") || `<tr><td colspan="4" class="muted">No local snapshots yet — enable backup, then run Backup now.</td></tr>`;

    const jobHTML = `<div id="job-live">${job ? jobPanelHTML(job) : ""}</div>`;

    shell(`
      <div class="topbar restore-hero ${job && job.status === "running" ? "is-running" : ""}">
        <div>
          <h2>Backup</h2>
          <div class="sub restore-sub"><span class="restore-live" aria-hidden="true"></span>${esc(resumeNote)}</div>
        </div>
        <div class="topbar-actions">
          ${canResume && resumeKind === "restore" ? `<button class="btn action" id="resume-restore">Resume restore</button>` : ""}
          <button class="btn primary action bak-now-btn" id="bak-now" ${!bk.enabled || (job && job.status === "running") ? "disabled" : ""}>
            <span class="bak-now-ring" aria-hidden="true"></span>
            ${esc(bakLabel)}
          </button>
        </div>
      </div>
      <div class="panel bak-switch-card">
        <div class="bak-switch-row">
          <div>
            <h3>GitHub backup</h3>
            <p class="muted">Classic PAT with <span class="mono">repo</span> scope. Tested before it turns on. Creates private repos and pushes files.</p>
          </div>
          <label class="switch" title="Enable backup">
            <input type="checkbox" id="bak-enable" ${bk.enabled ? "checked" : ""} />
            <span class="switch-ui"></span>
          </label>
        </div>
        <p class="ok-text" id="ghsaved">${bk.configured ? `Key saved (${esc(bk.token_hint || "••••")}) for @${esc(bk.github_user || "?")}` : ""}</p>
        <form id="gh-form" class="form-grid" style="margin-top:12px">
          <div class="field full"><label>Account key (GitHub PAT)</label>
            <input name="token" type="password" placeholder="${bk.configured ? "Paste a new key only to replace" : "ghp_…"}" autocomplete="off" /></div>
          <div class="full" style="display:flex;gap:8px;flex-wrap:wrap">
            <button class="btn primary action" type="submit">Test & enable</button>
            ${bk.configured ? `<button class="btn sm danger action" type="button" id="gh-clear">Remove key</button>` : ""}
          </div>
        </form>
        <p class="error" id="gherr"></p>
        <p class="muted" id="ghstatus" style="margin-top:8px">${bk.enabled ? `On · @${esc(bk.github_user || "?")} · last ${esc(bk.last_backup_at || "never")}` : "Off — paste a key and test it to turn on."}</p>
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

    const setBakErr = (msg) => {
      const el = document.querySelector("#gherr");
      if (el) el.textContent = msg || "";
    };
    document.querySelector("#bak-enable")?.addEventListener("change", async (e) => {
      const on = !!e.target.checked;
      setBakErr("");
      const raw = String(document.querySelector("#gh-form [name=token]")?.value || "").trim();
      if (on && !bk.configured && !raw) {
        e.target.checked = false;
        setBakErr("Paste a GitHub classic PAT with repo scope, then turn it on.");
        return;
      }
      try {
        const res = await api("/api/backup/enable", {
          method: "POST",
          body: JSON.stringify({ enabled: on, token: raw }),
        });
        state.backupReady = !!res.configured;
        toast(on ? "Backup enabled" : "Backup turned off");
        renderRestore();
      } catch (ex) {
        e.target.checked = !on;
        setBakErr(ex.message);
      }
    });
    document.querySelector("#gh-form")?.addEventListener("submit", async (e) => {
      e.preventDefault();
      setBakErr("");
      const raw = String(new FormData(e.target).get("token") || "").trim();
      if (!raw && !bk.configured) {
        setBakErr("Paste a GitHub classic PAT with repo scope.");
        return;
      }
      try {
        const res = await api("/api/backup/enable", {
          method: "POST",
          body: JSON.stringify({ enabled: true, token: raw }),
        });
        state.backupReady = !!res.configured;
        toast("Key tested — backup is on");
        renderRestore();
      } catch (ex) { setBakErr(ex.message); }
    });
    bindAction(document.querySelector("#gh-clear"), async () => {
      await api("/api/backup/token", { method: "DELETE" });
      state.backupReady = false;
      toast("GitHub key removed");
      renderRestore();
    });

    if (job && job.status === "running") {
      startJobPoll("restore");
    }

    const startRestore = async (snapshotId, token) => {
      const err = document.querySelector("#bakerr");
      const ok = document.querySelector("#bakok");
      if (err) err.textContent = "";
      ok?.classList.add("hidden");
      const r = await api("/api/backup/restore", { method: "POST", body: JSON.stringify({ token: token || "", snapshot_id: snapshotId }) });
      if (ok) {
        ok.textContent = r.message || "Restore started — inspecting last point.";
        ok.classList.remove("hidden");
      }
      paintJob({
        kind: "restore", status: "running", percent: 1,
        message: "Restore started — this can take several minutes",
        progress: "Inspecting last restore point…", logs: ["Inspecting last restore point…"],
      }, "#job-live");
      startJobPoll("restore");
    };

    bindAction(document.querySelector("#resume-restore"), async () => {
      await startRestore(bk.resume_snapshot || "latest");
    });

    bindAction(document.querySelector("#bak-now"), async () => {
      const err = document.querySelector("#bakerr");
      const ok = document.querySelector("#bakok");
      err.textContent = ""; ok.classList.add("hidden");
      try {
        const res = await api("/api/backup/now", {
          method: "POST",
          body: JSON.stringify({
            label: canResume && resumeKind === "backup" ? "Resume backup" : "Manual backup",
            description: "Inspect last point and continue — 24h timer reset from this moment",
          }),
        });
        ok.textContent = res.message || "Backup started on server.";
        ok.classList.remove("hidden");
        const nowBtn = document.querySelector("#bak-now");
        if (nowBtn) { nowBtn.dataset.lock = "1"; nowBtn.disabled = true; }
        document.querySelector(".restore-hero")?.classList.add("is-running");
        paintJob({
          kind: "backup", status: "running", percent: 1,
          message: "Backup started — this can take several minutes",
          progress: "Inspecting last point…", logs: ["Inspecting last point…"],
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

  function tokenCardHTML(t, opts = {}) {
    const secret = t.secret || opts.secret || "";
    const fresh = opts.fresh ? " tok-fresh" : "";
    const copyVal = secret || "";
    return `<div class="tok-card${fresh}" data-tok-id="${esc(t.id)}">
      <div class="tok-card-top">
        <div>
          <strong>${esc(t.name)}</strong>
          <span class="badge ${t.mode === "read" ? "stop" : "ok"}">${esc(t.mode)}</span>
        </div>
        <div class="row-actions">
          <button class="btn sm action" data-copy-btn="${esc(copyVal)}" ${copyVal ? "" : "disabled"}>Copy</button>
          <button class="btn sm danger action" data-del-tok="${esc(t.id)}">Revoke</button>
        </div>
      </div>
      <div class="secret-row tok-secret-row">
        <span class="secret-mask">${copyVal ? "••••••••••••••••••••" : (esc(t.token_prefix || "••••") + "…")}</span>
      </div>
      <div class="muted" style="font-size:0.75rem;margin-top:6px">created ${esc(t.created_at || "")}${t.last_used_at ? " · last used " + esc(t.last_used_at) : ""}</div>
    </div>`;
  }

  async function renderTokens() {
    const gen = state._gen;
    shell(`<div class="topbar"><div><h2>Tokens</h2><div class="sub">API keys · agent helps you create and use them</div></div></div>${skel(3)}`, "tokens");
    let tokens = [];
    try {
      tokens = await api("/api/settings/tokens");
    } catch (e) {
      if (!alive("tokens", gen)) return;
      shell(`<p class="error">${esc(e.message)}</p>`, "tokens"); return;
    }
    if (!alive("tokens", gen)) return;
    const list = tokens || [];
    const cards = list.map((t) => tokenCardHTML(t)).join("") || `<p class="muted" id="tok-empty">No tokens yet — the agent will create the first one.</p>`;

    shell(`
      <div class="topbar"><div>
        <h2>Tokens</h2>
        <div class="sub">${list.length ? `${list.length} saved` : "Create the first key with the agent"}</div>
      </div>
        <button class="btn primary action" id="tok-new">Create token</button>
      </div>
      <div id="tok-fresh"></div>
      <div id="tok-list" class="tok-list">${cards}</div>
      ${agentDeskHTML({
        title: "Tokens agent",
        showTerm: false,
      })}`, "tokens");

    const paintList = (items, fresh) => {
      const box = document.querySelector("#tok-list");
      if (!box) return;
      box.innerHTML = (items || []).map((t) => tokenCardHTML(t)).join("") || `<p class="muted">No tokens yet.</p>`;
      if (fresh) {
        const top = document.querySelector("#tok-fresh");
        if (top) top.innerHTML = tokenCardHTML(fresh.token || fresh, { secret: fresh.secret, fresh: true });
      }
      bindCopyables();
      document.querySelectorAll("[data-copy-btn]").forEach((b) => {
        if (!b.dataset.copyBtn) return;
        b.onclick = async () => { await copyText(b.dataset.copyBtn); toast("Copied"); };
      });
      document.querySelectorAll("[data-del-tok]").forEach((btn) => bindAction(btn, async () => {
        if (!confirm("Revoke this API token?")) return;
        await api(`/api/settings/tokens/${btn.dataset.delTok}`, { method: "DELETE" });
        renderTokens();
      }));
    };
    paintList(list);

    const agent = bindAgentChat({
      key: "tokens",
      stillHere: () => state.view === "tokens",
      aiPath: "/api/tokens/ai",
      hello: TOKEN_HELLO,
      onToken: (res) => {
        const extras = Array.isArray(res.tokens) ? res.tokens : [];
        const items = extras.length
          ? extras.map((x) => {
              const tok = x.token || x;
              tok.secret = x.secret || tok.secret;
              return tok;
            })
          : [Object.assign({}, res.token || {}, { secret: res.secret })];
        const fresh = items[0] ? { token: items[0], secret: items[0].secret } : res;
        const rest = list.filter((t) => !items.some((n) => n.id && n.id === t.id));
        paintList([...items, ...rest], fresh);
        items.reverse().forEach((tok) => list.unshift(tok));
      },
    });
    document.querySelector("#tok-new")?.addEventListener("click", () => {
      if (agent.pack.busy) return;
      const text = agent.pack.rtl
        ? "اعمل توكن جديد. اسألني مرة واحدة عن الصلاحية (read أو write أو both في نفس التوكن) وبعدين الاسم."
        : "Create a new API token. Ask once for mode (read, write, or both on the same token), then the name.";
      agent.pack.pendingAsk = null;
      agent.pack.bubbles.push({ role: "user", text, enter: true });
      agent.pack.messages.push({ role: "user", text });
      paintAIChat(document.querySelector("#ai-log"), agent.pack);
      agent.run();
    });
    if (!list.length && agent.pack.messages.length === 0) {
      agent.pack.messages.push({
        role: "user",
        text: "I opened Tokens. I have none yet. Help me create the first token. One question at a time, in your own words. both = one token that can read and write.",
      });
      agent.run();
    }
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
            ? `<div class="secret-row"><span class="secret-mask">••••••••</span><button type="button" class="btn sm action" data-copy="${esc(room.password)}">Copy</button></div>`
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
        <p class="muted mono" style="margin-bottom:6px">${esc(envMeta.path || "")}</p>
        <p class="muted" style="margin:0 0 10px;font-size:0.82rem">These are the project secrets the container started with. If this page was empty, the panel now fills it from the running container. After you save, pause then resume so the app reloads the new values.</p>
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
      const prompt = `root@${room.name}:~#`;
      body = agentDeskHTML({
        title: "Agent",
        prompt,
        termLines: lines,
        showTerm: true,
        hints: TERM_HINTS.map((h) => `<button type="button" class="hint" data-hint="${esc(h.cmd)}" title="${esc(h.tip)}">${esc(h.cmd)}</button>`).join(""),
      });
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
        <button data-tab="terminal" class="${tab === "terminal" ? "active" : ""}">Ai Agent | Terminal</button>
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
        if (!await confirmAction({
          title: "Delete this project?",
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
      const termOut = document.querySelector("#term-out");
      const prompt = `root@${room.name}:~# `;
      const persist = (line) => {
        state.termLines = state.termLines || [];
        state.termLines.push(line);
      };
      const agent = bindAgentChat({
        key: id,
        stillHere: () => state.view === "room" && state.roomId === id && (state.roomTab || "") === "terminal",
        aiPath: `/api/rooms/${id}/ai`,
        execFn: (cmd) => api(`/api/rooms/${id}/exec`, {
          method: "POST",
          body: JSON.stringify({
            command: cmd,
            project_id: mainProj?.id || "",
            timeout_sec: 120,
            host: true,
          }),
        }),
        termOut,
        prompt,
        hello: ROOM_HELLO,
        seedContext: `SYSTEM CONTEXT (do not ask again): You are inside room "${room.name}". ` +
          `Disk used ${fmtBytes(room.usage_bytes)} of quota ${room.quota_bytes ? fmtBytes(room.quota_bytes) : "not set"}. ` +
          `Projects: ${(projs || []).map((p) => `${p.name} image=${p.image} status=${p.status} port=${p.host_port || 0} domain=${p.domain || "-"}`).join("; ") || "none"}. ` +
          `If they ask for usage, answer with those numbers in one say. You may edit files via the terminal after reading them. Refuse cloning/downloading new projects. Refuse deleting this room.`,
        onQuota: (gb) => api(`/api/rooms/${id}/quota`, { method: "POST", body: JSON.stringify({ quota_gb: gb }) }),
        onAction: (action) => api(`/api/rooms/${id}/${action}`, { method: "POST" }),
        onTermLine: persist,
      });
      document.querySelectorAll("[data-hint]").forEach((b) => {
        b.onclick = () => {
          const inp = document.querySelector("#term-cmd");
          if (inp) { inp.value = b.dataset.hint; inp.focus(); }
        };
      });
      document.querySelector("#term-form")?.addEventListener("submit", async (e) => {
        e.preventDefault();
        const inp = document.querySelector("#term-cmd");
        const cmd = String(inp?.value || "").trim();
        if (!cmd) return;
        inp.value = "";
        const head = prompt + cmd + "\n";
        if (termOut) {
          termOut.textContent += head;
          termOut.scrollTop = termOut.scrollHeight;
        }
        persist(head);
        try {
          const res = await api(`/api/rooms/${id}/exec`, {
            method: "POST",
            body: JSON.stringify({
              command: cmd,
              project_id: mainProj?.id || "",
              timeout_sec: 120,
            }),
          });
          const where = res.where ? `[${res.where}] ` : "";
          const out = where + (res.output || "") + (res.error ? `\n${res.error}` : "");
          const line = out + (out.endsWith("\n") ? "" : "\n");
          if (termOut) {
            termOut.textContent += line;
            termOut.scrollTop = termOut.scrollHeight;
          }
          persist(line);
          agent.pack.messages.push({ role: "user", text: "I ran this in the terminal: " + cmd });
          agent.pack.messages.push({
            role: "terminal",
            text: `exit ${res.exit ?? (res.error ? 1 : 0)}\n${out}`.slice(0, 12000),
          });
        } catch (ex) {
          const err = (ex.message || ex) + "\n";
          if (termOut) termOut.textContent += err;
          persist(err);
        }
      });
      document.querySelector("#term-cmd")?.focus();
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
      if (state.view === "tokens") return renderTokens();
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

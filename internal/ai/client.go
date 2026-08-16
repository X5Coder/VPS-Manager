package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"
	"unicode"
)

const (
	AIBase   = "https://api.studixzone.com/random/"
	MaxTurns = 40
	slotWait = 16 * time.Second
)

// Preferred models only (multisearch 6–10, seek 11–16, chatbot 17–22).
var aiSlotGroups = [][]int{
	{6, 7, 8, 9, 10},
	{11, 12, 13, 14, 15, 16},
	{17, 18, 19, 20, 21, 22},
}

type Message struct {
	Role string `json:"role"` // user | assistant | terminal | answers
	Text string `json:"text"`
}

type Reply struct {
	Say            string   `json:"say"`
	Says           []string `json:"says"`
	Command        string   `json:"command"`
	Ask            []string `json:"ask"`
	Choices        []string `json:"choices"`
	QuotaGB        float64  `json:"quota_gb"`
	Image          string   `json:"image"`
	UpdateID       string   `json:"update_id"` // room id or project id — publish image onto that project
	Start          bool     `json:"start"`
	Done           bool     `json:"done"`
	Action         string   `json:"action"`   // pause | resume | ""
	LogKind        string   `json:"log_kind"` // panel | api | deploy | host
	CreateToken    bool     `json:"create_token"`
	CreateRoom     bool     `json:"create_room"`
	RoomName       string   `json:"room_name"`
	RoomPassword   string   `json:"room_password"`
	ContainerPort  int      `json:"container_port"`
	TokenName      string   `json:"token_name"`
	TokenMode      string   `json:"token_mode"`
	TokenProjectID string   `json:"token_project_id"`
	Tool           string   `json:"tool"`
	ToolArg        string   `json:"tool_arg"`
	TypeOnly       bool     `json:"type_only"` // fill the terminal input, do not run
}

type apiResp struct {
	Response string `json:"response"`
	Text     string `json:"text"`
}

const systemPrompt = `You are the VPS Manager Deploy agent. You deploy NEW projects and you UPDATE existing ones by their id. You do not handle logs or tokens.

Language: match the user's last message. If they write Arabic, "say" is natural spoken Arabic. If English, natural English. Do not mix. JSON keys stay English. In "say", use **bold** for important words. Short paragraphs. Wrap commands, ids, and names in markdown code spans. Code samples MUST use fenced blocks inside "say". Never dump the JSON wrapper into "say". Keep JSON valid (escape newlines in strings).

Always reply with ONLY one JSON object (no markdown fences around the JSON itself):
{"say":"first spoken bubble","says":[],"command":"","type_only":false,"ask":[],"choices":[],"quota_gb":0,"image":"","update_id":"","start":false,"done":false}

YOU ARE ALREADY ON THIS VPS. The panel terminal is a root shell on this host. That is your power — use it.
NEVER ssh, sshpass, scp, or sftp. NEVER put a password in a command. NEVER invent long -o flag lists. NEVER repeat ControlMaster/ControlPath.
If the user asks to SSH to this server (any IP): say you are already connected here, then run the LOCAL command they wanted (docker ps, df -h, ls, …).
If they only want a command typed (not run): type_only true and a SHORT command with no password and no extra -o flags.
Do not refuse ordinary shell work. You can run any normal command except the Never list below.

You work in a LOOP until the task is done or the user stops you:
1) One "say" (what you are doing / what happened). "says" stays empty.
2) Then either: ask a question (ask+choices, empty command, wait) OR run one "command" OR type_only.
3) After TERMINAL comes back: say what happened, then the next command. Keep looping. Do not stop after one command if more work remains. Set done true only when the job is finished or you are waiting on the user.
4) You MAY pause mid-job to ask. Empty command while asking.
5) type_only: if the user wants a command written in the terminal WITHOUT running it, set command to that text and type_only true. The panel types it into the input and does not send. Then done true.
6) Fix failures from TERMINAL. Do not repeat a failed command unchanged.

Be a practical operator: inspect, then act. Prefer doing the next real step over explaining what you could do.

IDs:
- SYSTEM-NOTE lists every project: id (use this), project_id, name, image, ports, quota, status.
- Show the id when you talk about a project. Another AI uses that same id to PATCH or to publish a Docker update.
- NEW deploy: update_id empty, image + quota_gb + start true. Creates a new room.
- UPDATE existing: set update_id to that project's id (room id or project_id). image = the tag you pulled/built. start true. Do NOT create a second room. Keep the same id, ports, env, password. quota_gb 0 on update (keeps current disk). If they also want a new disk size, set quota_gb.

GitHub → Docker:
- Public clone: git clone --depth 1 URL
- Inspect files, write a Dockerfile only if missing, docker build -t name:latest .
- Then set "image" to that tag.
- Private repos need a token they provide.

Disk quota is MANDATORY for a NEW project after a real install command:
- After you actually run git clone, docker pull, or docker build (you received TERMINAL for it) AND this is a new room: STOP. Do not start. Ask how many GB with ask + choices like "0.5 GB","1 GB","2 GB","5 GB","10 GB" (all ≤ free in SYSTEM-NOTE). Wait.
- Then set quota_gb to their number. Only then start.
- Never start a NEW room with quota_gb 0. Never invent GB.
- Updates skip the quota question unless they asked to change disk.

No defaults — ever:
- Do not assume image names, tags, ports, GB, Dockerfiles, or repo URLs.
- No URL? SEARCH GitHub then ask which repo with choices.
- Disk numbers ONLY from SYSTEM-NOTE.
- "image": only a tag you actually pulled or built.
- "start": true only when image is ready (and quota for new rooms).
- Do not docker compose up unrelated stacks.

Fields: command = at most ONE shell command or empty. type_only true = type into the terminal input only (do not run). ask = at most one question. choices = tap answers (user may pick more than one). done = true when this job is finished or you are waiting.

Never: rm -rf /, mkfs, dd onto disks, shutdown/reboot. Keep JSON valid.`

const RoomPrompt = `You are the VPS Manager room agent. You are INSIDE one project room only. You already know its id from SYSTEM-NOTE.

Language: match the user. Arabic or English — do not mix. JSON keys stay English. In "say", use **bold** and markdown code spans. Code samples MUST use fenced blocks inside "say". Keep JSON valid.

Always reply with ONLY one JSON object:
{"say":"one complete spoken reply","says":[],"command":"","type_only":false,"tool":"","tool_arg":"","ask":[],"choices":[],"quota_gb":0,"image":"","start":false,"action":"","done":false}

YOU ARE ALREADY ON THIS VPS, inside this room's isolated workspace. Commands you set run in the room terminal (room-host directory, or docker exec only when they pick a healthy container). Docker CLI (docker ps, build, compose) runs on the host in this room's folder.
NEVER ssh / sshpass / scp. NEVER put passwords in commands. NEVER invent long ssh -o flags.
If they ask to SSH here: you are already connected — run the local command instead.
Empty room bootstrap (LOOP until the stack is up):
1) git clone a public repo they name, OR tell them to upload a .tar / .tar.gz on Overview.
2) Inspect: ls, find compose.yml/docker-compose.yml/Dockerfile.
3) If compose with more than one service → this is multi. If one Dockerfile/one image → single.
4) docker compose build / docker build as needed. docker compose up -d OR they publish via image+start.
5) After clone/build, ASK quota/ports/env if still missing. Keep looping. Put every real action in "command" — the panel types it into the terminal. Put a short human explanation in "say" of what that command does (the chat shows "say"; the terminal shows the command).
You MAY run any normal command in this room (ls, cat, python, git, docker …). One command per turn.

Mini-harness tools (empty command while calling a tool). After TOOL result arrives as TERMINAL, answer with those numbers:
- tool=project_detail  — this room usage vs host CPU/RAM/disk
- tool=host_stats      — live host snapshot for comparison
When they ask how much this project uses, or to compare with the server: call the tool first, then explain.

You work in a LOOP until the task is done or the user hits Stop:
1) One "say". "says" MUST stay [].
2) Then the next step: tool OR command (runs) OR type_only OR ask. After TERMINAL/TOOL, continue. Do not stop after one step if the job is unfinished.
3) done true only when finished or waiting on the user.
4) type_only: WRITE a command in the terminal without sending — command text, type_only true, empty ask, done true.

Speech: if they asked to analyze usage, call project_detail (and host_stats if comparing), then ANSWER with the TOOL numbers. Do not stall. Do not invent.

Scope:
- You already know this room from SYSTEM-NOTE (id, project_id, image, ports, quota, password is NOT in your note). Act as its operator.
- MAY: files via terminal, edit files via terminal, run commands, analyze THIS room usage, raise quota (quota_gb — Save applies the live disk cap), pause/resume.
- MAY publish a Docker update to THIS same project: docker pull or docker build, then set image to that tag and start true. Same id. Do not create a new room. Do not git clone a different app.
- Tell them they can also drop a docker save .tar on Overview → Update image (live log). GitHub: create an API bound to this project, Copy script into .github/workflows/vps-deploy.yml.
- MUST refuse: delete this room, other rooms, tokens, host-wide logs, unrelated general chat. One short refusal, then what you can do here.
- Name and password are edited in the project page (or tell them the fields). You may explain how.
- Host CPU in SYSTEM-NOTE may jump second-to-second (normal VPS sampling). Real pressure = load1 staying above CPU cores, or disk/RAM high.

Quota: ask+choices for GB (<= max in SYSTEM-NOTE), wait, then quota_gb. The panel applies that cap to the running container.
Power: action "pause" or "resume" only when they ask. Never delete.

Golden tools = "tool" then "command". File contents are NEVER attached. Fetch then edit.

Read:
1) List: command ls -la OR find . -maxdepth 2 -type f. Wait TERMINAL.
2) Ask which file; choices = real names (user may pick several). Wait.
3) Print with head -n 200 -- PATH. Wait.
4) Explain real TERMINAL. FILE EMPTY means empty — never invent.

Edit a file they named (professional, via terminal only):
1) If you do not have current contents, command to read it first (head/cat). Wait.
2) Then ONE command that writes the new file. Prefer python3:
python3 - <<'PY'
from pathlib import Path
p = Path("RELATIVE/PATH")
p.write_text("""NEW FULL CONTENTS""", encoding="utf-8")
print("wrote", p, "bytes", p.stat().st_size)
PY
Or: printf '%s\n' 'line' > file   for tiny files.
3) After TERMINAL, confirm what changed. Never invent a write that did not run.
Do not use rm -rf on the project root.

Commands: at most ONE per turn. type_only true = fill the terminal input, do not execute. ask at most one. done true when this job is finished or waiting.

Never: mkfs, shutdown, delete this room. git clone is ALLOWED when they asked to bring a project into THIS empty room.`

const ManagerPrompt = `You are the single VPS Manager AI agent for the whole panel. You may use tools for any room.

Language: match the user. JSON keys English. Reply with ONLY one JSON object:
{"say":"...","says":[],"command":"","tool":"","tool_arg":"","ask":[],"choices":[],"done":false}

Tools (set tool + tool_arg, empty command): list_rooms, get_room, get_room_resources, list_containers, list_images, list_volumes, list_env, get_logs, vps_logs, read_compose, validate_compose, analyze_services, get_vps_status, get_cpu, get_ram, get_storage, get_docker_status, docs.

When you need data, call a tool first. After TERMINAL/TOOL text arrives, answer. done true when finished.
Never delete rooms. Never invent numbers.`

const LogsPrompt = `You are the VPS Manager Logs agent. You ONLY analyze panel logs (Panel, API, Deploy, Host events). Refuse anything else. Short refusal.

Language: match the user. In "say", use **bold** for important findings. Log/code snippets MUST use fenced blocks inside "say". Never dump raw JSON into "say".

Always reply with ONLY one JSON object:
{"say":"spoken analysis","says":[],"ask":[],"choices":[],"log_kind":"","done":false}

Speech: extra bubbles in "says". Use \\n for new lines inside JSON strings. Never dump JSON into say.
Question tool: after analysis, ask a follow-up with ask + choices (what to check next). Wait.

Rules:
- If the user wants analysis and no log is loaded yet, ask which log and set choices exactly: ["Panel","API","Deploy","Host events"]. Wait.
- When they pick one, set log_kind to one of: panel | api | deploy | host (map labels to those keys). Empty command work — the panel will attach that log next turn.
- After you receive a LOG excerpt in the conversation, explain clearly: what happened, errors, warnings, and what to do next. Keep it practical.
- Do not invent log lines. Only use the provided excerpt.
- ask at most one question. done true when finished.`

const TokenPrompt = `You are the VPS Manager API docs agent. You explain the FULL public API, GitHub deploy, empty rooms, and tokens. You CAN create a token (name only) or an empty room. You are not a refusal bot.

Language: match the user. Arabic or English — do not mix. JSON keys stay English. One spoken "say". "says" MUST stay []. Use **bold**, ` + "`code`" + `, and fenced curl/yaml inside say. Keep JSON valid (\\n for newlines).

Always reply with ONLY one JSON object:
{"say":"full useful answer","says":[],"tool":"","tool_arg":"","ask":[],"choices":[],"create_room":false,"room_name":"","room_password":"","quota_gb":0,"container_port":0,"create_token":false,"token_name":"","done":false}

Mini-harness: BEFORE a long how-to, fetch the docs section with a tool (empty create_*). TOOL comes back as TERMINAL. Then explain in the user's language using that text. Do not invent endpoints.

Tools (tool_arg = section id):
- docs_overview
- docs_token          (create API from Tokens page)
- docs_github         (Copy script into the project repo)
- docs_update         (update an existing room via tar / GitHub / ROOM_ID)
- docs_create_room    (empty room without a project yet)
- docs_list           (list rooms)
- docs_exec           (exec + storage + quota PATCH)
- docs_full           (entire API brief)

Map the question: "how do I update" → docs_update; "create token / from here" → docs_token; "github / workflow" → docs_github; "empty room" → docs_create_room; "list rooms" → docs_list; "everything" → docs_full.

Never invent BASE, TOKEN, ids, GB, or passwords. Use SYSTEM-NOTE + TOOL. Never print a guessed secret. Never DELETE.

CREATE TOKEN HERE
Ask for a short name if missing. Then create_token true with token_name only. One token = all rooms. Tell them to open Tokens (this page) if they need the UI.

CREATE EMPTY ROOM HERE
Need unique name (A-Za-z0-9_-), quota_gb > 0 and ≤ quota_available_gb, password ≥ 6 chars. Then create_room true.

If they want a token: name → create_token.
If they want a new empty room: collect name+GB+password, then create_room.
Do not refuse with “I only create tokens”.

ask at most one. create_* false while asking or while tool is set. done true when the answer is complete or you created something.`

const UsagePrompt = `You are the VPS Manager server harness. You do NOT type shell here — you CALL TOOLS in a loop, then answer from TOOL text. Never invent numbers. Never SSH. Never dump JSON into "say".

Language: match the user. JSON keys English. In "say", **bold** numbers and risk.

Always reply with ONLY one JSON object:
{"say":"spoken analysis","says":[],"tool":"","tool_arg":"","ask":[],"choices":[],"done":false}

Tools (one per turn; empty command; wait for TOOL):
- list_projects / list_rooms
- project_detail / get_room     tool_arg = name or id
- host_stats / get_vps_status
- get_cpu  get_ram  get_storage  get_docker_status
- docker_ps                     all containers the panel knows
- vps_logs                      recent host/panel events (tool_arg optional: panel|api|host)

Loop (harness):
1) Short say ("Checking live CPU…") + one tool.
2) After TOOL, either another tool or the final answer with done true.
3) Chain tools until you can answer with real numbers. Do not stop after the first tool if the question needs more.

Danger: disk/RAM >= 90%, CPU >= 90% sustained, load1 >> cores. Watch 70–89%.

ask + choices when useful. done true when the answer is complete.`

const HostPrompt = `You are the VPS Manager host terminal agent. You sit on the live VPS as root. The panel terminal is already a real shell on this machine (cwd /root). NEVER ssh/sshpass/scp. NEVER put a password in a command.

Language: match the user. JSON keys English. Code samples in fenced blocks inside "say". Keep JSON valid.

Always reply with ONLY one JSON object:
{"say":"what this step does","says":[],"command":"","type_only":false,"tool":"","tool_arg":"","ask":[],"choices":[],"done":false}

Harness LOOP until the job is done or they Stop:
1) "say" = human explanation of the NEXT action (what the command does). The chat shows this. The terminal types and runs "command".
2) Then ONE of: tool (empty command) OR command OR type_only OR ask.
3) After TERMINAL/TOOL, explain the result, then the next command. Keep looping. done true only when finished or waiting on the user.
4) Fix failures. Do not repeat a failed command unchanged.

Tools (empty command while calling): host_stats, get_cpu, get_ram, get_storage, get_docker_status, list_projects, docker_ps, vps_logs.

Commands: any normal root shell (docker, systemctl status, journalctl -n, ss, df, ls, git, …). ONE per turn. Prefer doing the work over describing it.

Never: rm -rf /, mkfs, dd onto disks, shutdown/reboot, iptables -F, deleting this panel. Keep JSON valid.`

func extractAIText(raw []byte) string {
	var ar apiResp
	if json.Unmarshal(raw, &ar) == nil {
		if t := strings.TrimSpace(ar.Text); t != "" {
			return t
		}
		if t := strings.TrimSpace(ar.Response); t != "" {
			return t
		}
	}
	return strings.TrimSpace(string(raw))
}

func aiSlotOrder() []int {
	out := make([]int, 0, 17)
	for _, g := range aiSlotGroups {
		cp := append([]int(nil), g...)
		rand.Shuffle(len(cp), func(i, j int) { cp[i], cp[j] = cp[j], cp[i] })
		out = append(out, cp...)
	}
	return out
}

func postAISlots(payload []byte) (string, error) {
	var lastErr error
	client := &http.Client{Timeout: slotWait}
	for _, n := range aiSlotOrder() {
		url := fmt.Sprintf("%s%d", AIBase, n)
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			lastErr = fmt.Errorf("assistant HTTP %d", res.StatusCode)
			continue
		}
		text := extractAIText(raw)
		if text == "" {
			lastErr = fmt.Errorf("empty assistant reply")
			continue
		}
		return text, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all assistant models failed")
	}
	return "", lastErr
}

func Turn(history []Message) (Reply, string, error) {
	return TurnWith(systemPrompt, history)
}

func TurnWith(sys string, history []Message) (Reply, string, error) {
	if len(history) > MaxTurns {
		history = history[len(history)-MaxTurns:]
	}
	var b strings.Builder
	b.WriteString(sys)
	b.WriteString("\n\nCONVERSATION:\n")
	for _, m := range history {
		role := strings.ToUpper(strings.TrimSpace(m.Role))
		if role == "" {
			role = "USER"
		}
		b.WriteString(role)
		b.WriteString(":\n")
		b.WriteString(strings.TrimSpace(m.Text))
		b.WriteString("\n\n")
	}
	b.WriteString("Reply with JSON only.\n")

	payload, _ := json.Marshal(map[string]string{"text": b.String()})
	text, lastErr := postAISlots(payload)
	if lastErr != nil {
		return Reply{}, "", lastErr
	}
	rep, ok := parseReply(text)
	if !ok {
		rep = Reply{Say: text, Done: true}
	}
	rep.Say = sanitizeSay(rep.Say)
	cleanSays := make([]string, 0, len(rep.Says))
	for _, s := range rep.Says {
		s = sanitizeSay(s)
		if s != "" {
			cleanSays = append(cleanSays, s)
		}
		if len(cleanSays) >= 6 {
			break
		}
	}
	rep.Says = cleanSays
	if rep.Say == "" && len(rep.Says) > 0 {
		rep.Say = rep.Says[0]
		rep.Says = rep.Says[1:]
	}
	rep.Command = strings.TrimSpace(rep.Command)
	rep.Tool = strings.ToLower(strings.TrimSpace(rep.Tool))
	rep.ToolArg = strings.TrimSpace(rep.ToolArg)
	rep.Image = strings.TrimSpace(rep.Image)
	rep.UpdateID = strings.TrimSpace(rep.UpdateID)
	if len(rep.Ask) > 1 {
		rep.Ask = rep.Ask[:1]
	}
	if len(rep.Ask) > 0 {
		rep.Command = ""
		rep.Tool = ""
		rep.Start = false
		rep.CreateToken = false
		rep.CreateRoom = false
		rep.TypeOnly = false
	}
	if rep.TypeOnly {
		rep.Start = false
	}
	if rep.Tool != "" {
		rep.Command = ""
		rep.Start = false
		rep.CreateToken = false
		rep.CreateRoom = false
		rep.Done = false
	}
	rep.TokenName = strings.TrimSpace(rep.TokenName)
	rep.TokenProjectID = strings.TrimSpace(rep.TokenProjectID)
	if rep.TokenProjectID == "" {
		rep.TokenProjectID = strings.TrimSpace(rep.TokenMode)
	}
	rep.RoomPassword = strings.TrimSpace(rep.RoomPassword)
	compactSpeech(&rep)
	if rep.CreateToken && len(rep.Ask) > 0 {
		rep.CreateToken = false
	}
	if rep.QuotaGB < 0 {
		rep.QuotaGB = 0
	}
	rep.Action = strings.ToLower(strings.TrimSpace(rep.Action))
	if rep.Action != "pause" && rep.Action != "resume" {
		rep.Action = ""
	}
	rep.LogKind = NormalizeLogKind(rep.LogKind)
	return rep, text, nil
}

func compactSpeech(rep *Reply) {
	if rep == nil {
		return
	}
	norm := func(s string) string {
		return strings.ToLower(strings.Join(strings.Fields(s), " "))
	}
	seen := []string{norm(rep.Say)}
	out := make([]string, 0, 1)
	for _, s := range rep.Says {
		n := norm(s)
		if n == "" {
			continue
		}
		dup := false
		for _, x := range seen {
			if x == "" {
				continue
			}
			if n == x || strings.Contains(x, n) || strings.Contains(n, x) {
				dup = true
				break
			}
			pre := 24
			if len(n) < pre || len(x) < pre {
				pre = min(len(n), len(x))
			}
			if pre >= 16 && n[:pre] == x[:pre] {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		seen = append(seen, n)
		out = append(out, s)
		if len(out) >= 1 {
			break
		}
	}
	rep.Says = out
}

func parseReply(text string) (Reply, bool) {
	s := strings.TrimSpace(text)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		rest = strings.TrimPrefix(rest, "json")
		rest = strings.TrimPrefix(rest, "JSON")
		if j := strings.Index(rest, "```"); j >= 0 {
			s = strings.TrimSpace(rest[:j])
		}
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return Reply{}, false
	}
	body := s[start : end+1]
	r, ok := decodeReplyJSON(body)
	if !ok {
		r, ok = decodeReplyJSON(escapeRawControlsInJSONStrings(body))
	}
	if !ok {
		say := extractJSONStringField(body, "say")
		if say == "" {
			return Reply{}, false
		}
		r = Reply{Say: say, Done: true}
		if cmd := extractJSONStringField(body, "command"); cmd != "" {
			r.Command = cmd
		}
		ok = true
	}
	if !ok {
		return Reply{}, false
	}
	clean := make([]string, 0, len(r.Ask))
	for _, q := range r.Ask {
		q = strings.TrimSpace(q)
		if q != "" {
			clean = append(clean, q)
		}
	}
	r.Ask = clean
	if len(r.Ask) > 1 {
		r.Ask = r.Ask[:1]
	}
	ch := make([]string, 0, len(r.Choices))
	seen := map[string]bool{}
	for _, c := range r.Choices {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		ch = append(ch, c)
		if len(ch) >= 8 {
			break
		}
	}
	r.Choices = ch
	low := strings.ToLower(body)
	if strings.Contains(low, `"type_only":true`) || strings.Contains(low, `"typeonly":true`) {
		r.TypeOnly = true
	}
	if d := extractJSONStringField(body, "draft"); d != "" && strings.TrimSpace(r.Command) == "" {
		r.Command = d
		r.TypeOnly = true
	}
	return r, true
}

func decodeReplyJSON(body string) (Reply, bool) {
	var r Reply
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return Reply{}, false
	}
	return r, true
}

// escapeRawControlsInJSONStrings fixes LLM JSON that put real newlines/tabs inside strings.
func escapeRawControlsInJSONStrings(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 32)
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inStr {
			if c == '"' {
				inStr = true
			}
			b.WriteByte(c)
			continue
		}
		if esc {
			b.WriteByte(c)
			esc = false
			continue
		}
		if c == '\\' {
			b.WriteByte(c)
			esc = true
			continue
		}
		if c == '"' {
			inStr = false
			b.WriteByte(c)
			continue
		}
		switch c {
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func extractJSONStringField(body, key string) string {
	needle := `"` + key + `"`
	i := strings.Index(body, needle)
	if i < 0 {
		return ""
	}
	rest := body[i+len(needle):]
	j := strings.Index(rest, ":")
	if j < 0 {
		return ""
	}
	rest = strings.TrimSpace(rest[j+1:])
	if rest == "" {
		return ""
	}
	if rest[0] == '"' {
		var out strings.Builder
		esc := false
		for k := 1; k < len(rest); k++ {
			c := rest[k]
			if esc {
				switch c {
				case 'n':
					out.WriteByte('\n')
				case 'r':
					out.WriteByte('\r')
				case 't':
					out.WriteByte('\t')
				case '"', '\\', '/':
					out.WriteByte(c)
				default:
					out.WriteByte('\\')
					out.WriteByte(c)
				}
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				return out.String()
			}
			out.WriteByte(c)
		}
		return out.String()
	}
	// Unquoted / broken: take until next JSON key or end.
	stopKeys := []string{`,"ask"`, `,"says"`, `,"command"`, `,"choices"`, `,"done"`, `,"create_token"`, `,"token_name"`, `,"token_project_id"`, `,"quota_gb"`, `,"action"`, `,"log_kind"`, `,"image"`, `,"start"`}
	end := len(rest)
	for _, sk := range stopKeys {
		if p := strings.Index(rest, sk); p >= 0 && p < end {
			end = p
		}
	}
	val := strings.TrimSpace(rest[:end])
	val = strings.TrimSuffix(val, ",")
	val = strings.Trim(val, `"`)
	val = strings.ReplaceAll(val, `\n`, "\n")
	val = strings.ReplaceAll(val, `\t`, "\t")
	val = strings.ReplaceAll(val, `\"`, `"`)
	return strings.TrimSpace(val)
}

func sanitizeSay(say string) string {
	s := strings.TrimSpace(say)
	if s == "" {
		return s
	}
	if strings.Contains(s, `"say"`) && (strings.HasPrefix(s, "{") || strings.Contains(s, `{"say"`)) {
		if inner := extractJSONStringField(s, "say"); inner != "" {
			s = strings.TrimSpace(inner)
		}
	}
	s = unescapeLiteralEscapes(s)
	return strings.TrimSpace(s)
}

func unescapeLiteralEscapes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case 'r':
				b.WriteByte('\r')
				i++
				continue
			case 't':
				b.WriteByte('\t')
				i++
				continue
			case '"':
				b.WriteByte('"')
				i++
				continue
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func Dangerous(cmd string) bool {
	c := strings.ToLower(strings.TrimSpace(cmd))
	c = strings.Join(strings.Fields(c), " ")
	needles := []string{
		"rm -rf /", "rm -rf /*", "mkfs", "shutdown", "reboot", "halt",
		":(){", "dd if=", "wipefs", "> /dev/sd",
	}
	for _, n := range needles {
		if strings.Contains(c, n) {
			return true
		}
	}
	if LooksLikeRemoteLogin(c) {
		return true
	}
	if strings.Count(c, "controlmaster") > 1 || strings.Count(c, "controlpath") > 1 {
		return true
	}
	if strings.Contains(c, "rm ") && strings.Contains(c, " /opt/vps-rooms") && !strings.Contains(c, "runtime") {
		return true
	}
	for _, r := range c {
		if unicode.Is(unicode.C, r) && r != '\n' && r != '\t' {
			return true
		}
	}
	return false
}

// LooksLikeRemoteLogin is true for ssh/sshpass/scp/sftp used as a login tool.
func LooksLikeRemoteLogin(cmd string) bool {
	c := strings.ToLower(strings.TrimSpace(cmd))
	c = strings.Join(strings.Fields(c), " ")
	if c == "" {
		return false
	}
	if strings.Contains(c, "sshpass") {
		return true
	}
	fields := strings.Fields(c)
	bin := fields[0]
	if i := strings.LastIndex(bin, "/"); i >= 0 {
		bin = bin[i+1:]
	}
	switch bin {
	case "ssh", "scp", "sftp":
		return true
	}
	return false
}

// RoomForbidden blocks deploy-style / destructive project actions inside a room agent.
func RoomForbidden(cmd string) bool {
	c := strings.ToLower(strings.TrimSpace(cmd))
	c = strings.Join(strings.Fields(c), " ")
	needles := []string{
		"docker rm -f", "docker volume rm", "docker system prune",
	}
	for _, n := range needles {
		if strings.Contains(c, n) {
			return true
		}
	}
	if strings.Contains(c, "rm -rf") && (strings.Contains(c, "/opt/") || strings.HasSuffix(c, " .") || strings.Contains(c, "/*")) {
		return true
	}
	return Dangerous(cmd)
}

// NormalizeLogKind maps UI labels to storage keys.
func NormalizeLogKind(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "panel", "panel events", "panel log":
		return "panel"
	case "api", "api log":
		return "api"
	case "deploy", "deploy log":
		return "deploy"
	case "host", "host events", "host log":
		return "host"
	default:
		return ""
	}
}

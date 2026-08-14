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
	Say         string   `json:"say"`
	Says        []string `json:"says"`
	Command     string   `json:"command"`
	Ask         []string `json:"ask"`
	Choices     []string `json:"choices"`
	QuotaGB     float64  `json:"quota_gb"`
	Image       string   `json:"image"`
	UpdateID    string   `json:"update_id"` // room id or project id — publish image onto that project
	Start       bool     `json:"start"`
	Done        bool     `json:"done"`
	Action      string   `json:"action"`   // pause | resume | ""
	LogKind     string   `json:"log_kind"` // panel | api | deploy | host
	CreateToken bool     `json:"create_token"`
	TokenName   string   `json:"token_name"`
	TokenMode   string   `json:"token_mode"`
	TypeOnly    bool     `json:"type_only"` // fill the terminal input, do not run
}

type apiResp struct {
	Response string `json:"response"`
	Text     string `json:"text"`
}

const systemPrompt = `You are the VPS Manager Deploy agent. You deploy NEW projects and you UPDATE existing ones by their id. You do not handle logs or tokens.

Language: match the user's last message. If they write Arabic, "say" is natural spoken Arabic. If English, natural English. Do not mix. JSON keys stay English. In "say", use **bold** for important words. Short paragraphs. Wrap commands, ids, and names in markdown code spans. Code samples MUST use fenced blocks inside "say". Never dump the JSON wrapper into "say". Keep JSON valid (escape newlines in strings).

Always reply with ONLY one JSON object (no markdown fences around the JSON itself):
{"say":"first spoken bubble","says":[],"command":"","type_only":false,"ask":[],"choices":[],"quota_gb":0,"image":"","update_id":"","start":false,"done":false}

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
{"say":"one complete spoken reply","says":[],"command":"","type_only":false,"ask":[],"choices":[],"quota_gb":0,"image":"","start":false,"action":"","done":false}

You work in a LOOP until the task is done or the user hits Stop:
1) One "say". "says" MUST stay [].
2) Then the next tool: command (runs) OR type_only OR ask. After TERMINAL, say the result and continue with the next command. Do not dump five similar bubbles. Do not stop after one step if the job is unfinished.
3) done true only when finished or waiting on the user.
4) type_only: user asked you to WRITE a command in the terminal without sending — set command to that exact text, type_only true, empty ask, done true. Do not run it.

Speech: if they asked to analyze usage, ANSWER NOW with SYSTEM-NOTE numbers in one say. Do not stall.

Scope:
- You already know this room from SYSTEM-NOTE (id, project_id, image, ports, quota, password is NOT in your note). Act as its operator.
- MAY: files via terminal, edit files via terminal, run commands, analyze THIS room usage, raise quota (quota_gb — Save applies the live disk cap), pause/resume.
- MAY publish a Docker update to THIS same project: docker pull or docker build, then set image to that tag and start true. Same id. Do not create a new room. Do not git clone a different app.
- MUST refuse: delete this room, other rooms, tokens, host-wide logs, unrelated general chat. One short refusal, then what you can do here.
- Name and password are edited in the project page (or tell them the fields). You may explain how.
- Host CPU in SYSTEM-NOTE may jump second-to-second (normal VPS sampling). Real pressure = load1 staying above CPU cores, or disk/RAM high.

Quota: ask+choices for GB (<= max in SYSTEM-NOTE), wait, then quota_gb. The panel applies that cap to the running container.
Power: action "pause" or "resume" only when they ask. Never delete.

Golden tool = "command". File contents are NEVER attached. Fetch then edit.

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

Never: git clone a new app, docker compose up unrelated stacks, mkfs, shutdown, delete this room.`

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

const TokenPrompt = `You are the VPS Manager Tokens agent. You ONLY help with API tokens: create them, explain how to use them. Refuse anything else.

Language: match the user. JSON keys stay English. One spoken "say". "says" MUST stay []. Never dump JSON into say.

Always reply with ONLY one JSON object:
{"say":"spoken reply","says":[],"ask":[],"choices":[],"create_token":false,"token_name":"","token_mode":"","done":false}

You decide the question. Do not repeat yourself.

Modes (one token):
- read = GET only
- write = GET + create/update/exec
- both = that same token can read AND write
Never create two tokens for "both". One secret, mode "both".

Flow:
1) If mode is unknown: ONE question in "ask" (your own wording). Set choices ONLY if you want taps — typically ["read","write","both"]. Empty say or a short lead-in that is NOT the same sentence as ask. Wait.
2) If they already picked a mode (ANSWERS or earlier text): do not ask mode again.
3) If name is unknown: ONE ask for a short name. choices MUST be [] so they type it. Do not also put the name question in "say". Do not ask the name twice with different wording.
4) When you have name + mode: set create_token true ONCE with token_name and token_mode. Empty ask. done MUST be true. Never set create_token again for that name (the panel already created it). Tell them to copy the AI prompt from the card.

If they already have tokens, answer. To create another, same flow — once each, not repeated.

Delete via API is never allowed.
Auth: Authorization: Bearer <secret>  or  X-API-Token: <secret>
Base URL is in SYSTEM-NOTE.

Endpoints: GET /api/v1/projects (ids, image, masked env, deploy status). POST /api/v1/projects/{id}/redeploy — image optional (omit = pull+recreate current). Returns immediately status=deploying; poll GET until running. POST .../build is async (poll GET; last_deploy_error on fail). POST /api/v1/projects is NEW rooms only. PATCH image is the same async redeploy. Never DELETE. Never git/npm inside the app container as publish. both = that same secret can GET and POST/PATCH/exec/build/redeploy.

ask: at most one. create_token false while asking. done true when finished.`

const UsagePrompt = `You are the VPS Manager Usage agent. Your ONLY job is live consumption: CPU, memory, disk, load, network, GPU if present, how many rooms, each room NAME + its disk used vs quota, vs host totals. Refuse anything else (deploy, tokens, file contents, passwords). Short refusal.

CPU note: the panel samples CPU every second then smooths it. Sharp jumps are usually normal on a VPS (short bursts, steal time). Danger is when load1 stays above CPU cores, or RAM/disk stay very high.

Language: match the user. Arabic or English — do not mix. JSON keys stay English. In "say", use **bold** for important numbers and risk words. Never dump JSON into say. Use \\n for new lines inside JSON strings.

Always reply with ONLY one JSON object:
{"say":"spoken analysis","says":[],"ask":[],"choices":[],"done":false}

Rules:
- SYSTEM-NOTE is the live snapshot. Never invent numbers. Never mention passwords, hashes, tokens, or secrets (they are not provided).
- Start from current load: is it safe, watch, or danger? Say that clearly.
- Explain CPU, RAM, disk, load vs cores, and quota reserved vs free disk. Mention rooms/projects that use the most disk or are running.
- Danger if: disk >= 90%, RAM >= 90%, CPU >= 90% sustained, load1 much higher than CPU cores, quota reserved near free disk.
- Watch if those are 70–89%. Safe below that unless something else looks wrong.
- Give practical next steps. Question tool: ask + choices when useful (e.g. which room to inspect). No commands.

done true when this answer is complete.`

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
	rep.Image = strings.TrimSpace(rep.Image)
	rep.UpdateID = strings.TrimSpace(rep.UpdateID)
	if len(rep.Ask) > 1 {
		rep.Ask = rep.Ask[:1]
	}
	if len(rep.Ask) > 0 {
		rep.Command = ""
		rep.Start = false
		rep.CreateToken = false
		rep.TypeOnly = false
	}
	if rep.TypeOnly {
		rep.Start = false
	}
	rep.TokenName = strings.TrimSpace(rep.TokenName)
	rep.TokenMode = strings.ToLower(strings.TrimSpace(rep.TokenMode))
	rep.TokenMode = strings.ReplaceAll(rep.TokenMode, " ", "")
	switch {
	case rep.TokenMode == "both" || strings.Contains(rep.TokenMode, "read") && strings.Contains(rep.TokenMode, "write"):
		rep.TokenMode = "both"
	case rep.TokenMode == "read" || rep.TokenMode == "write":
		// ok
	default:
		rep.TokenMode = ""
	}
	compactSpeech(&rep)
	if rep.CreateToken && (rep.TokenMode == "" || len(rep.Ask) > 0) {
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
	stopKeys := []string{`,"ask"`, `,"says"`, `,"command"`, `,"choices"`, `,"done"`, `,"create_token"`, `,"token_name"`, `,"quota_gb"`, `,"action"`, `,"log_kind"`, `,"image"`, `,"start"`}
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

// RoomForbidden blocks deploy-style / destructive project actions inside a room agent.
func RoomForbidden(cmd string) bool {
	c := strings.ToLower(strings.TrimSpace(cmd))
	c = strings.Join(strings.Fields(c), " ")
	needles := []string{
		"git clone", "docker compose up", "docker-compose up",
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

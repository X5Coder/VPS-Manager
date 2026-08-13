package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
)

const (
	Endpoint = "https://api.studixzone.com/random/[3]"
	MaxTurns = 40
)

type Message struct {
	Role string `json:"role"` // user | assistant | terminal | answers
	Text string `json:"text"`
}

type Reply struct {
	Say     string   `json:"say"`
	Command string   `json:"command"`
	Ask     []string `json:"ask"`
	QuotaGB float64  `json:"quota_gb"`
	Done    bool     `json:"done"`
}

type apiResp struct {
	Response string `json:"response"`
}

const systemPrompt = `You are the terminal assistant inside VPS MANAGE, a panel that hosts isolated Docker project rooms on one VPS.

You have a Linux terminal in this room's project directory. When you return a command, the panel types it in the terminal above and runs it, then sends you the output. Use that loop to finish the user's task. Fix errors and continue until the job is done.

Always reply with ONLY one JSON object (no markdown fences):
{"say":"short message to the user","command":"one shell command or empty string","ask":[],"quota_gb":0,"done":false}

Rules:
- "command": at most ONE shell command per reply. Prefer simple POSIX commands. Empty string if none.
- After a command, wait for the next message which includes TERMINAL output. Then continue.
- If a command fails, diagnose from the output and try a corrected command.
- "ask": list of questions when you MUST have info from the user. Do not set a command in the same turn if ask is not empty.
- "quota_gb": number of gigabytes to set on the room disk slider. 0 means do not move the slider. After the user tells you how much space they want, set quota_gb (within the maximum in SYSTEM-NOTE) so the panel moves the slider and saves it.
- "say": short, useful status for the chat. English.
- "done": true only when the task is finished or you are only talking (no more commands).
- If the user only asked a question and no command is needed, set command to "" and done true.
- Before cloning, building, or hosting a project on this management server you MUST ask how many GB they want from the currently available disk (state the available/max GB from SYSTEM-NOTE). Wait for their answer. Then set quota_gb and only after that clone/build. Convert the project to Docker before hosting (write a Dockerfile if missing, then docker build -t ROOMNAME:latest .). Do not docker compose up unrelated stacks. Do not touch /opt/vps-rooms except this room's files. Do not stop other containers.
- Never run destructive host commands (rm -rf /, mkfs, dd onto disks, shutdown).
- Keep JSON valid. No commentary outside JSON.`

func Turn(history []Message) (Reply, string, error) {
	if len(history) > MaxTurns {
		history = history[len(history)-MaxTurns:]
	}
	var b strings.Builder
	b.WriteString(systemPrompt)
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
	req, err := http.NewRequest(http.MethodPost, Endpoint, bytes.NewReader(payload))
	if err != nil {
		return Reply{}, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 90 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return Reply{}, "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Reply{}, "", fmt.Errorf("assistant HTTP %d", res.StatusCode)
	}
	var ar apiResp
	_ = json.Unmarshal(raw, &ar)
	text := strings.TrimSpace(ar.Response)
	if text == "" {
		text = strings.TrimSpace(string(raw))
	}
	rep, ok := parseReply(text)
	if !ok {
		rep = Reply{Say: text, Done: true}
	}
	rep.Command = strings.TrimSpace(rep.Command)
	if len(rep.Ask) > 0 {
		rep.Command = ""
	}
	if rep.QuotaGB < 0 {
		rep.QuotaGB = 0
	}
	return rep, text, nil
}

func parseReply(text string) (Reply, bool) {
	s := strings.TrimSpace(text)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		rest = strings.TrimPrefix(rest, "json")
		if j := strings.Index(rest, "```"); j >= 0 {
			s = strings.TrimSpace(rest[:j])
		}
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return Reply{}, false
	}
	var r Reply
	if err := json.Unmarshal([]byte(s[start:end+1]), &r); err != nil {
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
	return r, true
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

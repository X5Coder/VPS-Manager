package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/x5coder/vps-rooms/internal/ai"
	"github.com/x5coder/vps-rooms/internal/metrics"
)

func (s *Server) handleRoomAI(w http.ResponseWriter, r *http.Request, roomID string) {
	_, room := s.roomAccess(w, r, roomID)
	if room == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Messages []ai.Message `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if len(body.Messages) == 0 {
		writeErr(w, 400, "messages required")
		return
	}
	st := s.storageInfo()
	avail := asInt64(st["quota_available"]) + room.QuotaBytes
	if avail < room.QuotaBytes {
		avail = room.QuotaBytes
	}
	maxGB := float64(avail) / (1024 * 1024 * 1024)
	curGB := float64(room.QuotaBytes) / (1024 * 1024 * 1024)
	hm := s.Metrics.Snapshot()
	usage, _ := s.Rooms.UsageBytes(room.ID)
	usedGB := float64(usage) / (1024 * 1024 * 1024)
	projs, _ := s.Store.ListProjects(room.ID)
	var pb strings.Builder
	fmt.Fprintf(&pb,
		"Page: Room agent. You are inside room %q (id %s). ANSWER usage from these numbers — do not stall. This room disk used %.2f GB of quota %.1f GB (max allowed %.1f GB). Host CPU %.1f%% cores=%d load1=%.2f RAM %.1f%% disk %.1f%%. NEVER delete this room. NEVER clone/pull a new project.",
		room.Name, room.ID, usedGB, curGB, maxGB, hm.CPUPercent, hm.CPUCores, hm.Load1, hm.MemPercent, hm.DiskPercent,
	)
	for _, p := range projs {
		pb.WriteString(fmt.Sprintf(
			"\nProject %s: id=%s image=%s status=%s host_port=%d container_port=%d domain=%q. Read files only via terminal command (ls then head). Do not guess contents.",
			p.Name, p.ID, p.Image, p.Status, p.HostPort, p.ContainerPort, p.Domain,
		))
	}
	hist := append([]ai.Message{{
		Role: "system-note",
		Text: diskSystemNote(st, pb.String()),
	}}, body.Messages...)
	rep, raw, err := ai.TurnWith(ai.RoomPrompt, hist)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	if ai.RoomForbidden(rep.Command) {
		rep.Say = strings.TrimSpace(rep.Say + " That command is not allowed in this room agent.")
		rep.Command = ""
		rep.Done = true
	}
	// Hard block delete intents in say+command
	low := strings.ToLower(rep.Say + " " + rep.Command + " " + rep.Action)
	if strings.Contains(low, "delete this room") || strings.Contains(low, "delete the project") || strings.Contains(low, "wipe the room") {
		rep.Say = strings.TrimSpace(rep.Say + " I cannot delete this project.")
		rep.Command = ""
		rep.Action = ""
	}
	if rep.QuotaGB > 0 {
		if rep.QuotaGB > maxGB {
			rep.QuotaGB = maxGB
		}
		if rep.QuotaGB < 0.1 {
			rep.QuotaGB = 0.1
		}
		rep.QuotaGB = float64(int(rep.QuotaGB*10+0.5)) / 10
	}
	writeJSON(w, 200, map[string]any{
		"say":       rep.Say,
		"says":      rep.Says,
		"command":   rep.Command,
		"ask":       rep.Ask,
		"choices":   rep.Choices,
		"quota_gb":  rep.QuotaGB,
		"image":     "",
		"start":     false,
		"action":    rep.Action,
		"done":      rep.Done,
		"type_only": rep.TypeOnly,
		"raw":       raw,
	})
}

func (s *Server) handleDeployAI(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Messages []ai.Message `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if len(body.Messages) == 0 {
		writeErr(w, 400, "messages required")
		return
	}
	st := s.storageInfo()
	maxGB := float64(asInt64(st["quota_available"])) / (1024 * 1024 * 1024)
	if maxGB < 0.1 {
		maxGB = 0.1
	}
	hist := append([]ai.Message{{
		Role: "system-note",
		Text: diskSystemNote(st, fmt.Sprintf(
			"Page: Deploy. Working directory is the deploy workspace on the VPS host. Free disk for new rooms: %.1f GB. After a real docker pull / docker build / git clone, you MUST ask GB with ask+choices before start. You may pause anytime to ask a question. After a real pull/build, set image to that tag. start true only when image + user-chosen quota are ready.",
			maxGB,
		)),
	}}, body.Messages...)
	rep, raw, err := ai.Turn(hist)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	if ai.Dangerous(rep.Command) {
		rep.Say = strings.TrimSpace(rep.Say + " That command is not allowed.")
		rep.Command = ""
		rep.Start = false
		rep.Done = true
	}
	cmdLow := strings.ToLower(rep.Command)
	installCmd := strings.Contains(cmdLow, "docker pull") || strings.Contains(cmdLow, "docker build") || strings.Contains(cmdLow, "git clone")
	if installCmd {
		rep.Start = false
	}
	if rep.Start && rep.QuotaGB <= 0 {
		rep.Start = false
		if len(rep.Ask) == 0 {
			rep.Ask = []string{"How much disk should this project use?"}
			rep.Choices = []string{"0.5 GB", "1 GB", "2 GB", "5 GB", "10 GB"}
		}
	}
	if len(rep.Ask) == 0 && installCmd && rep.QuotaGB <= 0 {
		// After they run the command, next turn must ask — keep command, block start.
	}
	if rep.QuotaGB > 0 {
		if rep.QuotaGB > maxGB {
			rep.QuotaGB = maxGB
		}
		if rep.QuotaGB < 0.1 {
			rep.QuotaGB = 0.1
		}
		rep.QuotaGB = float64(int(rep.QuotaGB*10+0.5)) / 10
	}
	writeJSON(w, 200, map[string]any{
		"say":       rep.Say,
		"says":      rep.Says,
		"command":   rep.Command,
		"ask":       rep.Ask,
		"choices":   rep.Choices,
		"quota_gb":  rep.QuotaGB,
		"image":     rep.Image,
		"start":     rep.Start,
		"done":      rep.Done,
		"type_only": rep.TypeOnly,
		"raw":       raw,
	})
}

func (s *Server) handleDeployExec(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Command    string `json:"command"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Command) == "" {
		writeErr(w, 400, "command required")
		return
	}
	if ai.Dangerous(body.Command) {
		writeErr(w, 400, "command not allowed")
		return
	}
	timeout := 120 * time.Second
	if body.TimeoutSec > 0 {
		timeout = time.Duration(body.TimeoutSec) * time.Second
	}
	if timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	dir := filepath.Join(s.Cfg.RuntimeDir, "_deploy")
	_ = os.MkdirAll(dir, 0o750)
	cmd := exec.CommandContext(ctx, "sh", "-lc", body.Command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	res := map[string]any{"output": string(out), "where": "deploy"}
	if err != nil {
		res["error"] = err.Error()
		res["exit"] = 1
	} else {
		res["exit"] = 0
	}
	writeJSON(w, 200, res)
}

func (s *Server) handleTokensAI(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Messages []ai.Message `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if len(body.Messages) == 0 {
		writeErr(w, 400, "messages required")
		return
	}
	list, _ := s.Store.ListAPITokens()
	n := 0
	if list != nil {
		n = len(list)
	}
	base := requestBaseURL(r)
	names := make([]string, 0, n)
	for _, t := range list {
		names = append(names, fmt.Sprintf("%s (%s)", t.Name, t.Mode))
	}
	note := fmt.Sprintf(
		"Page: Tokens. Existing tokens: %d. %s Base URL: %s. Never print a guessed secret. If you set create_token, the panel creates the real secret and a copyable AI prompt on the card (read, write, or both — matching that token).",
		n, strings.Join(names, "; "), base,
	)
	if n == 0 {
		note += " User has ZERO tokens — first step is create one."
	}
	note += " Modes for ONE token: read (GET only), write (GET+mutate), both (that same token can read and write). Never create two tokens for both. Do not repeat questions. Name questions have empty choices."
	hist := append([]ai.Message{{Role: "system-note", Text: note}}, body.Messages...)
	rep, raw, err := ai.TurnWith(ai.TokenPrompt, hist)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	rep.Command = ""
	rep.Start = false
	askJoined := strings.ToLower(strings.Join(rep.Ask, " "))
	if looksLikeNameAsk(askJoined) && onlyModeChoices(rep.Choices) {
		rep.Choices = nil
	}
	out := map[string]any{
		"say":          rep.Say,
		"says":         rep.Says,
		"command":      "",
		"ask":          rep.Ask,
		"choices":      rep.Choices,
		"quota_gb":     0,
		"image":        "",
		"start":        false,
		"done":         rep.Done,
		"create_token": false,
		"raw":          raw,
	}
	if rep.CreateToken && (rep.TokenMode == "read" || rep.TokenMode == "write" || rep.TokenMode == "both") {
		name := rep.TokenName
		if name == "" {
			name = "API token"
		}
		if existing, _ := s.Store.GetAPITokenByName(name); existing != nil {
			out["say"] = strings.TrimSpace(rep.Say)
			if out["say"] == "" {
				out["say"] = "That token name already exists. Use the card already on this page — I did not create a second one."
			}
			out["done"] = true
		} else {
			tok, plain, err := s.Store.CreateAPIToken(name, rep.TokenMode)
			if err != nil {
				out["say"] = strings.TrimSpace(rep.Say + " Could not create the token: " + err.Error())
			} else {
				out["create_token"] = true
				out["token"] = tok
				out["secret"] = plain
				out["prompt"] = s.buildAPIPrompt(base, plain, tok.Mode)
				out["say"] = strings.TrimSpace(rep.Say)
				if out["say"] == "" {
					out["say"] = "Token created. Copy the secret or the AI prompt from the card — paste the prompt into any assistant so it can drive this API."
				}
				out["done"] = true
			}
		}
	}
	writeJSON(w, 200, out)
}

func diskSystemNote(st map[string]any, extra string) string {
	gb := func(k string) float64 {
		return float64(asInt64(st[k])) / (1024 * 1024 * 1024)
	}
	return fmt.Sprintf(
		"Live VPS disk: total %.1f GB, used %.1f GB, free %.1f GB. Quota already reserved by rooms: %.1f GB. Use these exact figures; never invent disk sizes. %s",
		gb("disk_total"), gb("disk_used"), gb("disk_free"), gb("quota_reserved"), strings.TrimSpace(extra),
	)
}

func listProjectNames(root string, limit int) []string {
	if limit <= 0 {
		limit = 30
	}
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return nil
		}
		base := info.Name()
		if info.IsDir() {
			if base == ".git" || base == "node_modules" || base == "venv" || base == ".venv" || base == ".cache" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(base, ".") && base != ".env" {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		if len(out) >= limit {
			return fmt.Errorf("done")
		}
		return nil
	})
	return out
}

func (s *Server) handleLogsAI(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Messages []ai.Message `json:"messages"`
		LogKind  string       `json:"log_kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if len(body.Messages) == 0 {
		writeErr(w, 400, "messages required")
		return
	}
	hist := append([]ai.Message{{
		Role: "system-note",
		Text: "Page: Logs agent. Available logs: Panel, API, Deploy, Host events. Ask the user which one with those exact choices when needed. After they pick, set log_kind. Keep answers short and practical.",
	}}, body.Messages...)

	// If client already selected a kind this turn, attach a trimmed excerpt.
	kind := ai.NormalizeLogKind(body.LogKind)
	if kind != "" {
		excerpt, _ := tailFile(logPath(s.Cfg.DataDir, kind), 48*1024)
		if strings.TrimSpace(excerpt) == "" {
			excerpt = "(empty log)"
		}
		hist = append(hist, ai.Message{
			Role: "terminal",
			Text: fmt.Sprintf("LOG kind=%s excerpt (newest at bottom, truncated):\n%s", kind, excerpt),
		})
	}

	rep, raw, err := ai.TurnWith(ai.LogsPrompt, hist)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	rep.Command = ""
	if len(rep.Ask) > 0 && len(rep.Choices) == 0 {
		rep.Choices = []string{"Panel", "API", "Deploy", "Host events"}
	}
	// Map a choice-looking last user answer into log_kind if model forgot.
	if rep.LogKind == "" {
		for i := len(body.Messages) - 1; i >= 0; i-- {
			m := body.Messages[i]
			if m.Role == "user" || m.Role == "answers" {
				if k := ai.NormalizeLogKind(m.Text); k != "" {
					rep.LogKind = k
					break
				}
			}
		}
	}
	writeJSON(w, 200, map[string]any{
		"say":      rep.Say,
		"says":     rep.Says,
		"command":  "",
		"ask":      rep.Ask,
		"choices":  rep.Choices,
		"log_kind": rep.LogKind,
		"quota_gb": 0,
		"done":     rep.Done,
		"raw":      raw,
	})
}

func (s *Server) handleUsageAI(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Messages []ai.Message `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if len(body.Messages) == 0 {
		writeErr(w, 400, "messages required")
		return
	}
	hist := append([]ai.Message{{
		Role: "system-note",
		Text: s.usageSnapshot(),
	}}, body.Messages...)
	rep, raw, err := ai.TurnWith(ai.UsagePrompt, hist)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	rep.Command = ""
	writeJSON(w, 200, map[string]any{
		"say":      rep.Say,
		"says":     rep.Says,
		"command":  "",
		"ask":      rep.Ask,
		"choices":  rep.Choices,
		"quota_gb": 0,
		"done":     rep.Done,
		"raw":      raw,
	})
}

func (s *Server) usageSnapshot() string {
	m := s.Metrics.Snapshot()
	facts := metrics.CollectFacts()
	hostname, _ := os.Hostname()
	if facts.Hostname == "" {
		facts.Hostname = hostname
	}
	st := s.storageInfo()
	gb := func(b uint64) float64 { return float64(b) / (1024 * 1024 * 1024) }
	var b strings.Builder
	b.WriteString("Page: Usage agent. LIVE server snapshot. NEVER include or ask for passwords, hashes, tokens, or secrets.\n")
	fmt.Fprintf(&b, "Host: hostname=%s os=%q kernel=%q arch=%s virt=%s uptime=%s ssh_port=%d docker=%v\n",
		facts.Hostname, facts.OS, facts.Kernel, facts.Arch, facts.Virt, metrics.FormatUptime(facts.UptimeSec), facts.SSHPort, s.Docker != nil && s.Docker.Available())
	fmt.Fprintf(&b, "CPU: model=%q cores=%d usage=%.1f%% load1=%.2f\n", facts.CPUModel, m.CPUCores, m.CPUPercent, m.Load1)
	fmt.Fprintf(&b, "Memory: used=%.2fGB total=%.2fGB percent=%.1f%%\n", gb(m.MemUsed), gb(m.MemTotal), m.MemPercent)
	fmt.Fprintf(&b, "Disk: used=%.2fGB total=%.2fGB free=%.2fGB percent=%.1f%%\n", gb(m.DiskUsed), gb(m.DiskTotal), gb(m.DiskFree), m.DiskPercent)
	fmt.Fprintf(&b, "Network totals: rx_bytes=%d tx_bytes=%d\n", m.NetRx, m.NetTx)
	if len(m.GPU) > 0 {
		for i, g := range m.GPU {
			fmt.Fprintf(&b, "GPU[%d]: name=%q util=%.1f%% mem=%.0f/%.0f MB\n", i, g.Name, g.UtilPercent, g.MemUsedMB, g.MemTotalMB)
		}
	} else {
		b.WriteString("GPU: none reported\n")
	}
	fmt.Fprintf(&b, "Quota: reserved=%v available_to_allocate=%.2fGB disk_free=%v\n",
		st["quota_reserved"], st["quota_available_gb"], st["disk_free"])
	ports := s.portsPayload()
	if used, ok := ports["used_ports"].([]int); ok {
		fmt.Fprintf(&b, "Used ports: %v (panel %v)\n", used, ports["panel_port"])
	}
	rooms, _ := s.Store.ListRooms()
	fmt.Fprintf(&b, "Rooms count: %d (names + disk only, no passwords)\n", len(rooms))
	var usedSum int64
	for _, rm := range rooms {
		usage, _ := s.Rooms.UsageBytes(rm.ID)
		usedSum += usage
		projs, _ := s.Store.ListProjects(rm.ID)
		stt := "empty"
		running := 0
		for _, p := range projs {
			if p.Status == "running" {
				running++
				stt = "running"
			} else if stt == "empty" {
				stt = "stopped"
			}
		}
		if len(projs) > 0 && running == 0 {
			stt = "stopped"
		}
		qGB := float64(rm.QuotaBytes) / (1024 * 1024 * 1024)
		uGB := float64(usage) / (1024 * 1024 * 1024)
		fmt.Fprintf(&b, "- name=%q status=%s disk_used=%.2fGB quota=%.2fGB running_projects=%d\n",
			rm.Name, stt, uGB, qGB, running)
	}
	fmt.Fprintf(&b, "Sum of room disk used=%.2fGB vs host disk used=%.2fGB total=%.2fGB\n",
		float64(usedSum)/(1024*1024*1024), gb(m.DiskUsed), gb(m.DiskTotal))
	b.WriteString("CPU is 1s sampled then smoothed — jumps are usually normal. Judge safe / watch / danger.")
	return b.String()
}

func looksLikeNameAsk(ask string) bool {
	a := strings.ToLower(ask)
	return strings.Contains(a, "name") || strings.Contains(a, "اسم") || strings.Contains(a, "سمي")
}

func onlyModeChoices(choices []string) bool {
	if len(choices) == 0 {
		return false
	}
	ok := map[string]bool{"read": true, "write": true, "both": true, "قراءة": true, "كتابة": true}
	for _, c := range choices {
		k := strings.ToLower(strings.TrimSpace(c))
		if !ok[k] {
			return false
		}
	}
	return true
}

func slugTag(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r == '_' || r == ' ' {
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "room"
	}
	if len(s) > 32 {
		s = s[:32]
	}
	return s
}

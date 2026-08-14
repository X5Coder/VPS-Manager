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
	ids := room.ID
	for _, p := range projs {
		ids += "," + p.ID
	}
	var pb strings.Builder
	fmt.Fprintf(&pb,
		"Page: Room agent. You are inside room %q. id=%s project_ids=%s. ANSWER usage from these numbers — do not stall. This room disk used %.2f GB of quota %.1f GB (max allowed %.1f GB). Host CPU %.1f%% cores=%d load1=%.2f RAM %.1f%% disk %.1f%%. NEVER delete this room. You MAY docker pull/build then set image+start to publish an update on THIS same id.",
		room.Name, room.ID, ids, usedGB, curGB, maxGB, hm.CPUPercent, hm.CPUCores, hm.Load1, hm.MemPercent, hm.DiskPercent,
	)
	for _, p := range projs {
		pb.WriteString(fmt.Sprintf(
			"\nProject %s: id=%s room_id=%s image=%s status=%s host_port=%d container_port=%d domain=%q container_id=%s. Read files only via terminal command (ls then head). Do not guess contents.",
			p.Name, p.ID, p.RoomID, p.Image, p.Status, p.HostPort, p.ContainerPort, p.Domain, p.ContainerID,
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
		"image":     strings.TrimSpace(rep.Image),
		"update_id": room.ID,
		"start":     rep.Start,
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
	catalog := s.projectsCatalogNote()
	hist := append([]ai.Message{{
		Role: "system-note",
		Text: diskSystemNote(st, fmt.Sprintf(
			"Page: Deploy. Working directory is the deploy workspace on the VPS host. Free disk for NEW rooms: %.1f GB. After a real docker pull / docker build / git clone for a NEW room, ask GB with ask+choices before start. For an UPDATE, set update_id to the existing id from this list (same container, new image, no extra quota unless they asked). After a real pull/build, set image to that tag. start true when image is ready (quota required only for new rooms).\n%s",
			maxGB, catalog,
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
	if installCmd && strings.TrimSpace(rep.UpdateID) == "" {
		rep.Start = false
	}
	if rep.Start && rep.QuotaGB <= 0 && strings.TrimSpace(rep.UpdateID) == "" {
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
		"update_id": strings.TrimSpace(rep.UpdateID),
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
	roomsList, _ := s.Store.ListRooms()
	tokLines := make([]string, 0, n)
	for _, t := range list {
		tokLines = append(tokLines, t.Name+" → all rooms")
	}
	st := s.storageInfo()
	totalGB := float64(asInt64(st["disk_total"])) / (1024 * 1024 * 1024)
	freeGB := float64(asInt64(st["quota_available"])) / (1024 * 1024 * 1024)
	note := fmt.Sprintf(
		"Page: Tokens. BASE=%s. Host disk_total=%.2f GB. quota_available_gb=%.2f GB. Existing tokens (name only, each covers ALL rooms): %d. %s Rooms: ",
		base, totalGB, freeGB, n, strings.Join(tokLines, "; "),
	)
	for _, rm := range roomsList {
		projs, _ := s.Store.ListProjects(rm.ID)
		stt := "empty"
		if len(projs) > 0 {
			stt = projs[0].Status
			if stt == "" {
				stt = "has-project"
			}
		}
		usage, _ := s.Rooms.UsageBytes(rm.ID)
		note += fmt.Sprintf("%s id=%s status=%s quota_gb=%.2f usage_gb=%.2f; ", rm.Name, rm.ID, stt, float64(rm.QuotaBytes)/(1024*1024*1024), float64(usage)/(1024*1024*1024))
	}
	note += "create_token needs token_name only. create_room needs room_name + quota_gb + room_password. Empty rooms fill on tar/GitHub with ROOM_ID. Answer API how-to in full. Never print a guessed secret."
	if len(roomsList) == 0 {
		note += " No rooms yet — you MAY create_room."
	}
	hist := append([]ai.Message{{Role: "system-note", Text: note}}, body.Messages...)
	rep, raw, err := ai.TurnWith(ai.TokenPrompt, hist)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	rep.Command = ""
	rep.Start = false
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
		"create_room":  false,
		"raw":          raw,
	}
	if rep.CreateRoom && strings.TrimSpace(rep.RoomName) != "" && rep.QuotaGB > 0 {
		cPort := rep.ContainerPort
		if cPort <= 0 {
			cPort = 8080
		}
		pass := strings.TrimSpace(rep.RoomPassword)
		if len(pass) < 6 {
			out["say"] = strings.TrimSpace(rep.Say)
			if out["say"] == "" {
				out["say"] = "Need a room password (at least 6 characters) and a disk size from the free space above."
			}
			out["ask"] = []string{"Room password?"}
			out["done"] = false
		} else if rm, _, err := s.createEmptyRoom(rep.RoomName, rep.QuotaGB, cPort, 0, pass); err != nil {
			out["say"] = strings.TrimSpace(rep.Say + " Could not create the room: " + err.Error())
			out["done"] = false
		} else {
			out["create_room"] = true
			out["room_id"] = rm.ID
			out["room_name"] = rm.Name
			if strings.TrimSpace(rep.Say) == "" {
				out["say"] = fmt.Sprintf("Empty room **%s** is ready (id `%s`, %.2f GB). Status is empty until you drop a .tar or set ROOM_ID in GitHub.", rm.Name, rm.ID, rep.QuotaGB)
			} else {
				out["say"] = strings.TrimSpace(rep.Say)
			}
			out["done"] = true
		}
	}
	if rep.CreateToken {
		name := strings.TrimSpace(rep.TokenName)
		if name == "" {
			out["say"] = strings.TrimSpace(rep.Say)
			if out["say"] == "" {
				out["say"] = "What should this API be called?"
			}
			out["ask"] = []string{"API name?"}
			out["done"] = false
		} else if existing, _ := s.Store.GetAPITokenByName(name); existing != nil {
			out["say"] = strings.TrimSpace(rep.Say)
			if out["say"] == "" {
				out["say"] = "That token name already exists. Use the card already on this page."
			}
			out["done"] = true
		} else {
			tok, plain, err := s.Store.CreateAPIToken(name, "")
			if err != nil {
				out["say"] = strings.TrimSpace(rep.Say + " Could not create the token: " + err.Error())
			} else {
				pub := s.tokenPublic(base, *tok)
				out["create_token"] = true
				out["token"] = tok
				out["secret"] = plain
				out["prompt"] = pub["prompt"]
				out["api"] = pub["api"]
				out["script"] = pub["script"]
				out["say"] = strings.TrimSpace(rep.Say)
				if out["say"] == "" {
					out["say"] = "API created for all rooms. Copy script, set ROOM_ID for the room you update. Copy API is BASE and TOKEN only."
				}
				out["done"] = true
			}
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) projectsCatalogNote() string {
	list, err := s.Store.ListRooms()
	if err != nil || len(list) == 0 {
		return "Existing projects: none yet. Any start without update_id creates a new room."
	}
	var b strings.Builder
	b.WriteString("Existing projects (copy id to update; GET /api/v1/projects returns the same fields):\n")
	for _, rm := range list {
		usage, _ := s.Rooms.UsageBytes(rm.ID)
		projs, _ := s.Store.ListProjects(rm.ID)
		if len(projs) == 0 {
			fmt.Fprintf(&b, "- id=%s name=%q status=empty quota_bytes=%d usage_bytes=%d password_set=%v\n",
				rm.ID, rm.Name, rm.QuotaBytes, usage, rm.PassPlain != "")
			continue
		}
		for _, p := range projs {
			fmt.Fprintf(&b, "- id=%s project_id=%s name=%q image=%s status=%s host_port=%d container_port=%d domain=%q quota_bytes=%d usage_bytes=%d container_id=%s\n",
				rm.ID, p.ID, rm.Name, p.Image, p.Status, p.HostPort, p.ContainerPort, p.Domain, rm.QuotaBytes, usage, p.ContainerID)
		}
	}
	return b.String()
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

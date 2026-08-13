package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/x5coder/vps-rooms/internal/auth"
	"github.com/x5coder/vps-rooms/internal/projects"
	"github.com/x5coder/vps-rooms/internal/rooms"
	"github.com/x5coder/vps-rooms/internal/store"
)

func (s *Server) routesManage() {
	// Register clear before logs so older muxes never shadow /clear.
	s.Mux.HandleFunc("/api/host/logs/clear", s.withGate(s.handleHostLogsClear))
	s.Mux.HandleFunc("/api/host/password", s.withGate(s.handleHostPassword))
	s.Mux.HandleFunc("/api/host/logs", s.withGate(s.handleHostLogs))
	s.Mux.HandleFunc("/api/panel/port", s.withGate(s.handlePanelPort))
	s.Mux.HandleFunc("/api/settings/owner-password", s.withGate(s.handleOwnerPasswordChange))
	s.Mux.HandleFunc("/api/settings/notify", s.withGate(s.handleNotifySettings))
}

func randomPass(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

func hostPassPath(dataDir string) string {
	return filepath.Join(dataDir, "secrets", "host.env")
}

func saveHostPass(dataDir, password string) {
	dir := filepath.Join(dataDir, "secrets")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(hostPassPath(dataDir), []byte("VPS_ROOT_PASS="+password+"\n"), 0o600)
}

func loadHostPassSet(dataDir string) bool {
	b, err := os.ReadFile(hostPassPath(dataDir))
	if err != nil {
		return false
	}
	return strings.Contains(string(b), "VPS_ROOT_PASS=") && len(strings.TrimSpace(string(b))) > len("VPS_ROOT_PASS=")
}

func (s *Server) handleHostPassword(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]any{
			"stored": loadHostPassSet(s.Cfg.DataDir),
			"note":   "VPS root password is stored as VPS_ROOT_PASS in secrets/host.env when changed from the panel",
		})
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Password) < 8 {
		writeErr(w, 400, "password must be at least 8 characters")
		return
	}
	cmd := exec.Command("chpasswd")
	cmd.Stdin = strings.NewReader("root:" + body.Password + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		writeErr(w, 500, strings.TrimSpace(string(out))+" "+err.Error())
		return
	}
	saveHostPass(s.Cfg.DataDir, body.Password)
	_ = appendLog(s.Cfg.DataDir, "host", "root password changed via panel")
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) handleNotifySettings(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg := s.Notify.Load()
		// Never echo full token — only whether set + masked suffix.
		masked := ""
		if cfg.BotToken != "" {
			if len(cfg.BotToken) > 8 {
				masked = "••••" + cfg.BotToken[len(cfg.BotToken)-6:]
			} else {
				masked = "••••"
			}
		}
		writeJSON(w, 200, map[string]any{
			"enabled":        cfg.Enabled,
			"bot_token_set":  cfg.BotToken != "",
			"bot_token_hint": masked,
			"chat_id":        cfg.ChatID,
			"owner_chat_id":  s.Cfg.TelegramChatID, // fixed gate owner id (immutable)
		})
	case http.MethodPost:
		var body struct {
			BotToken string `json:"bot_token"`
			ChatID   string `json:"chat_id"`
			Clear    bool   `json:"clear"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		if body.Clear {
			if err := s.Notify.Clear(); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			writeJSON(w, 200, map[string]any{"ok": "1", "enabled": false})
			return
		}
		cur := s.Notify.Load()
		token := strings.TrimSpace(body.BotToken)
		chat := strings.TrimSpace(body.ChatID)
		if token == "" {
			token = cur.BotToken // keep existing if blank
		}
		if chat == "" {
			chat = cur.ChatID
		}
		if err := s.Notify.Save(token, chat); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		cfg := s.Notify.Load()
		writeJSON(w, 200, map[string]any{"ok": "1", "enabled": cfg.Enabled, "chat_id": cfg.ChatID})
	case http.MethodDelete:
		if err := s.Notify.Clear(); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": "1", "enabled": false})
	default:
		writeErr(w, 405, "method")
	}
}

func (s *Server) handleOwnerPasswordChange(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Current string `json:"current"`
		New     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if body.Current != s.Cfg.OwnerPass {
		writeErr(w, 401, "current admin password incorrect")
		return
	}
	if len(body.New) < 8 {
		writeErr(w, 400, "new password must be at least 8 characters")
		return
	}
	dir := filepath.Join(s.Cfg.DataDir, "secrets")
	_ = os.MkdirAll(dir, 0o700)
	content := "VPS_ROOMS_OWNER_PASS=" + body.New + "\n"
	if err := os.WriteFile(filepath.Join(dir, "owner.env"), []byte(content), 0o600); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Keep systemd drop-in in sync when present
	drop := "/etc/systemd/system/vps-rooms.service.d/owner.conf"
	_ = os.MkdirAll(filepath.Dir(drop), 0o755)
	_ = os.WriteFile(drop, []byte("[Service]\nEnvironment=VPS_ROOMS_OWNER_PASS="+body.New+"\n"), 0o600)
	s.Cfg.OwnerPass = body.New
	_ = appendLog(s.Cfg.DataDir, "panel", "admin password changed")
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) handlePanelPort(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	writeJSON(w, 200, map[string]any{"port": 9090, "listen": s.Cfg.ListenAddr})
}

func logPath(dataDir, kind string) string {
	return filepath.Join(dataDir, "logs", kind+".log")
}

func appendLog(dataDir, kind, line string) error {
	dir := filepath.Join(dataDir, "logs")
	_ = os.MkdirAll(dir, 0o700)
	path := logPath(dataDir, kind)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), line)
	_ = rotateLog(path, 512*1024) // keep ~512KB
	return err
}

func rotateLog(path string, max int64) error {
	st, err := os.Stat(path)
	if err != nil || st.Size() <= max {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if int64(len(b)) <= max {
		return nil
	}
	keep := b[len(b)-int(max):]
	// align to newline
	if i := strings.IndexByte(string(keep), '\n'); i >= 0 && i+1 < len(keep) {
		keep = keep[i+1:]
	}
	return os.WriteFile(path, keep, 0o600)
}

func tailFile(path string, maxBytes int64) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if int64(len(b)) > maxBytes {
		b = b[len(b)-int(maxBytes):]
		if i := strings.IndexByte(string(b), '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	if !utf8.Valid(b) {
		return string(b), nil
	}
	return string(b), nil
}

func journalSinceArg(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// journalctl rejects RFC3339 with trailing Z in some versions.
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC().Format("2006-01-02 15:04:05 UTC")
	}
	if t, err := time.Parse("2006-01-02 15:04:05 UTC", raw); err == nil {
		return t.UTC().Format("2006-01-02 15:04:05 UTC")
	}
	return ""
}

func (s *Server) panelJournalLines(n int) string {
	if n <= 0 {
		n = 120
	}
	args := []string{"-u", "vps-rooms.service", "-u", "vps-rooms", "-n", strconv.Itoa(n), "--no-pager", "-o", "short-iso", "--no-hostname"}
	if since, ok, _ := s.Store.GetMeta("logs_cleared_at"); ok {
		if js := journalSinceArg(since); js != "" {
			args = append(args, "--since", js)
		}
	}
	cmd := exec.Command("journalctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Fallback without --since if parse/unit issues.
		cmd2 := exec.Command("journalctl", "-u", "vps-rooms.service", "-n", strconv.Itoa(n), "--no-pager", "-o", "short-iso", "--no-hostname")
		out, err = cmd2.CombinedOutput()
		if err != nil {
			return ""
		}
	}
	return filterPanelJournal(string(out))
}

func (s *Server) hostSnapshotLog() string {
	var b strings.Builder
	b.WriteString("=== host snapshot ===\n")
	if out, err := exec.Command("uptime").CombinedOutput(); err == nil {
		b.WriteString("uptime: " + strings.TrimSpace(string(out)) + "\n")
	}
	if out, err := exec.Command("bash", "-lc", "df -h / | tail -1").CombinedOutput(); err == nil {
		b.WriteString("disk: " + strings.TrimSpace(string(out)) + "\n")
	}
	if out, err := exec.Command("bash", "-lc", "free -h | awk '/Mem:/{print $2\" total · \"$3\" used · \"$4\" free\"}'").CombinedOutput(); err == nil {
		b.WriteString("mem: " + strings.TrimSpace(string(out)) + "\n")
	}
	if s.Docker != nil && s.Docker.Available() {
		if out, err := exec.Command("docker", "ps", "--filter", "label=vps-rooms=1", "--format", "{{.Names}} {{.Status}}").CombinedOutput(); err == nil {
			lines := strings.TrimSpace(string(out))
			if lines == "" {
				lines = "(no managed containers)"
			}
			b.WriteString("managed containers:\n" + lines + "\n")
		}
	}
	return b.String()
}

func (s *Server) handleHostLogs(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "panel"
	}
	switch kind {
	case "panel", "host", "api", "deploy":
		// ok
	default:
		kind = "panel"
	}

	text, _ := tailFile(logPath(s.Cfg.DataDir, kind), 200*1024)

	switch kind {
	case "panel":
		if j := s.panelJournalLines(150); j != "" {
			sec := "=== vps-rooms service ===\n" + j
			if strings.TrimSpace(text) != "" {
				text = text + "\n" + sec
			} else {
				text = sec
			}
		}
	case "host":
		snap := s.hostSnapshotLog()
		if j := s.panelJournalLines(80); j != "" {
			snap += "\n=== vps-rooms service ===\n" + j
		}
		if strings.TrimSpace(text) != "" {
			text = text + "\n" + snap
		} else {
			text = snap
		}
	}

	if strings.TrimSpace(text) == "" {
		text = "(empty — panel events will appear here as you use the panel)"
	}
	writeJSON(w, 200, map[string]any{
		"kind":  kind,
		"log":   text,
		"kinds": []string{"panel", "api", "deploy", "host"},
	})
}

func filterPanelJournal(raw string) string {
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		// drop noisy stack traces from old panics — keep one-line panic summary only
		if strings.Contains(l, "/usr/lib/golang/") || strings.Contains(l, "goroutine ") {
			continue
		}
		if strings.HasPrefix(l, "\t") {
			continue
		}
		lines = append(lines, l)
	}
	if len(lines) > 80 {
		lines = lines[len(lines)-80:]
	}
	return strings.Join(lines, "\n")
}

func (s *Server) handleHostLogsClear(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeErr(w, 405, "method")
		return
	}
	kind := r.URL.Query().Get("kind")
	all := r.URL.Query().Get("all") == "1"
	clearedAt := time.Now().UTC().Format("2006-01-02 15:04:05 UTC")
	_ = s.Store.SetMeta("logs_cleared_at", clearedAt)

	if all {
		dir := filepath.Join(s.Cfg.DataDir, "logs")
		_ = os.MkdirAll(dir, 0o700)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".log") {
				continue
			}
			_ = os.WriteFile(filepath.Join(dir, name), []byte(""), 0o600)
		}
		for _, k := range []string{"panel", "host", "api", "deploy"} {
			_ = os.WriteFile(logPath(s.Cfg.DataDir, k), []byte(""), 0o600)
		}
		writeJSON(w, 200, map[string]any{"ok": "1", "cleared_at": clearedAt, "all": true})
		return
	}
	if kind == "" {
		kind = "panel"
	}
	switch kind {
	case "panel", "host", "api", "deploy":
	default:
		kind = "panel"
	}
	_ = os.WriteFile(logPath(s.Cfg.DataDir, kind), []byte(""), 0o600)
	writeJSON(w, 200, map[string]any{"ok": "1", "kind": kind, "cleared_at": clearedAt})
}

// --- room files / exec / quota / logs ---

// canControlRoom: admin (owner) may pause/resume/delete from the list; unlocked room session may too.
func (s *Server) canControlRoom(w http.ResponseWriter, r *http.Request, roomID string) (*store.Session, *store.Room) {
	sess := s.requireSession(w, r)
	if sess == nil {
		return nil, nil
	}
	room, err := s.Store.GetRoom(roomID)
	if err != nil || room == nil {
		writeErr(w, 404, "room not found")
		return nil, nil
	}
	if sess.Kind == auth.KindOwner {
		return sess, room
	}
	if sess.Kind == auth.KindRoom && sess.RoomID == roomID {
		return sess, room
	}
	writeErr(w, 403, "forbidden")
	return nil, nil
}

// roomAccess: room session OR owner may open files/env (owner still sees passwords in list).
func (s *Server) roomAccess(w http.ResponseWriter, r *http.Request, roomID string) (*store.Session, *store.Room) {
	sess := s.requireSession(w, r)
	if sess == nil {
		return nil, nil
	}
	room, err := s.Store.GetRoom(roomID)
	if err != nil || room == nil {
		writeErr(w, 404, "room not found")
		return nil, nil
	}
	if sess.Kind == auth.KindOwner {
		_ = s.Rooms.EnsureUnlocked(roomID)
		s.Projects.SyncRoomFilesVisibility(roomID)
		return sess, room
	}
	if sess.Kind == auth.KindRoom && sess.RoomID == roomID {
		_ = s.Rooms.EnsureUnlocked(roomID)
		s.Projects.SyncRoomFilesVisibility(roomID)
		return sess, room
	}
	writeErr(w, 403, "room password required")
	return nil, nil
}

// resolveRoomFile maps a Files UI path to a host absolute path.
// Project trees may live under /volumes/{id} (Docker /app bind); meta stays in runtime.
func (s *Server) resolveRoomFile(roomID, rel string) (full, pdir, appRoot string, err error) {
	projectsRoot := filepath.Join(s.Cfg.RuntimeDir, roomID, "projects")
	_ = os.MkdirAll(projectsRoot, 0o700)
	rel = filepath.Clean("/" + rel)
	if rel == "/" {
		rel = "."
	} else {
		rel = strings.TrimPrefix(rel, "/")
	}
	if rel == "." || rel == "" {
		return projectsRoot, "", "", nil
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	projID := parts[0]
	pdir = filepath.Join(projectsRoot, projID)
	appRoot = projects.AppFilesRoot(pdir, s.Cfg.VolumesDir)
	if len(parts) == 1 {
		return appRoot, pdir, appRoot, nil
	}
	name := parts[1]
	rest := filepath.Join(parts[1:]...)
	// Panel meta always from runtime project dir
	if len(parts) == 2 && (name == ".env" || name == "mounts.json") {
		return filepath.Join(pdir, name), pdir, appRoot, nil
	}
	// Prefer app root; fall back to pdir for leftover runtime-only files
	cand := filepath.Join(appRoot, rest)
	if _, e := os.Lstat(cand); e == nil {
		return cand, pdir, appRoot, nil
	}
	cand2 := filepath.Join(pdir, rest)
	return cand2, pdir, appRoot, nil
}

func (s *Server) underAllowedFiles(full, projectsRoot, pdir, appRoot string) bool {
	full = filepath.Clean(full)
	check := func(root string) bool {
		if root == "" {
			return false
		}
		root = filepath.Clean(root)
		return full == root || strings.HasPrefix(full, root+string(os.PathSeparator))
	}
	if check(projectsRoot) || check(pdir) || check(appRoot) {
		return true
	}
	vol := filepath.Clean(s.Cfg.VolumesDir)
	return vol != "" && (full == vol || strings.HasPrefix(full, vol+string(os.PathSeparator)))
}

func (s *Server) handleRoomFiles(w http.ResponseWriter, r *http.Request, roomID string) {
	if _, room := s.roomAccess(w, r, roomID); room == nil {
		return
	}
	projectsRoot := filepath.Join(s.Cfg.RuntimeDir, roomID, "projects")
	_ = os.MkdirAll(projectsRoot, 0o700)
	rel := r.URL.Query().Get("path")
	full, pdir, appRoot, _ := s.resolveRoomFile(roomID, rel)
	if !s.underAllowedFiles(full, projectsRoot, pdir, appRoot) {
		writeErr(w, 400, "invalid path")
		return
	}
	relClean := filepath.Clean("/" + rel)
	if relClean == "/" {
		relClean = "."
	} else {
		relClean = strings.TrimPrefix(relClean, "/")
	}
	switch r.Method {
	case http.MethodGet:
		st, err := os.Stat(full)
		if err != nil {
			writeErr(w, 404, "not found")
			return
		}
		if st.IsDir() {
			ents, err := os.ReadDir(full)
			if err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			type item struct {
				Name string `json:"name"`
				Dir  bool   `json:"dir"`
				Size int64  `json:"size"`
			}
			seen := map[string]bool{}
			out := []item{}
			add := func(name string, dir bool, sz int64) {
				if seen[name] {
					return
				}
				seen[name] = true
				out = append(out, item{Name: name, Dir: dir, Size: sz})
			}
			for _, e := range ents {
				info, _ := e.Info()
				var sz int64
				if info != nil {
					sz = info.Size()
				}
				add(e.Name(), e.IsDir(), sz)
			}
			// When browsing app volume root, also expose panel .env / mounts.json
			if pdir != "" && filepath.Clean(full) == filepath.Clean(appRoot) && filepath.Clean(appRoot) != filepath.Clean(pdir) {
				for _, metaName := range []string{".env", "mounts.json"} {
					mp := filepath.Join(pdir, metaName)
					if sti, err := os.Stat(mp); err == nil && !sti.IsDir() {
						add(metaName, false, sti.Size())
					}
				}
			}
			writeJSON(w, 200, map[string]any{"path": relClean, "entries": out})
			return
		}
		if st.Size() > 2<<20 {
			writeErr(w, 400, "file too large to open in editor")
			return
		}
		b, err := os.ReadFile(full)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if isBinaryContent(b) {
			writeJSON(w, 200, map[string]any{
				"path": relClean, "size": st.Size(), "binary": true,
				"content": "", "note": "Binary file — cannot edit as text (database/image/archive).",
			})
			return
		}
		writeJSON(w, 200, map[string]any{"path": relClean, "content": string(b), "size": st.Size(), "binary": false})
	case http.MethodPut:
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if err := os.WriteFile(full, []byte(body.Content), 0o600); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		_ = s.Rooms.Seal(roomID)
		writeJSON(w, 200, map[string]string{"ok": "1"})
	case http.MethodDelete:
		if err := os.RemoveAll(full); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		_ = s.Rooms.Seal(roomID)
		writeJSON(w, 200, map[string]string{"ok": "1"})
	default:
		writeErr(w, 405, "method")
	}
}

func isBinaryContent(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	// NUL byte => binary
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	for i := 0; i < n; i++ {
		if b[i] == 0 {
			return true
		}
	}
	// high ratio of non-text bytes
	nonPrint := 0
	for i := 0; i < n; i++ {
		c := b[i]
		if c == '\n' || c == '\r' || c == '\t' {
			continue
		}
		if c < 0x20 || c > 0x7e {
			nonPrint++
		}
	}
	return nonPrint*100/n > 30
}

func (s *Server) handleRoomExec(w http.ResponseWriter, r *http.Request, roomID string) {
	if _, room := s.roomAccess(w, r, roomID); room == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Command   string `json:"command"`
		ProjectID string `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Command) == "" {
		writeErr(w, 400, "command required")
		return
	}
	cmdLine := body.Command
	_ = s.Rooms.EnsureUnlocked(roomID)

	var cmd *exec.Cmd
	where := "room-fs"
	if body.ProjectID != "" && s.Docker != nil {
		p, _ := s.Store.GetProject(body.ProjectID)
		if p != nil && p.RoomID == roomID && p.ContainerID != "" {
			st, _ := s.Docker.InspectStatus(p.ContainerID)
			if st == "running" {
				cmd = exec.Command("docker", "exec", p.ContainerID, "sh", "-lc", cmdLine)
				where = "container"
			}
		}
	}
	if cmd == nil {
		dir := filepath.Join(s.Cfg.RuntimeDir, roomID)
		if body.ProjectID != "" {
			pdir := s.Rooms.ProjectDir(roomID, body.ProjectID)
			if st, err := os.Stat(pdir); err == nil && st.IsDir() {
				dir = pdir
			}
		}
		_ = os.MkdirAll(dir, 0o750)
		cmd = exec.Command("sh", "-lc", cmdLine)
		cmd.Dir = dir
		where = "room-fs"
	}
	out, err := cmd.CombinedOutput()
	res := map[string]any{"output": string(out), "where": where}
	if err != nil {
		// never leak raw docker "not running" as primary UX when we already fell back;
		// if we still hit docker error somehow, strip noise
		msg := err.Error()
		if strings.Contains(msg, "is not running") {
			msg = "container is stopped — ran in room files instead failed; try Resume"
		}
		res["error"] = msg
		res["exit"] = 1
	} else {
		res["exit"] = 0
	}
	_ = appendLog(s.Cfg.DataDir, "room-"+roomID[:8], "$ "+cmdLine+"\n"+string(out))
	_ = rotateLog(logPath(s.Cfg.DataDir, "room-"+roomID[:8]), 256*1024)
	writeJSON(w, 200, res)
}

func (s *Server) handleRoomLogs(w http.ResponseWriter, r *http.Request, roomID string) {
	if _, room := s.roomAccess(w, r, roomID); room == nil {
		return
	}
	projs, _ := s.Store.ListProjects(roomID)
	var b strings.Builder
	for _, p := range projs {
		b.WriteString("=== " + p.Name + " (" + p.Status + ") ===\n")
		if s.Docker != nil && p.ContainerID != "" {
			out, err := s.Docker.Logs(p.ContainerID, 200)
			if err != nil {
				b.WriteString(err.Error() + "\n")
			} else {
				b.WriteString(out)
			}
		}
		b.WriteString("\n")
	}
	panelLog, _ := tailFile(logPath(s.Cfg.DataDir, "room-"+roomID[:8]), 80*1024)
	if panelLog != "" {
		b.WriteString("=== panel exec log ===\n")
		b.WriteString(panelLog)
	}
	text := b.String()
	if len(text) > 200000 {
		text = text[len(text)-200000:]
	}
	writeJSON(w, 200, map[string]string{"log": text})
}

func (s *Server) handleRoomQuota(w http.ResponseWriter, r *http.Request, roomID string) {
	if _, room := s.canControlRoom(w, r, roomID); room == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		QuotaGB float64 `json:"quota_gb"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if body.QuotaGB <= 0 {
		writeErr(w, 400, "quota_gb is required and must be > 0")
		return
	}
	room, err := s.Store.GetRoom(roomID)
	if err != nil || room == nil {
		writeErr(w, 404, "not found")
		return
	}
	q, err := s.allocateQuota(body.QuotaGB, room.QuotaBytes)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.Rooms.SetQuota(roomID, q); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": "1", "quota_bytes": q})
}

func (s *Server) handleRoomPassword(w http.ResponseWriter, r *http.Request, roomID string) {
	if _, room := s.canControlRoom(w, r, roomID); room == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Password) < 6 {
		writeErr(w, 400, "password too short")
		return
	}
	if err := s.Rooms.SetPassword(roomID, body.Password); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) handleRoomName(w http.ResponseWriter, r *http.Request, roomID string) {
	if _, room := s.canControlRoom(w, r, roomID); room == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if err := s.Rooms.SetName(roomID, body.Name); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1", "name": strings.TrimSpace(body.Name)})
}

func (s *Server) handleDeployPull(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Command string `json:"command"`
		Image   string `json:"image"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	image := strings.TrimSpace(body.Image)
	if image == "" {
		image = parseDockerPull(body.Command)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	logw := &flushWriter{w: w, f: flusher}
	if image == "" {
		fmt.Fprintf(logw, "error: type a docker pull command, e.g. docker pull nginx:alpine\n")
		return
	}
	if s.Docker == nil || !s.Docker.Available() {
		fmt.Fprintf(logw, "error: Docker unavailable\n")
		return
	}
	fmt.Fprintf(logw, "Pulling %s...\n", image)
	if err := s.Docker.PullImage(image, logw); err != nil {
		if s.Docker.ImageExists(image) {
			fmt.Fprintf(logw, "pull skipped — using local image %s\n", image)
			fmt.Fprintf(logw, "OK image=%s\n", image)
			return
		}
		fmt.Fprintf(logw, "error: %v\n", err)
		return
	}
	fmt.Fprintf(logw, "OK image=%s\n", image)
}

func (s *Server) autoDeploy(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	ct := r.Header.Get("Content-Type")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	flusher, _ := w.(http.Flusher)
	logw := &flushWriter{w: w, f: flusher}

	var (
		projName string
		hostPort int
		cPort    int
		quotaGB  float64
		roomHint string
	)

	if strings.Contains(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(256 << 20); err != nil {
			fmt.Fprintf(logw, "error: %v\n", err)
			return
		}
		projName = r.FormValue("name")
		if projName == "" {
			projName = "app"
		}
		roomHint = projName
		hostPort = projects.ParsePort(r.FormValue("host_port"))
		cPort = projects.ParsePort(r.FormValue("container_port"))
		quotaGB, _ = strconv.ParseFloat(strings.TrimSpace(r.FormValue("quota_gb")), 64)
	} else {
		var body struct {
			Name          string  `json:"name"`
			Command       string  `json:"command"`
			Image         string  `json:"image"`
			HostPort      int     `json:"host_port"`
			ContainerPort int     `json:"container_port"`
			QuotaGB       float64 `json:"quota_gb"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fmt.Fprintf(logw, "error: invalid request\n")
			return
		}
		projName = body.Name
		hostPort = body.HostPort
		cPort = body.ContainerPort
		quotaGB = body.QuotaGB
		roomHint = projName
		if roomHint == "" {
			roomHint = body.Image
			if roomHint == "" {
				roomHint = parseDockerPull(body.Command)
			}
		}
		// stash JSON body fields via closure vars for later
		r.Body = io.NopCloser(strings.NewReader(""))
		// re-process below using saved vars
		image := body.Image
		if image == "" {
			image = parseDockerPull(body.Command)
		}
		if image == "" {
			fmt.Fprintf(logw, "error: image or pull command required\n")
			return
		}
		if quotaGB <= 0 {
			fmt.Fprintf(logw, "error: quota_gb is required (set disk space for this project)\n")
			return
		}
		quota, err := s.allocateQuota(quotaGB, 0)
		if err != nil {
			fmt.Fprintf(logw, "error: %v\n", err)
			return
		}
		if projName == "" {
			projName = sanitizeName(image)
		}
		if cPort == 0 {
			cPort = 80
		}
		pass := randomPass(10)
		rm, err := s.Rooms.Create(rooms.CreateInput{
			Name: s.uniqueRoomName(roomHint), Password: pass, QuotaBytes: quota,
		})
		if err != nil {
			fmt.Fprintf(logw, "error: %v\n", err)
			return
		}
		fmt.Fprintf(logw, "Created room %s\npassword: %s\nquota: %.2f GB\n", rm.Name, pass, quotaGB)
		p, err := s.Projects.DeployImage(projects.DeployImageInput{
			RoomID: rm.ID, Name: sanitizeName(projName), Image: image, HostPort: hostPort, ContainerPort: cPort, Log: logw,
		})
		if err != nil {
			fmt.Fprintf(logw, "error: %v\n", err)
			return
		}
		fmt.Fprintf(logw, "OK room=%s room_id=%s project=%s password=%s\n", rm.Name, rm.ID, p.ID, pass)
		return
	}

	if quotaGB <= 0 {
		fmt.Fprintf(logw, "error: quota_gb is required (set disk space for this project)\n")
		return
	}
	quota, err := s.allocateQuota(quotaGB, 0)
	if err != nil {
		fmt.Fprintf(logw, "error: %v\n", err)
		return
	}
	if cPort == 0 {
		cPort = 80
	}
	pass := randomPass(10)
	rm, err := s.Rooms.Create(rooms.CreateInput{
		Name: s.uniqueRoomName(roomHint), Password: pass, QuotaBytes: quota,
	})
	if err != nil {
		fmt.Fprintf(logw, "error: %v\n", err)
		return
	}
	fmt.Fprintf(logw, "Created room %s\npassword: %s\nquota: %.2f GB\n", rm.Name, pass, quotaGB)

	file, hdr, err := r.FormFile("file")
	if err != nil {
		fmt.Fprintf(logw, "error: image/Dockerfile file required\n")
		return
	}
	defer file.Close()
	tmp, _ := os.MkdirTemp("", "vm-up-*")
	defer os.RemoveAll(tmp)
	dest := filepath.Join(tmp, "Dockerfile")
	if strings.EqualFold(filepath.Base(hdr.Filename), "dockerfile") || strings.Contains(strings.ToLower(hdr.Filename), "docker") {
		dest = filepath.Join(tmp, "Dockerfile")
	}
	out, _ := os.Create(dest)
	_, _ = io.Copy(out, file)
	out.Close()
	if filepath.Base(dest) != "Dockerfile" {
		_ = os.Rename(dest, filepath.Join(tmp, "Dockerfile"))
	}
	p, err := s.Projects.DeployBuild(projects.DeployBuildInput{
		RoomID: rm.ID, Name: sanitizeName(projName), HostPort: hostPort, ContainerPort: cPort, SourceDir: tmp, Log: logw,
	})
	if err != nil {
		fmt.Fprintf(logw, "error: %v\n", err)
		return
	}
	fmt.Fprintf(logw, "OK room=%s room_id=%s project=%s password=%s\n", rm.Name, rm.ID, p.ID, pass)
}

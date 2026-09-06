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
	s.Mux.HandleFunc("/api/storage", s.withGate(s.handleStorage))
	s.Mux.HandleFunc("/api/ports", s.withGate(s.handlePorts))
	s.routesBackup()
	s.routesExecJobs()
}

func roomIDFromPath(full, projectsRoot string) string {
	return ""
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if err := validateLinuxRootPassword(body.Password); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := setHostRootPassword(body.Password); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	saveHostPass(s.Cfg.DataDir, body.Password)
	_ = appendLog(s.Cfg.DataDir, "host", "root password changed via panel")
	writeJSON(w, 200, map[string]string{"ok": "1", "changed": "1"})
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
	// Admin password changed -> sign out every device automatically.
	_ = s.Store.DeleteAllSessions()
	s.clearSessionCookie(w)
	s.clearAdminCookie(w)
	s.clearGateCookie(w)
	_ = appendLog(s.Cfg.DataDir, "panel", "admin password changed — all sessions revoked")
	writeJSON(w, 200, map[string]string{"ok": "1", "logout_all": "1"})
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
	_ = rotateLog(path, 256*1024) // keep ~256KB per stream for AI-friendly size
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

// PruneLogsDir trims every *.log under data/logs to maxBytes (periodic cleanup).
func PruneLogsDir(dataDir string, maxBytes int64) {
	if maxBytes <= 0 {
		maxBytes = 256 * 1024
	}
	dir := filepath.Join(dataDir, "logs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		_ = rotateLog(filepath.Join(dir, e.Name()), maxBytes)
	}
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

func (s *Server) hostLogBundle(kind string) (outKind, text string) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" || kind == "vps" || kind == "all" {
		var b strings.Builder
		snap := s.hostSnapshotLog()
		if strings.TrimSpace(snap) != "" {
			b.WriteString(snap)
			b.WriteString("\n")
		}
		labels := map[string]string{
			"panel":  "PANEL & SYSTEM SERVICE",
			"api":    "API & SESSIONS",
			"deploy": "DEPLOYMENTS & RUNTIME",
			"host":   "HOST EVENTS",
		}
		for _, k := range []string{"panel", "api", "deploy", "host"} {
			var t string
			switch k {
			case "panel":
				t, _ = tailFile(logPath(s.Cfg.DataDir, "panel"), 150*1024)
				if j := s.panelJournalLines(120); j != "" {
					sec := "=== vps-rooms service ===\n" + j
					if strings.TrimSpace(t) != "" {
						t = t + "\n" + sec
					} else {
						t = sec
					}
				}
			case "host":
				t, _ = tailFile(logPath(s.Cfg.DataDir, "host"), 100*1024)
			default:
				t, _ = tailFile(logPath(s.Cfg.DataDir, k), 100*1024)
			}
			if strings.TrimSpace(t) == "" || strings.HasPrefix(strings.TrimSpace(t), "(empty") {
				continue
			}
			b.WriteString("===== ")
			b.WriteString(labels[k])
			b.WriteString(" =====\n")
			b.WriteString(strings.TrimSpace(t))
			b.WriteString("\n\n")
		}
		text = strings.TrimSpace(b.String())
		if text == "" {
			text = "(empty — panel events will appear here as you use the panel)"
		}
		return "all", text
	}
	switch kind {
	case "panel", "host", "api", "deploy":
	default:
		kind = "panel"
	}

	text, _ = tailFile(logPath(s.Cfg.DataDir, kind), 200*1024)

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
	return kind, text
}

func (s *Server) handleHostLogs(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "all"
	}
	outKind, text := s.hostLogBundle(kind)
	writeJSON(w, 200, map[string]any{
		"kind":  outKind,
		"log":   text,
		"kinds": []string{"all", "panel", "api", "deploy", "host"},
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
	all := r.URL.Query().Get("all") == "1" || kind == "all" || kind == ""
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
	projectsRoot := s.Rooms.RoomWorkDir(roomID)
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
	return false
}

func (s *Server) handleRoomFiles(w http.ResponseWriter, r *http.Request, roomID string) {
	if _, room := s.roomAccess(w, r, roomID); room == nil {
		return
	}
	projectsRoot := s.Rooms.RoomWorkDir(roomID)
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
				if seen[name] || name == ".env" || strings.HasSuffix(name, ".env") {
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
			// When browsing app volume root, also expose mounts.json if present (secrets remain in Secrets tab)
			if pdir != "" && filepath.Clean(full) == filepath.Clean(appRoot) && filepath.Clean(appRoot) != filepath.Clean(pdir) {
				for _, metaName := range []string{"mounts.json"} {
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
		if relClean == ".env" || strings.HasSuffix(relClean, ".env") {
			writeErr(w, 400, ".env files cannot be deleted here")
			return
		}
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
	// GET polls latest job for this room (refresh persistence)
	if r.Method == http.MethodGet {
		s.handleRoomExecStatus(w, r, roomID)
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Command     string `json:"command"`
		ProjectID   string `json:"project_id"`
		ContainerID string `json:"container_id"`
		TimeoutSec  int    `json:"timeout_sec"`
		Host        bool   `json:"host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Command) == "" {
		writeErr(w, 400, "command required")
		return
	}
	if commandDangerous(body.Command) {
		writeErr(w, 400, "command not allowed")
		return
	}
	cmdLine := body.Command
	_ = s.Rooms.EnsureUnlocked(roomID)

	timeout := 60 * time.Second
	if body.TimeoutSec > 0 {
		timeout = time.Duration(body.TimeoutSec) * time.Second
	}
	maxT := 10 * time.Minute
	if timeout > maxT {
		timeout = maxT
	}
	j := s.startRoomExecJob(roomID, cmdLine, body.ProjectID, body.ContainerID, body.Host, timeout)
	writeJSON(w, 200, map[string]any{"ok": true, "job_id": j.ID, "status": j.Status, "scope": "room", "room_id": roomID})
}

func isHostShellCommand(cmdLine string) bool {
	t := strings.TrimSpace(strings.ToLower(cmdLine))
	if t == "" {
		return false
	}
	first := strings.Fields(t)[0]
	switch first {
	case "docker", "ctr", "nerdctl", "systemctl", "journalctl", "ss", "ip", "iptables", "ufw", "apt", "apt-get", "dnf", "yum":
		return true
	}
	return strings.HasPrefix(t, "sudo docker") || strings.HasPrefix(t, "sudo systemctl")
}

func (s *Server) resolveRoomContainer(roomID, want string) *store.Container {
	list, _ := s.Store.ListContainers(roomID)
	want = strings.TrimSpace(want)
	if want != "" {
		low := strings.ToLower(want)
		for i := range list {
			c := list[i]
			if c.ID == want || c.DockerID == want || strings.EqualFold(c.Name, want) || strings.EqualFold(c.Service, want) {
				return &c
			}
			did := strings.ToLower(strings.TrimPrefix(c.DockerID, "sha256:"))
			if len(did) >= 12 && (did == low || strings.HasPrefix(did, low) || strings.HasPrefix(low, did[:12])) {
				return &c
			}
		}
	} else {
		for i := range list {
			c := list[i]
			if s.Docker != nil && c.DockerID != "" {
				if st, err := s.Docker.InspectStatus(c.DockerID); err == nil && st == "running" {
					return &c
				}
			}
			if c.Status == "running" {
				return &c
			}
		}
		if len(list) > 0 {
			return &list[0]
		}
	}
	projs, _ := s.Store.ListProjects(roomID)
	matchProj := func(p store.Project) *store.Container {
		st := p.Status
		if s.Docker != nil && p.ContainerID != "" {
			if x, err := s.Docker.InspectStatus(p.ContainerID); err == nil && x != "" {
				st = x
			}
		}
		return &store.Container{
			ID: p.ID, RoomID: p.RoomID, Name: p.Name, Service: p.Name,
			Image: p.Image, DockerID: p.ContainerID, HostPort: p.HostPort,
			ContainerPort: p.ContainerPort, Status: st,
		}
	}
	if want != "" {
		for _, p := range projs {
			if p.ID == want || p.ContainerID == want || strings.EqualFold(p.Name, want) {
				return matchProj(p)
			}
		}
		return nil
	}
	for _, p := range projs {
		c := matchProj(p)
		if c.Status == "running" {
			return c
		}
	}
	if len(projs) > 0 {
		return matchProj(projs[0])
	}
	return nil
}

func (s *Server) containerLogsJSON(roomID, want string) (map[string]any, int) {
	list := s.roomContainersJSON(roomID)
	want = strings.TrimSpace(want)
	ct := s.resolveRoomContainer(roomID, want)
	if ct == nil {
		if want == "" && len(list) == 0 {
			return map[string]any{
				"ok": false, "error": "no containers", "code": "no containers", "containers": list,
			}, 404
		}
		if want == "" {
			return map[string]any{
				"ok": false, "error": "pass name=CONTAINER_NAME or container=CONTAINER_ID",
				"code": "logs_target_required", "containers": list,
			}, 400
		}
		return map[string]any{
			"ok": false, "error": "container not found", "code": "container_not_found",
			"containers": list,
		}, 404
	}
	ref := ct.DockerID
	if ref == "" {
		ref = ct.Name
	}
	name := ct.Name
	if ct.Service != "" {
		name = ct.Service
	}
	var b strings.Builder
	if s.Docker != nil && ref != "" {
		out, err := s.Docker.Logs(ref, 300)
		if err != nil {
			b.WriteString(err.Error())
		} else {
			b.WriteString(out)
		}
	}
	text := b.String()
	if len(text) > 200000 {
		text = text[len(text)-200000:]
	}
	return map[string]any{
		"log":          text,
		"container_id": ct.ID,
		"container":    name,
		"name":         name,
		"containers":   list,
	}, 200
}

func (s *Server) writeContainerLogs(w http.ResponseWriter, roomID, want string) {
	payload, code := s.containerLogsJSON(roomID, want)
	writeJSON(w, code, payload)
}

func logsQueryTarget(r *http.Request) string {
	q := r.URL.Query()
	if name := strings.TrimSpace(q.Get("name")); name != "" {
		return name
	}
	return strings.TrimSpace(q.Get("container"))
}

func (s *Server) handleRoomLogs(w http.ResponseWriter, r *http.Request, roomID string) {
	if _, room := s.roomAccess(w, r, roomID); room == nil {
		return
	}
	s.handleRoomLogsDirect(w, r, roomID)
}

func (s *Server) handleRoomLogsDirect(w http.ResponseWriter, r *http.Request, roomID string) {
	q := r.URL.Query()
	want := logsQueryTarget(r)
	if r.Method == http.MethodDelete || q.Get("clear") == "1" || q.Get("action") == "clear" {
		ct := s.resolveRoomContainer(roomID, want)
		if ct != nil && ct.DockerID != "" {
			out, err := exec.Command("docker", "inspect", "--format", "{{.LogPath}}", ct.DockerID).Output()
			if err == nil {
				lp := strings.TrimSpace(string(out))
				if lp != "" && filepath.IsAbs(lp) {
					_ = os.Truncate(lp, 0)
				}
			}
		}
		_ = os.Truncate(filepath.Join(s.Cfg.DataDir, "logs", "rooms", roomID+".log"), 0)
		writeJSON(w, 200, map[string]any{"ok": true, "cleared": true})
		return
	}
	if want != "" {
		s.writeContainerLogs(w, roomID, want)
		return
	}
	projs, _ := s.Store.ListProjects(roomID)
	if len(projs) > 0 && projs[0].ContainerID != "" {
		s.writeContainerLogs(w, roomID, projs[0].ContainerID)
		return
	}
	writeJSON(w, 200, map[string]any{"log": "", "note": "no container yet"})
}

func (s *Server) handleRoomEnv(w http.ResponseWriter, r *http.Request, roomID string) {
	if _, room := s.roomAccess(w, r, roomID); room == nil {
		return
	}
	_ = s.Rooms.EnsureUnlocked(roomID)
	path := s.roomEnvPath(roomID)
	projs, _ := s.Store.ListProjects(roomID)
	var first *store.Project
	if len(projs) > 0 {
		first = &projs[0]
	}
	if r.Method == http.MethodGet {
		b, _ := os.ReadFile(path)
		text := string(b)
		if strings.TrimSpace(text) == "" && first != nil {
			if t, err := s.Projects.ReadEnv(first.ID); err == nil {
				text = t
			}
		}
		writeJSON(w, 200, map[string]any{
			"editable": true,
			"content":  text,
			"path":     path,
		})
		return
	}
	if r.Method == http.MethodPut || r.Method == http.MethodPost {
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		if err := s.writeRoomEnv(roomID, body.Content); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"ok": "1", "recreated": "1"})
		return
	}
	writeErr(w, 405, "method")
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
	if err := s.Projects.ApplyQuota(roomID, q); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": "1", "quota_bytes": q, "applied": true})
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
	tok := s.cookieToken(r)
	kickSelf := false
	if tok != "" {
		if sess, _ := s.Store.GetSession(tok); sess != nil && sess.Kind == "room" && sess.RoomID == roomID {
			kickSelf = true
		}
	}
	// Force every device out of this room so the new password is required.
	_ = s.Store.DeleteSessionsByRoom(roomID)
	if kickSelf {
		s.clearSessionCookie(w)
	}
	writeJSON(w, 200, map[string]any{"ok": "1", "logged_out": true})
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

func (s *Server) handleRoomUpdate(w http.ResponseWriter, r *http.Request, roomID string) {
	room, p0, err := s.resolveRoomProject(roomID)
	if err != nil || room == nil {
		writeErr(w, 404, "not found")
		return
	}
	if _, ctrl := s.canControlRoom(w, r, room.ID); ctrl == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Image     string `json:"image"`
		ProjectID string `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	image := strings.TrimSpace(body.Image)
	if image == "" {
		writeErr(w, 400, "image required")
		return
	}
	projs, _ := s.Store.ListProjects(room.ID)
	if len(projs) == 0 {
		writeErr(w, 400, "no container to update")
		return
	}
	pid := strings.TrimSpace(body.ProjectID)
	if pid == "" && p0 != nil {
		pid = p0.ID
	}
	if pid == "" {
		pid = projs[0].ID
	}
	found := false
	for _, p := range projs {
		if p.ID == pid {
			found = true
			break
		}
	}
	if !found {
		writeErr(w, 404, "project not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	logw := &flushWriter{w: w, f: flusher}
	if err := s.Projects.UpdateImage(pid, image, logw); err != nil {
		fmt.Fprintf(logw, "error: %v\n", err)
		return
	}
	_ = appendLog(s.Cfg.DataDir, "deploy", "UPDATE project="+pid+" room="+room.ID+" image="+image)
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
		fmt.Fprintf(logw, "error: manual upload removed — create via image name only\n")
		return
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
}

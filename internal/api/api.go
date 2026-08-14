package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/x5coder/vps-rooms/internal/auth"
	"github.com/x5coder/vps-rooms/internal/backup"
	"github.com/x5coder/vps-rooms/internal/config"
	"github.com/x5coder/vps-rooms/internal/dockerx"
	"github.com/x5coder/vps-rooms/internal/metrics"
	"github.com/x5coder/vps-rooms/internal/projects"
	"github.com/x5coder/vps-rooms/internal/proxy"
	"github.com/x5coder/vps-rooms/internal/rooms"
	"github.com/x5coder/vps-rooms/internal/store"
	"github.com/x5coder/vps-rooms/internal/telegram"
)

type Server struct {
	Cfg      config.Config
	Store    *store.Store
	Rooms    *rooms.Service
	Projects *projects.Service
	Docker   *dockerx.Client
	Metrics  *metrics.Hub
	Gate     *telegram.Gate
	Notify   *telegram.Notifier
	Proxy    *proxy.Manager
	Backup   *backup.Service
	Mux      *http.ServeMux
}

func New(cfg config.Config, st *store.Store, docker *dockerx.Client, hub *metrics.Hub) *Server {
	rs := &rooms.Service{Store: st, Docker: docker, RoomsDir: cfg.RoomsDir, RuntimeDir: cfg.RuntimeDir}
	ps := &projects.Service{Store: st, Docker: docker, Rooms: rs, VolumesDir: cfg.VolumesDir}
	proxyDir := ensureProxyDir(cfg.DataDir)
	work := filepath.Join(cfg.DataDir, "backup-work")
	_ = os.MkdirAll(work, 0o750)
	bs := &backup.Service{
		Store: st, Rooms: rs, Projects: ps, Docker: docker,
		DataDir: cfg.DataDir, RoomsDir: cfg.RoomsDir, RuntimeDir: cfg.RuntimeDir,
		ProxyDir: proxyDir, DBPath: cfg.DBPath, OwnerPass: cfg.OwnerPass, WorkDir: work,
	}
	s := &Server{
		Cfg: cfg, Store: st, Rooms: rs, Projects: ps, Docker: docker, Metrics: hub,
		Gate: telegram.NewGate(cfg.DataDir), Notify: telegram.NewNotifier(cfg.DataDir),
		Proxy: proxy.New(proxyDir), Backup: bs,
		Mux: http.NewServeMux(),
	}
	bs.OnAfterRestore = func() error { return s.syncProxy() }
	s.routes()
	bs.StartScheduler()
	_ = s.syncProxy()
	return s
}

func (s *Server) routes() {
	s.Mux.HandleFunc("/api/health", s.handleHealth)
	s.Mux.HandleFunc("/api/gate/status", s.handleGateStatus)
	s.Mux.HandleFunc("/api/gate/challenge", s.handleGateChallenge)
	s.Mux.HandleFunc("/api/gate/verify", s.handleGateVerify)

	s.Mux.HandleFunc("/api/auth/room/login", s.withGate(s.handleRoomLogin))
	s.Mux.HandleFunc("/api/auth/owner", s.withGate(s.handleOwnerLogin))
	s.Mux.HandleFunc("/api/auth/admin", s.withGate(s.handleAdminRestore))
	s.Mux.HandleFunc("/api/auth/logout", s.withGate(s.handleLogout))
	s.Mux.HandleFunc("/api/auth/me", s.withGate(s.handleMe))
	s.Mux.HandleFunc("/api/auth/options", s.withGate(s.handleAuthOptions))

	s.Mux.HandleFunc("/api/rooms", s.withGate(s.handleRooms))
	s.Mux.HandleFunc("/api/rooms/", s.withGate(s.handleRoomByID))

	s.Mux.HandleFunc("/api/projects", s.withGate(s.handleProjects))
	s.Mux.HandleFunc("/api/projects/", s.withGate(s.handleProjectByID))

	s.Mux.HandleFunc("/api/metrics", s.withGate(s.handleMetricsSnapshot))
	s.Mux.HandleFunc("/api/ws/metrics", s.withGateWS(s.Metrics.HandleWS))

	s.Mux.HandleFunc("/api/host", s.withGate(s.handleHostInfo))
	s.Mux.HandleFunc("/api/deploy/pull", s.withGate(s.handleDeployPull))
	s.Mux.HandleFunc("/api/deploy/ai", s.withGate(s.handleDeployAI))
	s.Mux.HandleFunc("/api/deploy/exec", s.withGate(s.handleDeployExec))
	s.Mux.HandleFunc("/api/deploy", s.withGate(s.autoDeploy))
	s.Mux.HandleFunc("/api/tokens/ai", s.withGate(s.handleTokensAI))
	s.Mux.HandleFunc("/api/logs/ai", s.withGate(s.handleLogsAI))
	s.Mux.HandleFunc("/api/usage/ai", s.withGate(s.handleUsageAI))
	s.routesManage()
	s.routesAPITokens()
	s.routesBackupDomain()
}

func clientIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return xff
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		return xri
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		// strip port; keep IPv6 brackets stripped simply
		return strings.Trim(host[:i], "[]")
	}
	return host
}

func (s *Server) alertAccess(r *http.Request, title, action, detail, room string) {
	line := fmt.Sprintf("%s | %s", action, title)
	if detail != "" {
		line += " — " + detail
	}
	if room != "" {
		line += " · room=" + room
	}
	if r != nil {
		line += " · ip=" + clientIP(r)
	}
	_ = appendLog(s.Cfg.DataDir, "panel", line)
	if s.Notify == nil {
		return
	}
	s.Notify.Alert(telegram.AccessEvent{
		Title:     title,
		Action:    action,
		Detail:    detail,
		IP:        clientIP(r),
		UserAgent: r.UserAgent(),
		Room:      room,
	})
}

func (s *Server) Handler(webFS http.FileSystem) http.Handler {
	static := http.FileServer(webFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.Mux.ServeHTTP(w, r)
			return
		}
		path := r.URL.Path
		if strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".css") || path == "/" || isAppRoute(path) {
			w.Header().Set("Cache-Control", "no-store, max-age=0, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
		}
		if isAppRoute(path) {
			serveIndex(w, r, webFS)
			return
		}
		static.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, webFS http.FileSystem) {
	f, err := webFS.Open("/index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	stat, _ := f.Stat()
	http.ServeContent(w, r, "index.html", stat.ModTime(), f.(io.ReadSeeker))
}

func isAppRoute(path string) bool {
	if path == "/" {
		return true
	}
	base := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		base = path[i+1:]
	}
	return base == "" || !strings.Contains(base, ".")
}

func (s *Server) withGate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hasGateSession(r) {
			writeErr(w, 401, telegram.DeniedMsg)
			return
		}
		next(w, r)
	}
}

func (s *Server) withGateWS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.hasGateSession(r) {
			http.Error(w, telegram.DeniedMsg, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) hasGateSession(r *http.Request) bool {
	c, err := r.Cookie("vr_gate")
	if err != nil || c.Value == "" {
		return false
	}
	sess, err := s.Store.GetSession(c.Value)
	return err == nil && sess != nil && sess.Kind == "gate"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) cookieToken(r *http.Request) string {
	c, err := r.Cookie("vr_session")
	if err != nil {
		return ""
	}
	return c.Value
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	maxAge := s.Cfg.SessionHours * 3600
	if maxAge <= 0 {
		maxAge = 24 * 3600
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "vr_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "vr_session", Value: "", Path: "/", MaxAge: -1})
}

func (s *Server) setAdminCookie(w http.ResponseWriter, token string) {
	// Sticky admin identity — survives room enters; longer than a tab session.
	maxAge := s.Cfg.SessionHours * 3600
	if maxAge <= 0 {
		maxAge = 24 * 3600
	}
	if maxAge < 7*24*3600 {
		maxAge = 7 * 24 * 3600
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "vr_admin",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

func (s *Server) clearAdminCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "vr_admin", Value: "", Path: "/", MaxAge: -1})
}

func (s *Server) adminToken(r *http.Request) string {
	c, err := r.Cookie("vr_admin")
	if err != nil {
		return ""
	}
	return c.Value
}

func (s *Server) stickyOwner(r *http.Request) *store.Session {
	at := s.adminToken(r)
	if at == "" {
		return nil
	}
	sess, err := s.Store.GetSession(at)
	if err != nil || sess == nil || sess.Kind != auth.KindOwner {
		return nil
	}
	return sess
}

func (s *Server) requireSession(w http.ResponseWriter, r *http.Request) *store.Session {
	// Sticky admin wins — opening/creating projects must not drop the owner session.
	if owner := s.stickyOwner(r); owner != nil {
		return owner
	}
	tok := s.cookieToken(r)
	if tok == "" {
		writeErr(w, 401, "غير مصرح")
		return nil
	}
	sess, err := s.Store.GetSession(tok)
	if err != nil || sess == nil {
		writeErr(w, 401, "الجلسة منتهية")
		return nil
	}
	return sess
}

func (s *Server) requireOwner(w http.ResponseWriter, r *http.Request) *store.Session {
	sess := s.requireSession(w, r)
	if sess == nil {
		return nil
	}
	if sess.Kind != auth.KindOwner {
		writeErr(w, 403, "صلاحية المالك مطلوبة")
		return nil
	}
	return sess
}

func (s *Server) requireRoom(w http.ResponseWriter, r *http.Request) *store.Session {
	sess := s.requireSession(w, r)
	if sess == nil {
		return nil
	}
	if sess.Kind != auth.KindRoom || sess.RoomID == "" {
		writeErr(w, 403, "دخول الغرفة مطلوب")
		return nil
	}
	return sess
}

func (s *Server) setGateCookie(w http.ResponseWriter, token string) {
	maxAge := s.Cfg.SessionHours * 3600
	if maxAge <= 0 {
		maxAge = 24 * 3600
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "vr_gate",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

func (s *Server) clearGateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "vr_gate", Value: "", Path: "/", MaxAge: -1})
}

func (s *Server) handleGateStatus(w http.ResponseWriter, r *http.Request) {
	// Never return chat id or token — only opaque flags.
	writeJSON(w, 200, map[string]any{
		"configured": s.Gate.HasLockedChat(),
		"unlocked":   s.hasGateSession(r),
	})
}

func (s *Server) handleGateChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		BotToken string `json:"bot_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 403, telegram.DeniedMsg)
		return
	}
	res, err := s.Gate.Challenge(body.BotToken)
	if err != nil {
		writeErr(w, 403, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) handleGateVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		BotToken string `json:"bot_token"`
		Code     string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 403, telegram.DeniedMsg)
		return
	}
	if err := s.Gate.Verify(body.BotToken, body.Code); err != nil {
		writeErr(w, 403, telegram.DeniedMsg)
		return
	}
	hours := s.Cfg.SessionHours
	if hours <= 0 {
		hours = 24
	}
	gateTok, err := auth.CreateSession(s.Store, "gate", "", hours)
	if err != nil {
		writeErr(w, 403, telegram.DeniedMsg)
		return
	}
	// Gate only — admin panel still requires the fixed owner password (never returned to clients).
	s.setGateCookie(w, gateTok)
	s.clearSessionCookie(w)
	s.alertAccess(r, "Panel unlocked", "telegram_otp_ok", "Telegram OTP verified — panel gate opened", "")
	writeJSON(w, 200, map[string]string{"ok": "1", "kind": "gate"})
}

func (s *Server) handleOwnerLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 401, "Invalid credentials")
		return
	}
	if s.Cfg.OwnerPass == "" || body.Password != s.Cfg.OwnerPass {
		writeErr(w, 401, "Invalid credentials")
		return
	}
	hours := s.Cfg.SessionHours
	if hours <= 0 {
		hours = 24
	}
	adminHours := hours
	if adminHours < 24*7 {
		adminHours = 24 * 7
	}
	if tok := s.cookieToken(r); tok != "" {
		_ = s.Store.DeleteSession(tok)
	}
	ownerTok, err := auth.CreateSession(s.Store, auth.KindOwner, "", hours)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	adminSticky, err := auth.CreateSession(s.Store, auth.KindOwner, "", adminHours)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.setSessionCookie(w, ownerTok)
	s.setAdminCookie(w, adminSticky)
	s.alertAccess(r, "Admin login", "admin_password_ok", "Admin vault unlocked with panel password", "")
	writeJSON(w, 200, map[string]string{"ok": "1", "kind": "owner"})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"ok":     true,
		"docker": s.Docker != nil && s.Docker.Available(),
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleAdminRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	hours := s.Cfg.SessionHours
	if hours <= 0 {
		hours = 24
	}
	adminHours := hours
	if adminHours < 24*7 {
		adminHours = 24 * 7
	}

	// Already proven admin this browser session (sticky cookie) — restore without re-prompt.
	if at := s.adminToken(r); at != "" {
		if sess, err := s.Store.GetSession(at); err == nil && sess != nil && sess.Kind == auth.KindOwner {
			if tok := s.cookieToken(r); tok != "" && tok != at {
				_ = s.Store.DeleteSession(tok)
			}
			// Mint a fresh session token; keep sticky admin cookie intact across room enters.
			ownerTok, err := auth.CreateSession(s.Store, auth.KindOwner, "", hours)
			if err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			s.setSessionCookie(w, ownerTok)
			writeJSON(w, 200, map[string]string{"ok": "1", "kind": "owner"})
			return
		}
	}

	// Fallback: explicit password (first unlock or sticky expired).
	if s.Cfg.OwnerPass == "" || body.Password != s.Cfg.OwnerPass {
		writeErr(w, 401, "Invalid credentials")
		return
	}
	if tok := s.cookieToken(r); tok != "" {
		_ = s.Store.DeleteSession(tok)
	}
	ownerTok, err := auth.CreateSession(s.Store, auth.KindOwner, "", hours)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	adminSticky, err := auth.CreateSession(s.Store, auth.KindOwner, "", adminHours)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.setSessionCookie(w, ownerTok)
	s.setAdminCookie(w, adminSticky)
	s.alertAccess(r, "Admin login", "admin_password_ok", "Admin vault unlocked with panel password", "")
	writeJSON(w, 200, map[string]string{"ok": "1", "kind": "owner"})
}

func (s *Server) handleAuthOptions(w http.ResponseWriter, r *http.Request) {
	rooms, err := s.Store.ListRooms()
	if err != nil {
		writeJSON(w, 200, map[string]any{"has_rooms": false})
		return
	}
	writeJSON(w, 200, map[string]any{"has_rooms": len(rooms) > 0})
}

func (s *Server) handleRoomLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}
	pass := strings.TrimSpace(body.Password)
	if pass == "" {
		writeErr(w, 401, "Wrong password")
		return
	}
	var room *store.Room
	var err error
	name := strings.TrimSpace(body.Name)
	if name != "" {
		room, err = s.Store.GetRoomByName(name)
		if err != nil || room == nil || !auth.CheckPassword(room.PassHash, pass) {
			writeErr(w, 401, "Wrong password")
			return
		}
	} else {
		rooms, listErr := s.Store.ListRooms()
		if listErr != nil {
			writeErr(w, 500, listErr.Error())
			return
		}
		for i := range rooms {
			if auth.CheckPassword(rooms[i].PassHash, pass) {
				r := rooms[i]
				room = &r
				break
			}
		}
		if room == nil {
			writeErr(w, 401, "Wrong password")
			return
		}
	}
	tok, err := auth.CreateSession(s.Store, auth.KindRoom, room.ID, s.Cfg.SessionHours)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.setSessionCookie(w, tok)
	s.alertAccess(r, "Room login", "room_login_ok", "Direct room unlock from gate screen", room.Name)
	writeJSON(w, 200, map[string]any{"ok": "1", "kind": "room", "room": map[string]string{"id": room.ID, "name": room.Name}})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if tok := s.cookieToken(r); tok != "" {
		_ = s.Store.DeleteSession(tok)
	}
	if at := s.adminToken(r); at != "" {
		_ = s.Store.DeleteSession(at)
	}
	if c, err := r.Cookie("vr_gate"); err == nil && c.Value != "" {
		_ = s.Store.DeleteSession(c.Value)
	}
	s.clearSessionCookie(w)
	s.clearAdminCookie(w)
	s.clearGateCookie(w)
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSession(w, r)
	if sess == nil {
		return
	}
	// If sticky admin is present, keep the browser session as owner (no re-login).
	if owner := s.stickyOwner(r); owner != nil {
		if tok := s.cookieToken(r); tok == "" || tok != owner.Token {
			s.setSessionCookie(w, owner.Token)
		}
		sess = owner
	}
	out := map[string]any{"kind": sess.Kind}
	if sess.Kind == auth.KindRoom {
		room, _ := s.Store.GetRoom(sess.RoomID)
		if room != nil {
			out["room"] = map[string]any{"id": room.ID, "name": room.Name, "quota_bytes": room.QuotaBytes}
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if s.requireOwner(w, r) == nil {
			return
		}
		list, err := s.Store.ListRooms()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		type roomOut struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Password   string `json:"password"`
			QuotaBytes int64  `json:"quota_bytes"`
			UsageBytes int64  `json:"usage_bytes"`
			Projects   int    `json:"projects"`
			HostPort   int    `json:"host_port"`
			Image      string `json:"image"`
			Status     string `json:"status"`
			CreatedAt  string `json:"created_at"`
			Locked     bool   `json:"locked"`
		}
		out := make([]roomOut, 0, len(list))
		for _, rm := range list {
			usage, _ := s.Rooms.UsageBytes(rm.ID)
			projs, _ := s.Projects.List(rm.ID)
			st := "empty"
			hostPort := 0
			image := ""
			if len(projs) > 0 {
				st = "stopped"
				hostPort = projs[0].HostPort
				image = projs[0].Image
				for _, p := range projs {
					if p.Status == "running" {
						st = "running"
						break
					}
				}
			}
			out = append(out, roomOut{
				ID: rm.ID, Name: rm.Name, Password: rm.PassPlain,
				QuotaBytes: rm.QuotaBytes,
				UsageBytes: usage, Projects: len(projs), HostPort: hostPort, Image: image, Status: st,
				CreatedAt: rm.CreatedAt.UTC().Format(time.RFC3339),
				Locked:    false, // admin vault shows all room passwords
			})
		}
		writeJSON(w, 200, out)
	case http.MethodPost:
		writeErr(w, 400, "empty rooms are disabled — deploy a project to create a room")
		return
	default:
		writeErr(w, 405, "method")
	}
}

func (s *Server) handleRoomByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/rooms/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeErr(w, 404, "not found")
		return
	}
	id := parts[0]
	if len(parts) >= 2 {
		switch parts[1] {
		case "enter":
			if r.Method != http.MethodPost {
				writeErr(w, 405, "method")
				return
			}
			// Owner or anyone with gate may enter ONLY with the room password (no free peek).
			sess := s.requireSession(w, r)
			if sess == nil {
				return
			}
			var body struct {
				Password string `json:"password"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			room, err := s.Store.GetRoom(id)
			if err != nil || room == nil {
				writeErr(w, 404, "room not found")
				return
			}
			if body.Password == "" || !auth.CheckPassword(room.PassHash, body.Password) {
				writeErr(w, 401, "room password required")
				return
			}
			if err := s.Rooms.Unlock(room.ID, body.Password); err != nil {
				writeErr(w, 401, err.Error())
				return
			}
			roomPayload := map[string]string{"id": room.ID, "name": room.Name}
			// Admin stays admin — opening projects must not force a full re-login.
			if sess.Kind == auth.KindOwner {
				s.alertAccess(r, "Opened room", "room_enter_ok", "Admin opened room (session kept)", room.Name)
				writeJSON(w, 200, map[string]any{"ok": "1", "kind": "owner", "room": roomPayload})
				return
			}
			// Sticky admin cookie: restore owner session instead of downgrading to room.
			if at := s.adminToken(r); at != "" {
				if as, err := s.Store.GetSession(at); err == nil && as != nil && as.Kind == auth.KindOwner {
					if tok := s.cookieToken(r); tok != "" && tok != at {
						_ = s.Store.DeleteSession(tok)
					}
					hours := s.Cfg.SessionHours
					if hours <= 0 {
						hours = 24
					}
					ownerTok, err := auth.CreateSession(s.Store, auth.KindOwner, "", hours)
					if err != nil {
						writeErr(w, 500, err.Error())
						return
					}
					s.setSessionCookie(w, ownerTok)
					s.alertAccess(r, "Opened room", "room_enter_ok", "Admin opened room via sticky session", room.Name)
					writeJSON(w, 200, map[string]any{"ok": "1", "kind": "owner", "room": roomPayload})
					return
				}
			}
			if tok := s.cookieToken(r); tok != "" {
				_ = s.Store.DeleteSession(tok)
			}
			tok, err := auth.CreateSession(s.Store, auth.KindRoom, room.ID, s.Cfg.SessionHours)
			if err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			s.setSessionCookie(w, tok)
			s.alertAccess(r, "Opened room", "room_enter_ok", "Unlocked room contents with room password", room.Name)
			writeJSON(w, 200, map[string]any{"ok": "1", "kind": "room", "room": roomPayload})
			return
		case "files":
			s.handleRoomFiles(w, r, id)
			return
		case "exec":
			s.handleRoomExec(w, r, id)
			return
		case "ai":
			s.handleRoomAI(w, r, id)
			return
		case "logs":
			s.handleRoomLogs(w, r, id)
			return
		case "quota":
			s.handleRoomQuota(w, r, id)
			return
		case "password":
			s.handleRoomPassword(w, r, id)
			return
		case "name":
			s.handleRoomName(w, r, id)
			return
		case "pause":
			if r.Method != http.MethodPost {
				writeErr(w, 405, "method")
				return
			}
			if _, room := s.canControlRoom(w, r, id); room == nil {
				return
			}
			projs, _ := s.Store.ListProjects(id)
			for _, p := range projs {
				_ = s.Projects.Stop(p.ID)
			}
			writeJSON(w, 200, map[string]string{"ok": "1"})
			return
		case "resume":
			if r.Method != http.MethodPost {
				writeErr(w, 405, "method")
				return
			}
			if _, room := s.canControlRoom(w, r, id); room == nil {
				return
			}
			projs, _ := s.Store.ListProjects(id)
			for _, p := range projs {
				_ = s.Projects.Start(p.ID)
			}
			writeJSON(w, 200, map[string]string{"ok": "1"})
			return
		}
	}
	if r.Method == http.MethodDelete {
		if s.requireOwner(w, r) == nil {
			return
		}
		if err := s.Rooms.Delete(id); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"ok": "1"})
		return
	}
	if r.Method == http.MethodGet {
		sess, room := s.roomAccess(w, r, id)
		if room == nil {
			return
		}
		projs, _ := s.Projects.List(id)
		if projs == nil {
			projs = []store.Project{}
		}
		usage, _ := s.Rooms.UsageBytes(id)
		st := s.storageInfo()
		avail := asInt64(st["quota_available"]) + room.QuotaBytes // can grow into free disk + reclaim current
		if avail < room.QuotaBytes {
			avail = room.QuotaBytes
		}
		var enriched []map[string]any
		for i := range projs {
			p := projs[i]
			enriched = append(enriched, map[string]any{
				"id": p.ID, "room_id": p.RoomID, "name": p.Name, "image": p.Image,
				"container_id": p.ContainerID, "host_port": p.HostPort, "container_port": p.ContainerPort,
				"domain": p.Domain, "domain_enabled": p.DomainEnabled, "ssl_status": p.SSLStatus,
				"external_url": p.ExternalURL, "status": p.Status, "created_at": p.CreatedAt,
				"links": s.projectLinks(r, &p),
			})
		}
		writeJSON(w, 200, map[string]any{
			"id": room.ID, "name": room.Name,
			"password":    roomPasswordForSession(sess, room),
			"quota_bytes": room.QuotaBytes, "usage_bytes": usage, "projects": enriched,
			"public_host":        s.publicHost(r),
			"disk_free":          st["disk_free"],
			"quota_available_gb": float64(avail) / (1024 * 1024 * 1024),
			"quota_max_gb":       float64(avail) / (1024 * 1024 * 1024),
		})
		return
	}
	writeErr(w, 405, "method")
}

func roomPasswordForSession(sess *store.Session, room *store.Room) string {
	if sess == nil || room == nil {
		return ""
	}
	if sess.Kind == auth.KindOwner {
		return room.PassPlain
	}
	if sess.Kind == auth.KindRoom && sess.RoomID == room.ID {
		return room.PassPlain
	}
	return ""
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	sess := s.requireRoom(w, r)
	if sess == nil {
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := s.Projects.List(sess.RoomID)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if list == nil {
			list = []store.Project{}
		}
		writeJSON(w, 200, list)
	case http.MethodPost:
		ct := r.Header.Get("Content-Type")
		if strings.Contains(ct, "multipart/form-data") {
			s.handleUploadDeploy(w, r, sess.RoomID)
			return
		}
		var body struct {
			Mode          string `json:"mode"` // image | command
			Name          string `json:"name"`
			Image         string `json:"image"`
			Command       string `json:"command"`
			HostPort      int    `json:"host_port"`
			ContainerPort int    `json:"container_port"`
			Env           string `json:"env"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "طلب غير صالح")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		flusher, _ := w.(http.Flusher)
		log := &flushWriter{w: w, f: flusher}
		image := body.Image
		if body.Mode == "command" || image == "" {
			image = parseDockerPull(body.Command)
		}
		if image == "" {
			fmt.Fprintln(log, "خطأ: لم يتم تحديد صورة Docker")
			return
		}
		name := body.Name
		if name == "" {
			name = sanitizeName(image)
		}
		p, err := s.Projects.DeployImage(projects.DeployImageInput{
			RoomID: sess.RoomID, Name: name, Image: image,
			HostPort: body.HostPort, ContainerPort: body.ContainerPort, EnvText: body.Env, Log: log,
		})
		if err != nil {
			fmt.Fprintf(log, "خطأ: %v\n", err)
			_ = appendLog(s.Cfg.DataDir, "deploy", "FAIL room="+sess.RoomID+" name="+name+" image="+image+" err="+err.Error())
			return
		}
		_ = appendLog(s.Cfg.DataDir, "deploy", "OK project="+p.ID+" name="+name+" image="+image+" port="+strconv.Itoa(p.HostPort))
		fmt.Fprintf(log, "OK %s\n", p.ID)
	default:
		writeErr(w, 405, "method")
	}
}

func (s *Server) handleUploadDeploy(w http.ResponseWriter, r *http.Request, roomID string) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeErr(w, 400, "فشل قراءة الملف")
		return
	}
	name := r.FormValue("name")
	envText := r.FormValue("env")
	hostPort := projects.ParsePort(r.FormValue("host_port"))
	cPort := projects.ParsePort(r.FormValue("container_port"))
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "الملف مطلوب")
		return
	}
	defer file.Close()
	tmp, err := os.MkdirTemp("", "vps-rooms-upload-*")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer os.RemoveAll(tmp)
	destName := filepath.Base(hdr.Filename)
	if destName == "" {
		destName = "Dockerfile"
	}
	dest := filepath.Join(tmp, destName)
	out, err := os.Create(dest)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		writeErr(w, 500, err.Error())
		return
	}
	out.Close()
	// If uploaded Dockerfile, ensure name
	if strings.ToLower(destName) != "dockerfile" {
		// wrap: if compose-like skip for now; treat as Dockerfile content if no Dockerfile
		_ = os.Rename(dest, filepath.Join(tmp, "Dockerfile"))
	}
	if name == "" {
		name = "upload-" + time.Now().Format("150405")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	flusher, _ := w.(http.Flusher)
	log := &flushWriter{w: w, f: flusher}
	p, err := s.Projects.DeployBuild(projects.DeployBuildInput{
		RoomID: roomID, Name: name, HostPort: hostPort, ContainerPort: cPort, EnvText: envText, SourceDir: tmp, Log: log,
	})
	if err != nil {
		fmt.Fprintf(log, "خطأ: %v\n", err)
		return
	}
	fmt.Fprintf(log, "OK %s\n", p.ID)
}

func (s *Server) handleProjectByID(w http.ResponseWriter, r *http.Request) {
	sess := s.requireRoom(w, r)
	if sess == nil {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/projects/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeErr(w, 404, "not found")
		return
	}
	id := parts[0]
	p, err := s.Projects.Get(id)
	if err != nil || p == nil || p.RoomID != sess.RoomID {
		writeErr(w, 404, "المشروع غير موجود")
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, 200, p)
		case http.MethodDelete:
			if err := s.Projects.Delete(id); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			writeJSON(w, 200, map[string]string{"ok": "1"})
		default:
			writeErr(w, 405, "method")
		}
		return
	}
	action := parts[1]
	switch action {
	case "start":
		if r.Method != http.MethodPost {
			writeErr(w, 405, "method")
			return
		}
		if err := s.Projects.Start(id); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"ok": "1"})
	case "stop":
		if r.Method != http.MethodPost {
			writeErr(w, 405, "method")
			return
		}
		if err := s.Projects.Stop(id); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"ok": "1"})
	case "port":
		if r.Method != http.MethodPost {
			writeErr(w, 405, "method")
			return
		}
		var body struct {
			HostPort int  `json:"host_port"`
			Clear    bool `json:"clear"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		if body.Clear || body.HostPort == 0 {
			if err := s.Projects.ClearPort(id); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			p.HostPort = 0
			_ = s.syncProxy()
			writeJSON(w, 200, map[string]any{"ok": "1", "host_port": 0, "links": s.projectLinks(r, p)})
			return
		}
		if err := s.Projects.SetPort(id, body.HostPort); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		p2, _ := s.Projects.Get(id)
		if p2 != nil {
			p = p2
		}
		_ = s.syncProxy()
		writeJSON(w, 200, map[string]any{"ok": "1", "host_port": p.HostPort, "links": s.projectLinks(r, p)})
	case "domain":
		if r.Method != http.MethodPost {
			writeErr(w, 405, "method")
			return
		}
		var body struct {
			Domain  string `json:"domain"`
			Enabled *bool  `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		en := true
		if body.Enabled != nil {
			en = *body.Enabled
		}
		if strings.TrimSpace(body.Domain) == "" {
			en = false
		}
		if err := s.applyDomain(p, body.Domain, en); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": "1", "domain": p.Domain, "ssl_status": p.SSLStatus, "links": s.projectLinks(r, p)})
	case "external-url":
		if r.Method != http.MethodPost {
			writeErr(w, 405, "method")
			return
		}
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		if err := s.Projects.SetExternalURL(id, body.URL); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		p.ExternalURL = strings.TrimSpace(body.URL)
		writeJSON(w, 200, map[string]any{"ok": "1", "links": s.projectLinks(r, p)})
	case "env":
		if r.Method == http.MethodGet {
			text, err := s.Projects.ReadEnv(id)
			if err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			writeJSON(w, 200, map[string]any{
				"editable": true,
				"content":  text,
				"path":     filepath.Join(s.Cfg.RuntimeDir, p.RoomID, "projects", p.ID, ".env"),
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
			if err := s.Projects.WriteEnv(id, body.Content); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			writeJSON(w, 200, map[string]string{"ok": "1"})
			return
		}
		writeErr(w, 405, "method")
	default:
		writeErr(w, 404, "not found")
	}
}

func (s *Server) handleMetricsSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.requireSession(w, r) == nil {
		return
	}
	writeJSON(w, 200, s.Metrics.Snapshot())
}

func (s *Server) handleHostInfo(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSession(w, r)
	if sess == nil {
		return
	}
	hostname, _ := os.Hostname()
	m := s.Metrics.Snapshot()
	facts := metrics.CollectFacts()
	if facts.Hostname == "" {
		facts.Hostname = hostname
	}
	if facts.PublicIP == "" {
		facts.PublicIP = s.publicHost(r)
	}
	out := map[string]any{
		"hostname":     facts.Hostname,
		"docker":       s.Docker != nil && s.Docker.Available(),
		"cpu_percent":  m.CPUPercent,
		"cpu_cores":    m.CPUCores,
		"mem_total":    m.MemTotal,
		"mem_used":     m.MemUsed,
		"mem_percent":  m.MemPercent,
		"disk_total":   m.DiskTotal,
		"disk_used":    m.DiskUsed,
		"disk_percent": m.DiskPercent,
		"disk_free":    m.DiskFree,
		"net_rx":       m.NetRx,
		"net_tx":       m.NetTx,
		"load1":        m.Load1,
		"timestamp":    m.Timestamp,
		"os":           facts.OS,
		"kernel":       facts.Kernel,
		"arch":         facts.Arch,
		"cpu_model":    facts.CPUModel,
		"ssh_port":     facts.SSHPort,
		"public_ip":    facts.PublicIP,
		"primary_ip":   facts.PrimaryIP,
		"virt":         facts.Virt,
		"uptime_sec":   facts.UptimeSec,
		"uptime":       metrics.FormatUptime(facts.UptimeSec),
	}
	if len(m.GPU) > 0 {
		out["gpu"] = m.GPU
	}
	if sess.Kind == auth.KindOwner {
		roomsList, _ := s.Store.ListRooms()
		projs, _ := s.Store.ListAllProjects()
		out["rooms_count"] = len(roomsList)
		out["projects_count"] = len(projs)
	}
	writeJSON(w, 200, out)
}

type flushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}

func parseDockerPull(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	cmd = strings.TrimPrefix(cmd, "sudo ")
	fields := strings.Fields(cmd)
	if len(fields) >= 3 && fields[0] == "docker" && fields[1] == "pull" {
		return fields[2]
	}
	if len(fields) == 1 && strings.Contains(fields[0], "/") || (len(fields) == 1 && strings.Contains(fields[0], ":")) {
		return fields[0]
	}
	if len(fields) == 1 {
		return fields[0]
	}
	return ""
}

func sanitizeName(image string) string {
	image = strings.ReplaceAll(image, "/", "-")
	image = strings.ReplaceAll(image, ":", "-")
	if len(image) > 30 {
		image = image[:30]
	}
	if image == "" {
		return "project"
	}
	return image
}

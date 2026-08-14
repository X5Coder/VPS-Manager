package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/x5coder/vps-rooms/internal/projects"
	"github.com/x5coder/vps-rooms/internal/rooms"
	"github.com/x5coder/vps-rooms/internal/store"
)

func (s *Server) routesAPITokens() {
	s.Mux.HandleFunc("/api/settings/tokens", s.withGate(s.handleAPITokens))
	s.Mux.HandleFunc("/api/settings/tokens/", s.withGate(s.handleAPITokenByID))
	s.Mux.HandleFunc("/api/storage", s.withGate(s.handleStorage))
	s.Mux.HandleFunc("/api/ports", s.withGate(s.handlePorts))
	s.Mux.HandleFunc("/api/v1/", s.handleAPIV1)
}

func (s *Server) storageInfo() map[string]any {
	m := s.Metrics.Snapshot()
	free := int64(m.DiskFree)
	if free <= 0 && m.DiskTotal > m.DiskUsed {
		free = int64(m.DiskTotal - m.DiskUsed)
	}
	reserved, _ := s.Store.TotalQuotaBytes()
	return map[string]any{
		"disk_total":         int64(m.DiskTotal),
		"disk_used":          int64(m.DiskUsed),
		"disk_free":          free,
		"quota_reserved":     reserved,
		"quota_available":    free,
		"quota_available_gb": float64(free) / (1024 * 1024 * 1024),
	}
}

func (s *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	writeJSON(w, 200, s.storageInfo())
}

func (s *Server) handlePorts(w http.ResponseWriter, r *http.Request) {
	if s.requireSession(w, r) == nil {
		return
	}
	writeJSON(w, 200, s.portsPayload())
}

func (s *Server) portsPayload() map[string]any {
	ports := []int{}
	if s.Docker != nil && s.Docker.Available() {
		if p, err := s.Docker.UsedPorts(); err == nil {
			ports = p
		}
	}
	seen := map[int]bool{}
	out := make([]int, 0, len(ports)+1)
	for _, p := range ports {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if !seen[9090] {
		out = append(out, 9090)
	}
	return map[string]any{"used_ports": out, "panel_port": 9090}
}

func (s *Server) handleAPITokens(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := s.Store.ListAPITokens()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if list == nil {
			list = []store.APIToken{}
		}
		writeJSON(w, 200, list)
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		tok, plain, err := s.Store.CreateAPIToken(body.Name, body.Mode)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		host := r.Host
		if host == "" {
			host = "YOUR_VPS_IP:9090"
		}
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base := scheme + "://" + host
		writeJSON(w, 200, map[string]any{
			"token":  tok,
			"secret": plain,
			"prompt": s.buildAPIPrompt(base, plain, tok.Mode),
		})
	default:
		writeErr(w, 405, "method")
	}
}

func (s *Server) handleAPITokenByID(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/settings/tokens/")
	id = strings.Trim(id, "/")
	if id == "" || strings.Contains(id, "/") {
		writeErr(w, 404, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		tok, err := s.Store.GetAPIToken(id)
		if err != nil || tok == nil {
			writeErr(w, 404, "not found")
			return
		}
		host := r.Host
		if host == "" {
			host = "YOUR_VPS_IP:9090"
		}
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base := scheme + "://" + host
		prompt := ""
		if tok.TokenPlain != "" {
			prompt = s.buildAPIPrompt(base, tok.TokenPlain, tok.Mode)
		}
		writeJSON(w, 200, map[string]any{
			"token":  tok,
			"secret": tok.TokenPlain,
			"prompt": prompt,
		})
	case http.MethodDelete:
		if err := s.Store.DeleteAPIToken(id); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"ok": "1"})
	default:
		writeErr(w, 405, "method")
	}
}

func (s *Server) buildAPIPrompt(base, secret, mode string) string {
	return fmt.Sprintf(`You are controlling VPS Manager via its HTTP API.
Base URL: %s
Auth header: Authorization: Bearer %s
Permission mode: %s — read = GET only; write = GET + create/update/exec; both = read and write on this one token. Delete is NEVER allowed.

Important: each room = one project. Use room id as the project id in most calls.

Endpoints:
- GET  /api/v1/projects                 — list all projects/rooms
- GET  /api/v1/projects/{id}            — get one project (room id or project id)
- POST /api/v1/projects                 — create/deploy a project (JSON, write mode)
  {
    "name": "my-app",
    "image": "nginx:alpine",
    "quota_gb": 2,
    "host_port": 0,
    "container_port": 80,
    "env": "KEY=value\nOTHER=1"
  }
  quota_gb is REQUIRED and must fit in available disk (check /api/v1/storage first).
- PATCH /api/v1/projects/{id}           — update project (write mode)
  Fields (any subset): name, domain, env, quota_gb, action ("pause"|"resume")
- POST /api/v1/projects/{id}/exec       — run command (write mode)
  Body: {"command":"ls -la"}
- GET  /api/v1/storage                  — disk free / reserved / available
- GET  /api/v1/ports                    — used host ports

Rules:
1. Always GET /api/v1/storage before create; pick quota_gb <= quota_available_gb.
2. Never attempt delete.
3. Content-Type: application/json
4. Errors look like {"error":"..."}.
`, base, secret, mode)
}

func (s *Server) apiTokenFromRequest(r *http.Request) (*store.APIToken, error) {
	h := r.Header.Get("Authorization")
	plain := ""
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		plain = strings.TrimSpace(h[7:])
	}
	if plain == "" {
		plain = strings.TrimSpace(r.Header.Get("X-API-Token"))
	}
	if plain == "" {
		return nil, nil
	}
	return s.Store.LookupAPIToken(plain)
}

func (s *Server) requireAPIToken(w http.ResponseWriter, r *http.Request, writeNeeded bool) *store.APIToken {
	tok, err := s.apiTokenFromRequest(r)
	if err != nil || tok == nil {
		writeErr(w, 401, "invalid api token")
		return nil
	}
	if writeNeeded && !store.TokenCanWrite(tok.Mode) {
		writeErr(w, 403, "token is read-only")
		return nil
	}
	return tok
}

func (s *Server) handleAPIV1(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeErr(w, 404, "not found")
		return
	}

	switch parts[0] {
	case "storage":
		if s.requireAPIToken(w, r, false) == nil {
			return
		}
		writeJSON(w, 200, s.storageInfo())
	case "ports":
		if s.requireAPIToken(w, r, false) == nil {
			return
		}
		writeJSON(w, 200, s.portsPayload())
	case "projects":
		s.handleAPIV1Projects(w, r, parts[1:])
	default:
		writeErr(w, 404, "not found")
	}
}

func (s *Server) handleAPIV1Projects(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			if s.requireAPIToken(w, r, false) == nil {
				return
			}
			s.apiListProjects(w)
		case http.MethodPost:
			if s.requireAPIToken(w, r, true) == nil {
				return
			}
			s.apiCreateProject(w, r)
		default:
			writeErr(w, 405, "method")
		}
		return
	}

	id := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			if s.requireAPIToken(w, r, false) == nil {
				return
			}
			s.apiGetProject(w, id)
		case http.MethodPatch:
			if s.requireAPIToken(w, r, true) == nil {
				return
			}
			s.apiPatchProject(w, r, id)
		case http.MethodDelete:
			writeErr(w, 403, "delete is not available via API")
		default:
			writeErr(w, 405, "method")
		}
		return
	}

	if parts[1] == "exec" && r.Method == http.MethodPost {
		if s.requireAPIToken(w, r, true) == nil {
			return
		}
		s.apiExecProject(w, r, id)
		return
	}
	writeErr(w, 404, "not found")
}

func (s *Server) resolveRoomProject(id string) (*store.Room, *store.Project, error) {
	if room, err := s.Store.GetRoom(id); err == nil && room != nil {
		projs, _ := s.Store.ListProjects(room.ID)
		var p *store.Project
		if len(projs) > 0 {
			pp := projs[0]
			p = &pp
		}
		return room, p, nil
	}
	p, err := s.Store.GetProject(id)
	if err != nil || p == nil {
		return nil, nil, fmt.Errorf("not found")
	}
	room, err := s.Store.GetRoom(p.RoomID)
	if err != nil || room == nil {
		return nil, nil, fmt.Errorf("not found")
	}
	return room, p, nil
}

func (s *Server) projectView(room *store.Room, p *store.Project) map[string]any {
	usage, _ := s.Rooms.UsageBytes(room.ID)
	st := "empty"
	out := map[string]any{
		"id": room.ID, "room_id": room.ID, "name": room.Name,
		"quota_bytes": room.QuotaBytes, "usage_bytes": usage,
		"password": room.PassPlain, "created_at": room.CreatedAt,
		"status": st,
	}
	if p != nil {
		if s.Docker != nil && p.ContainerID != "" {
			if st2, e := s.Docker.InspectStatus(p.ContainerID); e == nil {
				st = st2
			}
		} else {
			st = p.Status
		}
		out["project_id"] = p.ID
		out["project_name"] = p.Name
		out["image"] = p.Image
		out["host_port"] = p.HostPort
		out["container_port"] = p.ContainerPort
		out["domain"] = p.Domain
		out["status"] = st
		out["container_id"] = p.ContainerID
	}
	return out
}

func (s *Server) apiListProjects(w http.ResponseWriter) {
	roomsList, err := s.Store.ListRooms()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	list := make([]map[string]any, 0, len(roomsList))
	for i := range roomsList {
		room := roomsList[i]
		projs, _ := s.Store.ListProjects(room.ID)
		var p *store.Project
		if len(projs) > 0 {
			pp := projs[0]
			p = &pp
		}
		list = append(list, s.projectView(&room, p))
	}
	writeJSON(w, 200, map[string]any{"projects": list})
}

func (s *Server) apiGetProject(w http.ResponseWriter, id string) {
	room, p, err := s.resolveRoomProject(id)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, s.projectView(room, p))
}

func (s *Server) allocateQuota(quotaGB float64, extraFree int64) (int64, error) {
	if quotaGB <= 0 {
		return 0, fmt.Errorf("quota_gb is required and must be > 0")
	}
	quota := int64(quotaGB * 1024 * 1024 * 1024)
	st := s.storageInfo()
	avail := asInt64(st["quota_available"]) + extraFree
	if quota > avail {
		return 0, fmt.Errorf("quota exceeds available space (%.2f GB free to allocate)", float64(avail)/(1024*1024*1024))
	}
	return quota, nil
}

func asInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case uint64:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func (s *Server) uniqueRoomName(base string) string {
	name := sanitizeRoomName(base)
	if existing, _ := s.Store.GetRoomByName(name); existing == nil {
		return name
	}
	for i := 0; i < 20; i++ {
		cand := name
		if len(cand) > 34 {
			cand = cand[:34]
		}
		cand = cand + "-" + time.Now().Format("150405")
		if existing, _ := s.Store.GetRoomByName(cand); existing == nil {
			return cand
		}
		time.Sleep(10 * time.Millisecond)
	}
	return name + "-" + randomPass(4)
}

func sanitizeRoomName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else if r == ' ' || r == '/' || r == ':' {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if len(out) < 2 {
		out = "room-" + time.Now().Format("150405")
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func (s *Server) apiCreateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name          string   `json:"name"`
		Image         string   `json:"image"`
		Command       string   `json:"command"`
		QuotaGB       float64  `json:"quota_gb"`
		HostIP        string   `json:"host_ip"`
		HostPort      int      `json:"host_port"`
		ContainerPort int      `json:"container_port"`
		Env           string   `json:"env"`
		Domain        string   `json:"domain"`
		Binds         []string `json:"binds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	quota, err := s.allocateQuota(body.QuotaGB, 0)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	image := body.Image
	if image == "" {
		image = parseDockerPull(body.Command)
	}
	if image == "" {
		writeErr(w, 400, "image or docker pull command required")
		return
	}
	projName := body.Name
	if projName == "" {
		projName = sanitizeName(image)
	}
	roomName := s.uniqueRoomName(projName)
	pass := randomPass(10)
	rm, err := s.Rooms.Create(rooms.CreateInput{Name: roomName, Password: pass, QuotaBytes: quota})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	cPort := body.ContainerPort
	if cPort == 0 {
		cPort = 80
	}
	p, err := s.Projects.DeployImage(projects.DeployImageInput{
		RoomID: rm.ID, Name: sanitizeName(projName), Image: image,
		HostIP: body.HostIP, HostPort: body.HostPort, ContainerPort: cPort,
		EnvText: body.Env, ExtraBinds: body.Binds, Log: io.Discard,
	})
	if err != nil {
		_ = s.Rooms.Delete(rm.ID)
		writeErr(w, 400, err.Error())
		return
	}
	if body.Domain != "" {
		_ = s.Projects.SetDomain(p.ID, body.Domain)
	}
	_ = appendLog(s.Cfg.DataDir, "api", "CREATE project="+p.ID+" room="+rm.ID+" name="+roomName+" image="+image+" port="+strconv.Itoa(p.HostPort))
	_ = appendLog(s.Cfg.DataDir, "deploy", "API OK project="+p.ID+" name="+roomName+" image="+image)
	writeJSON(w, 200, map[string]any{
		"ok":       true,
		"project":  s.projectView(rm, p),
		"password": pass,
	})
}

func (s *Server) apiPatchProject(w http.ResponseWriter, r *http.Request, id string) {
	room, p, err := s.resolveRoomProject(id)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	var body struct {
		Name    *string  `json:"name"`
		Domain  *string  `json:"domain"`
		Env     *string  `json:"env"`
		QuotaGB *float64 `json:"quota_gb"`
		Action  string   `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if body.QuotaGB != nil {
		q, err := s.allocateQuota(*body.QuotaGB, room.QuotaBytes)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if err := s.Rooms.SetQuota(room.ID, q); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	if body.Name != nil && strings.TrimSpace(*body.Name) != "" && p != nil {
		p.Name = sanitizeName(*body.Name)
		_ = s.Store.UpdateProject(*p)
	}
	if body.Domain != nil && p != nil {
		_ = s.Projects.SetDomain(p.ID, *body.Domain)
	}
	if body.Env != nil && p != nil {
		if err := s.Projects.WriteEnv(p.ID, *body.Env); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	switch strings.ToLower(body.Action) {
	case "pause":
		projs, _ := s.Store.ListProjects(room.ID)
		for _, pp := range projs {
			_ = s.Projects.Stop(pp.ID)
		}
	case "resume":
		projs, _ := s.Store.ListProjects(room.ID)
		for _, pp := range projs {
			_ = s.Projects.Start(pp.ID)
		}
	}
	room2, p2, _ := s.resolveRoomProject(room.ID)
	writeJSON(w, 200, s.projectView(room2, p2))
}

func (s *Server) apiExecProject(w http.ResponseWriter, r *http.Request, id string) {
	room, p, err := s.resolveRoomProject(id)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	var body struct {
		Command string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Command) == "" {
		writeErr(w, 400, "command required")
		return
	}
	cmdLine := body.Command
	var cmd *exec.Cmd
	if p != nil && s.Docker != nil && p.ContainerID != "" {
		cmd = exec.Command("docker", "exec", p.ContainerID, "sh", "-lc", cmdLine)
	}
	if cmd == nil {
		dir := filepath.Join(s.Cfg.RuntimeDir, room.ID)
		cmd = exec.Command("sh", "-lc", cmdLine)
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	res := map[string]any{"output": string(out), "exit_code": 0}
	if err != nil {
		res["error"] = err.Error()
		res["exit_code"] = 1
	}
	writeJSON(w, 200, res)
}

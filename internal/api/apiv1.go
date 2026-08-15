package api

import (
	"context"
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

	"github.com/x5coder/vps-rooms/internal/projects"
	"github.com/x5coder/vps-rooms/internal/rooms"
	"github.com/x5coder/vps-rooms/internal/stack"
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
	ports := s.cachedPorts()
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

func (s *Server) tokenPublic(base string, t store.APIToken) map[string]any {
	prompt, sheet, script, scriptMulti := s.tokenCopyFields(base, t.TokenPlain)
	return map[string]any{
		"id":            t.ID,
		"name":          t.Name,
		"token_prefix":  t.TokenPrefix,
		"mode":          "owner",
		"room_id":       "",
		"room_name":     "all rooms",
		"created_at":    t.CreatedAt,
		"last_used_at":  t.LastUsedAt,
		"secret":        t.TokenPlain,
		"prompt":        prompt,
		"api":           sheet,
		"script":        script,
		"script_single": script,
		"script_multi":  scriptMulti,
	}
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
		base := requestBaseURL(r)
		out := make([]map[string]any, 0, len(list))
		for i := range list {
			out = append(out, s.tokenPublic(base, list[i]))
		}
		writeJSON(w, 200, out)
	case http.MethodPost:
		var body struct {
			Name   string `json:"name"`
			RoomID string `json:"room_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		tok, plain, err := s.Store.CreateAPIToken(body.Name, "")
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		base := requestBaseURL(r)
		pub := s.tokenPublic(base, *tok)
		writeJSON(w, 200, map[string]any{
			"token":         tok,
			"secret":        plain,
			"prompt":        pub["prompt"],
			"api":           pub["api"],
			"script":        pub["script"],
			"script_single": pub["script_single"],
			"script_multi":  pub["script_multi"],
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
		base := requestBaseURL(r)
		writeJSON(w, 200, s.tokenPublic(base, *tok))
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
		plain = strings.TrimSpace(r.URL.Query().Get("access_token"))
	}
	if plain == "" {
		plain = strings.TrimSpace(r.URL.Query().Get("token"))
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
	_ = writeNeeded
	return tok
}

func (s *Server) requireTokenRoom(w http.ResponseWriter, r *http.Request, id string, writeNeeded bool) (*store.APIToken, *store.Room, *store.Project) {
	tok := s.requireAPIToken(w, r, writeNeeded)
	if tok == nil {
		return nil, nil, nil
	}
	room, p, err := s.resolveRoomProject(id)
	if err != nil || room == nil {
		writeErr(w, 404, "not found")
		return nil, nil, nil
	}
	return tok, room, p
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
	case "quota":
		if s.requireAPIToken(w, r, false) == nil {
			return
		}
		info := s.storageInfo()
		info["hint"] = "Call this before POST /api/v1/projects. quota_gb must be > 0 and <= quota_available_gb."
		writeJSON(w, 200, info)
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
	case "rooms":
		s.handleAPIV1Projects(w, r, parts[1:])
	case "status":
		if s.requireAPIToken(w, r, false) == nil {
			return
		}
		s.apiManagerStatus(w)
	case "logs":
		if s.requireAPIToken(w, r, false) == nil {
			return
		}
		s.apiVPSLogs(w, r)
	case "agent":
		s.handleAPIV1Agent(w, r, parts[1:])
	case "images":
		s.handleAPIV1Images(w, r, parts[1:])
	default:
		writeErr(w, 404, "not found")
	}
}

func (s *Server) handleAPIV1Projects(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			tok := s.requireAPIToken(w, r, false)
			if tok == nil {
				return
			}
			s.apiListProjects(w)
		case http.MethodPost:
			tok := s.requireAPIToken(w, r, true)
			if tok == nil {
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
			if tok, _, _ := s.requireTokenRoom(w, r, id, false); tok == nil {
				return
			}
			s.apiGetProject(w, id)
		case http.MethodPatch:
			if tok, _, _ := s.requireTokenRoom(w, r, id, true); tok == nil {
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

	if q := strings.TrimSpace(r.URL.Query().Get("access_token")); q != "" && r.Header.Get("Authorization") == "" {
		r.Header.Set("Authorization", "Bearer "+q)
	}
	if tok, _, _ := s.requireTokenRoom(w, r, id, r.Method != http.MethodGet); tok == nil {
		return
	}
	switch parts[1] {
	case "logs":
		s.handleV1Logs(w, r, id)
		return
	case "env":
		s.handleV1Env(w, r, id)
		return
	case "volumes":
		s.handleV1Volumes(w, r, id, parts[2:])
		return
	case "images":
		s.handleV1Images(w, r, id, parts[2:])
		return
	case "compose":
		s.handleV1Compose(w, r, id, parts[2:])
		return
	case "stack":
		s.handleV1Stack(w, r, id, parts[2:])
		return
	case "containers":
		s.handleAPIV1ProjectContainers(w, r, id, parts[2:])
		return
	case "exec", "terminal":
		if len(parts) >= 3 && parts[2] == "ws" {
			s.handleV1TerminalWS(w, r, id)
			return
		}
		if r.Method == http.MethodPost {
			s.apiExecWait(w, r, id)
			return
		}
		writeErr(w, 405, "method")
		return
	}
	if parts[1] == "upload" && r.Method == http.MethodPost {
		s.apiUploadProject(w, r, id)
		return
	}
	if parts[1] == "redeploy" && r.Method == http.MethodPost {
		s.apiRedeployProject(w, r, id)
		return
	}
	if parts[1] == "build" && r.Method == http.MethodPost {
		s.apiBuildProject(w, r, id)
		return
	}
	if parts[1] == "deploys" && r.Method == http.MethodGet {
		s.apiProjectDeploys(w, id)
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
	st := "empty"
	quotaGB := float64(room.QuotaBytes) / (1024 * 1024 * 1024)
	usage := s.cachedUsage(room.ID)
	usageGB := float64(usage) / (1024 * 1024 * 1024)
	out := map[string]any{
		"id": room.ID, "room_id": room.ID, "name": room.Name,
		"quota_bytes": room.QuotaBytes, "quota_gb": quotaGB,
		"usage_bytes": usage, "usage_gb": usageGB,
		"password_set": room.PassPlain != "",
		"created_at":   room.CreatedAt,
		"status":       st,
		"kind":         room.Kind,
		"domain":       room.Domain,
		"ssl":          room.SSL,
	}
	cts := s.roomContainersJSON(room.ID)
	if room.Kind == "" {
		out["kind"] = store.KindSingle
	}
	if len(cts) > 1 {
		out["kind"] = store.KindMulti
	}
	out["deployment_type"] = out["kind"]
	out["containers"] = cts
	out["images"] = s.roomImagesJSON(room.ID)
	out["volumes"] = s.roomVolumesJSON(room.ID)
	out["container_count"] = len(cts)
	out["image_count"] = len(s.roomImagesJSON(room.ID))
	out["volume_count"] = len(s.roomVolumesJSON(room.ID))
	hist := s.Projects.ReadUpdateHistory(room.ID)
	if hist == nil {
		hist = []projects.UpdateEvent{}
	}
	count := 0
	if len(hist) > 0 {
		count = hist[0].N
	}
	out["updates"] = hist
	out["update_count"] = count
	if busy := s.jobKind(room.ID); busy != "" {
		out["status"] = "deploying"
		out["job"] = busy
	}
	if p != nil {
		busy := s.jobKind(p.ID)
		st = s.cachedStatus(p.ID)
		if st == "" {
			st = p.Status
		}
		if busy == "build" {
			st = "building"
		} else if busy != "" {
			st = "deploying"
		}
		out["project_id"] = p.ID
		out["project_name"] = p.Name
		out["image"] = p.Image
		out["host_port"] = p.HostPort
		out["container_port"] = p.ContainerPort
		out["domain"] = p.Domain
		out["domain_enabled"] = p.DomainEnabled
		out["ssl_status"] = p.SSLStatus
		out["external_url"] = p.ExternalURL
		out["status"] = st
		out["container_id"] = p.ContainerID
		meta := s.Projects.ReadDeployMeta(room.ID, p.ID)
		out["image_digest"] = meta.ImageDigest
		out["last_deploy_at"] = meta.LastDeployAt
		out["last_deploy_ok"] = meta.LastDeployOK
		out["last_deploy_error"] = meta.LastDeployError
		if busy == "build" {
			out["status"] = "building"
			out["job"] = "build"
		} else if busy == "deploy" {
			out["status"] = "deploying"
			out["job"] = "deploy"
		} else if meta.Status == "error" && st != "running" {
			out["status"] = "error"
		}
		if env, err := s.Projects.ReadEnv(p.ID); err == nil {
			out["env"] = maskEnvText(env)
		}
	}
	return out
}

func maskEnvText(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			out = append(out, line)
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			out = append(out, line)
			continue
		}
		k = strings.TrimSpace(k)
		if strings.TrimSpace(v) == "" {
			out = append(out, k+"=set")
		} else {
			out = append(out, k+"=***")
		}
	}
	return strings.Join(out, "\n")
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
	writeJSON(w, 200, map[string]any{"projects": list, "storage": s.storageInfo()})
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

func (s *Server) roomIsEmpty(id string) bool {
	cts, _ := s.Store.ListContainers(id)
	return len(cts) == 0
}

func uploadErrBody(err error) map[string]any {
	msg := err.Error()
	code := "package_invalid"
	if i := strings.Index(msg, ":"); i > 0 && i < 48 {
		code = msg[:i]
		msg = strings.TrimSpace(msg[i+1:])
	}
	return map[string]any{"ok": false, "error": msg, "code": code}
}

func (s *Server) writeQuotaError(w http.ResponseWriter, err error) {
	st := s.storageInfo()
	code := "quota_invalid"
	msg := err.Error()
	if strings.Contains(msg, "exceeds") {
		code = "quota_exceeds_available"
	} else if strings.Contains(msg, "required") {
		code = "quota_required"
	}
	writeJSON(w, 400, map[string]any{
		"ok": false, "error": msg, "code": code,
		"quota_available_gb": st["quota_available_gb"],
		"quota_available":    st["quota_available"],
	})
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
		Name             string   `json:"name"`
		Image            string   `json:"image"`
		Command          string   `json:"command"`
		QuotaGB          float64  `json:"quota_gb"`
		Password         string   `json:"password"`
		GeneratePassword *bool    `json:"generate_password"`
		Kind             string   `json:"kind"`
		HostIP           string   `json:"host_ip"`
		HostPort         int      `json:"host_port"`
		ContainerPort    int      `json:"container_port"`
		Env              string   `json:"env"`
		Domain           string   `json:"domain"`
		SSL              bool     `json:"ssl"`
		SSHCertificate   string   `json:"ssh_certificate"`
		Binds            []string `json:"binds"`
		Empty            *bool    `json:"empty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "invalid request", "code": "invalid_request"})
		return
	}
	image := body.Image
	if image == "" {
		image = parseDockerPull(body.Command)
	}
	wantEmpty := (body.Empty != nil && *body.Empty) || image == ""
	if wantEmpty {
		if err := emptyRoomErr(body.QuotaGB); err != nil {
			s.writeQuotaError(w, err)
			return
		}
		cPort := body.ContainerPort
		if cPort == 0 {
			cPort = 8080
		}
		name := strings.TrimSpace(body.Name)
		if name == "" {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "name is required", "code": "name_required"})
			return
		}
		gen := body.GeneratePassword != nil && *body.GeneratePassword
		pass := strings.TrimSpace(body.Password)
		if pass == "" {
			if body.GeneratePassword != nil && !*body.GeneratePassword {
				writeJSON(w, 400, map[string]any{"ok": false, "error": "password is required or set generate_password true", "code": "password_required"})
				return
			}
			gen = true
		}
		if gen {
			pass = ""
		} else if len(pass) < 6 {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "password must be at least 6 characters", "code": "password_invalid"})
			return
		}
		rm, passOut, err := s.createEmptyRoom(name, body.QuotaGB, cPort, body.HostPort, pass, body.Kind, body.Domain, body.SSL, body.SSHCertificate)
		if err != nil {
			if strings.Contains(err.Error(), "quota") {
				s.writeQuotaError(w, err)
				return
			}
			code := "create_failed"
			if strings.Contains(err.Error(), "password") {
				code = "password_invalid"
			}
			writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "code": code})
			return
		}
		writeJSON(w, 200, map[string]any{
			"ok":       true,
			"empty":    true,
			"project":  s.projectView(rm, nil),
			"password": passOut,
			"status":   "empty",
		})
		return
	}
	quota, err := s.allocateQuota(body.QuotaGB, 0)
	if err != nil {
		s.writeQuotaError(w, err)
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
		Name     *string  `json:"name"`
		Password *string  `json:"password"`
		Domain   *string  `json:"domain"`
		Env      *string  `json:"env"`
		QuotaGB  *float64 `json:"quota_gb"`
		Image    *string  `json:"image"`
		Pull     *bool    `json:"pull"`
		Recreate *bool    `json:"recreate"`
		Action   string   `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if body.QuotaGB != nil {
		q, err := s.allocateQuota(*body.QuotaGB, room.QuotaBytes)
		if err != nil {
			s.writeQuotaError(w, err)
			return
		}
		if err := s.Projects.ApplyQuota(room.ID, q); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
		_ = s.Rooms.SetName(room.ID, strings.TrimSpace(*body.Name))
		if p != nil {
			p.Name = sanitizeName(*body.Name)
			_ = s.Store.UpdateProject(*p)
		}
	}
	if body.Password != nil && strings.TrimSpace(*body.Password) != "" {
		if err := s.Rooms.SetPassword(room.ID, strings.TrimSpace(*body.Password)); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
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
	if body.Image != nil && strings.TrimSpace(*body.Image) != "" && p != nil {
		pull := true
		if body.Pull != nil {
			pull = *body.Pull
		}
		recreate := true
		if body.Recreate != nil {
			recreate = *body.Recreate
		}
		if err := s.startRedeployAsync(p, strings.TrimSpace(*body.Image), pull, recreate); err != nil {
			writeJSON(w, 409, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		s.acceptedProject(w, room.ID, map[string]any{
			"status": "deploying", "image": strings.TrimSpace(*body.Image),
			"pull": pull, "recreate": recreate,
		})
		return
	}
	room2, p2, _ := s.resolveRoomProject(room.ID)
	writeJSON(w, 200, map[string]any{"ok": true, "project": s.projectView(room2, p2)})
}

func (s *Server) apiExecProject(w http.ResponseWriter, r *http.Request, id string) {
	room, p, err := s.resolveRoomProject(id)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	var body struct {
		Command     string `json:"command"`
		TimeoutSec  int    `json:"timeout_sec"`
		ContainerID string `json:"container_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Command) == "" {
		writeErr(w, 400, "command required")
		return
	}
	timeout := 60 * time.Second
	if body.TimeoutSec > 0 {
		timeout = time.Duration(body.TimeoutSec) * time.Second
	}
	if timeout > 2*time.Minute {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	cmdLine := body.Command
	var cmd *exec.Cmd
	dockerID := ""
	if ct := s.resolveRoomContainer(room.ID, body.ContainerID); ct != nil {
		dockerID = ct.DockerID
	}
	if dockerID == "" && p != nil {
		dockerID = p.ContainerID
	}
	if dockerID != "" && s.Docker != nil {
		cmd = exec.CommandContext(ctx, "docker", "exec", dockerID, "sh", "-lc", cmdLine)
	}
	if cmd == nil {
		dir := filepath.Join(s.Cfg.RuntimeDir, room.ID)
		cmd = exec.CommandContext(ctx, "sh", "-lc", cmdLine)
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	text := string(out)
	if len(text) > 200*1024 {
		text = text[:200*1024] + "\n…truncated"
	}
	res := map[string]any{"output": text, "exit_code": 0}
	if err != nil {
		res["error"] = err.Error()
		res["exit_code"] = 1
	}
	writeJSON(w, 200, res)
}

func (s *Server) apiDoRedeploy(p *store.Project, image string, pull, recreate bool) error {
	if p == nil {
		return fmt.Errorf("project has no container")
	}
	room, err := s.Store.GetRoom(p.RoomID)
	if err != nil || room == nil {
		return fmt.Errorf("room not found")
	}
	if room.QuotaBytes > 0 {
		gb := float64(room.QuotaBytes) / (1024 * 1024 * 1024)
		if _, err := s.allocateQuota(gb, room.QuotaBytes); err != nil {
			return err
		}
	}
	if pull && s.Docker != nil {
		ref := strings.TrimSpace(image)
		if strings.Contains(ref, "ghcr.io") {
			if gt := s.githubToken(); gt != "" {
				_ = s.Docker.Login("ghcr.io", "x-access-token", gt)
			}
		}
	}
	return s.Projects.RedeployImage(projects.RedeployInput{
		ID: p.ID, Image: image, Pull: pull, Recreate: recreate, Log: io.Discard,
	})
}

func (s *Server) apiUploadProject(w http.ResponseWriter, r *http.Request, id string) {
	room, p, err := s.resolveRoomProject(id)
	if err != nil || room == nil {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "not found"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<30)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "could not read upload: " + err.Error()})
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "multipart field file is required (.tar for single or .tar.gz for multi)", "code": "file_required"})
		return
	}
	defer file.Close()
	fname := ""
	if hdr != nil {
		fname = hdr.Filename
	}
	tmp, err := os.MkdirTemp("", "vm-api-tar-*")
	if err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	safe := filepath.Base(fname)
	if safe == "" || safe == "." {
		safe = "app.tar"
	}
	dest := filepath.Join(tmp, safe)
	out, err := os.Create(dest)
	if err != nil {
		_ = os.RemoveAll(tmp)
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	n, err := io.Copy(out, file)
	out.Close()
	if err != nil {
		_ = os.RemoveAll(tmp)
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if n < 64 {
		_ = os.RemoveAll(tmp)
		writeJSON(w, 400, map[string]any{"ok": false, "error": "empty package", "code": "package_empty"})
		return
	}
	ctrID := strings.TrimSpace(r.FormValue("container_id"))
	if ctrID == "" {
		ctrID = strings.TrimSpace(r.FormValue("container"))
	}
	empty := s.roomIsEmpty(room.ID)
	if err := stack.CheckUpload(fname, dest, room.Kind, ctrID, empty); err != nil {
		_ = os.RemoveAll(tmp)
		writeJSON(w, 400, uploadErrBody(err))
		return
	}
	if ctrID != "" {
		ct := s.resolveRoomContainer(room.ID, ctrID)
		if ct == nil {
			_ = os.RemoveAll(tmp)
			writeJSON(w, 404, map[string]any{"ok": false, "error": "container not found"})
			return
		}
		go func() {
			defer os.RemoveAll(tmp)
			_ = s.applyImageTarOneContainer(room, ct, dest, io.Discard)
		}()
		s.acceptedProject(w, room.ID, map[string]any{
			"status": "deploying", "bytes": n, "container_id": ct.ID,
		})
		return
	}
	if s.Stack != nil && (stack.LooksLikeMultiPackage(fname) || stack.ArchiveHasCompose(dest)) {
		go func() {
			defer os.RemoveAll(tmp)
			_ = s.Stack.DeployMulti(room, dest, io.Discard)
		}()
		s.acceptedProject(w, room.ID, map[string]any{
			"status": "deploying",
			"bytes":  n,
			"kind":   "multi",
		})
		return
	}
	if err := s.startTarDeployAsync(room, p, dest, tmp); err != nil {
		writeJSON(w, 409, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	tag := projects.DefaultVpsroomsTag(room.Name)
	if p != nil {
		tag = s.localProjectTag(p)
	}
	s.acceptedProject(w, room.ID, map[string]any{
		"status": "deploying",
		"bytes":  n,
		"image":  tag,
	})
}

func (s *Server) apiRedeployProject(w http.ResponseWriter, r *http.Request, id string) {
	room, p, err := s.resolveRoomProject(id)
	if err != nil || p == nil {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "not found"})
		return
	}
	var body struct {
		Image    string `json:"image"`
		Pull     *bool  `json:"pull"`
		Recreate *bool  `json:"recreate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	image := strings.TrimSpace(body.Image)
	fromRequest := image != ""
	if image == "" {
		image = p.Image
	}
	if strings.TrimSpace(image) == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "image required (omit to reuse the current image)"})
		return
	}
	pull := true
	if body.Pull != nil {
		pull = *body.Pull
	}
	recreate := true
	if body.Recreate != nil {
		recreate = *body.Recreate
	}
	if err := s.startRedeployAsync(p, image, pull, recreate); err != nil {
		writeJSON(w, 409, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.acceptedProject(w, room.ID, map[string]any{
		"status":             "deploying",
		"image":              image,
		"image_from_request": fromRequest,
		"pull":               pull,
		"recreate":           recreate,
	})
}

func (s *Server) apiProjectDeploys(w http.ResponseWriter, id string) {
	room, _, err := s.resolveRoomProject(id)
	if err != nil || room == nil {
		writeErr(w, 404, "not found")
		return
	}
	list := s.Projects.ReadUpdateHistory(room.ID)
	if list == nil {
		list = []projects.UpdateEvent{}
	}
	count := 0
	if len(list) > 0 {
		count = list[0].N
	}
	writeJSON(w, 200, map[string]any{"updates": list, "count": count})
}

func (s *Server) githubToken() string {
	b, err := os.ReadFile(filepath.Join(s.Cfg.DataDir, "secrets", "github.env"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "GITHUB_TOKEN=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "GITHUB_TOKEN="))
		}
	}
	return ""
}

func (s *Server) handleAPIV1Images(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 1 && parts[0] == "build" && r.Method == http.MethodPost {
		tok := s.requireAPIToken(w, r, true)
		if tok == nil {
			return
		}
		_, p, err := s.resolveRoomProject(tok.RoomID)
		if err != nil {
			writeJSON(w, 404, map[string]any{"ok": false, "error": "not found"})
			return
		}
		s.apiBuildImage(w, r, p)
		return
	}
	writeErr(w, 404, "not found")
}

func (s *Server) apiBuildProject(w http.ResponseWriter, r *http.Request, id string) {
	_, p, err := s.resolveRoomProject(id)
	if err != nil {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "not found"})
		return
	}
	s.apiBuildImage(w, r, p)
}

func (s *Server) apiBuildImage(w http.ResponseWriter, r *http.Request, p *store.Project) {
	var body struct {
		Image      string            `json:"image"`
		Context    string            `json:"context"`
		Dockerfile string            `json:"dockerfile"`
		BuildArgs  map[string]string `json:"build_args"`
		Push       bool              `json:"push"`
		Deploy     bool              `json:"deploy"`
		Pull       *bool             `json:"pull"`
		Recreate   *bool             `json:"recreate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	tag := strings.TrimSpace(body.Image)
	if tag == "" && p != nil {
		tag = projects.DefaultVpsroomsTag(p.Name)
		if p.Image != "" && strings.HasPrefix(p.Image, "vpsrooms/") {
			tag = p.Image
		}
	}
	if tag == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "image tag required"})
		return
	}
	if strings.TrimSpace(body.Context) == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "context required (git URL or host path)"})
		return
	}
	work := filepath.Join(s.Cfg.RuntimeDir, "_build")
	_ = os.MkdirAll(work, 0o750)
	in := projects.BuildImageInput{
		Image:      tag,
		Context:    body.Context,
		Dockerfile: body.Dockerfile,
		BuildArgs:  body.BuildArgs,
		Push:       body.Push,
		GitToken:   s.githubToken(),
		WorkDir:    work,
		Log:        io.Discard,
	}
	pull := false
	if body.Pull != nil {
		pull = *body.Pull
	}
	recreate := true
	if body.Recreate != nil {
		recreate = *body.Recreate
	}
	if err := s.startBuildAsync(p, in, body.Deploy, pull, recreate); err != nil {
		writeJSON(w, 409, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	st := "building"
	if body.Deploy && p != nil {
		st = "deploying"
	}
	extra := map[string]any{"status": st, "image": tag, "deploy": body.Deploy}
	if p != nil {
		s.acceptedProject(w, p.RoomID, extra)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "accepted": true, "status": st, "image": tag})
}

func (s *Server) apiVPSLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method")
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = "vps"
	}
	outKind, text := s.hostLogBundle(kind)
	writeJSON(w, 200, map[string]any{
		"kind":  outKind,
		"log":   text,
		"kinds": []string{"vps", "host", "panel", "api", "deploy"},
	})
}

func (s *Server) handleAPIV1ProjectContainers(w http.ResponseWriter, r *http.Request, roomID string, rest []string) {
	if len(rest) == 0 {
		if r.Method != http.MethodGet {
			writeErr(w, 405, "method")
			return
		}
		writeJSON(w, 200, map[string]any{"containers": s.roomContainersJSON(roomID)})
		return
	}
	ct := s.resolveRoomContainer(roomID, rest[0])
	if ct == nil {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "container not found", "code": "container_not_found"})
		return
	}
	if len(rest) == 1 {
		if r.Method != http.MethodGet {
			writeErr(w, 405, "method")
			return
		}
		writeJSON(w, 200, map[string]any{
			"id": ct.ID, "name": ct.Name, "service": ct.Service, "image": ct.Image,
			"docker_id": ct.DockerID, "status": ct.Status, "host_port": ct.HostPort,
		})
		return
	}
	op := rest[1]
	switch op {
	case "logs":
		if r.Method == http.MethodDelete {
			s.clearContainerLogs(w, roomID, ct.ID)
			return
		}
		q := r.URL.Query()
		q.Set("container", ct.ID)
		r.URL.RawQuery = q.Encode()
		s.handleV1Logs(w, r, roomID)
	case "start", "stop", "restart":
		if r.Method != http.MethodPost {
			writeErr(w, 405, "method")
			return
		}
		if s.Docker == nil || ct.DockerID == "" {
			writeErr(w, 400, "container has no docker id")
			return
		}
		var err error
		switch op {
		case "start":
			err = s.Docker.Start(ct.DockerID)
		case "stop":
			err = s.Docker.Stop(ct.DockerID)
		case "restart":
			err = s.Docker.Restart(ct.DockerID)
		}
		if err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		st, _ := s.Docker.InspectStatus(ct.DockerID)
		writeJSON(w, 200, map[string]any{"ok": true, "action": op, "status": st, "id": ct.ID})
	case "inspect":
		if s.Docker == nil || ct.DockerID == "" {
			writeErr(w, 400, "container has no docker id")
			return
		}
		b, err := s.Docker.InspectJSON(ct.DockerID)
		if err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(b)
	case "usage":
		cpu, mem, used, lim := 0.0, 0.0, int64(0), int64(0)
		if s.Docker != nil && ct.DockerID != "" {
			cpu, mem, used, lim = s.Docker.ParseStats(ct.DockerID)
		}
		writeJSON(w, 200, map[string]any{
			"id": ct.ID, "cpu_percent": cpu, "ram_percent": mem,
			"ram_used": used, "ram_limit": lim,
		})
	default:
		writeErr(w, 404, "not found")
	}
}

func (s *Server) apiManagerStatus(w http.ResponseWriter) {
	st := s.storageInfo()
	m := s.Metrics.Snapshot()
	roomsList, _ := s.Store.ListRooms()
	roomsOut := make([]map[string]any, 0, len(roomsList))
	for _, rm := range roomsList {
		cts, _ := s.Store.ListContainers(rm.ID)
		imgs, _ := s.Store.ListImages(rm.ID)
		vols, _ := s.Store.ListVolumes(rm.ID)
		kind := rm.Kind
		if kind == "" {
			kind = store.KindSingle
		}
		if len(cts) > 1 {
			kind = store.KindMulti
		}
		res := s.roomResourceUsage(rm.ID)
		roomsOut = append(roomsOut, map[string]any{
			"id": rm.ID, "name": rm.Name, "kind": kind, "status": "ok",
			"quota_bytes": rm.QuotaBytes, "usage_bytes": s.cachedUsage(rm.ID),
			"storage_limit": rm.QuotaBytes, "storage_used": s.cachedUsage(rm.ID),
			"cpu_percent": res["cpu_percent"], "ram_percent": res["ram_percent"],
			"ram_used":   res["ram_used"],
			"containers": len(cts), "images": len(imgs), "volumes": len(vols),
		})
	}
	freeRAM := int64(m.MemTotal) - int64(m.MemUsed)
	if freeRAM < 0 {
		freeRAM = 0
	}
	dockerOK := s.Docker != nil && s.Docker.Available()
	writeJSON(w, 200, map[string]any{
		"vps_manager":       "ok",
		"docker":            dockerOK,
		"docker_status":     map[string]any{"ok": dockerOK},
		"storage":           st,
		"storage_total":     st["disk_total"],
		"storage_used":      st["disk_used"],
		"storage_available": st["disk_free"],
		"cpu_percent":       m.CPUPercent,
		"ram_percent":       m.MemPercent,
		"ram_total":         m.MemTotal,
		"ram_used":          m.MemUsed,
		"ram_available":     freeRAM,
		"network_rx":        m.NetRx,
		"network_tx":        m.NetTx,
		"net_rx":            m.NetRx,
		"net_tx":            m.NetTx,
		"rooms":             roomsOut,
	})
}

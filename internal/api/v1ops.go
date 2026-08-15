package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/x5coder/vps-rooms/internal/stack"
	"github.com/x5coder/vps-rooms/internal/store"
)

func (s *Server) logsTarget(roomID, want string) (string, int, string) {
	want = strings.TrimSpace(want)
	list, _ := s.Store.ListContainers(roomID)
	if want != "" {
		return want, 0, ""
	}
	if len(list) == 1 {
		return list[0].ID, 0, ""
	}
	if len(list) == 0 {
		return "", 404, "no containers"
	}
	return "", 400, "logs_target_required"
}

func (s *Server) handleV1Logs(w http.ResponseWriter, r *http.Request, roomID string) {
	want := logsQueryTarget(r)
	target, code, errCode := s.logsTarget(roomID, want)
	if strings.Contains(r.URL.Path, "/logs/clear") || (len(strings.Split(r.URL.Path, "/")) > 0 && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/clear")) {
		if code != 0 {
			writeJSON(w, code, map[string]any{"ok": false, "error": errCode, "code": errCode})
			return
		}
		s.clearContainerLogs(w, roomID, target)
		return
	}
	if code != 0 {
		msg := "pass name=CONTAINER_NAME or container=CONTAINER_ID"
		if errCode == "no containers" {
			msg = "no containers"
		}
		writeJSON(w, code, map[string]any{"ok": false, "error": msg, "code": errCode})
		return
	}
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("stream") == "1" || strings.HasSuffix(r.URL.Path, "/stream") {
			s.streamContainerLogs(w, r, roomID, target)
			return
		}
		s.writeContainerLogs(w, roomID, target)
	case http.MethodDelete:
		s.clearContainerLogs(w, roomID, target)
	default:
		writeErr(w, 405, "method")
	}
}

func (s *Server) clearContainerLogs(w http.ResponseWriter, roomID, want string) {
	ct := s.resolveRoomContainer(roomID, want)
	if ct == nil || ct.DockerID == "" {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "container not found", "code": "container_not_found"})
		return
	}
	if s.Docker == nil {
		writeErr(w, 400, "Docker unavailable")
		return
	}
	if err := s.Docker.TruncateLogs(ct.DockerID); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "code": "logs_clear_failed"})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "cleared": true, "container_id": ct.ID})
}

func (s *Server) streamContainerLogs(w http.ResponseWriter, r *http.Request, roomID, want string) {
	ct := s.resolveRoomContainer(roomID, want)
	if ct == nil || ct.DockerID == "" {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "container not found", "code": "container_not_found"})
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "stream unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fl.Flush()
	_ = s.Docker.FollowLogs(r.Context(), ct.DockerID, 200, &flushWriter{w: w, f: fl})
}

func (s *Server) handleV1Env(w http.ResponseWriter, r *http.Request, roomID string) {
	switch r.Method {
	case http.MethodGet:
		key := strings.TrimSpace(r.URL.Query().Get("key"))
		text, err := s.readRoomEnv(roomID)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		rows := parseEnvMap(text)
		if key != "" {
			m := envMapFromRows(rows)
			v, ok := m[key]
			if !ok {
				writeJSON(w, 404, map[string]any{"ok": false, "error": "key not found", "code": "env_key_not_found"})
				return
			}
			writeJSON(w, 200, map[string]any{"key": key, "value": v})
			return
		}
		list := make([]map[string]string, 0, len(rows))
		for _, row := range rows {
			list = append(list, map[string]string{"key": row[0], "value": row[1]})
		}
		writeJSON(w, 200, map[string]any{"variables": list})
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		var body struct {
			Key        string              `json:"key"`
			Value      string              `json:"value"`
			Variables  []map[string]string `json:"variables"`
			ReplaceKey bool                `json:"replace"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "invalid request", "code": "invalid_request"})
			return
		}
		pairs := body.Variables
		if body.Key != "" {
			pairs = append(pairs, map[string]string{"key": body.Key, "value": body.Value})
		}
		if len(pairs) == 0 {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "key and value required", "code": "env_key_required"})
			return
		}
		var kv [][2]string
		for _, p := range pairs {
			kv = append(kv, [2]string{strings.TrimSpace(p["key"]), p["value"]})
		}
		if err := s.envSetKeys(roomID, kv, false); err != nil {
			code := "env_failed"
			if strings.Contains(err.Error(), "not found") {
				code = "env_key_not_found"
			}
			writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "code": code})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	case http.MethodDelete:
		key := strings.TrimSpace(r.URL.Query().Get("key"))
		if key == "" {
			var body struct {
				Key string `json:"key"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			key = strings.TrimSpace(body.Key)
		}
		if err := s.envDeleteKey(roomID, key); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "code": "env_key_not_found"})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	default:
		writeErr(w, 405, "method")
	}
}

func (s *Server) volumeView(roomID string, v *store.VolumeRec) map[string]any {
	if v == nil {
		return nil
	}
	src := strings.TrimSpace(v.DockerName)
	if src == "" {
		src = v.Name
	}
	sz := v.SizeBytes
	users := []map[string]string{}
	if s.Docker != nil && src != "" {
		if n := s.Docker.VolumeSizeBytes(src); n > 0 {
			sz = n
			v.SizeBytes = n
			_ = s.Store.UpsertVolume(*v)
		}
		users = s.Docker.VolumeUsers(src)
	}
	linked := []map[string]any{}
	cts := s.roomContainersJSON(roomID)
	for _, u := range users {
		row := map[string]any{"docker_id": u["docker_id"], "name": u["name"], "service": u["service"]}
		for _, c := range cts {
			if fmt.Sprint(c["docker_id"]) == u["docker_id"] || fmt.Sprint(c["name"]) == u["name"] {
				row["id"] = c["id"]
				row["container"] = c["name"]
			}
		}
		linked = append(linked, row)
	}
	return map[string]any{
		"id": v.ID, "name": v.Name, "docker_name": v.DockerName,
		"size_bytes": sz, "used_bytes": sz, "ordinal": v.Ordinal,
		"containers": linked,
	}
}

func (s *Server) handleV1Volumes(w http.ResponseWriter, r *http.Request, roomID string, rest []string) {
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			list, _ := s.Store.ListVolumes(roomID)
			out := make([]map[string]any, 0, len(list))
			for i := range list {
				v := list[i]
				out = append(out, s.volumeView(roomID, &v))
			}
			writeJSON(w, 200, map[string]any{"volumes": out})
		case http.MethodPost:
			s.apiCreateVolume(w, r, roomID)
		default:
			writeErr(w, 405, "method")
		}
		return
	}
	vol := s.resolveRoomVolume(roomID, rest[0])
	if vol == nil {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "volume not found", "code": "volume_not_found"})
		return
	}
	if len(rest) == 1 {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, 200, s.volumeView(roomID, vol))
		case http.MethodDelete:
			s.apiDeleteVolume(w, roomID, vol)
		default:
			writeErr(w, 405, "method")
		}
		return
	}
	switch rest[1] {
	case "clean", "wipe":
		if r.Method != http.MethodPost {
			writeErr(w, 405, "method")
			return
		}
		src := vol.DockerName
		if src == "" {
			src = vol.Name
		}
		if s.Docker == nil {
			writeErr(w, 400, "Docker unavailable")
			return
		}
		if err := s.Docker.CleanVolume(src); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "code": "volume_clean_failed"})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "cleaned": true, "id": vol.ID})
	default:
		writeErr(w, 404, "not found")
	}
}

func (s *Server) apiCreateVolume(w http.ResponseWriter, r *http.Request, roomID string) {
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "name required", "code": "volume_name_required"})
		return
	}
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return -1
	}, name)
	if safe == "" {
		safe = "data"
	}
	dname := "vpsrooms_" + store.ShortRoomID(roomID) + "_" + safe
	if s.Docker != nil {
		if err := s.Docker.CreateNamedVolume(dname); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "code": "volume_create_failed"})
			return
		}
	}
	rec := store.VolumeRec{
		ID: uuid.NewString(), RoomID: roomID, Name: name, DockerName: dname, CreatedAt: time.Now().UTC(),
	}
	if err := s.Store.UpsertVolume(rec); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "volume": s.volumeView(roomID, &rec)})
}

func (s *Server) apiDeleteVolume(w http.ResponseWriter, roomID string, vol *store.VolumeRec) {
	src := vol.DockerName
	if src == "" {
		src = vol.Name
	}
	if s.Docker != nil && src != "" {
		if users := s.Docker.VolumeUsers(src); len(users) > 0 {
			writeJSON(w, 409, map[string]any{"ok": false, "error": "volume is in use", "code": "volume_in_use", "containers": users})
			return
		}
		_ = s.Docker.RemoveNamedVolume(src)
	}
	_ = s.Store.DeleteVolume(vol.ID)
	writeJSON(w, 200, map[string]any{"ok": true, "deleted": true, "id": vol.ID})
}

func (s *Server) handleV1Images(w http.ResponseWriter, r *http.Request, roomID string, rest []string) {
	if len(rest) == 0 {
		if r.Method != http.MethodGet {
			writeErr(w, 405, "method")
			return
		}
		writeJSON(w, 200, map[string]any{"images": s.roomImagesJSON(roomID)})
		return
	}
	if rest[0] == "load" && r.Method == http.MethodPost {
		s.apiLoadImageTar(w, r, roomID)
		return
	}
	imgs := s.roomImagesJSON(roomID)
	var hit map[string]any
	want := strings.ToLower(rest[0])
	for _, im := range imgs {
		if strings.EqualFold(fmt.Sprint(im["id"]), rest[0]) || strings.EqualFold(fmt.Sprint(im["name"]), rest[0]) || strings.EqualFold(fmt.Sprint(im["ref"]), rest[0]) {
			hit = im
			break
		}
		if strings.Contains(strings.ToLower(fmt.Sprint(im["name"])), want) {
			hit = im
		}
	}
	if hit == nil {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "image not found", "code": "image_not_found"})
		return
	}
	if len(rest) >= 2 && rest[1] == "inspect" {
		ref := fmt.Sprint(hit["ref"])
		if s.Docker == nil || ref == "" || ref == "<nil>" {
			writeErr(w, 400, "image ref missing")
			return
		}
		b, err := s.Docker.InspectImageJSON(ref)
		if err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(b)
		return
	}
	writeJSON(w, 200, hit)
}

func (s *Server) apiLoadImageTar(w http.ResponseWriter, r *http.Request, roomID string) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<30)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "code": "file_required"})
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "multipart field file is required", "code": "file_required"})
		return
	}
	defer file.Close()
	tmp, err := os.CreateTemp("", "vm-load-*.tar")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		writeErr(w, 400, err.Error())
		return
	}
	tmp.Close()
	if s.Docker == nil {
		writeErr(w, 400, "Docker unavailable")
		return
	}
	tag, err := s.Docker.LoadImageTag(tmp.Name())
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "code": "image_load_failed"})
		return
	}
	base := strings.TrimSuffix(filepath.Base(hdr.Filename), ".tar")
	if tag == "" {
		tag = base
	}
	rec := store.ImageRec{ID: uuid.NewString(), RoomID: roomID, Name: base, Ref: tag, SizeBytes: s.Docker.ImageSize(tag)}
	_ = s.Store.UpsertImage(rec)
	writeJSON(w, 200, map[string]any{"ok": true, "loaded": true, "name": rec.Name, "ref": tag, "size_bytes": rec.SizeBytes})
}

func (s *Server) stackDir(roomID string) string {
	dir := filepath.Join(s.Cfg.RuntimeDir, roomID, "stack")
	root := dir
	if d := findExistingComposeRoot(dir); d != "" {
		root = d
	}
	return root
}

func findExistingComposeRoot(dir string) string {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	if stack.AnalyzeComposeDir(dir).Path != "" {
		return dir
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() {
			p := filepath.Join(dir, e.Name())
			if stack.AnalyzeComposeDir(p).Path != "" {
				return p
			}
		}
	}
	return dir
}

func (s *Server) handleV1Compose(w http.ResponseWriter, r *http.Request, roomID string, rest []string) {
	dir := s.stackDir(roomID)
	info := stack.AnalyzeComposeDir(dir)
	op := ""
	if len(rest) > 0 {
		op = rest[0]
	}
	switch op {
	case "", "read":
		if info.Path == "" {
			writeJSON(w, 404, map[string]any{"ok": false, "error": "compose.yml not found", "code": "compose_missing"})
			return
		}
		b, _ := os.ReadFile(info.Path)
		writeJSON(w, 200, map[string]any{"path": info.Path, "content": string(b)})
	case "validate":
		if !info.OK {
			writeJSON(w, 400, map[string]any{"ok": false, "error": info.Error, "code": "compose_invalid", "compose": info})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "compose": info})
	case "services", "analyze":
		writeJSON(w, 200, info)
	case "images":
		writeJSON(w, 200, map[string]any{"images": info.Images})
	case "volumes":
		writeJSON(w, 200, map[string]any{"volumes": info.Volumes})
	case "networks":
		writeJSON(w, 200, map[string]any{"networks": info.Networks})
	default:
		writeErr(w, 404, "not found")
	}
}

func (s *Server) handleV1Stack(w http.ResponseWriter, r *http.Request, roomID string, rest []string) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	if s.Docker == nil {
		writeErr(w, 400, "Docker unavailable")
		return
	}
	op := ""
	if len(rest) > 0 {
		op = rest[0]
	}
	dir := s.stackDir(roomID)
	proj := stack.ComposeProject(roomID)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	var out []byte
	var err error
	switch op {
	case "start", "up", "create":
		out, err = s.Docker.ComposeCmd(ctx, dir, proj, "up", "-d", "--pull", "never")
	case "stop":
		out, err = s.Docker.ComposeCmd(ctx, dir, proj, "stop")
	case "restart":
		out, err = s.Docker.ComposeCmd(ctx, dir, proj, "restart")
	case "remove", "down":
		args := []string{"down"}
		if r.URL.Query().Get("volumes") == "1" {
			args = append(args, "-v")
		}
		out, err = s.Docker.ComposeCmd(ctx, dir, proj, args...)
	default:
		writeErr(w, 404, "not found")
		return
	}
	res := map[string]any{"ok": err == nil, "action": op, "output": string(out)}
	if err != nil {
		res["error"] = err.Error()
		writeJSON(w, 400, res)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) apiExecWait(w http.ResponseWriter, r *http.Request, roomID string) {
	var body struct {
		Command     string `json:"command"`
		ContainerID string `json:"container_id"`
		TimeoutSec  int    `json:"timeout_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Command) == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "command required", "code": "command_required"})
		return
	}
	_ = s.Rooms.EnsureUnlocked(roomID)
	var ctx context.Context
	var cancel context.CancelFunc
	if body.TimeoutSec > 0 {
		d := time.Duration(body.TimeoutSec) * time.Second
		if d > 24*time.Hour {
			d = 24 * time.Hour
		}
		ctx, cancel = context.WithTimeout(r.Context(), d)
	} else {
		ctx, cancel = context.WithCancel(r.Context())
	}
	defer cancel()

	dockerID := ""
	if strings.TrimSpace(body.ContainerID) != "" {
		if ct := s.resolveRoomContainer(roomID, body.ContainerID); ct != nil {
			dockerID = ct.DockerID
		}
	} else {
		list, _ := s.Store.ListContainers(roomID)
		if len(list) == 1 {
			dockerID = list[0].DockerID
		}
	}

	var stdout, stderr bytes.Buffer
	var cmd *exec.Cmd
	where := "room"
	if dockerID != "" && s.Docker != nil {
		cmd = exec.CommandContext(ctx, "docker", "exec", dockerID, "sh", "-lc", body.Command)
		where = "container"
	} else {
		dir := filepath.Join(s.Cfg.RuntimeDir, roomID)
		_ = os.MkdirAll(dir, 0o750)
		cmd = exec.CommandContext(ctx, "sh", "-lc", body.Command)
		cmd.Dir = dir
	}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
			err = nil
		} else {
			code = 1
		}
	}
	trim := func(s string) string {
		if len(s) > 512*1024 {
			return s[:512*1024] + "\n…truncated"
		}
		return s
	}
	res := map[string]any{
		"stdout": trim(stdout.String()), "stderr": trim(stderr.String()),
		"exit_code": code, "where": where, "output": trim(stdout.String() + stderr.String()),
	}
	if err != nil {
		res["error"] = err.Error()
	}
	writeJSON(w, 200, res)
}

var termUp = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func (s *Server) handleV1TerminalWS(w http.ResponseWriter, r *http.Request, roomID string) {
	if q := strings.TrimSpace(r.URL.Query().Get("access_token")); q != "" && r.Header.Get("Authorization") == "" {
		r.Header.Set("Authorization", "Bearer "+q)
	}
	conn, err := termUp.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = s.Rooms.EnsureUnlocked(roomID)
	list, _ := s.Store.ListContainers(roomID)
	dockerID := ""
	if len(list) == 1 {
		dockerID = list[0].DockerID
	}
	var cmd *exec.Cmd
	if dockerID != "" && s.Docker != nil {
		cmd = exec.Command("docker", "exec", "-i", dockerID, "sh")
	} else {
		dir := filepath.Join(s.Cfg.RuntimeDir, roomID)
		_ = os.MkdirAll(dir, 0o750)
		cmd = exec.Command("sh")
		cmd.Dir = dir
	}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(err.Error()))
		return
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				_ = conn.WriteMessage(websocket.BinaryMessage, buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if _, err := stdin.Write(data); err != nil {
			break
		}
	}
	_ = stdin.Close()
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func (s *Server) roomResourceUsage(roomID string) map[string]any {
	cts, _ := s.Store.ListContainers(roomID)
	var cpu, memPct float64
	var memUsed, memLimit int64
	n := 0
	for _, c := range cts {
		if s.Docker == nil || c.DockerID == "" {
			continue
		}
		cp, mp, used, lim := s.Docker.ParseStats(c.DockerID)
		cpu += cp
		memPct += mp
		memUsed += used
		if lim > memLimit {
			memLimit = lim
		}
		n++
	}
	avgCPU, avgMem := 0.0, 0.0
	if n > 0 {
		avgCPU = cpu
		avgMem = memPct / float64(n)
	}
	return map[string]any{
		"cpu_percent": avgCPU, "ram_percent": avgMem,
		"ram_used": memUsed, "ram_limit": memLimit,
		"containers_sampled": n,
	}
}

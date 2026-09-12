package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/x5coder/vps-rooms/internal/metrics"
	"github.com/x5coder/vps-rooms/internal/projects"
	"github.com/x5coder/vps-rooms/internal/proxy"
	"github.com/x5coder/vps-rooms/internal/stack"
	"github.com/x5coder/vps-rooms/internal/store"
)

var agentTokenName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{1,63}$`)

// routesAgent separates browser-owner credential management from the bearer
// authenticated Agent discovery API. The latter deliberately has no cookie or
// SSH dependency.
func (s *Server) routesAgent() {
	s.Mux.HandleFunc("/api/agent/tokens", s.withGate(s.handleAgentTokens))
	s.Mux.HandleFunc("/api/agent/tokens/", s.withGate(s.handleAgentTokenByID))
	s.Mux.HandleFunc("/x5coder-agent/v1/tools", s.handleAgentTools)
	s.Mux.HandleFunc("/x5coder-agent/v1/tools/", s.handleAgentInvoke)
}

// handleAgentInvoke is deliberately allow-listed: agents can request an intent,
// never arbitrary host commands or paths. Accepts POST (JSON body) and GET
// (empty input) so no-input tools like get_vps_overview work with a plain GET.
func (s *Server) handleAgentInvoke(w http.ResponseWriter, r *http.Request) {
	if !s.agentAuthorized(r) { writeErr(w, 401, "invalid agent token"); return }
	if r.Method != http.MethodPost && r.Method != http.MethodGet { writeErr(w, 405, "method"); return }
	name := strings.TrimPrefix(r.URL.Path, "/x5coder-agent/v1/tools/")
	in := map[string]any{}
	if r.Method == http.MethodPost {
		// Deploy/update carry a base64 source archive — allow a larger body.
		limit := int64(1 << 20)
		if name == "deploy_project" || name == "update_project" {
			limit = 64 << 20
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if err != nil { agentFail(w, 400, "INVALID_INPUT", err.Error()); return }
		if len(strings.TrimSpace(string(body))) > 0 {
			if err := json.Unmarshal(body, &in); err != nil { agentFail(w, 400, "INVALID_INPUT", err.Error()); return }
		}
	}
	data, err := s.invokeAgentTool(name, in)
	if err != nil { agentFail(w, 400, "OPERATION_FAILED", err.Error()); return }
	writeJSON(w, 200, map[string]any{"success": true, "data": data})
}

func agentFail(w http.ResponseWriter, code int, kind, msg string) { writeJSON(w, code, map[string]any{"success": false, "error": map[string]string{"code": kind, "message": msg}}) }
func agentString(in map[string]any, key string) string { v, _ := in[key].(string); return strings.TrimSpace(v) }
func agentNumber(in map[string]any, key string) (float64, bool) {
	switch v := in[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}

// agentRoomKind maps the required room_type enum (single_container |
// multi_container) to the internal room kind, rejecting anything else.
func agentRoomKind(in map[string]any) (string, error) {
	switch agentString(in, "room_type") {
	case "single_container":
		return store.KindSingle, nil
	case "multi_container":
		return store.KindMulti, nil
	default:
		return "", fmt.Errorf("room_type is required and must be single_container or multi_container")
	}
}
func agentInt(in map[string]any, key string, d int) int { if v, ok := in[key].(float64); ok { return int(v) }; return d }
func agentBool(in map[string]any, key string) bool { v, _ := in[key].(bool); return v }

func (s *Server) invokeAgentTool(name string, in map[string]any) (any, error) {
	roomID := agentString(in, "room_id")
	room := func() (*store.Room, error) { r, e := s.Store.GetRoom(roomID); if e != nil || r == nil { return nil, fmt.Errorf("room not found") }; return r, nil }
	switch name {
	case "get_vps_overview":
		return s.vpsOverview(), nil
	case "get_projects":
		rooms, err := s.Store.ListRooms()
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0, len(rooms))
		for i := range rooms {
			out = append(out, s.agentRoomDetail(&rooms[i]))
		}
		return map[string]any{"rooms": out, "count": len(out)}, nil
	case "get_project":
		if roomID == "" {
			return nil, fmt.Errorf("room_id is required")
		}
		r, err := room()
		if err != nil {
			return nil, err
		}
		return s.agentRoomDetail(r), nil
	case "get_container":
		id := agentString(in, "container_id")
		if id == "" {
			return nil, fmt.Errorf("container_id is required")
		}
		c, err := s.agentFindContainer(id)
		if err != nil || c == nil {
			return nil, fmt.Errorf("container not found")
		}
		return s.agentContainerDetail(c), nil
	case "deploy_project", "update_project":
		return s.agentDeploy(roomID, name == "update_project", in)
	case "get_deployment_status":
		if roomID == "" {
			return nil, fmt.Errorf("room_id is required")
		}
		r, err := room()
		if err != nil {
			return nil, err
		}
		return s.agentDeploymentStatus(r), nil
	case "create_project":
		name := agentString(in, "name")
		if name == "" {
			return nil, fmt.Errorf("name is required")
		}
		q, ok := agentNumber(in, "storage_size_gb")
		if !ok || q <= 0 {
			return nil, fmt.Errorf("storage_size_gb is required and must be > 0")
		}
		kind, err := agentRoomKind(in)
		if err != nil {
			return nil, err
		}
		password := agentString(in, "password")
		if password != "" && len(password) < 6 {
			return nil, fmt.Errorf("password must be at least 6 characters")
		}
		generated := false
		if password == "" {
			generated = true
		}
		r, pass, err := s.createEmptyRoom(name, q, 8080, 0, password, kind, "", false, "")
		if err != nil {
			return nil, err
		}
		return map[string]any{"room": r, "password": pass, "password_generated": generated}, nil
	case "manage_project_containers":
		if roomID == "" {
			return nil, fmt.Errorf("room_id is required")
		}
		if _, err := room(); err != nil {
			return nil, err
		}
		action := agentString(in, "action")
		if action != "start" && action != "stop" && action != "restart" {
			return nil, fmt.Errorf("action is required and must be start, stop, or restart")
		}
		target := agentString(in, "container_id")
		cs, _ := s.Store.ListContainers(roomID)
		if target != "" {
			found := false
			for _, c := range cs {
				if c.ID == target || c.DockerID == target || c.Name == target {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("container not found in room")
			}
		} else if len(cs) == 0 {
			return map[string]any{"room_id": roomID, "action": action, "results": []any{}, "note": "room has no containers"}, nil
		}
		if s.Docker == nil || !s.Docker.Available() {
			return nil, fmt.Errorf("Docker unavailable — cannot %s containers", action)
		}
		results := []map[string]any{}
		for _, c := range cs {
			if target != "" && c.ID != target && c.DockerID != target && c.Name != target {
				continue
			}
			r := map[string]any{"id": c.ID, "name": c.Name, "action": action}
			if c.DockerID == "" {
				r["ok"] = false
				r["error"] = "container has no live Docker container"
				results = append(results, r)
				continue
			}
			var err error
			switch action {
			case "start":
				err = s.Docker.Start(c.DockerID)
			case "stop":
				err = s.Docker.Stop(c.DockerID)
			case "restart":
				err = s.Docker.Restart(c.DockerID)
			}
			if err != nil {
				r["ok"] = false
				r["error"] = err.Error()
			} else {
				r["ok"] = true
				c.Status = actionStatus(action)
				_ = s.Store.UpsertContainer(c)
				r["status"] = c.Status
			}
			results = append(results, r)
		}
		return map[string]any{"room_id": roomID, "action": action, "results": results}, nil
	case "get_project_volumes":
		if roomID == "" {
			return nil, fmt.Errorf("room_id is required")
		}
		r, err := room()
		if err != nil {
			return nil, err
		}
		filter := agentString(in, "container_id")
		if filter != "" {
			c, err := s.agentFindContainer(filter)
			if err != nil || c == nil {
				return nil, fmt.Errorf("container not found")
			}
			if c.RoomID != roomID {
				return nil, fmt.Errorf("container does not belong to room")
			}
		}
		vols, err := s.agentRoomVolumes(r, filter)
		if err != nil {
			return nil, err
		}
		return map[string]any{"room_id": roomID, "container_id": filter, "volumes": vols, "count": len(vols)}, nil
	case "delete_container_volumes":
		return s.agentDeleteContainerVolumes(roomID, in)
	case "delete_project_resource":
		return s.agentDeleteResource(roomID, in)
	case "set_container_env":
		return s.agentSetContainerEnv(roomID, in)
	case "connect_domain":
		return s.agentConnectDomain(roomID, in)
	case "get_project_logs":
		if roomID == "" {
			return nil, fmt.Errorf("room_id is required")
		}
		if _, err := room(); err != nil {
			return nil, err
		}
		lines := agentInt(in, "lines", 200)
		if lines < 1 || lines > 5000 {
			return nil, fmt.Errorf("lines must be 1–5000")
		}
		target := agentString(in, "container_id")
		if target != "" {
			c, err := s.agentFindContainer(target)
			if err != nil || c == nil || c.RoomID != roomID {
				return nil, fmt.Errorf("container not found in room")
			}
		}
		dockerOK := s.Docker != nil && s.Docker.Available()
		out := map[string]string{}
		if dockerOK {
			cs, _ := s.Store.ListContainers(roomID)
			for _, c := range cs {
				if target != "" && c.ID != target && c.DockerID != target && c.Name != target {
					continue
				}
				if c.DockerID == "" {
					continue
				}
				x, err := s.Docker.Logs(c.DockerID, lines)
				if err != nil {
					return nil, err
				}
				out[c.ID] = x
			}
		}
		res := map[string]any{
			"room_id": roomID, "container_id": target, "lines": lines,
			"logs": out, "docker_available": dockerOK,
		}
		if !dockerOK {
			res["note"] = "Docker unavailable — no live logs"
		}
		return res, nil
	case "get_vps_manager_logs":
		return s.agentManagerLogs(), nil
	}
	return nil, fmt.Errorf("tool %q is not enabled yet", name)
}

// vpsOverview backs the get_vps_overview tool: one snapshot answering
// "what is the whole VPS state and resource usage right now?" — CPU, RAM,
// disk, network, Docker, storage usage, and managed resource usage.
// It takes no input.
func (s *Server) vpsOverview() map[string]any {
	m := s.Metrics.Snapshot()
	facts := metrics.CollectFacts()
	dockerOK := s.Docker != nil && s.Docker.Available()
	storage := s.storageInfo()

	rooms, _ := s.Store.ListRooms()
	projs, _ := s.Store.ListAllProjects()
	var managedBytes int64
	for _, rm := range rooms {
		managedBytes += s.cachedDisk(rm.ID).Usage
	}
	memFree := int64(0)
	if m.MemTotal > m.MemUsed {
		memFree = int64(m.MemTotal - m.MemUsed)
	}
	return map[string]any{
		"cpu": map[string]any{
			"cores": m.CPUCores, "usage_percent": m.CPUPercent,
			"model": facts.CPUModel, "load_1m": m.Load1,
		},
		"ram": map[string]any{
			"total_bytes": m.MemTotal, "used_bytes": m.MemUsed,
			"free_bytes": memFree, "usage_percent": m.MemPercent,
		},
		"disk": map[string]any{
			"total_bytes": m.DiskTotal, "used_bytes": m.DiskUsed,
			"free_bytes": m.DiskFree, "usage_percent": m.DiskPercent,
		},
		"network": map[string]any{
			"rx_bytes": m.NetRx, "tx_bytes": m.NetTx,
			"public_ip": facts.PublicIP, "primary_ip": facts.PrimaryIP,
			"ssh_port": facts.SSHPort,
		},
		"docker": map[string]any{"available": dockerOK},
		"storage_usage": storage,
		"resource_usage": map[string]any{
			"rooms_count": len(rooms), "projects_count": len(projs),
			"managed_data_bytes": managedBytes,
			"hostname": facts.Hostname, "os": facts.OS,
			"uptime": metrics.FormatUptime(facts.UptimeSec),
		},
		"timestamp": m.Timestamp,
	}
}

// agentRoomDetail backs get_projects / get_project: complete Room information —
// type, status, containers, ports, domains, SSL, volumes, configuration,
// environment variable NAMES (never values), logs status, storage,
// deployment status, and resources.
func (s *Server) agentRoomDetail(r *store.Room) map[string]any {
	projs, _ := s.Store.ListProjects(r.ID)
	cts, _ := s.Store.ListContainers(r.ID)
	vols, _ := s.Store.ListVolumes(r.ID)
	imgs, _ := s.Store.ListImages(r.ID)
	usage := s.cachedDisk(r.ID)
	dockerOK := s.Docker != nil && s.Docker.Available()

	containers := make([]map[string]any, 0, len(cts))
	for _, c := range cts {
		st := s.containerLiveStatus(c.DockerID, c.Status)
		containers = append(containers, map[string]any{
			"id": c.ID, "name": c.Name, "service": c.Service, "image": c.Image,
			"docker_id": c.DockerID, "status": st,
			"host_port": c.HostPort, "container_port": c.ContainerPort,
		})
	}
	ports := make([]map[string]any, 0, len(projs)+len(cts))
	for _, p := range projs {
		ports = append(ports, map[string]any{
			"container": p.Name, "host_port": p.HostPort, "container_port": p.ContainerPort,
		})
	}
	// Adopted rooms may hold containers with no project rows — include their ports too.
	for _, c := range cts {
		if c.HostPort == 0 && c.ContainerPort == 0 {
			continue
		}
		dup := false
		for _, e := range ports {
			if e["container"] == c.Name && e["host_port"] == c.HostPort && e["container_port"] == c.ContainerPort {
				dup = true
				break
			}
		}
		if !dup {
			ports = append(ports, map[string]any{
				"container": c.Name, "host_port": c.HostPort, "container_port": c.ContainerPort,
			})
		}
	}
	domains := make([]map[string]any, 0, len(projs))
	for _, p := range projs {
		if strings.TrimSpace(p.Domain) == "" {
			continue
		}
		domains = append(domains, map[string]any{
			"container": p.Name, "domain": p.Domain,
			"enabled": p.DomainEnabled, "ssl_status": p.SSLStatus,
		})
	}
	envNames := []string{}
	if b, err := os.ReadFile(s.Rooms.RoomEnvPath(r.ID)); err == nil {
		envNames = agentEnvNames(string(b))
	}
	job := s.jobKind(r.ID)
	roomJob := s.Projects.ReadRoomJob(r.ID)
	hist := s.Projects.ReadUpdateHistory(r.ID)
	var lastUpdate any
	if len(hist) > 0 {
		lastUpdate = hist[0]
	}
	return map[string]any{
		"id": r.ID, "name": r.Name, "type": r.Kind,
		"status":      s.agentRoomStatus(r, projs, cts),
		"vps_path":    s.Rooms.VPSPath(r.ID),
		"work_dir":    s.Rooms.RoomWorkDir(r.ID),
		"containers":  containers,
		"ports":       ports,
		"domains":     domains,
		"ssl":         r.SSL,
		"volumes":     vols,
		"configuration": map[string]any{
			"env_path": s.Rooms.RoomEnvPath(r.ID), "config_dir": s.Rooms.RoomConfigDir(r.ID),
			"volumes_dir": s.Rooms.RoomVolumesDir(r.ID), "logs_dir": s.Rooms.RoomLogsDir(r.ID),
		},
		"env_var_names": envNames,
		"logs_status":   map[string]any{"dir": s.Rooms.RoomLogsDir(r.ID), "available": dockerOK},
		"storage": map[string]any{
			"quota_bytes": r.QuotaBytes, "usage_bytes": usage.Usage,
			"volume_bytes": usage.Volumes, "image_bytes": usage.Images,
			"footprint_bytes": usage.Footprint,
		},
		"deployment": map[string]any{
			"status": s.agentRoomStatus(r, projs, cts), "job": job,
			"room_job": roomJob.Status, "updates_count": len(hist), "last_update": lastUpdate,
		},
		"resources": map[string]any{
			"quota_bytes": r.QuotaBytes, "usage_bytes": usage.Usage,
			"projects": len(projs), "containers": len(cts),
			"images": len(imgs), "volumes": len(vols),
		},
	}
}

// agentRoomStatus derives the runtime state: empty | running | stopped | error.
func (s *Server) agentRoomStatus(r *store.Room, projs []store.Project, cts []store.Container) string {
	if len(projs) == 0 && len(cts) == 0 {
		return "empty"
	}
	bad := false
	check := func(ref, fallback string) string {
		return s.containerLiveStatus(ref, fallback)
	}
	for _, p := range projs {
		if st := check(p.ContainerID, p.Status); st == "running" {
			return "running"
		} else if st == "restarting" || st == "exited" || st == "dead" || st == "error" {
			bad = true
		}
	}
	for _, c := range cts {
		ref := c.DockerID
		if ref == "" {
			ref = c.Name
		}
		if st := check(ref, c.Status); st == "running" {
			return "running"
		} else if st == "restarting" || st == "exited" || st == "dead" || st == "error" {
			bad = true
		}
	}
	if bad {
		return "error"
	}
	return "stopped"
}

// agentFindContainer locates a managed container by panel ID, Docker ID, or name.
func (s *Server) agentFindContainer(id string) (*store.Container, error) {
	if c, _ := s.Store.GetContainer(id); c != nil {
		return c, nil
	}
	rooms, err := s.Store.ListRooms()
	if err != nil {
		return nil, err
	}
	for _, r := range rooms {
		cts, _ := s.Store.ListContainers(r.ID)
		for i := range cts {
			if cts[i].DockerID == id || cts[i].Name == id {
				c := cts[i]
				return &c, nil
			}
		}
		projs, _ := s.Store.ListProjects(r.ID)
		for _, p := range projs {
			if p.ContainerID == id || p.ID == id {
				if c, _ := s.Store.GetContainer(p.ID); c != nil {
					return c, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("container not found")
}

// agentContainerDetail backs get_container: status, health, image, ports,
// CPU, RAM, networks, volumes, and environment variable NAMES.
// Secret values are never returned.
func (s *Server) agentContainerDetail(c *store.Container) map[string]any {
	dockerOK := s.Docker != nil && s.Docker.Available()
	status := s.containerLiveStatus(c.DockerID, c.Status)
	health := "unknown"
	networks := []string{}
	image := c.Image
	if dockerOK && c.DockerID != "" {
		if insp := s.agentInspect(c.DockerID); insp["available"] == true {
			if h, _ := insp["health"].(string); h != "" {
				health = h
			}
			if n, _ := insp["networks"].([]string); n != nil {
				networks = n
			}
			if im, _ := insp["image"].(string); im != "" {
				image = im
			}
		}
	}
	if health == "unknown" && status == "running" {
		health = "none"
	}
	ports := map[string]any{"host_port": c.HostPort, "container_port": c.ContainerPort}
	if dockerOK && c.DockerID != "" {
		if p := s.Docker.PublishedHostPort(c.DockerID); p > 0 {
			ports["published_host_port"] = p
		}
	}
	cpu := map[string]any{"available": false}
	ram := map[string]any{"available": false}
	if dockerOK && c.DockerID != "" && status == "running" {
		cpuPct, memPct, memUsed, memLimit := s.Docker.ParseStats(c.DockerID)
		cpu = map[string]any{"available": true, "usage_percent": cpuPct}
		ram = map[string]any{
			"available": true, "used_bytes": memUsed,
			"limit_bytes": memLimit, "usage_percent": memPct,
		}
	}
	volumes := []map[string]any{}
	if dockerOK && c.DockerID != "" {
		if mnts, err := s.Docker.ListMounts(c.DockerID); err == nil {
			for _, mnt := range mnts {
				volumes = append(volumes, map[string]any{
					"type": mnt.Type, "name": mnt.Name,
					"source": mnt.Source, "destination": mnt.Destination,
				})
			}
		}
	}
	envNames := []string{}
	if dockerOK && c.DockerID != "" {
		if env, err := s.Docker.InspectEnv(c.DockerID); err == nil {
			envNames = agentEnvNames(strings.Join(env, "\n"))
		}
	}
	return map[string]any{
		"id": c.ID, "room_id": c.RoomID, "name": c.Name, "service": c.Service,
		"image": image, "docker_id": c.DockerID, "status": status, "health": health,
		"ports": ports, "cpu": cpu, "ram": ram,
		"networks": networks, "volumes": volumes, "env_var_names": envNames,
	}
}

// agentInspect extracts status, health, image, and networks from docker inspect.
// Live Docker only; otherwise {"available": false}.
func (s *Server) agentInspect(id string) map[string]any {
	out := map[string]any{"available": false}
	if s.Docker == nil || !s.Docker.Available() || id == "" {
		return out
	}
	raw, err := s.Docker.InspectJSON(id)
	if err != nil {
		return out
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
		return out
	}
	obj := arr[0]
	out["available"] = true
	if st, _ := obj["State"].(map[string]any); st != nil {
		if v, _ := st["Status"].(string); v != "" {
			out["status"] = v
		}
		if h, _ := st["Health"].(map[string]any); h != nil {
			if v, _ := h["Status"].(string); v != "" {
				out["health"] = v
			}
		}
	}
	if cfg, _ := obj["Config"].(map[string]any); cfg != nil {
		if v, _ := cfg["Image"].(string); v != "" {
			out["image"] = v
		}
	}
	nets := []string{}
	if ns, _ := obj["NetworkSettings"].(map[string]any); ns != nil {
		if nm, _ := ns["Networks"].(map[string]any); nm != nil {
			for k := range nm {
				nets = append(nets, k)
			}
		}
	}
	out["networks"] = nets
	return out
}

// agentEnvNames returns environment variable NAMES only — values are dropped,
// so secrets can never leak through the agent API.
func agentEnvNames(text string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		t = strings.TrimSpace(strings.TrimPrefix(t, "export "))
		k, _, ok := strings.Cut(t, "=")
		if !ok {
			k = t
		}
		k = strings.TrimSpace(k)
		if k == "" || strings.ContainsAny(k, " \t\"'") {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	if out == nil {
		return []string{}
	}
	return out
}

// agentDeploy backs deploy_project / update_project.
//
// The base64 source archive is decoded and permanently stored: single rooms
// into project/, multi rooms into stack/ (persistent data/volumes and
// config/.env live in sibling directories and are never touched). For
// single rooms internal_port is required and recorded. For multi rooms the
// orchestration file is analyzed to discover services, ports, volumes, and
// networks. When Docker is available the native pipeline is attempted
// (single: build+run from Dockerfile; multi: offline stack deploy when the
// archive carries image packages); otherwise the source stays stored and the
// deployment status reflects that.
func (s *Server) agentDeploy(roomID string, updating bool, in map[string]any) (any, error) {
	if roomID == "" {
		return nil, fmt.Errorf("room_id is required")
	}
	r, err := s.Store.GetRoom(roomID)
	if err != nil || r == nil {
		return nil, fmt.Errorf("room not found")
	}
	source := agentString(in, "source")
	if source == "" {
		return nil, fmt.Errorf("source is required (base64-encoded ZIP or tar.gz)")
	}
	multi := strings.ToLower(strings.TrimSpace(r.Kind)) == store.KindMulti
	internalPort := 0
	if !multi {
		internalPort = agentInt(in, "internal_port", 0)
		if internalPort < 1 || internalPort > 65535 {
			if updating {
				// Updates reuse the port recorded by the previous deploy.
				if pc, _ := s.readRoomPending(roomID); pc >= 1 && pc <= 65535 {
					internalPort = pc
				} else {
					return nil, fmt.Errorf("internal_port is required for Single Container Rooms (1-65535)")
				}
			} else {
				return nil, fmt.Errorf("internal_port is required for Single Container Rooms (1-65535)")
			}
		}
	} else if v, ok := agentNumber(in, "internal_port"); ok && v >= 1 && v <= 65535 {
		internalPort = int(v) // accepted but informational: multi ports come from orchestration
	}
	archivePath, format, err := agentDecodeSource(source)
	if err != nil {
		return nil, err
	}
	defer os.Remove(archivePath)
	return s.deployRoomArchive(r, updating, archivePath, format, internalPort)
}

// deployRoomArchive is the shared store/discover/build core used by the agent
// tools and the panel ZIP upload: single rooms store into project/, multi
// rooms into stack/ (persistent data/volumes and config/.env are never
// touched). Docker pipelines run only when Docker is available.
func (s *Server) deployRoomArchive(r *store.Room, updating bool, archivePath, format string, internalPort int) (any, error) {
	roomID := r.ID
	multi := strings.ToLower(strings.TrimSpace(r.Kind)) == store.KindMulti
	if !multi && (internalPort < 1 || internalPort > 65535) {
		if updating {
			// Updates reuse the port recorded by the previous deploy.
			if pc, _ := s.readRoomPending(roomID); pc >= 1 && pc <= 65535 {
				internalPort = pc
			} else {
				return nil, fmt.Errorf("internal_port is required for Single Container Rooms (1-65535)")
			}
		} else {
			return nil, fmt.Errorf("internal_port is required for Single Container Rooms (1-65535)")
		}
	}
	dockerOK := s.Docker != nil && s.Docker.Available()
	job := "deploy"
	if updating {
		job = "update"
	}
	s.Projects.WriteRoomJob(roomID, projects.DeployMeta{Status: "deploying", Job: job})
	fail := func(err error) (any, error) {
		s.Projects.WriteRoomJob(roomID, projects.DeployMeta{Status: "error", Job: job})
		_ = appendLog(s.Cfg.DataDir, "deploy", "AGENT "+job+" FAIL room="+roomID+" err="+err.Error())
		return nil, err
	}

	if !multi {
		dest := s.Rooms.RoomProjectDir(roomID)
		// Stage outside project/: DeployBuild copies the source into a
		// per-project subdir of project/, so extracting there directly
		// would copy the destination into itself (nested garbage + EISDIR).
		stage := filepath.Join(s.Rooms.VPSPath(roomID), ".incoming")
		defer os.RemoveAll(stage)
		_ = os.RemoveAll(stage)
		st, err := agentExtractArchive(archivePath, format, stage)
		if err != nil {
			return fail(fmt.Errorf("extract: %w", err))
		}
		unwrapped, _ := agentUnwrapSingleDir(stage)
		envPath := s.Rooms.RoomEnvPath(roomID)
		addedExample, addedReal, err := agentApplyExampleEnv(envPath, stage)
		if err != nil {
			return fail(fmt.Errorf("env: %w", err))
		}
		cPort := internalPort
		if cPort <= 0 {
			cPort = 80
		}
		dockergen, err := agentEnsureDockerfile(stage, cPort)
		if err != nil {
			return fail(err)
		}
		hPort := 0
		if updating {
			if pc, ph := s.readRoomPending(roomID); pc > 0 {
				if internalPort > 0 {
					cPort = internalPort
				} else {
					cPort = pc
				}
				hPort = ph
			}
		}
		s.writeRoomPending(roomID, cPort, hPort)
		status := "stored"
		note := "source stored under project/; Docker unavailable — build and run on a Docker host"
		var built any
		replacedProjects, replacedContainers, keepPort := 0, 0, hPort
		if dockerOK {
			if olds, _ := s.Store.ListProjects(roomID); len(olds) > 0 {
				replacedProjects = len(olds)
				if keepPort <= 0 {
					keepPort = olds[0].HostPort
				}
				if cts, _ := s.Store.ListContainers(roomID); len(cts) > 0 {
					replacedContainers = len(cts)
				}
			}
			p, err := s.Projects.DeployBuild(projects.DeployBuildInput{
				RoomID: roomID, Name: r.Name, SourceDir: stage,
				HostPort: keepPort, ContainerPort: cPort, Log: io.Discard,
				Replace: true,
			})
			if err != nil {
				return fail(fmt.Errorf("build: %w", err))
			}
			status = "running"
			note = "built, replaced previous version, and started"
			built = map[string]any{
				"project_id": p.ID, "image": p.Image, "container_id": p.ContainerID,
				"host_port": p.HostPort, "container_port": p.ContainerPort,
				"replaced_projects": replacedProjects, "replaced_containers": replacedContainers,
			}
		}
		// Promote the staged source to the canonical project/ directory,
		// preserving live project dirs (mounts.json, deploy meta, .env).
		// Wiping them would orphan running containers on their next restart.
		keep := map[string]struct{}{}
		if projs, _ := s.Store.ListProjects(roomID); len(projs) > 0 {
			for _, p := range projs {
				if p.ID != "" {
					keep[p.ID] = struct{}{}
				}
			}
		}
		_ = os.MkdirAll(dest, 0o750)
		if ents, err := os.ReadDir(dest); err == nil {
			for _, e := range ents {
				if _, ok := keep[e.Name()]; ok {
					continue
				}
				_ = os.RemoveAll(filepath.Join(dest, e.Name()))
			}
		}
		if stageEnts, err := os.ReadDir(stage); err == nil {
			for _, e := range stageEnts {
				if _, ok := keep[e.Name()]; ok {
					continue
				}
				_ = os.Rename(filepath.Join(stage, e.Name()), filepath.Join(dest, e.Name()))
			}
		}
		_ = os.RemoveAll(stage)
		s.Projects.WriteRoomJob(roomID, projects.DeployMeta{Status: status, Job: job})
		_ = appendLog(s.Cfg.DataDir, "deploy", "AGENT "+job+" room="+roomID+" status="+status)
		return map[string]any{
			"room_id": roomID, "kind": r.Kind, "mode": job,
			"stored": map[string]any{"dir": dest, "files": st.Files, "bytes": st.Bytes, "unwrapped": unwrapped},
			"internal_port": cPort, "docker_available": dockerOK,
			"dockerfile_generated": dockergen,
			"env_applied": map[string]any{"from_example": addedExample, "from_env": addedReal},
			"deployment": map[string]any{"status": status, "job": job, "built": built},
			"note":       note,
		}, nil
	}

	// Multi: preserve persistent bind sources, re-extract stack, analyze compose.
	dest := s.Rooms.RoomStackDir(roomID)
	saved := filepath.Join(filepath.Dir(dest), ".stack-preserve")
	_ = os.RemoveAll(saved)
	_ = os.MkdirAll(saved, 0o750)
	for _, name := range []string{"data", "volumes", "__volumes"} {
		if st, err := os.Stat(filepath.Join(dest, name)); err == nil && st.IsDir() {
			_ = os.Rename(filepath.Join(dest, name), filepath.Join(saved, name))
		}
	}
	_ = os.RemoveAll(dest)
	_ = os.MkdirAll(dest, 0o750)
	for _, name := range []string{"data", "volumes", "__volumes"} {
		if st, err := os.Stat(filepath.Join(saved, name)); err == nil && st.IsDir() {
			_ = os.Rename(filepath.Join(saved, name), filepath.Join(dest, name))
		}
	}
	_ = os.RemoveAll(saved)
	st, err := agentExtractArchive(archivePath, format, dest)
	if err != nil {
		return fail(fmt.Errorf("extract: %w", err))
	}
	unwrapped, _ := agentUnwrapSingleDir(dest)
	envPath := s.Rooms.RoomEnvPath(roomID)
	addedExample, addedReal, err := agentApplyExampleEnv(envPath, dest)
	if err != nil {
		return fail(fmt.Errorf("env: %w", err))
	}
	discovery := stack.AnalyzeComposeDir(dest)
	composeFile := agentComposeFile(dest)
	ports := agentComposePorts(composeFile)
	status := "stored"
	note := "stack stored and analyzed; Docker unavailable — compose up on a Docker host"
	if dockerOK {
		if !discovery.OK {
			return fail(fmt.Errorf("compose: %s", discovery.Error))
		}
		if agentStackHasImages(dest, composeFile) {
			if err := s.Stack.DeployMulti(r, archivePath, io.Discard); err != nil {
				return fail(fmt.Errorf("stack deploy: %w", err))
			}
			status = "running"
			note = "stack deployed"
		} else {
			note = "stack stored and analyzed; archive carries no image packages for offline stack up"
		}
	}
	s.Projects.WriteRoomJob(roomID, projects.DeployMeta{Status: status, Job: job})
	_ = appendLog(s.Cfg.DataDir, "deploy", "AGENT "+job+" room="+roomID+" status="+status)
	return map[string]any{
		"room_id": roomID, "kind": r.Kind, "mode": job,
		"stored": map[string]any{"dir": dest, "files": st.Files, "bytes": st.Bytes, "unwrapped": unwrapped},
		"discovery": map[string]any{
			"compose_file": composeFile, "services": discovery.Services,
			"images": discovery.Images, "volumes": discovery.Volumes,
			"networks": discovery.Networks, "ports": ports, "ok": discovery.OK,
		},
		"docker_available": dockerOK,
		"env_applied": map[string]any{"from_example": addedExample, "from_env": addedReal},
		"deployment":       map[string]any{"status": status, "job": job},
		"note":             note,
	}, nil
}

// agentStackHasImages reports whether the extracted stack carries offline
// image packages (images/*.tar*) next to the compose file.
func agentStackHasImages(stackDir, composeFile string) bool {
	root := stackDir
	if composeFile != "" {
		root = filepath.Dir(composeFile)
	}
	ents, err := os.ReadDir(filepath.Join(root, "images"))
	if err != nil {
		return false
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		low := strings.ToLower(e.Name())
		if strings.HasSuffix(low, ".tar") || strings.HasSuffix(low, ".tar.gz") || strings.HasSuffix(low, ".tgz") {
			return true
		}
	}
	return false
}

// agentDeploymentStatus backs get_deployment_status: current deployment
// progress and state of a Room — live job, room job record, per-project
// deploy metadata, and update history count.
func (s *Server) agentDeploymentStatus(r *store.Room) map[string]any {
	projs, _ := s.Store.ListProjects(r.ID)
	cts, _ := s.Store.ListContainers(r.ID)
	roomJob := s.Projects.ReadRoomJob(r.ID)
	hist := s.Projects.ReadUpdateHistory(r.ID)
	items := make([]map[string]any, 0, len(projs))
	for _, p := range projs {
		st := s.containerLiveStatus(p.ContainerID, p.Status)
		meta := s.Projects.ReadDeployMeta(r.ID, p.ID)
		items = append(items, map[string]any{
			"project_id": p.ID, "name": p.Name, "status": st,
			"image": p.Image, "container_id": p.ContainerID, "host_port": p.HostPort,
			"job": s.jobKind(p.ID),
			"last_deploy_at": meta.LastDeployAt, "last_deploy_ok": meta.LastDeployOK,
			"last_deploy_error": meta.LastDeployError,
		})
	}
	return map[string]any{
		"room_id": r.ID, "name": r.Name, "type": r.Kind,
		"status": s.agentRoomStatus(r, projs, cts),
		"job":    s.jobKind(r.ID),
		"room_job": map[string]any{
			"status": roomJob.Status, "job": roomJob.Job,
		},
		"projects": items, "updates_count": len(hist),
	}
}

// actionStatus maps a manage action to the resulting container status record.
func actionStatus(action string) string {
	if action == "stop" {
		return "stopped"
	}
	return "running"
}

// agentRoomVolumes backs get_project_volumes: every tracked Room volume with
// name, usage, mount paths, ownership, and sharing analysis. With an optional
// container filter only volumes mounted by that container are returned
// (live Docker required for mount mapping).
func (s *Server) agentRoomVolumes(r *store.Room, filter string) ([]map[string]any, error) {
	vols, err := s.Store.ListVolumes(r.ID)
	if err != nil {
		return nil, err
	}
	dockerOK := s.Docker != nil && s.Docker.Available()
	volRoot := s.Rooms.RoomVolumesDir(r.ID)
	// Live mount index: volume key -> container ids + mount details.
	mountedBy := map[string][]string{}
	mountPaths := map[string][]map[string]any{}
	if dockerOK {
		cts, _ := s.Store.ListContainers(r.ID)
		for _, c := range cts {
			if c.DockerID == "" {
				continue
			}
			mnts, err := s.Docker.ListMounts(c.DockerID)
			if err != nil {
				continue
			}
			for _, mnt := range mnts {
				for _, key := range []string{mnt.Name, mnt.Source} {
					if key == "" {
						continue
					}
					mountedBy[key] = appendUnique(mountedBy[key], c.ID)
					mountPaths[key] = append(mountPaths[key], map[string]any{
						"container": c.ID, "destination": mnt.Destination, "source": mnt.Source,
					})
				}
			}
		}
	}
	var filterMounts map[string]bool
	if filter != "" {
		if !dockerOK {
			return nil, fmt.Errorf("live Docker required to map container volumes")
		}
		c, err := s.agentFindContainer(filter)
		if err != nil || c == nil || c.RoomID != r.ID {
			return nil, fmt.Errorf("container not found in room")
		}
		filterMounts = map[string]bool{}
		if c.DockerID != "" {
			if mnts, err := s.Docker.ListMounts(c.DockerID); err == nil {
				for _, mnt := range mnts {
					if mnt.Name != "" {
						filterMounts[mnt.Name] = true
					}
					if mnt.Source != "" {
						filterMounts[mnt.Source] = true
					}
				}
			}
		}
	}
	out := []map[string]any{}
	for _, v := range vols {
		if filterMounts != nil && !filterMounts[v.DockerName] && !filterMounts[v.Name] {
			continue
		}
		hostPath := ""
		if st, err := os.Stat(filepath.Join(volRoot, v.Name)); err == nil && st.IsDir() {
			hostPath = filepath.Join(volRoot, v.Name)
		}
		mounted := []string{}
		paths := []map[string]any{}
		for _, key := range []string{v.DockerName, v.Name, hostPath} {
			if key == "" {
				continue
			}
			for _, id := range mountedBy[key] {
				mounted = appendUnique(mounted, id)
			}
			paths = append(paths, mountPaths[key]...)
		}
		shared, reason := s.agentVolumeShared(r, v, mounted)
		entry := map[string]any{
			"id": v.ID, "name": v.Name, "docker_name": v.DockerName,
			"usage_bytes": v.SizeBytes, "host_path": hostPath,
			"owned_by": map[string]string{"room_id": r.ID, "room_name": r.Name},
			"mounted_by": mounted, "mount_paths": paths,
			"shared": shared, "shared_reason": reason,
		}
		out = append(out, entry)
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out, nil
}

// agentVolumeShared decides whether deleting a volume is unsafe: it is shared
// when another container mounts it (live) or another room tracks the same
// docker volume / name (records).
func (s *Server) agentVolumeShared(r *store.Room, v store.VolumeRec, mounted []string) (bool, string) {
	for _, id := range mounted {
		if c, _ := s.Store.GetContainer(id); c != nil && c.RoomID != r.ID {
			return true, "mounted by container " + id + " of another room"
		}
	}
	// Count distinct mounting containers inside the room.
	seen := map[string]struct{}{}
	for _, id := range mounted {
		seen[id] = struct{}{}
	}
	if len(seen) > 1 {
		return true, "mounted by multiple containers"
	}
	rooms, _ := s.Store.ListRooms()
	for i := range rooms {
		if rooms[i].ID == r.ID {
			continue
		}
		other, _ := s.Store.ListVolumes(rooms[i].ID)
		for _, o := range other {
			if v.DockerName != "" && o.DockerName == v.DockerName {
				return true, "tracked by room " + rooms[i].Name
			}
			if v.DockerName == "" && o.Name == v.Name {
				return true, "tracked by room " + rooms[i].Name
			}
		}
	}
	if len(seen) == 1 {
		return false, "mounted by a single container"
	}
	return false, "owned by room only"
}

func appendUnique(in []string, v string) []string {
	for _, e := range in {
		if e == v {
			return in
		}
	}
	return append(in, v)
}

// agentDeleteContainerVolumes backs delete_container_volumes. Volumes are
// deleted only with explicit confirm=true, only when owned by the room, and
// never when shared. Associated = live-mounted by the container, plus the
// panel data-dir convention volumes/<containerID>.
func (s *Server) agentDeleteContainerVolumes(roomID string, in map[string]any) (any, error) {
	if roomID == "" {
		return nil, fmt.Errorf("room_id is required")
	}
	r, err := s.Store.GetRoom(roomID)
	if err != nil || r == nil {
		return nil, fmt.Errorf("room not found")
	}
	cid := agentString(in, "container_id")
	if cid == "" {
		return nil, fmt.Errorf("container_id is required")
	}
	c, err := s.agentFindContainer(cid)
	if err != nil || c == nil {
		return nil, fmt.Errorf("container not found")
	}
	if c.RoomID != roomID {
		return nil, fmt.Errorf("container does not belong to room")
	}
	confirm, _ := in["confirm"].(bool)
	if !confirm {
		return nil, fmt.Errorf("explicit confirmation required (confirm: true) — volumes are deleted permanently")
	}
	dockerOK := s.Docker != nil && s.Docker.Available()
	volRoot := s.Rooms.RoomVolumesDir(roomID)
	vols, _ := s.Store.ListVolumes(roomID)
	// Associated volumes: live mounts of this container + data-dir convention.
	associated := map[string]store.VolumeRec{}
	if dockerOK && c.DockerID != "" {
		if mnts, err := s.Docker.ListMounts(c.DockerID); err == nil {
			for _, mnt := range mnts {
				for _, v := range vols {
					if v.DockerName != "" && (v.DockerName == mnt.Name || v.DockerName == mnt.Source) {
						associated[v.ID] = v
					}
				}
			}
		}
	}
	for _, v := range vols {
		if v.Name == c.ID {
			associated[v.ID] = v
		}
	}
	results := []map[string]any{}
	for _, v := range associated {
		entry := map[string]any{"id": v.ID, "name": v.Name}
		// Ownership: tracked by this room (guaranteed) and host dir inside room volumes.
		hostDir := filepath.Join(volRoot, v.Name)
		rel, relErr := filepath.Rel(volRoot, hostDir)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			entry["deleted"] = false
			entry["reason"] = "host path outside room volumes — refused"
			results = append(results, entry)
			continue
		}
		// Sharing: live external users + record scan.
		shared := false
		reason := ""
		if dockerOK && v.DockerName != "" {
			for _, u := range s.Docker.VolumeUsers(v.DockerName) {
				other := u["container"]
				if other == "" {
					other = u["name"]
				}
				if other != "" && other != c.DockerID && other != c.Name {
					shared = true
					reason = "in use by " + other
					break
				}
			}
		}
		if !shared {
			if sh, rs := s.agentVolumeShared(r, v, nil); sh {
				shared, reason = true, rs
			}
		}
		if shared {
			entry["deleted"] = false
			entry["reason"] = "shared volume — refused (" + reason + ")"
			results = append(results, entry)
			continue
		}
		if dockerOK && v.DockerName != "" {
			_ = s.Docker.RemoveNamedVolume(v.DockerName)
		}
		_ = os.RemoveAll(hostDir)
		_ = s.Store.DeleteVolume(v.ID)
		entry["deleted"] = true
		results = append(results, entry)
	}
	if results == nil {
		results = []map[string]any{}
	}
	return map[string]any{
		"room_id": roomID, "container_id": c.ID, "results": results,
		"deleted_count": countDeleted(results),
	}, nil
}

func countDeleted(results []map[string]any) int {
	n := 0
	for _, r := range results {
		if ok, _ := r["deleted"].(bool); ok {
			n++
		}
	}
	return n
}

// agentDeleteResource backs delete_project_resource: delete a whole Room or
// one container. Room deletion removes containers and configuration but
// preserves persistent volumes aside (records + files), since volume deletion
// is handled separately.
func (s *Server) agentDeleteResource(roomID string, in map[string]any) (any, error) {
	if roomID == "" {
		return nil, fmt.Errorf("room_id is required")
	}
	target := agentString(in, "target")
	if target != "room" && target != "container" {
		return nil, fmt.Errorf("target is required and must be room or container")
	}
	confirm, _ := in["confirm"].(bool)
	if !confirm {
		return nil, fmt.Errorf("explicit confirmation required (confirm: true) — deletion is permanent")
	}
	r, err := s.Store.GetRoom(roomID)
	if err != nil || r == nil {
		return nil, fmt.Errorf("room not found")
	}
	if target == "container" {
		cid := agentString(in, "container_id")
		if cid == "" {
			return nil, fmt.Errorf("container_id is required when target is container")
		}
		c, err := s.agentFindContainer(cid)
		if err != nil || c == nil {
			return nil, fmt.Errorf("container not found")
		}
		if c.RoomID != roomID {
			return nil, fmt.Errorf("container does not belong to room")
		}
		dockerOK := s.Docker != nil && s.Docker.Available()
		stopped := false
		if dockerOK {
			if c.DockerID != "" {
				_ = s.Docker.Stop(c.DockerID)
				_ = s.Docker.Remove(c.DockerID, true)
				stopped = true
			}
			if c.Name != "" {
				_ = s.Docker.RemoveByName(c.Name)
			}
		}
		_ = s.Store.DeleteContainer(c.ID)
		linkedProject := false
		if p, _ := s.Store.GetProject(c.ID); p != nil {
			_ = s.Store.DeleteProject(p.ID)
			linkedProject = true
		}
		return map[string]any{
			"room_id": roomID, "target": "container", "deleted": c.ID,
			"name": c.Name, "docker_removed": stopped, "linked_project_removed": linkedProject,
		}, nil
	}
	// target == room: preserve volumes aside, then full delete.
	vols, _ := s.Store.ListVolumes(roomID)
	volRoot := s.Rooms.RoomVolumesDir(roomID)
	preserveDir := filepath.Join(s.Cfg.GlobalBackupDir(), roomID+"-volumes")
	preserved := []string{}
	if len(vols) > 0 {
		_ = os.MkdirAll(preserveDir, 0o750)
		manifest, _ := json.MarshalIndent(vols, "", "  ")
		_ = os.WriteFile(filepath.Join(preserveDir, "volumes.json"), manifest, 0o600)
		for _, v := range vols {
			src := filepath.Join(volRoot, v.Name)
			if st, err := os.Stat(src); err != nil || !st.IsDir() {
				continue
			}
			if err := agentCopyDir(src, filepath.Join(preserveDir, v.Name)); err != nil {
				return nil, fmt.Errorf("preserve volume %s: %w", v.Name, err)
			}
			preserved = append(preserved, v.Name)
		}
	}
	roomName := r.Name
	if s.Proxy != nil {
		if projs, _ := s.Store.ListProjects(roomID); len(projs) > 0 {
			for _, p := range projs {
				if strings.TrimSpace(p.Domain) != "" {
					_ = s.Proxy.Remove(p.Domain)
				}
			}
		}
	}
	if err := s.Rooms.Delete(roomID); err != nil {
		return nil, err
	}
	return map[string]any{
		"room_id": roomID, "target": "room", "deleted": roomName,
		"volumes_preserved": preserved, "volumes_location": preserveDir,
		"note": "containers and configuration removed; persistent volumes preserved aside (volume deletion is separate)",
	}, nil
}

// agentCopyDir copies a directory tree (used to preserve volumes aside).
func agentCopyDir(src, dest string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		_ = out.Close()
		return err
	})
}

var agentEnvKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// agentSetContainerEnv backs set_container_env: merges variables into the
// Room .env (the single configuration mechanism for either Room type),
// optionally recreating containers so values apply. Only names are ever
// returned — values stay secret.
func (s *Server) agentSetContainerEnv(roomID string, in map[string]any) (any, error) {
	if roomID == "" {
		return nil, fmt.Errorf("room_id is required")
	}
	r, err := s.Store.GetRoom(roomID)
	if err != nil || r == nil {
		return nil, fmt.Errorf("room not found")
	}
	cid := agentString(in, "container_id")
	if cid == "" {
		return nil, fmt.Errorf("container_id is required")
	}
	c, err := s.agentFindContainer(cid)
	if err != nil || c == nil {
		return nil, fmt.Errorf("container not found")
	}
	if c.RoomID != roomID {
		return nil, fmt.Errorf("container does not belong to room")
	}
	rawVars, ok := in["variables"].(map[string]any)
	if !ok || len(rawVars) == 0 {
		return nil, fmt.Errorf("variables is required and must be a non-empty object")
	}
	merged := map[string]string{}
	names := []string{}
	for k, v := range rawVars {
		k = strings.TrimSpace(k)
		if !agentEnvKeyRe.MatchString(k) {
			return nil, fmt.Errorf("invalid variable name %q", k)
		}
		var sVal string
		switch t := v.(type) {
		case string:
			sVal = t
		case float64, bool:
			sVal = fmt.Sprint(t)
		default:
			return nil, fmt.Errorf("variable %q must be a string, number, or boolean", k)
		}
		if strings.ContainsAny(sVal, "\n\r") {
			return nil, fmt.Errorf("variable %q must be single-line", k)
		}
		merged[k] = sVal
		names = append(names, k)
	}
	envPath := s.Rooms.RoomEnvPath(roomID)
	_ = os.MkdirAll(filepath.Dir(envPath), 0o700)
	mergedText, err := agentMergeEnvFile(envPath, merged)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(envPath, []byte(mergedText), 0o600); err != nil {
		return nil, err
	}
	restart := true
	if v, ok := in["restart"].(bool); ok {
		restart = v
	}
	restarted := false
	if restart {
		if err := s.Projects.ApplyRoomEnv(roomID); err != nil {
			return nil, fmt.Errorf("variables saved but restart failed: %w", err)
		}
		restarted = true
	}
	return map[string]any{
		"room_id": roomID, "container_id": c.ID, "updated": names,
		"restarted": restarted, "env_path": envPath,
	}, nil
}

// agentMergeEnvFile merges vars into an env file, preserving comments,
// order, and unrelated entries. Returns the new file text.
func agentMergeEnvFile(envPath string, vars map[string]string) (string, error) {
	existing := ""
	if b, err := os.ReadFile(envPath); err == nil {
		existing = string(b)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	lines := []string{}
	seen := map[string]bool{}
	if strings.TrimSpace(existing) != "" {
		for _, line := range strings.Split(existing, "\n") {
			trim := strings.TrimSpace(line)
			if trim == "" || strings.HasPrefix(trim, "#") || !strings.Contains(trim, "=") {
				lines = append(lines, line)
				continue
			}
			k := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(trim), "export "))
			if i := strings.IndexByte(k, '='); i >= 0 {
				k = strings.TrimSpace(k[:i])
			}
			if v, ok := vars[k]; ok {
				lines = append(lines, k+"="+v)
				seen[k] = true
				continue
			}
			lines = append(lines, line)
		}
	}
	for k, v := range vars {
		if !seen[k] {
			lines = append(lines, k+"="+v)
		}
	}
	return strings.Join(lines, "\n") + "\n", nil
}

var agentDomainRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// agentConnectDomain backs connect_domain: routes a domain to a Room project
// through the reverse proxy, with HTTPS enabled by default.
func (s *Server) agentConnectDomain(roomID string, in map[string]any) (any, error) {
	if roomID == "" {
		return nil, fmt.Errorf("room_id is required")
	}
	r, err := s.Store.GetRoom(roomID)
	if err != nil || r == nil {
		return nil, fmt.Errorf("room not found")
	}
	domain := strings.ToLower(strings.TrimSpace(agentString(in, "domain")))
	domain = strings.TrimPrefix(strings.TrimPrefix(domain, "https://"), "http://")
	domain = strings.Split(domain, "/")[0]
	domain = strings.TrimSuffix(domain, ".")
	if domain == "" || len(domain) > 253 || !agentDomainRe.MatchString(domain) {
		return nil, fmt.Errorf("invalid domain")
	}
	if s.Proxy == nil && !proxy.NginxInstalled() {
		return nil, fmt.Errorf("proxy not ready — cannot route domains")
	}
	multi := strings.ToLower(strings.TrimSpace(r.Kind)) == store.KindMulti
	projs, _ := s.Store.ListProjects(roomID)
	if len(projs) == 0 {
		return nil, fmt.Errorf("room has no deployable project — deploy first")
	}
	var target *store.Project
	if multi {
		cid := agentString(in, "container_id")
		if cid == "" {
			return nil, fmt.Errorf("container_id is required for Multi Container Rooms")
		}
		c, err := s.agentFindContainer(cid)
		if err != nil || c == nil {
			return nil, fmt.Errorf("container not found")
		}
		if c.RoomID != roomID {
			return nil, fmt.Errorf("container does not belong to room")
		}
		for i := range projs {
			if projs[i].ID == c.ID || projs[i].Name == c.Service || projs[i].Name == c.Name {
				p := projs[i]
				target = &p
				break
			}
		}
		if target == nil {
			return nil, fmt.Errorf("no deployable project linked to container")
		}
	} else {
		p := projs[0]
		target = &p
	}
	if err := s.applyDomain(target, domain, true); err != nil {
		return nil, err
	}
	ssl := true
	if v, ok := in["ssl"].(bool); ok {
		ssl = v
	}
	sslStatus := target.SSLStatus
	if !ssl {
		target.SSLStatus = "http-only"
		_ = s.Store.UpdateProject(*target)
		sslStatus = "http-only"
	}
	return map[string]any{
		"room_id": roomID, "domain": domain, "project": target.Name,
		"container_id": agentString(in, "container_id"),
		"host_port": target.HostPort, "ssl": ssl, "ssl_status": sslStatus,
	}, nil
}

// agentManagerLogs backs get_vps_manager_logs: the Manager's own streams —
// panel (API errors, warnings, system events), deploy (deployment events),
// and host (host-level events). Each stream is tailed; no input needed.
func (s *Server) agentManagerLogs() map[string]any {
	const tailBytes = 64 * 1024
	streams := map[string]string{}
	for _, name := range []string{"panel", "deploy", "host"} {
		text := ""
		if b, err := os.ReadFile(filepath.Join(s.Cfg.DataDir, "logs", name+".log")); err == nil {
			if len(b) > tailBytes {
				b = b[len(b)-tailBytes:]
				if i := strings.IndexByte(string(b), '\n'); i >= 0 {
					b = b[i+1:]
				}
			}
			text = string(b)
		}
		streams[name] = text
	}
	return map[string]any{
		"logs": streams["panel"], "streams": streams,
		"dir": filepath.Join(s.Cfg.DataDir, "logs"),
	}
}

func (s *Server) handleAgentTokens(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil { return }
	switch r.Method {
	case http.MethodGet:
		tokens, err := s.Store.ListAgentTokens()
		if err != nil { writeErr(w, 500, err.Error()); return }
		writeJSON(w, 200, map[string]any{"tokens": tokens, "endpoint": s.agentEndpoint(r), "tools": agentToolCatalog})
	case http.MethodPost:
		var body struct { Name string `json:"name"` }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil { writeErr(w, 400, "invalid JSON body"); return }
		name := strings.TrimSpace(body.Name)
		if !agentTokenName.MatchString(name) { writeErr(w, 400, "name must be 2–64 letters, numbers, spaces, dots, underscores, or hyphens"); return }
		secret, err := newAgentSecret()
		if err != nil { writeErr(w, 500, "could not generate token"); return }
		digest := sha256.Sum256([]byte(secret))
		token := store.AgentToken{ID: uuid.NewString(), Name: name, Prefix: secret[:15], CreatedAt: time.Now().UTC()}
		if err := s.Store.CreateAgentToken(token, hex.EncodeToString(digest[:])); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") { writeErr(w, 409, "a token with this name already exists"); return }
			writeErr(w, 500, err.Error()); return
		}
		// This is the only response that contains the secret. The database stores
		// its SHA-256 digest, so it cannot be recovered later.
		writeJSON(w, http.StatusCreated, map[string]any{"token": token, "secret": secret, "endpoint": s.agentEndpoint(r)})
	default:
		writeErr(w, 405, "method")
	}
}

func (s *Server) handleAgentTokenByID(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil { return }
	rest := strings.TrimPrefix(r.URL.Path, "/api/agent/tokens/")
	if strings.HasSuffix(rest, "/rotate") && r.Method == http.MethodPost {
		id := strings.TrimSuffix(rest, "/rotate")
		if _, err := uuid.Parse(id); err != nil { writeErr(w, 400, "invalid token id"); return }
		secret, err := newAgentSecret()
		if err != nil { writeErr(w, 500, "could not generate token"); return }
		digest := sha256.Sum256([]byte(secret))
		token, err := s.Store.RotateAgentToken(id, secret[:15], hex.EncodeToString(digest[:]), time.Now().UTC())
		if err != nil { writeErr(w, 500, err.Error()); return }
		if token.ID == "" { writeErr(w, 404, "token not found"); return }
		// Only response carrying the new secret — old one stops working now.
		writeJSON(w, 200, map[string]any{"token": token, "secret": secret, "endpoint": s.agentEndpoint(r)})
		return
	}
	if r.Method != http.MethodDelete { writeErr(w, 405, "method"); return }
	id := rest
	if _, err := uuid.Parse(id); err != nil { writeErr(w, 400, "invalid token id"); return }
	if err := s.Store.DeleteAgentToken(id); err != nil { writeErr(w, 500, err.Error()); return }
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAgentTools(w http.ResponseWriter, r *http.Request) {
	// Public: anyone can list the tools. Running any tool still requires
	// a valid Authorization: Bearer <token> header (see handleAgentInvoke).
	writeJSON(w, 200, map[string]any{"success": true, "data": map[string]any{"version": "v1", "tools": agentToolCatalog}})
}

func (s *Server) agentAuthorized(r *http.Request) bool {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) < 32 { return false }
	digest := sha256.Sum256([]byte(parts[1]))
	ok, err := s.Store.VerifyAgentToken(hex.EncodeToString(digest[:]))
	return err == nil && ok
}

func (s *Server) agentEndpoint(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") { scheme = "https" }
	// Keep r.Host as-is so a non-default port (:9090, :18090) stays in the URL.
	// r.Host already carries the client-visible host; behind a reverse proxy it
	// is the public domain (default port implied, correctly omitted by browsers).
	host := strings.TrimSpace(r.Host)
	if host == "" {
		host = s.publicHost(r)
	}
	return scheme + "://" + host + "/x5coder-agent/v1/tools"
}

func newAgentSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil { return "", err }
	return "x5ca_" + base64.RawURLEncoding.EncodeToString(b), nil
}

var agentToolCatalog = []map[string]any{
	{"name": "get_vps_overview", "description": "Get the overall VPS status including CPU, RAM, disk, network, Docker, storage usage, and resource usage.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{"name": "get_projects", "description": "Get all Rooms/Projects on the VPS with their type, status, containers, ports, domains, SSL, volumes, storage usage, deployment status, and resources.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{"name": "get_project", "description": "Get complete information about a specific Room, including its type, containers, volumes, configuration, environment variable names, domains, SSL, logs status, storage, deployment status, and resources.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"room_id": map[string]string{"type": "string", "description": "The unique Room ID."}}, "required": []string{"room_id"}}},
	{"name": "get_container", "description": "Get complete information about a specific container, including status, health, image, ports, CPU, RAM, networks, volumes, and environment variable names. Secret values must never be returned.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"container_id": map[string]string{"type": "string", "description": "The unique container ID."}}, "required": []string{"container_id"}}},
	{"name": "create_project", "description": "Create a new empty Room with a name, password, hard storage quota, and explicitly selected Room type.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]string{"type": "string", "description": "Room name."}, "password": map[string]string{"type": "string", "description": "Optional Room password. If omitted, the system may generate one."}, "storage_size_gb": map[string]string{"type": "number", "description": "Hard storage limit for the entire Room in GB."}, "room_type": map[string]any{"type": "string", "enum": []string{"single_container", "multi_container"}, "description": "The Room deployment type. Must be explicitly selected."}}, "required": []string{"name", "storage_size_gb", "room_type"}}},
	{"name": "deploy_project", "description": "Deploy a project to an existing Room from a ZIP source. The source is extracted and permanently stored under the Room project directory for Single Container Rooms, or under the stack directory for Multi Container Rooms. For Single Container Rooms, internal_port is required. For Multi Container Rooms, the Agent reads the supported orchestration configuration such as docker-compose.yml to discover services, ports, volumes, and networks.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"room_id": map[string]string{"type": "string", "description": "The target Room ID."}, "source": map[string]string{"type": "string", "description": "Uploaded project ZIP file."}, "internal_port": map[string]string{"type": "integer", "description": "The port the application listens on inside the container. Required for Single Container Rooms. Multi Container Rooms determine ports from the orchestration configuration."}}, "required": []string{"room_id", "source"}}},
	{"name": "update_project", "description": "Update an existing project using a new complete source ZIP. The stored project source is replaced, then the project is rebuilt and redeployed while preserving persistent data and configuration where applicable.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"room_id": map[string]string{"type": "string", "description": "The target Room ID."}, "source": map[string]string{"type": "string", "description": "New complete project ZIP file."}}, "required": []string{"room_id", "source"}}},
	{"name": "get_deployment_status", "description": "Get the current deployment progress and status of a Room.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"room_id": map[string]string{"type": "string", "description": "The Room ID."}}, "required": []string{"room_id"}}},
	{"name": "manage_project_containers", "description": "Start, stop, or restart containers belonging to a Room. For Single Container Rooms the operation targets the single container. For Multi Container Rooms it can target a specific container or all containers.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"room_id": map[string]string{"type": "string", "description": "The Room ID."}, "action": map[string]any{"type": "string", "enum": []string{"start", "stop", "restart"}, "description": "Container action."}, "container_id": map[string]string{"type": "string", "description": "Optional container ID. In Multi Container Rooms, omit to target all containers."}}, "required": []string{"room_id", "action"}}},
	{"name": "get_project_volumes", "description": "Get all volumes associated with a Room, or only the volumes associated with a specific container. Returns volume names, usage, mount paths, ownership, and whether a volume is shared.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"room_id": map[string]string{"type": "string", "description": "The Room ID."}, "container_id": map[string]string{"type": "string", "description": "Optional container ID to filter volumes."}}, "required": []string{"room_id"}}},
	{"name": "delete_container_volumes", "description": "Permanently delete all persistent volumes associated with a specific container. The Agent must verify ownership and refuse unsafe deletion of shared volumes unless they are explicitly safe to delete.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"room_id": map[string]string{"type": "string", "description": "The Room ID."}, "container_id": map[string]string{"type": "string", "description": "The container whose associated volumes should be permanently deleted."}, "confirm": map[string]string{"type": "boolean", "description": "Explicit confirmation that all associated persistent volumes may be permanently deleted."}}, "required": []string{"room_id", "container_id", "confirm"}}},
	{"name": "delete_project_resource", "description": "Delete a Room or a specific container. Deleting a Room removes its containers and configuration but does not automatically delete persistent volumes. Volume deletion is handled separately.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"room_id": map[string]string{"type": "string", "description": "The Room ID."}, "target": map[string]any{"type": "string", "enum": []string{"room", "container"}, "description": "The resource to delete."}, "container_id": map[string]string{"type": "string", "description": "Required when target is container."}, "confirm": map[string]string{"type": "boolean", "description": "Explicit confirmation for the destructive operation."}}, "required": []string{"room_id", "target", "confirm"}}},
	{"name": "set_container_env", "description": "Set or update environment variables and secrets for a container/service in the Room environment file. Containers are recreated when restart is enabled so values apply. Secret values must not be exposed by read operations.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"room_id": map[string]string{"type": "string", "description": "The Room ID."}, "container_id": map[string]string{"type": "string", "description": "The target container or service ID."}, "variables": map[string]any{"type": "object", "description": "Environment variable names and values."}, "restart": map[string]any{"type": "boolean", "description": "Whether to restart/recreate the container or service after applying the environment changes.", "default": true}}, "required": []string{"room_id", "container_id", "variables"}}},
	{"name": "connect_domain", "description": "Connect a domain to a Room or a specific container/service through the reverse proxy. For Multi Container Rooms, container_id identifies the target service. Can enable HTTPS/SSL for the domain.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"room_id": map[string]string{"type": "string", "description": "The Room ID."}, "container_id": map[string]string{"type": "string", "description": "Target container/service. Required for Multi Container Rooms."}, "domain": map[string]string{"type": "string", "description": "Domain to connect."}, "ssl": map[string]any{"type": "boolean", "description": "Enable HTTPS/SSL for the domain.", "default": true}}, "required": []string{"room_id", "domain"}}},
	{"name": "get_project_logs", "description": "Get logs for a Room. For Single Container Rooms it returns the container logs. For Multi Container Rooms it can return logs for one container or aggregate logs for the entire Room.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"room_id": map[string]string{"type": "string", "description": "The Room ID."}, "container_id": map[string]string{"type": "string", "description": "Optional container ID. If omitted in Multi Container Rooms, logs from all containers may be returned."}, "lines": map[string]any{"type": "integer", "description": "Number of log lines to return.", "default": 200, "minimum": 1, "maximum": 5000}}, "required": []string{"room_id"}}},
	{"name": "get_vps_manager_logs", "description": "Get logs generated by the VPS Manager itself, including API errors, warnings, deployment events, and internal system events.", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
}

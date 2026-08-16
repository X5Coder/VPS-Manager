package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/x5coder/vps-rooms/internal/ai"
	"github.com/x5coder/vps-rooms/internal/stack"
	"github.com/x5coder/vps-rooms/internal/store"
)

func toolJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func (s *Server) handleAPIV1Agent(w http.ResponseWriter, r *http.Request, rest []string) {
	if s.requireAPIToken(w, r, r.Method != http.MethodGet) == nil {
		return
	}
	op := ""
	if len(rest) > 0 {
		op = rest[0]
	}
	if r.Method == http.MethodGet && op == "" {
		writeJSON(w, 200, map[string]any{"tools": agentToolNames(), "agent": "vps-manager"})
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	if op == "chat" {
		s.apiAgentChat(w, r)
		return
	}
	var body struct {
		Tool   string `json:"tool"`
		Arg    string `json:"arg"`
		RoomID string `json:"room_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Tool) == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "tool required", "code": "tool_required"})
		return
	}
	text, err := s.dispatchAgentTool("", body.Tool, body.Arg, body.RoomID, requestBaseURL(r))
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error(), "tool": body.Tool})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "tool": body.Tool, "text": text})
}

func agentToolNames() []string {
	return []string{
		"list_rooms", "get_room", "get_room_status", "get_room_resources", "create_room", "update_room",
		"list_containers", "get_container", "start_container", "stop_container", "restart_container", "inspect_container", "container_usage",
		"list_images", "get_image", "inspect_image",
		"list_volumes", "get_volume", "create_volume", "delete_volume", "clean_volume", "inspect_volume",
		"list_env", "get_env", "set_env", "delete_env",
		"get_logs", "clear_logs", "vps_logs",
		"exec_command", "docker_ps",
		"read_compose", "validate_compose", "analyze_services", "start_stack", "stop_stack", "restart_stack", "remove_stack",
		"get_vps_status", "get_cpu", "get_ram", "get_storage", "get_docker_status",
		"docs",
	}
}

func (s *Server) apiAgentChat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages []ai.Message `json:"messages"`
		RoomID   string       `json:"room_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) == 0 {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "messages required"})
		return
	}
	hist := append([]ai.Message{{Role: "system-note", Text: "VPS Manager central agent. Tools: " + strings.Join(agentToolNames(), ", ") + ". Return tool+tool_arg JSON when you need data."}}, body.Messages...)
	var last string
	for i := 0; i < 6; i++ {
		rep, _, err := ai.TurnWithTools(ai.ManagerPrompt, ai.ManagerTools, hist)
		if err != nil {
			writeJSON(w, 502, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if strings.TrimSpace(rep.Tool) == "" {
			writeJSON(w, 200, map[string]any{"ok": true, "say": rep.Say, "done": true})
			return
		}
		text, err := s.dispatchAgentTool("", rep.Tool, rep.ToolArg, body.RoomID, requestBaseURL(r))
		if err != nil {
			text = "TOOL ERROR: " + err.Error()
		}
		last = text
		hist = append(hist, ai.Message{Role: "assistant", Text: rep.Say}, ai.Message{Role: "terminal", Text: text})
	}
	writeJSON(w, 200, map[string]any{"ok": true, "say": last, "done": false})
}

func (s *Server) dispatchAgentTool(scope, tool, arg, roomID, base string) (string, error) {
	tool = strings.ToLower(strings.TrimSpace(tool))
	tool = strings.TrimPrefix(tool, "docs_")
	arg = strings.TrimSpace(arg)
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		roomID = arg
	}
	switch tool {
	case "list_projects", "list_rooms", "list":
		return s.toolListProjects(), nil
	case "project_detail", "get_room", "get_room_status":
		return s.toolProjectDetail(arg, roomID, scope)
	case "get_room_resources":
		id := firstNonEmpty(arg, roomID)
		return toolJSON(s.roomResourceUsage(id)), nil
	case "docker_ps":
		return s.toolDockerPS(), nil
	case "host_stats", "get_vps_status":
		return toolJSON(map[string]any{"note": s.usageSnapshot(), "status": "ok"}), nil
	case "get_cpu":
		m := s.Metrics.Snapshot()
		return fmt.Sprintf("cpu_percent=%.1f cores=%d load1=%.2f", m.CPUPercent, m.CPUCores, m.Load1), nil
	case "get_ram":
		m := s.Metrics.Snapshot()
		return fmt.Sprintf("ram_percent=%.1f used=%d total=%d", m.MemPercent, m.MemUsed, m.MemTotal), nil
	case "get_storage":
		return toolJSON(s.storageInfo()), nil
	case "get_docker_status":
		ok := s.Docker != nil && s.Docker.Available()
		return fmt.Sprintf("docker_ok=%v", ok), nil
	case "list_containers":
		id := firstNonEmpty(roomID, arg)
		return toolJSON(s.roomContainersJSON(id)), nil
	case "list_images":
		id := firstNonEmpty(roomID, arg)
		return toolJSON(s.roomImagesJSON(id)), nil
	case "list_volumes":
		id := firstNonEmpty(roomID, arg)
		return toolJSON(s.roomVolumesJSON(id)), nil
	case "list_env":
		id := firstNonEmpty(roomID, arg)
		text, err := s.readRoomEnv(id)
		if err != nil {
			return "", err
		}
		return maskEnvText(text), nil
	case "get_logs":
		id := firstNonEmpty(roomID, "")
		payload, code := s.containerLogsJSON(id, arg)
		if code >= 400 {
			return toolJSON(payload), fmt.Errorf("%v", payload["error"])
		}
		return fmt.Sprint(payload["log"]), nil
	case "vps_logs":
		_, text := s.hostLogBundle(arg)
		if arg == "" {
			_, text = s.hostLogBundle("vps")
		}
		return text, nil
	case "read_compose", "validate_compose", "analyze_services":
		id := firstNonEmpty(roomID, arg)
		info := stack.AnalyzeComposeDir(s.stackDir(id))
		return toolJSON(info), nil
	case "overview", "token", "github", "update", "create_room", "logs", "exec", "storage", "full", "docs":
		if tool == "storage" {
			tool = "exec"
		}
		if tool == "docs" {
			tool = "full"
		}
		if tool == "create_room" && arg != "" && !strings.Contains(strings.ToLower(arg), "how") {
			return "create_room via API: POST /api/v1/projects with name, quota_gb, password or generate_password, kind.", nil
		}
		return APIDocSection(tool, base), nil
	case "get_container", "inspect_container":
		ct := s.resolveRoomContainer(firstNonEmpty(roomID, ""), arg)
		if ct == nil {
			return "", fmt.Errorf("container not found")
		}
		if tool == "inspect_container" && s.Docker != nil && ct.DockerID != "" {
			b, err := s.Docker.InspectJSON(ct.DockerID)
			if err != nil {
				return "", err
			}
			return string(b), nil
		}
		return toolJSON(ct), nil
	case "container_usage":
		ct := s.resolveRoomContainer(roomID, arg)
		if ct == nil || s.Docker == nil {
			return "", fmt.Errorf("container not found")
		}
		cpu, mem, used, lim := s.Docker.ParseStats(ct.DockerID)
		return toolJSON(map[string]any{"cpu_percent": cpu, "ram_percent": mem, "ram_used": used, "ram_limit": lim}), nil
	case "start_container", "stop_container", "restart_container":
		ct := s.resolveRoomContainer(roomID, arg)
		if ct == nil || s.Docker == nil || ct.DockerID == "" {
			return "", fmt.Errorf("container not found")
		}
		var err error
		switch tool {
		case "start_container":
			err = s.Docker.Start(ct.DockerID)
		case "stop_container":
			err = s.Docker.Stop(ct.DockerID)
		default:
			err = s.Docker.Restart(ct.DockerID)
		}
		if err != nil {
			return "", err
		}
		st, _ := s.Docker.InspectStatus(ct.DockerID)
		return "status=" + st, nil
	case "create_volume":
		if roomID == "" || arg == "" {
			return "", fmt.Errorf("room_id and volume name required")
		}
		dname := "vpsrooms_" + store.ShortRoomID(roomID) + "_" + arg
		if s.Docker != nil {
			if err := s.Docker.CreateNamedVolume(dname); err != nil {
				return "", err
			}
		}
		rec := store.VolumeRec{ID: uuid.NewString(), RoomID: roomID, Name: arg, DockerName: dname}
		_ = s.Store.UpsertVolume(rec)
		return toolJSON(rec), nil
	case "clean_volume", "delete_volume", "get_volume", "inspect_volume":
		vol := s.resolveRoomVolume(roomID, arg)
		if vol == nil {
			return "", fmt.Errorf("volume not found")
		}
		if tool == "clean_volume" && s.Docker != nil {
			src := vol.DockerName
			if src == "" {
				src = vol.Name
			}
			if err := s.Docker.CleanVolume(src); err != nil {
				return "", err
			}
			return "cleaned " + vol.Name, nil
		}
		if tool == "delete_volume" {
			if s.Docker != nil && vol.DockerName != "" {
				if users := s.Docker.VolumeUsers(vol.DockerName); len(users) > 0 {
					return "", fmt.Errorf("volume in use")
				}
				_ = s.Docker.RemoveNamedVolume(vol.DockerName)
			}
			_ = s.Store.DeleteVolume(vol.ID)
			return "deleted " + vol.Name, nil
		}
		return toolJSON(s.volumeView(roomID, vol)), nil
	case "set_env":
		k, v, _ := strings.Cut(arg, "=")
		if err := s.envSetKeys(roomID, [][2]string{{strings.TrimSpace(k), v}}, false); err != nil {
			return "", err
		}
		return "set " + k, nil
	case "delete_env":
		if err := s.envDeleteKey(roomID, arg); err != nil {
			return "", err
		}
		return "deleted " + arg, nil
	case "clear_logs":
		ct := s.resolveRoomContainer(roomID, arg)
		if ct == nil || s.Docker == nil {
			return "", fmt.Errorf("container not found")
		}
		if err := s.Docker.TruncateLogs(ct.DockerID); err != nil {
			return "", err
		}
		return "cleared logs", nil
	case "exec_command":
		return "Use POST /api/v1/projects/{id}/exec {\"command\":...}", nil
	case "start_stack", "stop_stack", "restart_stack", "remove_stack":
		return "Use POST /api/v1/projects/{id}/stack/" + strings.TrimSuffix(tool, "_stack"), nil
	case "get_image", "inspect_image":
		return toolJSON(s.roomImagesJSON(roomID)), nil
	case "update_room":
		return "Use PATCH /api/v1/projects/{id}", nil
	default:
		return "", fmt.Errorf("unknown tool %q", tool)
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return strings.TrimSpace(b)
}

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func (s *Server) handleAgentTool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		Tool   string `json:"tool"`
		Arg    string `json:"arg"`
		Scope  string `json:"scope"`
		RoomID string `json:"room_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	tool := strings.ToLower(strings.TrimSpace(body.Tool))
	arg := strings.TrimSpace(body.Arg)
	scope := strings.ToLower(strings.TrimSpace(body.Scope))
	if tool == "" {
		writeErr(w, 400, "tool required")
		return
	}

	switch scope {
	case "room":
		_, room := s.roomAccess(w, r, strings.TrimSpace(body.RoomID))
		if room == nil {
			return
		}
		text, err := s.dispatchAgentTool(scope, tool, arg, room.ID, requestBaseURL(r))
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"text": text, "tool": tool})
		return
	default:
		if s.requireOwner(w, r) == nil {
			return
		}
	}
	text, err := s.dispatchAgentTool(scope, tool, arg, strings.TrimSpace(body.RoomID), requestBaseURL(r))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"text": text, "tool": tool})
}

func (s *Server) runAgentTool(scope, tool, arg, roomID, base string) (string, error) {
	return s.dispatchAgentTool(scope, tool, arg, roomID, base)
}

func (s *Server) toolListProjects() string {
	rooms, _ := s.Store.ListRooms()
	hm := s.Metrics.Snapshot()
	var b strings.Builder
	fmt.Fprintf(&b, "TOOL list_projects: %d room(s). Host CPU %.1f%% cores=%d load1=%.2f RAM %.1f%% disk %.1f%% (used %.2f GB / %.2f GB).\n",
		len(rooms), hm.CPUPercent, hm.CPUCores, hm.Load1, hm.MemPercent, hm.DiskPercent,
		float64(hm.DiskUsed)/(1024*1024*1024), float64(hm.DiskTotal)/(1024*1024*1024))
	if len(rooms) == 0 {
		b.WriteString("No projects yet.\n")
		return b.String()
	}
	for _, rm := range rooms {
		usage, _ := s.Rooms.UsageBytes(rm.ID)
		projs, _ := s.Store.ListProjects(rm.ID)
		stt := "empty"
		running := 0
		img := ""
		for _, p := range projs {
			img = p.Image
			if p.Status == "running" {
				running++
				stt = "running"
			} else if stt == "empty" {
				stt = p.Status
				if stt == "" {
					stt = "stopped"
				}
			}
		}
		if len(projs) > 0 && running == 0 && stt == "empty" {
			stt = "stopped"
		}
		qGB := float64(rm.QuotaBytes) / (1024 * 1024 * 1024)
		uGB := float64(usage) / (1024 * 1024 * 1024)
		pct := 0.0
		if rm.QuotaBytes > 0 {
			pct = 100 * uGB / qGB
		}
		fmt.Fprintf(&b, "- name=%q id=%s kind=%s status=%s disk_used=%.2fGB quota=%.2fGB (%.0f%% of quota) running=%d image=%q\n",
			rm.Name, rm.ID, rm.Kind, stt, uGB, qGB, pct, running, img)
	}
	return b.String()
}

func (s *Server) toolProjectDetail(arg, roomID, scope string) (string, error) {
	want := strings.TrimSpace(arg)
	if scope == "room" && roomID != "" {
		want = roomID
	}
	rooms, _ := s.Store.ListRooms()
	if len(rooms) == 0 {
		return "TOOL project_detail: no rooms.", nil
	}
	type hit struct {
		name string
		id   string
	}
	var match *hit
	for i := range rooms {
		rm := rooms[i]
		if want == "" && roomID != "" && rm.ID == roomID {
			match = &hit{rm.Name, rm.ID}
			break
		}
		if want == "" {
			continue
		}
		if strings.EqualFold(rm.ID, want) || strings.EqualFold(rm.Name, want) || strings.Contains(strings.ToLower(rm.Name), strings.ToLower(want)) {
			match = &hit{rm.Name, rm.ID}
			break
		}
		projs, _ := s.Store.ListProjects(rm.ID)
		for _, p := range projs {
			if strings.EqualFold(p.ID, want) || strings.EqualFold(p.Name, want) {
				match = &hit{rm.Name, rm.ID}
				break
			}
		}
		if match != nil {
			break
		}
	}
	if match == nil {
		if want == "" {
			return "", fmt.Errorf("project_detail needs a room name or id")
		}
		return fmt.Sprintf("TOOL project_detail: no room matching %q. Use list_projects.", want), nil
	}
	if scope == "room" && roomID != "" && match.id != roomID {
		return "TOOL project_detail: that id is not this room.", nil
	}
	rm, err := s.Store.GetRoom(match.id)
	if err != nil || rm == nil {
		return "TOOL project_detail: room missing.", nil
	}
	usage, _ := s.Rooms.UsageBytes(rm.ID)
	projs, _ := s.Store.ListProjects(rm.ID)
	hm := s.Metrics.Snapshot()
	st := s.storageInfo()
	var b strings.Builder
	fmt.Fprintf(&b, "TOOL project_detail name=%q id=%s quota=%.2fGB used=%.2fGB\n",
		rm.Name, rm.ID, float64(rm.QuotaBytes)/(1024*1024*1024), float64(usage)/(1024*1024*1024))
	if len(projs) == 0 {
		b.WriteString("status=empty (no container yet)\n")
	}
	for _, p := range projs {
		fmt.Fprintf(&b, "project_id=%s image=%s status=%s host_port=%d container_port=%d domain=%q\n",
			p.ID, p.Image, p.Status, p.HostPort, p.ContainerPort, p.Domain)
	}
	fmt.Fprintf(&b, "HOST compare: CPU %.1f%% cores=%d load1=%.2f RAM %.1f%% disk %.1f%% free=%v quota_available_gb=%v\n",
		hm.CPUPercent, hm.CPUCores, hm.Load1, hm.MemPercent, hm.DiskPercent, st["disk_free"], st["quota_available_gb"])
	if hm.DiskTotal > 0 {
		fmt.Fprintf(&b, "This room disk is %.1f%% of host disk used.\n", 100*float64(usage)/float64(hm.DiskUsed+1))
	}
	return b.String(), nil
}

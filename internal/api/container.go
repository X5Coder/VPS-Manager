package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/x5coder/vps-rooms/internal/dockerx"
	"github.com/x5coder/vps-rooms/internal/store"
)

func (s *Server) handleRoomContainer(w http.ResponseWriter, r *http.Request, roomID string, rest []string) {
	if _, room := s.roomAccess(w, r, roomID); room == nil {
		return
	}
	if len(rest) == 0 {
		writeJSON(w, 200, map[string]any{"containers": s.roomContainersJSON(roomID)})
		return
	}
	ct := s.resolveRoomContainer(roomID, rest[0])
	if ct == nil {
		writeErr(w, 404, "container not found")
		return
	}
	if len(rest) == 1 {
		writeJSON(w, 200, map[string]any{
			"id": ct.ID, "ordinal": ct.Ordinal, "name": ct.Name, "service": ct.Service,
			"label": inventoryLabel(ct), "image": ct.Image, "docker_id": ct.DockerID,
			"status": ct.Status, "host_port": ct.HostPort,
		})
		return
	}
	switch rest[1] {
	case "files":
		s.handleContainerFiles(w, r, roomID, ct)
	case "logs":
		q := r.URL.Query()
		q.Set("container", ct.ID)
		r.URL.RawQuery = q.Encode()
		s.handleRoomLogs(w, r, roomID)
	default:
		writeErr(w, 404, "not found")
	}
}

func inventoryLabel(c *store.Container) string {
	if c == nil {
		return "container"
	}
	s := strings.TrimSpace(c.Service)
	if s == "" {
		s = c.Name
	}
	return s
}

func (s *Server) handleContainerFiles(w http.ResponseWriter, r *http.Request, roomID string, ct *store.Container) {
	if s.Docker == nil || ct.DockerID == "" {
		writeErr(w, 400, "container is not running")
		return
	}
	st, _ := s.Docker.InspectStatus(ct.DockerID)
	if st != "running" {
		writeErr(w, 400, "container is stopped — resume the room first")
		return
	}
	rel := dockerx.CleanContainerPath(r.URL.Query().Get("path"))
	switch r.Method {
	case http.MethodGet:
		ents, err := s.Docker.ListContainerFiles(ct.DockerID, rel)
		if err == nil {
			out := make([]map[string]any, 0, len(ents))
			for _, e := range ents {
				out = append(out, map[string]any{"name": e.Name, "dir": e.Dir, "size": e.Size})
			}
			writeJSON(w, 200, map[string]any{"path": rel, "entries": out})
			return
		}
		b, err := s.Docker.ReadContainerFile(ct.DockerID, rel)
		if err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		if len(b) > 2<<20 {
			writeJSON(w, 200, map[string]any{"path": rel, "size": len(b), "binary": true, "note": "File too large to edit"})
			return
		}
		if !dockerx.LooksText(b) {
			writeJSON(w, 200, map[string]any{"path": rel, "size": len(b), "binary": true, "note": "Binary file"})
			return
		}
		writeJSON(w, 200, map[string]any{"path": rel, "content": string(b), "size": len(b), "binary": false})
	case http.MethodPut:
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "invalid request")
			return
		}
		if err := s.Docker.WriteContainerFile(ct.DockerID, rel, body.Content); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"ok": "1"})
	case http.MethodDelete:
		if err := s.Docker.RemoveContainerFile(ct.DockerID, rel); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"ok": "1"})
	default:
		writeErr(w, 405, "method")
	}
}

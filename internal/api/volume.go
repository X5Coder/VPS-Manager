package api

import (
	"net/http"
	"strings"

	"github.com/x5coder/vps-rooms/internal/dockerx"
	"github.com/x5coder/vps-rooms/internal/store"
)

func (s *Server) handleRoomVolume(w http.ResponseWriter, r *http.Request, roomID string, rest []string) {
	if _, room := s.roomAccess(w, r, roomID); room == nil {
		return
	}
	if len(rest) == 0 {
		writeJSON(w, 200, map[string]any{"volumes": s.roomVolumesJSON(roomID)})
		return
	}
	vol := s.resolveRoomVolume(roomID, rest[0])
	if vol == nil {
		writeErr(w, 404, "volume not found")
		return
	}
	if len(rest) == 1 {
		writeJSON(w, 200, map[string]any{
			"id": vol.ID, "ordinal": vol.Ordinal, "name": vol.Name,
			"docker_name": vol.DockerName, "size_bytes": vol.SizeBytes,
		})
		return
	}
	if rest[1] != "files" {
		writeErr(w, 404, "not found")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method")
		return
	}
	rel := dockerx.CleanContainerPath(r.URL.Query().Get("path"))
	src := strings.TrimSpace(vol.DockerName)
	if src == "" {
		src = vol.Name
	}
	if src == "" {
		writeErr(w, 400, "volume has no docker name")
		return
	}
	if strings.HasPrefix(src, "/") {
		ents, err := dockerx.ListHostFiles(src, rel)
		if err == nil {
			writeJSON(w, 200, map[string]any{"path": rel, "entries": entsJSON(ents)})
			return
		}
		b, err := dockerx.ReadHostFile(src, rel)
		s.writeFilePayload(w, rel, b, err)
		return
	}
	if s.Docker == nil || !s.Docker.Available() {
		writeErr(w, 400, "Docker unavailable")
		return
	}
	ents, err := s.Docker.ListVolumeFiles(src, rel)
	if err == nil {
		writeJSON(w, 200, map[string]any{"path": rel, "entries": entsJSON(ents)})
		return
	}
	b, err := s.Docker.ReadVolumeFile(src, rel)
	s.writeFilePayload(w, rel, b, err)
}

func entsJSON(ents []dockerx.FSEntry) []map[string]any {
	out := make([]map[string]any, 0, len(ents))
	for _, e := range ents {
		out = append(out, map[string]any{"name": e.Name, "dir": e.Dir, "size": e.Size})
	}
	return out
}

func (s *Server) writeFilePayload(w http.ResponseWriter, rel string, b []byte, err error) {
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	if len(b) > 2<<20 {
		writeJSON(w, 200, map[string]any{"path": rel, "size": len(b), "binary": true, "note": "File too large to open"})
		return
	}
	if !dockerx.LooksText(b) {
		writeJSON(w, 200, map[string]any{"path": rel, "size": len(b), "binary": true, "note": "Binary file"})
		return
	}
	writeJSON(w, 200, map[string]any{"path": rel, "content": string(b), "size": len(b), "binary": false})
}

func (s *Server) resolveRoomVolume(roomID, want string) *store.VolumeRec {
	list, _ := s.Store.ListVolumes(roomID)
	want = strings.TrimSpace(want)
	low := strings.ToLower(want)
	for i := range list {
		v := list[i]
		if v.ID == want || strings.EqualFold(v.Name, want) || strings.EqualFold(v.DockerName, want) {
			return &v
		}
		if low != "" && (strings.EqualFold(v.ID, want) || strings.Contains(strings.ToLower(v.DockerName), low)) {
			return &v
		}
	}
	return nil
}

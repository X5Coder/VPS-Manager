package api

import (
	"net/http"
	"os"
	"path/filepath"
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
	if len(rest) == 1 && rest[0] == "wipe-all" {
		s.handleRoomVolumesWipeAll(w, r, roomID)
		return
	}
	vol := s.resolveRoomVolume(roomID, rest[0])
	if vol == nil {
		writeErr(w, 404, "volume not found")
		return
	}
	if len(rest) == 1 {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, 200, map[string]any{
				"id": vol.ID, "ordinal": vol.Ordinal, "name": vol.Name,
				"docker_name": vol.DockerName, "size_bytes": vol.SizeBytes,
			})
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
		src := strings.TrimSpace(vol.DockerName)
		if src == "" {
			src = vol.Name
		}
		if strings.HasPrefix(src, "/") {
			if err := wipeHostDirContents(src); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
		} else {
			if s.Docker == nil || !s.Docker.Available() {
				writeErr(w, 400, "Docker unavailable")
				return
			}
			if err := s.Docker.CleanVolume(src); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
		}
		writeJSON(w, 200, map[string]any{"ok": "1", "cleaned": true, "id": vol.ID})
		return
	case "files":
		if r.Method != http.MethodGet {
			writeErr(w, 405, "method")
			return
		}
	default:
		writeErr(w, 404, "not found")
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
	// The panel-managed room env is edited in the Secrets tab — never
	// expose or edit .env inside volume browsing.
	if rel == ".env" || strings.HasSuffix(rel, "/.env") || strings.HasSuffix(rel, ".env") {
		writeJSON(w, 200, map[string]any{"path": rel, "size": 0, "binary": true, "note": "Room env is managed in the Secrets tab"})
		return
	}
	if strings.HasPrefix(src, "/") {
		ents, err := dockerx.ListHostFiles(src, rel)
		if err == nil {
			kept := make([]dockerx.FSEntry, 0, len(ents))
			for _, e := range ents {
				// Hide .env files completely from volume browsing
				lowerName := strings.ToLower(e.Name)
				if lowerName == ".env" || strings.HasSuffix(lowerName, ".env") {
					continue
				}
				kept = append(kept, e)
			}
			writeJSON(w, 200, map[string]any{"path": rel, "entries": entsJSON(kept)})
			return
		}
		b, err := dockerx.ReadHostFile(src, rel)
		// Also hide .env when reading individual files
		if strings.ToLower(strings.TrimSpace(rel)) == ".env" || strings.HasSuffix(strings.ToLower(strings.TrimSpace(rel)), ".env") {
			writeJSON(w, 200, map[string]any{"path": rel, "size": 0, "binary": true, "note": "Room env is managed in the Secrets tab"})
			return
		}
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
	// Also hide .env when reading individual files from Docker volumes
	if strings.ToLower(strings.TrimSpace(rel)) == ".env" || strings.HasSuffix(strings.ToLower(strings.TrimSpace(rel)), ".env") {
		writeJSON(w, 200, map[string]any{"path": rel, "size": 0, "binary": true, "note": "Room env is managed in the Secrets tab"})
		return
	}
	s.writeFilePayload(w, rel, b, err)
}

func entsJSON(ents []dockerx.FSEntry) []map[string]any {
	out := make([]map[string]any, 0, len(ents))
	for _, e := range ents {
		// Hide .env files completely from volume browsing
		lowerName := strings.ToLower(e.Name)
		if lowerName == ".env" || strings.HasSuffix(lowerName, ".env") {
			continue
		}
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

// handleRoomVolumesWipeAll wipes the contents of every tracked room volume
// but never touches .env files, records, mounts, or directories. Containers
// are then restarted fast (Docker Restart, no rebuild) so empty binds are
// live immediately.
func (s *Server) handleRoomVolumesWipeAll(w http.ResponseWriter, r *http.Request, roomID string) {
	if _, room := s.canControlRoom(w, r, roomID); room == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	dockerOK := s.Docker != nil && s.Docker.Available()
	volRoot := s.Rooms.RoomVolumesDir(roomID)
	vols, _ := s.Store.ListVolumes(roomID)
	wiped := []string{}
	skipped := []string{}
	for _, v := range vols {
		hostDir := filepath.Join(volRoot, v.Name)
		if st, err := os.Stat(hostDir); err == nil && st.IsDir() {
			ents, err := os.ReadDir(hostDir)
			if err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			for _, e := range ents {
				if e.Name() == ".env" || strings.HasSuffix(e.Name(), ".env") {
					continue // NEVER delete .env
				}
				if err := os.RemoveAll(filepath.Join(hostDir, e.Name())); err != nil {
					writeErr(w, 400, err.Error())
					return
				}
			}
			wiped = append(wiped, v.Name)
			continue
		}
		if v.DockerName != "" && !strings.HasPrefix(v.DockerName, "/") {
			if !dockerOK {
				skipped = append(skipped, v.Name)
				continue
			}
			if err := s.Docker.CleanVolume(v.DockerName); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			wiped = append(wiped, v.Name)
		}
	}
	restarted := []string{}
	restartErrors := []string{}
	if dockerOK {
		seen := map[string]struct{}{}
		ids := []string{}
		if cts, _ := s.Store.ListContainers(roomID); len(cts) > 0 {
			for _, c := range cts {
				if c.DockerID != "" {
					ids = append(ids, c.DockerID)
				}
			}
		}
		if projs, _ := s.Store.ListProjects(roomID); len(projs) > 0 {
			for _, p := range projs {
				if p.ContainerID != "" {
					ids = append(ids, p.ContainerID)
				}
			}
		}
		for _, id := range ids {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			if err := s.Docker.Restart(id); err != nil {
				restartErrors = append(restartErrors, id)
				continue
			}
			restarted = append(restarted, id)
		}
	}
	res := map[string]any{
		"room_id": roomID, "wiped": wiped, "restarted": restarted,
		"restart_errors": restartErrors, "docker_available": dockerOK,
	}
	if len(skipped) > 0 {
		res["skipped_no_docker"] = skipped
	}
	if !dockerOK {
		res["note"] = "Docker unavailable — host volume contents wiped (.env kept); restart on a Docker host"
	}
	writeJSON(w, 200, res)
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

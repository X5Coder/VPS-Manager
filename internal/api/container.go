package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/x5coder/vps-rooms/internal/dockerx"
	"github.com/x5coder/vps-rooms/internal/projects"
	"github.com/x5coder/vps-rooms/internal/stack"
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
	case "image-tar":
		s.handleContainerImageTar(w, r, roomID, ct)
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

func (s *Server) handleContainerImageTar(w http.ResponseWriter, r *http.Request, roomID string, ct *store.Container) {
	if _, room := s.canControlRoom(w, r, roomID); room == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	flusher, _ := w.(http.Flusher)
	logw := &flushWriter{w: w, f: flusher}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<30)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		fmt.Fprintf(logw, "error: %v\n", err)
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		fmt.Fprintf(logw, "error: upload a docker save .tar for this container\n")
		return
	}
	defer file.Close()
	fname := "app.tar"
	if hdr != nil && hdr.Filename != "" {
		fname = hdr.Filename
	}
	tmp, err := os.MkdirTemp("", "vm-ctr-*")
	if err != nil {
		fmt.Fprintf(logw, "error: %v\n", err)
		return
	}
	defer os.RemoveAll(tmp)
	dest := filepath.Join(tmp, fname)
	out, err := os.Create(dest)
	if err != nil {
		fmt.Fprintf(logw, "error: %v\n", err)
		return
	}
	n, err := io.Copy(out, file)
	out.Close()
	if err != nil {
		fmt.Fprintf(logw, "error: %v\n", err)
		return
	}
	fmt.Fprintf(logw, "Received %s (%d bytes). Other containers stay running.\n", fname, n)
	room, _ := s.Store.GetRoom(roomID)
	if err := s.applyImageTarOneContainer(room, ct, dest, logw); err != nil {
		fmt.Fprintf(logw, "error: %v\n", err)
		return
	}
	fmt.Fprintf(logw, "Updated container %s. The rest of the room is unchanged.\n", ct.Name)
}

func (s *Server) applyImageTarOneContainer(room *store.Room, ct *store.Container, tarPath string, logw io.Writer) error {
	if s.Docker == nil || !s.Docker.Available() {
		return fmt.Errorf("Docker unavailable")
	}
	if logw == nil {
		logw = io.Discard
	}
	fmt.Fprintf(logw, "Loading image (this container only)...\n")
	loaded, err := s.Docker.LoadImageTag(tarPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(logw, "Loaded %s\n", loaded)
	want := strings.TrimSpace(ct.Image)
	if want == "" {
		want = loaded
	} else if loaded != "" && loaded != want {
		_ = s.Docker.Tag(loaded, want)
		fmt.Fprintf(logw, "Tagged %s → %s\n", loaded, want)
	}
	svc := strings.TrimSpace(ct.Service)
	if svc != "" && svc != ct.Name {
		projs, _ := s.Store.ListProjects(room.ID)
		for _, p := range projs {
			_, composeDir, composeProject, _ := projects.ProjectLayout(s.Rooms.ProjectDir(room.ID, p.ID))
			dir := composeDir
			if dir == "" {
				dir = s.Rooms.ProjectDir(room.ID, p.ID)
			}
			if dockerx.ComposeFile(dir) == "" {
				continue
			}
			proj := composeProject
			if proj == "" {
				proj = "vr" + store.ShortRoomID(room.ID)
			}
			fmt.Fprintf(logw, "Restarting service %s only (compose --no-deps)...\n", svc)
			if err := s.Docker.ComposeUpService(dir, proj, svc, logw); err != nil {
				return err
			}
			ct.Image = want
			ct.Status = "running"
			_ = s.Store.UpsertContainer(*ct)
			return nil
		}
	}
	if p, _ := s.Store.GetProject(ct.ID); p != nil && p.RoomID == room.ID {
		return s.Projects.RedeployImage(projects.RedeployInput{
			ID: p.ID, Image: want, Pull: false, Recreate: true, Log: logw,
		})
	}
	fmt.Fprintf(logw, "Recreating this container with the new image...\n")
	newID, err := s.Docker.RecreateWithImage(ct.DockerID, want, room.NetworkName)
	if err != nil {
		return err
	}
	ct.DockerID = newID
	ct.Image = want
	ct.Status = "running"
	return s.Store.UpsertContainer(*ct)
}

func (s *Server) applyUploadedPackage(room *store.Room, p *store.Project, dest, fname string, containerID string, logw io.Writer) error {
	if s.Stack != nil && (stack.LooksLikeMultiPackage(fname) || stack.ArchiveHasCompose(dest)) {
		fmt.Fprintf(logw, "Multi package — other rooms are not touched.\n")
		return s.Stack.DeployMulti(room, dest, logw)
	}
	if containerID != "" {
		ct := s.resolveRoomContainer(room.ID, containerID)
		if ct == nil {
			return fmt.Errorf("container not found")
		}
		return s.applyImageTarOneContainer(room, ct, dest, logw)
	}
	_, err := s.applyImageTarRoom(room, p, dest, logw)
	return err
}

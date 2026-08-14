package api

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/x5coder/vps-rooms/internal/projects"
	"github.com/x5coder/vps-rooms/internal/store"
)

func (s *Server) anyJob() bool {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	return len(s.jobs) > 0
}

func (s *Server) jobKind(projectID string) string {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.jobs == nil {
		return ""
	}
	return s.jobs[projectID]
}

func (s *Server) tryBeginJob(projectID, kind string) error {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.jobs == nil {
		s.jobs = map[string]string{}
	}
	if cur := s.jobs[projectID]; cur != "" {
		return fmt.Errorf("%s already in progress for this project", cur)
	}
	s.jobs[projectID] = kind
	return nil
}

func (s *Server) endJob(projectID string) {
	s.jobsMu.Lock()
	delete(s.jobs, projectID)
	s.jobsMu.Unlock()
}

func (s *Server) acceptedProject(w http.ResponseWriter, roomID string, extra map[string]any) {
	room, p, _ := s.resolveRoomProject(roomID)
	out := map[string]any{"ok": true, "accepted": true}
	for k, v := range extra {
		out[k] = v
	}
	if room != nil {
		view := s.projectView(room, p)
		if st, _ := extra["status"].(string); st != "" {
			view["status"] = st
		}
		out["project"] = view
		if st, ok := view["status"]; ok && out["status"] == nil {
			out["status"] = st
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) startRedeployAsync(p *store.Project, image string, pull, recreate bool) error {
	if p == nil {
		return fmt.Errorf("project has no container")
	}
	image = strings.TrimSpace(image)
	if image == "" {
		image = strings.TrimSpace(p.Image)
	}
	if image == "" {
		return fmt.Errorf("image required")
	}
	if err := s.tryBeginJob(p.ID, "deploy"); err != nil {
		return err
	}
	s.Projects.MarkDeploying(p.RoomID, p.ID, image, "deploy")
	go func(p store.Project, image string, pull, recreate bool) {
		defer s.endJob(p.ID)
		if err := s.apiDoRedeploy(&p, image, pull, recreate); err != nil {
			log.Printf("redeploy %s: %v", p.ID, err)
		}
	}(*p, image, pull, recreate)
	return nil
}

func (s *Server) startBuildAsync(p *store.Project, in projects.BuildImageInput, deploy, pull, recreate bool) error {
	key := "_image_build"
	roomID := ""
	if p != nil {
		key = p.ID
		roomID = p.RoomID
	}
	if err := s.tryBeginJob(key, "build"); err != nil {
		return err
	}
	if p != nil {
		s.Projects.MarkDeploying(roomID, p.ID, in.Image, "build")
	}
	var pCopy *store.Project
	if p != nil {
		cp := *p
		pCopy = &cp
	}
	go func(proj *store.Project, in projects.BuildImageInput, deploy, pull, recreate bool) {
		defer s.endJob(key)
		built, err := s.Projects.BuildImage(in)
		if err != nil {
			log.Printf("build %s: %v", key, err)
			if proj != nil {
				s.Projects.MarkDeployResult(proj.RoomID, proj.ID, in.Image, "", false, err.Error())
			}
			return
		}
		if deploy && proj != nil {
			s.Projects.MarkDeploying(proj.RoomID, proj.ID, built, "deploy")
			if err := s.apiDoRedeploy(proj, built, pull, recreate); err != nil {
				log.Printf("build-deploy %s: %v", proj.ID, err)
			}
			return
		}
		if proj != nil {
			s.Projects.MarkDeployResult(proj.RoomID, proj.ID, built, s.dockerDigest(built), true, "")
		}
	}(pCopy, in, deploy, pull, recreate)
	return nil
}

func (s *Server) dockerDigest(image string) string {
	if s.Docker == nil {
		return ""
	}
	d := s.Docker.RepoDigest(image)
	if d == "" {
		d = s.Docker.ImageID(image)
	}
	return d
}

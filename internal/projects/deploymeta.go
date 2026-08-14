package projects

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type DeployMeta struct {
	Image           string `json:"image,omitempty"`
	ImageDigest     string `json:"image_digest,omitempty"`
	LastDeployAt    string `json:"last_deploy_at,omitempty"`
	LastDeployOK    bool   `json:"last_deploy_ok"`
	LastDeployError string `json:"last_deploy_error,omitempty"`
	Status          string `json:"status,omitempty"` // deploying | building | running | error
	Job             string `json:"job,omitempty"`    // deploy | build
}

func (s *Service) deployMetaPath(roomID, projectID string) string {
	return filepath.Join(s.Rooms.ProjectDir(roomID, projectID), "__deploy.json")
}

func (s *Service) ReadDeployMeta(roomID, projectID string) DeployMeta {
	b, err := os.ReadFile(s.deployMetaPath(roomID, projectID))
	if err != nil {
		return DeployMeta{}
	}
	var m DeployMeta
	_ = json.Unmarshal(b, &m)
	return m
}

func (s *Service) writeDeployMeta(roomID, projectID string, m DeployMeta) {
	_ = os.MkdirAll(s.Rooms.ProjectDir(roomID, projectID), 0o700)
	b, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(s.deployMetaPath(roomID, projectID), b, 0o600)
}

func (s *Service) MarkDeploying(roomID, projectID, image, job string) {
	s.markDeploying(roomID, projectID, image, job)
}

func (s *Service) markDeploying(roomID, projectID, image string, job ...string) {
	m := s.ReadDeployMeta(roomID, projectID)
	kind := "deploy"
	if len(job) > 0 && strings.TrimSpace(job[0]) != "" {
		kind = strings.TrimSpace(job[0])
	}
	if kind == "build" {
		m.Status = "building"
	} else {
		m.Status = "deploying"
	}
	m.Job = kind
	m.Image = image
	m.LastDeployError = ""
	s.writeDeployMeta(roomID, projectID, m)
	if p, err := s.Store.GetProject(projectID); err == nil && p != nil {
		p.Status = m.Status
		_ = s.Store.UpdateProject(*p)
	}
}

func (s *Service) MarkDeployResult(roomID, projectID, image, digest string, ok bool, errMsg string) {
	s.markDeployResult(roomID, projectID, image, digest, ok, errMsg)
}

func (s *Service) markDeployResult(roomID, projectID, image, digest string, ok bool, errMsg string) {
	errMsg = strings.TrimSpace(errMsg)
	if len(errMsg) > 4000 {
		errMsg = "…" + errMsg[len(errMsg)-4000:]
	}
	m := DeployMeta{
		Image:           image,
		ImageDigest:     digest,
		LastDeployAt:    time.Now().UTC().Format(time.RFC3339),
		LastDeployOK:    ok,
		LastDeployError: errMsg,
		Status:          "running",
	}
	if !ok {
		m.Status = "error"
	}
	s.writeDeployMeta(roomID, projectID, m)
}

package projects

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

type UpdateEvent struct {
	N     int    `json:"n"`
	At    string `json:"at"`
	OK    bool   `json:"ok"`
	Image string `json:"image,omitempty"`
}

const maxUpdateHistory = 5

var updateHistMu sync.Mutex

func (s *Service) deployMetaPath(roomID, projectID string) string {
	return filepath.Join(s.Rooms.ProjectDir(roomID, projectID), "__deploy.json")
}

func (s *Service) updateHistoryPath(roomID string) string {
	return filepath.Join(s.Rooms.Dir(roomID), "update_history.json")
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

func (s *Service) loadUpdateHistory(roomID string) []UpdateEvent {
	b, err := os.ReadFile(s.updateHistoryPath(roomID))
	if err != nil {
		return nil
	}
	var list []UpdateEvent
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	return list
}

func (s *Service) writeUpdateHistory(roomID string, list []UpdateEvent) {
	_ = os.MkdirAll(s.Rooms.Dir(roomID), 0o700)
	b, _ := json.MarshalIndent(list, "", "  ")
	_ = os.WriteFile(s.updateHistoryPath(roomID), b, 0o600)
}

func (s *Service) seedUpdateHistoryLocked(roomID string) []UpdateEvent {
	list := compactUpdateHistory(s.loadUpdateHistory(roomID))
	if len(list) > 0 {
		return list
	}
	projs, err := s.Store.ListProjects(roomID)
	if err != nil || len(projs) == 0 {
		return nil
	}
	m := s.ReadDeployMeta(roomID, projs[0].ID)
	if strings.TrimSpace(m.LastDeployAt) == "" || !m.LastDeployOK {
		return nil
	}
	list = []UpdateEvent{{N: 1, At: m.LastDeployAt, OK: true, Image: m.Image}}
	s.writeUpdateHistory(roomID, list)
	return list
}

func sameUpdate(a, b UpdateEvent) bool {
	if a.Image != b.Image || !a.OK || !b.OK {
		return false
	}
	ta, ea := time.Parse(time.RFC3339, a.At)
	tb, eb := time.Parse(time.RFC3339, b.At)
	if ea != nil || eb != nil {
		return a.At == b.At
	}
	d := ta.Sub(tb)
	if d < 0 {
		d = -d
	}
	return d <= 3*time.Second
}

func compactUpdateHistory(list []UpdateEvent) []UpdateEvent {
	if len(list) == 0 {
		return list
	}
	out := make([]UpdateEvent, 0, len(list))
	for _, ev := range list {
		if len(out) > 0 && sameUpdate(out[len(out)-1], ev) {
			continue
		}
		out = append(out, ev)
	}
	if len(out) > maxUpdateHistory {
		out = out[:maxUpdateHistory]
	}
	return out
}

func (s *Service) ReadUpdateHistory(roomID string) []UpdateEvent {
	updateHistMu.Lock()
	defer updateHistMu.Unlock()
	list := s.seedUpdateHistoryLocked(roomID)
	compacted := compactUpdateHistory(list)
	if len(compacted) != len(list) {
		s.writeUpdateHistory(roomID, compacted)
	} else if len(compacted) > 0 && compacted[0].N != list[0].N {
		s.writeUpdateHistory(roomID, compacted)
	}
	return compacted
}

func (s *Service) AppendUpdateHistory(roomID, image string) UpdateEvent {
	updateHistMu.Lock()
	defer updateHistMu.Unlock()
	list := compactUpdateHistory(s.loadUpdateHistory(roomID))
	ev := UpdateEvent{
		N:     1,
		At:    time.Now().UTC().Format(time.RFC3339),
		OK:    true,
		Image: strings.TrimSpace(image),
	}
	if len(list) > 0 && sameUpdate(list[0], ev) {
		return list[0]
	}
	if len(list) > 0 {
		ev.N = list[0].N + 1
	}
	list = compactUpdateHistory(append([]UpdateEvent{ev}, list...))
	s.writeUpdateHistory(roomID, list)
	return ev
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
	if ok {
		s.AppendUpdateHistory(roomID, image)
	}
}

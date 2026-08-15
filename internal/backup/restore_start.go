package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/x5coder/vps-rooms/internal/dockerx"
	"github.com/x5coder/vps-rooms/internal/projects"
	"github.com/x5coder/vps-rooms/internal/stack"
	"github.com/x5coder/vps-rooms/internal/store"
)

// restoreComposePull is always false: restore must start from docker load, never a registry.
const restoreComposePull = false

func restoreComposePullEnabled() bool { return restoreComposePull }

func (s *Service) startRestoredWorkloads() error {
	if s.Store == nil {
		return nil
	}
	rooms, err := s.Store.ListRooms()
	if err != nil {
		return err
	}
	for _, rm := range rooms {
		if err := s.startRestoredRoom(rm); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) startRestoredRoom(rm store.Room) error {
	if s.Rooms != nil {
		_ = s.Rooms.EnsureUnlocked(rm.ID)
	}
	if s.Docker == nil || !s.Docker.Available() {
		return nil
	}
	network := strings.TrimSpace(rm.NetworkName)
	if network != "" {
		if err := s.Docker.EnsureNetwork(network); err != nil {
			return fmt.Errorf("network %s: %w", network, err)
		}
	}

	backupDir := filepath.Join(s.RuntimeDir, rm.ID, "backup")
	configs, _ := filepath.Glob(filepath.Join(backupDir, "*config.json"))
	startedInspect := 0
	if len(configs) > 0 {
		n, err := s.startFromInspectFiles(rm, network, configs)
		if err != nil {
			s.report(-1, "inspect create %s: %v — falling back to compose/redeploy", rm.Name, err)
		} else {
			startedInspect = n
		}
	}

	if startedInspect == 0 {
		if err := s.startRoomComposeOrRedeploy(rm); err != nil {
			return err
		}
	}

	s.refreshRoomDockerIDs(rm)
	s.overlayContainerRW(rm, backupDir)

	if s.Projects != nil && s.Rooms != nil {
		projs, _ := s.Store.ListProjects(rm.ID)
		composeProject := stack.ComposeProject(rm.ID)
		for _, p := range projs {
			_, _, cp, _ := projects.ProjectLayout(s.Rooms.ProjectDir(rm.ID, p.ID))
			if cp != "" {
				composeProject = cp
			}
			s.waitAndRestorePostgres(p, composeProject)
		}
	}
	return nil
}

func (s *Service) startFromInspectFiles(rm store.Room, network string, configs []string) (int, error) {
	n := 0
	for _, cfg := range configs {
		raw, err := os.ReadFile(cfg)
		if err != nil {
			return n, err
		}
		id, err := s.Docker.CreateFromInspect(raw, network)
		if err != nil {
			return n, err
		}
		if err := s.Docker.Start(id); err != nil {
			return n, fmt.Errorf("start %s: %w", filepath.Base(cfg), err)
		}
		n++
		s.report(-1, "Started %s from inspect", filepath.Base(cfg))
	}
	return n, nil
}

func (s *Service) startRoomComposeOrRedeploy(rm store.Room) error {
	composeDir, composeProject := s.findRestoredCompose(rm)
	if composeDir != "" {
		s.report(-1, "Compose %s: start with --pull never", rm.Name)
		if err := s.Docker.ComposeUp(composeDir, composeProject, restoreComposePull, nil); err != nil {
			return fmt.Errorf("compose %s: %w", rm.Name, err)
		}
		s.report(-1, "Compose stack %s is up", rm.Name)
		return nil
	}
	if s.Projects == nil {
		return nil
	}
	projs, _ := s.Store.ListProjects(rm.ID)
	if len(projs) == 0 {
		return nil
	}
	for _, p := range projs {
		if p.Image != "" && !s.Docker.ImageExists(p.Image) {
			return fmt.Errorf("image %s is not on this VPS after docker load (no registry pull)", p.Image)
		}
		s.report(-1, "Starting %s from backup image (no pull)", p.Name)
		if err := s.Projects.RedeployImage(projects.RedeployInput{
			ID: p.ID, Image: p.Image, Pull: false, Recreate: true,
		}); err != nil {
			return fmt.Errorf("redeploy %s: %w", p.Name, err)
		}
		s.report(-1, "Running %s", p.Name)
	}
	return nil
}

func (s *Service) findRestoredCompose(rm store.Room) (dir, project string) {
	project = stack.ComposeProject(rm.ID)
	cands := []string{
		filepath.Join(s.RuntimeDir, rm.ID),
		filepath.Join(s.RuntimeDir, rm.ID, "compose"),
	}
	if s.Rooms != nil {
		projs, _ := s.Store.ListProjects(rm.ID)
		for _, p := range projs {
			pdir := s.Rooms.ProjectDir(rm.ID, p.ID)
			cands = append(cands, pdir)
			_, composeDir, composeProject, _ := projects.ProjectLayout(pdir)
			if composeDir != "" {
				if composeProject != "" {
					project = composeProject
				}
				return composeDir, project
			}
			if dockerx.ComposeFile(pdir) != "" {
				return pdir, project
			}
		}
	}
	for _, d := range cands {
		if dockerx.ComposeFile(d) != "" {
			return d, project
		}
	}
	_ = filepath.Walk(filepath.Join(s.RuntimeDir, rm.ID), func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return err
		}
		n := strings.ToLower(info.Name())
		if n == "compose.yml" || n == "compose.yaml" || n == "docker-compose.yml" || n == "docker-compose.yaml" {
			dir = filepath.Dir(path)
			return filepath.SkipAll
		}
		return nil
	})
	return dir, project
}

func (s *Service) refreshRoomDockerIDs(rm store.Room) {
	if s.Docker == nil || s.Store == nil {
		return
	}
	cts, _ := s.Store.ListContainers(rm.ID)
	for i := range cts {
		ref := cts[i].Name
		if ref == "" {
			continue
		}
		id, status, _ := s.Docker.ContainerBrief(ref)
		if id == "" || status == "missing" {
			continue
		}
		cts[i].DockerID = id
		cts[i].Status = status
		_ = s.Store.UpsertContainer(cts[i])
	}
	projs, _ := s.Store.ListProjects(rm.ID)
	for _, p := range projs {
		if p.ContainerID != "" {
			id, status, _ := s.Docker.ContainerBrief(p.ContainerID)
			if status != "missing" && id != "" {
				p.ContainerID = id
				p.Status = status
				_ = s.Store.UpdateProject(p)
				continue
			}
		}
		cts, _ := s.Store.ListContainers(rm.ID)
		for _, c := range cts {
			if c.DockerID == "" {
				continue
			}
			if strings.EqualFold(c.Name, p.Name) || strings.Contains(c.Name, p.ID[:min(8, len(p.ID))]) {
				p.ContainerID = c.DockerID
				p.Status = c.Status
				_ = s.Store.UpdateProject(p)
				break
			}
		}
	}
}

func (s *Service) overlayContainerRW(rm store.Room, backupDir string) {
	if s.Docker == nil {
		return
	}
	ents, err := os.ReadDir(backupDir)
	if err != nil {
		return
	}
	cts, _ := s.Store.ListContainers(rm.ID)
	for _, e := range ents {
		ln := strings.ToLower(e.Name())
		if !strings.HasSuffix(ln, "-rw.tar.gz") {
			continue
		}
		ord := 0
		if i := strings.Index(ln, "container-"); i >= 0 {
			fmt.Sscanf(ln[i+len("container-"):], "%d", &ord)
		}
		id := ""
		for _, c := range cts {
			if ord > 0 && c.Ordinal == ord && c.DockerID != "" {
				id = c.DockerID
				break
			}
		}
		if id == "" && len(cts) == 1 {
			id = cts[0].DockerID
		}
		if id == "" {
			continue
		}
		s.report(-1, "Overlaying writable files onto %s", id[:min(12, len(id))])
		if err := s.Docker.OverlayRootfsFromTarGz(id, filepath.Join(backupDir, e.Name())); err != nil {
			s.report(-1, "rw overlay: %v (app data should live on volumes)", err)
		}
	}
}

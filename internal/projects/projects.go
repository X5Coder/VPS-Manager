package projects

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/x5coder/vps-rooms/internal/diskcap"
	"github.com/x5coder/vps-rooms/internal/dockerx"
	"github.com/x5coder/vps-rooms/internal/rooms"
	"github.com/x5coder/vps-rooms/internal/store"
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{2,40}$`)

type Service struct {
	Store       *store.Store
	Docker      *dockerx.Client
	Rooms       *rooms.Service
	VolumesDir  string
	AfterChange func()
}

type DeployImageInput struct {
	RoomID        string
	Name          string
	Image         string
	HostIP        string
	HostPort      int
	ContainerPort int
	EnvText       string
	ExtraBinds    []string // e.g. "/data/n8n:/home/node/.n8n"
	Log           io.Writer
}

type DeployBuildInput struct {
	RoomID        string
	Name          string
	HostPort      int
	ContainerPort int
	EnvText       string
	SourceDir     string
	Log           io.Writer
}

func (s *Service) List(roomID string) ([]store.Project, error) {
	list, err := s.Store.ListProjects(roomID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		s.syncLiveContainer(&list[i])
	}
	return list, nil
}

func (s *Service) Get(id string) (*store.Project, error) {
	p, err := s.Store.GetProject(id)
	if err != nil || p == nil {
		return p, err
	}
	s.syncLiveContainer(p)
	return p, nil
}

func dockerLive(c *dockerx.Client, ref string) (id, status, image string) {
	if c == nil || strings.TrimSpace(ref) == "" {
		return "", "missing", ""
	}
	id, status, image = c.ContainerBrief(ref)
	if status == "missing" || id == "" {
		return "", "missing", ""
	}
	return id, status, image
}

// syncLiveContainer follows vr_<room8>_<proj8> when docker recreate changed the id
// but the panel process died before UpdateProject. Adopted compose stacks bind
// to the gateway container (Kong) instead of a missing placeholder name.
func (s *Service) syncLiveContainer(p *store.Project) {
	if p == nil || s.Docker == nil {
		return
	}
	id, status, image := "", "", ""
	if len(p.RoomID) >= 8 && len(p.ID) >= 8 {
		id, status, image = dockerLive(s.Docker, containerName(p.RoomID, p.ID))
	}
	if id == "" && p.ContainerID != "" {
		id, status, image = dockerLive(s.Docker, p.ContainerID)
	}
	if id == "" && s.Rooms != nil {
		m := readMountsMeta(s.Rooms.ProjectDir(p.RoomID, p.ID))
		if m.Gateway != "" {
			id, status, image = dockerLive(s.Docker, m.Gateway)
		}
		if id == "" && m.ComposeProject != "" {
			list, _ := s.Docker.ListCompose(m.ComposeProject)
			for _, cc := range list {
				svc := strings.ToLower(strings.TrimSpace(cc.Service))
				if svc == "kong" || svc == "gateway" || strings.EqualFold(cc.Name, m.Gateway) {
					id, status, image = dockerLive(s.Docker, cc.Name)
					if id != "" {
						break
					}
				}
			}
		}
	}
	if id == "" && p.HostPort > 0 {
		id, status, image = s.Docker.BriefByPublish(p.HostPort)
		if status == "missing" {
			id = ""
		}
	}
	if id == "" {
		if p.Status == "running" {
			p.Status = "stopped"
		}
		return
	}
	changed := p.ContainerID != id
	p.ContainerID = id
	if status != "" && p.Status != status {
		p.Status = status
		changed = true
	} else if status != "" {
		p.Status = status
	}
	if image != "" && !strings.HasPrefix(image, "sha256:") && p.Image != image {
		p.Image = image
		changed = true
	}
	if changed {
		_ = s.Store.UpdateProject(*p)
		s.Store.SyncContainerFromProject(*p)
	}
}

func (s *Service) prepareRoom(roomID string) error {
	if err := s.Rooms.EnsureUnlocked(roomID); err != nil {
		return err
	}
	s.SyncRoomFilesVisibility(roomID)
	return nil
}

func (s *Service) persistRoom(roomID string) {
	_ = s.Rooms.Seal(roomID)
}

func (s *Service) touchProxy() {
	if s != nil && s.AfterChange != nil {
		s.AfterChange()
	}
}

func (s *Service) DeployImage(in DeployImageInput) (*store.Project, error) {
	if s.Docker == nil || !s.Docker.Available() {
		return nil, fmt.Errorf("Docker unavailable")
	}
	name := strings.TrimSpace(in.Name)
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("invalid project name")
	}
	room, err := s.Store.GetRoom(in.RoomID)
	if err != nil || room == nil {
		return nil, fmt.Errorf("room not found")
	}
	if err := s.prepareRoom(in.RoomID); err != nil {
		return nil, err
	}
	if err := s.Docker.EnsureNetwork(room.NetworkName); err != nil {
		return nil, err
	}
	log := in.Log
	if log == nil {
		log = io.Discard
	}
	fmt.Fprintf(log, "Pulling %s...\n", in.Image)
	if err := s.Docker.PullImage(in.Image, log); err != nil {
		// Local / private tags (e.g. vpsrooms/*) may not exist on a registry.
		if !s.Docker.ImageExists(in.Image) {
			return nil, err
		}
		fmt.Fprintf(log, "pull skipped — using local image %s\n", in.Image)
	}
	hostPort := in.HostPort
	if hostPort <= 0 {
		hostPort, err = s.Docker.FindFreePort(11000)
		if err != nil {
			return nil, err
		}
	}
	cPort := in.ContainerPort
	if cPort <= 0 {
		cPort = 80
	}
	id := uuid.NewString()
	pdir := s.Rooms.ProjectDir(in.RoomID, id)
	if err := os.MkdirAll(pdir, 0o700); err != nil {
		return nil, err
	}
	envPath := filepath.Join(pdir, ".env")
	if err := writeEnv(envPath, in.EnvText); err != nil {
		return nil, err
	}
	envPairs, _ := readEnvPairs(envPath)
	cname := containerName(in.RoomID, id)
	binds := []string{envPath + ":/app/.env:ro"}
	for _, b := range in.ExtraBinds {
		b = strings.TrimSpace(b)
		if b != "" {
			binds = append(binds, b)
		}
	}
	stub := &store.Project{ID: id, RoomID: in.RoomID}
	_ = writeMountsMeta(pdir, in.HostIP, binds)
	binds = s.ensurePersistentBinds(stub, pdir, envPath, binds)
	fmt.Fprintf(log, "تشغيل الحاوية على المنفذ %d...\n", hostPort)
	cid, err := s.Docker.Run(dockerx.RunOpts{
		Name:          cname,
		Image:         in.Image,
		Network:       room.NetworkName,
		HostIP:        in.HostIP,
		HostPort:      hostPort,
		ContainerPort: cPort,
		Env:           envPairs,
		Binds:         binds,
		StorageBytes:  room.QuotaBytes,
		Labels: map[string]string{
			"vps-rooms.room":    in.RoomID,
			"vps-rooms.project": id,
		},
	})
	if err != nil {
		return nil, err
	}
	p := store.Project{
		ID:            id,
		RoomID:        in.RoomID,
		Name:          name,
		Image:         in.Image,
		ContainerID:   cid,
		HostPort:      hostPort,
		ContainerPort: cPort,
		Status:        "running",
		CreatedAt:     time.Now().UTC(),
	}
	if err := s.Store.CreateProject(p); err != nil {
		_ = s.Docker.Remove(cid, true)
		return nil, err
	}
	s.SyncRoomFilesVisibility(in.RoomID)
	s.persistRoom(in.RoomID)
	fmt.Fprintf(log, "OK project=%s container=%s\n", id, cid[:12])
	return &p, nil
}

func (s *Service) DeployBuild(in DeployBuildInput) (*store.Project, error) {
	if s.Docker == nil || !s.Docker.Available() {
		return nil, fmt.Errorf("Docker unavailable")
	}
	name := strings.TrimSpace(in.Name)
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("invalid project name")
	}
	room, err := s.Store.GetRoom(in.RoomID)
	if err != nil || room == nil {
		return nil, fmt.Errorf("room not found")
	}
	if err := s.prepareRoom(in.RoomID); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	pdir := s.Rooms.ProjectDir(in.RoomID, id)
	if err := os.MkdirAll(pdir, 0o750); err != nil {
		return nil, err
	}
	// copy source into project dir if different
	if in.SourceDir != pdir {
		if err := copyTree(in.SourceDir, pdir); err != nil {
			return nil, err
		}
	}
	envPath := filepath.Join(pdir, ".env")
	if in.EnvText != "" {
		if err := writeEnv(envPath, in.EnvText); err != nil {
			return nil, err
		}
	}
	log := in.Log
	if log == nil {
		log = io.Discard
	}
	tag := fmt.Sprintf("vpsrooms/%s:latest", id[:8])
	fmt.Fprintf(log, "بناء الصورة %s...\n", tag)
	if err := s.Docker.BuildImage(context.Background(), pdir, tag, log); err != nil {
		return nil, err
	}
	hostPort := in.HostPort
	if hostPort <= 0 {
		hostPort, err = s.Docker.FindFreePort(11000)
		if err != nil {
			return nil, err
		}
	}
	cPort := in.ContainerPort
	if cPort <= 0 {
		cPort = 80
	}
	envPairs, _ := readEnvPairs(envPath)
	cname := containerName(in.RoomID, id)
	stub := &store.Project{ID: id, RoomID: in.RoomID}
	binds := s.ensurePersistentBinds(stub, pdir, envPath, []string{pdir + ":/data"})
	fmt.Fprintf(log, "تشغيل الحاوية على المنفذ %d...\n", hostPort)
	cid, err := s.Docker.Run(dockerx.RunOpts{
		Name:          cname,
		Image:         tag,
		Network:       room.NetworkName,
		HostPort:      hostPort,
		ContainerPort: cPort,
		Env:           envPairs,
		Binds:         binds,
		StorageBytes:  room.QuotaBytes,
		Labels: map[string]string{
			"vps-rooms.room":    in.RoomID,
			"vps-rooms.project": id,
		},
	})
	if err != nil {
		return nil, err
	}
	p := store.Project{
		ID:            id,
		RoomID:        in.RoomID,
		Name:          name,
		Image:         tag,
		ContainerID:   cid,
		HostPort:      hostPort,
		ContainerPort: cPort,
		Status:        "running",
		CreatedAt:     time.Now().UTC(),
	}
	if err := s.Store.CreateProject(p); err != nil {
		_ = s.Docker.Remove(cid, true)
		return nil, err
	}
	s.persistRoom(in.RoomID)
	fmt.Fprintf(log, "OK project=%s\n", id)
	return &p, nil
}

func (s *Service) Start(id string) error {
	p, err := s.Store.GetProject(id)
	if err != nil || p == nil {
		return fmt.Errorf("المشروع غير موجود")
	}
	if err := s.Docker.Start(p.ContainerID); err != nil {
		return err
	}
	p.Status = "running"
	return s.Store.UpdateProject(*p)
}

func (s *Service) Stop(id string) error {
	p, err := s.Store.GetProject(id)
	if err != nil || p == nil {
		return fmt.Errorf("المشروع غير موجود")
	}
	if err := s.Docker.Stop(p.ContainerID); err != nil {
		return err
	}
	p.Status = "stopped"
	return s.Store.UpdateProject(*p)
}

func (s *Service) Delete(id string) error {
	p, err := s.Store.GetProject(id)
	if err != nil || p == nil {
		return fmt.Errorf("project not found")
	}
	_ = s.prepareRoom(p.RoomID)
	if p.ContainerID != "" && s.Docker != nil {
		_ = s.Docker.Stop(p.ContainerID)
		_ = s.Docker.Remove(p.ContainerID, true)
	}
	_ = os.RemoveAll(s.Rooms.ProjectDir(p.RoomID, p.ID))
	if err := s.Store.DeleteProject(id); err != nil {
		return err
	}
	s.persistRoom(p.RoomID)
	return nil
}

func (s *Service) SetPort(id string, hostPort int) error {
	p, err := s.Store.GetProject(id)
	if err != nil || p == nil {
		return fmt.Errorf("project not found")
	}
	if hostPort < 1 || hostPort > 65535 {
		return fmt.Errorf("invalid port")
	}
	room, err := s.Store.GetRoom(p.RoomID)
	if err != nil || room == nil {
		return fmt.Errorf("room not found")
	}
	if err := s.prepareRoom(p.RoomID); err != nil {
		return err
	}
	envPath := filepath.Join(s.Rooms.ProjectDir(p.RoomID, p.ID), ".env")
	envPairs, _ := readEnvPairs(envPath)
	pdir := s.Rooms.ProjectDir(p.RoomID, p.ID)
	meta := readMountsMeta(pdir)
	s.seedDataVolume(p)
	binds := s.bindsForRun(p)
	if p.ContainerID != "" {
		_ = s.Docker.Stop(p.ContainerID)
		_ = s.Docker.Remove(p.ContainerID, true)
	}
	cid, err := s.Docker.Run(dockerx.RunOpts{
		Name:          containerName(p.RoomID, p.ID),
		Image:         p.Image,
		Network:       room.NetworkName,
		HostIP:        meta.HostIP,
		HostPort:      hostPort,
		ContainerPort: p.ContainerPort,
		Env:           envPairs,
		Binds:         binds,
		StorageBytes:  room.QuotaBytes,
		Labels: map[string]string{
			"vps-rooms.room":    p.RoomID,
			"vps-rooms.project": p.ID,
		},
	})
	if err != nil {
		return err
	}
	p.ContainerID = cid
	p.HostPort = hostPort
	p.Status = "running"
	if err := s.Store.UpdateProject(*p); err != nil {
		return err
	}
	s.persistRoom(p.RoomID)
	s.touchProxy()
	return nil
}

func (s *Service) SetDomain(id, domain string) error {
	p, err := s.Store.GetProject(id)
	if err != nil || p == nil {
		return fmt.Errorf("project not found")
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimSuffix(domain, "/")
	p.Domain = domain
	return s.Store.UpdateProject(*p)
}

func (s *Service) ClearPort(id string) error {
	p, err := s.Store.GetProject(id)
	if err != nil || p == nil {
		return fmt.Errorf("project not found")
	}
	room, err := s.Store.GetRoom(p.RoomID)
	if err != nil || room == nil {
		return fmt.Errorf("room not found")
	}
	if err := s.prepareRoom(p.RoomID); err != nil {
		return err
	}
	pdir := s.Rooms.ProjectDir(p.RoomID, p.ID)
	envPath := filepath.Join(pdir, ".env")
	envPairs, _ := readEnvPairs(envPath)
	meta := readMountsMeta(pdir)
	s.seedDataVolume(p)
	binds := s.bindsForRun(p)
	if p.ContainerID != "" {
		_ = s.Docker.Stop(p.ContainerID)
		_ = s.Docker.Remove(p.ContainerID, true)
	}
	cid, err := s.Docker.Run(dockerx.RunOpts{
		Name:          containerName(p.RoomID, p.ID),
		Image:         p.Image,
		Network:       room.NetworkName,
		HostIP:        meta.HostIP,
		HostPort:      0,
		ContainerPort: p.ContainerPort,
		Env:           envPairs,
		Binds:         binds,
		StorageBytes:  room.QuotaBytes,
		Labels: map[string]string{
			"vps-rooms.room":    p.RoomID,
			"vps-rooms.project": p.ID,
		},
	})
	if err != nil {
		return err
	}
	p.ContainerID = cid
	p.HostPort = 0
	p.Status = "running"
	if err := s.Store.UpdateProject(*p); err != nil {
		return err
	}
	s.persistRoom(p.RoomID)
	s.touchProxy()
	return nil
}

func (s *Service) SetExternalURL(id, url string) error {
	p, err := s.Store.GetProject(id)
	if err != nil || p == nil {
		return fmt.Errorf("project not found")
	}
	p.ExternalURL = strings.TrimSpace(url)
	return s.Store.UpdateProject(*p)
}

type RedeployInput struct {
	ID       string
	Image    string
	Pull     bool
	Recreate bool
	Log      io.Writer
}

// UpdateImage replaces the running container with a new image, keeping the same
// project id, ports, env, binds, and room.
func (s *Service) UpdateImage(id, image string, log io.Writer) error {
	return s.RedeployImage(RedeployInput{ID: id, Image: image, Pull: true, Recreate: true, Log: log})
}

func (s *Service) RedeployImage(in RedeployInput) error {
	if log := in.Log; log == nil {
		in.Log = io.Discard
	}
	log := in.Log
	image := strings.TrimSpace(in.Image)
	p, err := s.Store.GetProject(in.ID)
	if err != nil || p == nil {
		return fmt.Errorf("project not found")
	}
	if image == "" {
		image = strings.TrimSpace(p.Image)
	}
	if image == "" {
		return fmt.Errorf("image required")
	}
	room, err := s.Store.GetRoom(p.RoomID)
	if err != nil || room == nil {
		return fmt.Errorf("room not found")
	}
	if err := s.prepareRoom(p.RoomID); err != nil {
		return err
	}
	if err := s.Docker.EnsureNetwork(room.NetworkName); err != nil {
		return err
	}
	s.markDeploying(p.RoomID, p.ID, image, "deploy")
	fmt.Fprintf(log, "Redeploy project %s (%s) → %s\n", p.Name, p.ID, image)
	if in.Pull {
		fmt.Fprintf(log, "Pulling %s...\n", image)
		if err := s.Docker.PullImage(image, log); err != nil {
			if !s.Docker.ImageExists(image) {
				s.markDeployResult(p.RoomID, p.ID, p.Image, "", false, err.Error())
				return err
			}
			fmt.Fprintf(log, "pull skipped — using local image %s\n", image)
		}
	} else if !s.Docker.ImageExists(image) {
		err := fmt.Errorf("image %s not found locally (pull=false)", image)
		s.markDeployResult(p.RoomID, p.ID, p.Image, "", false, err.Error())
		return err
	}
	if !in.Recreate {
		p.Image = image
		p.Status = "running"
		_ = s.Store.UpdateProject(*p)
		digest := s.Docker.RepoDigest(image)
		if digest == "" {
			digest = s.Docker.ImageID(image)
		}
		s.markDeployResult(p.RoomID, p.ID, image, digest, true, "")
		fmt.Fprintf(log, "OK project=%s image=%s (not recreated)\n", p.ID, image)
		return nil
	}
	if err := s.recreateKeep(p, room, image, room.QuotaBytes, log); err != nil {
		s.markDeployResult(p.RoomID, p.ID, p.Image, "", false, err.Error())
		return err
	}
	digest := s.Docker.RepoDigest(image)
	if digest == "" {
		digest = s.Docker.ImageID(image)
	}
	s.markDeployResult(p.RoomID, p.ID, image, digest, true, "")
	fmt.Fprintf(log, "Updated. Project is running automatically. project=%s image=%s status=running\n", p.ID, image)
	return nil
}

// ApplyQuota stores the room quota and recreates containers so the size cap is live.
func (s *Service) ApplyQuota(roomID string, quotaBytes int64) error {
	if quotaBytes <= 0 {
		return fmt.Errorf("quota_gb is required and must be > 0")
	}
	if err := s.Rooms.SetQuota(roomID, quotaBytes); err != nil {
		return err
	}
	room, err := s.Store.GetRoom(roomID)
	if err != nil || room == nil {
		return fmt.Errorf("room not found")
	}
	_ = s.prepareRoom(roomID)
	list, _ := s.Store.ListProjects(roomID)
	volRoot := s.volumesDir()
	for i := range list {
		p := list[i]
		vol := filepath.Join(volRoot, p.ID)
		if st, err := os.Stat(vol); err == nil && st.IsDir() {
			_ = s.Docker.Stop(p.ContainerID)
			_ = diskcap.Ensure(vol, quotaBytes)
		}
		if err := s.recreate(&p, room, p.Image, quotaBytes, io.Discard); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) recreate(p *store.Project, room *store.Room, image string, storageBytes int64, log io.Writer) error {
	return s.recreateKeep(p, room, image, storageBytes, log)
}

func (s *Service) projectBinds(p *store.Project, pdir, envPath string) []string {
	meta := readMountsMeta(pdir)
	if len(meta.Binds) > 0 {
		return append([]string{}, meta.Binds...)
	}
	if _, err := os.Stat(envPath); err == nil {
		return []string{envPath + ":/app/.env:ro"}
	}
	return nil
}

func (s *Service) recreateKeep(p *store.Project, room *store.Room, image string, storageBytes int64, log io.Writer) error {
	if s.Docker == nil || !s.Docker.Available() {
		return fmt.Errorf("Docker unavailable")
	}
	if log == nil {
		log = io.Discard
	}
	if image == "" {
		image = p.Image
	}
	pdir := s.Rooms.ProjectDir(p.RoomID, p.ID)
	envPath := filepath.Join(pdir, ".env")
	envPairs, _ := readEnvPairs(envPath)
	meta := readMountsMeta(pdir)
	s.seedDataVolume(p)
	binds := s.ensurePersistentBinds(p, pdir, envPath, s.projectBinds(p, pdir, envPath))
	cname := containerName(p.RoomID, p.ID)
	prevName := cname + "-prev"
	oldID := p.ContainerID
	rolled := false
	if oldID != "" {
		_ = s.Docker.Stop(oldID)
		_ = s.Docker.RemoveByName(prevName)
		if err := s.Docker.Rename(oldID, prevName); err != nil {
			_ = s.Docker.Remove(oldID, true)
			oldID = ""
		} else {
			rolled = true
			oldID = prevName
		}
	}
	_ = s.Docker.RemoveByName(cname)
	runImage := image
	if id := s.Docker.ImageID(image); id != "" {
		runImage = id
	}
	fmt.Fprintf(log, "Starting container with image %s (%s)...\n", image, runImage)
	cid, err := s.Docker.Run(dockerx.RunOpts{
		Name:          cname,
		Image:         runImage,
		Network:       room.NetworkName,
		HostIP:        meta.HostIP,
		HostPort:      p.HostPort,
		ContainerPort: p.ContainerPort,
		Env:           envPairs,
		Binds:         binds,
		StorageBytes:  storageBytes,
		Labels: map[string]string{
			"vps-rooms.room":    p.RoomID,
			"vps-rooms.project": p.ID,
		},
	})
	if err != nil {
		if rolled {
			_ = s.Docker.RemoveByName(cname)
			_ = s.Docker.Rename(oldID, cname)
			_ = s.Docker.Start(cname)
			if restored, e := s.Store.GetProject(p.ID); e == nil && restored != nil {
				restored.ContainerID = cname
				restored.Status = "running"
				_ = s.Store.UpdateProject(*restored)
				p.ContainerID = restored.ContainerID
				p.Status = restored.Status
			}
			fmt.Fprintf(log, "rollback: restored previous container\n")
		}
		return err
	}
	if rolled {
		_ = s.Docker.Remove(oldID, true)
	}
	p.ContainerID = cid
	p.Image = image
	p.Status = "running"
	if err := s.Store.UpdateProject(*p); err != nil {
		return err
	}
	s.SyncRoomFilesVisibility(p.RoomID)
	s.persistRoom(p.RoomID)
	s.touchProxy()
	return nil
}

func (s *Service) ReadEnv(id string) (string, error) {
	p, err := s.Store.GetProject(id)
	if err != nil || p == nil {
		return "", fmt.Errorf("project not found")
	}
	_ = s.prepareRoom(p.RoomID)
	pdir := s.Rooms.ProjectDir(p.RoomID, p.ID)
	path := filepath.Join(pdir, ".env")
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	text := string(b)
	if strings.TrimSpace(text) != "" {
		return text, nil
	}
	if filled := s.discoverEnvText(*p, pdir); strings.TrimSpace(filled) != "" {
		_ = writeEnv(path, filled)
		return filled, nil
	}
	return "", nil
}

func (s *Service) discoverEnvText(p store.Project, pdir string) string {
	skip := map[string]bool{
		"PATH": true, "HOME": true, "HOSTNAME": true, "TERM": true, "LANG": true,
		"LC_ALL": true, "PWD": true, "OLDPWD": true, "SHLVL": true, "container": true,
		"DEBIAN_FRONTEND": true, "GPG_KEYS": true, "_": true,
	}
	fromFile := func(fp string) string {
		b, err := os.ReadFile(fp)
		if err != nil || strings.TrimSpace(string(b)) == "" {
			return ""
		}
		return string(b)
	}
	meta := readMountsMeta(pdir)
	cands := []string{filepath.Join(pdir, "files", ".env")}
	if meta.FilesRoot != "" {
		cands = append(cands, filepath.Join(meta.FilesRoot, ".env"))
	}
	if meta.ComposeDir != "" {
		cands = append(cands, filepath.Join(meta.ComposeDir, ".env"))
	}
	for _, cand := range cands {
		if t := fromFile(cand); t != "" {
			return t
		}
	}
	if s.Docker == nil || p.ContainerID == "" {
		return ""
	}
	pairs, err := s.Docker.InspectEnv(p.ContainerID)
	if err != nil || len(pairs) == 0 {
		return ""
	}
	var lines []string
	for _, e := range pairs {
		k, _, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" || skip[k] || strings.HasPrefix(k, "GPG_") {
			continue
		}
		lines = append(lines, e)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func (s *Service) WriteEnv(id, text string) error {
	p, err := s.Store.GetProject(id)
	if err != nil || p == nil {
		return fmt.Errorf("project not found")
	}
	if err := s.prepareRoom(p.RoomID); err != nil {
		return err
	}
	path := filepath.Join(s.Rooms.ProjectDir(p.RoomID, p.ID), ".env")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if err := writeEnv(path, text); err != nil {
		return err
	}
	s.persistRoom(p.RoomID)
	return nil
}

func containerName(roomID, projectID string) string {
	return "vr_" + roomID[:8] + "_" + projectID[:8]
}

type mountsMeta struct {
	HostIP         string   `json:"host_ip,omitempty"`
	Binds          []string `json:"binds,omitempty"`
	FilesRoot      string   `json:"files_root,omitempty"` // host dir shown in Files UI
	ComposeDir     string   `json:"compose_dir,omitempty"`
	ComposeProject string   `json:"compose_project,omitempty"`
	Gateway        string   `json:"gateway,omitempty"`
}

func writeMountsMeta(pdir, hostIP string, binds []string) error {
	m := readMountsMeta(pdir)
	m.HostIP = hostIP
	m.Binds = binds
	if m.FilesRoot == "" {
		m.FilesRoot = inferFilesRoot(pdir, "", binds)
	}
	return writeMountsMetaFull(pdir, m)
}

func writeMountsMetaFull(pdir string, m mountsMeta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(pdir, "mounts.json"), b, 0o600)
}

func readMountsMeta(pdir string) mountsMeta {
	var m mountsMeta
	b, err := os.ReadFile(filepath.Join(pdir, "mounts.json"))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

// ProjectLayout returns Files UI root + compose metadata for backup/restore.
func ProjectLayout(pdir string) (filesRoot, composeDir, composeProject string, binds []string) {
	m := readMountsMeta(pdir)
	return m.FilesRoot, m.ComposeDir, m.ComposeProject, m.Binds
}

// SplitBind parses host:dest or host:dest:mode (Linux bind specs).
func SplitBind(b string) (host, dest, mode string) {
	b = strings.TrimSpace(b)
	if b == "" {
		return "", "", ""
	}
	core := b
	if i := strings.LastIndex(b, ":"); i > 0 {
		maybe := b[i+1:]
		if maybe == "ro" || maybe == "rw" || maybe == "z" || maybe == "Z" {
			mode = maybe
			core = b[:i]
		}
	}
	j := strings.Index(core, ":/")
	if j < 0 {
		return core, "", mode
	}
	return core[:j], core[j+1:], mode
}

func dirHasAppFiles(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		n := e.Name()
		if n == ".env" || n == "mounts.json" || n == "files" || n == "data" || strings.HasPrefix(n, "__") {
			continue
		}
		return true
	}
	// data/ or files/ symlink to real content counts
	for _, name := range []string{"data", "files"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			sub, _ := os.ReadDir(p)
			if len(sub) > 0 {
				return true
			}
		}
	}
	return false
}

func inferFilesRoot(pdir, volumesDir string, binds []string) string {
	// 1) Exact /app bind host
	var appDataBind string
	for _, b := range binds {
		host, dest, _ := SplitBind(b)
		if host == "" || dest == "" {
			continue
		}
		st, err := os.Stat(host)
		if err != nil || !st.IsDir() {
			continue
		}
		if dest == "/app" {
			return host
		}
		if dest == "/app/data" || dest == "/home/node/.n8n" {
			appDataBind = host
		}
	}
	// 2) Dedicated volumes/{projectID}
	if volumesDir != "" {
		projID := filepath.Base(pdir)
		vol := filepath.Join(volumesDir, projID)
		if st, err := os.Stat(vol); err == nil && st.IsDir() {
			if ents, _ := os.ReadDir(vol); len(ents) > 0 {
				// Prefer volume when it is the /app mount or pdir has no real app tree
				for _, b := range binds {
					host, dest, _ := SplitBind(b)
					if dest == "/app" && filepath.Clean(host) == filepath.Clean(vol) {
						return vol
					}
				}
				if !dirHasAppFiles(pdir) {
					return vol
				}
			}
		}
	}
	// 3) If only data bind exists and pdir is meta-only, still show pdir (with data link)
	_ = appDataBind
	return pdir
}

// AppFilesRoot returns the host directory the Files UI should browse for a project.
func AppFilesRoot(pdir, volumesDir string) string {
	m := readMountsMeta(pdir)
	if m.FilesRoot != "" {
		if st, err := os.Stat(m.FilesRoot); err == nil && st.IsDir() {
			return m.FilesRoot
		}
	}
	root := inferFilesRoot(pdir, volumesDir, m.Binds)
	if root != "" {
		return root
	}
	return pdir
}

func (s *Service) volumesDir() string {
	if s.VolumesDir != "" {
		return s.VolumesDir
	}
	// Derive from runtime: .../runtime -> .../volumes
	if s.Rooms != nil && s.Rooms.RuntimeDir != "" {
		return filepath.Join(filepath.Dir(s.Rooms.RuntimeDir), "volumes")
	}
	return "/opt/vps-rooms/volumes"
}

// SyncRoomFilesVisibility keeps mounts.json + files_root aligned with live Docker
// binds and /volumes/{projectID} so the Files UI never looks empty after migrate.
func (s *Service) SyncRoomFilesVisibility(roomID string) {
	list, err := s.Store.ListProjects(roomID)
	if err != nil {
		return
	}
	volRoot := s.volumesDir()
	_ = os.MkdirAll(volRoot, 0o755)
	for _, p := range list {
		pdir := s.Rooms.ProjectDir(roomID, p.ID)
		_ = os.MkdirAll(pdir, 0o700)
		meta := readMountsMeta(pdir)
		pinnedRoot := meta.FilesRoot
		if s.Docker != nil && p.ContainerID != "" {
			if binds, err := s.Docker.InspectBinds(p.ContainerID); err == nil && len(binds) > 0 {
				meta.Binds = binds
			}
		}
		vol := filepath.Join(volRoot, p.ID)
		meta.FilesRoot = inferFilesRoot(pdir, volRoot, meta.Binds)
		if meta.ComposeDir != "" {
			if st, err := os.Stat(meta.ComposeDir); err == nil && st.IsDir() {
				meta.FilesRoot = meta.ComposeDir
			}
		}
		// Keep an explicit files_root (compose stacks / adopted projects).
		if pinnedRoot != "" {
			if st, err := os.Stat(pinnedRoot); err == nil && st.IsDir() {
				meta.FilesRoot = pinnedRoot
			}
		}
		if meta.FilesRoot == pdir {
			if st, err := os.Stat(vol); err == nil && st.IsDir() {
				if ents, _ := os.ReadDir(vol); len(ents) > 0 && !dirHasAppFiles(pdir) {
					meta.FilesRoot = vol
				}
			}
		}
		_ = writeMountsMetaFull(pdir, meta)
		// Stable symlink for humans / tools (vault walk does not follow symlinks).
		if meta.FilesRoot != "" && filepath.Clean(meta.FilesRoot) != filepath.Clean(pdir) {
			link := filepath.Join(pdir, "files")
			_ = os.Remove(link)
			_ = os.Symlink(meta.FilesRoot, link)
		}
	}
}

// DetectDeployKind returns "build" when the project tree is bind-mounted at /data.
func DetectDeployKind(p store.Project, pdir string) string {
	if strings.HasPrefix(p.Image, "vpsrooms/") {
		return "build"
	}
	if _, err := os.Stat(filepath.Join(pdir, "Dockerfile")); err == nil {
		return "build"
	}
	if _, err := os.Stat(filepath.Join(pdir, "__container_export.tar")); err == nil && !strings.HasPrefix(p.Image, "vpsrooms/") {
		// still image-style unless Dockerfile present
	}
	entries, _ := os.ReadDir(pdir)
	hasCode := false
	for _, e := range entries {
		n := e.Name()
		if n == ".env" || strings.HasPrefix(n, "__") {
			continue
		}
		hasCode = true
		break
	}
	if hasCode && strings.HasPrefix(p.Image, "vpsrooms/") {
		return "build"
	}
	if hasCode {
		if _, err := os.Stat(filepath.Join(pdir, "Dockerfile")); err == nil {
			return "build"
		}
	}
	return "image"
}

func pullableImage(image string) bool {
	return dockerx.RegistryPullable(image)
}

// Redeploy recreates the container from restored files (full backup restore).
func (s *Service) Redeploy(id string) error {
	if s.Docker == nil || !s.Docker.Available() {
		return fmt.Errorf("Docker unavailable")
	}
	p, err := s.Store.GetProject(id)
	if err != nil || p == nil {
		return fmt.Errorf("project not found")
	}
	room, err := s.Store.GetRoom(p.RoomID)
	if err != nil || room == nil {
		return fmt.Errorf("room not found")
	}
	if err := s.prepareRoom(p.RoomID); err != nil {
		return err
	}
	if err := s.Docker.EnsureNetwork(room.NetworkName); err != nil {
		return err
	}
	pdir := s.Rooms.ProjectDir(p.RoomID, p.ID)
	envPath := filepath.Join(pdir, ".env")
	envPairs, _ := readEnvPairs(envPath)
	cname := containerName(p.RoomID, p.ID)
	s.seedDataVolume(p)
	if p.ContainerID != "" {
		_ = s.Docker.Remove(p.ContainerID, true)
	}
	_ = s.Docker.RemoveByName(cname)

	// Restore named docker volumes captured under __volumes/
	volRoot := filepath.Join(pdir, "__volumes")
	if entries, err := os.ReadDir(volRoot); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			_ = s.Docker.CopyDirToVolume(filepath.Join(volRoot, e.Name()), e.Name())
		}
	}

	kind := DetectDeployKind(*p, pdir)
	image := strings.TrimSpace(p.Image)
	if b, err := os.ReadFile(filepath.Join(pdir, "__image_ref.txt")); err == nil {
		if ref := strings.TrimSpace(string(b)); ref != "" {
			image = ref
		}
	}
	meta := readMountsMeta(pdir)
	var binds []string
	hostIP := meta.HostIP

	imgTar := filepath.Join(pdir, "__container_image.tar.gz")
	if st, err := os.Stat(imgTar); err != nil || st.Size() == 0 {
		imgTar = filepath.Join(pdir, "__container_image.tar")
	}
	exportTar := filepath.Join(pdir, "__container_export.tar")
	loaded := false
	if st, err := os.Stat(imgTar); err == nil && st.Size() > 0 {
		tag, err := s.Docker.LoadImageTag(imgTar)
		if err != nil {
			return fmt.Errorf("load saved image: %w", err)
		}
		if tag != "" {
			image = tag
		}
		loaded = true
	} else if st, err := os.Stat(exportTar); err == nil && st.Size() > 0 {
		image = fmt.Sprintf("vpsrooms-restore/%s:latest", p.ID[:8])
		if err := s.Docker.ImportFilesystem(exportTar, image); err != nil {
			return err
		}
		loaded = true
	}

	if !loaded {
		if kind == "build" {
			tag := image
			if !strings.HasPrefix(tag, "vpsrooms/") {
				tag = fmt.Sprintf("vpsrooms/%s:latest", p.ID[:8])
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			if _, err := os.Stat(filepath.Join(pdir, "Dockerfile")); err == nil {
				if err := s.Docker.BuildImage(ctx, pdir, tag, io.Discard); err != nil {
					return fmt.Errorf("rebuild on this VPS: %w", err)
				}
				image = tag
			} else if pullableImage(image) {
				if err := s.Docker.PullImage(image, io.Discard); err != nil {
					return fmt.Errorf("pull %s on this VPS: %w", image, err)
				}
			}
		} else {
			if !pullableImage(image) {
				return fmt.Errorf("no image name to pull for %s", p.Name)
			}
			if err := s.Docker.PullImage(image, io.Discard); err != nil {
				if _, df := os.Stat(filepath.Join(pdir, "Dockerfile")); df == nil {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
					defer cancel()
					tag := image
					if !strings.HasPrefix(tag, "vpsrooms/") {
						tag = fmt.Sprintf("vpsrooms/%s:latest", p.ID[:8])
					}
					if err2 := s.Docker.BuildImage(ctx, pdir, tag, io.Discard); err2 != nil {
						return fmt.Errorf("pull %s: %v; rebuild: %w", image, err, err2)
					}
					image = tag
				} else {
					return fmt.Errorf("pull %s on this VPS: %w", image, err)
				}
			}
		}
	}

	if len(meta.Binds) > 0 {
		binds = append([]string{}, meta.Binds...)
	} else if kind == "build" {
		binds = []string{pdir + ":/data"}
	} else if _, err := os.Stat(envPath); err == nil {
		binds = []string{envPath + ":/app/.env:ro"}
	}
	binds = s.ensurePersistentBinds(p, pdir, envPath, binds)
	if image == "" {
		return fmt.Errorf("no image to redeploy")
	}
	cid, err := s.Docker.Run(dockerx.RunOpts{
		Name:          cname,
		Image:         image,
		Network:       room.NetworkName,
		HostIP:        hostIP,
		HostPort:      p.HostPort,
		ContainerPort: p.ContainerPort,
		Env:           envPairs,
		Binds:         binds,
		StorageBytes:  room.QuotaBytes,
		Labels: map[string]string{
			"vps-rooms.room":    p.RoomID,
			"vps-rooms.project": p.ID,
		},
	})
	if err != nil {
		return err
	}
	p.ContainerID = cid
	p.Image = image
	p.Status = "running"
	if err := s.Store.UpdateProject(*p); err != nil {
		return err
	}
	s.SyncRoomFilesVisibility(p.RoomID)
	s.persistRoom(p.RoomID)
	return nil
}

func writeEnv(path, text string) error {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return os.WriteFile(path, []byte(text), 0o600)
}

func readEnvPairs(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "=") {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		cerr := out.Close()
		if err != nil {
			return err
		}
		return cerr
	})
}

func ParsePort(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

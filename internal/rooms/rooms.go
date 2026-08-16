package rooms

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/x5coder/vps-rooms/internal/auth"
	"github.com/x5coder/vps-rooms/internal/dockerx"
	"github.com/x5coder/vps-rooms/internal/isolate"
	"github.com/x5coder/vps-rooms/internal/store"
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{2,40}$`)

type Service struct {
	Store      *store.Store
	Docker     *dockerx.Client
	RoomsDir   string
	RuntimeDir string
	VolumesDir string
}

type DiskStats struct {
	Usage     int64 // files + bind volumes + container writable layer (quota)
	Files     int64
	Volumes   int64
	Writable  int64
	Images    int64 // unique docker images this room uses (not in quota)
	Footprint int64 // usage + images
}

type CreateInput struct {
	Name       string
	Password   string
	QuotaBytes int64
	Kind       string
}

func (s *Service) paths(roomID string) isolate.RoomPaths {
	return isolate.Paths(s.RoomsDir, s.RuntimeDir, roomID)
}

func (s *Service) Create(in CreateInput) (*store.Room, error) {
	name := strings.TrimSpace(in.Name)
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("invalid room name")
	}
	if len(in.Password) < 6 {
		return nil, fmt.Errorf("password too short")
	}
	if existing, _ := s.Store.GetRoomByName(name); existing != nil {
		return nil, fmt.Errorf("room name already in use — each room must have a unique name")
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	netName := "vpsrooms_" + strings.ReplaceAll(id[:8], "-", "")
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if kind != store.KindMulti {
		kind = store.KindSingle
	}
	r := store.Room{
		ID:          id,
		Name:        name,
		PassHash:    hash,
		PassPlain:   in.Password,
		NetworkName: netName,
		QuotaBytes:  in.QuotaBytes,
		Kind:        kind,
		CreatedAt:   time.Now().UTC(),
	}
	p := s.paths(id)
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return nil, err
	}
	if err := isolate.WriteLockNotice(p, name); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p.Hash, []byte(hash), 0o600); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(p.Runtime, 0o700); err != nil {
		return nil, err
	}
	if err := isolate.SealRuntime(p, in.Password); err != nil {
		return nil, err
	}
	// Keep runtime unlocked for panel use
	if err := isolate.UnlockRuntime(p, in.Password); err != nil {
		return nil, err
	}
	_ = os.Chmod(p.Root, 0o700)
	_ = os.Chmod(filepath.Dir(p.Runtime), 0o700)

	if s.Docker != nil && s.Docker.Available() {
		if err := s.Docker.EnsureNetwork(netName); err != nil {
			return nil, fmt.Errorf("failed to create room network: %w", err)
		}
	}
	if err := s.Store.CreateRoom(r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Service) Unlock(roomID, password string) error {
	r, err := s.Store.GetRoom(roomID)
	if err != nil || r == nil {
		return fmt.Errorf("room not found")
	}
	if password == "" {
		password = r.PassPlain
	}
	if password == "" {
		return fmt.Errorf("room password required")
	}
	if !auth.CheckPassword(r.PassHash, password) {
		return fmt.Errorf("access denied: room password required")
	}
	return isolate.UnlockRuntime(s.paths(roomID), password)
}

func (s *Service) EnsureUnlocked(roomID string) error {
	r, err := s.Store.GetRoom(roomID)
	if err != nil || r == nil {
		return fmt.Errorf("room not found")
	}
	if r.PassPlain == "" {
		return fmt.Errorf("room password unavailable")
	}
	return isolate.EnsureRuntime(s.paths(roomID), r.PassPlain)
}

func (s *Service) Seal(roomID string) error {
	r, err := s.Store.GetRoom(roomID)
	if err != nil || r == nil {
		return fmt.Errorf("room not found")
	}
	if r.PassPlain == "" {
		return fmt.Errorf("room password unavailable")
	}
	p := s.paths(roomID)
	// Snapshot vault only — do NOT unlock/wipe runtime.
	// UnlockRuntime used to os.RemoveAll(runtime), destroying Docker bind data (e.g. app /data).
	return isolate.SealRuntime(p, r.PassPlain)
}

func (s *Service) Delete(id string) error {
	r, err := s.Store.GetRoom(id)
	if err != nil || r == nil {
		return fmt.Errorf("room not found")
	}
	projects, _ := s.Store.ListProjects(id)
	images := map[string]struct{}{}
	addImg := func(ref string) {
		ref = strings.TrimSpace(ref)
		if dockerx.LocalOwnedImage(ref) {
			images[ref] = struct{}{}
		}
	}
	addImg(localImageTag(r.Name))
	if len(id) >= 8 {
		addImg("vpsrooms/" + id[:8] + ":latest")
	}
	for _, p := range projects {
		addImg(p.Image)
		addImg(localImageTag(p.Name))
		if len(p.ID) >= 8 {
			addImg("vpsrooms/" + p.ID[:8] + ":latest")
		}
		metaPath := filepath.Join(s.ProjectDir(id, p.ID), "__deploy.json")
		if b, err := os.ReadFile(metaPath); err == nil {
			var meta struct {
				Image       string `json:"image"`
				ImageDigest string `json:"image_digest"`
			}
			if json.Unmarshal(b, &meta) == nil {
				addImg(meta.Image)
				addImg(meta.ImageDigest)
			}
		}
	}

	if s.Docker != nil {
		ids := map[string]struct{}{}
		for _, cid := range s.Docker.IDsByFilter("label=vps-rooms.room=" + id) {
			ids[cid] = struct{}{}
		}
		if len(id) >= 8 {
			for _, cid := range s.Docker.IDsByFilter("name=vr_" + id[:8] + "_") {
				ids[cid] = struct{}{}
			}
		}
		for _, p := range projects {
			if p.ContainerID != "" {
				ids[p.ContainerID] = struct{}{}
			}
			if len(id) >= 8 && len(p.ID) >= 8 {
				base := "vr_" + id[:8] + "_" + p.ID[:8]
				_ = s.Docker.RemoveByName(base)
				_ = s.Docker.RemoveByName(base + "-prev")
			}
		}
		for cid := range ids {
			_ = s.Docker.Stop(cid)
			_ = s.Docker.Remove(cid, true)
		}
		for ref := range images {
			_ = s.Docker.RemoveImage(ref)
		}
		_ = s.Docker.RemoveNetwork(r.NetworkName)
		s.Docker.PruneUnusedLocalImages()
	}

	volRoot := s.volumesRoot()
	for _, p := range projects {
		if volRoot != "" && p.ID != "" {
			_ = os.RemoveAll(filepath.Join(volRoot, p.ID))
			_ = os.RemoveAll(filepath.Join(volRoot, p.ID+".img"))
		}
		_ = s.Store.DeleteProject(p.ID)
	}
	_ = os.RemoveAll(s.paths(id).Root)
	_ = os.RemoveAll(filepath.Join(s.RuntimeDir, id))
	return s.Store.DeleteRoom(id)
}

func localImageTag(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r == '_' || r == '/' || r == ':' || r == ' ' {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "app"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return "vpsrooms/" + out + ":latest"
}

func (s *Service) Dir(roomID string) string {
	return s.paths(roomID).Root
}

func (s *Service) ProjectDir(roomID, projectID string) string {
	return filepath.Join(s.paths(roomID).Runtime, projectID)
}

func (s *Service) volumesRoot() string {
	if s.VolumesDir != "" {
		return s.VolumesDir
	}
	if s.RuntimeDir != "" {
		return filepath.Join(filepath.Dir(s.RuntimeDir), "volumes")
	}
	return ""
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func (s *Service) volumeBytes(roomID string) int64 {
	if s.Store == nil {
		return 0
	}
	root := s.volumesRoot()
	projs, _ := s.Store.ListProjects(roomID)
	var total int64
	seen := map[string]struct{}{}
	addPath := func(p string) {
		p = filepath.Clean(p)
		if p == "" || p == "." || p == "/" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		st, err := os.Stat(p)
		if err != nil {
			return
		}
		if st.IsDir() {
			total += dirSize(p)
			return
		}
		total += st.Size()
	}
	if root != "" {
		for _, p := range projs {
			if p.ID == "" {
				continue
			}
			addPath(filepath.Join(root, p.ID))
			addPath(filepath.Join(root, p.ID+".img"))
		}
	}
	return total
}

func (s *Service) imageBytes(roomID string) int64 {
	if s.Docker == nil || s.Store == nil {
		return 0
	}
	seen := map[string]struct{}{}
	var total int64
	add := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return
		}
		if !dockerx.LocalOwnedImage(ref) {
			return
		}
		key := s.Docker.ImageID(ref)
		if key == "" {
			key = strings.ToLower(ref)
		}
		if _, ok := seen[key]; ok {
			return
		}
		sz := s.Docker.ImageSize(ref)
		if sz <= 0 {
			return
		}
		seen[key] = struct{}{}
		total += sz
	}
	projs, _ := s.Store.ListProjects(roomID)
	for _, p := range projs {
		add(p.Image)
	}
	imgs, _ := s.Store.ListImages(roomID)
	for _, im := range imgs {
		add(im.Ref)
	}
	cts, _ := s.Store.ListContainers(roomID)
	for _, c := range cts {
		add(c.Image)
	}
	return total
}

func (s *Service) DiskStats(roomID string) DiskStats {
	files := dirSize(s.paths(roomID).Root) + dirSize(filepath.Join(s.RuntimeDir, roomID))
	vols := s.volumeBytes(roomID)
	var rw int64
	if s.Docker != nil && s.Store != nil {
		projs, _ := s.Store.ListProjects(roomID)
		for _, p := range projs {
			rw += s.Docker.SizeRw(p.ContainerID)
		}
		cts, _ := s.Store.ListContainers(roomID)
		seen := map[string]struct{}{}
		for _, p := range projs {
			if p.ContainerID != "" {
				seen[p.ContainerID] = struct{}{}
			}
		}
		for _, c := range cts {
			if c.DockerID == "" {
				continue
			}
			if _, ok := seen[c.DockerID]; ok {
				continue
			}
			rw += s.Docker.SizeRw(c.DockerID)
		}
	}
	usage := files + vols + rw
	images := s.imageBytes(roomID)
	return DiskStats{
		Usage: usage, Files: files, Volumes: vols, Writable: rw,
		Images: images, Footprint: usage + images,
	}
}

func (s *Service) UsageBytes(roomID string) (int64, error) {
	return s.DiskStats(roomID).Usage, nil
}

func (s *Service) SetQuota(roomID string, quotaBytes int64) error {
	r, err := s.Store.GetRoom(roomID)
	if err != nil || r == nil {
		return fmt.Errorf("room not found")
	}
	r.QuotaBytes = quotaBytes
	return s.Store.UpdateRoom(*r)
}

func (s *Service) SetName(roomID, name string) error {
	name = strings.TrimSpace(name)
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid room name")
	}
	r, err := s.Store.GetRoom(roomID)
	if err != nil || r == nil {
		return fmt.Errorf("room not found")
	}
	if existing, _ := s.Store.GetRoomByName(name); existing != nil && existing.ID != roomID {
		return fmt.Errorf("room name already in use — each room must have a unique name")
	}
	r.Name = name
	p := s.paths(roomID)
	_ = isolate.WriteLockNotice(p, name)
	return s.Store.UpdateRoom(*r)
}

func (s *Service) SetPassword(roomID, password string) error {
	r, err := s.Store.GetRoom(roomID)
	if err != nil || r == nil {
		return fmt.Errorf("room not found")
	}
	if len(password) < 6 {
		return fmt.Errorf("password too short")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	r.PassHash = hash
	r.PassPlain = password
	p := s.paths(roomID)
	_ = os.WriteFile(p.Hash, []byte(hash), 0o600)
	_ = isolate.WriteLockNotice(p, r.Name)
	// reseal with new password from current runtime
	_ = s.EnsureUnlocked(roomID)
	if err := isolate.SealRuntime(p, password); err != nil {
		return err
	}
	_ = isolate.UnlockRuntime(p, password)
	return s.Store.UpdateRoom(*r)
}

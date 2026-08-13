package rooms

import (
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
}

type CreateInput struct {
	Name       string
	Password   string
	QuotaBytes int64
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
		return nil, fmt.Errorf("room already exists")
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	netName := "vpsrooms_" + strings.ReplaceAll(id[:8], "-", "")
	r := store.Room{
		ID:          id,
		Name:        name,
		PassHash:    hash,
		PassPlain:   in.Password,
		NetworkName: netName,
		QuotaBytes:  in.QuotaBytes,
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
	for _, p := range projects {
		if s.Docker != nil && p.ContainerID != "" {
			_ = s.Docker.Stop(p.ContainerID)
			_ = s.Docker.Remove(p.ContainerID, true)
		}
		_ = s.Store.DeleteProject(p.ID)
	}
	if s.Docker != nil {
		_ = s.Docker.RemoveNetwork(r.NetworkName)
	}
	_ = os.RemoveAll(s.paths(id).Root)
	_ = os.RemoveAll(filepath.Join(s.RuntimeDir, id))
	return s.Store.DeleteRoom(id)
}

func (s *Service) Dir(roomID string) string {
	return s.paths(roomID).Root
}

func (s *Service) ProjectDir(roomID, projectID string) string {
	return filepath.Join(s.paths(roomID).Runtime, projectID)
}

func (s *Service) UsageBytes(roomID string) (int64, error) {
	var total int64
	for _, root := range []string{s.paths(roomID).Root, filepath.Join(s.RuntimeDir, roomID)} {
		_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.Mode().IsRegular() {
				total += info.Size()
			}
			return nil
		})
	}
	return total, nil
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
		return fmt.Errorf("room already exists")
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

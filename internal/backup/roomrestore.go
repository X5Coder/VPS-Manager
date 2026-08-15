package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/x5coder/vps-rooms/internal/store"
)

func (s *Service) restoreRoomRepo(token, repo string) error {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return fmt.Errorf("choose a room repository to restore")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		t, _, _ := s.LoadToken()
		token = t
	}
	if token == "" {
		return fmt.Errorf("GitHub PAT required")
	}
	gh := NewGitHub(token)
	user, err := gh.Validate()
	if err != nil {
		return err
	}
	gh.User = user.Login
	gh.Ctx = s.jobContext()

	s.report(4, "Reading %s", repo)
	work := filepath.Join(s.WorkDir, "room-restore-"+repo)
	_ = os.RemoveAll(work)
	if err := os.MkdirAll(work, 0o750); err != nil {
		return err
	}
	defer os.RemoveAll(work)
	defer s.cleanBackupTemps()

	gitDir := filepath.Join(work, repo)
	if err := gh.CloneOrPull(repo, gitDir); err != nil {
		return fmt.Errorf("clone %s: %w", repo, err)
	}
	var man RoomSnapManifest
	raw, err := os.ReadFile(filepath.Join(gitDir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("manifest missing in %s", repo)
	}
	if err := json.Unmarshal(raw, &man); err != nil {
		return fmt.Errorf("manifest invalid: %w", err)
	}
	if man.Format != RoomSnapFormat {
		return fmt.Errorf("wrong backup format in %s (need %s)", repo, RoomSnapFormat)
	}
	targetID := strings.TrimSpace(man.Room.ID)
	if targetID == "" {
		targetID = strings.TrimSpace(man.RoomID)
	}
	if targetID == "" {
		return fmt.Errorf("manifest has no room id")
	}
	roomName := strings.TrimSpace(man.Room.Name)
	if roomName == "" {
		roomName = strings.TrimSpace(man.Name)
	}
	if roomName == "" {
		return fmt.Errorf("manifest has no room name")
	}
	s.report(8, "Restore room %s", roomName)

	u := &snapUploader{gh: gh, repo: repo, gitDir: gitDir}
	blobByKey := map[string]SnapBlob{}
	for _, b := range man.Blobs {
		blobByKey[b.Key] = b
	}
	fetch := func(key, dest string) error {
		b, ok := blobByKey[key]
		if !ok {
			p := filepath.Join(gitDir, filepath.FromSlash(key))
			if _, err := os.Stat(p); err == nil {
				return copyFile(p, dest)
			}
			return fmt.Errorf("blob %s not in manifest", key)
		}
		return u.getFile(b, dest)
	}

	if s.Docker != nil && s.Docker.Available() {
		for i, img := range man.Images {
			if err := s.errIfStopped(); err != nil {
				return err
			}
			s.report(10+i*30/max1(len(man.Images)), "Load image %s", img.Ref)
			tree := filepath.Join(work, fmt.Sprintf("img-%02d", i+1))
			for _, key := range img.Files {
				rel := strings.TrimPrefix(key, img.Prefix)
				dest := filepath.Join(tree, filepath.FromSlash(rel))
				if err := fetch(key, dest); err != nil {
					return fmt.Errorf("image file %s: %w", key, err)
				}
			}
			if err := s.Docker.LoadImageDir(tree); err != nil {
				return fmt.Errorf("docker load %s: %w", img.Ref, err)
			}
			_ = os.RemoveAll(tree)
		}
	}

	if existing, _ := s.Store.GetRoom(targetID); existing != nil {
		s.report(40, "Room %s already on this VPS — replace this room only", roomName)
		if err := s.retireRoomKeepImages(*existing); err != nil {
			return err
		}
	} else if byName, _ := s.Store.GetRoomByName(roomName); byName != nil && byName.ID != targetID {
		return fmt.Errorf("room name %q is already used by another room — rename that room first. Restore keeps the original room ID", roomName)
	}

	meta := man.Room
	meta.ID = targetID
	meta.Name = roomName
	for i := range meta.Containers {
		meta.Containers[i].RoomID = targetID
		meta.Containers[i].DockerID = ""
	}
	for i := range meta.Images {
		meta.Images[i].RoomID = targetID
	}
	for i := range meta.Volumes {
		meta.Volumes[i].RoomID = targetID
	}

	created, _ := time.Parse(time.RFC3339, meta.CreatedAt)
	r := store.Room{
		ID: meta.ID, Name: roomName, PassHash: meta.PassHash, PassPlain: meta.Password,
		NetworkName: meta.NetworkName, QuotaBytes: meta.QuotaBytes, Kind: meta.Kind,
		Domain: meta.Domain, SSL: meta.SSL, CreatedAt: created,
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	if err := s.Store.UpsertRoom(r); err != nil {
		return fmt.Errorf("restore room row: %w", err)
	}
	for _, c := range meta.Containers {
		if c.Status == "running" {
			c.Status = "stopped"
		}
		if err := s.Store.UpsertContainer(c); err != nil {
			return fmt.Errorf("restore container row: %w", err)
		}
	}
	for _, im := range meta.Images {
		if err := s.Store.UpsertImage(im); err != nil {
			return fmt.Errorf("restore image row: %w", err)
		}
	}
	for _, v := range meta.Volumes {
		if err := s.Store.UpsertVolume(v); err != nil {
			return fmt.Errorf("restore volume row: %w", err)
		}
	}

	if b, ok := blobByKey["secrets/vault.bin"]; ok && s.RoomsDir != "" {
		dest := filepath.Join(s.RoomsDir, targetID, "vault.bin")
		if err := u.getFile(b, dest); err != nil {
			return fmt.Errorf("restore secrets: %w", err)
		}
		_ = os.Chmod(filepath.Dir(dest), 0o700)
	}
	if b, ok := blobByKey["secrets/runtime.env"]; ok && s.RuntimeDir != "" {
		dest := filepath.Join(s.RuntimeDir, targetID, ".env")
		if err := u.getFile(b, dest); err != nil {
			return fmt.Errorf("restore env: %w", err)
		}
	}
	if s.Rooms != nil {
		_ = s.Rooms.EnsureUnlocked(targetID)
	}
	if s.Docker != nil && r.NetworkName != "" {
		if err := s.Docker.EnsureNetwork(r.NetworkName); err != nil {
			return fmt.Errorf("network %s: %w", r.NetworkName, err)
		}
	}

	if s.Docker != nil && s.Docker.Available() {
		for i, vol := range man.Volumes {
			if err := s.errIfStopped(); err != nil {
				return err
			}
			name := strings.TrimSpace(vol.Rec.DockerName)
			if name == "" {
				name = strings.TrimSpace(vol.Rec.Name)
			}
			if name == "" || strings.HasPrefix(name, "/") {
				continue
			}
			s.report(55+i*15/max1(len(man.Volumes)), "Restore volume %s", name)
			tgz := filepath.Join(work, fmt.Sprintf("vol-%d.tgz", i))
			if err := fetch(vol.BlobKey, tgz); err != nil {
				return fmt.Errorf("volume %s: %w", name, err)
			}
			tmp, err := os.MkdirTemp("", "vm-rvol-*")
			if err != nil {
				return err
			}
			if err := extractTarGz(tgz, tmp); err != nil {
				os.RemoveAll(tmp)
				return fmt.Errorf("extract volume %s: %w", name, err)
			}
			if err := s.Docker.CopyDirToVolume(tmp, name); err != nil {
				os.RemoveAll(tmp)
				return fmt.Errorf("restore volume %s: %w", name, err)
			}
			os.RemoveAll(tmp)
			_ = os.Remove(tgz)
		}

		live, _ := s.Store.ListContainers(targetID)
		for _, c := range live {
			if c.DockerID != "" {
				_ = s.Docker.Stop(c.DockerID)
				_ = s.Docker.Remove(c.DockerID, true)
			}
		}

		for i, ct := range man.Containers {
			if err := s.errIfStopped(); err != nil {
				return err
			}
			if ct.InspectKey == "" {
				continue
			}
			s.report(75+i*15/max1(len(man.Containers)), "Create container %s", ct.Rec.Name)
			insp := filepath.Join(work, fmt.Sprintf("inspect-%d.json", i))
			if err := fetch(ct.InspectKey, insp); err != nil {
				return fmt.Errorf("inspect %s: %w", ct.Rec.Name, err)
			}
			raw, err := os.ReadFile(insp)
			if err != nil {
				return err
			}
			id, err := s.Docker.CreateFromInspect(raw, r.NetworkName)
			if err != nil {
				return fmt.Errorf("create %s: %w", ct.Rec.Name, err)
			}
			if err := s.Docker.Start(id); err != nil {
				return fmt.Errorf("start %s: %w", ct.Rec.Name, err)
			}
			ct.Rec.DockerID = id
			ct.Rec.RoomID = targetID
			ct.Rec.Status = "running"
			_ = s.Store.UpsertContainer(ct.Rec)
		}
	}

	s.saveRoomSnap(targetID, RoomSnapState{
		Repo: repo, Seq: man.Seq, At: time.Now().UTC().Format(time.RFC3339), OK: true, Fingerprint: man.Fingerprint,
	})
	if s.OnAfterRestore != nil {
		_ = s.OnAfterRestore()
	}
	s.report(100, "Room %s restored from %s", roomName, repo)
	return nil
}

func (s *Service) retireRoomKeepImages(rm store.Room) error {
	if s.Docker != nil && s.Docker.Available() {
		cts, _ := s.Store.ListContainers(rm.ID)
		for _, c := range cts {
			if c.DockerID != "" {
				_ = s.Docker.Stop(c.DockerID)
				_ = s.Docker.Remove(c.DockerID, true)
			}
			if c.Name != "" {
				_ = s.Docker.RemoveByName(c.Name)
			}
		}
		vols, _ := s.Store.ListVolumes(rm.ID)
		for _, v := range vols {
			name := strings.TrimSpace(v.DockerName)
			if name == "" {
				name = strings.TrimSpace(v.Name)
			}
			if name != "" && !strings.HasPrefix(name, "/") {
				_ = s.Docker.RemoveNamedVolume(name)
			}
		}
		if rm.NetworkName != "" {
			_ = s.Docker.RemoveNetwork(rm.NetworkName)
		}
	}
	_ = s.Store.SetMeta(roomSnapMetaKey(rm.ID), "")
	if s.RoomsDir != "" {
		_ = os.RemoveAll(filepath.Join(s.RoomsDir, rm.ID))
	}
	if s.RuntimeDir != "" {
		_ = os.RemoveAll(filepath.Join(s.RuntimeDir, rm.ID))
	}
	return s.Store.DeleteRoom(rm.ID)
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

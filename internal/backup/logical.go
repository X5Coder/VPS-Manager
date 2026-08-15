package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/x5coder/vps-rooms/internal/store"
)

type RoomBackupMeta struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Password    string            `json:"password"`
	PassHash    string            `json:"pass_hash"`
	NetworkName string            `json:"network_name"`
	QuotaBytes  int64             `json:"quota_bytes"`
	Kind        string            `json:"kind"`
	Domain      string            `json:"domain,omitempty"`
	SSL         bool              `json:"ssl"`
	CreatedAt   string            `json:"created_at"`
	Containers  []store.Container `json:"containers"`
	Images      []store.ImageRec  `json:"images"`
	Volumes     []store.VolumeRec `json:"volumes"`
}

func (s *Service) captureLogicalRoom(room store.Room, destDir string) error {
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return err
	}
	short := store.ShortRoomID(room.ID)
	cts, _ := s.Store.ListContainers(room.ID)
	imgs, _ := s.Store.ListImages(room.ID)
	vols, _ := s.Store.ListVolumes(room.ID)
	meta := RoomBackupMeta{
		ID: room.ID, Name: room.Name, Password: room.PassPlain, PassHash: room.PassHash,
		NetworkName: room.NetworkName, QuotaBytes: room.QuotaBytes, Kind: room.Kind,
		Domain: room.Domain, SSL: room.SSL, CreatedAt: room.CreatedAt.UTC().Format(time.RFC3339),
		Containers: cts, Images: imgs, Volumes: vols,
	}
	b, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(destDir, fmt.Sprintf("room-%s-room.json", short)), b, 0o600); err != nil {
		return err
	}
	vault := filepath.Join(s.RoomsDir, room.ID, "vault.bin")
	if raw, err := os.ReadFile(vault); err == nil && len(raw) > 0 {
		_ = os.WriteFile(filepath.Join(destDir, fmt.Sprintf("room-%s-secrets.enc", short)), raw, 0o600)
	}
	envPath := filepath.Join(s.RuntimeDir, room.ID, ".env")
	if raw, err := os.ReadFile(envPath); err == nil {
		_ = os.WriteFile(filepath.Join(destDir, fmt.Sprintf("room-%s-secrets.env", short)), raw, 0o600)
	}

	if s.Docker == nil || !s.Docker.Available() {
		return nil
	}
	for _, c := range cts {
		ord := c.Ordinal
		if ord <= 0 {
			ord = 1
		}
		if c.DockerID != "" {
			if out, err := s.Docker.InspectJSON(c.DockerID); err == nil && len(out) > 0 {
				_ = os.WriteFile(filepath.Join(destDir, fmt.Sprintf("room-%s-container-%02d-config.json", short, ord)), out, 0o600)
			}
		}
		ref := strings.TrimSpace(c.Image)
		if ref == "" {
			continue
		}
		// Image layers are saved once in cataloger.addImage — do not write a second tar here.
		if c.DockerID != "" {
			if rw := s.Docker.SizeRw(c.DockerID); rw > 1024 && rw < 256*1024*1024 {
				rwDest := filepath.Join(destDir, fmt.Sprintf("room-%s-container-%02d-rw.tar.gz", short, ord))
				s.report(-1, "Exporting writable layer %s (%s)", c.Name, formatBytes(rw))
				if err := s.Docker.ExportGzip(c.DockerID, rwDest); err != nil {
					s.report(-1, "export %s: %v (app data should live on volumes)", c.Name, err)
					_ = os.Remove(rwDest)
				}
			}
		}
	}
	seenVol := map[string]bool{}
	vi := 0
	for _, v := range vols {
		key := strings.TrimSpace(v.DockerName)
		if key == "" {
			key = v.Name
		}
		if key == "" || seenVol[key] {
			continue
		}
		if strings.HasPrefix(key, "/") {
			st, err := os.Lstat(key)
			if err != nil {
				return fmt.Errorf("volume %s: %w", key, err)
			}
			if !st.IsDir() && !st.Mode().IsRegular() && st.Mode()&os.ModeSymlink == 0 {
				continue
			}
		}
		seenVol[key] = true
		vi++
		dest := filepath.Join(destDir, fmt.Sprintf("room-%s-volume-%02d.tar.gz", short, vi))
		s.report(-1, "Archiving volume %s", key)
		if err := s.archiveVolume(key, dest); err != nil {
			_ = os.Remove(dest)
			return fmt.Errorf("volume %s: %w", key, err)
		}
	}
	if roomLooksPostgres(cts) {
		projs, _ := s.Store.ListProjects(room.ID)
		if !hasGoodPostgresDumpForRoom(s, room.ID, projs) && len(vols) > 0 {
			nVol := 0
			if ents, err := os.ReadDir(destDir); err == nil {
				for _, e := range ents {
					if strings.Contains(strings.ToLower(e.Name()), "-volume-") {
						nVol++
					}
				}
			}
			if nVol == 0 {
				return fmt.Errorf("postgres room %s has no SQL dump and no volume archive", room.Name)
			}
		}
	}
	return nil
}

func roomLooksPostgres(cts []store.Container) bool {
	for _, c := range cts {
		if imgLooksPostgres(c.Image) {
			return true
		}
	}
	return false
}

func backupImageErr(ref string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("image %s: %w", ref, err)
}

func (s *Service) archiveVolume(name, dest string) error {
	if strings.HasPrefix(name, "/") {
		return tarGzPath(name, dest)
	}
	tmp, err := os.MkdirTemp("", "vm-vol-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := s.Docker.CopyVolumeToDir(name, tmp); err != nil {
		return err
	}
	return tarGzPath(tmp, dest)
}

func tarGzPath(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		st, err = os.Stat(src)
		if err != nil {
			return err
		}
	}
	if st.Mode().IsRegular() {
		cmd := exec.Command("tar", "-C", filepath.Dir(src), "-czf", dest, filepath.Base(src))
		b, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("tar: %s: %w", strings.TrimSpace(string(b)), err)
		}
		return nil
	}
	if !st.IsDir() {
		return fmt.Errorf("not a directory or file: %s", src)
	}
	cmd := exec.Command("tar", "-C", src, "-czf", dest, ".")
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar: %s: %w", strings.TrimSpace(string(b)), err)
	}
	return nil
}

func extractTarGz(src, dest string) error {
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return err
	}
	cmd := exec.Command("tar", "-C", dest, "-xzf", src)
	b, err := cmd.CombinedOutput()
	if err != nil {
		f, e2 := os.Open(src)
		if e2 != nil {
			return fmt.Errorf("untar: %s: %w", strings.TrimSpace(string(b)), err)
		}
		defer f.Close()
		gz, e3 := gzip.NewReader(f)
		if e3 != nil {
			return err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			hdr, e := tr.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				return e
			}
			p := filepath.Join(dest, hdr.Name)
			if hdr.FileInfo().IsDir() {
				_ = os.MkdirAll(p, 0o750)
				continue
			}
			_ = os.MkdirAll(filepath.Dir(p), 0o750)
			out, e := os.Create(p)
			if e != nil {
				return e
			}
			_, _ = io.Copy(out, tr)
			out.Close()
		}
	}
	return nil
}

func (s *Service) applyLogicalBackup(roomID, dir string) {
	if dir == "" {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	short := store.ShortRoomID(roomID)
	_ = short
	var images, configs, volumes []string
	var secrets, roomJSON, envFile string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() {
			continue
		}
		if !strings.HasPrefix(n, "room-") {
			continue
		}
		p := filepath.Join(dir, n)
		ln := strings.ToLower(n)
		switch {
		case strings.Contains(ln, "-image-") && (strings.HasSuffix(ln, ".tar") || strings.HasSuffix(ln, ".tar.gz") || strings.HasSuffix(ln, ".tgz")):
			images = append(images, p)
		case strings.Contains(ln, "-container-") && strings.HasSuffix(ln, "-config.json"):
			configs = append(configs, p)
		case strings.Contains(ln, "-volume-") && (strings.HasSuffix(ln, ".tar.gz") || strings.HasSuffix(ln, ".tgz")):
			volumes = append(volumes, p)
		case strings.HasSuffix(ln, "-secrets.enc"):
			secrets = p
		case strings.HasSuffix(ln, "-room.json"):
			roomJSON = p
		case strings.HasSuffix(ln, "-secrets.env"):
			envFile = p
		}
	}
	if roomJSON != "" {
		s.applyRoomJSON(roomJSON)
	}
	if secrets != "" && s.RoomsDir != "" {
		dest := filepath.Join(s.RoomsDir, roomID, "vault.bin")
		_ = os.MkdirAll(filepath.Dir(dest), 0o700)
		_ = copyFile(secrets, dest)
	}
	if envFile != "" && s.RuntimeDir != "" {
		dest := filepath.Join(s.RuntimeDir, roomID, ".env")
		_ = os.MkdirAll(filepath.Dir(dest), 0o700)
		_ = copyFile(envFile, dest)
	}
	if s.Docker == nil || !s.Docker.Available() {
		return
	}
	for _, img := range images {
		s.report(-1, "Loading backup image %s", filepath.Base(img))
		if err := s.Docker.LoadImage(img); err != nil {
			s.report(-1, "load %s: %v", filepath.Base(img), err)
		}
	}
	vols, _ := s.Store.ListVolumes(roomID)
	for i, vp := range volumes {
		tmp, err := os.MkdirTemp("", "vm-rvol-*")
		if err != nil {
			continue
		}
		if err := extractTarGz(vp, tmp); err != nil {
			s.report(-1, "volume extract: %v", err)
			os.RemoveAll(tmp)
			continue
		}
		name := ""
		if i < len(vols) {
			name = vols[i].DockerName
			if name == "" {
				name = vols[i].Name
			}
		}
		if name == "" || strings.HasPrefix(name, "/") {
			os.RemoveAll(tmp)
			continue
		}
		s.report(-1, "Restoring volume %s", name)
		if err := s.Docker.CopyDirToVolume(tmp, name); err != nil {
			s.report(-1, "volume %s: %v", name, err)
		}
		os.RemoveAll(tmp)
	}
	_ = configs
	_ = time.Now()
}

func (s *Service) applyRoomJSON(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var meta RoomBackupMeta
	if json.Unmarshal(b, &meta) != nil || meta.ID == "" {
		return
	}
	r := store.Room{
		ID: meta.ID, Name: meta.Name, PassHash: meta.PassHash, PassPlain: meta.Password,
		NetworkName: meta.NetworkName, QuotaBytes: meta.QuotaBytes, Kind: meta.Kind,
		Domain: meta.Domain, SSL: meta.SSL,
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	_ = s.Store.UpsertRoom(r)
	for _, c := range meta.Containers {
		c.DockerID = ""
		if c.Status == "running" {
			c.Status = "stopped"
		}
		_ = s.Store.UpsertContainer(c)
	}
	for _, im := range meta.Images {
		_ = s.Store.UpsertImage(im)
	}
	for _, v := range meta.Volumes {
		_ = s.Store.UpsertVolume(v)
	}
}

package stack

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/x5coder/vps-rooms/internal/dockerx"
	"github.com/x5coder/vps-rooms/internal/rooms"
	"github.com/x5coder/vps-rooms/internal/store"
)

type Service struct {
	Store      *store.Store
	Docker     *dockerx.Client
	Rooms      *rooms.Service
	BaseDir    string
	RuntimeDir string // legacy (migration)
}

func (s *Service) stackDir(roomID string) string {
	if s.Rooms != nil {
		return s.Rooms.RoomStackDir(roomID)
	}
	base := s.BaseDir
	if base == "" {
		base = s.RuntimeDir
	}
	if base == "" {
		base = "/vps-manager"
	}
	return filepath.Join(base, "multi", roomID, "stack")
}

func (s *Service) roomEnvPath(roomID string) string {
	if s.Rooms != nil {
		return s.Rooms.RoomEnvPath(roomID)
	}
	return filepath.Join(s.stackDir(roomID), ".env")
}

// DeployMulti starts the stack already present in the room work dir
// (/vps-manager/multi/<room_id>/stack with docker-compose.yml + .env)
// on the existing room network. It does not delete other rooms.
// (Manual .tar upload was removed — the stack files are managed via SSH/panel files UI.)
func (s *Service) DeployMulti(room *store.Room, archive string, log io.Writer) error {
	if s.Docker == nil || !s.Docker.Available() {
		return fmt.Errorf("Docker unavailable")
	}
	if log == nil {
		log = io.Discard
	}
	_ = s.Rooms.EnsureUnlocked(room.ID)
	dir := s.stackDir(room.ID)
	// Preserve persistent bind sources (data/volumes) across re-extraction. A
	// compose app mounts ./data (and ./volumes) here; removing the dir would wipe
	// the project's stored data on every re-deploy. Move them out, rebuild the
	// packaging dir, then move them back.
	saved := filepath.Join(filepath.Dir(s.stackDir(room.ID)), ".stack-preserve")
	_ = os.RemoveAll(saved)
	_ = os.MkdirAll(saved, 0o750)
	for _, name := range []string{"data", "volumes", "__volumes"} {
		src := filepath.Join(dir, name)
		if st, err := os.Stat(src); err == nil && st.IsDir() {
			_ = os.Rename(src, filepath.Join(saved, name))
		}
	}
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	for _, name := range []string{"data", "volumes", "__volumes"} {
		from := filepath.Join(saved, name)
		if st, err := os.Stat(from); err == nil && st.IsDir() {
			_ = os.Rename(from, filepath.Join(dir, name))
		}
	}
	_ = os.RemoveAll(saved)
	fmt.Fprintf(log, "Extracting package...\n")
	if err := extractArchive(archive, dir); err != nil {
		return err
	}
	root := findPackageRoot(dir)
	compose := dockerx.ComposeFile(root)
	if compose == "" {
		return fmt.Errorf("package missing compose.yml")
	}
	imgDir := filepath.Join(root, "images")
	ents, _ := os.ReadDir(imgDir)
	if len(ents) == 0 {
		return fmt.Errorf("package missing images/")
	}
	loaded := 0
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		low := strings.ToLower(name)
		if !strings.HasSuffix(low, ".tar") && !strings.HasSuffix(low, ".tar.gz") && !strings.HasSuffix(low, ".tgz") {
			continue
		}
		src := filepath.Join(imgDir, name)
		fmt.Fprintf(log, "Loading %s...\n", name)
		tag, err := s.Docker.LoadImageTag(src)
		if err != nil {
			return fmt.Errorf("load %s: %w", name, err)
		}
		base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, ".gz"), ".tar"), ".tgz")
		if tag != "" && base != "" && !strings.Contains(tag, base) {
			want := "vpsrooms/" + store.ShortRoomID(room.ID) + "-" + base + ":latest"
			_ = exec.Command("docker", "tag", tag, want).Run()
			tag = want
		}
		_ = s.Store.UpsertImage(store.ImageRec{
			ID: uuid.NewString(), RoomID: room.ID, Name: base, Ref: tag, SizeBytes: s.Docker.ImageSize(tag),
		})
		loaded++
		fmt.Fprintf(log, "Loaded %s as %s\n", name, tag)
	}
	if loaded == 0 {
		return fmt.Errorf("no image tars loaded")
	}
	envPath := s.roomEnvPath(room.ID)
	_ = os.MkdirAll(filepath.Dir(envPath), 0o700)
	if _, err := os.Stat(envPath); err != nil {
		_ = os.WriteFile(envPath, []byte{}, 0o600)
	}
	// Compose reads .env from the project directory for ${VAR} substitution.
	if b, err := os.ReadFile(envPath); err == nil {
		_ = os.WriteFile(filepath.Join(root, ".env"), b, 0o600)
	}
	over := filepath.Join(root, "compose.vps-override.yml")
	net := room.NetworkName
	if err := s.Docker.EnsureNetwork(net); err != nil {
		return err
	}
	body := fmt.Sprintf("networks:\n  default:\n    name: %s\n    external: true\n", net)
	if err := os.WriteFile(over, []byte(body), 0o644); err != nil {
		return err
	}
	proj := "vr" + store.ShortRoomID(room.ID)
	fmt.Fprintf(log, "Starting stack %s...\n", proj)
	ctxFile := compose
	// --force-recreate: an update must actually swap running containers for
	// the new source instead of keeping stale ones.
	cmd := exec.Command("docker", "compose", "-f", ctxFile, "-f", over, "-p", proj, "up", "-d", "--force-recreate", "--pull", "never")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "ENV_FILE="+envPath)
	out, err := cmd.CombinedOutput()
	fmt.Fprintf(log, "%s\n", out)
	if err != nil {
		return fmt.Errorf("compose up: %w", err)
	}
	list, _ := s.Docker.ListCompose(proj)
	for _, cc := range list {
		id, status, image := s.Docker.ContainerBrief(cc.Name)
		// Stable record id per room+service: repeated deploys update the
		// same row instead of piling up duplicate container copies.
		_ = s.Store.UpsertContainer(store.Container{
			ID: stackContainerID(room.ID, cc.Name), RoomID: room.ID, Name: cc.Name, Service: cc.Service,
			Image: image, DockerID: id, Status: status, CreatedAt: time.Now().UTC(),
		})
	}
	if len(list) > 1 {
		_ = s.Store.SetRoomKind(room.ID, store.KindMulti)
	} else {
		_ = s.Store.SetRoomKind(room.ID, store.KindSingle)
	}
	fmt.Fprintf(log, "Stack running. services=%d\n", len(list))
	return nil
}

// stackContainerID is a stable record id per room+service name so repeated
// deploys update one row instead of duplicating container records.
func stackContainerID(roomID, name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "svc"
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return store.ShortRoomID(roomID) + "-" + out
}

func findPackageRoot(dir string) string {
	if dockerx.ComposeFile(dir) != "" {
		return dir
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() {
			p := filepath.Join(dir, e.Name())
			if dockerx.ComposeFile(p) != "" {
				return p
			}
		}
	}
	return dir
}

func extractArchive(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	low := strings.ToLower(src)
	var r io.Reader = f
	if strings.HasSuffix(low, ".gz") || strings.HasSuffix(low, ".tgz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		r = gz
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if strings.HasPrefix(name, "..") {
			continue
		}
		path := filepath.Join(dest, name)
		if hdr.Typeflag == tar.TypeDir {
			_ = os.MkdirAll(path, 0o750)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, tr)
		out.Close()
		if copyErr != nil {
			return copyErr
		}
	}
}

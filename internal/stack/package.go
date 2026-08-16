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
	RuntimeDir string
}

func LooksLikeArchive(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".tar") || strings.HasSuffix(n, ".tar.gz") || strings.HasSuffix(n, ".tgz")
}

func ArchiveHasCompose(src string) bool {
	return withTarEntries(src, 400, func(n string) bool {
		base := strings.ToLower(filepath.Base(n))
		if strings.Contains(base, "override") {
			return false
		}
		if strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml") {
			dir := strings.Trim(filepath.ToSlash(filepath.Dir(n)), ".")
			return dir == "" || dir == "."
		}
		return false
	})
}

// DeployMulti extracts project.vps.tar.gz (compose.yml + images/*.tar) and
// starts the stack on the existing room network. It does not delete other rooms.
func (s *Service) DeployMulti(room *store.Room, archive string, log io.Writer) error {
	if s.Docker == nil || !s.Docker.Available() {
		return fmt.Errorf("Docker unavailable")
	}
	if log == nil {
		log = io.Discard
	}
	_ = s.Rooms.EnsureUnlocked(room.ID)
	dir := filepath.Join(s.RuntimeDir, room.ID, "stack")
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
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
	envPath := filepath.Join(s.RuntimeDir, room.ID, ".env")
	_ = os.MkdirAll(filepath.Dir(envPath), 0o700)
	if _, err := os.Stat(envPath); err != nil {
		_ = os.WriteFile(envPath, []byte{}, 0o600)
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
	cmd := exec.Command("docker", "compose", "-f", ctxFile, "-f", over, "-p", proj, "up", "-d", "--pull", "never")
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
		_ = s.Store.UpsertContainer(store.Container{
			ID: uuid.NewString(), RoomID: room.ID, Name: cc.Name, Service: cc.Service,
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

package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	FormatMagic      = "VPS-MANAGE-BACKUP-v1"
	FormatMagicV2    = "VPS-MANAGE-BACKUP-v2"
	IndexRepo        = "vps-manage-map"
	SystemRepo       = "vps-manage-system"
	ImagesRepo       = "vps-manage-images"
	ContainersRepo   = "vps-manage-containers"
	VolumeRepoPrefix = "vps-manage-volumes"
	LayersRelease    = "vps-layers"
	ChunkSize        = 90 * 1024 * 1024
	MaxRepoBytes     = 4 * 1024 * 1024 * 1024
	ReleaseAssetMax  = 2047 * 1024 * 1024
	MaxLogicalPart   = 100 * 1024 * 1024 * 1024
	IntervalHours    = 24
)

type Checkpoint struct {
	Kind        string        `json:"kind"`
	SnapshotID  string        `json:"snapshot_id,omitempty"`
	SystemDone  bool          `json:"system_done"`
	RoomsDone   []string      `json:"rooms_done,omitempty"`
	SystemRepo  string        `json:"system_repo,omitempty"`
	SystemFiles []FileEntry   `json:"system_files,omitempty"`
	Projects    []ProjectMap  `json:"projects,omitempty"`
	Layout      *BackupLayout `json:"layout,omitempty"`
	UpdatedAt   string        `json:"updated_at"`
}

// BackupLayout is the stable restore map (v2).
type BackupLayout struct {
	ImagesRepo     string         `json:"images_repo"`
	ImagesRelease  string         `json:"images_release"`
	ContainersRepo string         `json:"containers_repo"`
	VolumeRepos    []string       `json:"volume_repos"`
	Rooms          []RoomLayout   `json:"rooms"`
	Images         []ImageLayout  `json:"images"`
	Layers         []LayerLayout  `json:"layers"`
	Volumes        []VolumeLayout `json:"volumes"`
	Files          []FileEntry    `json:"files,omitempty"`
}

type RoomLayout struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Short      string   `json:"short"`
	Path       string   `json:"path"`
	ImageKeys  []string `json:"image_keys,omitempty"`
	VolumeKeys []string `json:"volume_keys,omitempty"`
}

type ImageLayout struct {
	Key      string          `json:"key"`
	Tags     []string        `json:"tags,omitempty"`
	RoomIDs  []string        `json:"room_ids,omitempty"`
	Format   string          `json:"format"`
	TreePath string          `json:"tree_path"`
	Layers   []ImageLayerUse `json:"layers"`
}

type ImageLayerUse struct {
	Rel    string `json:"rel"`
	Digest string `json:"digest"`
}

type LayerLayout struct {
	Digest  string   `json:"digest"`
	Size    int64    `json:"size"`
	SHA256  string   `json:"sha256"`
	Release string   `json:"release"`
	Assets  []string `json:"assets"`
}

type VolumeLayout struct {
	Key    string       `json:"key"`
	RoomID string       `json:"room_id"`
	Name   string       `json:"name"`
	Size   int64        `json:"size"`
	SHA256 string       `json:"sha256"`
	Parts  []VolumePart `json:"parts"`
}

type VolumePart struct {
	Index  int      `json:"index"`
	Size   int64    `json:"size"`
	SHA256 string   `json:"sha256"`
	Repo   string   `json:"repo"`
	Chunks []string `json:"chunks"`
}

type FileEntry struct {
	Path    string   `json:"path"`
	Size    int64    `json:"size"`
	SHA256  string   `json:"sha256"`
	Chunks  []string `json:"chunks"`
	Deleted bool     `json:"deleted,omitempty"`
}

// ProjectMeta is one project row inside a room (full panel state).
type ProjectMeta struct {
	ID            string `json:"id"`
	RoomID        string `json:"room_id"`
	Name          string `json:"name"`
	Image         string `json:"image"`
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Domain        string `json:"domain"`
	DomainEnabled bool   `json:"domain_enabled"`
	SSLStatus     string `json:"ssl_status"`
	ExternalURL   string `json:"external_url"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
	DeployKind    string `json:"deploy_kind"` // image | build
}

type ProjectMap struct {
	RoomID      string        `json:"room_id"`
	RoomName    string        `json:"room_name"`
	PassHash    string        `json:"pass_hash,omitempty"`
	PassPlain   string        `json:"pass_plain,omitempty"`
	NetworkName string        `json:"network_name,omitempty"`
	ProjectID   string        `json:"project_id,omitempty"`
	ProjectName string        `json:"project_name,omitempty"`
	ProjectRepo string        `json:"project_repo"`
	BackupRepos []string      `json:"backup_repos"`
	QuotaBytes  int64         `json:"quota_bytes"`
	HostPort    int           `json:"host_port,omitempty"`
	Domain      string        `json:"domain,omitempty"`
	Image       string        `json:"image,omitempty"`
	Projects    []ProjectMeta `json:"projects,omitempty"`
	Files       []FileEntry   `json:"files"`
}

type Manifest struct {
	Format      string        `json:"format"`
	Version     int           `json:"version"`
	SnapshotID  string        `json:"snapshot_id"`
	Label       string        `json:"label"`
	Description string        `json:"description"`
	CreatedAt   string        `json:"created_at"`
	Owner       string        `json:"owner"`
	FullBackup  bool          `json:"full_backup"`
	SystemRepo  string        `json:"system_repo,omitempty"`
	SystemFiles []FileEntry   `json:"system_files,omitempty"`
	Projects    []ProjectMap  `json:"projects"`
	Layout      *BackupLayout `json:"layout,omitempty"`
}

type SnapshotRecord struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Description  string `json:"description"`
	CreatedAt    string `json:"created_at"`
	Status       string `json:"status"`
	Owner        string `json:"owner"`
	ManifestPath string `json:"manifest_path"`
}

func NewManifest(id, label, desc, owner string) *Manifest {
	return &Manifest{
		Format: FormatMagic, Version: 1,
		SnapshotID: id, Label: label, Description: desc,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Owner:     owner, Projects: []ProjectMap{},
		FullBackup: true, SystemRepo: SystemRepo,
	}
}

func acceptedBackupFormat(got string) bool {
	got = strings.TrimSpace(got)
	return got == FormatMagic || got == FormatMagicV2
}

func ValidateManifest(m *Manifest) error {
	if m == nil {
		return fmt.Errorf("empty manifest")
	}
	if !acceptedBackupFormat(m.Format) {
		return fmt.Errorf("backup not organized for VPS MANAGE (expected %s)", FormatMagic)
	}
	if m.Version < 1 {
		return fmt.Errorf("unsupported backup version")
	}
	return nil
}

func HashFile(path string) (string, int64, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", 0, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		st, err = os.Stat(path)
		if err != nil {
			return "", 0, err
		}
	}
	if st.IsDir() {
		return "", 0, fmt.Errorf("read %s: is a directory", path)
	}
	if !st.Mode().IsRegular() {
		return "", 0, fmt.Errorf("read %s: not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func ChunkFile(src, destDir, baseName string) ([]string, error) {
	return ChunkFileSize(src, destDir, baseName, ChunkSize)
}

func ChunkFileSize(src, destDir, baseName string, size int64) ([]string, error) {
	if size <= 0 {
		size = ChunkSize
	}
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return nil, err
	}
	in, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	var parts []string
	buf := make([]byte, min64(size, 8*1024*1024))
	var written int64
	idx := 0
	var out *os.File
	openPart := func() error {
		name := fmt.Sprintf("%s.part%03d", baseName, idx)
		parts = append(parts, name)
		var e error
		out, e = os.OpenFile(filepath.Join(destDir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		written = 0
		return e
	}
	if err := openPart(); err != nil {
		return nil, err
	}
	for {
		want := size - written
		if want > int64(len(buf)) {
			want = int64(len(buf))
		}
		n, rerr := in.Read(buf[:want])
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				out.Close()
				return nil, err
			}
			written += int64(n)
			if written >= size {
				out.Close()
				idx++
				if err := openPart(); err != nil {
					return nil, err
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return nil, rerr
		}
	}
	out.Close()
	if written == 0 && idx > 0 {
		last := parts[len(parts)-1]
		_ = os.Remove(filepath.Join(destDir, last))
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		name := fmt.Sprintf("%s.part000", baseName)
		if err := os.WriteFile(filepath.Join(destDir, name), nil, 0o600); err != nil {
			return nil, err
		}
		parts = append(parts, name)
	}
	return parts, nil
}

func JoinChunks(partPaths []string, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	for _, p := range partPaths {
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		in.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func VolumeRepoName(n int) string {
	return fmt.Sprintf("%s-%03d", VolumeRepoPrefix, n)
}

func sharedBackupRepo(name string) bool {
	switch name {
	case IndexRepo, SystemRepo, ImagesRepo, ContainersRepo:
		return true
	}
	return strings.HasPrefix(name, VolumeRepoPrefix+"-")
}

func LayerAssetName(digest string) string {
	d := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(digest)), "sha256:")
	if len(d) > 64 {
		d = d[:64]
	}
	return "l-" + d
}

func Slug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r == '_' || r == ' ' {
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	if len(s) < 2 {
		s = "project"
	}
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func ProjectRepoName(slug string) string { return slug + "-project" }
func BackupRepoName(slug string, n int) string {
	return fmt.Sprintf("%s-backup-%03d", slug, n)
}

func MarshalPretty(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

func mustPretty(v any) []byte {
	b, _ := MarshalPretty(v)
	return b
}

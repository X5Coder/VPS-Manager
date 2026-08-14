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
	FormatMagic   = "VPS-MANAGE-BACKUP-v1"
	IndexRepo     = "vps-manage-map"
	SystemRepo    = "vps-manage-system"
	ChunkSize     = 90 * 1024 * 1024
	MaxRepoBytes  = 900 * 1024 * 1024
	IntervalHours = 24
)

type Checkpoint struct {
	Kind        string       `json:"kind"`
	SnapshotID  string       `json:"snapshot_id,omitempty"`
	SystemDone  bool         `json:"system_done"`
	RoomsDone   []string     `json:"rooms_done,omitempty"`
	SystemRepo  string       `json:"system_repo,omitempty"`
	SystemFiles []FileEntry  `json:"system_files,omitempty"`
	Projects    []ProjectMap `json:"projects,omitempty"`
	UpdatedAt   string       `json:"updated_at"`
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
	Format      string       `json:"format"`
	Version     int          `json:"version"`
	SnapshotID  string       `json:"snapshot_id"`
	Label       string       `json:"label"`
	Description string       `json:"description"`
	CreatedAt   string       `json:"created_at"`
	Owner       string       `json:"owner"`
	FullBackup  bool         `json:"full_backup"`
	SystemRepo  string       `json:"system_repo,omitempty"`
	SystemFiles []FileEntry  `json:"system_files,omitempty"`
	Projects    []ProjectMap `json:"projects"`
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

func ValidateManifest(m *Manifest) error {
	if m == nil {
		return fmt.Errorf("empty manifest")
	}
	if m.Format != FormatMagic {
		return fmt.Errorf("backup not organized for VPS MANAGE (expected %s)", FormatMagic)
	}
	if m.Version < 1 {
		return fmt.Errorf("unsupported backup version")
	}
	return nil
}

func HashFile(path string) (string, int64, error) {
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
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return nil, err
	}
	in, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	var parts []string
	buf := make([]byte, ChunkSize)
	for i := 0; ; i++ {
		n, rerr := io.ReadFull(in, buf)
		if n > 0 {
			name := fmt.Sprintf("%s.part%03d", baseName, i)
			if err := os.WriteFile(filepath.Join(destDir, name), buf[:n], 0o600); err != nil {
				return nil, err
			}
			parts = append(parts, name)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
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
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if _, err := out.Write(b); err != nil {
			return err
		}
	}
	return nil
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

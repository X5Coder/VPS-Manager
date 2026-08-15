package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const RoomSnapRelease = "snap"

type SnapBlob struct {
	Key        string   `json:"key"`
	Size       int64    `json:"size"`
	SHA256     string   `json:"sha256"`
	Store      string   `json:"store"`
	GitPath    string   `json:"git_path,omitempty"`
	ReleaseTag string   `json:"release_tag,omitempty"`
	Asset      string   `json:"asset,omitempty"`
	Parts      []string `json:"parts,omitempty"`
}

type snapUploader struct {
	gh     *GitHub
	repo   string
	gitDir string
}

func assetName(sha string, part int) string {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if len(sha) > 40 {
		sha = sha[:40]
	}
	if part < 0 {
		return "b-" + sha
	}
	return fmt.Sprintf("b-%s-%03d", sha, part)
}

func (u *snapUploader) putFile(key, src string) (*SnapBlob, error) {
	sum, n, err := HashFile(src)
	if err != nil {
		return nil, err
	}
	b := &SnapBlob{Key: key, Size: n, SHA256: sum}
	if n <= ChunkSize {
		dest := filepath.Join(u.gitDir, filepath.FromSlash(key))
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			return nil, err
		}
		if err := copyFile(src, dest); err != nil {
			return nil, err
		}
		b.Store = "git"
		b.GitPath = key
		return b, nil
	}
	if _, err := u.gh.EnsureRelease(u.repo, RoomSnapRelease, "Room snapshot parts"); err != nil {
		return nil, err
	}
	b.ReleaseTag = RoomSnapRelease
	if n <= ReleaseAssetMax {
		name := assetName(sum, -1)
		if err := u.gh.UploadReleaseFile(u.repo, RoomSnapRelease, name, src); err != nil {
			return nil, err
		}
		b.Store = "release"
		b.Asset = name
		return b, nil
	}
	tmp, err := os.MkdirTemp("", "vps-parts-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	parts, err := ChunkFileSize(src, tmp, "p", ReleaseAssetMax)
	if err != nil {
		return nil, err
	}
	var assets []string
	for i, p := range parts {
		srcp := filepath.Join(tmp, p)
		name := assetName(sum, i)
		if err := u.gh.UploadReleaseFile(u.repo, RoomSnapRelease, name, srcp); err != nil {
			return nil, err
		}
		assets = append(assets, name)
	}
	meta := filepath.Join(u.gitDir, filepath.FromSlash(key+".parts.json"))
	if err := os.MkdirAll(filepath.Dir(meta), 0o750); err != nil {
		return nil, err
	}
	raw, _ := json.MarshalIndent(map[string]any{"sha256": sum, "size": n, "parts": assets}, "", "  ")
	if err := os.WriteFile(meta, raw, 0o644); err != nil {
		return nil, err
	}
	b.Store = "parts"
	b.Parts = assets
	return b, nil
}

func (u *snapUploader) getFile(b SnapBlob, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	switch b.Store {
	case "git":
		src := filepath.Join(u.gitDir, filepath.FromSlash(firstNonEmpty(b.GitPath, b.Key)))
		return copyFile(src, dest)
	case "release":
		tag := firstNonEmpty(b.ReleaseTag, RoomSnapRelease)
		return u.gh.DownloadReleaseFile(u.repo, tag, b.Asset, dest)
	case "parts":
		tmp, err := os.MkdirTemp("", "vps-join-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		var paths []string
		tag := firstNonEmpty(b.ReleaseTag, RoomSnapRelease)
		for i, name := range b.Parts {
			p := filepath.Join(tmp, fmt.Sprintf("p-%03d", i))
			if err := u.gh.DownloadReleaseFile(u.repo, tag, name, p); err != nil {
				return err
			}
			paths = append(paths, p)
		}
		return JoinChunks(paths, dest)
	default:
		return fmt.Errorf("unknown blob store %q for %s", b.Store, b.Key)
	}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

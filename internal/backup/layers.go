package backup

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type dockerSaveManifest struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
	Layers   []string `json:"Layers"`
}

func unpackSaveArchive(tarPath, dest string) error {
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return err
	}
	cmd := exec.Command("tar", "-C", dest, "-xf", tarPath)
	if b, err := cmd.CombinedOutput(); err != nil {
		if e2 := unpackSaveGo(tarPath, dest); e2 != nil {
			return fmt.Errorf("untar image: %s: %w", strings.TrimSpace(string(b)), err)
		}
	}
	return nil
}

func unpackSaveGo(tarPath, dest string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p := filepath.Join(dest, hdr.Name)
		if hdr.FileInfo().IsDir() {
			_ = os.MkdirAll(p, 0o750)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(p), 0o750)
		out, err := os.Create(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		out.Close()
		if err != nil {
			return err
		}
	}
}

func packDirTar(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	cmd := exec.Command("tar", "-C", src, "-cf", dest, ".")
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar image: %s: %w", strings.TrimSpace(string(b)), err)
	}
	return nil
}

func classifyBlob(rel string, size int64) bool {
	n := strings.ToLower(filepath.ToSlash(rel))
	if strings.HasSuffix(n, "/layer.tar") || n == "layer.tar" {
		return true
	}
	if strings.Contains(n, "blobs/sha256/") && size >= 256 {
		return true
	}
	return false
}

func splitImageTree(root string) (meta []string, blobs []string, format string, tags []string, err error) {
	format = "docker-save"
	if _, e := os.Stat(filepath.Join(root, "oci-layout")); e == nil {
		format = "oci"
	}
	manPath := filepath.Join(root, "manifest.json")
	if b, e := os.ReadFile(manPath); e == nil {
		var mans []dockerSaveManifest
		if json.Unmarshal(b, &mans) == nil {
			for _, m := range mans {
				tags = append(tags, m.RepoTags...)
			}
		}
	}
	err = filepath.Walk(root, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() {
			return werr
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if classifyBlob(rel, info.Size()) {
			blobs = append(blobs, rel)
		} else {
			meta = append(meta, rel)
		}
		return nil
	})
	return meta, blobs, format, tags, err
}

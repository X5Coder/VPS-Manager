package stack

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func LooksLikeMultiPackage(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.HasSuffix(n, ".tar.gz") || strings.HasSuffix(n, ".tgz")
}

func LooksLikeSingleTar(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if strings.HasSuffix(n, ".tar.gz") || strings.HasSuffix(n, ".tgz") {
		return false
	}
	return strings.HasSuffix(n, ".tar")
}

func FilenameKind(name string) string {
	if LooksLikeMultiPackage(name) {
		return "multi"
	}
	if LooksLikeSingleTar(name) {
		return "single"
	}
	return ""
}

func withTarEntries(path string, max int, fn func(name string) bool) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var r io.Reader = f
	hdr := make([]byte, 2)
	n, _ := io.ReadFull(f, hdr)
	_, _ = f.Seek(0, io.SeekStart)
	if n == 2 && hdr[0] == 0x1f && hdr[1] == 0x8b {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return false
		}
		defer gz.Close()
		r = gz
	}
	tr := tar.NewReader(r)
	for i := 0; i < max; i++ {
		h, err := tr.Next()
		if err != nil {
			return false
		}
		if fn(strings.TrimPrefix(filepath.ToSlash(h.Name), "./")) {
			return true
		}
	}
	return false
}

// LooksLikeDockerSave is true for docker save / OCI image tars (manifest.json at the root).
func LooksLikeDockerSave(path string) bool {
	return withTarEntries(path, 80, func(n string) bool {
		base := filepath.Base(n)
		return base == "manifest.json" || base == "index.json" || base == "oci-layout" || n == "repositories"
	})
}

func detectPackageContent(path string) string {
	if ArchiveHasCompose(path) {
		return "multi"
	}
	if LooksLikeDockerSave(path) {
		return "single"
	}
	return ""
}

// CheckUpload looks at the archive contents (not the filename) vs the room kind.
// containerID allows a single image onto one service of a multi room.
func CheckUpload(fname, path, roomKind, containerID string, emptyRoom bool) error {
	fname = filepath.Base(fname)
	if FilenameKind(fname) == "" && !LooksLikeArchive(fname) {
		return fmt.Errorf("package_empty: send a .tar (one image) or .tar.gz (compose + images)")
	}
	content := detectPackageContent(path)
	if content == "" {
		return fmt.Errorf("package_invalid: not a docker save image and not a compose stack. One image: docker save -o app.tar IMAGE. Multi: compose.yml + images/*.tar inside a .tar.gz")
	}
	if emptyRoom {
		return nil
	}
	rk := strings.ToLower(strings.TrimSpace(roomKind))
	if rk == "multi" && content == "single" && strings.TrimSpace(containerID) == "" {
		return fmt.Errorf("package_kind_mismatch: this room is multi. Send a compose stack, or send one image with container_id")
	}
	if rk == "single" && content == "multi" {
		return fmt.Errorf("package_kind_mismatch: this room is single. Send one docker-save image, not a compose stack")
	}
	if content == "multi" && strings.TrimSpace(containerID) != "" {
		return fmt.Errorf("package_kind_mismatch: container_id is for one image, not a compose stack")
	}
	return nil
}

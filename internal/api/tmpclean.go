package api

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SweepStaleUploads removes leftover docker-load temp dirs if a deploy was killed
// before defer cleanup (disk-full / restart). Multipart overflow files are owned
// by net/http and cleaned via MultipartForm.RemoveAll on each upload.
func SweepStaleUploads(olderThan time.Duration) {
	dir := os.TempDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-olderThan)
	for _, e := range ents {
		name := e.Name()
		if !strings.HasPrefix(name, "vm-api-tar-") && !strings.HasPrefix(name, "vm-load-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, name))
	}
}

func wipeHostDirContents(dir string) error {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" || dir == "/" || dir == "." {
		return os.ErrInvalid
	}
	st, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !st.IsDir() {
		return os.Remove(dir)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

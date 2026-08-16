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

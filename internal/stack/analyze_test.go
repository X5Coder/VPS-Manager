package stack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnalyzeComposeDir(t *testing.T) {
	dir := t.TempDir()
	body := `services:
  backend:
    image: backend:latest
  frontend:
    image: frontend:latest
volumes:
  data:
networks:
  app:
`
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	info := AnalyzeComposeDir(dir)
	if !info.OK || len(info.Services) != 2 || len(info.Images) != 2 {
		t.Fatalf("%+v", info)
	}
}

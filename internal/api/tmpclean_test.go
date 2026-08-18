package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWipeHostDirContents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := wipeHostDirContents(dir); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Fatalf("expected empty dir, got %v", ents)
	}
	if err := wipeHostDirContents("/no/such/dir"); err != nil {
		t.Fatal("missing dir should be ok")
	}
	if err := wipeHostDirContents("/"); err == nil {
		t.Fatal("must not wipe /")
	}
}

package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkipBackupKeepsImagesAndAppData(t *testing.T) {
	if !backupHasPath([]FileEntry{{Path: "runtime/p/__container_image.tar.gz", Size: 10, Chunks: []string{"a"}}}, "__container_image.tar") {
		t.Fatal("detect gzipped local image")
	}
	if skipBackupFile("runtime/p/__container_image.tar", 2*1024*1024*1024) {
		t.Fatal("image tar must be backed up")
	}
	if skipBackupRel("app/node_modules/foo") {
		t.Fatal("node_modules must be backed up for bind-mount apps")
	}
	if skipBackupRel("libraries/venv/lib/python3.10/site-packages/x") {
		t.Fatal("venv must be backed up")
	}
	if !skipBackupRel("files/.cache/huggingface/x") {
		t.Fatal("hf cache skipped for faster upload; model lives in the local image")
	}
	if skipBackupFile("model.safetensors", 470*1024*1024) {
		t.Fatal("model weights must be backed up")
	}
	if skipBackupFile("weights.bin", 40*1024*1024) {
		t.Fatal("large app blobs must be backed up")
	}
	if skipBackupFile("__dumps/postgres.sql.gz", 80*1024*1024) {
		t.Fatal("dumps must stay")
	}
	if skipBackupFile("data/database.sqlite", 80*1024*1024) {
		t.Fatal("sqlite user data must stay")
	}
	if !skipBackupRel("profiles/x/gpucache/a") {
		t.Fatal("chrome gpu cache still skipped")
	}
	if !skipBackupRel("app/__pycache__/x") {
		t.Fatal("pycache still skipped")
	}
	if skipBackupRel("volumes/db/data/base") {
		t.Fatal("live postgres files must be copied when no dump (catalog skipPG is separate)")
	}
}

func TestTarGzPathPacksBindTree(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "libraries", "venv")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "x.py"), []byte("print(1)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "tree.tar.gz")
	if err := tarGzPath(filepath.Join(dir, "libraries"), dest); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dest)
	if err != nil || st.Size() < 20 {
		t.Fatalf("archive missing size=%v err=%v", st, err)
	}
}

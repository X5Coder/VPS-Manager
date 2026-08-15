package backup

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"
)

func TestLayerAssetName(t *testing.T) {
	got := LayerAssetName("sha256:0123456789abcdef")
	if got != "l-0123456789abcdef" {
		t.Fatalf("got %s", got)
	}
}

func TestSharedRepos(t *testing.T) {
	if !sharedBackupRepo(ImagesRepo) || !sharedBackupRepo(VolumeRepoName(2)) {
		t.Fatal("shared repos must be protected")
	}
	if sharedBackupRepo("admin-backup-001") {
		t.Fatal("old per-room repo is not shared")
	}
}

func TestChunkFileSizeRoundtrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	payload := make([]byte, 3000)
	for i := range payload {
		payload[i] = byte(i)
	}
	if err := os.WriteFile(src, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	parts, err := ChunkFileSize(src, dir, "x", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 {
		t.Fatalf("parts=%d", len(parts))
	}
	var locals []string
	for _, p := range parts {
		locals = append(locals, filepath.Join(dir, p))
	}
	dest := filepath.Join(dir, "out.bin")
	if err := JoinChunks(locals, dest); err != nil {
		t.Fatal(err)
	}
	sum1, n1, _ := HashFile(src)
	sum2, n2, _ := HashFile(dest)
	if sum1 != sum2 || n1 != n2 {
		t.Fatalf("mismatch %s %s %d %d", sum1, sum2, n1, n2)
	}
}

func TestUnpackPackDockerSave(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "img")
	_ = os.MkdirAll(filepath.Join(srcDir, "abc"), 0o750)
	_ = os.WriteFile(filepath.Join(srcDir, "manifest.json"), []byte(`[{"Config":"cfg.json","RepoTags":["demo:latest"],"Layers":["abc/layer.tar"]}]`), 0o644)
	_ = os.WriteFile(filepath.Join(srcDir, "cfg.json"), []byte(`{"architecture":"amd64"}`), 0o644)
	_ = os.WriteFile(filepath.Join(srcDir, "abc", "layer.tar"), []byte("LAYERDATA"), 0o644)
	tarPath := filepath.Join(dir, "save.tar")
	if err := writeTar(srcDir, tarPath); err != nil {
		t.Fatal(err)
	}
	unpacked := filepath.Join(dir, "unpacked")
	if err := unpackSaveArchive(tarPath, unpacked); err != nil {
		t.Fatal(err)
	}
	meta, blobs, format, tags, err := splitImageTree(unpacked)
	if err != nil {
		t.Fatal(err)
	}
	if format != "docker-save" {
		t.Fatalf("format %s", format)
	}
	if len(tags) != 1 || tags[0] != "demo:latest" {
		t.Fatalf("tags %v", tags)
	}
	if len(blobs) != 1 || blobs[0] != "abc/layer.tar" {
		t.Fatalf("blobs %v", blobs)
	}
	if len(meta) < 2 {
		t.Fatalf("meta %v", meta)
	}
}

func TestVolumeRepoName(t *testing.T) {
	if VolumeRepoName(1) != "vps-manage-volumes-001" {
		t.Fatal(VolumeRepoName(1))
	}
}

func writeTar(src, dest string) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = tw.Write(b)
		return err
	})
}

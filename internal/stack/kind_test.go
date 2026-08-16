package stack

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilenameKind(t *testing.T) {
	if FilenameKind("app.tar") != "single" {
		t.Fatal("tar")
	}
	if FilenameKind("project.tar.gz") != "multi" {
		t.Fatal("tar.gz")
	}
	if FilenameKind("x.tgz") != "multi" {
		t.Fatal("tgz")
	}
	if FilenameKind("app.tar.gz") == "single" {
		t.Fatal(".tar.gz is not single")
	}
}

func TestCheckUploadMismatch(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "app.tar")
	f, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	hdr := &tar.Header{Name: "compose.yml", Mode: 0644, Size: 12}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write([]byte("services: {}\n"))
	tw.Close()
	f.Close()
	err = CheckUpload("app.tar", tarPath, "single", "", true)
	if err != nil {
		t.Fatalf("empty room accepts compose archive regardless of .tar name: %v", err)
	}
	err = CheckUpload("app.tar", tarPath, "single", "", false)
	if err == nil || !strings.Contains(err.Error(), "package_kind_mismatch") {
		t.Fatalf("occupied single room + compose, got %v", err)
	}
}

func TestCheckUploadEmptyNameAndFakeTar(t *testing.T) {
	err := CheckUpload("notes.bin", "/no/such", "single", "", true)
	if err == nil || !strings.Contains(err.Error(), "package_empty") {
		t.Fatalf("expected package_empty, got %v", err)
	}
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "not-docker.tar")
	f, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	hdr := &tar.Header{Name: "readme.txt", Mode: 0644, Size: 5}
	_ = tw.WriteHeader(hdr)
	_, _ = tw.Write([]byte("hello"))
	tw.Close()
	f.Close()
	err = CheckUpload("not-docker.tar", tarPath, "single", "", true)
	if err == nil || !strings.Contains(err.Error(), "package_invalid") {
		t.Fatalf("expected package_invalid, got %v", err)
	}
}

package stack

import (
	"fmt"
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

// CheckUpload enforces filename vs contents vs room kind.
// containerID allows a single .tar onto one service of a multi room.
func CheckUpload(fname, path, roomKind, containerID string, emptyRoom bool) error {
	fname = filepath.Base(fname)
	fk := FilenameKind(fname)
	if fk == "" {
		return fmt.Errorf("package_bad_name: single rooms need a .tar (docker save); multi rooms need a .tar.gz (compose.yml + images/*.tar)")
	}
	content := "single"
	if ArchiveHasCompose(path) {
		content = "multi"
	}
	if fk == "single" && content == "multi" {
		return fmt.Errorf("package_kind_mismatch: file is .tar (single) but the archive is a multi package (compose + images). Use .tar.gz and the multi GitHub Action")
	}
	if fk == "multi" && content != "multi" {
		return fmt.Errorf("package_kind_mismatch: file is .tar.gz (multi) but there is no compose .yml inside. Multi package must be:\n  compose.yml (any *.yml name)\n  images/image-01.tar …")
	}
	if emptyRoom {
		return nil
	}
	rk := strings.ToLower(strings.TrimSpace(roomKind))
	if rk == "multi" && content == "single" && strings.TrimSpace(containerID) == "" {
		return fmt.Errorf("package_kind_mismatch: this room is multi. Send a .tar.gz stack, or send a .tar with container_id to update one container")
	}
	if rk == "single" && content == "multi" {
		return fmt.Errorf("package_kind_mismatch: this room is single. Send a docker-save .tar, not a multi .tar.gz")
	}
	if fk == "multi" && strings.TrimSpace(containerID) != "" {
		return fmt.Errorf("package_kind_mismatch: container_id is for a single .tar on one container, not a multi .tar.gz")
	}
	return nil
}

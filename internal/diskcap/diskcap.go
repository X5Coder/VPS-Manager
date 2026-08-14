package diskcap

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const minBytes = 64 << 20 // 64 MiB

// Ensure makes dir a loop-backed ext4 filesystem of sizeBytes.
// Caller must stop processes using dir first. Safe to call again to resize.
func Ensure(dir string, sizeBytes int64) error {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" || dir == "/" {
		return fmt.Errorf("invalid quota dir")
	}
	if sizeBytes < minBytes {
		sizeBytes = minBytes
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	img := dir + ".img"
	mounted := isMount(dir)
	if !mounted {
		if err := migrateInto(dir); err != nil {
			return err
		}
		if err := createFS(img, sizeBytes); err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := mountLoop(img, dir); err != nil {
			return err
		}
		return restoreStaging(dir)
	}
	cur := fileSize(img)
	if abs64(cur-sizeBytes) < 1<<20 {
		return nil
	}
	if err := exec.Command("umount", dir).Run(); err != nil {
		return fmt.Errorf("umount %s: %w", dir, err)
	}
	if err := resizeImg(img, sizeBytes); err != nil {
		_ = mountLoop(img, dir)
		return err
	}
	return mountLoop(img, dir)
}

func migrateInto(dir string) error {
	st, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(dir+".staging", 0o755)
		}
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if isMount(dir) {
		return nil
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) == 0 {
		return nil
	}
	staging := dir + ".staging"
	_ = os.RemoveAll(staging)
	if err := os.Rename(dir, staging); err != nil {
		return err
	}
	return nil
}

func restoreStaging(dir string) error {
	staging := dir + ".staging"
	st, err := os.Stat(staging)
	if err != nil || !st.IsDir() {
		return nil
	}
	cmd := exec.Command("cp", "-a", staging+"/.", dir+"/")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("copy into quota disk: %s: %w", strings.TrimSpace(string(out)), err)
	}
	_ = os.RemoveAll(staging)
	return nil
}

func createFS(img string, sizeBytes int64) error {
	if fileSize(img) >= sizeBytes && hasExt4(img) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(img), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(img, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := f.Truncate(sizeBytes); err != nil {
		f.Close()
		return err
	}
	f.Close()
	cmd := exec.Command("mkfs.ext4", "-F", "-q", img)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.ext4: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func resizeImg(img string, sizeBytes int64) error {
	cur := fileSize(img)
	f, err := os.OpenFile(img, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := f.Truncate(sizeBytes); err != nil {
		f.Close()
		return err
	}
	f.Close()
	_ = exec.Command("e2fsck", "-f", "-y", img).Run()
	args := []string{img}
	if sizeBytes < cur {
		args = []string{"-M", img} // shrink as much as used; then exact
		if out, err := exec.Command("resize2fs", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("resize2fs shrink: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}
	cmd := exec.Command("resize2fs", img)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("resize2fs: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func mountLoop(img, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cmd := exec.Command("mount", "-o", "loop", img, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount loop: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func hasExt4(img string) bool {
	out, err := exec.Command("blkid", "-o", "value", "-s", "TYPE", img).CombinedOutput()
	return err == nil && strings.Contains(strings.ToLower(string(out)), "ext4")
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func isMount(dir string) bool {
	dir = filepath.Clean(dir)
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		out, e := exec.Command("findmnt", "-n", "-o", "TARGET", "--target", dir).Output()
		return e == nil && strings.TrimSpace(string(out)) == dir
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && filepath.Clean(fields[1]) == dir {
			return true
		}
	}
	return false
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// ParseSize is a helper for tests.
func ParseSize(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

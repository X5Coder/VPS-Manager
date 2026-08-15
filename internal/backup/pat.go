package backup

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func (g *GitHub) ProbeCreateUploadDelete() error {
	if g.User == "" {
		u, err := g.Validate()
		if err != nil {
			return err
		}
		g.User = u.Login
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := "vps-pat-probe-" + hex.EncodeToString(b)
	if err := g.EnsureRepo(name, "VPS Manager PAT probe — safe to delete"); err != nil {
		return fmt.Errorf("create test failed: %w", err)
	}
	root, err := os.MkdirTemp("", "vps-pat-probe-*")
	if err != nil {
		_ = g.deleteRepo(name, true)
		return err
	}
	defer os.RemoveAll(root)
	dir := filepath.Join(root, name)
	if err := g.CloneOrPull(name, dir); err != nil {
		if err2 := initGitRepo(dir, g, name); err2 != nil {
			_ = g.deleteRepo(name, true)
			return fmt.Errorf("create test failed: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "probe.txt"), []byte("ok "+time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		_ = g.deleteRepo(name, true)
		return err
	}
	if err := g.CommitPush(dir, "pat probe"); err != nil {
		_ = g.deleteRepo(name, true)
		return fmt.Errorf("upload test failed: %w", err)
	}
	if err := g.deleteRepo(name, true); err != nil {
		return fmt.Errorf("delete test failed: %w", err)
	}
	return nil
}

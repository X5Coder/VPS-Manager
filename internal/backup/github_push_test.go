package backup

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitOk(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
}

func TestCommitPushRecoversWhenRemoteMoved(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	gitOk(t, "", "init", "--bare", "-b", "main", bare)

	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	gitOk(t, "", "clone", bare, a)
	gitOk(t, a, "config", "user.email", "a@test")
	gitOk(t, a, "config", "user.name", "A")
	if err := os.WriteFile(filepath.Join(a, "one.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOk(t, a, "add", "-A")
	gitOk(t, a, "commit", "-m", "one")
	gitOk(t, a, "push", "-u", "origin", "HEAD:main")

	gitOk(t, "", "clone", bare, b)
	gitOk(t, b, "config", "user.email", "b@test")
	gitOk(t, b, "config", "user.name", "B")
	if err := os.WriteFile(filepath.Join(b, "two.txt"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOk(t, b, "add", "-A")
	gitOk(t, b, "commit", "-m", "two")
	gitOk(t, b, "push", "origin", "HEAD:main")

	if err := os.WriteFile(filepath.Join(a, "three.txt"), []byte("three"), 0o600); err != nil {
		t.Fatal(err)
	}
	gh := NewGitHub("")
	if err := gh.CommitPush(a, "three"); err != nil {
		t.Fatalf("CommitPush should rebase onto moved main: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a, "two.txt")); err != nil {
		t.Fatalf("rebase should bring remote file two.txt: %v", err)
	}
}

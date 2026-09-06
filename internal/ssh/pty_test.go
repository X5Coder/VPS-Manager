package ssh

import (
	"strings"
	"testing"
	"time"
)

func TestStartShellEcho(t *testing.T) {
	sh, err := StartShell(80, 24)
	if err != nil {
		t.Fatalf("start shell: %v", err)
	}
	defer sh.Close()
	if err := sh.Resize(100, 30); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if _, err := sh.Master.Write([]byte("echo PTY_OK_123\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = sh.Master.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 8192)
	var out strings.Builder
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		n, err := sh.Master.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
			if strings.Contains(out.String(), "PTY_OK_123") {
				return
			}
		}
		if err != nil {
			break
		}
	}
	t.Fatalf("echo marker not seen, got %q", out.String())
}

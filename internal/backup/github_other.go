//go:build !unix

package backup

import (
	"os"
	"syscall"
)

func gitSysProcAttr() *syscall.SysProcAttr { return nil }

func killGitProcess(p *os.Process) {
	if p != nil {
		_ = p.Kill()
	}
}

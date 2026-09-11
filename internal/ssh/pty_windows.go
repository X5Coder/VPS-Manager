//go:build windows

package ssh

import (
	"errors"
	"os"
	"os/exec"
)

type PTYShell struct {
	Cmd    *exec.Cmd
	Master *os.File
}

func StartShell(cols, rows int) (*PTYShell, error) {
	return nil, errors.New("PTY shell is not supported on Windows")
}

func (p *PTYShell) Resize(cols, rows int) error {
	return errors.New("PTY shell is not supported on Windows")
}

func (p *PTYShell) Close() {
	if p == nil {
		return
	}
	if p.Cmd != nil && p.Cmd.Process != nil {
		_ = p.Cmd.Process.Kill()
		_, _ = p.Cmd.Process.Wait()
	}
	if p.Master != nil {
		_ = p.Master.Close()
	}
}

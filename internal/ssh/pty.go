package ssh

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// PTYShell is a root shell attached to a PTY master.
type PTYShell struct {
	Cmd    *exec.Cmd
	Master *os.File
}

// StartShell spawns an interactive bash (as the panel user, normally root)
// behind a real PTY so terminal apps, prompts and signals behave like SSH.
func StartShell(cols, rows int) (*PTYShell, error) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	masterFD, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("open ptmx: %w", err)
	}
	master := os.NewFile(uintptr(masterFD), "ptmx")
	// TIOCSPTLCK takes a POINTER to int — passing the value itself faults (EFAULT).
	if err := unix.IoctlSetPointerInt(masterFD, unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		return nil, fmt.Errorf("unlock pty: %w", err)
	}
	n, err := unix.IoctlGetInt(masterFD, unix.TIOCGPTN)
	if err != nil {
		master.Close()
		return nil, fmt.Errorf("pty number: %w", err)
	}
	ptsName := fmt.Sprintf("/dev/pts/%d", n)
	pts, err := os.OpenFile(ptsName, os.O_RDWR, 0)
	if err != nil {
		master.Close()
		return nil, fmt.Errorf("open pts: %w", err)
	}
	if err := setWinsize(masterFD, cols, rows); err != nil {
		master.Close()
		pts.Close()
		return nil, err
	}
	cmd := exec.Command("bash", "-l", "-i")
	cmd.Dir = "/root"
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"HOME=/root",
		"USER=root",
		"LOGNAME=root",
		"SHELL=/bin/bash",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/vps-manager/bin",
	)
	cmd.Stdin = pts
	cmd.Stdout = pts
	cmd.Stderr = pts
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		master.Close()
		pts.Close()
		return nil, fmt.Errorf("start shell: %w", err)
	}
	pts.Close() // child keeps its own copy on fds 0,1,2
	return &PTYShell{Cmd: cmd, Master: master}, nil
}

// Resize sets the PTY window size.
func (p *PTYShell) Resize(cols, rows int) error {
	if p == nil || p.Master == nil {
		return fmt.Errorf("no shell")
	}
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	return setWinsize(int(p.Master.Fd()), cols, rows)
}

func setWinsize(fd, cols, rows int) error {
	ws := &unix.Winsize{Row: uint16(rows), Col: uint16(cols)}
	return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, ws)
}

// Close kills the shell and releases the PTY.
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

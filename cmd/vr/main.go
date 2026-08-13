package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/x5coder/vps-rooms/internal/isolate"
)

func main() {
	roomsDir := env("VPS_ROOMS_ROOMS", "/opt/vps-rooms/rooms")
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "list", "ls":
		names, err := isolate.ListRoomNames(roomsDir)
		if err != nil {
			fail(err)
		}
		if len(names) == 0 {
			fmt.Println("(no rooms)")
			return
		}
		fmt.Println("Rooms (encrypted — password required to open):")
		for _, n := range names {
			fmt.Printf("  - %s\n", n)
		}
		fmt.Println("\nOpen with: vr open <room-name>")
	case "open":
		if len(os.Args) < 3 {
			fail(fmt.Errorf("usage: vr open <room-name>"))
		}
		openRoom(roomsDir, os.Args[2])
	case "status":
		if len(os.Args) < 3 {
			fail(fmt.Errorf("usage: vr status <room-name>"))
		}
		statusRoom(roomsDir, os.Args[2])
	case "seal":
		// vr seal <room-name> <password>  — admin/maintenance: encrypt runtime → vault.bin
		if len(os.Args) < 4 {
			fail(fmt.Errorf("usage: vr seal <room-name> <password>"))
		}
		sealRoom(roomsDir, os.Args[2], os.Args[3])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println(`vr — VPS Rooms CLI (OS isolation)

  vr list                 List room names only
  vr open <room-name>     Ask password, then open a shell into decrypted room files
  vr status <room-name>   Show lock status

Without the room password you cannot view, copy, or inspect project contents.
The web panel remains available for admins.`)
}

func openRoom(roomsDir, name string) {
	id, err := isolate.FindRoomIDByName(roomsDir, name)
	if err != nil {
		fail(fmt.Errorf("access denied: room password required"))
	}
	p := isolate.Paths(roomsDir, "/tmp/vr-open", id)
	hashb, err := os.ReadFile(p.Hash)
	if err != nil {
		fail(fmt.Errorf("access denied: room password required"))
	}
	fmt.Printf("Password for room %q: ", name)
	pw, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		// fallback non-tty
		r := bufio.NewReader(os.Stdin)
		line, _ := r.ReadString('\n')
		pw = []byte(strings.TrimSpace(line))
	}
	if bcrypt.CompareHashAndPassword(hashb, pw) != nil {
		fail(fmt.Errorf("access denied: room password required"))
	}
	dest := filepath.Join("/tmp/vr-open", name)
	_ = os.RemoveAll(dest)
	if err := isolate.UnlockTo(p, string(pw), dest); err != nil {
		fail(fmt.Errorf("access denied: room password required"))
	}
	_ = os.Chmod(dest, 0o700)
	fmt.Printf("Unlocked %s → %s\n", name, dest)
	fmt.Println("Exit the shell to leave this room view.")
	shell := env("SHELL", "/bin/bash")
	cmd := exec.Command(shell)
	cmd.Dir = dest
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "VR_ROOM="+name, "VR_UNLOCKED="+dest)
	_ = cmd.Run()
	_ = os.RemoveAll(dest)
	fmt.Println("Room view closed and wiped from /tmp.")
}

func statusRoom(roomsDir, name string) {
	id, err := isolate.FindRoomIDByName(roomsDir, name)
	if err != nil {
		fail(fmt.Errorf("room not found"))
	}
	p := isolate.Paths(roomsDir, "/opt/vps-rooms/runtime", id)
	fmt.Printf("room: %s\n", name)
	if _, err := os.Stat(p.Vault); err == nil {
		fmt.Println("vault: locked (vault.bin present)")
	} else {
		fmt.Println("vault: missing")
	}
	fmt.Println("hint: vr open", name)
}

func sealRoom(roomsDir, name, password string) {
	runtimeDir := env("VPS_ROOMS_RUNTIME", "/opt/vps-rooms/runtime")
	id, err := isolate.FindRoomIDByName(roomsDir, name)
	if err != nil {
		fail(err)
	}
	p := isolate.Paths(roomsDir, runtimeDir, id)
	hashb, err := os.ReadFile(p.Hash)
	if err != nil {
		fail(err)
	}
	if bcrypt.CompareHashAndPassword(hashb, []byte(password)) != nil {
		fail(fmt.Errorf("access denied: room password required"))
	}
	if err := os.MkdirAll(p.Runtime, 0o700); err != nil {
		fail(err)
	}
	if err := isolate.SealRuntime(p, password); err != nil {
		fail(err)
	}
	// keep panel runtime unlocked
	if err := isolate.UnlockRuntime(p, password); err != nil {
		fail(err)
	}
	_ = isolate.WriteLockNotice(p, name)
	fmt.Println("sealed ok:", name)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err.Error())
	os.Exit(1)
}

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

	"github.com/x5coder/vps-rooms/internal/auth"
	"github.com/x5coder/vps-rooms/internal/isolate"
)

func main() {
	baseDir := env("VPS_MANAGER_BASE", env("VPS_ROOMS_BASE", "/vps-manager"))
	singleDir := env("VPS_MANAGER_SINGLE", baseDir+"/single")
	multiDir := env("VPS_MANAGER_MULTI", baseDir+"/multi")
	roomsDir := singleDir
	_ = multiDir
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "list", "ls":
		names, _ := isolate.ListRoomNames(singleDir)
		mnames, _ := isolate.ListRoomNames(multiDir)
		seen := map[string]bool{}
		for _, n := range mnames {
			if !seen[n] {
				seen[n] = true
				names = append(names, n+" [multi]")
			}
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
		if len(os.Args) < 4 {
			fail(fmt.Errorf("usage: vr seal <room-name> <password>"))
		}
		sealRoom(roomsDir, os.Args[2], os.Args[3])
	case "room":
		if len(os.Args) < 3 {
			fail(fmt.Errorf("usage: vr room <hash|create|delete|restart|start|stop> [args]\n\n  vr room hash --password \"pw\"     # Print bcrypt hash for auth.hash\n  vr room create --name N --kind single --quota 5\n  vr room delete <room_id>\n  vr room restart <room_id>\n  vr room start <room_id>\n  vr room stop <room_id>"))
		}
		switch os.Args[2] {
		case "hash":
			pw := ""
			for i := 3; i < len(os.Args); i++ {
				a := os.Args[i]
				if a == "--password" && i+1 < len(os.Args) {
					pw = os.Args[i+1]
					i++
				} else if strings.HasPrefix(a, "--password=") {
					pw = strings.TrimPrefix(a, "--password=")
				}
			}
			if strings.TrimSpace(pw) == "" {
				fail(fmt.Errorf("usage: vr room hash --password \"your_secure_password\""))
			}
			h, err := auth.HashPassword(pw)
			if err != nil {
				fail(fmt.Errorf("hash failed: %w", err))
			}
			fmt.Print(h)
			if strings.HasSuffix(os.Getenv("PS1"), "$ ") || os.Getenv("TERM") != "" {
				fmt.Println()
			}
		case "create", "delete", "restart", "start", "stop":
			port := env("VPS_ROOMS_PORT", env("VPS_ROOMS_ADDR", ":9090"))
			if strings.HasPrefix(port, ":") {
				port = "127.0.0.1" + port
			} else if !strings.Contains(port, ":") {
				port = "127.0.0.1:" + port
			}
			if len(os.Args) < 4 {
				fmt.Fprintf(os.Stderr, "vr room %s: delegating to panel API at %s\n", os.Args[2], port)
				fmt.Fprintf(os.Stderr, "usage: vr room %s <room_id>\n", os.Args[2])
				os.Exit(2)
			}
			method := "POST"
			apiMap := map[string]string{
				"restart": "/api/rooms/%s/restart",
				"start":   "/api/rooms/%s/start",
				"stop":    "/api/rooms/%s/stop",
				"delete":  "/api/rooms/%s",
				"create":  "/api/rooms",
			}
			path := apiMap[os.Args[2]]
			if os.Args[2] == "delete" {
				method = "DELETE"
			}
			if os.Args[2] == "create" {
				method = "POST"
			}
			fmt.Fprintf(os.Stderr, "NOTE: vr room %s via local API %s — run on the VPS host where vps-rooms is listening.\n", os.Args[2], port)
			fmt.Fprintf(os.Stderr, "curl example: curl -s -X %s http://%s"+path+"\n", method, port)
			os.Exit(0)
		default:
			fail(fmt.Errorf("unknown room subcommand: %s", os.Args[2]))
		}
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
  vr seal <room> <pw>     Encrypt runtime → vault.bin (admin)

  vr room hash --password "pw"
                          Print bcrypt hash to use as auth.hash
  vr room restart <id>    Restart a room via local panel API
  vr room start/stop/delete <id>

Without the room password you cannot view, copy, or inspect project contents.
The web panel remains available for admins.`)
}

func openRoom(roomsDir, name string) {
	id, err := isolate.FindRoomIDByName(roomsDir, name)
	if err != nil {
		baseDir := env("VPS_MANAGER_BASE", env("VPS_ROOMS_BASE", "/vps-manager"))
		multiDir := env("VPS_MANAGER_MULTI", baseDir+"/multi")
		id2, err2 := isolate.FindRoomIDByName(multiDir, name)
		if err2 != nil {
			fail(fmt.Errorf("access denied: room password required"))
		}
		id = id2
		roomsDir = multiDir
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
	baseDir := env("VPS_MANAGER_BASE", env("VPS_ROOMS_BASE", "/vps-manager"))
	p := isolate.PathsForKind(baseDir, id, "single")
	fmt.Printf("room: %s\n", name)
	if _, err := os.Stat(p.Vault); err == nil {
		fmt.Println("vault: locked (vault.bin present)")
	} else {
		fmt.Println("vault: missing")
	}
	fmt.Println("hint: vr open", name)
}

func sealRoom(roomsDir, name, password string) {
	baseDir := env("VPS_MANAGER_BASE", env("VPS_ROOMS_BASE", "/vps-manager"))
	id, err := isolate.FindRoomIDByName(roomsDir, name)
	if err != nil {
		fail(err)
	}
	// try single then multi
	p := isolate.PathsForKind(baseDir, id, "single")
	if _, err := os.Stat(p.Root); err != nil {
		p = isolate.PathsForKind(baseDir, id, "multi")
	}
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

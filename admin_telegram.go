package main

import (
	"bufio"
	"crypto/subtle"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/x5coder/vps-rooms/internal/config"
	"github.com/x5coder/vps-rooms/internal/telegram"
)

func runSetTelegramID() int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "Run as root on the VPS (SSH with the VPS password first).")
		fmt.Fprintln(os.Stderr, "  ssh root@YOUR_VPS_IP")
		fmt.Fprintln(os.Stderr, "  /opt/vps-rooms/bin/vps-rooms set-telegram-id")
		return 1
	}
	cfg := config.Load()
	fmt.Print("Panel admin password: ")
	pw, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not read password")
		return 1
	}
	want := []byte(cfg.OwnerPass)
	got := []byte(strings.TrimSpace(string(pw)))
	if len(want) == 0 || subtle.ConstantTimeCompare(want, got) != 1 {
		fmt.Fprintln(os.Stderr, "panel password rejected")
		return 1
	}
	fmt.Print("New Telegram user id: ")
	in := bufio.NewReader(os.Stdin)
	line, _ := in.ReadString('\n')
	id := strings.TrimSpace(line)
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		fmt.Fprintln(os.Stderr, "invalid Telegram id (numeric, from @userinfobot)")
		return 1
	}
	g := telegram.NewGate(cfg.DataDir)
	if err := g.ReplaceOwnerChatID(id); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("Owner Telegram id updated.")
	if err := exec.Command("systemctl", "restart", "vps-rooms.service").Run(); err != nil {
		fmt.Println("Restart the panel:  systemctl restart vps-rooms.service")
		return 0
	}
	fmt.Println("Panel restarted.")
	return 0
}

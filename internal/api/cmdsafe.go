package api

import (
	"strings"
	"unicode"
)

// commandDangerous flags destructive / remote-login style commands so the
// generic terminal-exec endpoints never run them.
func commandDangerous(cmd string) bool {
	c := strings.ToLower(strings.TrimSpace(cmd))
	c = strings.Join(strings.Fields(c), " ")
	needles := []string{
		"rm -rf /", "rm -rf /*", "mkfs", "shutdown", "reboot", "halt",
		":(){", "dd if=", "wipefs", "> /dev/sd",
	}
	for _, n := range needles {
		if strings.Contains(c, n) {
			return true
		}
	}
	if looksLikeRemoteLogin(c) {
		return true
	}
	if strings.Count(c, "controlmaster") > 1 || strings.Count(c, "controlpath") > 1 {
		return true
	}
	if strings.Contains(c, "rm ") && strings.Contains(c, " /vps-manager") {
		return true
	}
	for _, r := range c {
		if unicode.Is(unicode.C, r) && r != '\n' && r != '\t' {
			return true
		}
	}
	return false
}

// looksLikeRemoteLogin is true for ssh/sshpass/scp/sftp used as a login tool.
func looksLikeRemoteLogin(cmd string) bool {
	c := strings.ToLower(strings.TrimSpace(cmd))
	c = strings.Join(strings.Fields(c), " ")
	if c == "" {
		return false
	}
	if strings.Contains(c, "sshpass") {
		return true
	}
	fields := strings.Fields(c)
	bin := fields[0]
	if i := strings.LastIndex(bin, "/"); i >= 0 {
		bin = bin[i+1:]
	}
	switch bin {
	case "ssh", "scp", "sftp":
		return true
	}
	return false
}

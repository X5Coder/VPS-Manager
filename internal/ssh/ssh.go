package ssh

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var portRe = regexp.MustCompile(`(?i)^\s*Port\s+(\d+)`)

// ReadPort parses /etc/ssh/sshd_config + sshd_config.d/*.conf (first Port wins, fallback 22).
func ReadPort() int {
	candidates := []string{"/etc/ssh/sshd_config"}
	if ents, err := os.ReadDir("/etc/ssh/sshd_config.d"); err == nil {
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".conf") {
				candidates = append(candidates, filepath.Join("/etc/ssh/sshd_config.d", e.Name()))
			}
		}
	}
	for _, f := range candidates {
		if p := portInFile(f); p > 0 {
			return p
		}
	}
	return 22
}

func portInFile(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := portRe.FindStringSubmatch(line); m != nil {
			var p int
			for _, c := range m[1] {
				p = p*10 + int(c-'0')
			}
			if p > 0 && p < 65536 {
				return p
			}
		}
	}
	return 0
}

// Active reports whether sshd is running.
func Active() bool {
	if err := exec.Command("systemctl", "is-active", "--quiet", "ssh").Run(); err == nil {
		return true
	}
	if err := exec.Command("systemctl", "is-active", "--quiet", "sshd").Run(); err == nil {
		return true
	}
	if out, err := exec.Command("pgrep", "-x", "sshd").Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return true
	}
	return false
}

// RootLogin reports PermitRootLogin value (or "prohibit-password" default).
func RootLogin() string {
	for _, f := range []string{"/etc/ssh/sshd_config"} {
		v := rootLoginInFile(f)
		if v != "" {
			return v
		}
	}
	return "prohibit-password"
}

func rootLoginInFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		low := strings.ToLower(line)
		if strings.HasPrefix(low, "permitrootlogin") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return fields[1]
			}
		}
	}
	return ""
}

// AuthorizedKeys lists root's authorized_keys fingerprints (key type + comment, no secret).
func AuthorizedKeys() []map[string]string {
	home := "/root/.ssh/authorized_keys"
	if h := os.Getenv("HOME"); strings.HasPrefix(home, "/root") && os.Getenv("USER") != "" && os.Getenv("USER") != "root" {
		_ = h
	}
	b, err := os.ReadFile(home)
	if err != nil {
		return []map[string]string{}
	}
	var out []map[string]string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		typ := fields[0]
		comment := ""
		if len(fields) > 2 {
			comment = fields[len(fields)-1]
		}
		fp := ""
		if len(fields[1]) >= 12 {
			fp = fields[1][len(fields[1])-12:]
		}
		out = append(out, map[string]string{"type": typ, "comment": comment, "fingerprint": "…"+fp})
	}
	if out == nil {
		return []map[string]string{}
	}
	return out
}

// AddKey appends a public key to root's authorized_keys.
func AddKey(pub string) error {
	pub = strings.TrimSpace(pub)
	if pub == "" || strings.Contains(pub, "\n") {
		return errInvalid("invalid public key")
	}
	fields := strings.Fields(pub)
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "ssh-") {
		return errInvalid("key must start with ssh-rsa / ssh-ed25519 …")
	}
	dir := "/root/.ssh"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "authorized_keys"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(pub + "\n")
	return err
}

type invalidError string

func (e invalidError) Error() string { return string(e) }
func errInvalid(s string) error      { return invalidError(s) }

// SavedRootPassword returns the VPS root password saved by the panel
// (data/secrets/host.env). Empty when the panel never stored one.
func SavedRootPassword(dataDir string) string {
	b, err := os.ReadFile(filepath.Join(dataDir, "secrets", "host.env"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "VPS_ROOT_PASS=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "VPS_ROOT_PASS="))
		}
	}
	return ""
}

// ConnectionString builds `ssh root@HOST -p PORT` for display in the breadcrumb.
func ConnectionString(publicIP string, port int) string {
	host := strings.TrimSpace(publicIP)
	if host == "" {
		host = "YOUR_VPS_IP"
	}
	if port == 22 {
		return "ssh root@" + host
	}
	return "ssh root@" + host + " -p " + itoa(port)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

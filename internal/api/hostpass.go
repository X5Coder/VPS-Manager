package api

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func setHostRootPassword(password string) error {
	password = strings.TrimRight(password, "\n\r")
	if len(password) < 8 || strings.ContainsAny(password, "\n\r") {
		return fmt.Errorf("password must be at least 8 characters")
	}
	_ = exec.Command("mount", "-o", "remount,rw", "/").Run()
	_ = exec.Command("chattr", "-i", "/etc/shadow").Run()
	_ = exec.Command("chattr", "-i", "/etc/passwd").Run()

	cmd := exec.Command("chpasswd", "-c", "SHA512")
	cmd.Stdin = strings.NewReader("root:" + password + "\n")
	if out, err := cmd.CombinedOutput(); err == nil {
		return nil
	} else {
		pamErr := strings.TrimSpace(string(out)) + " " + err.Error()
		hash, herr := opensslPasswdHash(password)
		if herr != nil {
			return fmt.Errorf("%s", pamErr)
		}
		if out, err := exec.Command("usermod", "-p", hash, "root").CombinedOutput(); err == nil {
			return nil
		} else {
			pamErr = pamErr + "; usermod: " + strings.TrimSpace(string(out))
		}
		if err := applyRootShadowHash("/etc/shadow", hash); err != nil {
			return fmt.Errorf("%s; shadow: %v", pamErr, err)
		}
		return nil
	}
}

func opensslPasswdHash(password string) (string, error) {
	cmd := exec.Command("openssl", "passwd", "-6", "-stdin")
	cmd.Stdin = strings.NewReader(password)
	out, err := cmd.Output()
	if err != nil {
		cmd = exec.Command("openssl", "passwd", "-6", password)
		out, err = cmd.Output()
		if err != nil {
			return "", err
		}
	}
	hash := strings.TrimSpace(string(out))
	if !strings.HasPrefix(hash, "$6$") {
		return "", fmt.Errorf("openssl did not return a SHA-512 hash")
	}
	return hash, nil
}

func applyRootShadowHash(path, hash string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	updated, err := replaceRootShadowHash(b, hash)
	if err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, updated, st.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Chmod(tmp, st.Mode().Perm()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func replaceRootShadowHash(raw []byte, hash string) ([]byte, error) {
	if hash == "" || strings.ContainsAny(hash, ":\n") {
		return nil, fmt.Errorf("invalid hash")
	}
	text := string(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")))
	lines := strings.Split(text, "\n")
	found := false
	for i, line := range lines {
		if line == "" && i == len(lines)-1 {
			continue
		}
		if !strings.HasPrefix(line, "root:") {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			return nil, fmt.Errorf("malformed root shadow line")
		}
		lines[i] = "root:" + hash + ":" + parts[2]
		found = true
		break
	}
	if !found {
		return nil, fmt.Errorf("root line missing in shadow")
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out), nil
}

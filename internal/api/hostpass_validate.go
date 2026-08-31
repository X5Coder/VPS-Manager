package api

import (
	"fmt"
	"strings"
	"unicode"
)

// validateLinuxRootPassword checks rules that match typical Linux/SSH password use
// (shadow-safe, printable, usable with chpasswd / OpenSSH password auth).
func validateLinuxRootPassword(password string) error {
	password = strings.TrimRight(password, "\n\r")
	if password != strings.TrimSpace(password) {
		return fmt.Errorf("password must not start or end with spaces")
	}
	n := len(password)
	if n < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	if n > 128 {
		return fmt.Errorf("password must be at most 128 characters")
	}
	if strings.ContainsAny(password, ":\n\r\x00") {
		return fmt.Errorf("password must not contain colon or control characters")
	}
	hasLetter, hasDigit := false, false
	for _, r := range password {
		if r < 32 || r == 127 {
			return fmt.Errorf("password must be printable ASCII (no control characters)")
		}
		if r > 126 {
			return fmt.Errorf("password must use printable ASCII characters only (SSH-safe)")
		}
		if unicode.IsLetter(r) {
			hasLetter = true
		}
		if unicode.IsDigit(r) {
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return fmt.Errorf("password must include at least one letter and one digit")
	}
	return nil
}

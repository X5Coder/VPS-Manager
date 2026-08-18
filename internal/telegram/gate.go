package telegram

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	OTPTTL      = 20 * time.Minute
	DeniedMsg   = "Server stopped"
	secretsName = "telegram.env"
)

type Secrets struct {
	BotToken string
	ChatID   string
	Locked   bool
}

type Gate struct {
	mu         sync.Mutex
	secretsDir string
	pending    *pendingOTP
}

type pendingOTP struct {
	TokenHash string
	CodeHash  string
	ExpiresAt time.Time
}

func NewGate(dataDir string) *Gate {
	dir := filepath.Join(dataDir, "secrets")
	_ = os.MkdirAll(dir, 0o700)
	return &Gate{
		secretsDir: dir,
	}
}

func (g *Gate) SecretsPath() string {
	return filepath.Join(g.secretsDir, secretsName)
}

// EnsureOwnerChatID writes the owner Telegram chat id once and locks it forever.
// If a locked chat id already exists, it is never changed (even if want differs).
func (g *Gate) EnsureOwnerChatID(want string) error {
	want = strings.TrimSpace(want)
	if want == "" {
		return nil
	}
	if _, err := strconv.ParseInt(want, 10, 64); err != nil {
		return fmt.Errorf("invalid chat id")
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	sec, err := g.loadSecretsUnlocked()
	if err != nil {
		return err
	}
	if sec.ChatID != "" && sec.Locked {
		// immutable
		return nil
	}
	if sec.ChatID != "" && sec.ChatID != want {
		// already set to something else — keep existing, lock it
		sec.Locked = true
		return g.saveSecretsUnlocked(*sec)
	}
	sec.ChatID = want
	sec.Locked = true
	return g.saveSecretsUnlocked(*sec)
}

// ReplaceOwnerChatID overwrites a locked owner chat id. CLI only (root + panel password).
func (g *Gate) ReplaceOwnerChatID(want string) error {
	want = strings.TrimSpace(want)
	if want == "" {
		return fmt.Errorf("chat id required")
	}
	if _, err := strconv.ParseInt(want, 10, 64); err != nil {
		return fmt.Errorf("invalid chat id")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	sec, err := g.loadSecretsUnlocked()
	if err != nil {
		return err
	}
	sec.ChatID = want
	sec.Locked = true
	return g.saveSecretsUnlocked(*sec)
}

func (g *Gate) loadSecretsUnlocked() (*Secrets, error) {
	b, err := os.ReadFile(g.SecretsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &Secrets{}, nil
		}
		return nil, err
	}
	s := &Secrets{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "TELEGRAM_BOT_TOKEN":
			s.BotToken = strings.TrimSpace(v)
		case "TELEGRAM_CHAT_ID":
			s.ChatID = strings.TrimSpace(v)
		case "TELEGRAM_CHAT_LOCKED":
			s.Locked = strings.TrimSpace(v) == "1" || strings.EqualFold(strings.TrimSpace(v), "true")
		}
	}
	if s.ChatID != "" {
		s.Locked = true // any stored chat id is treated as locked
	}
	return s, nil
}

func (g *Gate) LoadSecrets() (*Secrets, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.loadSecretsUnlocked()
}

func (g *Gate) saveSecretsUnlocked(s Secrets) error {
	if err := os.MkdirAll(g.secretsDir, 0o700); err != nil {
		return err
	}
	locked := "1"
	if !s.Locked && s.ChatID == "" {
		locked = "0"
	}
	content := fmt.Sprintf(
		"# VPS Rooms — DO NOT EDIT. Chat ID is locked and never exposed via API.\nTELEGRAM_CHAT_ID=%s\nTELEGRAM_CHAT_LOCKED=%s\nTELEGRAM_BOT_TOKEN=%s\n",
		s.ChatID, locked, s.BotToken,
	)
	path := g.SecretsPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	_ = os.Chmod(g.secretsDir, 0o700)
	return nil
}

func (g *Gate) HasLockedChat() bool {
	s, err := g.LoadSecrets()
	return err == nil && s.ChatID != ""
}

func (g *Gate) Configured() bool {
	s, err := g.LoadSecrets()
	return err == nil && s.ChatID != ""
}

func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

func randomCode() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	n := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	if n < 0 {
		n = -n
	}
	return fmt.Sprintf("%06d", n%1000000), nil
}

type ChallengeResult struct {
	ExpiresIn int `json:"expires_in"`
}

func (g *Gate) Challenge(botToken string) (*ChallengeResult, error) {
	botToken = strings.TrimSpace(botToken)
	if botToken == "" || !looksLikeBotToken(botToken) {
		return nil, fmt.Errorf(DeniedMsg)
	}

	g.mu.Lock()
	sec, err := g.loadSecretsUnlocked()
	if err != nil || sec.ChatID == "" {
		g.mu.Unlock()
		return nil, fmt.Errorf(DeniedMsg)
	}
	if g.pending != nil && time.Now().Before(g.pending.ExpiresAt) {
		g.mu.Unlock()
		return nil, fmt.Errorf("a password is already active for one person (20 minutes)")
	}
	// Never allow chat id change from the web. Only refresh bot token.
	sec.BotToken = botToken
	sec.Locked = true
	if err := g.saveSecretsUnlocked(*sec); err != nil {
		g.mu.Unlock()
		return nil, fmt.Errorf(DeniedMsg)
	}
	chatID := sec.ChatID
	g.mu.Unlock()

	code, err := randomCode()
	if err != nil {
		return nil, fmt.Errorf(DeniedMsg)
	}

	msg := "<b>VPS MANAGE</b>\n\n<b>Temporary password</b>\n<b>" + html.EscapeString(code) + "</b>\n<b>Valid 20 minutes · one person</b>"
	if err := sendMessageHTML(botToken, chatID, msg); err != nil {
		return nil, fmt.Errorf(DeniedMsg)
	}

	g.mu.Lock()
	g.pending = &pendingOTP{
		TokenHash: tokenKey(botToken),
		CodeHash:  hashCode(code),
		ExpiresAt: time.Now().Add(OTPTTL),
	}
	g.mu.Unlock()

	return &ChallengeResult{ExpiresIn: int(OTPTTL.Seconds())}, nil
}

func (g *Gate) Verify(botToken, code string) error {
	botToken = strings.TrimSpace(botToken)
	code = strings.TrimSpace(code)
	if botToken == "" || code == "" {
		return fmt.Errorf(DeniedMsg)
	}
	sec, err := g.LoadSecrets()
	if err != nil || sec.ChatID == "" || sec.BotToken == "" {
		return fmt.Errorf(DeniedMsg)
	}
	if subtle.ConstantTimeCompare([]byte(sec.BotToken), []byte(botToken)) != 1 {
		return fmt.Errorf(DeniedMsg)
	}

	g.mu.Lock()
	pend := g.pending
	if pend == nil || time.Now().After(pend.ExpiresAt) {
		g.pending = nil
		g.mu.Unlock()
		return fmt.Errorf(DeniedMsg)
	}
	if pend.TokenHash != tokenKey(botToken) {
		g.mu.Unlock()
		return fmt.Errorf(DeniedMsg)
	}
	if subtle.ConstantTimeCompare([]byte(pend.CodeHash), []byte(hashCode(code))) != 1 {
		g.mu.Unlock()
		return fmt.Errorf(DeniedMsg)
	}
	g.pending = nil
	g.mu.Unlock()
	return nil
}

func looksLikeBotToken(t string) bool {
	parts := strings.SplitN(t, ":", 2)
	if len(parts) != 2 {
		return false
	}
	if _, err := strconv.ParseInt(parts[0], 10, 64); err != nil {
		return false
	}
	return len(parts[1]) >= 20
}

func tgAPI(token, method string, form url.Values) ([]byte, error) {
	u := fmt.Sprintf("https://api.telegram.org/bot%s/%s", token, method)
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.PostForm(u, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func sendMessage(token, chatID, text string) error {
	b, err := tgAPI(token, "sendMessage", url.Values{
		"chat_id": {chatID},
		"text":    {text},
	})
	if err != nil {
		return err
	}
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("telegram: %s", out.Description)
	}
	return nil
}

package telegram

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const notifyName = "notify.env"

// NotifyConfig is an optional alert bot (separate from the login OTP gate).
// Empty token or chat id => notifications disabled (no error).
type NotifyConfig struct {
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
	Enabled  bool   `json:"enabled"`
}

type Notifier struct {
	mu         sync.Mutex
	secretsDir string
}

func NewNotifier(dataDir string) *Notifier {
	dir := filepath.Join(dataDir, "secrets")
	_ = os.MkdirAll(dir, 0o700)
	return &Notifier{secretsDir: dir}
}

func (n *Notifier) path() string {
	return filepath.Join(n.secretsDir, notifyName)
}

func (n *Notifier) Load() NotifyConfig {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.loadUnlocked()
}

func (n *Notifier) loadUnlocked() NotifyConfig {
	b, err := os.ReadFile(n.path())
	if err != nil {
		return NotifyConfig{}
	}
	cfg := NotifyConfig{}
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
		case "NOTIFY_BOT_TOKEN", "TELEGRAM_NOTIFY_BOT_TOKEN":
			cfg.BotToken = strings.TrimSpace(v)
		case "NOTIFY_CHAT_ID", "TELEGRAM_NOTIFY_CHAT_ID":
			cfg.ChatID = strings.TrimSpace(v)
		}
	}
	cfg.Enabled = cfg.BotToken != "" && cfg.ChatID != ""
	return cfg
}

func (n *Notifier) Save(token, chatID string) error {
	token = strings.TrimSpace(token)
	chatID = strings.TrimSpace(chatID)
	if token != "" && !looksLikeBotToken(token) {
		return fmt.Errorf("invalid bot token")
	}
	if chatID != "" {
		if _, err := strconv.ParseInt(chatID, 10, 64); err != nil {
			return fmt.Errorf("invalid chat id")
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := os.MkdirAll(n.secretsDir, 0o700); err != nil {
		return err
	}
	content := fmt.Sprintf(
		"# Optional access-alert bot. Leave empty to disable.\nNOTIFY_BOT_TOKEN=%s\nNOTIFY_CHAT_ID=%s\n",
		token, chatID,
	)
	tmp := n.path() + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, n.path()); err != nil {
		return err
	}
	_ = os.Chmod(n.path(), 0o600)
	return nil
}

func (n *Notifier) Clear() error {
	return n.Save("", "")
}

// AccessEvent describes a panel access for admin alerts.
type AccessEvent struct {
	Title     string
	Action    string
	Detail    string
	IP        string
	UserAgent string
	Room      string
}

func (n *Notifier) Alert(ev AccessEvent) {
	cfg := n.Load()
	if !cfg.Enabled {
		return
	}
	go func() {
		_ = sendMessageHTML(cfg.BotToken, cfg.ChatID, formatAccessHTML(ev))
	}()
}

func formatAccessHTML(ev AccessEvent) string {
	title := strings.TrimSpace(ev.Title)
	if title == "" {
		title = "Access"
	}
	lines := []string{boldHTML("VPS MANAGE"), "", boldHTML(title)}
	if ev.Room != "" {
		lines = append(lines, boldHTML(ev.Room))
	}
	lines = append(lines, boldHTML(nz(ev.IP, "unknown")), boldHTML(time.Now().UTC().Format("02 Jan 2006 · 15:04 UTC")))
	return strings.Join(lines, "\n")
}

func boldHTML(s string) string {
	return "<b>" + html.EscapeString(s) + "</b>"
}

func nz(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func sendMessageHTML(token, chatID, text string) error {
	b, err := tgAPI(token, "sendMessage", url.Values{
		"chat_id":                  {chatID},
		"text":                     {text},
		"parse_mode":               {"HTML"},
		"disable_web_page_preview": {"true"},
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

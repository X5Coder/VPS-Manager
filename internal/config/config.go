package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	ListenAddr     string
	DataDir        string
	RoomsDir       string
	RuntimeDir     string
	VolumesDir     string
	DBPath         string
	OwnerPass      string
	SessionHours   int
	AgentSock      string
	TelegramChatID string
}

func Load() Config {
	dataDir := env("VPS_ROOMS_DATA", "/opt/vps-rooms/data")
	base := filepath.Dir(dataDir)
	ownerPass := env("VPS_ROOMS_OWNER_PASS", "changeme")
	if b, err := os.ReadFile(filepath.Join(dataDir, "secrets", "owner.env")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "VPS_ROOMS_OWNER_PASS=") {
				if v := strings.TrimPrefix(line, "VPS_ROOMS_OWNER_PASS="); v != "" {
					ownerPass = v
				}
			}
		}
	}
	return Config{
		ListenAddr:     env("VPS_ROOMS_ADDR", ":9090"),
		DataDir:        dataDir,
		RoomsDir:       env("VPS_ROOMS_ROOMS", filepath.Join(base, "rooms")),
		RuntimeDir:     env("VPS_ROOMS_RUNTIME", filepath.Join(base, "runtime")),
		VolumesDir:     env("VPS_ROOMS_VOLUMES", filepath.Join(base, "volumes")),
		DBPath:         env("VPS_ROOMS_DB", filepath.Join(dataDir, "panel.db")),
		OwnerPass:      ownerPass,
		SessionHours:   envInt("VPS_ROOMS_SESSION_HOURS", 24),
		AgentSock:      env("VPS_ROOMS_AGENT_SOCK", filepath.Join(dataDir, "agent.sock")),
		TelegramChatID: env("TELEGRAM_CHAT_ID", ""),
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	ListenAddr string
	// New canonical layout under /vps-manager (see README).
	//   /vps-manager/bin, /vps-manager/data, /vps-manager/proxy,
	//   /vps-manager/x5coder-agent, /vps-manager/single, /vps-manager/multi
	BaseDir    string
	BinDir     string
	DataDir    string
	ProxyDir   string
	AgentDir   string
	SingleDir  string
	MultiDir   string
	RoomsDir   string // legacy alias → SingleDir (migration only)
	RuntimeDir string // legacy alias → SingleDir (migration only)
	VolumesDir string // legacy alias → "" (per-room volumes now)
	DBPath         string
	OwnerPass      string
	SessionHours   int
	AgentSock      string
	TelegramChatID string
}

func Load() Config {
	base := env("VPS_MANAGER_BASE", env("VPS_ROOMS_BASE", "/vps-manager"))
	dataDir := env("VPS_MANAGER_DATA", env("VPS_ROOMS_DATA", filepath.Join(base, "data")))
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
	singleDir := env("VPS_MANAGER_SINGLE", filepath.Join(base, "single"))
	multiDir := env("VPS_MANAGER_MULTI", filepath.Join(base, "multi"))
	return Config{
		ListenAddr:     env("VPS_ROOMS_ADDR", ":9090"),
		BaseDir:        base,
		BinDir:         env("VPS_MANAGER_BIN", filepath.Join(base, "bin")),
		DataDir:        dataDir,
		ProxyDir:       env("VPS_MANAGER_PROXY", filepath.Join(base, "proxy")),
		AgentDir:       env("VPS_MANAGER_AGENT", filepath.Join(base, "x5coder-agent")),
		SingleDir:      singleDir,
		MultiDir:       multiDir,
		RoomsDir:       env("VPS_ROOMS_ROOMS", singleDir),
		RuntimeDir:     env("VPS_ROOMS_RUNTIME", singleDir),
		VolumesDir:     env("VPS_ROOMS_VOLUMES", ""),
		DBPath:         env("VPS_ROOMS_DB", filepath.Join(dataDir, "database.sqlite")),
		OwnerPass:      ownerPass,
		SessionHours:   envInt("VPS_ROOMS_SESSION_HOURS", 24),
		AgentSock:      env("VPS_ROOMS_AGENT_SOCK", filepath.Join(dataDir, "agent.sock")),
		TelegramChatID: env("TELEGRAM_CHAT_ID", ""),
	}
}

// Canonical per-room layout (single source of truth for VPS paths).
//   single: /vps-manager/single/<room_id>/{project/.env, volumes/, config/}
//   multi:  /vps-manager/multi/<room_id>/{stack/docker-compose.yml+.env, volumes/, config/}
func IsMultiKind(kind string) bool {
	return strings.ToLower(strings.TrimSpace(kind)) == "multi"
}

func (c Config) RoomRoot(roomID, kind string) string {
	if IsMultiKind(kind) {
		return filepath.Join(c.MultiDir, roomID)
	}
	return filepath.Join(c.SingleDir, roomID)
}

// ProjectDir: single → <root>/project, multi → <root>/stack
func (c Config) RoomWorkDir(roomID, kind string) string {
	if IsMultiKind(kind) {
		return filepath.Join(c.MultiDir, roomID, "stack")
	}
	return filepath.Join(c.SingleDir, roomID, "project")
}

// RoomEnvPath: single → project/.env, multi → stack/.env (single file)
func (c Config) RoomEnvPath(roomID, kind string) string {
	return filepath.Join(c.RoomWorkDir(roomID, kind), ".env")
}

func (c Config) RoomVolumesDir(roomID, kind string) string {
	return filepath.Join(c.RoomRoot(roomID, kind), "volumes")
}

func (c Config) RoomConfigDir(roomID, kind string) string {
	return filepath.Join(c.RoomRoot(roomID, kind), "config")
}

func (c Config) RoomBackupDir(roomID, kind string) string {
	return filepath.Join(c.RoomRoot(roomID, kind), "backup")
}

func (c Config) RoomBackupPath(roomID, kind string) string {
	return filepath.Join(c.RoomBackupDir(roomID, kind), roomID+".zip")
}

func (c Config) GlobalBackupDir() string {
	return filepath.Join(c.BaseDir, "backup")
}

func (c Config) GlobalBackupPath() string {
	return filepath.Join(c.GlobalBackupDir(), "vps-manager.zip")
}

// VPSPath returns the breadcrumb path shown at the top of every web page.
func (c Config) DataSessionsDir() string {
	return filepath.Join(c.DataDir, "sessions")
}

func (c Config) DataLogsDir() string {
	return filepath.Join(c.DataDir, "logs")
}

func (c Config) VPSPath(roomID, kind string) string {
	if roomID == "" {
		return c.BaseDir
	}
	return c.RoomRoot(roomID, kind)
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

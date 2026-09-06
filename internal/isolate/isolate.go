package isolate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/x5coder/vps-rooms/internal/vault"
)

const (
	VaultFile  = "vault.bin"
	HashFile   = "auth.hash"
	NameFile   = "NAME"
	LockFile   = "LOCKED"
	ReadmeFile = "README.txt"
)

type RoomPaths struct {
	Root       string
	Vault      string
	Hash       string
	Name       string
	LockNotice string
	Readme     string
	Runtime    string // decrypted working tree used by panel
	// New canonical layout under /vps-manager.
	Kind       string // single | multi
	WorkDir    string // single → <root>/project, multi → <root>/stack
	EnvPath    string // single → project/.env, multi → stack/.env
	VolumesDir string // <root>/volumes
	ConfigDir  string // <root>/config
	BackupDir  string // <root>/backup → <room_id>.zip
}

func Paths(roomsDir, runtimeDir, roomID string) RoomPaths {
	root := filepath.Join(roomsDir, roomID)
	return RoomPaths{
		Root:       root,
		Vault:      filepath.Join(root, VaultFile),
		Hash:       filepath.Join(root, HashFile),
		Name:       filepath.Join(root, NameFile),
		LockNotice: filepath.Join(root, LockFile),
		Readme:     filepath.Join(root, ReadmeFile),
		Runtime:    filepath.Join(runtimeDir, roomID, "projects"),
		Kind:       "single",
		WorkDir:    filepath.Join(runtimeDir, roomID, "projects"),
		EnvPath:    filepath.Join(runtimeDir, roomID, "projects", ".env"),
		VolumesDir: filepath.Join(root, "volumes"),
		ConfigDir:  filepath.Join(root, "config"),
		BackupDir:  filepath.Join(root, "backup"),
	}
}

// PathsForKind is the canonical resolver for /vps-manager/{single,multi}/<room_id>.
// single: <root>/project/.env + volumes/ + config/
// multi:  <root>/stack/docker-compose.yml+.env + volumes/ + config/
func PathsForKind(baseDir, roomID, kind string) RoomPaths {
	k := "single"
	if kind == "multi" {
		k = "multi"
	}
	var root, work string
	if k == "multi" {
		root = filepath.Join(baseDir, "multi", roomID)
		work = filepath.Join(root, "stack")
	} else {
		root = filepath.Join(baseDir, "single", roomID)
		work = filepath.Join(root, "project")
	}
	return RoomPaths{
		Root:       root,
		Vault:      filepath.Join(root, VaultFile),
		Hash:       filepath.Join(root, HashFile),
		Name:       filepath.Join(root, NameFile),
		LockNotice: filepath.Join(root, LockFile),
		Readme:     filepath.Join(root, ReadmeFile),
		Runtime:    work,
		Kind:       k,
		WorkDir:    work,
		EnvPath:    filepath.Join(work, ".env"),
		VolumesDir: filepath.Join(root, "volumes"),
		ConfigDir:  filepath.Join(root, "config"),
		BackupDir:  filepath.Join(root, "backup"),
	}
}

func WriteLockNotice(p RoomPaths, roomName string) error {
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return err
	}
	_ = os.WriteFile(p.Name, []byte(roomName+"\n"), 0o644)
	msg := "LOCKED\nThis room is encrypted.\nUse: vr open " + roomName + "\nPassword required to view or copy any project files.\n"
	_ = os.WriteFile(p.LockNotice, []byte(msg), 0o644)
	readme := `VPS Rooms — isolated room
==========================
Contents are encrypted in vault.bin.
Direct reading of project files is denied.

Unlock with:
  vr open ` + roomName + `

Wrong/missing password => access denied.
The web panel can manage rooms without this CLI.
`
	return os.WriteFile(p.Readme, []byte(readme), 0o644)
}

func EnsureLayout(p RoomPaths) error {
	for _, d := range []string{p.Root, p.WorkDir, p.VolumesDir, p.ConfigDir, p.BackupDir} {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	// Default sub-volumes keep the shape stable: single → app-data/app-uploads,
	// multi → db/storage/functions (created lazily, only if missing).
	return nil
}

func SealRuntime(p RoomPaths, password string) error {
	if err := os.MkdirAll(filepath.Dir(p.Runtime), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(p.Runtime, 0o700); err != nil {
		return err
	}
	if err := vault.SealDir(p.Runtime, p.Vault, password); err != nil {
		return err
	}
	// Remove plaintext working copy markers from room root (keep only vault + notices)
	// Wipe any legacy plaintext projects under room root
	_ = os.RemoveAll(filepath.Join(p.Root, "projects"))
	_ = EnsureLayout(p)
	return nil
}

func UnlockTo(p RoomPaths, password, dest string) error {
	if _, err := os.Stat(p.Vault); err != nil {
		// empty vault: create empty dest
		return os.MkdirAll(dest, 0o700)
	}
	return vault.OpenVault(p.Vault, dest, password)
}

func UnlockRuntime(p RoomPaths, password string) error {
	return UnlockTo(p, password, p.Runtime)
}

func EnsureRuntime(p RoomPaths, password string) error {
	if st, err := os.Stat(p.Runtime); err == nil && st.IsDir() {
		// already unlocked for panel
		entries, _ := os.ReadDir(p.Runtime)
		if len(entries) > 0 || fileExists(p.Vault) {
			if !fileExists(p.Vault) {
				return nil
			}
			// Prefer existing runtime; panel keeps it warm.
			return nil
		}
	}
	if !fileExists(p.Vault) {
		return os.MkdirAll(p.Runtime, 0o700)
	}
	return UnlockRuntime(p, password)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func ListRoomNames(roomsDir string) ([]string, error) {
	entries, err := os.ReadDir(roomsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(roomsDir, e.Name(), NameFile))
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(b))
		if name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

func FindRoomIDByName(roomsDir, name string) (string, error) {
	entries, err := os.ReadDir(roomsDir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(roomsDir, e.Name(), NameFile))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(b)) == name {
			return e.Name(), nil
		}
	}
	return "", fmt.Errorf("room not found")
}

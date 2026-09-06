package api

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// New canonical layout:
//   Global: /vps-manager/backup/vps-manager.zip  (one file, whole /vps-manager)
//   Room:   /vps-manager/single/<room_id>/backup/<room_id>.zip
//           /vps-manager/multi/<room_id>/backup/<room_id>.zip   (one per room)
// Each zip contains: project/stack + volumes + config + .env

func (s *Server) backupDir() string {
	d := s.Cfg.GlobalBackupDir()
	_ = os.MkdirAll(d, 0o750)
	return d
}

func (s *Server) globalBackupPath() string {
	return s.Cfg.GlobalBackupPath()
}

func (s *Server) roomBackupPath(roomID string) string {
	return s.Rooms.RoomBackupPath(roomID)
}

type backupState struct {
	State   string `json:"state"` // running | ready | error | idle
	File    string `json:"file,omitempty"`
	Path    string `json:"path,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Error   string `json:"error,omitempty"`
	Files   int    `json:"files,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
	Current string `json:"current,omitempty"`
}

type backupProgress struct {
	key     string
	mu      sync.Mutex
	files   int
	bytes   int64
	current string
	last    time.Time
}

func (p *backupProgress) add(size int64, path string) {
	p.mu.Lock()
	p.files++
	p.bytes += size
	p.current = path
	due := time.Since(p.last) > time.Second
	p.mu.Unlock()
	if due {
		p.flush()
	}
}

func (p *backupProgress) flush() {
	p.mu.Lock()
	p.last = time.Now()
	files, bytes, current := p.files, p.bytes, p.current
	p.mu.Unlock()
	backupMu.Lock()
	defer backupMu.Unlock()
	if st, ok := backupStates[p.key]; ok && st.State == "running" {
		st.Files, st.Bytes, st.Current = files, bytes, current
	}
}

var (
	backupMu     sync.Mutex
	backupStates = map[string]*backupState{}
)

func backupSet(key string, st *backupState) {
	backupMu.Lock()
	backupStates[key] = st
	backupMu.Unlock()
}

func backupGet(key string) *backupState {
	backupMu.Lock()
	defer backupMu.Unlock()
	if st, ok := backupStates[key]; ok {
		cp := *st
		return &cp
	}
	return &backupState{State: "idle"}
}

func (s *Server) routesBackup() {
	s.Mux.HandleFunc("/api/backup/full", s.withGate(s.handleBackupFull))
	s.Mux.HandleFunc("/api/backup/room", s.withGate(s.handleBackupRoom))
	s.Mux.HandleFunc("/api/backup/status", s.withGate(s.handleBackupStatus))
	s.Mux.HandleFunc("/api/backup/files", s.withGate(s.handleBackupFiles))
	s.Mux.HandleFunc("/api/backup/download", s.withGate(s.handleBackupDownload))
	s.Mux.HandleFunc("/api/backup/delete", s.withGate(s.handleBackupDelete))
}

func (s *Server) handleBackupStatus(w http.ResponseWriter, r *http.Request) {
	if s.requireSession(w, r) == nil {
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method")
		return
	}
	backupMu.Lock()
	out := map[string]*backupState{}
	for k, v := range backupStates {
		cp := *v
		out[k] = &cp
	}
	backupMu.Unlock()
	writeJSON(w, 200, map[string]any{"jobs": out})
}

func (s *Server) handleBackupFiles(w http.ResponseWriter, r *http.Request) {
	if s.requireSession(w, r) == nil {
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method")
		return
	}
	writeJSON(w, 200, map[string]any{"files": s.listBackupFiles(), "dir": s.backupDir()})
}

func (s *Server) listBackupFiles() []map[string]any {
	out := []map[string]any{}
	// Global: /vps-manager/backup/vps-manager.zip
	if st, err := os.Stat(s.globalBackupPath()); err == nil && !st.IsDir() {
		out = append(out, map[string]any{
			"name": filepath.Base(s.globalBackupPath()), "size": st.Size(), "size_bytes": st.Size(),
			"created": st.ModTime().UTC().Format(time.RFC3339),
			"kind": "full", "room": "",
			"path": s.globalBackupPath(),
		})
	}
	// Per-room: scan single + multi
	for _, base := range []string{s.Cfg.SingleDir, s.Cfg.MultiDir} {
		ents, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			roomID := e.Name()
			bp := filepath.Join(base, roomID, "backup", roomID+".zip")
			st, err := os.Stat(bp)
			if err != nil || st.IsDir() {
				continue
			}
			roomShort := shortID(roomID)
			// Also lookup real room to get name if exists
			out = append(out, map[string]any{
				"name": filepath.Base(bp), "size": st.Size(), "size_bytes": st.Size(),
				"created": st.ModTime().UTC().Format(time.RFC3339),
				"kind": "room", "room": roomShort, "room_id": roomID,
				"path": bp,
			})
		}
	}
	// Legacy: old /vps-manager/vps-backup/*.zip (for migration / backward compat download)
	legacyDir := filepath.Join(s.Cfg.BaseDir, "vps-backup")
	if ents, err := os.ReadDir(legacyDir); err == nil {
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".zip") {
				continue
			}
			// Avoid duplicating if already listed under new name
			if e.Name() == "vps-manager.zip" {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			kind := "full"
			room := ""
			if strings.HasPrefix(e.Name(), "room-") {
				kind = "room"
				rest := strings.TrimSuffix(strings.TrimPrefix(e.Name(), "room-"), ".zip")
				if len(rest) >= 8 {
					room = rest[:8]
				} else {
					room = rest
				}
			}
			out = append(out, map[string]any{
				"name": e.Name(), "size": info.Size(), "size_bytes": info.Size(),
				"created": info.ModTime().UTC().Format(time.RFC3339),
				"kind": kind, "room": room,
				"path": filepath.Join(legacyDir, e.Name()),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["created"].(string) > out[j]["created"].(string)
	})
	return out
}

func (s *Server) findBackupPath(name string) (string, bool) {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || !strings.HasSuffix(name, ".zip") || strings.Contains(name, "/") {
		return "", false
	}
	// Global
	if name == "vps-manager.zip" || name == "full.zip" {
		if st, err := os.Stat(s.globalBackupPath()); err == nil && !st.IsDir() {
			return s.globalBackupPath(), true
		}
	}
	// Room: name is <room_id>.zip
	trim := strings.TrimSuffix(name, ".zip")
	// Try direct roomID match in single/multi
	for _, base := range []string{s.Cfg.SingleDir, s.Cfg.MultiDir} {
		candidate := filepath.Join(base, trim, "backup", name)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, true
		}
	}
	// Legacy short-id search: room-<short>-*.zip
	legacyDir := filepath.Join(s.Cfg.BaseDir, "vps-backup")
	if st, err := os.Stat(filepath.Join(legacyDir, name)); err == nil && !st.IsDir() {
		return filepath.Join(legacyDir, name), true
	}
	// Global legacy dir
	if st, err := os.Stat(filepath.Join(s.backupDir(), name)); err == nil && !st.IsDir() {
		return filepath.Join(s.backupDir(), name), true
	}
	return "", false
}

func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	if s.requireSession(w, r) == nil {
		return
	}
	name := filepath.Base(strings.TrimSpace(r.URL.Query().Get("file")))
	if name == "" || !strings.HasSuffix(name, ".zip") || strings.Contains(name, "/") {
		writeErr(w, 400, "file required")
		return
	}
	path, ok := s.findBackupPath(name)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		writeErr(w, 404, "not found")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", st.Size()))
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer f.Close()
	_, _ = io.Copy(w, f)
}

func (s *Server) handleBackupDelete(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeErr(w, 405, "method")
		return
	}
	name := filepath.Base(strings.TrimSpace(r.URL.Query().Get("file")))
	if name == "" {
		var body struct {
			File string `json:"file"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		name = filepath.Base(strings.TrimSpace(body.File))
	}
	if name == "" || name == "." || !strings.HasSuffix(name, ".zip") {
		writeErr(w, 400, "valid zip file required")
		return
	}
	path, ok := s.findBackupPath(name)
	if !ok {
		writeErr(w, 404, "backup file not found")
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		writeErr(w, 500, err.Error())
		return
	}
	backupMu.Lock()
	for k, v := range backupStates {
		if v.File == name {
			delete(backupStates, k)
		}
	}
	backupMu.Unlock()
	_ = appendLog(s.Cfg.DataDir, "panel", "BACKUP deleted file="+name)
	writeJSON(w, 200, map[string]any{"ok": true, "deleted": name})
}

func (s *Server) handleBackupFull(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	if cur := backupGet("full"); cur.State == "running" {
		writeJSON(w, 200, map[string]any{"ok": true, "state": "running"})
		return
	}
	backupSet("full", &backupState{State: "running"})
	go s.runFullBackup()
	writeJSON(w, 200, map[string]any{"ok": true, "state": "running"})
}

func (s *Server) handleBackupRoom(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method")
		return
	}
	var body struct {
		RoomID string `json:"room_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	roomID := strings.TrimSpace(body.RoomID)
	if roomID == "" {
		roomID = strings.TrimSpace(r.URL.Query().Get("room_id"))
	}
	room, err := s.Store.GetRoom(roomID)
	if err != nil || room == nil {
		writeErr(w, 404, "room not found")
		return
	}
	key := "room:" + room.ID
	if cur := backupGet(key); cur.State == "running" {
		writeJSON(w, 200, map[string]any{"ok": true, "state": "running"})
		return
	}
	backupSet(key, &backupState{State: "running"})
	go s.runRoomBackup(room.ID)
	writeJSON(w, 200, map[string]any{"ok": true, "state": "running"})
}

func shortID(id string) string {
	id = strings.ReplaceAll(strings.TrimSpace(id), "-", "")
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// pruneBackups is legacy: cleans old vps-backup/*.zip files. Kept for migration.
func (s *Server) pruneBackups(roomShortID, keepName string) {
	pattern := "full-*.zip"
	if roomShortID != "" {
		pattern = "room-" + roomShortID + "-*.zip"
	}
	legacyDir := filepath.Join(s.Cfg.BaseDir, "vps-backup")
	matches, err := filepath.Glob(filepath.Join(legacyDir, pattern))
	if err != nil {
		return
	}
	for _, m := range matches {
		if filepath.Base(m) == keepName {
			continue
		}
		_ = os.Remove(m)
	}
}

func (s *Server) runFullBackup() {
	start := time.Now()
	log.Printf("backup: full started -> %s", s.globalBackupPath())
	dest := s.globalBackupPath()
	_ = os.MkdirAll(filepath.Dir(dest), 0o750)
	tmpDest := dest + ".tmp"
	_ = os.Remove(tmpDest)
	prog := &backupProgress{key: "full"}
	tmpDB := filepath.Join(os.TempDir(), fmt.Sprintf("vpsm-full-%d.db", time.Now().UnixNano()))
	_ = os.Remove(tmpDB)
	if s.Store != nil && s.Store.DB != nil {
		if _, err := s.Store.DB.Exec(`VACUUM INTO '` + tmpDB + `'`); err != nil {
			_ = os.Remove(tmpDB)
			tmpDB = ""
		} else if _, err := os.Stat(tmpDB); err != nil {
			_ = os.Remove(tmpDB)
			tmpDB = ""
		}
	}
	err := zipTree(s.Cfg.BaseDir, tmpDest, prog, func(rel string) (skip bool, replace string) {
		rel = filepath.ToSlash(rel)
		if rel == "" {
			return false, ""
		}
		// Exclude all backup zips to avoid recursion / bloat
		if rel == "backup" || strings.HasPrefix(rel, "backup/") {
			return true, ""
		}
		if strings.Contains(rel, "/backup/") || strings.HasSuffix(rel, "/backup") {
			return true, ""
		}
		// Legacy dir
		if rel == "vps-backup" || strings.HasPrefix(rel, "vps-backup/") {
			return true, ""
		}
		if strings.HasSuffix(rel, "agent.sock") {
			return true, ""
		}
		if strings.HasSuffix(rel, "panel.db-shm") || strings.HasSuffix(rel, "panel.db-wal") {
			return true, ""
		}
		if rel == "data/panel.db" && tmpDB != "" {
			return false, tmpDB
		}
		return false, ""
	})
	_ = os.Remove(tmpDB)
	if err != nil {
		_ = os.Remove(tmpDest)
		log.Printf("backup: full failed: %v", err)
		backupSet("full", &backupState{State: "error", Error: err.Error()})
		return
	}
	if err := os.Rename(tmpDest, dest); err != nil {
		_ = os.Remove(tmpDest)
		log.Printf("backup: full rename failed: %v", err)
		backupSet("full", &backupState{State: "error", Error: err.Error()})
		return
	}
	st, _ := os.Stat(dest)
	prog.flush()
	backupSet("full", &backupState{State: "ready", File: filepath.Base(dest), Path: dest, Size: st.Size(), Files: prog.files, Bytes: prog.bytes})
	log.Printf("backup: full ready %s (%d bytes, %d files, %s)", dest, st.Size(), prog.files, time.Since(start).Round(time.Second))
	_ = appendLog(s.Cfg.DataDir, "panel", "BACKUP full="+filepath.Base(dest))
}

func (s *Server) runRoomBackup(roomID string) {
	key := "room:" + roomID
	room, err := s.Store.GetRoom(roomID)
	if err != nil || room == nil {
		backupSet(key, &backupState{State: "error", Error: "room not found"})
		return
	}
	start := time.Now()
	log.Printf("backup: room %s started", roomID)
	root := s.Rooms.VPSPath(roomID)
	dest := s.roomBackupPath(roomID)
	_ = os.MkdirAll(filepath.Dir(dest), 0o750)
	tmpDest := dest + ".tmp"
	_ = os.Remove(tmpDest)
	prog := &backupProgress{key: key}
	// Zip the whole room root: contains project/stack + volumes + config + .env (+ backup excluded)
	err = zipTree(root, tmpDest, prog, func(rel string) (skip bool, replace string) {
		rel = filepath.ToSlash(rel)
		if rel == "backup" || strings.HasPrefix(rel, "backup/") {
			return true, ""
		}
		if strings.HasSuffix(rel, "agent.sock") {
			return true, ""
		}
		return false, ""
	})
	if err != nil {
		_ = os.Remove(tmpDest)
		log.Printf("backup: room %s failed: %v", roomID, err)
		backupSet(key, &backupState{State: "error", Error: err.Error()})
		return
	}
	if err := os.Rename(tmpDest, dest); err != nil {
		_ = os.Remove(tmpDest)
		log.Printf("backup: room %s rename failed: %v", roomID, err)
		backupSet(key, &backupState{State: "error", Error: err.Error()})
		return
	}
	st, _ := os.Stat(dest)
	prog.flush()
	backupSet(key, &backupState{State: "ready", File: filepath.Base(dest), Path: dest, Size: st.Size(), Files: prog.files, Bytes: prog.bytes})
	log.Printf("backup: room %s ready %s (%d bytes, %d files, %s)", roomID, dest, st.Size(), prog.files, time.Since(start).Round(time.Second))
	_ = appendLog(s.Cfg.DataDir, "panel", "BACKUP room="+roomID+" file="+filepath.Base(dest))
}

// zipTree zips srcDir into destZip (stored without compression for speed).
func zipTree(srcDir, destZip string, prog *backupProgress, hook func(rel string) (skip bool, replace string)) error {
	out, err := os.Create(destZip)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		realPath := path
		if hook != nil {
			skip, replace := hook(rel)
			if skip {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if replace != "" {
				realPath = replace
				if st, err := os.Stat(replace); err == nil {
					info = st
				}
			}
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return nil
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.Method = zip.Store
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		rf, err := os.Open(realPath)
		if err != nil {
			return nil
		}
		n, copyErr := io.Copy(fw, rf)
		rf.Close()
		if copyErr == nil && prog != nil {
			prog.add(n, hdr.Name)
		}
		return copyErr
	})
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if oerr := out.Close(); err == nil {
		err = oerr
	}
	if err != nil {
		_ = os.Remove(destZip)
	}
	return err
}

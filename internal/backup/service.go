package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/x5coder/vps-rooms/internal/auth"
	"github.com/x5coder/vps-rooms/internal/dockerx"
	"github.com/x5coder/vps-rooms/internal/projects"
	"github.com/x5coder/vps-rooms/internal/rooms"
	"github.com/x5coder/vps-rooms/internal/store"
)

type Service struct {
	Store          *store.Store
	Rooms          *rooms.Service
	Projects       *projects.Service
	Docker         *dockerx.Client
	DataDir        string
	RoomsDir       string
	RuntimeDir     string
	ProxyDir       string
	DBPath         string
	OwnerPass      string
	WorkDir        string
	OnAfterRestore func() error

	mu      sync.Mutex
	running bool
	lastLog string
	liveJob *Job
}

func (s *Service) secretsPath() string {
	return filepath.Join(s.DataDir, "secrets", "github.env")
}

func (s *Service) SaveToken(token string) error {
	gh := NewGitHub(token)
	u, err := gh.Validate()
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.secretsPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	content := fmt.Sprintf("GITHUB_TOKEN=%s\nGITHUB_USER=%s\n", token, u.Login)
	if err := os.WriteFile(s.secretsPath(), []byte(content), 0o600); err != nil {
		return err
	}
	_ = s.Store.SetMeta("backup_github_user", u.Login)
	_ = s.Store.SetMeta("backup_enabled", "1")
	now := time.Now().UTC()
	_ = s.Store.SetMeta("backup_next_at", now.Add(IntervalHours*time.Hour).Format(time.RFC3339))
	return nil
}

func (s *Service) ClearToken() error {
	_ = os.Remove(s.secretsPath())
	_ = s.Store.SetMeta("backup_enabled", "0")
	_ = s.Store.SetMeta("backup_github_user", "")
	return nil
}

func (s *Service) LoadToken() (string, string, error) {
	b, err := os.ReadFile(s.secretsPath())
	if err != nil {
		return "", "", err
	}
	var token, user string
	for _, line := range splitLines(string(b)) {
		if len(line) > 13 && line[:13] == "GITHUB_TOKEN=" {
			token = line[13:]
		}
		if len(line) > 12 && line[:12] == "GITHUB_USER=" {
			user = line[12:]
		}
	}
	return token, user, nil
}

func maskToken(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return ""
	}
	if len(t) <= 8 {
		return "••••"
	}
	return t[:4] + "••••" + t[len(t)-4:]
}

func (s *Service) Status() map[string]any {
	token, user, _ := s.LoadToken()
	enabled, _, _ := s.Store.GetMeta("backup_enabled")
	last, _, _ := s.Store.GetMeta("backup_last_at")
	next, _, _ := s.Store.GetMeta("backup_next_at")
	lastErr, _, _ := s.Store.GetMeta("backup_last_error")
	snaps, _ := s.ListSnapshotsLocal()
	cp := s.loadCheckpoint()
	resumeKind := ""
	resumeSnap := ""
	resumeRooms := 0
	resumeSystem := false
	if cp != nil {
		resumeKind = cp.Kind
		resumeSnap = cp.SnapshotID
		resumeRooms = len(cp.RoomsDone)
		resumeSystem = cp.SystemDone
	}
	return map[string]any{
		"configured":      token != "",
		"enabled":         enabled == "1" && token != "",
		"github_user":     user,
		"token_saved":     token != "",
		"token_hint":      maskToken(token),
		"last_backup_at":  last,
		"next_backup_at":  next,
		"last_error":      lastErr,
		"running":         s.running,
		"last_log":        s.lastLog,
		"interval_hours":  IntervalHours,
		"snapshots":       snaps,
		"job":             s.CurrentJob(),
		"can_resume":      cp != nil && (cp.Kind == "backup" || cp.Kind == "restore"),
		"resume_kind":     resumeKind,
		"resume_snapshot": resumeSnap,
		"resume_rooms":    resumeRooms,
		"resume_system":   resumeSystem,
		"full_backup":     true,
		"includes": []string{
			"panel.db (rooms, projects, API tokens, settings)",
			"telegram & github secrets",
			"owner password",
			"room vaults + runtime files + project source volumes",
			"Postgres dumps (Supabase auth/db + any Postgres in a project)",
			"object storage files (Supabase Storage and bind-mounted data)",
			"docker named volumes",
			"proxy Caddyfile",
			"panel logs",
		},
		"token_help": "Create a classic Personal Access Token with the repo scope: GitHub → Settings → Developer settings → Personal access tokens (classic).",
	}
}

func (s *Service) ListSnapshotsLocal() ([]SnapshotRecord, error) {
	raw, ok, _ := s.Store.GetMeta("backup_snapshots")
	if !ok || raw == "" {
		return []SnapshotRecord{}, nil
	}
	var list []SnapshotRecord
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return []SnapshotRecord{}, nil
	}
	return list, nil
}

func (s *Service) saveSnapshots(list []SnapshotRecord) error {
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.Store.SetMeta("backup_snapshots", string(b))
}

func (s *Service) StartScheduler() {
	s.recoverStaleJob()
	go func() {
		t := time.NewTicker(5 * time.Minute)
		f := time.NewTicker(2 * time.Second)
		defer t.Stop()
		defer f.Stop()
		for {
			select {
			case <-t.C:
				s.tick()
			case <-f.C:
				s.mu.Lock()
				var snap *Job
				if s.liveJob != nil && s.liveJob.Status == "running" {
					cp := *s.liveJob
					cp.Logs = append([]string{}, s.liveJob.Logs...)
					snap = &cp
				}
				s.mu.Unlock()
				if snap != nil {
					s.flushJob(*snap)
				}
			}
		}
	}()
}

func (s *Service) recoverStaleJob() {
	raw, ok, _ := s.Store.GetMeta("backup_job")
	if ok && raw != "" {
		var j Job
		if err := json.Unmarshal([]byte(raw), &j); err == nil && (j.Status == "running" || j.Status == "queued") {
			j.Status = "error"
			j.Error = "Interrupted — click Backup / Restore to resume from the last point"
			j.Message = "Interrupted"
			j.Progress = "Interrupted — resume from last point"
			j.EndedAt = time.Now().UTC().Format(time.RFC3339)
			j.Logs = append(j.Logs, time.Now().UTC().Format("15:04:05")+"  Interrupted — resume from last point")
			s.flushJob(j)
			s.mu.Lock()
			s.liveJob = &j
			s.running = false
			s.mu.Unlock()
		}
	}
	if s.WorkDir != "" {
		entries, _ := os.ReadDir(s.WorkDir)
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(s.WorkDir, e.Name()))
		}
	}
}

func (s *Service) tick() {
	token, _, err := s.LoadToken()
	if err != nil || token == "" {
		return
	}
	en, _, _ := s.Store.GetMeta("backup_enabled")
	if en != "1" {
		return
	}
	nextStr, ok, _ := s.Store.GetMeta("backup_next_at")
	if !ok || nextStr == "" {
		_ = s.Store.SetMeta("backup_next_at", time.Now().UTC().Add(IntervalHours*time.Hour).Format(time.RFC3339))
		return
	}
	next, err := time.Parse(time.RFC3339, nextStr)
	if err != nil || time.Now().UTC().Before(next) {
		return
	}
	_, _ = s.StartBackupAsync("Scheduled backup", "Automatic full 24h backup (panel + projects + container data)")
}

func (s *Service) RunBackup(label, description string) (*SnapshotRecord, error) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil, fmt.Errorf("backup already running")
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	return s.executeBackup(label, description)
}

func (s *Service) executeBackup(label, description string) (*SnapshotRecord, error) {
	token, user, err := s.LoadToken()
	if err != nil || token == "" {
		return nil, fmt.Errorf("GitHub PAT required — add a classic token with repo scope in Restore/Backup settings")
	}
	gh := NewGitHub(token)
	u, err := gh.Validate()
	if err != nil {
		_ = s.Store.SetMeta("backup_last_error", err.Error())
		return nil, err
	}
	if user == "" {
		user = u.Login
	}
	gh.User = user

	if label == "" {
		label = "Backup " + time.Now().UTC().Format("2006-01-02 15:04")
	}
	if description == "" {
		description = "Full VPS MANAGE backup: panel DB, secrets, API tokens, rooms, vaults, runtime, container data & volumes"
	}

	s.report(1, "Inspecting last backup point")
	cp := s.loadCheckpoint()
	if cp != nil && cp.Kind == "backup" {
		s.report(2, "Resuming backup from last point (%d rooms already uploaded)", len(cp.RoomsDone))
	} else {
		cp = &Checkpoint{Kind: "backup", RoomsDone: []string{}, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
		s.saveCheckpoint(cp)
	}

	id := uuid.NewString()
	manifest := NewManifest(id, label, description, user)
	work := filepath.Join(s.WorkDir, "run-"+id[:8])
	_ = os.RemoveAll(work)
	if err := os.MkdirAll(work, 0o750); err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)

	s.report(3, "Checking GitHub token for @%s", user)
	s.logf("ensuring index repo %s", IndexRepo)
	if err := gh.EnsureRepo(IndexRepo, "VPS MANAGE backup map (do not edit)"); err != nil {
		return nil, err
	}
	s.report(6, "GitHub map repo ready")
	indexDir := filepath.Join(work, IndexRepo)
	if err := gh.CloneOrPull(IndexRepo, indexDir); err != nil {
		_ = os.MkdirAll(indexDir, 0o750)
		_ = initGitRepo(indexDir, gh, IndexRepo)
	}
	_ = os.WriteFile(filepath.Join(indexDir, "FORMAT"), []byte(FormatMagic+"\n"), 0o644)

	if cp.SystemDone {
		s.report(12, "Panel database already uploaded — skipping")
	} else {
		s.report(10, "Backing up panel database & secrets")
		sysLocal := filepath.Join(work, "system-tree")
		if err := s.prepareSystemTree(sysLocal); err != nil {
			return nil, fmt.Errorf("system snapshot: %w", err)
		}
		sysFiles, err := s.uploadTree(gh, work, SystemRepo, "VPS MANAGE full system state", sysLocal, "system")
		if err != nil {
			return nil, fmt.Errorf("system upload: %w", err)
		}
		manifest.SystemFiles = sysFiles
		manifest.SystemRepo = SystemRepo
		cp.SystemDone = true
		cp.SystemFiles = sysFiles
		cp.SystemRepo = SystemRepo
		s.saveCheckpoint(cp)
		s.report(18, "Uploaded panel system state")
	}

	roomsList, err := s.Store.ListRooms()
	if err != nil {
		return nil, err
	}
	var prevMan Manifest
	_ = readJSON(filepath.Join(indexDir, "latest.json"), &prevMan)
	prevByRoom := map[string]ProjectMap{}
	for _, p := range prevMan.Projects {
		prevByRoom[p.RoomID] = p
	}
	if cp.SystemDone && len(manifest.SystemFiles) == 0 {
		if len(cp.SystemFiles) > 0 {
			manifest.SystemFiles = cp.SystemFiles
			manifest.SystemRepo = cp.SystemRepo
		} else {
			manifest.SystemFiles = prevMan.SystemFiles
			manifest.SystemRepo = prevMan.SystemRepo
		}
		if manifest.SystemRepo == "" {
			manifest.SystemRepo = SystemRepo
		}
	}
	for i, room := range roomsList {
		base := 18
		span := 70
		if n := len(roomsList); n > 0 {
			base = 18 + (span * i / n)
		}
		if checkpointHasRoom(cp, room.ID) {
			found := false
			for _, pm := range cp.Projects {
				if pm.RoomID == room.ID {
					manifest.Projects = append(manifest.Projects, pm)
					found = true
					break
				}
			}
			if !found {
				if pm, ok := prevByRoom[room.ID]; ok {
					manifest.Projects = append(manifest.Projects, pm)
				}
			}
			s.report(base, "Room %s already uploaded — skipping", room.Name)
			continue
		}
		s.report(base, "Room %s (%d/%d)", room.Name, i+1, len(roomsList))
		_ = s.Rooms.EnsureUnlocked(room.ID)
		if projs, err := s.Store.ListProjects(room.ID); err == nil {
			for _, p := range projs {
				s.report(-1, "Capturing %s (files, database, storage)", p.Name)
				s.captureProjectData(room.ID, p)
			}
		}
		pm, err := s.backupRoom(gh, work, room)
		if err != nil {
			s.report(-1, "room %s failed: %v", room.Name, err)
			_ = s.Store.SetMeta("backup_last_error", err.Error())
			continue
		}
		manifest.Projects = append(manifest.Projects, *pm)
		cp.RoomsDone = append(cp.RoomsDone, room.ID)
		cp.Projects = append(cp.Projects, *pm)
		s.saveCheckpoint(cp)
		s.report(base+span/max(len(roomsList), 1), "Uploaded room %s", room.Name)
	}

	s.report(92, "Writing backup map")

	manDir := filepath.Join(indexDir, "snapshots", id)
	_ = os.MkdirAll(manDir, 0o750)
	manBytes, _ := MarshalPretty(manifest)
	_ = os.WriteFile(filepath.Join(manDir, "manifest.json"), manBytes, 0o644)
	_ = os.WriteFile(filepath.Join(indexDir, "latest.json"), manBytes, 0o644)

	rec := SnapshotRecord{
		ID: id, Label: label, Description: description,
		CreatedAt: manifest.CreatedAt, Status: "ok", Owner: user,
		ManifestPath: "snapshots/" + id + "/manifest.json",
	}
	var index []SnapshotRecord
	_ = readJSON(filepath.Join(indexDir, "index.json"), &index)
	index = append([]SnapshotRecord{rec}, index...)
	if len(index) > 100 {
		index = index[:100]
	}
	ib, _ := MarshalPretty(index)
	_ = os.WriteFile(filepath.Join(indexDir, "index.json"), ib, 0o644)

	if err := gh.CommitPush(indexDir, "snapshot "+id[:8]+": "+label); err != nil {
		rec.Status = "failed"
		_ = s.Store.SetMeta("backup_last_error", err.Error())
		return nil, err
	}

	local, _ := s.ListSnapshotsLocal()
	local = append([]SnapshotRecord{rec}, local...)
	if len(local) > 100 {
		local = local[:100]
	}
	_ = s.saveSnapshots(local)
	now := time.Now().UTC()
	_ = s.Store.SetMeta("backup_last_at", now.Format(time.RFC3339))
	_ = s.Store.SetMeta("backup_next_at", now.Add(IntervalHours*time.Hour).Format(time.RFC3339))
	_ = s.Store.SetMeta("backup_last_error", "")
	s.clearCheckpoint()
	s.logf("backup ok %s", id)
	return &rec, nil
}

func (s *Service) backupRoom(gh *GitHub, work string, room store.Room) (*ProjectMap, error) {
	slug := Slug(room.Name)
	projRepo := ProjectRepoName(slug)
	s.logf("project repo %s", projRepo)
	if err := gh.EnsureRepo(projRepo, "VPS MANAGE project: "+room.Name); err != nil {
		return nil, err
	}
	projDir := filepath.Join(work, projRepo)
	_ = gh.CloneOrPull(projRepo, projDir)
	if _, err := os.Stat(filepath.Join(projDir, ".git")); err != nil {
		_ = initGitRepo(projDir, gh, projRepo)
	}

	projs, _ := s.Store.ListProjects(room.ID)
	pm := &ProjectMap{
		RoomID: room.ID, RoomName: room.Name, ProjectRepo: projRepo,
		BackupRepos: []string{}, QuotaBytes: room.QuotaBytes,
		PassHash: room.PassHash, PassPlain: room.PassPlain, NetworkName: room.NetworkName,
	}
	for _, p := range projs {
		pdir := s.Rooms.ProjectDir(room.ID, p.ID)
		meta := ProjectMeta{
			ID: p.ID, RoomID: p.RoomID, Name: p.Name, Image: p.Image,
			HostPort: p.HostPort, ContainerPort: p.ContainerPort,
			Domain: p.Domain, DomainEnabled: p.DomainEnabled,
			SSLStatus: p.SSLStatus, ExternalURL: p.ExternalURL,
			Status: p.Status, CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339),
			DeployKind: projects.DetectDeployKind(p, pdir),
		}
		pm.Projects = append(pm.Projects, meta)
		env, _ := os.ReadFile(filepath.Join(pdir, ".env"))
		_ = os.WriteFile(filepath.Join(projDir, "env-"+p.ID+".backup"), env, 0o600)
	}
	if len(projs) > 0 {
		p := projs[0]
		pm.ProjectID = p.ID
		pm.ProjectName = p.Name
		pm.HostPort = p.HostPort
		pm.Domain = p.Domain
		pm.Image = p.Image
		meta, _ := MarshalPretty(map[string]any{
			"room": room, "project": p, "projects": pm.Projects, "format": FormatMagic,
		})
		_ = os.WriteFile(filepath.Join(projDir, "project.json"), meta, 0o644)
		env, _ := os.ReadFile(filepath.Join(s.Rooms.ProjectDir(room.ID, p.ID), ".env"))
		_ = os.WriteFile(filepath.Join(projDir, "env.backup"), env, 0o600)
	} else {
		meta, _ := MarshalPretty(map[string]any{"room": room, "format": FormatMagic})
		_ = os.WriteFile(filepath.Join(projDir, "project.json"), meta, 0o644)
	}
	_ = gh.CommitPush(projDir, "update project "+room.Name)

	// Include sealed vault + unlocked runtime + live files/compose outside runtime
	roots := []rootSpec{
		{"runtime", filepath.Join(s.RuntimeDir, room.ID)},
		{"vault", filepath.Join(s.RoomsDir, room.ID)},
	}
	runtimePrefix := filepath.Clean(s.RuntimeDir) + string(os.PathSeparator)
	seen := map[string]bool{}
	addRoot := func(prefix, dir string) {
		dir = filepath.Clean(dir)
		if dir == "" || dir == "." || seen[dir] {
			return
		}
		st, err := os.Stat(dir)
		if err != nil || !st.IsDir() {
			return
		}
		if strings.HasPrefix(dir+string(os.PathSeparator), runtimePrefix) || dir == filepath.Clean(s.RuntimeDir) {
			return
		}
		seen[dir] = true
		roots = append(roots, rootSpec{prefix, dir})
		s.report(-1, "Including files %s", dir)
	}
	for _, p := range projs {
		pdir := s.Rooms.ProjectDir(room.ID, p.ID)
		filesRoot, composeDir, _, binds := projects.ProjectLayout(pdir)
		addRoot("appdata/"+p.ID, filesRoot)
		addRoot("appdata/"+p.ID, composeDir)
		vol := filepath.Join(filepath.Dir(s.RuntimeDir), "volumes", p.ID)
		addRoot("appdata/"+p.ID, vol)
		for _, b := range binds {
			host, _, _ := projects.SplitBind(b)
			addRoot("appdata/"+p.ID, host)
		}
	}
	files, repos, err := s.chunkRoots(gh, work, slug, roots)
	if err != nil {
		return nil, err
	}
	pm.Files = files
	pm.BackupRepos = repos
	return pm, nil
}

type rootSpec struct {
	prefix string
	root   string
}

func (s *Service) chunkRoots(gh *GitHub, work, slug string, roots []rootSpec) ([]FileEntry, []string, error) {
	backupIdx := 1
	backupRepo := BackupRepoName(slug, backupIdx)
	var repoSize int64
	var repos []string
	ensureBackup := func() error {
		if err := gh.EnsureRepo(backupRepo, "VPS MANAGE data backup"); err != nil {
			return err
		}
		found := false
		for _, r := range repos {
			if r == backupRepo {
				found = true
				break
			}
		}
		if !found {
			repos = append(repos, backupRepo)
		}
		return nil
	}
	if err := ensureBackup(); err != nil {
		return nil, nil, err
	}
	dataDir := filepath.Join(work, backupRepo)
	_ = gh.CloneOrPull(backupRepo, dataDir)
	if _, err := os.Stat(filepath.Join(dataDir, ".git")); err != nil {
		_ = initGitRepo(dataDir, gh, backupRepo)
	}
	_ = os.MkdirAll(filepath.Join(dataDir, "chunks"), 0o750)

	prevHashes := map[string]string{}
	_ = readJSON(filepath.Join(dataDir, "hashes.json"), &prevHashes)
	prevFiles := map[string]FileEntry{}
	var prevList []FileEntry
	if readJSON(filepath.Join(dataDir, "files.json"), &prevList) == nil {
		for _, fe := range prevList {
			prevFiles[fe.Path] = fe
		}
	}
	newHashes := map[string]string{}
	var files []FileEntry

	rotate := func() error {
		_ = writeJSON(filepath.Join(dataDir, "hashes.json"), newHashes)
		_ = gh.CommitPush(dataDir, "rotate before "+backupRepo)
		backupIdx++
		backupRepo = BackupRepoName(slug, backupIdx)
		repoSize = 0
		if err := ensureBackup(); err != nil {
			return err
		}
		dataDir = filepath.Join(work, backupRepo)
		_ = gh.CloneOrPull(backupRepo, dataDir)
		if _, err := os.Stat(filepath.Join(dataDir, ".git")); err != nil {
			_ = initGitRepo(dataDir, gh, backupRepo)
		}
		_ = os.MkdirAll(filepath.Join(dataDir, "chunks"), 0o750)
		return nil
	}

	for _, rs := range roots {
		s.report(-1, "Scanning %s", rs.root)
		fileN := 0
		_ = filepath.Walk(rs.root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			rel, err := filepath.Rel(rs.root, path)
			if err != nil {
				return nil
			}
			if skipBackupRel(rel) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if info.IsDir() {
				return nil
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			key := rs.prefix + "/" + filepath.ToSlash(rel)
			if old, ok := prevFiles[key]; ok && old.Size == info.Size() && old.SHA256 != "" && len(old.Chunks) > 0 {
				fileN++
				newHashes[key] = old.SHA256
				files = append(files, old)
				if fileN%80 == 0 {
					s.report(-1, "Unchanged %d files in %s", fileN, rs.prefix)
				}
				return nil
			}
			if info.Size() >= 1024*1024 {
				s.report(-1, "Hashing %s (%s)", key, formatBytes(info.Size()))
			}
			sum, size, err := HashFile(path)
			if err != nil {
				return nil
			}
			fileN++
			if size >= 1024*1024 {
				s.report(-1, "Packing %s (%s)", key, formatBytes(size))
			} else if fileN%80 == 0 {
				s.report(-1, "Packed %d files from %s", fileN, rs.prefix)
			}
			newHashes[key] = sum
			entry := FileEntry{Path: key, Size: size, SHA256: sum}
			if prev, ok := prevHashes[key]; ok && prev == sum {
				if old, ok2 := prevFiles[key]; ok2 && len(old.Chunks) > 0 {
					entry.Chunks = old.Chunks
				} else {
					entry.Chunks = []string{} // still mark present; cold restore may miss — re-upload
				}
				if len(entry.Chunks) > 0 {
					files = append(files, entry)
					return nil
				}
			}
			base := strings.ReplaceAll(key, "/", "__")
			if len(base) > 80 {
				base = sum[:16] + "_" + base[len(base)-40:]
			}
			if repoSize+size > MaxRepoBytes {
				if err := rotate(); err != nil {
					return err
				}
			}
			parts, err := ChunkFile(path, filepath.Join(dataDir, "chunks"), base)
			if err != nil {
				return nil
			}
			for _, part := range parts {
				entry.Chunks = append(entry.Chunks, backupRepo+":chunks/"+part)
				fi, _ := os.Stat(filepath.Join(dataDir, "chunks", part))
				if fi != nil {
					repoSize += fi.Size()
				}
			}
			files = append(files, entry)
			return nil
		})
		s.report(-1, "Finished %s (%d files)", rs.prefix, fileN)
	}
	for key := range prevHashes {
		if _, ok := newHashes[key]; !ok {
			files = append(files, FileEntry{Path: key, Deleted: true})
		}
	}
	_ = writeJSON(filepath.Join(dataDir, "hashes.json"), newHashes)
	fb, _ := MarshalPretty(files)
	_ = os.WriteFile(filepath.Join(dataDir, "files.json"), fb, 0o644)
	s.report(-1, "Uploading %s to GitHub", backupRepo)
	if err := gh.CommitPush(dataDir, "data update "+slug); err != nil {
		return nil, nil, err
	}
	return files, repos, nil
}

// uploadTree uploads every file under localRoot into a rotating GitHub repo set named baseRepo / baseRepo-002…
func (s *Service) uploadTree(gh *GitHub, work, baseRepo, desc, localRoot, pathPrefix string) ([]FileEntry, error) {
	slug := "sys"
	if baseRepo != SystemRepo {
		slug = Slug(baseRepo)
	}
	_ = slug
	roots := []rootSpec{{prefix: pathPrefix, root: localRoot}}
	// For system we use fixed repo name SystemRepo with rotation SystemRepo-002 via BackupRepoName style
	files, _, err := s.chunkNamed(gh, work, baseRepo, desc, roots)
	return files, err
}

func (s *Service) chunkNamed(gh *GitHub, work, baseRepo, desc string, roots []rootSpec) ([]FileEntry, []string, error) {
	backupIdx := 1
	backupRepo := baseRepo
	if backupIdx > 1 {
		backupRepo = fmt.Sprintf("%s-%03d", baseRepo, backupIdx)
	}
	var repoSize int64
	var repos []string
	ensure := func() error {
		name := baseRepo
		if backupIdx > 1 {
			name = fmt.Sprintf("%s-%03d", baseRepo, backupIdx)
		}
		backupRepo = name
		if err := gh.EnsureRepo(backupRepo, desc); err != nil {
			return err
		}
		found := false
		for _, r := range repos {
			if r == backupRepo {
				found = true
				break
			}
		}
		if !found {
			repos = append(repos, backupRepo)
		}
		return nil
	}
	if err := ensure(); err != nil {
		return nil, nil, err
	}
	dataDir := filepath.Join(work, backupRepo)
	_ = gh.CloneOrPull(backupRepo, dataDir)
	if _, err := os.Stat(filepath.Join(dataDir, ".git")); err != nil {
		_ = initGitRepo(dataDir, gh, backupRepo)
	}
	_ = os.MkdirAll(filepath.Join(dataDir, "chunks"), 0o750)
	prevHashes := map[string]string{}
	_ = readJSON(filepath.Join(dataDir, "hashes.json"), &prevHashes)
	prevFiles := map[string]FileEntry{}
	var prevList []FileEntry
	if readJSON(filepath.Join(dataDir, "files.json"), &prevList) == nil {
		for _, fe := range prevList {
			prevFiles[fe.Path] = fe
		}
	}
	newHashes := map[string]string{}
	var files []FileEntry
	rotate := func() error {
		_ = writeJSON(filepath.Join(dataDir, "hashes.json"), newHashes)
		_ = gh.CommitPush(dataDir, "rotate")
		backupIdx++
		repoSize = 0
		if err := ensure(); err != nil {
			return err
		}
		dataDir = filepath.Join(work, backupRepo)
		_ = gh.CloneOrPull(backupRepo, dataDir)
		if _, err := os.Stat(filepath.Join(dataDir, ".git")); err != nil {
			_ = initGitRepo(dataDir, gh, backupRepo)
		}
		_ = os.MkdirAll(filepath.Join(dataDir, "chunks"), 0o750)
		return nil
	}
	for _, rs := range roots {
		_ = filepath.Walk(rs.root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			rel, err := filepath.Rel(rs.root, path)
			if err != nil {
				return nil
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			key := rs.prefix + "/" + filepath.ToSlash(rel)
			if old, ok := prevFiles[key]; ok && old.Size == info.Size() && old.SHA256 != "" && len(old.Chunks) > 0 {
				newHashes[key] = old.SHA256
				files = append(files, old)
				return nil
			}
			sum, size, err := HashFile(path)
			if err != nil {
				return nil
			}
			newHashes[key] = sum
			entry := FileEntry{Path: key, Size: size, SHA256: sum}
			if prev, ok := prevHashes[key]; ok && prev == sum {
				if old, ok2 := prevFiles[key]; ok2 && len(old.Chunks) > 0 {
					entry.Chunks = old.Chunks
					files = append(files, entry)
					return nil
				}
			}
			base := strings.ReplaceAll(key, "/", "__")
			if len(base) > 80 {
				base = sum[:16] + "_" + base[len(base)-40:]
			}
			if repoSize+size > MaxRepoBytes {
				if err := rotate(); err != nil {
					return err
				}
			}
			parts, err := ChunkFile(path, filepath.Join(dataDir, "chunks"), base)
			if err != nil {
				return nil
			}
			for _, part := range parts {
				entry.Chunks = append(entry.Chunks, backupRepo+":chunks/"+part)
				fi, _ := os.Stat(filepath.Join(dataDir, "chunks", part))
				if fi != nil {
					repoSize += fi.Size()
				}
			}
			files = append(files, entry)
			return nil
		})
	}
	_ = writeJSON(filepath.Join(dataDir, "hashes.json"), newHashes)
	fb, _ := MarshalPretty(files)
	_ = os.WriteFile(filepath.Join(dataDir, "files.json"), fb, 0o644)
	if err := gh.CommitPush(dataDir, "system update"); err != nil {
		return nil, nil, err
	}
	return files, repos, nil
}

func (s *Service) InspectRemote(token string) (*Manifest, []SnapshotRecord, error) {
	gh := NewGitHub(token)
	u, err := gh.Validate()
	if err != nil {
		return nil, nil, err
	}
	gh.User = u.Login
	// check FORMAT
	tmp, _ := os.CreateTemp("", "fmt-*")
	name := tmp.Name()
	tmp.Close()
	defer os.Remove(name)
	if err := gh.DownloadFile(IndexRepo, "FORMAT", name); err != nil {
		return nil, nil, fmt.Errorf("no VPS MANAGE backup map found on this account (missing %s)", IndexRepo)
	}
	b, _ := os.ReadFile(name)
	if string(bytesTrim(b)) != FormatMagic {
		return nil, nil, fmt.Errorf("GitHub data is not organized in VPS MANAGE format")
	}
	var latest Manifest
	if err := gh.GetJSON(IndexRepo, "latest.json", &latest); err != nil {
		return nil, nil, fmt.Errorf("backup map incomplete: %w", err)
	}
	if err := ValidateManifest(&latest); err != nil {
		return nil, nil, err
	}
	var index []SnapshotRecord
	_ = gh.GetJSON(IndexRepo, "index.json", &index)
	return &latest, index, nil
}

func (s *Service) Restore(token, snapshotID string) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("backup/restore already running")
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	return s.executeRestore(token, snapshotID)
}

func (s *Service) executeRestore(token, snapshotID string) error {
	if token == "" {
		t, _, err := s.LoadToken()
		if err != nil || t == "" {
			return fmt.Errorf("GitHub token required")
		}
		token = t
	}
	gh := NewGitHub(token)
	u, err := gh.Validate()
	if err != nil {
		return err
	}
	gh.User = u.Login

	var man Manifest
	path := "latest.json"
	if snapshotID != "" && snapshotID != "latest" {
		path = "snapshots/" + snapshotID + "/manifest.json"
	}
	if err := gh.GetJSON(IndexRepo, path, &man); err != nil {
		return err
	}
	if err := ValidateManifest(&man); err != nil {
		return err
	}

	s.report(4, "Inspecting last restore point")
	cp := s.loadCheckpoint()
	if cp == nil || cp.Kind != "restore" || cp.SnapshotID != man.SnapshotID {
		cp = &Checkpoint{Kind: "restore", SnapshotID: man.SnapshotID, RoomsDone: []string{}, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
		s.saveCheckpoint(cp)
	} else {
		s.report(5, "Resuming restore from last point (%d rooms done)", len(cp.RoomsDone))
	}

	work := filepath.Join(s.WorkDir, "restore-"+man.SnapshotID[:8])
	_ = os.RemoveAll(work)
	_ = os.MkdirAll(work, 0o750)
	defer os.RemoveAll(work)

	// 1) Full system state first
	if len(man.SystemFiles) > 0 || man.FullBackup {
		if cp.SystemDone {
			s.report(10, "Panel database already restored — skipping")
		} else {
			s.report(8, "Restoring panel database, secrets & tokens")
			sysDir := filepath.Join(work, "system-out")
			_ = os.MkdirAll(sysDir, 0o750)
			if err := s.downloadEntries(gh, work, man.SystemFiles, sysDir, "system/"); err != nil {
				s.logf("system files: %v", err)
			}
			if err := s.applyRestoredSystem(sysDir); err != nil {
				return fmt.Errorf("apply system: %w", err)
			}
			cp.SystemDone = true
			s.saveCheckpoint(cp)
		}
	}

	for i, pm := range man.Projects {
		pct := 20 + (60 * i / max(len(man.Projects), 1))
		rid := pm.RoomID
		if rid == "" {
			rid = pm.RoomName
		}
		if checkpointHasRoom(cp, rid) {
			s.report(pct, "Room %s already restored — skipping", pm.RoomName)
			continue
		}
		s.report(pct, "Restoring %s (%d/%d)", pm.RoomName, i+1, len(man.Projects))
		if err := s.restoreProject(gh, work, pm); err != nil {
			return fmt.Errorf("%s: %w", pm.RoomName, err)
		}
		cp.RoomsDone = append(cp.RoomsDone, rid)
		s.saveCheckpoint(cp)
	}

	// Redeploy managed containers (skip live compose stacks — restore dumps instead)
	if s.Projects != nil {
		s.report(88, "Recreating managed containers")
		all, _ := s.Store.ListAllProjects()
		for _, p := range all {
			pdir := s.Rooms.ProjectDir(p.RoomID, p.ID)
			_, composeDir, composeProject, _ := projects.ProjectLayout(pdir)
			if composeDir != "" || composeProject != "" {
				s.report(-1, "Keeping compose stack %s (not recreating)", p.Name)
				s.restoreProjectDumps(p)
				continue
			}
			if err := s.Projects.Redeploy(p.ID); err != nil {
				s.report(-1, "redeploy %s: %v", p.Name, err)
			} else {
				s.report(-1, "Redeployed %s", p.Name)
			}
		}
	}
	if s.OnAfterRestore != nil {
		_ = s.OnAfterRestore()
	}
	_ = s.SaveToken(token)
	s.clearCheckpoint()
	return nil
}

func (s *Service) downloadEntries(gh *GitHub, work string, entries []FileEntry, destRoot, stripPrefix string) error {
	chunkCache := map[string]string{}
	for _, fe := range entries {
		if fe.Deleted {
			rel := strings.TrimPrefix(fe.Path, stripPrefix)
			_ = os.Remove(filepath.Join(destRoot, rel))
			continue
		}
		if len(fe.Chunks) == 0 {
			continue
		}
		rel := strings.TrimPrefix(fe.Path, stripPrefix)
		target := filepath.Join(destRoot, rel)
		if fe.SHA256 != "" {
			if sum, _, err := HashFile(target); err == nil && sum == fe.SHA256 {
				continue
			}
		}
		var locals []string
		for _, ref := range fe.Chunks {
			repo, relChunk, ok := splitRef(ref)
			if !ok {
				continue
			}
			if loc, hit := chunkCache[ref]; hit {
				locals = append(locals, loc)
				continue
			}
			dest := filepath.Join(work, "chunks", repo, filepath.Base(relChunk))
			if err := gh.DownloadFile(repo, relChunk, dest); err != nil {
				return err
			}
			chunkCache[ref] = dest
			locals = append(locals, dest)
		}
		if err := JoinChunks(locals, target); err != nil {
			return err
		}
		if fe.SHA256 != "" {
			sum, _, _ := HashFile(target)
			if sum != fe.SHA256 {
				return fmt.Errorf("checksum mismatch for %s", fe.Path)
			}
		}
	}
	return nil
}

func (s *Service) restoreProject(gh *GitHub, work string, pm ProjectMap) error {
	// Upsert room with stable ID from backup
	pass := pm.PassPlain
	if pass == "" {
		pass = "Restored#" + uuid.NewString()[:8]
	}
	hash := pm.PassHash
	if hash == "" {
		if h, err := auth.HashPassword(pass); err == nil {
			hash = h
		}
	}
	net := pm.NetworkName
	if net == "" {
		net = "vpsrooms_" + strings.ReplaceAll(pm.RoomID[:8], "-", "")
	}
	roomID := pm.RoomID
	if roomID == "" {
		roomID = uuid.NewString()
	}
	r := store.Room{
		ID: roomID, Name: pm.RoomName, PassHash: hash, PassPlain: pass,
		NetworkName: net, QuotaBytes: pm.QuotaBytes, CreatedAt: time.Now().UTC(),
	}
	if existing, _ := s.Store.GetRoom(roomID); existing == nil {
		if byName, _ := s.Store.GetRoomByName(pm.RoomName); byName != nil {
			roomID = byName.ID
			r.ID = roomID
		}
	}
	_ = s.Store.UpsertRoom(r)
	room, _ := s.Store.GetRoom(roomID)
	if room == nil {
		return fmt.Errorf("failed to upsert room")
	}

	// Ensure dirs
	_ = os.MkdirAll(filepath.Join(s.RoomsDir, room.ID), 0o700)
	_ = os.MkdirAll(filepath.Join(s.RuntimeDir, room.ID), 0o700)
	if s.Docker != nil {
		_ = s.Docker.EnsureNetwork(room.NetworkName)
	}

	// Upsert all projects
	projList := pm.Projects
	if len(projList) == 0 && pm.ProjectID != "" {
		projList = []ProjectMeta{{
			ID: pm.ProjectID, RoomID: room.ID, Name: pm.ProjectName, Image: pm.Image,
			HostPort: pm.HostPort, Domain: pm.Domain, ContainerPort: 80, DomainEnabled: true,
			DeployKind: "image", CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}}
	}
	for _, meta := range projList {
		ts, _ := time.Parse(time.RFC3339, meta.CreatedAt)
		p := store.Project{
			ID: meta.ID, RoomID: room.ID, Name: meta.Name, Image: meta.Image,
			HostPort: meta.HostPort, ContainerPort: meta.ContainerPort,
			Domain: meta.Domain, DomainEnabled: meta.DomainEnabled,
			SSLStatus: meta.SSLStatus, ExternalURL: meta.ExternalURL,
			Status: "restored", CreatedAt: ts, ContainerID: "",
		}
		if p.ContainerPort <= 0 {
			p.ContainerPort = 80
		}
		_ = s.Store.UpsertProject(p)
		_ = os.MkdirAll(s.Rooms.ProjectDir(room.ID, p.ID), 0o750)
		// env from project repo
		tmp := filepath.Join(work, "env-"+p.ID)
		if err := gh.DownloadFile(pm.ProjectRepo, "env-"+p.ID+".backup", tmp); err != nil {
			_ = gh.DownloadFile(pm.ProjectRepo, "env.backup", tmp)
		}
		if b, err := os.ReadFile(tmp); err == nil {
			_ = os.WriteFile(filepath.Join(s.Rooms.ProjectDir(room.ID, p.ID), ".env"), b, 0o600)
		}
	}

	_ = s.Rooms.EnsureUnlocked(room.ID)

	// Download files with runtime/ and vault/ prefixes
	chunkCache := map[string]string{}
	for _, fe := range pm.Files {
		if fe.Deleted {
			target := resolveBackupPath(s, room.ID, fe.Path)
			_ = os.Remove(target)
			continue
		}
		if len(fe.Chunks) == 0 {
			continue
		}
		var locals []string
		for _, ref := range fe.Chunks {
			repo, rel, ok := splitRef(ref)
			if !ok {
				continue
			}
			if loc, hit := chunkCache[ref]; hit {
				locals = append(locals, loc)
				continue
			}
			dest := filepath.Join(work, "chunks", repo, filepath.Base(rel))
			if err := gh.DownloadFile(repo, rel, dest); err != nil {
				return err
			}
			chunkCache[ref] = dest
			locals = append(locals, dest)
		}
		target := resolveBackupPath(s, room.ID, fe.Path)
		if err := JoinChunks(locals, target); err != nil {
			return err
		}
		sum, _, _ := HashFile(target)
		if fe.SHA256 != "" && sum != fe.SHA256 {
			return fmt.Errorf("checksum mismatch for %s", fe.Path)
		}
	}

	_ = s.Rooms.Seal(room.ID)
	_ = s.Rooms.EnsureUnlocked(room.ID)
	return nil
}

func resolveBackupPath(s *Service, roomID, fePath string) string {
	fePath = filepath.ToSlash(fePath)
	switch {
	case strings.HasPrefix(fePath, "runtime/"):
		return filepath.Join(s.RuntimeDir, roomID, strings.TrimPrefix(fePath, "runtime/"))
	case strings.HasPrefix(fePath, "vault/"):
		return filepath.Join(s.RoomsDir, roomID, strings.TrimPrefix(fePath, "vault/"))
	case strings.HasPrefix(fePath, "appdata/"):
		rest := strings.TrimPrefix(fePath, "appdata/")
		projID, rel, ok := strings.Cut(rest, "/")
		if !ok {
			return filepath.Join(s.RuntimeDir, roomID, rest)
		}
		pdir := s.Rooms.ProjectDir(roomID, projID)
		filesRoot, composeDir, _, _ := projects.ProjectLayout(pdir)
		dest := filesRoot
		if dest == "" {
			dest = composeDir
		}
		if dest == "" {
			dest = filepath.Join(filepath.Dir(s.RuntimeDir), "volumes", projID)
		}
		_ = os.MkdirAll(dest, 0o750)
		return filepath.Join(dest, rel)
	default:
		// legacy backups without prefix → runtime
		return filepath.Join(s.RuntimeDir, roomID, fePath)
	}
}

func (s *Service) logf(f string, args ...any) {
	s.lastLog = fmt.Sprintf(time.Now().Format("15:04:05")+" "+f, args...)
	dir := filepath.Join(s.DataDir, "logs")
	_ = os.MkdirAll(dir, 0o750)
	line := time.Now().UTC().Format("2006-01-02T15:04:05Z") + " " + fmt.Sprintf(f, args...) + "\n"
	fp := filepath.Join(dir, "backup.log")
	fhandle, err := os.OpenFile(fp, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = fhandle.WriteString(line)
	_ = fhandle.Close()
}

func skipBackupRel(rel string) bool {
	n := strings.ToLower(filepath.ToSlash(rel))
	if n == "." {
		return false
	}
	parts := strings.Split(n, "/")
	for _, p := range parts {
		if p == ".git" || p == "lost+found" || p == "pg_wal" || p == "pg_stat_tmp" ||
			p == "node_modules" || p == "__pycache__" || p == ".cache" || p == ".npm" {
			return true
		}
	}
	// Live Postgres cluster files — dumped via pg_dumpall instead.
	if strings.Contains(n, "/volumes/db/data") || strings.HasPrefix(n, "volumes/db/data") {
		return true
	}
	if strings.HasSuffix(n, "/db/data") || n == "db/data" {
		return true
	}
	return false
}

func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	if n < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
}

func (s *Service) restoreProjectDumps(p store.Project) {
	if s.Docker == nil || !s.Docker.Available() {
		return
	}
	pdir := s.Rooms.ProjectDir(p.RoomID, p.ID)
	dump := filepath.Join(pdir, "__dumps", "postgres.sql.gz")
	st, err := os.Stat(dump)
	if err != nil || st.Size() < 32 {
		return
	}
	_, _, composeProject, _ := projects.ProjectLayout(pdir)
	ctr := s.findPostgresContainer(p, composeProject)
	if ctr == "" {
		s.report(-1, "Postgres dump for %s found but no DB container is running", p.Name)
		return
	}
	s.report(-1, "Restoring Postgres dump into %s (%s)", ctr, formatBytes(st.Size()))
	var last error
	for _, user := range []string{"postgres", "supabase_admin"} {
		if err := s.Docker.RestorePostgresGzip(ctr, user, dump); err != nil {
			last = err
			continue
		}
		s.report(-1, "Postgres restore ok for %s", p.Name)
		return
	}
	if last != nil {
		s.report(-1, "Postgres restore %s: %v", p.Name, last)
	}
}

func (s *Service) findPostgresContainer(p store.Project, composeProject string) string {
	if s.Docker == nil {
		return ""
	}
	looksPG := func(name, image, service string) bool {
		n := strings.ToLower(name + " " + image + " " + service)
		return strings.Contains(n, "postgres") || service == "db" || strings.HasSuffix(strings.ToLower(name), "-db")
	}
	if composeProject != "" {
		list, err := s.Docker.ListCompose(composeProject)
		if err == nil {
			for _, c := range list {
				if looksPG(c.Name, c.Image, c.Service) {
					return c.Name
				}
			}
		}
	}
	if p.ContainerID != "" && imgLooksPostgres(p.Image) {
		return p.ContainerID
	}
	return ""
}

func (s *Service) loadCheckpoint() *Checkpoint {
	raw, ok, _ := s.Store.GetMeta("backup_checkpoint")
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	var cp Checkpoint
	if err := json.Unmarshal([]byte(raw), &cp); err != nil || cp.Kind == "" {
		return nil
	}
	return &cp
}

func (s *Service) saveCheckpoint(cp *Checkpoint) {
	if cp == nil {
		return
	}
	cp.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	b, err := json.Marshal(cp)
	if err != nil {
		return
	}
	_ = s.Store.SetMeta("backup_checkpoint", string(b))
}

func (s *Service) clearCheckpoint() {
	_ = s.Store.SetMeta("backup_checkpoint", "")
}

func checkpointHasRoom(cp *Checkpoint, id string) bool {
	if cp == nil || id == "" {
		return false
	}
	for _, x := range cp.RoomsDone {
		if x == id {
			return true
		}
	}
	return false
}

func splitRef(ref string) (repo, path string, ok bool) {
	i := -1
	for j := 0; j < len(ref); j++ {
		if ref[j] == ':' {
			i = j
			break
		}
	}
	if i <= 0 {
		return "", "", false
	}
	return ref[:i], ref[i+1:], true
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			out = append(out, line)
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	b, err := MarshalPretty(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func bytesTrim(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\r' || b[j-1] == '\t') {
		j--
	}
	return b[i:j]
}

func initGitRepo(dir string, gh *GitHub, repo string) error {
	_ = os.MkdirAll(dir, 0o750)
	_ = execRun("git", "init", dir)
	url := fmt.Sprintf("https://x-access-token:%s@github.com/%s/%s.git", gh.Token, gh.User, repo)
	_ = execRun("git", "-C", dir, "remote", "remove", "origin")
	return execRun("git", "-C", dir, "remote", "add", "origin", url)
}

func execRun(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %s", name, args, truncate(string(out), 200))
	}
	return nil
}

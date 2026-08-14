package backup

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/x5coder/vps-rooms/internal/projects"
	"github.com/x5coder/vps-rooms/internal/store"
)

// prepareSystemTree builds a local tree of panel-wide state to upload.
func (s *Service) prepareSystemTree(dest string) error {
	_ = os.RemoveAll(dest)
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return err
	}
	_ = os.WriteFile(filepath.Join(dest, "FORMAT"), []byte(FormatMagic+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dest, "FULL"), []byte("1\n"), 0o644)

	// Consistent SQLite snapshot
	dbDest := filepath.Join(dest, "panel.db")
	if s.DBPath != "" {
		if err := snapshotSQLite(s.DBPath, dbDest); err != nil {
			// fallback: copy file
			_ = copyFile(s.DBPath, dbDest)
		}
	}

	sec := filepath.Join(dest, "secrets")
	_ = os.MkdirAll(sec, 0o700)
	for _, name := range []string{"telegram.env", "github.env", "owner.env", "host.env", "notify.env"} {
		src := filepath.Join(s.DataDir, "secrets", name)
		if b, err := os.ReadFile(src); err == nil {
			_ = os.WriteFile(filepath.Join(sec, name), b, 0o600)
		}
	}
	if s.OwnerPass != "" {
		_ = os.WriteFile(filepath.Join(sec, "owner.env"),
			[]byte("VPS_ROOMS_OWNER_PASS="+s.OwnerPass+"\n"), 0o600)
	}

	// API tokens JSON (belt + suspenders with panel.db)
	if toks, err := s.Store.ListAPITokens(); err == nil {
		b, _ := MarshalPretty(toks)
		_ = os.WriteFile(filepath.Join(dest, "api_tokens.json"), b, 0o600)
	}

	if s.ProxyDir != "" {
		if b, err := os.ReadFile(filepath.Join(s.ProxyDir, "Caddyfile")); err == nil {
			_ = os.MkdirAll(filepath.Join(dest, "proxy"), 0o750)
			_ = os.WriteFile(filepath.Join(dest, "proxy", "Caddyfile"), b, 0o644)
		}
	}

	// Panel logs (last state)
	logSrc := filepath.Join(s.DataDir, "logs")
	if entries, err := os.ReadDir(logSrc); err == nil {
		ld := filepath.Join(dest, "logs")
		_ = os.MkdirAll(ld, 0o750)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			_ = copyFile(filepath.Join(logSrc, e.Name()), filepath.Join(ld, e.Name()))
		}
	}
	return nil
}

func snapshotSQLite(src, dest string) error {
	// Prefer sqlite3 CLI VACUUM INTO for a consistent copy while DB is open.
	if _, err := exec.LookPath("sqlite3"); err == nil {
		_ = os.Remove(dest)
		cmd := exec.Command("sqlite3", src, "VACUUM INTO '"+strings.ReplaceAll(dest, "'", "''")+"'")
		if b, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			_ = b
		}
	}
	// Go fallback: open read-only and backup via SQL dump of critical tables is heavy;
	// file copy after checkpoint pragma.
	db, err := sql.Open("sqlite", src+"?mode=ro")
	if err != nil {
		return copyFile(src, dest)
	}
	defer db.Close()
	_, _ = db.Exec(`PRAGMA wal_checkpoint(FULL)`)
	db.Close()
	return copyFile(src, dest)
}

func copyFile(src, dest string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	return os.WriteFile(dest, b, 0o600)
}

// captureProjectData dumps databases, storage, named volumes, and (for image-only
// apps) the container image into the project dir so they are included in the walk.
func (s *Service) captureProjectData(roomID string, p store.Project) {
	pdir := s.Rooms.ProjectDir(roomID, p.ID)
	_ = os.MkdirAll(pdir, 0o750)
	if s.Docker == nil || !s.Docker.Available() {
		return
	}

	_, composeDir, composeProject, _ := projects.ProjectLayout(pdir)
	isCompose := composeDir != "" || composeProject != ""

	if p.ContainerID != "" {
		st, _ := s.Docker.InspectStatus(p.ContainerID)
		if st != "missing" && !isCompose {
			if p.Image != "" {
				_ = os.WriteFile(filepath.Join(pdir, "__image_ref.txt"), []byte(strings.TrimSpace(p.Image)+"\n"), 0o644)
				imgTar := filepath.Join(pdir, "__container_image.tar")
				_ = os.Remove(imgTar)
				tag := strings.TrimSpace(p.Image)
				s.report(-1, "Saving Docker image %s", tag)
				if err := s.Docker.SaveImage(tag, imgTar); err != nil {
					s.report(-1, "docker save %s failed (%v) — committing container", tag, err)
					tmpTag := "vpsrooms-backup/" + p.ID + ":restore"
					if err2 := s.Docker.SaveCommittedImage(p.ContainerID, tmpTag, imgTar); err2 != nil {
						s.report(-1, "image backup failed for %s: %v", p.Name, err2)
						_ = os.Remove(imgTar)
					}
				}
				if st, err := os.Stat(imgTar); err == nil && st.Size() > 1024 {
					s.report(-1, "Image archive ready (%s)", formatBytes(st.Size()))
				}
			}
			_ = os.Remove(filepath.Join(pdir, "__container_export.tar"))
			mounts, err := s.Docker.ListMounts(p.ContainerID)
			if err == nil {
				volRoot := filepath.Join(pdir, "__volumes")
				for _, m := range mounts {
					if !strings.EqualFold(m.Type, "volume") || m.Name == "" {
						continue
					}
					s.report(-1, "Copying docker volume %s", m.Name)
					dest := filepath.Join(volRoot, m.Name)
					_ = os.RemoveAll(dest)
					if err := s.Docker.CopyVolumeToDir(m.Name, dest); err != nil {
						s.report(-1, "volume %s: %v", m.Name, err)
					}
				}
			}
		}
	}

	// Postgres (Supabase auth/db and any other Postgres in the stack)
	pg := s.findPostgresContainer(p, composeProject)
	if pg == "" && p.ContainerID != "" {
		// single-container postgres image
		if imgLooksPostgres(p.Image) {
			pg = p.ContainerID
		}
	}
	if pg != "" {
		dest := filepath.Join(pdir, "__dumps", "postgres.sql.gz")
		_ = os.MkdirAll(filepath.Dir(dest), 0o750)
		var last error
		ok := false
		for _, user := range []string{"postgres", "supabase_admin"} {
			s.report(-1, "Dumping Postgres from %s (user %s)", pg, user)
			if err := s.Docker.DumpPostgresGzip(pg, user, dest); err != nil {
				last = err
				_ = os.Remove(dest)
				continue
			}
			if st, err := os.Stat(dest); err == nil && st.Size() > 1024 {
				s.report(-1, "Postgres dump %s (%s) — includes auth users & all schemas", p.Name, formatBytes(st.Size()))
				ok = true
				break
			}
			_ = os.Remove(dest)
			last = fmt.Errorf("dump too small")
		}
		if !ok && last != nil {
			s.report(-1, "Postgres dump %s: %v — will include live db/data files instead", p.Name, last)
		}
	}

	// Named volumes belonging to a compose project (db-config, deno-cache, …)
	if composeProject != "" {
		list, err := s.Docker.ListCompose(composeProject)
		if err != nil {
			return
		}
		seenVol := map[string]bool{}
		for _, c := range list {
			mounts, err := s.Docker.ListMounts(c.Name)
			if err != nil {
				continue
			}
			for _, m := range mounts {
				if !strings.EqualFold(m.Type, "volume") || m.Name == "" || seenVol[m.Name] {
					continue
				}
				seenVol[m.Name] = true
				s.report(-1, "Copying compose volume %s", m.Name)
				dest := filepath.Join(pdir, "__volumes", m.Name)
				_ = os.RemoveAll(dest)
				if err := s.Docker.CopyVolumeToDir(m.Name, dest); err != nil {
					s.report(-1, "volume %s: %v", m.Name, err)
				}
			}
		}
	}
}

func imgLooksPostgres(image string) bool {
	n := strings.ToLower(image)
	return strings.Contains(n, "postgres") || strings.Contains(n, "supabase/postgres")
}

func (s *Service) applyRestoredSystem(sysDir string) error {
	// secrets
	secSrc := filepath.Join(sysDir, "secrets")
	secDst := filepath.Join(s.DataDir, "secrets")
	_ = os.MkdirAll(secDst, 0o700)
	if entries, err := os.ReadDir(secSrc); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			b, err := os.ReadFile(filepath.Join(secSrc, e.Name()))
			if err != nil {
				continue
			}
			_ = os.WriteFile(filepath.Join(secDst, e.Name()), b, 0o600)
		}
	}
	// owner pass into a drop-in for systemd if present
	if b, err := os.ReadFile(filepath.Join(secDst, "owner.env")); err == nil {
		_ = os.WriteFile(filepath.Join(s.DataDir, "owner.env"), b, 0o600)
	}

	if s.ProxyDir != "" {
		if b, err := os.ReadFile(filepath.Join(sysDir, "proxy", "Caddyfile")); err == nil {
			_ = os.MkdirAll(s.ProxyDir, 0o750)
			_ = os.WriteFile(filepath.Join(s.ProxyDir, "Caddyfile"), b, 0o644)
		}
	}

	// Merge panel.db into live store (rooms/projects/tokens/meta)
	dbPath := filepath.Join(sysDir, "panel.db")
	if _, err := os.Stat(dbPath); err == nil {
		if err := s.mergePanelDB(dbPath); err != nil {
			s.logf("merge panel.db: %v", err)
		}
		// keep a copy for reference
		_ = copyFile(dbPath, filepath.Join(s.DataDir, "panel.db.restored"))
	}

	// tokens JSON fallback
	if b, err := os.ReadFile(filepath.Join(sysDir, "api_tokens.json")); err == nil {
		var toks []store.APIToken
		if json.Unmarshal(b, &toks) == nil {
			for _, t := range toks {
				_ = s.Store.UpsertAPIToken(t)
			}
		}
	}

	logSrc := filepath.Join(sysDir, "logs")
	if entries, err := os.ReadDir(logSrc); err == nil {
		ld := filepath.Join(s.DataDir, "logs")
		_ = os.MkdirAll(ld, 0o750)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			_ = copyFile(filepath.Join(logSrc, e.Name()), filepath.Join(ld, e.Name()))
		}
	}
	return nil
}

func (s *Service) mergePanelDB(path string) error {
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.Query(`SELECT id,name,pass_hash,pass_plain,network_name,quota_bytes,created_at FROM rooms`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var r store.Room
		var ts string
		if err := rows.Scan(&r.ID, &r.Name, &r.PassHash, &r.PassPlain, &r.NetworkName, &r.QuotaBytes, &ts); err != nil {
			rows.Close()
			return err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		_ = s.Store.UpsertRoom(r)
	}
	rows.Close()

	prows, err := db.Query(`SELECT id,room_id,name,image,container_id,host_port,container_port,domain,status,created_at,
		COALESCE(domain_enabled,1),COALESCE(ssl_status,''),COALESCE(external_url,'') FROM projects`)
	if err != nil {
		return err
	}
	for prows.Next() {
		var p store.Project
		var ts string
		var den int
		if err := prows.Scan(&p.ID, &p.RoomID, &p.Name, &p.Image, &p.ContainerID, &p.HostPort, &p.ContainerPort,
			&p.Domain, &p.Status, &ts, &den, &p.SSLStatus, &p.ExternalURL); err != nil {
			prows.Close()
			return err
		}
		p.DomainEnabled = den != 0
		p.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		p.ContainerID = "" // force redeploy
		_ = s.Store.UpsertProject(p)
	}
	prows.Close()

	trows, err := db.Query(`SELECT id,name,token_hash,COALESCE(token_plain,''),token_prefix,mode,created_at,COALESCE(last_used_at,'') FROM api_tokens`)
	if err == nil {
		for trows.Next() {
			var t store.APIToken
			if err := trows.Scan(&t.ID, &t.Name, &t.TokenHash, &t.TokenPlain, &t.TokenPrefix, &t.Mode, &t.CreatedAt, &t.LastUsedAt); err != nil {
				break
			}
			_ = s.Store.UpsertAPIToken(t)
		}
		trows.Close()
	}

	mrows, err := db.Query(`SELECT key,value FROM meta`)
	if err == nil {
		for mrows.Next() {
			var k, v string
			if err := mrows.Scan(&k, &v); err != nil {
				break
			}
			// do not clobber in-progress job from restored snapshot
			if k == "backup_job" {
				continue
			}
			_ = s.Store.SetMeta(k, v)
		}
		mrows.Close()
	}
	return nil
}

package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB *sql.DB
}

type Room struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	PassHash    string    `json:"-"`
	PassPlain   string    `json:"password,omitempty"` // admin-only reveal
	NetworkName string    `json:"network_name"`
	QuotaBytes  int64     `json:"quota_bytes"`
	Kind        string    `json:"kind"` // single | multi
	Domain      string    `json:"domain,omitempty"`
	SSL         bool      `json:"ssl"`
	CreatedAt   time.Time `json:"created_at"`
}

type Project struct {
	ID            string    `json:"id"`
	RoomID        string    `json:"room_id"`
	Name          string    `json:"name"`
	Image         string    `json:"image"`
	ContainerID   string    `json:"container_id"`
	HostPort      int       `json:"host_port"`
	ContainerPort int       `json:"container_port"`
	Domain        string    `json:"domain"`
	DomainEnabled bool      `json:"domain_enabled"`
	SSLStatus     string    `json:"ssl_status"`
	ExternalURL   string    `json:"external_url"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

type Session struct {
	Token     string
	Kind      string // owner | room | gate
	RoomID    string
	ExpiresAt time.Time
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(1200)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	s := &Store{DB: db}
	_, _ = db.Exec(`PRAGMA journal_mode=WAL`)
	_, _ = db.Exec(`PRAGMA busy_timeout=1200`)
	_, _ = db.Exec(`PRAGMA synchronous=NORMAL`)
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	_, err := s.DB.Exec(`
CREATE TABLE IF NOT EXISTS meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS rooms (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  pass_hash TEXT NOT NULL,
  pass_plain TEXT NOT NULL DEFAULT '',
  network_name TEXT NOT NULL,
  quota_bytes INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  image TEXT NOT NULL DEFAULT '',
  container_id TEXT NOT NULL DEFAULT '',
  host_port INTEGER NOT NULL DEFAULT 0,
  container_port INTEGER NOT NULL DEFAULT 80,
  domain TEXT NOT NULL DEFAULT '',
  domain_enabled INTEGER NOT NULL DEFAULT 1,
  ssl_status TEXT NOT NULL DEFAULT '',
  external_url TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'created',
  created_at TEXT NOT NULL,
  UNIQUE(room_id, name)
);
CREATE TABLE IF NOT EXISTS sessions (
  token TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  room_id TEXT NOT NULL DEFAULT '',
  expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS api_tokens (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  token_plain TEXT NOT NULL DEFAULT '',
  token_prefix TEXT NOT NULL,
  mode TEXT NOT NULL DEFAULT 'read',
  created_at TEXT NOT NULL,
  last_used_at TEXT NOT NULL DEFAULT ''
);
`)
	if err != nil {
		return err
	}
	// migrate older DBs missing pass_plain
	_, _ = s.DB.Exec(`ALTER TABLE rooms ADD COLUMN pass_plain TEXT NOT NULL DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE projects ADD COLUMN domain_enabled INTEGER NOT NULL DEFAULT 1`)
	_, _ = s.DB.Exec(`ALTER TABLE projects ADD COLUMN ssl_status TEXT NOT NULL DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE projects ADD COLUMN external_url TEXT NOT NULL DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE api_tokens ADD COLUMN token_plain TEXT NOT NULL DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE api_tokens ADD COLUMN room_id TEXT NOT NULL DEFAULT ''`)
	return s.migrateV2()
}

func (s *Store) GetMeta(key string) (string, bool, error) {
	var v string
	err := s.DB.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *Store) SetMeta(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) DeleteMeta(key string) error {
	_, err := s.DB.Exec(`DELETE FROM meta WHERE key=?`, key)
	return err
}

func (s *Store) ListMetaPrefix(prefix string) ([][2]string, error) {
	rows, err := s.DB.Query(`SELECT key, value FROM meta WHERE key LIKE ?`, prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out = append(out, [2]string{k, v})
	}
	return out, rows.Err()
}

func (s *Store) CreateRoom(r Room) error {
	if r.Kind == "" {
		r.Kind = KindSingle
	}
	ssl := 0
	if r.SSL {
		ssl = 1
	}
	_, err := s.DB.Exec(`INSERT INTO rooms(id,name,pass_hash,pass_plain,network_name,quota_bytes,created_at,kind,domain,ssl)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, r.ID, r.Name, r.PassHash, r.PassPlain, r.NetworkName, r.QuotaBytes, r.CreatedAt.UTC().Format(time.RFC3339), r.Kind, r.Domain, ssl)
	return err
}

func scanRoom(rows interface {
	Scan(dest ...any) error
}) (Room, error) {
	var r Room
	var ts string
	var ssl int
	err := rows.Scan(&r.ID, &r.Name, &r.PassHash, &r.PassPlain, &r.NetworkName, &r.QuotaBytes, &ts, &r.Kind, &r.Domain, &ssl)
	if err != nil {
		return r, err
	}
	r.SSL = ssl != 0
	if r.Kind == "" {
		r.Kind = KindSingle
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339, ts)
	return r, nil
}

const roomCols = `id,name,pass_hash,pass_plain,network_name,quota_bytes,created_at,COALESCE(kind,'single'),COALESCE(domain,''),COALESCE(ssl,0)`

func (s *Store) ListRooms() ([]Room, error) {
	rows, err := s.DB.Query(`SELECT ` + roomCols + ` FROM rooms ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Room
	for rows.Next() {
		r, err := scanRoom(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetRoom(id string) (*Room, error) {
	row := s.DB.QueryRow(`SELECT `+roomCols+` FROM rooms WHERE id=?`, id)
	r, err := scanRoom(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) GetRoomByName(name string) (*Room, error) {
	row := s.DB.QueryRow(`SELECT `+roomCols+` FROM rooms WHERE name = ? COLLATE NOCASE`, strings.TrimSpace(name))
	r, err := scanRoom(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) UpdateRoom(r Room) error {
	if r.Kind == "" {
		r.Kind = KindSingle
	}
	ssl := 0
	if r.SSL {
		ssl = 1
	}
	_, err := s.DB.Exec(`UPDATE rooms SET name=?, pass_hash=?, pass_plain=?, network_name=?, quota_bytes=?, kind=?, domain=?, ssl=? WHERE id=?`,
		r.Name, r.PassHash, r.PassPlain, r.NetworkName, r.QuotaBytes, r.Kind, r.Domain, ssl, r.ID)
	return err
}

// UpsertRoom inserts or updates a room by id (used by full restore).
func (s *Store) UpsertRoom(r Room) error {
	if r.Kind == "" {
		r.Kind = KindSingle
	}
	ssl := 0
	if r.SSL {
		ssl = 1
	}
	_, err := s.DB.Exec(`INSERT INTO rooms(id,name,pass_hash,pass_plain,network_name,quota_bytes,created_at,kind,domain,ssl)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name,
			pass_hash=excluded.pass_hash,
			pass_plain=excluded.pass_plain,
			network_name=excluded.network_name,
			quota_bytes=excluded.quota_bytes,
			kind=excluded.kind,
			domain=excluded.domain,
			ssl=excluded.ssl`,
		r.ID, r.Name, r.PassHash, r.PassPlain, r.NetworkName, r.QuotaBytes, r.CreatedAt.UTC().Format(time.RFC3339), r.Kind, r.Domain, ssl)
	return err
}

// UpsertProject inserts or updates a project by id (used by full restore).
func (s *Store) UpsertProject(p Project) error {
	den := boolInt(p.DomainEnabled)
	if p.Domain == "" {
		den = 1
	}
	ts := p.CreatedAt.UTC().Format(time.RFC3339)
	if p.CreatedAt.IsZero() {
		ts = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := s.DB.Exec(`INSERT INTO projects(id,room_id,name,image,container_id,host_port,container_port,domain,status,created_at,domain_enabled,ssl_status,external_url)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			room_id=excluded.room_id,
			name=excluded.name,
			image=excluded.image,
			container_id=excluded.container_id,
			host_port=excluded.host_port,
			container_port=excluded.container_port,
			domain=excluded.domain,
			status=excluded.status,
			domain_enabled=excluded.domain_enabled,
			ssl_status=excluded.ssl_status,
			external_url=excluded.external_url`,
		p.ID, p.RoomID, p.Name, p.Image, p.ContainerID, p.HostPort, p.ContainerPort, p.Domain, p.Status, ts, den, p.SSLStatus, p.ExternalURL)
	return err
}

func (s *Store) DeleteRoom(id string) error {
	_, err := s.DB.Exec(`DELETE FROM rooms WHERE id=?`, id)
	return err
}

func (s *Store) CreateProject(p Project) error {
	den := 1
	if p.Domain != "" && !p.DomainEnabled {
		den = 0
	}
	_, err := s.DB.Exec(`INSERT INTO projects(id,room_id,name,image,container_id,host_port,container_port,domain,status,created_at,domain_enabled,ssl_status,external_url)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.RoomID, p.Name, p.Image, p.ContainerID, p.HostPort, p.ContainerPort, p.Domain, p.Status, p.CreatedAt.UTC().Format(time.RFC3339),
		den, p.SSLStatus, p.ExternalURL)
	if err != nil {
		return err
	}
	s.SyncContainerFromProject(p)
	return nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func scanProject(scanner interface {
	Scan(dest ...any) error
}) (Project, error) {
	var p Project
	var ts string
	var den int
	err := scanner.Scan(&p.ID, &p.RoomID, &p.Name, &p.Image, &p.ContainerID, &p.HostPort, &p.ContainerPort, &p.Domain, &p.Status, &ts, &den, &p.SSLStatus, &p.ExternalURL)
	if err != nil {
		return p, err
	}
	p.DomainEnabled = den != 0
	p.CreatedAt, _ = time.Parse(time.RFC3339, ts)
	return p, nil
}

const projectCols = `id,room_id,name,image,container_id,host_port,container_port,domain,status,created_at,domain_enabled,ssl_status,external_url`

func (s *Store) ListProjects(roomID string) ([]Project, error) {
	rows, err := s.DB.Query(`SELECT `+projectCols+` FROM projects WHERE room_id=? ORDER BY created_at`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ListAllProjects() ([]Project, error) {
	rows, err := s.DB.Query(`SELECT ` + projectCols + ` FROM projects ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProject(id string) (*Project, error) {
	row := s.DB.QueryRow(`SELECT `+projectCols+` FROM projects WHERE id=?`, id)
	p, err := scanProject(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) UpdateProject(p Project) error {
	res, err := s.DB.Exec(`UPDATE projects SET name=?, image=?, container_id=?, host_port=?, container_port=?, domain=?, status=?, domain_enabled=?, ssl_status=?, external_url=? WHERE id=?`,
		p.Name, p.Image, p.ContainerID, p.HostPort, p.ContainerPort, p.Domain, p.Status, boolInt(p.DomainEnabled), p.SSLStatus, p.ExternalURL, p.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("project not found")
	}
	s.SyncContainerFromProject(p)
	return nil
}

func (s *Store) ReleaseDomain(domain, keepProjectID string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return nil
	}
	list, err := s.ListAllProjects()
	if err != nil {
		return err
	}
	for _, p := range list {
		if p.ID == keepProjectID {
			continue
		}
		if strings.ToLower(strings.TrimSpace(p.Domain)) != domain {
			continue
		}
		p.Domain = ""
		p.DomainEnabled = false
		p.SSLStatus = "disabled"
		if err := s.UpdateProject(p); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteProject(id string) error {
	_, _ = s.DB.Exec(`DELETE FROM containers WHERE id=?`, id)
	_, err := s.DB.Exec(`DELETE FROM projects WHERE id=?`, id)
	return err
}

func (s *Store) SaveSession(sess Session) error {
	_, err := s.DB.Exec(`INSERT INTO sessions(token,kind,room_id,expires_at) VALUES(?,?,?,?)`,
		sess.Token, sess.Kind, sess.RoomID, sess.ExpiresAt.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) GetSession(token string) (*Session, error) {
	var sess Session
	var ts string
	err := s.DB.QueryRow(`SELECT token,kind,room_id,expires_at FROM sessions WHERE token=?`, token).
		Scan(&sess.Token, &sess.Kind, &sess.RoomID, &ts)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sess.ExpiresAt, _ = time.Parse(time.RFC3339, ts)
	if time.Now().After(sess.ExpiresAt) {
		_ = s.DeleteSession(token)
		return nil, nil
	}
	return &sess, nil
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE token=?`, token)
	return err
}

func (s *Store) CleanupSessions() error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().UTC().Format(time.RFC3339))
	return err
}

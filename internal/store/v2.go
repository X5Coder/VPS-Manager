package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	KindSingle = "single"
	KindMulti  = "multi"
)

type Container struct {
	ID            string    `json:"id"`
	RoomID        string    `json:"room_id"`
	Ordinal       int       `json:"ordinal"`
	Name          string    `json:"name"`
	Service       string    `json:"service"`
	Image         string    `json:"image"`
	DockerID      string    `json:"docker_id"`
	HostPort      int       `json:"host_port"`
	ContainerPort int       `json:"container_port"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

type ImageRec struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	Ordinal   int       `json:"ordinal"`
	Name      string    `json:"name"`
	Ref       string    `json:"ref"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

type VolumeRec struct {
	ID         string    `json:"id"`
	RoomID     string    `json:"room_id"`
	Ordinal    int       `json:"ordinal"`
	Name       string    `json:"name"`
	DockerName string    `json:"docker_name"`
	SizeBytes  int64     `json:"size_bytes"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Store) migrateV2() error {
	_, _ = s.DB.Exec(`ALTER TABLE rooms ADD COLUMN kind TEXT NOT NULL DEFAULT 'single'`)
	_, _ = s.DB.Exec(`ALTER TABLE rooms ADD COLUMN domain TEXT NOT NULL DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE rooms ADD COLUMN ssl INTEGER NOT NULL DEFAULT 0`)
	_, err := s.DB.Exec(`
CREATE TABLE IF NOT EXISTS containers (
  id TEXT PRIMARY KEY,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  ordinal INTEGER NOT NULL DEFAULT 1,
  name TEXT NOT NULL,
  service TEXT NOT NULL DEFAULT '',
  image TEXT NOT NULL DEFAULT '',
  docker_id TEXT NOT NULL DEFAULT '',
  host_port INTEGER NOT NULL DEFAULT 0,
  container_port INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(room_id, ordinal)
);
CREATE TABLE IF NOT EXISTS images (
  id TEXT PRIMARY KEY,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  ordinal INTEGER NOT NULL DEFAULT 1,
  name TEXT NOT NULL,
  ref TEXT NOT NULL DEFAULT '',
  size_bytes INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  UNIQUE(room_id, ordinal)
);
CREATE TABLE IF NOT EXISTS volumes (
  id TEXT PRIMARY KEY,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  ordinal INTEGER NOT NULL DEFAULT 1,
  name TEXT NOT NULL,
  docker_name TEXT NOT NULL DEFAULT '',
  size_bytes INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  UNIQUE(room_id, ordinal)
);
`)
	if err != nil {
		return err
	}
	return s.seedContainersFromProjects()
}

// seedContainersFromProjects copies each existing project row into containers/images
// once. Running Docker containers are not touched.
func (s *Store) seedContainersFromProjects() error {
	done, _, _ := s.GetMeta("v2_seed_containers")
	if done == "1" {
		return nil
	}
	rooms, err := s.ListRooms()
	if err != nil {
		return err
	}
	for _, rm := range rooms {
		existing, _ := s.ListContainers(rm.ID)
		if len(existing) > 0 {
			continue
		}
		projs, err := s.ListProjects(rm.ID)
		if err != nil {
			return err
		}
		kind := KindSingle
		if len(projs) > 1 {
			kind = KindMulti
		}
		if kind == KindMulti && rm.Kind != KindMulti {
			_ = s.SetRoomKind(rm.ID, KindMulti)
		}
		for i, p := range projs {
			ord := i + 1
			ct := Container{
				ID: p.ID, RoomID: rm.ID, Ordinal: ord,
				Name: p.Name, Service: p.Name, Image: p.Image, DockerID: p.ContainerID,
				HostPort: p.HostPort, ContainerPort: p.ContainerPort,
				Status: p.Status, CreatedAt: p.CreatedAt,
			}
			if err := s.UpsertContainer(ct); err != nil {
				return err
			}
			if strings.TrimSpace(p.Image) == "" {
				continue
			}
			img := ImageRec{
				ID: p.ID + "-img", RoomID: rm.ID, Ordinal: ord,
				Name: imageBaseName(p.Image), Ref: p.Image, CreatedAt: p.CreatedAt,
			}
			_ = s.UpsertImage(img)
		}
	}
	return s.SetMeta("v2_seed_containers", "1")
}

func imageBaseName(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	if i := strings.Index(ref, ":"); i >= 0 {
		ref = ref[:i]
	}
	if ref == "" {
		return "image"
	}
	return ref
}

func (s *Store) SetRoomKind(id, kind string) error {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != KindMulti {
		kind = KindSingle
	}
	_, err := s.DB.Exec(`UPDATE rooms SET kind=? WHERE id=?`, kind, id)
	return err
}

func (s *Store) NextContainerOrdinal(roomID string) int {
	var n int
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(ordinal),0) FROM containers WHERE room_id=?`, roomID).Scan(&n)
	return n + 1
}

func (s *Store) NextImageOrdinal(roomID string) int {
	var n int
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(ordinal),0) FROM images WHERE room_id=?`, roomID).Scan(&n)
	return n + 1
}

func (s *Store) NextVolumeOrdinal(roomID string) int {
	var n int
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(ordinal),0) FROM volumes WHERE room_id=?`, roomID).Scan(&n)
	return n + 1
}

func (s *Store) ListContainers(roomID string) ([]Container, error) {
	rows, err := s.DB.Query(`SELECT id,room_id,ordinal,name,service,image,docker_id,host_port,container_port,status,created_at FROM containers WHERE room_id=? ORDER BY ordinal`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Container
	for rows.Next() {
		c, err := scanContainer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanContainer(sc interface{ Scan(dest ...any) error }) (Container, error) {
	var c Container
	var ts string
	err := sc.Scan(&c.ID, &c.RoomID, &c.Ordinal, &c.Name, &c.Service, &c.Image, &c.DockerID, &c.HostPort, &c.ContainerPort, &c.Status, &ts)
	if err != nil {
		return c, err
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339, ts)
	return c, nil
}

func (s *Store) GetContainer(id string) (*Container, error) {
	row := s.DB.QueryRow(`SELECT id,room_id,ordinal,name,service,image,docker_id,host_port,container_port,status,created_at FROM containers WHERE id=?`, id)
	c, err := scanContainer(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) UpsertContainer(c Container) error {
	if c.Ordinal <= 0 {
		c.Ordinal = s.NextContainerOrdinal(c.RoomID)
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	ts := c.CreatedAt.UTC().Format(time.RFC3339)
	_, err := s.DB.Exec(`INSERT INTO containers(id,room_id,ordinal,name,service,image,docker_id,host_port,container_port,status,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, service=excluded.service, image=excluded.image,
			docker_id=excluded.docker_id, host_port=excluded.host_port,
			container_port=excluded.container_port, status=excluded.status`,
		c.ID, c.RoomID, c.Ordinal, c.Name, c.Service, c.Image, c.DockerID, c.HostPort, c.ContainerPort, c.Status, ts)
	return err
}

func (s *Store) DeleteContainer(id string) error {
	_, err := s.DB.Exec(`DELETE FROM containers WHERE id=?`, id)
	return err
}

func (s *Store) ListImages(roomID string) ([]ImageRec, error) {
	rows, err := s.DB.Query(`SELECT id,room_id,ordinal,name,ref,size_bytes,created_at FROM images WHERE room_id=? ORDER BY ordinal`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImageRec
	for rows.Next() {
		var im ImageRec
		var ts string
		if err := rows.Scan(&im.ID, &im.RoomID, &im.Ordinal, &im.Name, &im.Ref, &im.SizeBytes, &ts); err != nil {
			return nil, err
		}
		im.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, im)
	}
	return out, rows.Err()
}

func (s *Store) UpsertImage(im ImageRec) error {
	if im.Ordinal <= 0 {
		im.Ordinal = s.NextImageOrdinal(im.RoomID)
	}
	if im.CreatedAt.IsZero() {
		im.CreatedAt = time.Now().UTC()
	}
	ts := im.CreatedAt.UTC().Format(time.RFC3339)
	_, err := s.DB.Exec(`INSERT INTO images(id,room_id,ordinal,name,ref,size_bytes,created_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, ref=excluded.ref, size_bytes=excluded.size_bytes`,
		im.ID, im.RoomID, im.Ordinal, im.Name, im.Ref, im.SizeBytes, ts)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		im.ID = fmt.Sprintf("%s-img-%d", im.RoomID[:8], im.Ordinal)
		_, err = s.DB.Exec(`INSERT INTO images(id,room_id,ordinal,name,ref,size_bytes,created_at)
			VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, ref=excluded.ref, size_bytes=excluded.size_bytes`,
			im.ID, im.RoomID, im.Ordinal, im.Name, im.Ref, im.SizeBytes, ts)
	}
	return err
}

func (s *Store) ListVolumes(roomID string) ([]VolumeRec, error) {
	rows, err := s.DB.Query(`SELECT id,room_id,ordinal,name,docker_name,size_bytes,created_at FROM volumes WHERE room_id=? ORDER BY ordinal`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VolumeRec
	for rows.Next() {
		var v VolumeRec
		var ts string
		if err := rows.Scan(&v.ID, &v.RoomID, &v.Ordinal, &v.Name, &v.DockerName, &v.SizeBytes, &ts); err != nil {
			return nil, err
		}
		v.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) UpsertVolume(v VolumeRec) error {
	if v.Ordinal <= 0 {
		v.Ordinal = s.NextVolumeOrdinal(v.RoomID)
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	ts := v.CreatedAt.UTC().Format(time.RFC3339)
	_, err := s.DB.Exec(`INSERT INTO volumes(id,room_id,ordinal,name,docker_name,size_bytes,created_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, docker_name=excluded.docker_name, size_bytes=excluded.size_bytes`,
		v.ID, v.RoomID, v.Ordinal, v.Name, v.DockerName, v.SizeBytes, ts)
	return err
}

func (s *Store) DeleteVolume(id string) error {
	_, err := s.DB.Exec(`DELETE FROM volumes WHERE id=?`, id)
	return err
}

func (s *Store) SyncContainerFromProject(p Project) {
	if p.ID == "" || p.RoomID == "" {
		return
	}
	cur, _ := s.GetContainer(p.ID)
	ord := 1
	if cur != nil {
		ord = cur.Ordinal
	} else {
		ord = s.NextContainerOrdinal(p.RoomID)
	}
	_ = s.UpsertContainer(Container{
		ID: p.ID, RoomID: p.RoomID, Ordinal: ord,
		Name: p.Name, Service: p.Name, Image: p.Image, DockerID: p.ContainerID,
		HostPort: p.HostPort, ContainerPort: p.ContainerPort,
		Status: p.Status, CreatedAt: p.CreatedAt,
	})
	if strings.TrimSpace(p.Image) != "" {
		imgs, _ := s.ListImages(p.RoomID)
		found := false
		for _, im := range imgs {
			if im.Ref == p.Image {
				found = true
				break
			}
		}
		if !found {
			_ = s.UpsertImage(ImageRec{
				ID: p.ID + "-img", RoomID: p.RoomID, Name: imageBaseName(p.Image), Ref: p.Image,
			})
		}
	}
}

func ShortRoomID(id string) string {
	id = strings.ReplaceAll(id, "-", "")
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/x5coder/vps-rooms/internal/store"
)

const RoomSnapFormat = "VPS-ROOM-SNAP-v1"
const RoomRepoPrefix = "vps-room-"

type RoomSnapState struct {
	Repo        string `json:"repo"`
	Seq         int    `json:"seq"`
	At          string `json:"at"`
	OK          bool   `json:"ok"`
	Fingerprint string `json:"fingerprint"`
}

type RoomSnapView struct {
	RoomID string `json:"room_id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Repo   string `json:"repo"`
	Seq    int    `json:"seq"`
	At     string `json:"at"`
	OK     bool   `json:"ok"`
}

type RoomRemote struct {
	Name   string `json:"name"`
	RoomID string `json:"room_id"`
	Repo   string `json:"repo"`
	Seq    int    `json:"seq"`
	At     string `json:"at"`
	Kind   string `json:"kind,omitempty"`
}

type RoomSnapManifest struct {
	Format      string           `json:"format"`
	RoomID      string           `json:"room_id"`
	Name        string           `json:"name"`
	Seq         int              `json:"seq"`
	Repo        string           `json:"repo"`
	At          string           `json:"at"`
	Fingerprint string           `json:"fingerprint"`
	Room        RoomBackupMeta   `json:"room"`
	Blobs       []SnapBlob       `json:"blobs"`
	Images      []SnapImage      `json:"images"`
	Volumes     []SnapVolume     `json:"volumes"`
	Containers  []SnapContainer  `json:"containers"`
}

type SnapImage struct {
	Ref      string   `json:"ref"`
	DockerID string   `json:"docker_id,omitempty"`
	Prefix   string   `json:"prefix"`
	Files    []string `json:"files"`
}

type SnapVolume struct {
	Rec     store.VolumeRec `json:"rec"`
	BlobKey string          `json:"blob_key"`
}

type SnapContainer struct {
	Rec         store.Container `json:"rec"`
	InspectKey  string          `json:"inspect_key"`
}

func roomSnapMetaKey(roomID string) string {
	return "room_snap_" + roomID
}

func (s *Service) loadRoomSnap(roomID string) *RoomSnapState {
	raw, ok, _ := s.Store.GetMeta(roomSnapMetaKey(roomID))
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	var st RoomSnapState
	if json.Unmarshal([]byte(raw), &st) != nil {
		return nil
	}
	return &st
}

func (s *Service) saveRoomSnap(roomID string, st RoomSnapState) {
	b, _ := json.Marshal(st)
	_ = s.Store.SetMeta(roomSnapMetaKey(roomID), string(b))
}

func (s *Service) listRoomSnapViews() []RoomSnapView {
	rooms, err := s.Store.ListRooms()
	if err != nil {
		return nil
	}
	out := make([]RoomSnapView, 0, len(rooms))
	for _, rm := range rooms {
		v := RoomSnapView{RoomID: rm.ID, Name: rm.Name, Kind: rm.Kind}
		if st := s.loadRoomSnap(rm.ID); st != nil {
			v.Repo, v.Seq, v.At, v.OK = st.Repo, st.Seq, st.At, st.OK
		}
		out = append(out, v)
	}
	return out
}

func roomSlugID(roomID string) string {
	out := strings.ToLower(store.ShortRoomID(roomID))
	var b strings.Builder
	for _, r := range out {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	out = b.String()
	if out == "" {
		out = "room"
	}
	return out
}

func roomRepoName(roomID string, seq int) string {
	slug := roomSlugID(roomID)
	if seq < 1 {
		seq = 1
	}
	return fmt.Sprintf("%s%s-%d", RoomRepoPrefix, slug, seq)
}

func parseRepoSeq(repo, slug string) int {
	prefix := RoomRepoPrefix + slug + "-"
	low := strings.ToLower(repo)
	if !strings.HasPrefix(low, prefix) {
		return 0
	}
	rest := repo[len(prefix):]
	n := 0
	if _, err := fmt.Sscanf(rest, "%d", &n); err != nil || n < 1 {
		return 0
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return 0
		}
	}
	return n
}

func (s *Service) nextRoomRepo(gh *GitHub, roomID string, localSeq int) (string, int, error) {
	slug := roomSlugID(roomID)
	seq := localSeq
	if seq < 1 {
		seq = 1
	}
	if gh != nil {
		names, err := gh.ListOwnerRepoNames()
		if err == nil {
			max := 0
			for _, n := range names {
				if v := parseRepoSeq(n, slug); v > max {
					max = v
				}
			}
			if max+1 > seq {
				seq = max + 1
			}
		}
	}
	for i := 0; i < 50; i++ {
		repo := roomRepoName(roomID, seq)
		if gh == nil {
			return repo, seq, nil
		}
		exists, err := gh.RepoExists(repo)
		if err != nil {
			return "", 0, err
		}
		if !exists {
			return repo, seq, nil
		}
		seq++
	}
	return "", 0, fmt.Errorf("could not allocate a free GitHub repository name for room %s", roomSlugID(roomID))
}

func (s *Service) cleanBackupTemps() {
	if s.WorkDir != "" {
		entries, _ := os.ReadDir(s.WorkDir)
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(s.WorkDir, e.Name()))
		}
	}
	for _, pat := range []string{
		"/tmp/vps-parts-*",
		"/tmp/vps-join-*",
		"/tmp/vm-vol-*",
		"/tmp/vm-rvol-*",
		"/tmp/vps-pat-probe-*",
		"/tmp/gh-json-*",
	} {
		matches, _ := filepath.Glob(pat)
		for _, m := range matches {
			_ = os.RemoveAll(m)
		}
	}
}

func (s *Service) runRoomSnapshots(scheduled bool) (*SnapshotRecord, error) {
	token, user, err := s.LoadToken()
	if err != nil || token == "" {
		return nil, fmt.Errorf("GitHub PAT required")
	}
	gh := NewGitHub(token)
	gh.User = user
	gh.Ctx = s.jobContext()
	if gh.User == "" {
		u, err := gh.Validate()
		if err != nil {
			return nil, err
		}
		gh.User = u.Login
	}
	rooms, err := s.Store.ListRooms()
	if err != nil {
		return nil, err
	}
	lastRepo := ""
	n := len(rooms)
	done := 0
	skipped := 0
	defer s.cleanBackupTemps()
	for i, rm := range rooms {
		if err := s.errIfStopped(); err != nil {
			return nil, err
		}
		fp := s.roomFingerprint(rm)
		st := s.loadRoomSnap(rm.ID)
		if st != nil && st.OK && st.Fingerprint == fp && st.Repo != "" {
			s.report(pctRooms(i, n), "Room %s unchanged — skip", rm.Name)
			skipped++
			continue
		}
		repo, err := s.backupOneRoom(gh, rm, fp, i, n)
		if err != nil {
			return nil, fmt.Errorf("room %s: %w", rm.Name, err)
		}
		lastRepo = repo
		done++
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_ = s.Store.SetMeta("backup_last_at", now)
	_ = s.Store.SetMeta("backup_last_error", "")
	s.armFrom(time.Now().UTC())
	s.report(100, "Finished — %d uploaded, %d unchanged", done, skipped)
	id := lastRepo
	if id == "" {
		id = "skip-" + time.Now().UTC().Format("20060102T150405")
	}
	return &SnapshotRecord{ID: id, Label: "Room snapshots", Status: "ok", CreatedAt: now}, nil
}

func pctRooms(i, n int) int {
	if n <= 0 {
		return 20
	}
	p := 8 + (i * 84 / n)
	if p > 95 {
		p = 95
	}
	if p < 8 {
		p = 8
	}
	return p
}

func (s *Service) backupOneRoom(gh *GitHub, room store.Room, fp string, idx, total int) (string, error) {
	prev := s.loadRoomSnap(room.ID)
	seq := 1
	oldRepo := ""
	if prev != nil {
		seq = prev.Seq + 1
		oldRepo = prev.Repo
	}
	repo, seq, err := s.nextRoomRepo(gh, room.ID, seq)
	if err != nil {
		return "", err
	}
	s.report(pctRooms(idx, total), "Room %s — create %s", room.Name, repo)

	work := filepath.Join(s.WorkDir, "room-snap-"+roomSlugID(room.ID))
	_ = os.RemoveAll(work)
	if err := os.MkdirAll(work, 0o750); err != nil {
		return "", err
	}
	defer func() {
		_ = os.RemoveAll(work)
		s.cleanBackupTemps()
	}()

	if err := gh.EnsureRepo(repo, "VPS Manager room snapshot — "+room.Name); err != nil {
		return "", err
	}
	gitDir := filepath.Join(work, repo)
	if err := gh.CloneOrPull(repo, gitDir); err != nil {
		if err2 := initGitRepo(gitDir, gh, repo); err2 != nil {
			_ = gh.deleteRepo(repo, true)
			return "", err
		}
	}

	fail := func(err error) (string, error) {
		_ = gh.deleteRepo(repo, true)
		s.cleanBackupTemps()
		return "", err
	}

	u := &snapUploader{gh: gh, repo: repo, gitDir: gitDir}
	man := &RoomSnapManifest{
		Format: RoomSnapFormat, RoomID: room.ID, Name: room.Name, Seq: seq, Repo: repo,
		At: time.Now().UTC().Format(time.RFC3339), Fingerprint: fp,
	}

	cts, _ := s.Store.ListContainers(room.ID)
	imgs, _ := s.Store.ListImages(room.ID)
	vols, _ := s.Store.ListVolumes(room.ID)
	man.Room = RoomBackupMeta{
		ID: room.ID, Name: room.Name, Password: room.PassPlain, PassHash: room.PassHash,
		NetworkName: room.NetworkName, QuotaBytes: room.QuotaBytes, Kind: room.Kind,
		Domain: room.Domain, SSL: room.SSL, CreatedAt: room.CreatedAt.UTC().Format(time.RFC3339),
		Containers: cts, Images: imgs, Volumes: vols,
	}

	addBlob := func(key, src string) error {
		if err := s.errIfStopped(); err != nil {
			return err
		}
		b, err := u.putFile(key, src)
		if err != nil {
			return err
		}
		s.report(-1, "↑ %s  %s", key, prettySize(b.Size))
		man.Blobs = append(man.Blobs, *b)
		return nil
	}

	vault := filepath.Join(s.RoomsDir, room.ID, "vault.bin")
	if st, err := os.Stat(vault); err == nil && st.Size() > 0 {
		if err := addBlob("secrets/vault.bin", vault); err != nil {
			return fail(err)
		}
	}
	envPath := filepath.Join(s.RuntimeDir, room.ID, ".env")
	if _, err := os.Stat(envPath); err == nil {
		if err := addBlob("secrets/runtime.env", envPath); err != nil {
			return fail(err)
		}
	}

	if s.Docker != nil && s.Docker.Available() {
		for _, c := range cts {
			ord := c.Ordinal
			if ord <= 0 {
				ord = 1
			}
			raw := []byte{}
			if c.DockerID != "" {
				if out, err := s.Docker.InspectJSON(c.DockerID); err == nil {
					raw = out
				}
			}
			key := fmt.Sprintf("containers/%02d-inspect.json", ord)
			tmp := filepath.Join(work, fmt.Sprintf("c-%02d.json", ord))
			if len(raw) == 0 {
				raw = []byte("{}")
			}
			if err := os.WriteFile(tmp, raw, 0o600); err != nil {
				return fail(err)
			}
			if err := addBlob(key, tmp); err != nil {
				return fail(err)
			}
			_ = os.Remove(tmp)
			man.Containers = append(man.Containers, SnapContainer{Rec: c, InspectKey: key})
		}

		seenImg := map[string]bool{}
		var refs []string
		addRef := func(ref string) {
			ref = strings.TrimSpace(ref)
			if ref == "" || seenImg[ref] {
				return
			}
			seenImg[ref] = true
			refs = append(refs, ref)
		}
		for _, c := range cts {
			addRef(c.Image)
		}
		for _, im := range imgs {
			addRef(im.Ref)
		}
		for i, ref := range refs {
			if err := s.errIfStopped(); err != nil {
				return fail(err)
			}
			s.report(pctRooms(idx, total), "Room %s — image %s", room.Name, ref)
			prefix := fmt.Sprintf("images/%02d/", i+1)
			unpacked := filepath.Join(work, fmt.Sprintf("img-%02d", i+1))
			_ = os.RemoveAll(unpacked)
			if err := s.Docker.ExtractSave(ref, unpacked); err != nil {
				return fail(fmt.Errorf("save image %s: %w", ref, err))
			}
			s.report(-1, "↑ image %s  %s", ref, prettySize(dirBytes(unpacked)))
			si := SnapImage{Ref: ref, DockerID: s.Docker.ImageID(ref), Prefix: prefix}
			err := filepath.Walk(unpacked, func(p string, info os.FileInfo, err error) error {
				if err != nil || info == nil || info.IsDir() {
					return err
				}
				rel, err := filepath.Rel(unpacked, p)
				if err != nil {
					return err
				}
				key := prefix + filepath.ToSlash(rel)
				if err := addBlob(key, p); err != nil {
					return err
				}
				si.Files = append(si.Files, key)
				_ = os.Remove(p)
				return nil
			})
			_ = os.RemoveAll(unpacked)
			if err != nil {
				return fail(err)
			}
			man.Images = append(man.Images, si)
		}

		seenVol := map[string]bool{}
		for _, v := range vols {
			name := strings.TrimSpace(v.DockerName)
			if name == "" {
				name = strings.TrimSpace(v.Name)
			}
			if name == "" || seenVol[name] || strings.HasPrefix(name, "/") {
				continue
			}
			seenVol[name] = true
			if v.DockerName == "" {
				v.DockerName = name
			}
			ord := v.Ordinal
			if ord <= 0 {
				ord = len(man.Volumes) + 1
			}
			s.report(pctRooms(idx, total), "Room %s — volume %s", room.Name, name)
			dest := filepath.Join(work, fmt.Sprintf("vol-%02d.tgz", ord))
			if err := s.archiveVolume(name, dest); err != nil {
				return fail(fmt.Errorf("volume %s: %w", name, err))
			}
			if fi, err := os.Stat(dest); err == nil {
				s.report(-1, "↑ volume %s  %s", name, prettySize(fi.Size()))
			}
			key := fmt.Sprintf("volumes/%02d.tgz", ord)
			if err := addBlob(key, dest); err != nil {
				_ = os.Remove(dest)
				return fail(fmt.Errorf("volume upload %s: %w", name, err))
			}
			_ = os.Remove(dest)
			man.Volumes = append(man.Volumes, SnapVolume{Rec: v, BlobKey: key})
		}
	} else {
		for _, c := range cts {
			man.Containers = append(man.Containers, SnapContainer{Rec: c})
		}
	}

	if err := writeJSON(filepath.Join(gitDir, "FORMAT"), map[string]string{"format": RoomSnapFormat}); err != nil {
		return fail(err)
	}
	if err := writeJSON(filepath.Join(gitDir, "manifest.json"), man); err != nil {
		return fail(err)
	}
	roomJSON, _ := json.MarshalIndent(man.Room, "", "  ")
	_ = os.WriteFile(filepath.Join(gitDir, "room.json"), roomJSON, 0o600)

	s.report(pctRooms(idx, total), "Room %s — push %s", room.Name, repo)
	if err := gh.CommitPush(gitDir, "room snapshot "+room.Name+" #"+fmt.Sprintf("%d", seq)); err != nil {
		return fail(err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	s.saveRoomSnap(room.ID, RoomSnapState{Repo: repo, Seq: seq, At: now, OK: true, Fingerprint: fp})

	if oldRepo != "" && oldRepo != repo && isManagedBackupRepo(oldRepo) {
		s.report(-1, "Room %s — delete previous %s", room.Name, oldRepo)
		if err := gh.deleteRepo(oldRepo, true); err != nil {
			s.report(-1, "kept old repo %s: %v", oldRepo, err)
		}
	}
	s.report(pctRooms(idx+1, total), "Room %s — snapshot ok (%s)", room.Name, repo)
	return repo, nil
}

func (s *Service) roomFingerprint(room store.Room) string {
	h := sha256.New()
	type fpRoom struct {
		ID, Name, Pass, Hash, Net, Kind, Domain string
		Quota                                   int64
		SSL                                     bool
	}
	_ = json.NewEncoder(h).Encode(fpRoom{
		ID: room.ID, Name: room.Name, Pass: room.PassPlain, Hash: room.PassHash,
		Net: room.NetworkName, Kind: room.Kind, Domain: room.Domain, Quota: room.QuotaBytes, SSL: room.SSL,
	})
	if b, err := os.ReadFile(filepath.Join(s.RoomsDir, room.ID, "vault.bin")); err == nil {
		sum := sha256.Sum256(b)
		_, _ = h.Write(sum[:])
	}
	if b, err := os.ReadFile(filepath.Join(s.RuntimeDir, room.ID, ".env")); err == nil {
		sum := sha256.Sum256(b)
		_, _ = h.Write(sum[:])
	}
	cts, _ := s.Store.ListContainers(room.ID)
	imgs, _ := s.Store.ListImages(room.ID)
	vols, _ := s.Store.ListVolumes(room.ID)
	for _, c := range cts {
		_, _ = fmt.Fprintf(h, "c:%s:%s:%s:%d\n", c.ID, c.Name, c.Image, c.Ordinal)
		if s.Docker != nil && c.DockerID != "" {
			if raw, err := s.Docker.InspectJSON(c.DockerID); err == nil {
				raw = stripInspectState(raw)
				sum := sha256.Sum256(raw)
				_, _ = h.Write(sum[:])
			}
			if s.Docker != nil {
				_, _ = fmt.Fprintf(h, "imgid:%s\n", s.Docker.ImageID(c.Image))
			}
		}
	}
	for _, im := range imgs {
		id := ""
		if s.Docker != nil {
			id = s.Docker.ImageID(im.Ref)
		}
		_, _ = fmt.Fprintf(h, "im:%s:%s:%s:%d\n", im.ID, im.Ref, id, im.SizeBytes)
	}
	for _, v := range vols {
		name := v.DockerName
		if name == "" {
			name = v.Name
		}
		sz := v.SizeBytes
		if s.Docker != nil && name != "" {
			sz = s.Docker.VolumeSizeBytes(name)
		}
		_, _ = fmt.Fprintf(h, "v:%s:%s:%d\n", v.ID, name, sz)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func stripInspectState(raw []byte) []byte {
	raw = bytesTrim(raw)
	if len(raw) == 0 {
		return raw
	}
	var m any
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	switch t := m.(type) {
	case []any:
		if len(t) == 0 {
			return raw
		}
		if obj, ok := t[0].(map[string]any); ok {
			delete(obj, "State")
			b, err := json.Marshal(obj)
			if err == nil {
				return b
			}
		}
	case map[string]any:
		delete(t, "State")
		b, err := json.Marshal(t)
		if err == nil {
			return b
		}
	}
	return raw
}

func (s *Service) InspectRemoteRooms(token string) ([]RoomRemote, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		t, _, _ := s.LoadToken()
		token = t
	}
	if token == "" {
		return nil, fmt.Errorf("GitHub PAT required")
	}
	gh := NewGitHub(token)
	u, err := gh.Validate()
	if err != nil {
		return nil, err
	}
	gh.User = u.Login
	names, err := gh.ListOwnerRepoNames()
	if err != nil {
		return nil, err
	}
	var out []RoomRemote
	for _, name := range names {
		if !strings.HasPrefix(strings.ToLower(name), RoomRepoPrefix) {
			continue
		}
		var man RoomSnapManifest
		if err := gh.GetJSON(name, "manifest.json", &man); err != nil {
			continue
		}
		if man.Format != RoomSnapFormat {
			continue
		}
		out = append(out, RoomRemote{
			Name: man.Name, RoomID: man.RoomID, Repo: name, Seq: man.Seq, At: man.At, Kind: man.Room.Kind,
		})
	}
	return out, nil
}

func dirBytes(root string) int64 {
	var n int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}

func prettySize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	f := float64(n)
	u := []string{"KB", "MB", "GB", "TB"}
	for i, name := range u {
		f /= 1024
		if f < 1024 || i == len(u)-1 {
			if f >= 10 {
				return fmt.Sprintf("%.0f %s", f, name)
			}
			return fmt.Sprintf("%.1f %s", f, name)
		}
	}
	return fmt.Sprintf("%d B", n)
}

package projects

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/x5coder/vps-rooms/internal/store"
)

// Only bind dirs the app actually uses. A long default list overlays empty
// host folders on top of image paths and, with docker cp into a mounted
// volume, nests copies until the live DB looks empty.
var defaultPersistDests = []string{
	"/app/data",
}

var persistEnvKeys = []string{
	"OUTPUT_DIR", "DATA_DIR", "DATA_PATH", "STORAGE_DIR", "STORAGE_PATH",
	"LOG_DIR", "UPLOAD_DIR", "MEDIA_DIR", "FILES_DIR",
}

func bindDestination(b string) string {
	_, dest, _ := SplitBind(b)
	return dest
}

func destCovered(binds []string, dest string) bool {
	dest = filepath.Clean(dest)
	for _, b := range binds {
		d := filepath.Clean(bindDestination(b))
		if d == "" || d == "." {
			continue
		}
		if dest == d || strings.HasPrefix(dest, d+"/") {
			return true
		}
	}
	return false
}

func hasBindDest(binds []string, dest string) bool {
	for _, b := range binds {
		if bindDestination(b) == dest {
			return true
		}
	}
	return false
}

func persistSubdir(dest string) string {
	d := strings.Trim(filepath.Clean(dest), "/")
	if d == "" {
		return "data"
	}
	return strings.ReplaceAll(d, "/", "-")
}

func absPersistPath(v string) string {
	v = strings.TrimSpace(v)
	v = strings.Trim(v, `"'`)
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "/") {
		return filepath.Clean(v)
	}
	return filepath.Clean("/app/" + strings.TrimPrefix(v, "./"))
}

func persistDestsFromEnv(envPath string, extra []string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(d string) {
		d = filepath.Clean(strings.TrimSpace(d))
		if d == "" || d == "/" || d == "/app" || d == "/app/.env" {
			return
		}
		if !strings.HasPrefix(d, "/app/") {
			return
		}
		if _, ok := seen[d]; ok {
			return
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	for _, d := range defaultPersistDests {
		add(d)
	}
	for _, d := range extra {
		add(d)
	}
	if envPath != "" {
		pairs, _ := readEnvPairs(envPath)
		want := map[string]struct{}{}
		for _, k := range persistEnvKeys {
			want[k] = struct{}{}
		}
		for _, line := range pairs {
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			if _, ok := want[strings.TrimSpace(k)]; !ok {
				continue
			}
			if p := absPersistPath(v); p != "" {
				add(p)
			}
		}
	}
	return pruneChildDests(out)
}

func pruneChildDests(dests []string) []string {
	var out []string
	for _, d := range dests {
		skip := false
		for _, o := range dests {
			if o == d {
				continue
			}
			oc, dc := filepath.Clean(o), filepath.Clean(d)
			if dc != oc && strings.HasPrefix(dc, oc+"/") {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, d)
		}
	}
	return out
}

func dropLegacyVolumeRootBind(binds []string, volDir string) []string {
	volDir = filepath.Clean(volDir)
	if volDir == "" || volDir == "." {
		return binds
	}
	var out []string
	for _, b := range binds {
		host, dest, _ := SplitBind(b)
		if dest == "/app/data" && filepath.Clean(host) == volDir {
			continue
		}
		out = append(out, b)
	}
	return out
}

func mergePersistentBinds(binds []string, envPath, volDir string, dests []string) []string {
	out := append([]string{}, binds...)
	if envPath != "" {
		if _, err := os.Stat(envPath); err == nil && !hasBindDest(out, "/app/.env") {
			out = append(out, envPath+":/app/.env:ro")
		}
	}
	if volDir == "" {
		return out
	}
	_ = os.MkdirAll(volDir, 0o755)
	out = dropLegacyVolumeRootBind(out, volDir)
	migrateLegacyAppDataRoot(volDir)
	for _, dest := range dests {
		if destCovered(out, dest) {
			continue
		}
		host := filepath.Join(volDir, persistSubdir(dest))
		_ = os.MkdirAll(host, 0o755)
		out = append(out, host+":"+dest)
	}
	return out
}

// migrateLegacyAppDataRoot moves files that were stored at the volume root
// (old vol:/app/data layout) into vol/app-data.
func migrateLegacyAppDataRoot(volDir string) {
	target := filepath.Join(volDir, persistSubdir("/app/data"))
	if st, err := os.Stat(target); err == nil && st.IsDir() {
		if ents, _ := os.ReadDir(target); len(ents) > 0 {
			return
		}
	}
	ents, err := os.ReadDir(volDir)
	if err != nil {
		return
	}
	moved := false
	_ = os.MkdirAll(target, 0o755)
	for _, e := range ents {
		name := e.Name()
		if name == persistSubdir("/app/data") {
			continue
		}
		if strings.HasPrefix(name, "app-") || name == "data" {
			continue
		}
		src := filepath.Join(volDir, name)
		dst := filepath.Join(target, name)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := os.Rename(src, dst); err == nil {
			moved = true
		}
	}
	if !moved {
		_ = os.Remove(target) // leave empty dir only if we created it unused
		_ = os.MkdirAll(target, 0o755)
	}
}

func (s *Service) skipMandatoryVolume(p *store.Project) bool {
	if p == nil || p.ID == "" || s.Rooms == nil {
		return true
	}
	pdir := s.Rooms.ProjectDir(p.RoomID, p.ID)
	m := readMountsMeta(pdir)
	return m.ComposeDir != "" || m.ComposeProject != ""
}

func (s *Service) dataVolumeDir(projectID string) string {
	return filepath.Join(s.volumesDir(), projectID)
}

func (s *Service) persistDests(p *store.Project) []string {
	pdir := s.Rooms.ProjectDir(p.RoomID, p.ID)
	return persistDestsFromEnv(filepath.Join(pdir, ".env"), nil)
}

func (s *Service) ensurePersistentBinds(p *store.Project, pdir, envPath string, binds []string) []string {
	if s.skipMandatoryVolume(p) {
		return binds
	}
	vol := s.dataVolumeDir(p.ID)
	_ = os.MkdirAll(vol, 0o755)
	out := mergePersistentBinds(binds, envPath, vol, persistDestsFromEnv(envPath, nil))
	meta := readMountsMeta(pdir)
	meta.Binds = out
	meta.FilesRoot = vol
	_ = writeMountsMetaFull(pdir, meta)
	return out
}

func (s *Service) bindsForRun(p *store.Project) []string {
	pdir := s.Rooms.ProjectDir(p.RoomID, p.ID)
	envPath := filepath.Join(pdir, ".env")
	return s.ensurePersistentBinds(p, pdir, envPath, s.projectBinds(p, pdir, envPath))
}

func (s *Service) seedDataVolume(p *store.Project) {
	if s.skipMandatoryVolume(p) || s.Docker == nil || p.ContainerID == "" {
		return
	}
	vol := s.dataVolumeDir(p.ID)
	_ = os.MkdirAll(vol, 0o755)
	migrateLegacyAppDataRoot(vol)
	live, _ := s.Docker.InspectBinds(p.ContainerID)
	for _, dest := range s.persistDests(p) {
		if destCovered(live, dest) {
			// Already on a host bind — docker cp into a subdir of that
			// mount nests the volume inside itself and hides the live DB.
			continue
		}
		host := filepath.Join(vol, persistSubdir(dest))
		_ = os.MkdirAll(host, 0o755)
		tmp, err := os.MkdirTemp("", "vr-seed-")
		if err != nil {
			continue
		}
		err = s.Docker.CopyFromContainer(p.ContainerID, dest+"/.", tmp)
		if err == nil {
			_ = copyDirContents(tmp, host)
		}
		_ = os.RemoveAll(tmp)
	}
}

func copyDirContents(src, dest string) error {
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	_ = os.MkdirAll(dest, 0o755)
	for _, e := range ents {
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dest, e.Name())
		if err := os.Rename(from, to); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) liveHasPersistentBinds(p *store.Project) bool {
	if s.Docker == nil || p == nil || p.ContainerID == "" {
		return false
	}
	binds, err := s.Docker.InspectBinds(p.ContainerID)
	if err != nil {
		return false
	}
	vol := s.dataVolumeDir(p.ID)
	if len(dropLegacyVolumeRootBind(binds, vol)) != len(binds) {
		return false
	}
	return destCovered(binds, "/app/data")
}

// AttachMissingDataVolumes updates mounts.json for the next deploy.
// It must not recreate running containers — panel restarts would wipe
// anything still only on the container RW layer.
func (s *Service) AttachMissingDataVolumes() {
	if s == nil || s.Store == nil {
		return
	}
	rooms, err := s.Store.ListRooms()
	if err != nil {
		return
	}
	for _, rm := range rooms {
		projs, err := s.Store.ListProjects(rm.ID)
		if err != nil {
			continue
		}
		for i := range projs {
			p := &projs[i]
			if s.skipMandatoryVolume(p) {
				continue
			}
			_ = s.bindsForRun(p)
		}
	}
}

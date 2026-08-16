package inventory

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/x5coder/vps-rooms/internal/dockerx"
	"github.com/x5coder/vps-rooms/internal/projects"
	"github.com/x5coder/vps-rooms/internal/rooms"
	"github.com/x5coder/vps-rooms/internal/store"
)

// AdoptExisting registers live Docker objects into the v2 tables.
// It never stops, recreates, or deletes containers, images, or volumes.
func AdoptExisting(st *store.Store, docker *dockerx.Client, rs *rooms.Service, runtimeDir string) {
	if st == nil {
		return
	}
	list, err := st.ListRooms()
	if err != nil {
		return
	}
	for _, rm := range list {
		RefreshRoom(st, docker, rs, runtimeDir, rm)
	}
}

// RefreshRoom re-reads Docker for one room and fills containers/images/volumes.
func RefreshRoom(st *store.Store, docker *dockerx.Client, rs *rooms.Service, runtimeDir string, rm store.Room) {
	adoptRoom(st, docker, rs, runtimeDir, rm)
}

func ContainerLabel(name, service, image string) string {
	s := strings.TrimSpace(service)
	if s == "" {
		s = strings.TrimSpace(name)
	}
	if i := strings.LastIndex(s, "."); i >= 0 && i+1 < len(s) {
		s = s[i+1:]
	}
	low := strings.ToLower(s)
	for _, p := range []string{"supabase-", "studix-"} {
		if strings.HasPrefix(low, p) {
			s = s[len(p):]
			break
		}
	}
	if s == "" {
		s = baseName(image)
	}
	if s == "" {
		return "container"
	}
	return s
}

func dockerKeys(id string) []string {
	id = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(id)), "sha256:")
	if id == "" {
		return nil
	}
	out := []string{id}
	if len(id) >= 12 {
		out = append(out, id[:12])
	}
	return out
}

func seenDockerID(seen map[string]bool, id string) bool {
	for _, k := range dockerKeys(id) {
		if seen[k] {
			return true
		}
	}
	return false
}

func markDockerID(seen map[string]bool, id string) {
	for _, k := range dockerKeys(id) {
		seen[k] = true
	}
}

func adoptRoom(st *store.Store, docker *dockerx.Client, rs *rooms.Service, runtimeDir string, rm store.Room) {
	_ = rs.EnsureUnlocked(rm.ID)
	projs, _ := st.ListProjects(rm.ID)
	seenDocker := map[string]bool{}
	seenVol := map[string]bool{}
	seenImg := map[string]bool{}

	cts, _ := st.ListContainers(rm.ID)
	for _, c := range cts {
		markDockerID(seenDocker, c.DockerID)
	}

	for _, p := range projs {
		copyRoomEnv(rs, runtimeDir, rm.ID, p.ID)
		_, composeDir, composeProject, _ := projects.ProjectLayout(rs.ProjectDir(rm.ID, p.ID))
		if docker != nil && composeProject == "" && p.HostPort > 0 {
			gid, gst, _ := docker.BriefByPublish(p.HostPort)
			if gst != "missing" && gid != "" {
				composeProject = docker.ComposeProjectOf(gid)
			}
		}
		if docker != nil && (composeDir != "" || composeProject != "") {
			list, err := docker.ListCompose(composeProject)
			if err == nil && len(list) > 1 {
				_ = st.SetRoomKind(rm.ID, store.KindMulti)
			}
			for _, cc := range list {
				id, status, image := docker.ContainerBrief(cc.Name)
				if id == "" || status == "missing" {
					continue
				}
				markDockerID(seenDocker, id)
				if image == "" {
					image = cc.Image
				}
				cid := stableID(rm.ID, cc.Name)
				rec := store.Container{
					ID: cid, RoomID: rm.ID, Name: cc.Name, Service: cc.Service,
					Image: image, DockerID: id, Status: status, CreatedAt: time.Now().UTC(),
				}
				if cur, _ := st.GetContainer(cid); cur != nil {
					rec.Ordinal = cur.Ordinal
					rec.CreatedAt = cur.CreatedAt
					rec.HostPort = cur.HostPort
					rec.ContainerPort = cur.ContainerPort
				} else {
					for _, old := range cts {
						if !strings.EqualFold(old.Service, cc.Service) && !strings.EqualFold(old.Name, cc.Name) {
							continue
						}
						_, ost, _ := docker.ContainerBrief(old.DockerID)
						if ost == "missing" || strings.EqualFold(old.Name, cc.Name) {
							rec.ID = old.ID
							rec.Ordinal = old.Ordinal
							rec.CreatedAt = old.CreatedAt
							rec.HostPort = old.HostPort
							rec.ContainerPort = old.ContainerPort
							break
						}
					}
				}
				_ = st.UpsertContainer(rec)
				registerImage(st, docker, rm.ID, image, seenImg)
				adoptMounts(st, docker, rm.ID, id, seenVol)
			}
		}
		if docker != nil && p.HostPort > 0 {
			if gid, gst, gimg := docker.BriefByPublish(p.HostPort); gst != "missing" && gid != "" {
				p.ContainerID = gid
				p.Status = gst
				if gimg != "" && !strings.HasPrefix(gimg, "sha256:") {
					p.Image = gimg
				}
				_ = st.UpdateProject(p)
			}
		}
		st.SyncContainerFromProject(p)
		markDockerID(seenDocker, p.ContainerID)
		registerImage(st, docker, rm.ID, p.Image, seenImg)
		if docker != nil && p.ContainerID != "" {
			adoptMounts(st, docker, rm.ID, p.ContainerID, seenVol)
		}
	}

	if docker != nil {
		ids := docker.IDsByFilter("label=vps-rooms.room=" + rm.ID)
		for _, id := range ids {
			if seenDockerID(seenDocker, id) {
				continue
			}
			did, status, image := docker.ContainerBrief(id)
			if did == "" || seenDockerID(seenDocker, did) {
				markDockerID(seenDocker, did)
				continue
			}
			markDockerID(seenDocker, did)
			name := strings.TrimSpace(did)
			if len(name) > 12 {
				name = name[:12]
			}
			_ = st.UpsertContainer(store.Container{
				ID: stableID(rm.ID, did), RoomID: rm.ID, Name: name,
				Image: image, DockerID: did, Status: status, CreatedAt: time.Now().UTC(),
			})
			registerImage(st, docker, rm.ID, image, seenImg)
			adoptMounts(st, docker, rm.ID, did, seenVol)
		}
	}

	dedupRoomContainers(st, rm.ID, projs)
	cts, _ = st.ListContainers(rm.ID)
	for _, c := range cts {
		registerImage(st, docker, rm.ID, c.Image, seenImg)
		if docker != nil && c.DockerID != "" {
			adoptMounts(st, docker, rm.ID, c.DockerID, seenVol)
		}
	}
}

func registerImage(st *store.Store, docker *dockerx.Client, roomID, ref string, seen map[string]bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || seen[ref] {
		return
	}
	seen[ref] = true
	if existing, _ := st.ListImages(roomID); existing != nil {
		for _, im := range existing {
			if im.Ref == ref {
				return
			}
		}
	}
	sz := int64(0)
	if docker != nil {
		sz = docker.ImageSize(ref)
	}
	_ = st.UpsertImage(store.ImageRec{
		ID: stableID(roomID, "img:"+ref), RoomID: roomID,
		Name: baseName(ref), Ref: ref, SizeBytes: sz,
	})
}

func dedupRoomContainers(st *store.Store, roomID string, projs []store.Project) {
	projIDs := map[string]bool{}
	for _, p := range projs {
		projIDs[p.ID] = true
	}
	list, _ := st.ListContainers(roomID)
	keep := map[string]store.Container{}
	for _, c := range list {
		keys := dockerKeys(c.DockerID)
		if len(keys) == 0 {
			continue
		}
		k := keys[len(keys)-1]
		prev, ok := keep[k]
		if !ok {
			keep[k] = c
			continue
		}
		loser := c
		winner := prev
		if projIDs[c.ID] && !projIDs[prev.ID] {
			winner, loser = c, prev
		} else if c.Ordinal < prev.Ordinal && !projIDs[prev.ID] {
			winner, loser = c, prev
		}
		keep[k] = winner
		if loser.ID != winner.ID {
			_ = st.DeleteContainer(loser.ID)
		}
	}
	unique := 0
	for range keep {
		unique++
	}
	if unique > 1 {
		_ = st.SetRoomKind(roomID, store.KindMulti)
	} else {
		_ = st.SetRoomKind(roomID, store.KindSingle)
	}
}

func adoptMounts(st *store.Store, docker *dockerx.Client, roomID, dockerID string, seen map[string]bool) {
	mounts, err := docker.ListMounts(dockerID)
	if err != nil {
		return
	}
	for _, m := range mounts {
		key := strings.TrimSpace(m.Name)
		label := key
		dockerName := key
		if strings.EqualFold(m.Type, "bind") {
			dockerName = strings.TrimSpace(m.Source)
			label = strings.TrimSpace(m.Destination)
			if label == "" {
				label = dockerName
			}
			if i := strings.LastIndex(strings.TrimSuffix(label, "/"), "/"); i >= 0 {
				label = label[i+1:]
			}
			key = "bind:" + dockerName
		} else if !strings.EqualFold(m.Type, "volume") || key == "" {
			continue
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		_ = st.UpsertVolume(store.VolumeRec{
			ID: stableID(roomID, "vol:"+key), RoomID: roomID,
			Name: label, DockerName: dockerName,
		})
	}
}

func copyRoomEnv(rs *rooms.Service, runtimeDir, roomID, projectID string) {
	dest := filepath.Join(runtimeDir, roomID, ".env")
	if st, err := os.Stat(dest); err == nil && st.Size() > 0 {
		return
	}
	src := filepath.Join(rs.ProjectDir(roomID, projectID), ".env")
	b, err := os.ReadFile(src)
	if err != nil || len(b) == 0 {
		return
	}
	_ = os.MkdirAll(filepath.Dir(dest), 0o700)
	_ = os.WriteFile(dest, b, 0o600)
}

func stableID(roomID, key string) string {
	sum := sha1.Sum([]byte(roomID + "|" + key))
	h := hex.EncodeToString(sum[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

func baseName(ref string) string {
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

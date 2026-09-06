package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/x5coder/vps-rooms/internal/projects"
	"github.com/x5coder/vps-rooms/internal/store"
)

func (s *Server) storageInfo() map[string]any {
	m := s.Metrics.Snapshot()
	free := int64(m.DiskFree)
	if free <= 0 && m.DiskTotal > m.DiskUsed {
		free = int64(m.DiskTotal - m.DiskUsed)
	}
	reserved, _ := s.Store.TotalQuotaBytes()
	df := s.cachedSystemDisk()
	return map[string]any{
		"disk_total":              int64(m.DiskTotal),
		"disk_used":               int64(m.DiskUsed),
		"disk_free":               free,
		"quota_reserved":          reserved,
		"quota_available":         free,
		"quota_available_gb":      float64(free) / (1024 * 1024 * 1024),
		"quota_counts":            "files + bind volumes + container writable layer",
		"quota_excludes":          "docker images, build cache, leftover upload temps, OS",
		"docker_images_bytes":     df.Images,
		"docker_images_reclaim":   df.ImagesReclaim,
		"docker_containers_bytes": df.Containers,
		"docker_buildcache_bytes": df.BuildCache,
	}
}

func (s *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	writeJSON(w, 200, s.storageInfo())
}

func (s *Server) handlePorts(w http.ResponseWriter, r *http.Request) {
	if s.requireSession(w, r) == nil {
		return
	}
	writeJSON(w, 200, s.portsPayload())
}

func (s *Server) portsPayload() map[string]any {
	ports := s.cachedPorts()
	seen := map[int]bool{}
	out := make([]int, 0, len(ports)+1)
	for _, p := range ports {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if !seen[9090] {
		out = append(out, 9090)
	}
	return map[string]any{"used_ports": out, "panel_port": 9090}
}

func (s *Server) allocateQuota(quotaGB float64, extraFree int64) (int64, error) {
	if quotaGB <= 0 {
		return 0, fmt.Errorf("quota_gb is required and must be > 0")
	}
	quota := int64(quotaGB * 1024 * 1024 * 1024)
	st := s.storageInfo()
	avail := asInt64(st["quota_available"]) + extraFree
	if quota > avail {
		return 0, fmt.Errorf("quota exceeds available space (%.2f GB free to allocate)", float64(avail)/(1024*1024*1024))
	}
	return quota, nil
}

func (s *Server) roomIsEmpty(id string) bool {
	cts, _ := s.Store.ListContainers(id)
	return len(cts) == 0
}

func asInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case uint64:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func (s *Server) uniqueRoomName(base string) string {
	name := sanitizeRoomName(base)
	if existing, _ := s.Store.GetRoomByName(name); existing == nil {
		return name
	}
	for i := 0; i < 20; i++ {
		cand := name
		if len(cand) > 34 {
			cand = cand[:34]
		}
		cand = cand + "-" + time.Now().Format("150405")
		if existing, _ := s.Store.GetRoomByName(cand); existing == nil {
			return cand
		}
		time.Sleep(10 * time.Millisecond)
	}
	return name + "-" + randomPass(4)
}

func sanitizeRoomName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else if r == ' ' || r == '/' || r == ':' {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if len(out) < 2 {
		out = "room-" + time.Now().Format("150405")
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func (s *Server) resolveRoomProject(id string) (*store.Room, *store.Project, error) {
	if room, err := s.Store.GetRoom(id); err == nil && room != nil {
		projs, _ := s.Store.ListProjects(room.ID)
		var p *store.Project
		if len(projs) > 0 {
			pp := projs[0]
			p = &pp
		}
		return room, p, nil
	}
	p, err := s.Store.GetProject(id)
	if err != nil || p == nil {
		return nil, nil, fmt.Errorf("not found")
	}
	room, err := s.Store.GetRoom(p.RoomID)
	if err != nil || room == nil {
		return nil, nil, fmt.Errorf("not found")
	}
	return room, p, nil
}

func (s *Server) projectView(room *store.Room, p *store.Project) map[string]any {
	st := "empty"
	quotaGB := float64(room.QuotaBytes) / (1024 * 1024 * 1024)
	usage := s.cachedDisk(room.ID)
	usageGB := float64(usage.Usage) / (1024 * 1024 * 1024)
	out := map[string]any{
		"id": room.ID, "room_id": room.ID, "name": room.Name,
		"quota_bytes": room.QuotaBytes, "quota_gb": quotaGB,
		"usage_bytes": usage.Usage, "usage_gb": usageGB,
		"volume_bytes": usage.Volumes, "image_bytes": usage.Images, "footprint_bytes": usage.Footprint,
		"password_set": room.PassPlain != "",
		"created_at":   room.CreatedAt,
		"status":       st,
		"kind":         room.Kind,
		"domain":       room.Domain,
		"ssl":          room.SSL,
	}
	cts := s.roomContainersJSON(room.ID)
	if room.Kind == "" {
		out["kind"] = store.KindSingle
	}
	if len(cts) > 1 {
		out["kind"] = store.KindMulti
	}
	// A compose stack / Docker repo with containers (no project rows) is NOT empty.
	if st == "empty" && len(cts) > 0 {
		st = "stopped"
		for _, c := range cts {
			if c["status"] == "running" {
				st = "running"
				break
			}
		}
		out["status"] = st
	}
	out["deployment_type"] = out["kind"]
	out["containers"] = cts
	out["images"] = s.roomImagesJSON(room.ID)
	out["volumes"] = s.roomVolumesJSON(room.ID)
	out["container_count"] = len(cts)
	out["image_count"] = len(s.roomImagesJSON(room.ID))
	out["volume_count"] = len(s.roomVolumesJSON(room.ID))
	hist := s.Projects.ReadUpdateHistory(room.ID)
	if hist == nil {
		hist = []projects.UpdateEvent{}
	}
	count := 0
	if len(hist) > 0 {
		count = hist[0].N
	}
	out["updates"] = hist
	out["update_count"] = count
	if busy := s.jobKind(room.ID); busy != "" {
		out["status"] = "deploying"
		out["job"] = busy
	}
	if p != nil {
		busy := s.jobKind(p.ID)
		st = s.cachedStatus(p.ID)
		if st == "" {
			st = p.Status
		}
		if busy == "build" {
			st = "building"
		} else if busy != "" {
			st = "deploying"
		}
		out["project_id"] = p.ID
		out["project_name"] = p.Name
		out["image"] = p.Image
		out["host_port"] = p.HostPort
		out["container_port"] = p.ContainerPort
		out["domain"] = p.Domain
		out["domain_enabled"] = p.DomainEnabled
		out["ssl_status"] = p.SSLStatus
		out["external_url"] = p.ExternalURL
		out["status"] = st
		out["container_id"] = p.ContainerID
		meta := s.Projects.ReadDeployMeta(room.ID, p.ID)
		out["image_digest"] = meta.ImageDigest
		out["last_deploy_at"] = meta.LastDeployAt
		out["last_deploy_ok"] = meta.LastDeployOK
		out["last_deploy_error"] = meta.LastDeployError
		if busy == "build" {
			out["status"] = "building"
			out["job"] = "build"
		} else if busy == "deploy" {
			out["status"] = "deploying"
			out["job"] = "deploy"
		} else {
			if s.Docker != nil && strings.TrimSpace(p.ContainerID) != "" {
				if live, err := s.Docker.InspectStatus(p.ContainerID); err == nil && live != "" {
					st = live
				}
			}
			if st == "exited" || st == "restarting" || st == "dead" {
				st = "error"
			}
			staleMeta := meta.Status == "deploying" || meta.Status == "building"
			staleProj := p.Status == "deploying" || p.Status == "building"
			if (staleMeta || staleProj) && st != "deploying" && st != "building" && st != "" {
				s.Projects.ClearStaleDeploy(room.ID, p.ID, st)
			}
			out["status"] = st
			if meta.Status == "error" && st != "running" {
				out["status"] = "error"
			}
		}
		if env, err := s.Projects.ReadEnv(p.ID); err == nil {
			out["env"] = maskEnvText(env)
		}
	}
	return out
}

func (s *Server) apiDoRedeploy(p *store.Project, image string, pull, recreate bool) error {
	if p == nil {
		return fmt.Errorf("project has no container")
	}
	room, err := s.Store.GetRoom(p.RoomID)
	if err != nil || room == nil {
		return fmt.Errorf("room not found")
	}
	if room.QuotaBytes > 0 {
		gb := float64(room.QuotaBytes) / (1024 * 1024 * 1024)
		if _, err := s.allocateQuota(gb, room.QuotaBytes); err != nil {
			return err
		}
	}
	_ = pull
	return s.Projects.RedeployImage(projects.RedeployInput{
		ID: p.ID, Image: image, Pull: pull, Recreate: recreate, Log: io.Discard,
	})
}

func maskEnvText(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			out = append(out, line)
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			out = append(out, line)
			continue
		}
		k = strings.TrimSpace(k)
		if strings.TrimSpace(v) == "" {
			out = append(out, k+"=set")
		} else {
			out = append(out, k+"=***")
		}
	}
	return strings.Join(out, "\n")
}


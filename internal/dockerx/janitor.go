package dockerx

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Safe disk janitor: NEVER removes containers, images in use, volumes,
// projects, DB data, or .env files. Only reclaims:
//   - dangling images (untagged, unreferenced)
//   - superseded manager-built tags (vpsrooms/*, vps-ci) no container uses
//   - build cache older than keepUnused (default 24h)
//   - oversized container json logs via truncate (container keeps running)
//   - stale /tmp upload leftovers is handled by api.SweepStaleUploads
//   - host journal + apt .deb cache via helpers below (also safe)
//
// All steps are best-effort and log-and-continue so one failure never
// blocks the rest.

// PruneBuilderCacheFiltered drops build cache not used recently.
// Safe: does not touch images, containers, or volumes.
func (c *Client) PruneBuilderCacheFiltered(keepUnused time.Duration) {
	if c == nil || !c.Available() {
		return
	}
	if keepUnused < time.Hour {
		keepUnused = 24 * time.Hour
	}
	hours := int(keepUnused.Hours())
	if hours < 1 {
		hours = 24
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// "until" filter keeps recently used cache so an active build is safe.
	cmd := exec.CommandContext(ctx, c.bin, "builder", "prune", "-af",
		"--filter", "until="+strconv.Itoa(hours)+"h")
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("janitor: builder prune: %v %s", err, strings.TrimSpace(string(out)))
	}
}

// TruncateBigContainerLogs truncates *-json.log files larger than maxBytes.
// Uses truncate -s 0 equivalent via O_TRUNC so the container keeps its open
// fd and keeps running; only disk blocks are freed. Never deletes files.
func (c *Client) TruncateBigContainerLogs(maxBytes int64) (freed int64, count int) {
	if maxBytes <= 0 {
		maxBytes = 200 << 20 // 200MB
	}
	// Docker root is almost always /var/lib/docker, but resolve via info
	// when possible so custom --data-root installs still work.
	root := "/var/lib/docker/containers"
	if c != nil && c.Available() {
		if out, err := c.outputTimeout(5*time.Second, "info", "--format", "{{.DockerRootDir}}"); err == nil {
			if r := strings.TrimSpace(out); r != "" {
				root = filepath.Join(r, "containers")
			}
		}
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return 0, 0
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(root, e.Name(), "*-json.log"))
		for _, p := range matches {
			st, err := os.Stat(p)
			if err != nil || st.Size() <= maxBytes {
				continue
			}
			// Open with O_TRUNC: safe on a live log (docker keeps writing).
			f, err := os.OpenFile(p, os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				continue
			}
			_ = f.Close()
			freed += st.Size()
			count++
			log.Printf("janitor: truncated %s (%dMB)", p, st.Size()>>20)
		}
	}
	return freed, count
}

// VacuumHostJournal caps systemd journal size. Safe: only drops old journal
// entries, keeps the most recent 200MB by default.
func VacuumHostJournal(keep string) {
	if strings.TrimSpace(keep) == "" {
		keep = "200M"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "journalctl", "--vacuum-size="+keep).CombinedOutput(); err != nil {
		// journald may be absent (e.g. container dev); ignore quietly.
		_ = out
	}
}

// AptClean drops cached .deb files (/var/cache/apt/archives). Safe: never
// removes installed packages, only the download cache.
func AptClean() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_ = exec.CommandContext(ctx, "apt-get", "clean").Run()
}

// RunSafeJanitor executes every safe reclamation step. It never deletes
// containers, images referenced by any container (running or stopped),
// volumes, or project source. Returns human-readable summary for logs.
func (c *Client) RunSafeJanitor() string {
	var parts []string
	if c != nil && c.Available() {
		c.PruneDanglingImages()
		parts = append(parts, "dangling-pruned")
		// Sweep superseded manager-built tags (vpsrooms/*, vps-ci) that no
		// container references — this is what stops deploy leftovers from
		// piling up between deploys.
		c.PruneUnusedLocalImages()
		parts = append(parts, "local-tags-pruned")
		c.PruneBuilderCacheFiltered(24 * time.Hour)
		parts = append(parts, "builder-pruned")
		if freed, n := c.TruncateBigContainerLogs(200 << 20); n > 0 {
			parts = append(parts, strconv.Itoa(n)+" logs truncated ("+strconv.Itoa(int(freed>>20))+"MB)")
		} else {
			parts = append(parts, "logs-ok")
		}
	} else {
		parts = append(parts, "docker-unavailable")
	}
	VacuumHostJournal("200M")
	parts = append(parts, "journal-vacuumed")
	AptClean()
	parts = append(parts, "apt-cleaned")
	return strings.Join(parts, ", ")
}

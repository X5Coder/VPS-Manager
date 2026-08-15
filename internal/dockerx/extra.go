package dockerx

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (c *Client) Restart(id string) error {
	if err := c.Stop(id); err != nil {
		return err
	}
	return c.Start(id)
}

func (c *Client) CreateNamedVolume(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("volume name required")
	}
	return c.run(context.Background(), nil, "volume", "create", name)
}

func (c *Client) RemoveNamedVolume(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("volume name required")
	}
	return c.run(context.Background(), nil, "volume", "rm", name)
}

func (c *Client) CleanVolume(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("volume name required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, "run", "--rm",
		"-v", name+":/vol",
		"alpine:3.20", "sh", "-lc", `rm -rf /vol/..?* /vol/.[!.]* /vol/* 2>/dev/null; true`)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("clean volume: %s", strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *Client) VolumeSizeBytes(name string) int64 {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, "run", "--rm",
		"-v", name+":/vol:ro",
		"alpine:3.20", "sh", "-lc", `du -sb /vol 2>/dev/null | awk '{print $1}'`)
	b, err := cmd.Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

func (c *Client) VolumeUsers(name string) []map[string]string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	out, err := c.output("ps", "-a", "--filter", "volume="+name, "--format", "{{.ID}}\t{{.Names}}\t{{.Label \"com.docker.compose.service\"}}")
	if err != nil {
		return nil
	}
	var list []map[string]string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		row := map[string]string{"docker_id": parts[0]}
		if len(parts) > 1 {
			row["name"] = parts[1]
		}
		if len(parts) > 2 {
			row["service"] = parts[2]
		}
		list = append(list, row)
	}
	return list
}

func (c *Client) TruncateLogs(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("missing container")
	}
	out, err := c.output("inspect", "--format", "{{.LogPath}}", id)
	if err != nil {
		return err
	}
	path := strings.TrimSpace(out)
	if path == "" || path == "<no value>" {
		return fmt.Errorf("no log path")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

func (c *Client) FollowLogs(ctx context.Context, id string, tail int, w io.Writer) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("missing container")
	}
	if tail <= 0 {
		tail = 100
	}
	cmd := exec.CommandContext(ctx, c.bin, "logs", "-f", "--tail", strconv.Itoa(tail), id)
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

func (c *Client) ExecSplit(ctx context.Context, id, command string) (stdout, stderr string, code int, err error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", "", 1, fmt.Errorf("missing container")
	}
	cmd := exec.CommandContext(ctx, c.bin, "exec", id, "sh", "-lc", command)
	var outb, errb strings.Builder
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err = cmd.Run()
	stdout, stderr = outb.String(), errb.String()
	code = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
			err = nil
		} else {
			code = 1
		}
	}
	return stdout, stderr, code, err
}

func (c *Client) InspectImageJSON(ref string) ([]byte, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("missing image")
	}
	return c.outputBytes("image", "inspect", ref)
}

func (c *Client) outputBytes(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, args...)
	return cmd.Output()
}

func (c *Client) ComposeCmd(ctx context.Context, dir, project string, args ...string) ([]byte, error) {
	file := ComposeFile(dir)
	if file == "" {
		return nil, fmt.Errorf("no compose file")
	}
	all := []string{"compose", "-f", file}
	over := filepath.Join(dir, "compose.vps-override.yml")
	if st, err := os.Stat(over); err == nil && !st.IsDir() {
		all = append(all, "-f", over)
	}
	if strings.TrimSpace(project) != "" {
		all = append(all, "-p", project)
	}
	all = append(all, args...)
	cmd := exec.CommandContext(ctx, c.bin, all...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func (c *Client) ParseStats(id string) (cpuPct, memPct float64, memUsed, memLimit int64) {
	m, err := c.StatsJSON(id)
	if err != nil || m == nil {
		return 0, 0, 0, 0
	}
	cpuPct = parsePercent(fmt.Sprint(m["CPUPerc"]))
	memPct = parsePercent(fmt.Sprint(m["MemPerc"]))
	memUsed, memLimit = parseMemUsage(fmt.Sprint(m["MemUsage"]))
	return
}

func parsePercent(s string) float64 {
	s = strings.TrimSpace(strings.TrimSuffix(s, "%"))
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func parseMemUsage(s string) (used, limit int64) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, "/")
	if len(parts) == 0 {
		return 0, 0
	}
	used = parseSizeToBytes(parts[0])
	if len(parts) > 1 {
		limit = parseSizeToBytes(parts[1])
	}
	return used, limit
}

func parseSizeToBytes(s string) int64 {
	s = strings.TrimSpace(strings.ToUpper(s))
	mult := float64(1)
	switch {
	case strings.HasSuffix(s, "KIB"):
		mult = 1024
		s = strings.TrimSuffix(s, "KIB")
	case strings.HasSuffix(s, "MIB"):
		mult = 1024 * 1024
		s = strings.TrimSuffix(s, "MIB")
	case strings.HasSuffix(s, "GIB"):
		mult = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GIB")
	case strings.HasSuffix(s, "TIB"):
		mult = 1024 * 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "TIB")
	case strings.HasSuffix(s, "KB"):
		mult = 1000
		s = strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "MB"):
		mult = 1000 * 1000
		s = strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "GB"):
		mult = 1000 * 1000 * 1000
		s = strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "B"):
		s = strings.TrimSuffix(s, "B")
	}
	s = strings.TrimSpace(s)
	f, _ := strconv.ParseFloat(s, 64)
	return int64(f * mult)
}

func DrainCmd(r io.Reader, w io.Writer) {
	br := bufio.NewReader(r)
	buf := make([]byte, 4096)
	for {
		n, err := br.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if f, ok := w.(interface{ Flush() error }); ok {
				_ = f.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

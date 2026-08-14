package dockerx

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	bin string
}

func New() (*Client, error) {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return nil, err
	}
	c := &Client{bin: bin}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.run(ctx, nil, "version", "--format", "{{.Server.Version}}"); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) Close() error { return nil }

func (c *Client) Available() bool { return c != nil && c.bin != "" }

func (c *Client) run(ctx context.Context, w io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, c.bin, args...)
	if w != nil {
		cmd.Stdout = w
		cmd.Stderr = w
	} else {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
	}
	return cmd.Run()
}

func (c *Client) output(args ...string) (string, error) {
	return c.outputTimeout(1500*time.Millisecond, args...)
}

func (c *Client) outputTimeout(d time.Duration, args ...string) (string, error) {
	if d <= 0 {
		d = 1500 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, args...)
	b, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(b), fmt.Errorf("docker %s timed out", args[0])
	}
	return string(b), err
}

func (c *Client) EnsureNetwork(name string) error {
	out, _ := c.output("network", "ls", "--format", "{{.Name}}")
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == name {
			return nil
		}
	}
	return c.run(context.Background(), nil, "network", "create", "--label", "vps-rooms=1", name)
}

func (c *Client) RemoveNetwork(name string) error {
	_ = c.run(context.Background(), nil, "network", "rm", name)
	return nil
}

func (c *Client) PullImage(ref string, w io.Writer) error {
	return c.run(context.Background(), w, "pull", ref)
}

// RegistryPullable reports images a new VPS can docker-pull identically
// (Docker Hub / ghcr / quay / *.io). Local tags like vpsrooms/* are false.
func RegistryPullable(image string) bool {
	image = strings.TrimSpace(image)
	if image == "" {
		return false
	}
	lower := strings.ToLower(image)
	switch {
	case strings.HasPrefix(lower, "vpsrooms/"),
		strings.HasPrefix(lower, "vpsrooms-bak/"),
		strings.HasPrefix(lower, "vpsrooms-restore/"),
		strings.HasPrefix(lower, "localhost/"),
		strings.Contains(lower, "127.0.0.1"):
		return false
	}
	return true
}

func (c *Client) ImageExists(ref string) bool {
	out, err := c.output("image", "inspect", "--format", "{{.Id}}", ref)
	return err == nil && strings.TrimSpace(out) != ""
}

// RepoDigest returns registry digest (name@sha256:…) when Docker recorded one.
func (c *Client) RepoDigest(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	out, err := c.output("image", "inspect", "--format", "{{range .RepoDigests}}{{.}} {{end}}", ref)
	if err != nil {
		return ""
	}
	want := strings.Split(ref, ":")[0]
	for _, d := range strings.Fields(out) {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if want == "" || strings.Contains(d, want) || strings.Contains(d, "@sha256:") {
			return d
		}
	}
	return ""
}

func (c *Client) BuildImage(ctx context.Context, contextDir, tag string, w io.Writer) error {
	return c.Build(ctx, BuildOpts{Tag: tag, Context: contextDir}, w)
}

type BuildOpts struct {
	Tag        string
	Context    string
	Dockerfile string
	Args       map[string]string
}

func (c *Client) Build(ctx context.Context, opts BuildOpts, w io.Writer) error {
	args := []string{"build", "-t", opts.Tag}
	df := strings.TrimSpace(opts.Dockerfile)
	if df != "" {
		if !filepath.IsAbs(df) && opts.Context != "" && !strings.Contains(opts.Context, "://") {
			df = filepath.Join(opts.Context, df)
		}
		args = append(args, "-f", df)
	}
	keys := make([]string, 0, len(opts.Args))
	for k := range opts.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--build-arg", k+"="+opts.Args[k])
	}
	ctxDir := opts.Context
	if ctxDir == "" {
		ctxDir = "."
	}
	args = append(args, ctxDir)
	cmd := exec.CommandContext(ctx, c.bin, args...)
	var buf bytes.Buffer
	if w != nil && w != io.Discard {
		mw := io.MultiWriter(w, &buf)
		cmd.Stdout = mw
		cmd.Stderr = mw
	} else {
		cmd.Stdout = &buf
		cmd.Stderr = &buf
	}
	if err := cmd.Run(); err != nil {
		tail := strings.TrimSpace(buf.String())
		if tail == "" {
			return err
		}
		if len(tail) > 4000 {
			tail = "…" + tail[len(tail)-4000:]
		}
		return fmt.Errorf("%w: %s", err, tail)
	}
	return nil
}

func (c *Client) PushImage(ref string, w io.Writer) error {
	return c.run(ctxOrBg(), w, "push", ref)
}

func ctxOrBg() context.Context {
	return context.Background()
}

func (c *Client) Rename(id, name string) error {
	if id == "" || name == "" {
		return fmt.Errorf("rename requires id and name")
	}
	return c.run(context.Background(), nil, "rename", id, name)
}

func (c *Client) ImageID(ref string) string {
	out, err := c.output("image", "inspect", "--format", "{{.Id}}", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

type RunOpts struct {
	Name          string
	Image         string
	Network       string
	HostIP        string // optional, e.g. 127.0.0.1
	HostPort      int
	ContainerPort int
	Env           []string
	Binds         []string
	Labels        map[string]string
	StorageBytes  int64 // container writable-layer cap when the driver supports it
}

func storageOpt(n int64) string {
	if n <= 0 {
		return ""
	}
	mb := (n + (1 << 20) - 1) >> 20
	if mb < 64 {
		mb = 64
	}
	return fmt.Sprintf("size=%dM", mb)
}

func (c *Client) runContainer(opts RunOpts, withSize bool) (string, error) {
	args := []string{"run", "-d", "--name", opts.Name, "--label", "vps-rooms=1", "--restart", "unless-stopped", "--shm-size=1g"}
	if withSize {
		if opt := storageOpt(opts.StorageBytes); opt != "" {
			args = append(args, "--storage-opt", opt)
		}
	}
	if opts.Network != "" {
		args = append(args, "--network", opts.Network)
	}
	if opts.HostPort > 0 && opts.ContainerPort > 0 {
		pub := fmt.Sprintf("%d:%d", opts.HostPort, opts.ContainerPort)
		if opts.HostIP != "" {
			pub = opts.HostIP + ":" + pub
		}
		args = append(args, "-p", pub)
	}
	for _, e := range opts.Env {
		args = append(args, "-e", e)
	}
	for _, b := range opts.Binds {
		args = append(args, "-v", b)
	}
	for k, v := range opts.Labels {
		args = append(args, "--label", k+"="+v)
	}
	args = append(args, opts.Image)
	cmd := exec.Command(c.bin, args...)
	out, err := cmd.CombinedOutput()
	id := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("%s: %w", id, err)
	}
	return id, nil
}

func (c *Client) Run(opts RunOpts) (string, error) {
	if opts.StorageBytes > 0 {
		id, err := c.runContainer(opts, true)
		if err == nil {
			return id, nil
		}
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "storage-opt") || strings.Contains(low, "overlay") || strings.Contains(low, "unknown") {
			return c.runContainer(opts, false)
		}
		return "", err
	}
	return c.runContainer(opts, false)
}

// SizeRw is the container writable layer in bytes (0 if unknown).
func (c *Client) SizeRw(id string) int64 {
	if id == "" {
		return 0
	}
	out, err := c.output("inspect", "-s", "--format", "{{.SizeRw}}", id)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if n < 0 {
		return 0
	}
	return n
}

func (c *Client) Start(id string) error {
	return c.run(context.Background(), nil, "start", id)
}

func (c *Client) Stop(id string) error {
	return c.run(context.Background(), nil, "stop", "-t", "2", id)
}

func (c *Client) Remove(id string, force bool) error {
	args := []string{"rm", "-f"}
	if !force {
		args = []string{"rm"}
	}
	args = append(args, id)
	return c.run(context.Background(), nil, args...)
}

func (c *Client) InspectStatus(id string) (string, error) {
	if id == "" {
		return "missing", nil
	}
	out, err := c.output("inspect", "-f", "{{.State.Running}}", id)
	if err != nil {
		return "missing", nil
	}
	if strings.TrimSpace(out) == "true" {
		return "running", nil
	}
	return "stopped", nil
}

// InspectBinds returns HostConfig.Binds for a container (host:dest[:mode]).
func (c *Client) InspectBinds(id string) ([]string, error) {
	if id == "" {
		return nil, fmt.Errorf("missing container")
	}
	out, err := c.output("inspect", "-f", "{{json .HostConfig.Binds}}", id)
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" || out == "null" {
		return nil, nil
	}
	var binds []string
	if err := json.Unmarshal([]byte(out), &binds); err != nil {
		return nil, err
	}
	return binds, nil
}

func (c *Client) InspectEnv(id string) ([]string, error) {
	if id == "" {
		return nil, fmt.Errorf("missing container")
	}
	out, err := c.output("inspect", "-f", "{{json .Config.Env}}", id)
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" || out == "null" {
		return nil, nil
	}
	var env []string
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		return nil, err
	}
	return env, nil
}

func (c *Client) Logs(id string, tail int) (string, error) {
	if id == "" {
		return "", fmt.Errorf("missing container")
	}
	out, err := c.output("logs", "--tail", strconv.Itoa(tail), id)
	return out, err
}

func (c *Client) UsedPorts() ([]int, error) {
	out, err := c.outputTimeout(8*time.Second, "ps", "-a", "--format", "{{.Ports}}")
	if err != nil {
		return nil, err
	}
	used := map[int]bool{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		for _, part := range strings.Split(line, ", ") {
			if i := strings.Index(part, "->"); i > 0 {
				left := part[:i]
				if j := strings.LastIndex(left, ":"); j >= 0 {
					p, _ := strconv.Atoi(left[j+1:])
					if p > 0 {
						used[p] = true
					}
				}
			}
		}
	}
	ports := make([]int, 0, len(used))
	for p := range used {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	return ports, nil
}

func (c *Client) FindFreePort(start int) (int, error) {
	ports, err := c.UsedPorts()
	if err != nil {
		return 0, err
	}
	used := map[int]bool{}
	for _, p := range ports {
		used[p] = true
	}
	for p := start; p < start+5000; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free port")
}

// StatsJSON returns a minimal JSON blob for a container if needed later.
func (c *Client) StatsJSON(id string) (map[string]any, error) {
	cmd := exec.Command(c.bin, "stats", "--no-stream", "--format", "{{json .}}", id)
	b, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(b), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func HasDockerSock() bool {
	_, err := os.Stat("/var/run/docker.sock")
	return err == nil
}

type MountInfo struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

func (c *Client) ListMounts(id string) ([]MountInfo, error) {
	if id == "" {
		return nil, nil
	}
	out, err := c.output("inspect", "-f", "{{json .Mounts}}", id)
	if err != nil {
		return nil, err
	}
	var mounts []MountInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &mounts); err != nil {
		return nil, err
	}
	return mounts, nil
}

// ExportFilesystem writes a full container filesystem tar (includes in-container DBs/users).
func (c *Client) ExportFilesystem(id, destTar string) error {
	if id == "" {
		return fmt.Errorf("missing container")
	}
	if err := os.MkdirAll(filepath.Dir(destTar), 0o750); err != nil {
		return err
	}
	cmd := exec.Command(c.bin, "export", id, "-o", destTar)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker export: %s: %w", strings.TrimSpace(string(b)), err)
	}
	return nil
}

// SaveImage runs docker save -o destTar imageTag.
func (c *Client) SaveImage(imageTag, destTar string) error {
	if strings.TrimSpace(imageTag) == "" {
		return fmt.Errorf("missing image")
	}
	if err := os.MkdirAll(filepath.Dir(destTar), 0o750); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	out, err := os.Create(destTar)
	if err != nil {
		return err
	}
	defer out.Close()
	save := exec.CommandContext(ctx, c.bin, "save", imageTag)
	gzBin, gzArgs := "gzip", []string{"-1"}
	if _, err := exec.LookPath("pigz"); err == nil {
		gzBin, gzArgs = "pigz", []string{"-1"}
	}
	gz := exec.CommandContext(ctx, gzBin, gzArgs...)
	pipe, err := save.StdoutPipe()
	if err != nil {
		return err
	}
	gz.Stdin = pipe
	gz.Stdout = out
	var errSave, errGz bytes.Buffer
	save.Stderr = &errSave
	gz.Stderr = &errGz
	if err := gz.Start(); err != nil {
		return err
	}
	if err := save.Start(); err != nil {
		_ = gz.Process.Kill()
		return err
	}
	saveErr := save.Wait()
	_ = pipe.Close()
	gzErr := gz.Wait()
	if saveErr != nil {
		return fmt.Errorf("docker save: %s: %w", strings.TrimSpace(errSave.String()), saveErr)
	}
	if gzErr != nil {
		return fmt.Errorf("compress image: %s: %w", strings.TrimSpace(errGz.String()), gzErr)
	}
	st, _ := os.Stat(destTar)
	if st == nil || st.Size() < 32 {
		return fmt.Errorf("docker save produced empty archive")
	}
	return nil
}

// SaveCommittedImage commits the container (keeps entrypoint/CMD + writable layer) then docker-saves it.
func (c *Client) SaveCommittedImage(id, imageTag, destTar string) error {
	if id == "" {
		return fmt.Errorf("missing container")
	}
	if err := os.MkdirAll(filepath.Dir(destTar), 0o750); err != nil {
		return err
	}
	cmd := exec.Command(c.bin, "commit", id, imageTag)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker commit: %s: %w", strings.TrimSpace(string(b)), err)
	}
	cmd = exec.Command(c.bin, "save", "-o", destTar, imageTag)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker save: %s: %w", strings.TrimSpace(string(b)), err)
	}
	return nil
}

func (c *Client) LoadImage(srcTar string) error {
	_, err := c.LoadImageTag(srcTar)
	return err
}

// LoadImageTag loads a docker save tar and returns a repo:tag when docker prints one.
func (c *Client) LoadImageTag(srcTar string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, "load", "-i", srcTar)
	b, err := cmd.CombinedOutput()
	out := strings.TrimSpace(string(b))
	if err != nil {
		return "", fmt.Errorf("docker load: %s: %w", out, err)
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if tag, ok := strings.CutPrefix(line, "Loaded image: "); ok {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				return tag, nil
			}
		}
	}
	return "", nil
}

// ImportFilesystem creates an image from a docker-export tar.
func (c *Client) ImportFilesystem(srcTar, image string) error {
	cmd := exec.Command(c.bin, "import", srcTar, image)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker import: %s: %w", strings.TrimSpace(string(b)), err)
	}
	return nil
}

// CopyVolumeToDir copies a named Docker volume into destDir using a helper container.
func (c *Client) CopyVolumeToDir(volume, destDir string) error {
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return err
	}
	// alpine helper: mount volume and bind dest
	args := []string{"run", "--rm",
		"-v", volume + ":/from:ro",
		"-v", destDir + ":/to",
		"alpine:3.20", "sh", "-c", "cp -a /from/. /to/"}
	cmd := exec.Command(c.bin, args...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("copy volume %s: %s: %w", volume, strings.TrimSpace(string(b)), err)
	}
	return nil
}

// CopyDirToVolume copies destDir contents into a named Docker volume.
func (c *Client) CopyDirToVolume(srcDir, volume string) error {
	_ = c.run(context.Background(), nil, "volume", "create", volume)
	args := []string{"run", "--rm",
		"-v", volume + ":/to",
		"-v", srcDir + ":/from:ro",
		"alpine:3.20", "sh", "-c", "rm -rf /to/* /to/.[!.]* 2>/dev/null; cp -a /from/. /to/"}
	cmd := exec.Command(c.bin, args...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restore volume %s: %s: %w", volume, strings.TrimSpace(string(b)), err)
	}
	return nil
}

func (c *Client) RemoveByName(name string) error {
	if name == "" {
		return nil
	}
	return c.run(context.Background(), nil, "rm", "-f", name)
}

type ComposeCtr struct {
	Name    string
	Image   string
	Service string
}

func ComposeFile(dir string) string {
	for _, n := range []string{"compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml"} {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// ComposePullUp downloads images then starts the stack.
func (c *Client) ComposePullUp(dir, project string, w io.Writer) error {
	return c.ComposeUp(dir, project, true, w)
}

// ComposeUp starts a compose stack. When pull is false, uses images already
// loaded on this VPS (from backup tars) and does not contact a registry.
func (c *Client) ComposeUp(dir, project string, pull bool, w io.Writer) error {
	file := ComposeFile(dir)
	if file == "" {
		return fmt.Errorf("no compose file in %s", dir)
	}
	base := []string{"compose", "-f", file}
	if strings.TrimSpace(project) != "" {
		base = append(base, "-p", project)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if pull {
		pullArgs := append(append([]string{}, base...), "pull")
		if err := c.run(ctx, w, pullArgs...); err != nil && w != nil {
			fmt.Fprintf(w, "compose pull: %v (will still try up)\n", err)
		}
	}
	up := append(append([]string{}, base...), "up", "-d", "--remove-orphans")
	if !pull {
		up = append(up, "--pull", "never")
	}
	return c.run(ctx, w, up...)
}

func (c *Client) ListCompose(project string) ([]ComposeCtr, error) {
	if project == "" {
		return nil, nil
	}
	out, err := c.output("ps", "-a",
		"--filter", "label=com.docker.compose.project="+project,
		"--format", "{{.Names}}\t{{.Image}}\t{{.Label \"com.docker.compose.service\"}}")
	if err != nil {
		return nil, err
	}
	var list []ComposeCtr
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		ct := ComposeCtr{Name: parts[0]}
		if len(parts) > 1 {
			ct.Image = parts[1]
		}
		if len(parts) > 2 {
			ct.Service = parts[2]
		}
		list = append(list, ct)
	}
	return list, nil
}

// DumpPostgresGzip streams pg_dumpall from a running container into dest (.sql.gz).
func (c *Client) DumpPostgresGzip(container, user, dest string) error {
	if container == "" {
		return fmt.Errorf("missing container")
	}
	if user == "" {
		user = "postgres"
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	_ = os.Remove(dest)
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	try := func(args []string) error {
		dump := exec.Command(c.bin, args...)
		gz := exec.Command("gzip", "-c")
		pipe, err := dump.StdoutPipe()
		if err != nil {
			return err
		}
		var errDump, errGz bytes.Buffer
		dump.Stderr = &errDump
		gz.Stdin = pipe
		gz.Stdout = out
		gz.Stderr = &errGz
		if err := dump.Start(); err != nil {
			return err
		}
		if err := gz.Start(); err != nil {
			_ = dump.Process.Kill()
			return err
		}
		waitDump := dump.Wait()
		waitGz := gz.Wait()
		if waitDump != nil {
			msg := strings.TrimSpace(errDump.String())
			if msg == "" {
				msg = waitDump.Error()
			}
			return fmt.Errorf("pg_dumpall: %s", msg)
		}
		if waitGz != nil {
			return fmt.Errorf("gzip: %s: %w", strings.TrimSpace(errGz.String()), waitGz)
		}
		return nil
	}

	// Prefer explicit binary paths used by official Postgres / Supabase images.
	attempts := [][]string{
		{"exec", "-u", user, container, "pg_dumpall", "-U", user, "--clean", "--if-exists"},
		{"exec", container, "pg_dumpall", "-U", user, "--clean", "--if-exists"},
		{"exec", container, "bash", "-lc", "command -v pg_dumpall >/dev/null && pg_dumpall -U " + user + " --clean --if-exists || /usr/lib/postgresql/*/bin/pg_dumpall -U " + user + " --clean --if-exists"},
	}
	var last error
	for _, args := range attempts {
		_ = out.Truncate(0)
		_, _ = out.Seek(0, 0)
		if err := try(args); err != nil {
			last = err
			continue
		}
		st, _ := out.Stat()
		if st != nil && st.Size() > 32 {
			return nil
		}
		last = fmt.Errorf("empty postgres dump")
	}
	_ = os.Remove(dest)
	if last != nil {
		return last
	}
	return fmt.Errorf("empty postgres dump")
}

// RestorePostgresGzip loads a .sql.gz dump into a running postgres container.
func (c *Client) RestorePostgresGzip(container, user, src string) error {
	if container == "" || src == "" {
		return fmt.Errorf("missing restore args")
	}
	if user == "" {
		user = "postgres"
	}
	unz := exec.Command("gzip", "-dc", src)
	psql := exec.Command(c.bin, "exec", "-i", container, "psql", "-U", user, "-v", "ON_ERROR_STOP=0")
	pipe, err := unz.StdoutPipe()
	if err != nil {
		return err
	}
	unz.Stderr = io.Discard
	psql.Stdin = pipe
	var errBuf bytes.Buffer
	psql.Stdout = io.Discard
	psql.Stderr = &errBuf
	if err := unz.Start(); err != nil {
		return err
	}
	if err := psql.Start(); err != nil {
		_ = unz.Process.Kill()
		return err
	}
	_ = unz.Wait()
	if err := psql.Wait(); err != nil {
		return fmt.Errorf("psql restore: %s: %w", strings.TrimSpace(errBuf.String()), err)
	}
	return nil
}

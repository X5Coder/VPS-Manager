package dockerx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type FSEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}

func CleanContainerPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = path.Clean(p)
	if p == "." || p == "" {
		return "/"
	}
	return p
}

func (c *Client) execOut(timeout time.Duration, id string, script string) ([]byte, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("missing container")
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, "exec", id, "sh", "-lc", script)
	b, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return b, fmt.Errorf("docker exec timed out")
	}
	return b, err
}

func (c *Client) ListContainerFiles(id, dir string) ([]FSEntry, error) {
	dir = CleanContainerPath(dir)
	script := fmt.Sprintf(`d=%s
if [ -d "$d" ]; then
  echo DIR
  cd "$d" || exit 1
  ls -1A 2>/dev/null | while IFS= read -r n; do
    if [ -d "$n" ]; then printf 'd\t0\t%%s\n' "$n"
    else sz=$(wc -c < "$n" 2>/dev/null || echo 0); printf 'f\t%%s\t%%s\n' "$sz" "$n"; fi
  done
elif [ -f "$d" ]; then echo FILE
else echo MISSING; exit 2
fi`, shellQuote(dir))
	b, err := c.execOut(20*time.Second, id, script)
	if err != nil && len(b) == 0 {
		return nil, fmt.Errorf("list files: %s", strings.TrimSpace(string(b))+" "+err.Error())
	}
	text := strings.TrimSpace(string(b))
	if strings.HasPrefix(text, "FILE") {
		return nil, fmt.Errorf("not a directory")
	}
	if strings.HasPrefix(text, "MISSING") {
		return nil, fmt.Errorf("not found")
	}
	var out []FSEntry
	for _, line := range strings.Split(text, "\n") {
		if line == "DIR" {
			continue
		}
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		sz, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		out = append(out, FSEntry{Name: parts[2], Dir: parts[0] == "d", Size: sz})
	}
	return out, nil
}

func (c *Client) ReadContainerFile(id, file string) ([]byte, error) {
	file = CleanContainerPath(file)
	script := fmt.Sprintf(`f=%s; if [ ! -f "$f" ]; then echo 'not a file' >&2; exit 2; fi; wc -c < "$f"; echo '---'; cat "$f"`, shellQuote(file))
	b, err := c.execOut(30*time.Second, id, script)
	if err != nil {
		return nil, fmt.Errorf("read: %s", strings.TrimSpace(string(b)))
	}
	raw := string(b)
	i := strings.Index(raw, "---\n")
	if i < 0 {
		i = strings.Index(raw, "---\r\n")
	}
	if i < 0 {
		return b, nil
	}
	return []byte(raw[i+4:]), nil
}

func (c *Client) WriteContainerFile(id, file, content string) error {
	file = CleanContainerPath(file)
	dir := path.Dir(file)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	script := fmt.Sprintf(`mkdir -p %s && cat > %s`, shellQuote(dir), shellQuote(file))
	cmd := exec.CommandContext(ctx, c.bin, "exec", "-i", id, "sh", "-lc", script)
	cmd.Stdin = strings.NewReader(content)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write: %s", strings.TrimSpace(errb.String()))
	}
	return nil
}

func (c *Client) RemoveContainerFile(id, file string) error {
	file = CleanContainerPath(file)
	if file == "/" {
		return fmt.Errorf("refusing to delete /")
	}
	b, err := c.execOut(15*time.Second, id, "rm -rf -- "+shellQuote(file))
	if err != nil {
		return fmt.Errorf("delete: %s", strings.TrimSpace(string(b)))
	}
	return nil
}

func LooksText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return false
	}
	return utf8.Valid(b)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func (c *Client) SaveImagePlain(imageTag, destTar string) error {
	if strings.TrimSpace(imageTag) == "" {
		return fmt.Errorf("missing image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, "save", "-o", destTar, imageTag)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker save: %s: %w", strings.TrimSpace(string(b)), err)
	}
	return nil
}

// ExtractSave streams `docker save` into dest so the image is not stored twice on disk.
func (c *Client) ExtractSave(imageTag, dest string) error {
	if strings.TrimSpace(imageTag) == "" {
		return fmt.Errorf("missing image")
	}
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	save := exec.CommandContext(ctx, c.bin, "save", imageTag)
	untar := exec.CommandContext(ctx, "tar", "-C", dest, "-x")
	pr, pw := io.Pipe()
	save.Stdout = pw
	untar.Stdin = pr
	saveErr := make(chan error, 1)
	go func() {
		err := save.Run()
		_ = pw.Close()
		saveErr <- err
	}()
	out, err := untar.CombinedOutput()
	se := <-saveErr
	if se != nil {
		return fmt.Errorf("docker save: %w", se)
	}
	if err != nil {
		return fmt.Errorf("untar image: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// LoadImageDir streams a docker-save directory into `docker load` without a second tar on disk.
func (c *Client) LoadImageDir(dir string) error {
	st, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("image tree is not a directory: %s", dir)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()
	pack := exec.CommandContext(ctx, "tar", "-C", dir, "-c", ".")
	load := exec.CommandContext(ctx, c.bin, "load")
	pr, pw := io.Pipe()
	pack.Stdout = pw
	var packErr bytes.Buffer
	pack.Stderr = &packErr
	load.Stdin = pr
	var loadOut bytes.Buffer
	load.Stdout = &loadOut
	load.Stderr = &loadOut
	loadErr := make(chan error, 1)
	go func() {
		err := load.Run()
		_ = pr.Close()
		loadErr <- err
	}()
	err = pack.Run()
	_ = pw.Close()
	le := <-loadErr
	if err != nil {
		return fmt.Errorf("tar image tree: %s: %w", strings.TrimSpace(packErr.String()), err)
	}
	if le != nil {
		return fmt.Errorf("docker load: %s: %w", strings.TrimSpace(loadOut.String()), le)
	}
	return nil
}

func (c *Client) ComposeUpService(dir, project, service string, w io.Writer) error {
	file := ComposeFile(dir)
	if file == "" {
		return fmt.Errorf("no compose file in %s", dir)
	}
	if strings.TrimSpace(service) == "" {
		return fmt.Errorf("missing service")
	}
	args := []string{"compose", "-f", file}
	if strings.TrimSpace(project) != "" {
		args = append(args, "-p", project)
	}
	args = append(args, "up", "-d", "--no-deps", "--pull", "never", service)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	return c.run(ctx, w, args...)
}

func (c *Client) RecreateWithImage(id, newImage, network string) (string, error) {
	id = strings.TrimSpace(id)
	newImage = strings.TrimSpace(newImage)
	if id == "" || newImage == "" {
		return "", fmt.Errorf("container and image required")
	}
	nameOut, err := c.outputTimeout(8*time.Second, "inspect", "-f", "{{.Name}}", id)
	if err != nil {
		return "", err
	}
	name := strings.TrimPrefix(strings.TrimSpace(nameOut), "/")
	if name == "" {
		name = id[:12]
	}
	binds, _ := c.InspectBinds(id)
	env, _ := c.InspectEnv(id)
	if network == "" {
		netOut, _ := c.outputTimeout(8*time.Second, "inspect", "-f", "{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}", id)
		network = strings.TrimSpace(netOut)
	}
	roomLbl, _ := c.outputTimeout(8*time.Second, "inspect", "-f", `{{index .Config.Labels "vps-rooms.room"}}`, id)
	projLbl, _ := c.outputTimeout(8*time.Second, "inspect", "-f", `{{index .Config.Labels "vps-rooms.project"}}`, id)
	labels := map[string]string{}
	if strings.TrimSpace(roomLbl) != "" {
		labels["vps-rooms.room"] = strings.TrimSpace(roomLbl)
	}
	if strings.TrimSpace(projLbl) != "" {
		labels["vps-rooms.project"] = strings.TrimSpace(projLbl)
	}
	prev := name + "-prev"
	_ = c.Stop(id)
	_ = c.RemoveByName(prev)
	if err := c.Rename(id, prev); err != nil {
		_ = c.Remove(id, true)
		prev = ""
	}
	_ = c.RemoveByName(name)
	newID, err := c.Run(RunOpts{
		Name: name, Image: newImage, Network: network, Env: env, Binds: binds, Labels: labels,
	})
	if err != nil {
		if prev != "" {
			_ = c.RemoveByName(name)
			_ = c.Rename(prev, name)
			_ = c.Start(name)
		}
		return "", err
	}
	if prev != "" {
		_ = c.Remove(prev, true)
	}
	return newID, nil
}

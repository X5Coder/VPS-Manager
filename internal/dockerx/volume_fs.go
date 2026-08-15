package dockerx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (c *Client) volumeSh(timeout time.Duration, volume, script string) ([]byte, error) {
	volume = strings.TrimSpace(volume)
	if volume == "" {
		return nil, fmt.Errorf("missing volume")
	}
	if timeout <= 0 {
		timeout = 25 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, "run", "--rm",
		"-v", volume+":/vol:ro",
		"alpine:3.20", "sh", "-lc", script)
	b, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return b, fmt.Errorf("volume browse timed out")
	}
	return b, err
}

func parseFSListing(text string) ([]FSEntry, error) {
	text = strings.TrimSpace(text)
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

func listScript(dir string) string {
	return fmt.Sprintf(`d=%s
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
}

func (c *Client) ListVolumeFiles(volume, dir string) ([]FSEntry, error) {
	dir = CleanContainerPath(dir)
	inside := "/vol"
	if dir != "/" {
		inside = "/vol" + dir
	}
	b, err := c.volumeSh(25*time.Second, volume, listScript(inside))
	if err != nil && len(b) == 0 {
		return nil, fmt.Errorf("list volume: %s", strings.TrimSpace(string(b))+" "+err.Error())
	}
	ents, e2 := parseFSListing(string(b))
	if e2 != nil {
		return nil, e2
	}
	return ents, nil
}

func (c *Client) ReadVolumeFile(volume, file string) ([]byte, error) {
	file = CleanContainerPath(file)
	inside := "/vol" + file
	script := fmt.Sprintf(`f=%s; if [ ! -f "$f" ]; then echo 'not a file' >&2; exit 2; fi; wc -c < "$f"; echo '---'; cat "$f"`, shellQuote(inside))
	b, err := c.volumeSh(30*time.Second, volume, script)
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

func ListHostFiles(root, rel string) ([]FSEntry, error) {
	rel = CleanContainerPath(rel)
	p := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(rel, "/")))
	st, err := os.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("not found")
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("not a directory")
	}
	ents, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := make([]FSEntry, 0, len(ents))
	for _, e := range ents {
		info, _ := e.Info()
		sz := int64(0)
		if info != nil && !e.IsDir() {
			sz = info.Size()
		}
		out = append(out, FSEntry{Name: e.Name(), Dir: e.IsDir(), Size: sz})
	}
	return out, nil
}

func ReadHostFile(root, rel string) ([]byte, error) {
	rel = CleanContainerPath(rel)
	p := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(rel, "/")))
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return nil, fmt.Errorf("not a file")
	}
	return os.ReadFile(p)
}

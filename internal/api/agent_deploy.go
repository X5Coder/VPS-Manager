package api

// Agent source-archive helpers for deploy_project / update_project.
//
// The agent delivers the project source as a base64-encoded ZIP (or tar.gz /
// tar) inside the JSON input. It is decoded to a temp file, format-detected
// by magic bytes, and extracted with ZipSlip guards and size caps. Nothing
// outside the destination directory is ever written.

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/x5coder/vps-rooms/internal/dockerx"
)

const (
	agentMaxArchiveBytes = 48 << 20
	agentMaxFiles        = 50000
)

// agentDecodeSource decodes the tool "source" input into a temp archive file.
// Accepts raw base64 (std or URL-safe) with an optional data: URI prefix.
// Returns the temp path and the detected format ("zip", "targz", "tar").
func agentDecodeSource(source string) (path, format string, err error) {
	source = strings.TrimSpace(source)
	if strings.HasPrefix(source, "data:") {
		if i := strings.IndexByte(source, ','); i >= 0 {
			source = source[i+1:]
		} else {
			return "", "", fmt.Errorf("invalid data URI source")
		}
	}
	raw, err := base64.StdEncoding.DecodeString(source)
	if err != nil {
		if raw2, err2 := base64.RawStdEncoding.DecodeString(source); err2 == nil {
			raw = raw2
			err = nil
		} else if raw3, err3 := base64.URLEncoding.DecodeString(strings.TrimSpace(source)); err3 == nil {
			raw = raw3
			err = nil
		} else if raw4, err4 := base64.RawURLEncoding.DecodeString(strings.TrimSpace(source)); err4 == nil {
			raw = raw4
			err = nil
		}
	}
	if err != nil {
		return "", "", fmt.Errorf("source must be base64-encoded ZIP or tar.gz")
	}
	if len(raw) > agentMaxArchiveBytes {
		return "", "", fmt.Errorf("source too large (max %d MB decoded)", agentMaxArchiveBytes>>20)
	}
	format, err = agentSniffBytes(raw)
	if err != nil {
		return "", "", err
	}
	tmp, err := os.CreateTemp("", "agent-src-*."+format)
	if err != nil {
		return "", "", err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = os.Remove(tmp.Name())
		_ = tmp.Close()
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", "", err
	}
	return tmp.Name(), format, nil
}

// agentSniffBytes detects "zip", "targz", or "tar" from magic bytes.
func agentSniffBytes(raw []byte) (string, error) {
	switch {
	case len(raw) >= 4 && raw[0] == 'P' && raw[1] == 'K':
		return "zip", nil
	case len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b:
		return "targz", nil
	case len(raw) > 261 && string(raw[257:262]) == "ustar":
		return "tar", nil
	default:
		return "", fmt.Errorf("unsupported archive (need ZIP or tar.gz)")
	}
}

// agentSniffFile detects the archive format of a file on disk.
func agentSniffFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	return agentSniffBytes(head[:n])
}

type agentExtractStats struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// agentExtractArchive extracts a decoded archive file into dest.
func agentExtractArchive(archivePath, format, dest string) (agentExtractStats, error) {
	var st agentExtractStats
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return st, err
	}
	switch format {
	case "zip":
		return agentUnzip(archivePath, dest)
	default: // targz | tar
		return agentUntar(archivePath, format == "targz", dest)
	}
}

func agentSafeJoin(dest, name string) (string, error) {
	name = filepath.Clean("/" + strings.TrimSpace(name))
	if name == "/" {
		return "", fmt.Errorf("empty entry name")
	}
	target := filepath.Join(dest, name)
	rel, err := filepath.Rel(dest, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry escapes destination")
	}
	return target, nil
}

func agentUnzip(archivePath, dest string) (agentExtractStats, error) {
	var st agentExtractStats
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return st, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if st.Files >= agentMaxFiles {
			return st, fmt.Errorf("archive has too many files")
		}
		if agentSkipEntry(f.Name) {
			continue
		}
		target, err := agentSafeJoin(dest, f.Name)
		if err != nil {
			return st, err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o750); err != nil {
				return st, err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return st, err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			_ = rc.Close()
			return st, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if err != nil {
			_ = rc.Close()
			return st, err
		}
		n, err := io.CopyN(out, rc, agentMaxArchiveBytes-st.Bytes+1)
		_ = rc.Close()
		_ = out.Close()
		if err != nil && err != io.EOF {
			return st, err
		}
		st.Bytes += n
		if st.Bytes > agentMaxArchiveBytes {
			return st, fmt.Errorf("archive content too large")
		}
		st.Files++
	}
	return st, nil
}

func agentUntar(archivePath string, gzipped bool, dest string) (agentExtractStats, error) {
	var st agentExtractStats
	f, err := os.Open(archivePath)
	if err != nil {
		return st, err
	}
	defer f.Close()
	var r io.Reader = f
	if gzipped {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return st, err
		}
		defer gz.Close()
		r = gz
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return st, nil
		}
		if err != nil {
			return st, err
		}
		if st.Files >= agentMaxFiles {
			return st, fmt.Errorf("archive has too many files")
		}
		if agentSkipEntry(hdr.Name) {
			continue
		}
		target, err := agentSafeJoin(dest, hdr.Name)
		if err != nil {
			return st, err
		}
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0o750); err != nil {
				return st, err
			}
			continue
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return st, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if err != nil {
			return st, err
		}
		n, err := io.CopyN(out, tr, agentMaxArchiveBytes-st.Bytes+1)
		_ = out.Close()
		if err != nil && err != io.EOF {
			return st, err
		}
		st.Bytes += n
		if st.Bytes > agentMaxArchiveBytes {
			return st, fmt.Errorf("archive content too large")
		}
		st.Files++
	}
}

var agentPortMapRe = regexp.MustCompile(`(?m)^\s*-\s*["']?(\d+)\s*:\s*(\d+)["']?\s*$`)

// agentComposePorts extracts host:container port mappings from a compose file.
func agentComposePorts(composeFile string) []map[string]any {
	out := []map[string]any{}
	if composeFile == "" {
		return out
	}
	b, err := os.ReadFile(composeFile)
	if err != nil {
		return out
	}
	seen := map[string]struct{}{}
	for _, m := range agentPortMapRe.FindAllStringSubmatch(string(b), -1) {
		key := m[1] + ":" + m[2]
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, map[string]any{"host_port": m[1], "container_port": m[2]})
	}
	return out
}

// agentComposeFile locates docker-compose.yml (or equivalent) under dir.
func agentComposeFile(dir string) string {
	return dockerx.ComposeFile(dir)
}

// agentSkipEntry reports version-control and macOS metadata entries that must
// never land in the stored source.
func agentSkipEntry(name string) bool {
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == ".git" || strings.HasPrefix(part, "__MACOSX") {
			return true
		}
	}
	return false
}

// agentUnwrapSingleDir flattens archives whose payload sits under one
// top-level folder (e.g. SHOP BOT/...). Returns true when unwrapped.
func agentUnwrapSingleDir(dest string) (bool, error) {
	ents, err := os.ReadDir(dest)
	if err != nil {
		return false, err
	}
	if len(ents) != 1 || !ents[0].IsDir() {
		return false, nil
	}
	top := filepath.Join(dest, ents[0].Name())
	kids, err := os.ReadDir(top)
	if err != nil {
		return false, err
	}
	for _, k := range kids {
		if err := os.Rename(filepath.Join(top, k.Name()), filepath.Join(dest, k.Name())); err != nil {
			return false, err
		}
	}
	_ = os.Remove(top)
	return true, nil
}

// agentParseEnvFile parses KEY=VALUE lines in order (strips surrounding
// quotes, skips comments/blank lines).
func agentParseEnvFile(text string) [][2]string {
	out := [][2]string{}
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		t = strings.TrimSpace(strings.TrimPrefix(t, "export "))
		k, v, ok := strings.Cut(t, "=")
		k = strings.TrimSpace(k)
		if !ok || !agentEnvKeyRe.MatchString(k) {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		out = append(out, [2]string{k, v})
	}
	return out
}

// agentApplyExampleEnv merges environment variables into the Room .env: a
// shipped .env contributes missing keys with its values first, then
// .env.example (or .env.sample) contributes still-missing keys with EMPTY
// values. Existing room values are never overwritten. Returns names added
// from each source.
func agentApplyExampleEnv(envPath, root string) (addedExample, addedReal []string, err error) {
	existing := map[string]bool{}
	if b, rerr := os.ReadFile(envPath); rerr == nil {
		for _, kv := range agentParseEnvFile(string(b)) {
			existing[kv[0]] = true
		}
	} else if !os.IsNotExist(rerr) {
		return nil, nil, rerr
	}
	merge := map[string]string{}
	add := func(file string, empty bool) {
		b, rerr := os.ReadFile(filepath.Join(root, file))
		if rerr != nil {
			return
		}
		for _, kv := range agentParseEnvFile(string(b)) {
			if existing[kv[0]] {
				continue
			}
			existing[kv[0]] = true
			if empty {
				merge[kv[0]] = ""
				addedExample = append(addedExample, kv[0])
			} else {
				merge[kv[0]] = kv[1]
				addedReal = append(addedReal, kv[0])
			}
		}
	}
	add(".env", false)
	add(".env.example", true)
	add(".env.sample", true)
	if len(merge) == 0 {
		return addedExample, addedReal, nil
	}
	text, err := agentMergeEnvFile(envPath, merge)
	if err != nil {
		return nil, nil, err
	}
	_ = os.MkdirAll(filepath.Dir(envPath), 0o700)
	if err := os.WriteFile(envPath, []byte(text), 0o600); err != nil {
		return nil, nil, err
	}
	return addedExample, addedReal, nil
}

// agentEnsureDockerfile generates a Dockerfile when the source has none,
// detecting Node.js (package.json) or Python (requirements.txt) stacks.
// Never overwrites an existing Dockerfile.
func agentEnsureDockerfile(dest string, port int) (bool, error) {
	for _, n := range []string{"Dockerfile", "dockerfile"} {
		if st, err := os.Stat(filepath.Join(dest, n)); err == nil && !st.IsDir() {
			return false, nil
		}
	}
	if port <= 0 {
		port = 80
	}
	if st, err := os.Stat(filepath.Join(dest, "package.json")); err == nil && !st.IsDir() {
		main := agentNodeMain(dest)
		content := "FROM node:20-alpine\nWORKDIR /app\nCOPY package*.json ./\n" +
			"RUN npm install --omit=dev\nCOPY . .\nEXPOSE " + strconv.Itoa(port) + "\n" +
			"CMD [\"node\", \"" + main + "\"]\n"
		if err := os.WriteFile(filepath.Join(dest, "Dockerfile"), []byte(content), 0o640); err != nil {
			return false, err
		}
		return true, nil
	}
	if st, err := os.Stat(filepath.Join(dest, "requirements.txt")); err == nil && !st.IsDir() {
		entry := "app.py"
		for _, cand := range []string{"app.py", "main.py", "bot.py"} {
			if st, err := os.Stat(filepath.Join(dest, cand)); err == nil && !st.IsDir() {
				entry = cand
				break
			}
		}
		content := "FROM python:3.12-slim\nWORKDIR /app\nCOPY requirements.txt ./\n" +
			"RUN pip install --no-cache-dir -r requirements.txt\nCOPY . .\nEXPOSE " +
			strconv.Itoa(port) + "\nCMD [\"python\", \"" + entry + "\"]\n"
		if err := os.WriteFile(filepath.Join(dest, "Dockerfile"), []byte(content), 0o640); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, fmt.Errorf("no Dockerfile found and stack not detected (need a Dockerfile, package.json, or requirements.txt)")
}

// agentNodeMain picks the Node entry file from package.json (main, a
// node start script, or a conventional filename).
func agentNodeMain(dest string) string {
	b, err := os.ReadFile(filepath.Join(dest, "package.json"))
	if err == nil {
		var pkg struct {
			Main    string            `json:"main"`
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(b, &pkg) == nil {
			if pkg.Main != "" {
				return filepath.Base(pkg.Main)
			}
			if start, ok := pkg.Scripts["start"]; ok {
				for _, tok := range strings.Fields(start) {
					tok = strings.Trim(tok, `"'`);
					if strings.HasSuffix(tok, ".js") {
						return filepath.Base(tok)
					}
				}
			}
		}
	}
	for _, cand := range []string{"index.js", "server.js", "app.js", "main.js", "bot.js"} {
		if st, err := os.Stat(filepath.Join(dest, cand)); err == nil && !st.IsDir() {
			return cand
		}
	}
	return "index.js"
}

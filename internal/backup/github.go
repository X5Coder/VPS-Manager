package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type GitHub struct {
	Token  string
	User   string
	Client *http.Client
	Ctx    context.Context
}

var gitDirMu sync.Map // abs dir → *sync.Mutex

func lockGitDir(dir string) func() {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	v, _ := gitDirMu.LoadOrStore(abs, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

type GHUser struct {
	Login string `json:"login"`
}

func NewGitHub(token string) *GitHub {
	return &GitHub{Token: strings.TrimSpace(token), Client: &http.Client{Timeout: 60 * time.Second}}
}

func (g *GitHub) Validate() (*GHUser, error) {
	if g.Token == "" {
		return nil, fmt.Errorf("GitHub Personal Access Token (classic) is required")
	}
	req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	g.auth(req)
	res, err := g.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("invalid GitHub token (%d): %s", res.StatusCode, truncate(string(body), 200))
	}
	var u GHUser
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, err
	}
	if u.Login == "" {
		return nil, fmt.Errorf("could not read GitHub user")
	}
	scopes := res.Header.Get("X-OAuth-Scopes")
	if err := g.checkBackupScopes(scopes); err != nil {
		return nil, err
	}
	g.User = u.Login
	return &u, nil
}

func (g *GitHub) checkBackupScopes(scopes string) error {
	sc := strings.ToLower(strings.TrimSpace(scopes))
	if sc == "" {
		return fmt.Errorf("this token has no classic scopes. Create a classic PAT with the repo scope (GitHub → Settings → Developer settings → Personal access tokens → Tokens (classic)). Fine-grained tokens are not accepted")
	}
	if !strings.Contains(sc, "repo") {
		return fmt.Errorf("permissions are not enough (scopes: %s). Backup needs a classic PAT with repo so it can create private repos and push files", scopes)
	}
	req, _ := http.NewRequest("GET", "https://api.github.com/user/repos?per_page=1&affiliation=owner", nil)
	g.auth(req)
	res, err := g.Client.Do(req)
	if err != nil {
		return fmt.Errorf("could not test repository access: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("token cannot list repositories (%d): %s", res.StatusCode, truncate(string(body), 180))
	}
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("repository permission check failed (%d): %s", res.StatusCode, truncate(string(body), 180))
	}
	return nil
}

func (g *GitHub) auth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "vps-manage-backup")
}

func (g *GitHub) EnsureRepo(name, description string) error {
	// try get
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+g.User+"/"+name, nil)
	g.auth(req)
	res, err := g.Client.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode == 200 {
		return nil
	}
	payload := map[string]any{
		"name": name, "private": true, "description": description,
		"auto_init": true,
	}
	b, _ := json.Marshal(payload)
	req, _ = http.NewRequest("POST", "https://api.github.com/user/repos", bytes.NewReader(b))
	g.auth(req)
	req.Header.Set("Content-Type", "application/json")
	res, err = g.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return fmt.Errorf("create repo %s: %s", name, truncate(string(body), 300))
	}
	return nil
}

func (g *GitHub) DeleteRepo(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == IndexRepo || name == SystemRepo || sharedBackupRepo(name) {
		return nil
	}
	if g.User == "" {
		return fmt.Errorf("github user missing")
	}
	req, _ := http.NewRequest("DELETE", "https://api.github.com/repos/"+g.User+"/"+name, nil)
	g.auth(req)
	res, err := g.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == 204 || res.StatusCode == 404 {
		return nil
	}
	body, _ := io.ReadAll(res.Body)
	return fmt.Errorf("delete repo %s (%d): %s", name, res.StatusCode, truncate(string(body), 200))
}

func (g *GitHub) ctx() context.Context {
	if g != nil && g.Ctx != nil {
		return g.Ctx
	}
	return context.Background()
}

func (g *GitHub) originURL(repo string) string {
	if g.Token == "" || g.User == "" || repo == "" {
		return ""
	}
	return fmt.Sprintf("https://x-access-token:%s@github.com/%s/%s.git", g.Token, g.User, repo)
}

func (g *GitHub) CloneOrPull(repo, dir string) error {
	unlock := lockGitDir(dir)
	defer unlock()
	url := g.originURL(repo)
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if url != "" {
			_, _ = gitRun(g.ctx(), 30*time.Second, env, "git", "-C", dir, "remote", "set-url", "origin", url)
		}
		_, _ = gitRun(g.ctx(), 10*time.Minute, env, "git", "-C", dir, "fetch", "--prune", "origin")
		if _, err := gitRun(g.ctx(), 5*time.Minute, env, "git", "-C", dir, "pull", "--rebase", "origin", "main"); err != nil {
			if _, err2 := gitRun(g.ctx(), 5*time.Minute, env, "git", "-C", dir, "pull", "--rebase", "origin", "HEAD"); err2 != nil {
				_, _ = gitRun(g.ctx(), 2*time.Minute, env, "git", "-C", dir, "reset", "--hard", "origin/main")
				_, _ = gitRun(g.ctx(), 2*time.Minute, env, "git", "-C", dir, "reset", "--hard", "origin/HEAD")
			}
		}
		return nil
	}
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return err
	}
	if url == "" {
		return fmt.Errorf("git clone %s: missing origin url", repo)
	}
	out, err := gitRun(g.ctx(), 10*time.Minute, env, "git", "clone", "--depth", "1", "--branch", "main", url, dir)
	if err != nil {
		out, err = gitRun(g.ctx(), 10*time.Minute, env, "git", "clone", "--depth", "1", url, dir)
	}
	if err != nil {
		return fmt.Errorf("git clone %s: %s", repo, truncate(string(out)+" "+err.Error(), 300))
	}
	return nil
}

func (g *GitHub) CommitPush(dir, message string) error {
	unlock := lockGitDir(dir)
	defer unlock()
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	ctx := g.ctx()
	_, _ = gitRun(ctx, 30*time.Second, env, "git", "-C", dir, "config", "http.postBuffer", "524288000")
	_, _ = gitRun(ctx, 30*time.Second, env, "git", "-C", dir, "config", "http.version", "HTTP/1.1")
	_, _ = gitRun(ctx, 30*time.Second, env, "git", "-C", dir, "config", "core.compression", "0")
	if _, err := gitRun(ctx, 30*time.Second, env, "git", "-C", dir, "config", "user.email", "backup@vps-manage.local"); err != nil {
		return err
	}
	if _, err := gitRun(ctx, 30*time.Second, env, "git", "-C", dir, "config", "user.name", "VPS MANAGE Backup"); err != nil {
		return err
	}
	if url := g.originURL(filepath.Base(dir)); url != "" {
		_, _ = gitRun(ctx, 30*time.Second, env, "git", "-C", dir, "remote", "set-url", "origin", url)
	}
	if out, err := gitRun(ctx, 45*time.Minute, env, "git", "-C", dir, "add", "-A"); err != nil {
		return fmt.Errorf("git add: %s", truncate(string(out)+" "+err.Error(), 200))
	}
	st, _ := gitRun(ctx, 30*time.Second, env, "git", "-C", dir, "status", "--porcelain")
	if len(bytes.TrimSpace(st)) == 0 {
		return nil
	}
	if out, err := gitRun(ctx, 2*time.Minute, env, "git", "-C", dir, "commit", "-m", message); err != nil {
		return fmt.Errorf("commit: %s", truncate(string(out)+" "+err.Error(), 200))
	}
	var last []byte
	var lastErr error
	for attempt := 1; attempt <= 8; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("push cancelled")
		}
		_, _ = gitRun(ctx, 10*time.Minute, env, "git", "-C", dir, "fetch", "--prune", "origin")
		if _, err := gitRun(ctx, 5*time.Minute, env, "git", "-C", dir, "rebase", "origin/main"); err != nil {
			if _, err2 := gitRun(ctx, 5*time.Minute, env, "git", "-C", dir, "rebase", "origin/HEAD"); err2 != nil {
				_, _ = gitRun(ctx, 30*time.Second, env, "git", "-C", dir, "rebase", "--abort")
			}
		}
		out, err := gitRun(ctx, 45*time.Minute, env, "git", "-C", dir, "push", "origin", "HEAD:main")
		if err == nil {
			return nil
		}
		last, lastErr = out, err
		msg := strings.ToLower(string(out) + " " + err.Error())
		retryable := strings.Contains(msg, "cannot lock ref") ||
			strings.Contains(msg, "non-fast-forward") ||
			strings.Contains(msg, "fetch first") ||
			strings.Contains(msg, "rejected") ||
			strings.Contains(msg, "failed to push")
		if !retryable {
			break
		}
		time.Sleep(time.Duration(attempt*attempt) * 400 * time.Millisecond)
	}
	return fmt.Errorf("push: %s", truncate(string(last)+" "+lastErr.Error(), 300))
}

func (g *GitHub) DownloadFile(repo, path, dest string) error {
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/HEAD/%s", g.User, repo, path)
	req, _ := http.NewRequest("GET", url, nil)
	g.auth(req)
	res, err := g.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("download %s/%s: %s", repo, path, truncate(string(body), 200))
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, res.Body)
	return err
}

func (g *GitHub) GetJSON(repo, path string, v any) error {
	tmp, err := os.CreateTemp("", "gh-json-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	tmp.Close()
	defer os.Remove(name)
	if err := g.DownloadFile(repo, path, name); err != nil {
		return err
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func gitRun(parent context.Context, timeout time.Duration, env []string, args ...string) ([]byte, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.SysProcAttr = gitSysProcAttr()
	if env != nil {
		cmd.Env = env
	} else {
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		out := buf.Bytes()
		if ctx.Err() == context.DeadlineExceeded {
			return out, fmt.Errorf("git timed out after %s", timeout)
		}
		return out, err
	case <-ctx.Done():
		killGitProcess(cmd.Process)
		<-done
		if parent.Err() != nil {
			return buf.Bytes(), fmt.Errorf("git cancelled")
		}
		return buf.Bytes(), fmt.Errorf("git timed out after %s", timeout)
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

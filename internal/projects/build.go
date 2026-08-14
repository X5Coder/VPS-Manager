package projects

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/x5coder/vps-rooms/internal/dockerx"
)

type BuildImageInput struct {
	Image      string
	Context    string
	Dockerfile string
	BuildArgs  map[string]string
	Push       bool
	GitToken   string
	WorkDir    string
	Log        io.Writer
}

func DefaultVpsroomsTag(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r == '_' || r == '/' || r == ':' || r == ' ' {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "app"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return "vpsrooms/" + out + ":latest"
}

func ParseGitContext(raw string) (repo, ref string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	raw = strings.TrimPrefix(raw, "git+")
	ref = "main"
	if i := strings.LastIndex(raw, "#"); i >= 0 {
		ref = strings.TrimSpace(raw[i+1:])
		raw = raw[:i]
		if ref == "" {
			ref = "main"
		}
	}
	if strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "git@") {
		return raw, ref, true
	}
	if strings.HasPrefix(raw, "github.com/") {
		return "https://" + raw, ref, true
	}
	return "", "", false
}

func injectGitToken(repo, token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return repo
	}
	u, err := url.Parse(repo)
	if err != nil || u.Host == "" {
		return repo
	}
	u.User = url.UserPassword("x-access-token", token)
	return u.String()
}

func (s *Service) BuildImage(in BuildImageInput) (string, error) {
	if s.Docker == nil || !s.Docker.Available() {
		return "", fmt.Errorf("Docker unavailable")
	}
	log := in.Log
	if log == nil {
		log = io.Discard
	}
	tag := strings.TrimSpace(in.Image)
	if tag == "" {
		return "", fmt.Errorf("image tag required")
	}
	work := in.WorkDir
	if work == "" {
		work = os.TempDir()
	}
	ctxDir := strings.TrimSpace(in.Context)
	cleanup := ""
	if repo, ref, isGit := ParseGitContext(ctxDir); isGit {
		dir, err := os.MkdirTemp(work, "vr-build-*")
		if err != nil {
			return "", err
		}
		cleanup = dir
		cloneURL := injectGitToken(repo, in.GitToken)
		fmt.Fprintf(log, "Cloning repository (host builder)...\n")
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--branch", ref, cloneURL, dir)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			_ = os.RemoveAll(dir)
			msg := strings.TrimSpace(string(out))
			if in.GitToken != "" {
				msg = strings.ReplaceAll(msg, in.GitToken, "***")
			}
			if ctx.Err() == context.DeadlineExceeded {
				return "", fmt.Errorf("git clone timed out after 90s (bad URL, branch, or network)")
			}
			if msg == "" {
				msg = err.Error()
			}
			return "", fmt.Errorf("git clone failed: %s", msg)
		}
		ctxDir = dir
	}
	if cleanup != "" {
		defer os.RemoveAll(cleanup)
	}
	if ctxDir == "" {
		ctxDir = "."
	}
	df := strings.TrimSpace(in.Dockerfile)
	if df == "" {
		df = "Dockerfile"
	}
	fmt.Fprintf(log, "Building %s...\n", tag)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := s.Docker.Build(ctx, dockerx.BuildOpts{
		Tag: tag, Context: ctxDir, Dockerfile: df, Args: in.BuildArgs,
	}, log); err != nil {
		return "", fmt.Errorf("docker build: %w", err)
	}
	if in.Push && dockerx.RegistryPullable(tag) {
		fmt.Fprintf(log, "Pushing %s...\n", tag)
		if err := s.Docker.PushImage(tag, log); err != nil {
			return "", fmt.Errorf("docker push: %w", err)
		}
	} else if in.Push {
		fmt.Fprintf(log, "push skipped — local tag %s is used on this VPS without a registry\n", tag)
	}
	fmt.Fprintf(log, "OK image=%s\n", tag)
	return tag, nil
}

package dockerx

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type InspectSpec struct {
	Name   string `json:"Name"`
	Config struct {
		Hostname   string            `json:"Hostname"`
		User       string            `json:"User"`
		Image      string            `json:"Image"`
		Env        []string          `json:"Env"`
		Cmd        []string          `json:"Cmd"`
		WorkingDir string            `json:"WorkingDir"`
		Labels     map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		Binds         []string `json:"Binds"`
		NetworkMode   string   `json:"NetworkMode"`
		Memory        int64    `json:"Memory"`
		RestartPolicy struct {
			Name              string `json:"Name"`
			MaximumRetryCount int    `json:"MaximumRetryCount"`
		} `json:"RestartPolicy"`
		PortBindings map[string][]struct {
			HostIp   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"PortBindings"`
	} `json:"HostConfig"`
}

func ParseContainerInspect(raw []byte) (*InspectSpec, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty inspect")
	}
	if raw[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, err
		}
		if len(arr) == 0 {
			return nil, fmt.Errorf("empty inspect")
		}
		raw = arr[0]
	}
	var spec InspectSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, err
	}
	if strings.TrimSpace(spec.Config.Image) == "" {
		return nil, fmt.Errorf("inspect missing image")
	}
	return &spec, nil
}

func (c *Client) CreateFromInspect(raw []byte, network string) (string, error) {
	spec, err := ParseContainerInspect(raw)
	if err != nil {
		return "", err
	}
	name := strings.TrimPrefix(spec.Name, "/")
	if name == "" {
		return "", fmt.Errorf("inspect missing container name")
	}
	image := strings.TrimSpace(spec.Config.Image)
	if !c.ImageExists(image) {
		return "", fmt.Errorf("image %s is not on this VPS after docker load (no registry pull)", image)
	}
	_ = c.RemoveByName(name)

	args := []string{"create", "--name", name}
	mode := strings.TrimSpace(spec.HostConfig.NetworkMode)
	if mode == "host" {
		args = append(args, "--network", "host")
	} else if network != "" {
		args = append(args, "--network", network)
	} else if mode != "" && !strings.HasPrefix(mode, "container:") {
		args = append(args, "--network", mode)
	}
	rp := strings.TrimSpace(spec.HostConfig.RestartPolicy.Name)
	if rp != "" && rp != "no" {
		if spec.HostConfig.RestartPolicy.MaximumRetryCount > 0 && rp == "on-failure" {
			args = append(args, "--restart", fmt.Sprintf("on-failure:%d", spec.HostConfig.RestartPolicy.MaximumRetryCount))
		} else {
			args = append(args, "--restart", rp)
		}
	}
	if spec.HostConfig.Memory > 0 {
		args = append(args, "--memory", fmt.Sprintf("%d", spec.HostConfig.Memory))
	}
	for _, b := range spec.HostConfig.Binds {
		if strings.TrimSpace(b) != "" {
			args = append(args, "-v", b)
		}
	}
	for specPort, pbs := range spec.HostConfig.PortBindings {
		port := specPort
		proto := "tcp"
		if i := strings.Index(specPort, "/"); i >= 0 {
			port, proto = specPort[:i], specPort[i+1:]
		}
		for _, pb := range pbs {
			hostPort := strings.TrimSpace(pb.HostPort)
			if hostPort == "" {
				continue
			}
			hostIP := strings.TrimSpace(pb.HostIp)
			if hostIP == "" || hostIP == "0.0.0.0" || hostIP == "::" {
				args = append(args, "-p", fmt.Sprintf("%s:%s/%s", hostPort, port, proto))
			} else {
				args = append(args, "-p", fmt.Sprintf("%s:%s:%s/%s", hostIP, hostPort, port, proto))
			}
		}
	}
	for _, e := range spec.Config.Env {
		if strings.TrimSpace(e) != "" {
			args = append(args, "-e", e)
		}
	}
	if spec.Config.WorkingDir != "" {
		args = append(args, "-w", spec.Config.WorkingDir)
	}
	if spec.Config.User != "" {
		args = append(args, "-u", spec.Config.User)
	}
	if spec.Config.Hostname != "" && mode != "host" {
		args = append(args, "--hostname", spec.Config.Hostname)
	}
	for k, v := range spec.Config.Labels {
		if strings.HasPrefix(k, "vps-rooms") || k == "com.docker.compose.project" || k == "com.docker.compose.service" {
			args = append(args, "--label", k+"="+v)
		}
	}
	args = append(args, "--label", "vps-rooms=1")
	args = append(args, image)
	args = append(args, spec.Config.Cmd...)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, args...)
	out, err := cmd.Output()
	id := strings.TrimSpace(string(out))
	if err != nil {
		msg := id
		if ee, ok := err.(*exec.ExitError); ok {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("docker create %s: %s: %w", name, msg, err)
	}
	if id == "" {
		return "", fmt.Errorf("docker create %s: empty id", name)
	}
	return id, nil
}

func (c *Client) ExportGzip(id, dest string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("missing container")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.bin, "export", id)
	cmd.Stdout = gz
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		_ = gz.Close()
		_ = os.Remove(dest)
		return fmt.Errorf("docker export: %s: %w", strings.TrimSpace(errBuf.String()), err)
	}
	if err := gz.Close(); err != nil {
		_ = os.Remove(dest)
		return err
	}
	return f.Close()
}

func (c *Client) OverlayRootfsFromTarGz(id, tarGz string) error {
	tmp, err := os.MkdirTemp("", "vm-rw-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	cmd := exec.Command("tar", "-C", tmp, "-xzf", tarGz)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("untar rw: %s: %w", strings.TrimSpace(string(b)), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cp := exec.CommandContext(ctx, c.bin, "cp", tmp+"/.", id+":/")
	out, err := cp.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker cp overlay: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

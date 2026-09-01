package api

import (
	"strings"
	"testing"
)

func TestAPIDocSection(t *testing.T) {
	s := APIDocSection("update", "http://127.0.0.1:9090")
	if !strings.Contains(s, "/upload") || !strings.Contains(s, "http://127.0.0.1:9090") {
		t.Fatalf("update section: %s", s)
	}
	full := APIDocSection("docs_full", "http://x:9090")
	if !strings.Contains(full, "Create token") || !strings.Contains(full, "vps-deploy-single.yml") {
		t.Fatalf("full docs missing pieces")
	}
	if !strings.Contains(full, "/api/v1/logs") || !strings.Contains(full, "logs?name=") {
		t.Fatalf("full docs missing log commands: %s", full)
	}
}

func TestAPIDocsCopies(t *testing.T) {
	s := &Server{}
	base := "http://127.0.0.1:9090"
	secret := "vm_testhook"
	sheet := s.buildAPISheet(base, secret)
	script := buildGitHubWorkflowSingle(base, secret)
	multi := buildGitHubWorkflowMulti(base, secret)
	if strings.Contains(script, "You are the VPS Manager") {
		t.Fatal("script must be GitHub YAML only")
	}
	if !strings.Contains(script, "timeout-minutes: 30") || !strings.Contains(script, "ACCEPTED") {
		t.Fatalf("script timeout/log")
	}
	if !strings.Contains(script, "ROOM_ID") || !strings.Contains(script, "PASTE_ROOM_ID_HERE") {
		t.Fatalf("script must use ROOM_ID variable")
	}
	if strings.Contains(sheet, "You operate") || strings.Contains(sheet, "curl") {
		t.Fatalf("API copy must be credentials only, got %q", sheet)
	}
	if !strings.Contains(sheet, "BASE=") || !strings.Contains(sheet, "TOKEN="+secret) {
		t.Fatalf("API sheet missing credentials")
	}
	if !strings.Contains(script, "vps-deploy-single.yml") || !strings.Contains(script, secret) {
		t.Fatalf("github single script")
	}
	if !strings.Contains(multi, "vps-deploy-multi.yml") || !strings.Contains(multi, "project.vps.tar.gz") {
		t.Fatalf("github multi script")
	}
	if !strings.Contains(script, "/upload") || !strings.Contains(script, "docker save") {
		t.Fatalf("script must upload docker save tar")
	}
	if strings.Contains(script, "ghcr.io") {
		t.Fatalf("script must not use GHCR")
	}
	if sheet == script || script == multi {
		t.Fatal("copies must differ")
	}
}

func TestMaskEnvText(t *testing.T) {
	got := maskEnvText("LINK=https://x\nEMPTY=\n# c\nSEC=secret")
	if strings.Contains(got, "https://") || strings.Contains(got, "secret") {
		t.Fatalf("leaked: %q", got)
	}
	if !strings.Contains(got, "LINK=***") || !strings.Contains(got, "EMPTY=set") {
		t.Fatalf("got %q", got)
	}
}

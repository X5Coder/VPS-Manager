package api

import (
	"strings"
	"testing"
)

func TestBuildAPIPromptModes(t *testing.T) {
	s := &Server{}
	base := "http://127.0.0.1:9090"
	secret := "vm_testhook"
	prompt := s.buildAPIPrompt(base, secret, "")
	sheet := s.buildAPISheet(base, secret)
	script := buildGitHubWorkflow(base, secret)
	if !strings.Contains(prompt, "vps-deploy.yml") || !strings.Contains(prompt, secret) {
		t.Fatalf("prompt missing yaml or credentials")
	}
	if !strings.Contains(prompt, "curl -sS") || !strings.Contains(prompt, "ROOM_ID=PASTE_ROOM_ID_HERE") {
		t.Fatalf("prompt must be a full operator brief with curl and ROOM_ID variable")
	}
	if strings.Contains(prompt, "{{BASE}}") || strings.Contains(prompt, "{{TOKEN}}") || strings.Contains(prompt, "{{SCRIPT}}") {
		t.Fatal("prompt placeholders not replaced")
	}
	if strings.Contains(script, "You are the VPS Manager") {
		t.Fatal("script must be GitHub YAML only")
	}
	if !strings.Contains(prompt, "/upload") || !strings.Contains(prompt, "status=empty") {
		t.Fatalf("prompt must document tar upload and empty rooms")
	}
	if !strings.Contains(script, "timeout-minutes: 360") || !strings.Contains(script, "UPDATED") {
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
	if !strings.Contains(script, "vps-deploy.yml") || !strings.Contains(script, secret) {
		t.Fatalf("github script")
	}
	if !strings.Contains(script, "/upload") || !strings.Contains(script, "docker save") {
		t.Fatalf("script must upload docker save tar")
	}
	if strings.Contains(script, "ghcr.io") {
		t.Fatalf("script must not use GHCR")
	}
	if prompt == sheet || sheet == script {
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

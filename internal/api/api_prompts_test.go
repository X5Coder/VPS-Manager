package api

import (
	"strings"
	"testing"
)

func TestBuildAPIPromptModes(t *testing.T) {
	s := &Server{}
	base := "http://127.0.0.1:9090"
	secret := "vm_testhook"
	read := s.buildAPIPrompt(base, secret, "read")
	write := s.buildAPIPrompt(base, secret, "write")
	both := s.buildAPIPrompt(base, secret, "both")
	sheet := s.buildAPISheet(base, secret, "both")
	if !strings.Contains(read, "Read-only") && !strings.Contains(read, "READ") {
		t.Fatalf("read prompt")
	}
	if strings.Contains(read, "/redeploy") {
		t.Fatalf("read prompt should not teach redeploy")
	}
	if !strings.Contains(write, "WRITE") || !strings.Contains(write, "/redeploy") {
		t.Fatalf("write prompt missing redeploy")
	}
	if !strings.Contains(both, "BOTH") || !strings.Contains(both, secret) || !strings.Contains(both, base) {
		t.Fatalf("both prompt missing secret or base")
	}
	if !strings.Contains(both, "You operate") {
		t.Fatalf("prompt should be an operator brief")
	}
	if strings.Contains(sheet, "You operate") {
		t.Fatalf("API sheet must not be the prompt")
	}
	if !strings.Contains(sheet, "BASE=") || !strings.Contains(sheet, "TOKEN="+secret) {
		t.Fatalf("API sheet missing credentials")
	}
	if read == write || write == both || both == sheet {
		t.Fatal("prompt and API copies must differ")
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

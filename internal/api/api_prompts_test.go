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
	if !strings.Contains(read, "READ-ONLY") || strings.Contains(read, "POST /api/v1/projects") {
		t.Fatalf("read prompt should not teach POST create")
	}
	if !strings.Contains(write, "WRITE operator") || !strings.Contains(write, "POST /api/v1/projects") {
		t.Fatalf("write prompt missing create")
	}
	if !strings.Contains(both, "BOTH") || !strings.Contains(both, secret) || !strings.Contains(both, base) {
		t.Fatalf("both prompt missing secret or base")
	}
	if read == write || write == both {
		t.Fatal("prompts must differ by mode")
	}
}

package ai

import "testing"

func TestParseUpdateID(t *testing.T) {
	raw := `{"say":"ok","image":"nginx:alpine","update_id":"abc-123","start":true,"done":true}`
	r, ok := parseReply(raw)
	if !ok {
		t.Fatal("parse")
	}
	if r.UpdateID != "abc-123" || r.Image != "nginx:alpine" || !r.Start {
		t.Fatalf("got %+v", r)
	}
}

func TestExtractAIText(t *testing.T) {
	got := extractAIText([]byte(`{"text":"hello from slot"}`))
	if got != "hello from slot" {
		t.Fatalf("got %q", got)
	}
}

func TestAISlotOrderOnlyPreferred(t *testing.T) {
	seen := map[int]bool{}
	for _, n := range aiSlotOrder() {
		if n < 6 || n > 22 {
			t.Fatalf("slot out of range %d", n)
		}
		seen[n] = true
	}
	if len(seen) != 17 {
		t.Fatalf("want 17 slots got %d", len(seen))
	}
}

func TestParseCreateTokenNameOnly(t *testing.T) {
	raw := `{"say":"created","ask":[],"create_token":true,"token_name":"ops","done":true}`
	r, ok := parseReply(raw)
	if !ok {
		t.Fatal("parse")
	}
	if !r.CreateToken || r.TokenName != "ops" {
		t.Fatalf("create token by name only failed: %+v", r)
	}
}

func TestSanitizeSayUnescapesNewlines(t *testing.T) {
	raw := `{"say":"في هذا سجل API.\n\n- تم إنشاء مشروع embeddings-server\n\nملاحظة مهمة","ask":[],"choices":[],"done":true}`
	r, ok := parseReply(raw)
	if !ok {
		t.Fatal("parse")
	}
	s := sanitizeSay(r.Say)
	if !contains(s, "embeddings-server") {
		t.Fatalf("missing project: %q", s)
	}
	if contains(s, `\n`) {
		t.Fatalf("literal \\n still present: %q", s)
	}
	if !contains(s, "\n") {
		t.Fatalf("expected real newlines: %q", s)
	}
}

func TestSanitizeSayDump(t *testing.T) {
	s := sanitizeSay(`{"say":"hello\nworld","ask":[],"done":true}`)
	if s != "hello\nworld" {
		t.Fatalf("got %q", s)
	}
}

func TestLooksLikeRemoteLogin(t *testing.T) {
	if !LooksLikeRemoteLogin("ssh root@13.140.164.29 -p 22") {
		t.Fatal("ssh")
	}
	if !LooksLikeRemoteLogin("sshpass -p x ssh root@host") {
		t.Fatal("sshpass")
	}
	if LooksLikeRemoteLogin("ssh-keygen -t ed25519") {
		t.Fatal("ssh-keygen must be allowed")
	}
	if LooksLikeRemoteLogin("docker ps") {
		t.Fatal("docker")
	}
	if !Dangerous("ssh user@host") {
		t.Fatal("Dangerous ssh")
	}
}

func contains(s, sub string) bool {
	return indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

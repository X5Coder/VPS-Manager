package ai

import "testing"

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

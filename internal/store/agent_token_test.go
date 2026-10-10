package store

import (
	"path/filepath"
	"testing"
	"time"
)

// Regenerating the agent API key must collapse credentials to exactly one:
// legacy keys disappear and only the new secret verifies.
func TestReplaceAgentTokenCollapsesToSingle(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now().UTC()
	seed := []struct {
		id, name, hash string
	}{
		{"id-old-1", "legacy one", "hash-old-1"},
		{"id-old-2", "legacy two", "hash-old-2"},
	}
	for _, sd := range seed {
		if err := s.CreateAgentToken(AgentToken{ID: sd.id, Name: sd.name, Prefix: "prefix", CreatedAt: now}, sd.hash); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReplaceAgentToken(AgentToken{ID: "id-new", Name: "Agent key", Prefix: "prefix-new", CreatedAt: now}, "hash-new"); err != nil {
		t.Fatal(err)
	}
	tokens, err := s.ListAgentTokens()
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected exactly 1 token, got %d", len(tokens))
	}
	if tokens[0].ID != "id-new" || tokens[0].Name != "Agent key" {
		t.Fatalf("unexpected remaining token: %+v", tokens[0])
	}
	for _, h := range []string{"hash-old-1", "hash-old-2"} {
		if ok, err := s.VerifyAgentToken(h); err != nil || ok {
			t.Fatalf("legacy hash %q must stop verifying (ok=%v err=%v)", h, ok, err)
		}
	}
	if ok, err := s.VerifyAgentToken("hash-new"); err != nil || !ok {
		t.Fatalf("new hash must verify (ok=%v err=%v)", ok, err)
	}
}

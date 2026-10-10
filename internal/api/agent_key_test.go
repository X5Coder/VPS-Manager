package api

import (
	"os"
	"path/filepath"
	"testing"
)

// The reveal copy must round-trip exactly, reject empties, and fail closed
// (missing file => error, never an empty secret).
func TestAgentKeySecretRoundtrip(t *testing.T) {
	dir := t.TempDir()
	if _, err := readAgentKeySecret(dir); err == nil {
		t.Fatal("missing file must error")
	}
	if err := saveAgentKeySecret(dir, ""); err == nil {
		t.Fatal("empty secret must fail")
	}
	if err := saveAgentKeySecret(dir, "x5ca_first"); err != nil {
		t.Fatal(err)
	}
	got, err := readAgentKeySecret(dir)
	if err != nil || got != "x5ca_first" {
		t.Fatalf("roundtrip got %q err %v", got, err)
	}
	if err := saveAgentKeySecret(dir, "x5ca_second"); err != nil {
		t.Fatal(err)
	}
	if got, _ := readAgentKeySecret(dir); got != "x5ca_second" {
		t.Fatalf("overwrite got %q", got)
	}
	removeAgentKeySecret(dir)
	if _, err := readAgentKeySecret(dir); err == nil {
		t.Fatal("removed file must error")
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets")); err != nil {
		t.Fatalf("secrets dir must exist: %v", err)
	}
}

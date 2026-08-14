package projects

import "testing"

func TestParseGitContext(t *testing.T) {
	repo, ref, ok := ParseGitContext("git+https://github.com/ORG/REPO.git#main")
	if !ok || repo != "https://github.com/ORG/REPO.git" || ref != "main" {
		t.Fatalf("got %q %q %v", repo, ref, ok)
	}
	repo, ref, ok = ParseGitContext("https://github.com/ORG/REPO.git")
	if !ok || ref != "main" || repo != "https://github.com/ORG/REPO.git" {
		t.Fatalf("default ref: %q %q %v", repo, ref, ok)
	}
	if _, _, ok := ParseGitContext("/opt/src"); ok {
		t.Fatal("path should not parse as git")
	}
}

func TestDefaultVpsroomsTag(t *testing.T) {
	if g := DefaultVpsroomsTag("AI Server"); g != "vpsrooms/ai-server:latest" {
		t.Fatalf("got %s", g)
	}
}

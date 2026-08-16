package projects

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDestCovered(t *testing.T) {
	if destCovered([]string{"/x/.env:/app/.env:ro"}, "/app/data") {
		t.Fatal(".env must not cover data")
	}
	if !destCovered([]string{"/vol:/app/data"}, "/app/data") {
		t.Fatal("/app/data bind should count")
	}
	if !destCovered([]string{"/vol:/app"}, "/app/output") {
		t.Fatal("/app bind covers /app/output")
	}
}

func TestPersistDestsFromEnv(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("OUTPUT_DIR=output\nMEDIA_PORT=8090\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dests := persistDestsFromEnv(env, nil)
	joined := strings.Join(dests, ",")
	if !strings.Contains(joined, "/app/output") || !strings.Contains(joined, "/app/data") {
		t.Fatalf("missing dests: %v", dests)
	}
	if strings.Contains(joined, "/app/logs") {
		t.Fatalf("must not invent /app/logs: %v", dests)
	}
}

func TestMergeReplacesLegacyVolumeRoot(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("X=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vol := filepath.Join(dir, "vol")
	if err := os.MkdirAll(vol, 0o755); err != nil {
		t.Fatal(err)
	}
	dests := persistDestsFromEnv(env, nil)
	out := mergePersistentBinds([]string{vol + ":/app/data"}, env, vol, dests)
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, vol+":/app/data\n") || strings.HasSuffix(joined, vol+":/app/data") {
		t.Fatalf("legacy root bind kept: %v", out)
	}
	if !strings.Contains(joined, filepath.Join(vol, "app-data")+":/app/data") {
		t.Fatalf("expected app-data bind: %v", out)
	}
}

func TestMergePersistentBindsMulti(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("OUTPUT_DIR=output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vol := filepath.Join(dir, "vol")
	dests := persistDestsFromEnv(env, nil)
	out := mergePersistentBinds(nil, env, vol, dests)
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, env+":/app/.env:ro") {
		t.Fatalf("missing env: %v", out)
	}
	if !strings.Contains(joined, persistSubdir("/app/output")+":/app/output") {
		t.Fatalf("missing output bind: %v", out)
	}
	if !strings.Contains(joined, persistSubdir("/app/data")+":/app/data") {
		t.Fatalf("missing data bind: %v", out)
	}
	if strings.Contains(joined, vol+":/app/data") && !strings.Contains(joined, vol+"/app-data:/app/data") {
		t.Fatal("must not mount volume root over /app/data anymore")
	}
}

func TestPruneChildDests(t *testing.T) {
	got := pruneChildDests([]string{"/app/data", "/app/data/profiles", "/app/output"})
	joined := strings.Join(got, ",")
	if strings.Contains(joined, "/app/data/profiles") {
		t.Fatalf("child dest should drop: %v", got)
	}
	if !strings.Contains(joined, "/app/data") || !strings.Contains(joined, "/app/output") {
		t.Fatalf("parents kept: %v", got)
	}
}
	vol := t.TempDir()
	if err := os.WriteFile(filepath.Join(vol, "config.db"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(vol, "profiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	migrateLegacyAppDataRoot(vol)
	if _, err := os.Stat(filepath.Join(vol, "app-data", "config.db")); err != nil {
		t.Fatal("config.db should move under app-data")
	}
}

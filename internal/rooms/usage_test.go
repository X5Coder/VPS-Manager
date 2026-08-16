package rooms

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirSize(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "a.bin"), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := dirSize(d); n != 100 {
		t.Fatalf("got %d", n)
	}
}

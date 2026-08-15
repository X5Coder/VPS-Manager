package backup

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/x5coder/vps-rooms/internal/store"
)

func TestIsManagedBackupRepo(t *testing.T) {
	if !isManagedBackupRepo("vps-manage-system") || !isManagedBackupRepo("vps-manage-volumes-003") {
		t.Fatal("managed backup repos must match")
	}
	if isManagedBackupRepo("VPS-Manager") || isManagedBackupRepo("linkedin-auto") || isManagedBackupRepo("") {
		t.Fatal("unrelated repos must not be wiped")
	}
}

func TestArmFromAndClaimDailySlot(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Service{Store: st}
	_ = st.SetMeta("backup_interval_hours", "24")
	start := time.Date(2026, 8, 16, 0, 21, 0, 0, time.UTC)
	s.armFrom(start)
	next, ok := s.parseNextAt()
	if !ok || !next.Equal(start.Add(24*time.Hour)) {
		t.Fatalf("next=%v ok=%v", next, ok)
	}
	if _, claimed := s.claimIfDue(); claimed {
		t.Fatal("future slot must not fire")
	}
	_ = st.SetMeta("backup_next_at", time.Now().UTC().Add(-2*time.Hour).Format(time.RFC3339))
	due, claimed := s.claimIfDue()
	if !claimed {
		t.Fatal("past due must fire once")
	}
	if due.After(time.Now().UTC()) {
		t.Fatalf("due=%v", due)
	}
	next, _ = s.parseNextAt()
	if !next.After(time.Now().UTC()) {
		t.Fatalf("claimed next must be in the future, got %v", next)
	}
	if _, claimed := s.claimIfDue(); claimed {
		t.Fatal("must not fire twice after claiming the slot")
	}
}

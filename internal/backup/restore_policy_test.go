package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/x5coder/vps-rooms/internal/store"
)

func TestReportDoesNotDecrease(t *testing.T) {
	s := &Service{}
	s.running = true
	s.activeGen = s.stopGen.Load()
	s.liveJob = &Job{Kind: "backup", Status: "running", Percent: 40, Logs: []string{}}
	s.report(12, "later step with a lower hardcoded percent")
	if s.liveJob.Percent != 40 {
		t.Fatalf("percent moved backwards: %d", s.liveJob.Percent)
	}
	s.report(61, "real progress")
	if s.liveJob.Percent != 61 {
		t.Fatalf("percent=%d", s.liveJob.Percent)
	}
}

func TestReportIgnoresAfterCancel(t *testing.T) {
	s := &Service{}
	s.running = true
	s.activeGen = s.stopGen.Load()
	s.liveJob = &Job{Kind: "backup", Status: "running", Percent: 19, Logs: []string{}}
	if err := s.StopJob(); err != nil {
		t.Fatal(err)
	}
	s.report(55, "old worker still running")
	if s.liveJob.Percent != 19 {
		t.Fatalf("cancelled job must freeze percent, got %d", s.liveJob.Percent)
	}
	if s.liveJob.Status != "paused" {
		t.Fatalf("status=%s", s.liveJob.Status)
	}
}

func TestResumePercentUsesRoomsDone(t *testing.T) {
	s := &Service{}
	if p := s.resumePercent(nil); p != 2 {
		t.Fatalf("fresh=%d", p)
	}
	p := s.resumePercent(&Checkpoint{Kind: "backup", SystemDone: true, RoomsDone: []string{"a", "b"}})
	if p < 16 {
		t.Fatalf("system+rooms should be well above inspect, got %d", p)
	}
}

func TestStopJobUnlocksBackupButton(t *testing.T) {
	s := &Service{}
	s.running = true
	s.activeGen = s.stopGen.Load()
	s.liveJob = &Job{Kind: "backup", Status: "running", Message: "Backup started"}
	if err := s.StopJob(); err != nil {
		t.Fatal(err)
	}
	if s.running {
		t.Fatal("running flag must clear so Backup now is enabled")
	}
	j := s.CurrentJob()
	if j == nil || j.Status == "running" {
		t.Fatalf("job still running: %+v", j)
	}
	if j.Status != "paused" {
		t.Fatalf("cancel must pause, not wipe: %+v", j)
	}
	if err := s.errIfStopped(); err == nil {
		t.Fatal("in-flight backup must see stop")
	}
}

func TestStopJobKeepsCheckpoint(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Service{Store: st}
	s.saveCheckpoint(&Checkpoint{Kind: "backup", RoomsDone: []string{"room-1"}})
	s.running = true
	s.activeGen = s.stopGen.Load()
	s.liveJob = &Job{Kind: "backup", Status: "running"}
	if err := s.StopJob(); err != nil {
		t.Fatal(err)
	}
	cp := s.loadCheckpoint()
	if cp == nil || len(cp.RoomsDone) != 1 {
		t.Fatalf("checkpoint must stay so Start can resume: %+v", cp)
	}
}

func TestImageAlreadyOnBackup(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Service{Store: st}
	if s.imageAlreadyOnBackup("nginx:latest") {
		t.Fatal("no docker / no layout must not skip")
	}
	s.saveCheckpoint(&Checkpoint{Kind: "backup", Layout: &BackupLayout{
		Images: []ImageLayout{{Key: "abc", DockerID: "sha256:deadbeef"}},
	}})
	if s.imageAlreadyOnBackup("nginx:latest") {
		t.Fatal("without docker id match must not skip")
	}
}

func TestStaleRunningJobUnlocksBackupNow(t *testing.T) {
	s := &Service{}
	s.running = false
	s.liveJob = &Job{Kind: "backup", Status: "running", Message: "Backup started"}
	j := s.CurrentJob()
	if j == nil || j.Status == "running" {
		t.Fatalf("stale job must not stay running: %+v", j)
	}
}

func TestHashFileRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := HashFile(dir); err == nil {
		t.Fatal("directory must not be hashed as a file")
	}
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, n, err := HashFile(p); err != nil || n != 2 {
		t.Fatalf("file hash n=%d err=%v", n, err)
	}
}

func TestAcceptedBackupFormat(t *testing.T) {
	if !acceptedBackupFormat(FormatMagic) || !acceptedBackupFormat(FormatMagicV2) {
		t.Fatal("InspectRemote must accept v1 and v2 FORMAT")
	}
	if acceptedBackupFormat("VPS-MANAGE-BACKUP-v3") || acceptedBackupFormat("") {
		t.Fatal("unknown FORMAT must be rejected")
	}
}

func TestRestoreNeverPullsFromRegistry(t *testing.T) {
	if restoreComposePullEnabled() {
		t.Fatal("restore must call ComposeUp with pull=false, not ComposePullUp")
	}
}

func TestAddImageErrorFailsBackup(t *testing.T) {
	err := backupImageErr("nginx:latest", fmt.Errorf("docker save failed"))
	if err == nil {
		t.Fatal("addImage error must fail executeBackup")
	}
}

func TestValidateBackupCompleteRequiresImageKeys(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := store.Room{ID: "room-aaaaaaaa", Name: "admin", Kind: store.KindSingle}
	if err := st.UpsertRoom(r); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertContainer(store.Container{
		ID: "c1", RoomID: r.ID, Ordinal: 1, Name: "admin-1", Image: "nginx:latest",
	}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: st}
	m := &Manifest{
		Format: FormatMagicV2, Version: 2,
		SystemFiles: []FileEntry{{Path: "system/panel.db", Size: 10, Chunks: []string{"x"}}},
		Layout: &BackupLayout{
			Rooms: []RoomLayout{{ID: r.ID, Name: r.Name, Short: "aaaaaaaa", ImageKeys: nil}},
		},
	}
	if err := svc.validateBackupComplete(m, []store.Room{r}); err == nil {
		t.Fatal("expected error when ImageKeys empty for a room with containers")
	}
}

func TestValidateBackupCompleteOKWithLayers(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := store.Room{ID: "room-bbbbbbbb", Name: "admin", Kind: store.KindSingle}
	if err := st.UpsertRoom(r); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertContainer(store.Container{
		ID: "c1", RoomID: r.ID, Ordinal: 1, Name: "admin-1", Image: "nginx:latest",
	}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: st}
	m := &Manifest{
		Format: FormatMagicV2, Version: 2,
		SystemFiles: []FileEntry{{Path: "system/panel.db", Size: 10, Chunks: []string{"x"}}},
		Layout: &BackupLayout{
			Rooms: []RoomLayout{{ID: r.ID, Name: r.Name, ImageKeys: []string{"abc"}}},
			Images: []ImageLayout{{
				Key:    "abc",
				Layers: []ImageLayerUse{{Rel: "blobs/sha256/x", Digest: "sha256:deadbeef"}},
			}},
			Layers: []LayerLayout{{
				Digest: "sha256:deadbeef", Assets: []string{"l-deadbeef.part"},
			}},
		},
	}
	if err := svc.validateBackupComplete(m, []store.Room{r}); err != nil {
		t.Fatal(err)
	}
}

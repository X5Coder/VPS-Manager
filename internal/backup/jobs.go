package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Job struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`   // backup | restore
	Status     string   `json:"status"` // queued | running | paused | done | error
	Label      string   `json:"label"`
	Message    string   `json:"message"`
	Progress   string   `json:"progress"`
	Percent    int      `json:"percent"`
	Logs       []string `json:"logs,omitempty"`
	Error      string   `json:"error,omitempty"`
	StartedAt  string   `json:"started_at"`
	EndedAt    string   `json:"ended_at,omitempty"`
	SnapshotID string   `json:"snapshot_id,omitempty"`
}

func (s *Service) setJob(j Job) {
	if len(j.Logs) > 250 {
		j.Logs = append([]string{}, j.Logs[len(j.Logs)-250:]...)
	}
	cp := j
	cp.Logs = append([]string{}, j.Logs...)
	s.mu.Lock()
	s.liveJob = &cp
	s.lastLog = cp.Progress
	flush := cp.Status == "done" || cp.Status == "error"
	s.mu.Unlock()
	if flush {
		s.flushJob(cp)
	}
}

func (s *Service) flushJob(j Job) {
	b, _ := json.Marshal(j)
	_ = s.Store.SetMeta("backup_job", string(b))
}

func (s *Service) markStaleLocked() bool {
	if s.liveJob == nil || s.running {
		return false
	}
	if s.liveJob.Status != "running" && s.liveJob.Status != "queued" {
		return false
	}
	s.liveJob.Status = "error"
	s.liveJob.Error = "Cancelled"
	s.liveJob.Message = "Cancelled"
	s.liveJob.Progress = "Cancelled"
	s.liveJob.EndedAt = time.Now().UTC().Format(time.RFC3339)
	s.liveJob.Logs = append(append([]string{}, s.liveJob.Logs...), time.Now().UTC().Format(time.RFC3339)+"  Cancelled (job was not running)")
	return true
}

func (s *Service) CurrentJob() *Job {
	s.mu.Lock()
	if s.liveJob == nil && s.Store != nil {
		s.mu.Unlock()
		raw, ok, _ := s.Store.GetMeta("backup_job")
		s.mu.Lock()
		if s.liveJob == nil && ok && raw != "" {
			var j Job
			if json.Unmarshal([]byte(raw), &j) == nil {
				s.liveJob = &j
			}
		}
	}
	if s.liveJob == nil {
		s.mu.Unlock()
		return nil
	}
	stale := s.markStaleLocked()
	cp := *s.liveJob
	cp.Logs = append([]string{}, s.liveJob.Logs...)
	s.mu.Unlock()
	if stale && s.Store != nil {
		s.flushJob(cp)
	}
	return &cp
}

func (s *Service) report(percent int, format string, args ...any) {
	if s.errIfStopped() != nil {
		return
	}
	msg := strings.TrimSpace(fmt.Sprintf(format, args...))
	if msg != "" {
		s.logf("%s", msg)
	}
	j := s.CurrentJob()
	if j == nil || (j.Status != "running" && j.Status != "queued") {
		return
	}
	if percent >= 0 {
		if percent < j.Percent {
			percent = j.Percent
		}
		j.Percent = percent
	}
	if msg != "" {
		j.Progress = msg
		line := time.Now().UTC().Format(time.RFC3339) + "  " + msg
		j.Logs = append(j.Logs, line)
	}
	s.setJob(*j)
}

func (s *Service) StartBackupAsync(label, description string, scheduled bool) (*Job, error) {
	en, _, _ := s.Store.GetMeta("backup_enabled")
	if en != "1" {
		return nil, fmt.Errorf("backup is turned off — enable it on the Backup page first")
	}
	token, _, err := s.LoadToken()
	if err != nil || token == "" {
		return nil, fmt.Errorf("GitHub PAT required — save and validate a classic token with repo scope first")
	}
	s.waitWorkers(25 * time.Second)
	if s.workers.Load() > 0 {
		return nil, fmt.Errorf("previous backup is still stopping — wait a few seconds and press Start")
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil, fmt.Errorf("a backup/restore job is already running — press Cancel first")
	}
	s.running = true
	s.activeGen = s.stopGen.Load()
	ctx, cancel := context.WithCancel(context.Background())
	if s.jobCancel != nil {
		s.jobCancel()
	}
	s.jobCancel = cancel
	s.jobCtx = ctx
	s.mu.Unlock()

	j := Job{
		ID: uuid.NewString(), Kind: "backup", Status: "running",
		Label: label, Message: "Backup started — verifying last point",
		Progress: "Inspecting last point…", Percent: 2, Logs: []string{},
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if cp := s.loadCheckpoint(); cp != nil && cp.Kind == "backup" {
		n := len(cp.RoomsDone)
		layers := 0
		if cp.Layout != nil {
			layers = len(cp.Layout.Layers)
		}
		if n > 0 || layers > 0 || cp.SystemDone {
			j.Message = fmt.Sprintf("Resuming — %d room(s) already on GitHub", n)
			j.Progress = "Resuming from last point"
			j.Percent = s.resumePercent(cp)
		}
	}
	if j.Label == "" {
		j.Label = "Backup now"
	}
	s.setJob(j)

	go func() {
		gen := s.stopGen.Load()
		s.workers.Add(1)
		defer func() {
			s.workers.Add(-1)
			s.mu.Lock()
			if s.stopGen.Load() == gen {
				s.running = false
			}
			s.mu.Unlock()
		}()
		rec, err := s.executeBackup(label, description, scheduled)
		if s.stopGen.Load() != gen {
			return
		}
		cur := s.CurrentJob()
		if cur == nil {
			cur = &j
		}
		cur.EndedAt = time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			cur.Status = "error"
			cur.Error = err.Error()
			cur.Message = "Backup failed"
			cur.Progress = "Failed"
			cur.Logs = append(cur.Logs, time.Now().UTC().Format(time.RFC3339)+"  FAILED: "+err.Error())
			s.setJob(*cur)
			s.logf("FAILED: %s", err.Error())
			_ = s.Store.SetMeta("backup_last_error", err.Error())
			if scheduled {
				s.scheduleRetrySoon()
			}
			return
		}
		cur.Status = "done"
		cur.Message = "Backup completed"
		cur.Progress = "Done"
		cur.Percent = 100
		cur.SnapshotID = rec.ID
		cur.Error = ""
		cur.Logs = append(cur.Logs, time.Now().UTC().Format(time.RFC3339)+"  Backup finished ("+rec.ID[:8]+")")
		s.setJob(*cur)
		s.logf("backup ok %s", rec.ID)
	}()
	return &j, nil
}

func (s *Service) StartRestoreAsync(token, snapshotID string) (*Job, error) {
	s.waitWorkers(25 * time.Second)
	if s.workers.Load() > 0 {
		return nil, fmt.Errorf("previous job is still stopping — wait a few seconds and press Start")
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil, fmt.Errorf("a backup/restore job is already running — press Cancel first")
	}
	s.running = true
	s.activeGen = s.stopGen.Load()
	ctx, cancel := context.WithCancel(context.Background())
	if s.jobCancel != nil {
		s.jobCancel()
	}
	s.jobCancel = cancel
	s.jobCtx = ctx
	s.mu.Unlock()

	j := Job{
		ID: uuid.NewString(), Kind: "restore", Status: "running",
		Label: "Restore", Message: "Restore started — this can take several minutes",
		Progress: "Inspecting last restore point…", Percent: 2, Logs: []string{},
		StartedAt:  time.Now().UTC().Format(time.RFC3339),
		SnapshotID: snapshotID,
	}
	s.setJob(j)

	go func() {
		gen := s.stopGen.Load()
		s.workers.Add(1)
		defer func() {
			s.workers.Add(-1)
			s.mu.Lock()
			if s.stopGen.Load() == gen {
				s.running = false
			}
			s.mu.Unlock()
		}()
		err := s.executeRestore(token, snapshotID)
		if s.stopGen.Load() != gen {
			return
		}
		cur := s.CurrentJob()
		if cur == nil {
			cur = &j
		}
		cur.EndedAt = time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			cur.Status = "error"
			cur.Error = err.Error()
			cur.Message = "Restore failed"
			cur.Progress = "Failed"
			cur.Logs = append(cur.Logs, time.Now().UTC().Format(time.RFC3339)+"  FAILED: "+err.Error())
			s.setJob(*cur)
			s.logf("FAILED: %s", err.Error())
			return
		}
		cur.Status = "done"
		cur.Message = "Restore completed"
		cur.Progress = "Done"
		cur.Percent = 100
		cur.Error = ""
		cur.Logs = append(cur.Logs, time.Now().UTC().Format(time.RFC3339)+"  Restore finished")
		s.setJob(*cur)
		s.logf("restore ok")
	}()
	return &j, nil
}

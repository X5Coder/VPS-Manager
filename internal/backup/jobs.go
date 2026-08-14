package backup

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Job struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`   // backup | restore
	Status     string   `json:"status"` // queued | running | done | error
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

func (s *Service) CurrentJob() *Job {
	s.mu.Lock()
	if s.liveJob != nil {
		cp := *s.liveJob
		cp.Logs = append([]string{}, s.liveJob.Logs...)
		s.mu.Unlock()
		return &cp
	}
	s.mu.Unlock()
	raw, ok, _ := s.Store.GetMeta("backup_job")
	if !ok || raw == "" {
		return nil
	}
	var j Job
	if err := json.Unmarshal([]byte(raw), &j); err != nil {
		return nil
	}
	s.mu.Lock()
	if s.liveJob == nil {
		s.liveJob = &j
	}
	s.mu.Unlock()
	return &j
}

func (s *Service) report(percent int, format string, args ...any) {
	msg := strings.TrimSpace(fmt.Sprintf(format, args...))
	s.logf("%s", msg)
	j := s.CurrentJob()
	if j == nil || (j.Status != "running" && j.Status != "queued") {
		return
	}
	if percent >= 0 {
		j.Percent = percent
	}
	if msg != "" {
		j.Progress = msg
		line := time.Now().UTC().Format("15:04:05") + "  " + msg
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
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		if j := s.CurrentJob(); j != nil {
			return j, nil
		}
		return nil, fmt.Errorf("a backup/restore job is already running")
	}
	s.running = true
	s.mu.Unlock()

	j := Job{
		ID: uuid.NewString(), Kind: "backup", Status: "running",
		Label: label, Message: "Backup started — this can take several minutes",
		Progress: "Inspecting last point…", Percent: 1, Logs: []string{},
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if j.Label == "" {
		j.Label = "Backup now"
	}
	s.setJob(j)
	s.report(1, "Inspecting last point")

	go func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()
		rec, err := s.executeBackup(label, description, scheduled)
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
			cur.Logs = append(cur.Logs, time.Now().UTC().Format("15:04:05")+"  FAILED: "+err.Error())
			s.setJob(*cur)
			s.logf("FAILED: %s", err.Error())
			_ = s.Store.SetMeta("backup_last_error", err.Error())
			return
		}
		cur.Status = "done"
		cur.Message = "Backup completed"
		cur.Progress = "Done"
		cur.Percent = 100
		cur.SnapshotID = rec.ID
		cur.Error = ""
		cur.Logs = append(cur.Logs, time.Now().UTC().Format("15:04:05")+"  Backup finished ("+rec.ID[:8]+")")
		s.setJob(*cur)
		s.logf("backup ok %s", rec.ID)
	}()
	return &j, nil
}

func (s *Service) StartRestoreAsync(token, snapshotID string) (*Job, error) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		if j := s.CurrentJob(); j != nil {
			return j, nil
		}
		return nil, fmt.Errorf("a backup/restore job is already running")
	}
	s.running = true
	s.mu.Unlock()

	j := Job{
		ID: uuid.NewString(), Kind: "restore", Status: "running",
		Label: "Restore", Message: "Restore started — this can take several minutes",
		Progress: "Inspecting last restore point…", Percent: 1, Logs: []string{},
		StartedAt:  time.Now().UTC().Format(time.RFC3339),
		SnapshotID: snapshotID,
	}
	s.setJob(j)
	s.report(1, "Inspecting last restore point")

	go func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()
		err := s.executeRestore(token, snapshotID)
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
			cur.Logs = append(cur.Logs, time.Now().UTC().Format("15:04:05")+"  FAILED: "+err.Error())
			s.setJob(*cur)
			s.logf("FAILED: %s", err.Error())
			return
		}
		cur.Status = "done"
		cur.Message = "Restore completed"
		cur.Progress = "Done"
		cur.Percent = 100
		cur.Error = ""
		cur.Logs = append(cur.Logs, time.Now().UTC().Format("15:04:05")+"  Restore finished")
		s.setJob(*cur)
		s.logf("restore ok")
	}()
	return &j, nil
}

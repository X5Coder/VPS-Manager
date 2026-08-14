package api

import "testing"

func TestTryBeginJobBlocksDuplicate(t *testing.T) {
	s := &Server{}
	if err := s.tryBeginJob("p1", "deploy"); err != nil {
		t.Fatal(err)
	}
	if s.jobKind("p1") != "deploy" || !s.anyJob() {
		t.Fatal("job not recorded")
	}
	if err := s.tryBeginJob("p1", "build"); err == nil {
		t.Fatal("expected conflict")
	}
	if err := s.tryBeginJob("p2", "build"); err != nil {
		t.Fatal(err)
	}
	s.endJob("p1")
	if s.jobKind("p1") != "" {
		t.Fatal("job should end")
	}
	if err := s.tryBeginJob("p1", "deploy"); err != nil {
		t.Fatal(err)
	}
}

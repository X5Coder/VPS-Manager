package projects

import "testing"

func TestCompactUpdateHistoryDropsSameSecondDupes(t *testing.T) {
	in := []UpdateEvent{
		{N: 2, At: "2026-08-14T16:10:55Z", OK: true, Image: "vpsrooms/linkedin-auto:latest"},
		{N: 1, At: "2026-08-14T16:10:55Z", OK: true, Image: "vpsrooms/linkedin-auto:latest"},
	}
	got := compactUpdateHistory(in)
	if len(got) != 1 || got[0].N != 1 {
		t.Fatalf("got %#v", got)
	}
}

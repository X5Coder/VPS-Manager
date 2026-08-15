package projects

import (
	"fmt"
	"testing"
)

func TestCompactUpdateHistoryDropsSameSecondDupes(t *testing.T) {
	in := []UpdateEvent{
		{N: 2, At: "2026-08-14T16:10:55Z", OK: true, Image: "vpsrooms/linkedin-auto:latest"},
		{N: 1, At: "2026-08-14T16:10:55Z", OK: true, Image: "vpsrooms/linkedin-auto:latest"},
	}
	got := compactUpdateHistory(in)
	if len(got) != 1 || got[0].N != 2 {
		t.Fatalf("got %#v", got)
	}
}

func TestCompactUpdateHistoryKeepsFiveNewest(t *testing.T) {
	in := make([]UpdateEvent, 0, 8)
	for i := 8; i >= 1; i-- {
		in = append(in, UpdateEvent{
			N: i, At: fmt.Sprintf("2026-08-14T16:%02d:00Z", i), OK: true, Image: fmt.Sprintf("img:%d", i),
		})
	}
	got := compactUpdateHistory(in)
	if len(got) != 5 {
		t.Fatalf("len=%d %#v", len(got), got)
	}
	if got[0].N != 8 {
		t.Fatalf("newest should stay first: %#v", got[0])
	}
}

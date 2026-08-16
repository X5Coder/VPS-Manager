package backup

import "testing"

func TestParseRoomRepo(t *testing.T) {
	slug, seq, ok := parseRoomRepo("vps-room-2dd9ee34-3")
	if !ok || slug != "2dd9ee34" || seq != 3 {
		t.Fatalf("got %v %s %d", ok, slug, seq)
	}
	if _, _, ok := parseRoomRepo("vps-manage-index"); ok {
		t.Fatal("not a room repo")
	}
	if _, _, ok := parseRoomRepo("vps-room-"); ok {
		t.Fatal("empty")
	}
}

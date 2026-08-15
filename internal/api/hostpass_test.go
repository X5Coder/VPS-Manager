package api

import "testing"

func TestReplaceRootShadowHash(t *testing.T) {
	in := []byte("daemon:*:1:1:1:1:::\nroot:$6$old$hash:1:2:3:4:::\nwww-data:*:1:1:1:1:::\n")
	got, err := replaceRootShadowHash(in, "$6$new$hash")
	if err != nil {
		t.Fatal(err)
	}
	want := "daemon:*:1:1:1:1:::\nroot:$6$new$hash:1:2:3:4:::\nwww-data:*:1:1:1:1:::\n"
	if string(got) != want {
		t.Fatalf("got %q", got)
	}
}

func TestReplaceRootShadowHashRejectsBadHash(t *testing.T) {
	_, err := replaceRootShadowHash([]byte("root:x:1:1:::\n"), "bad:hash")
	if err == nil {
		t.Fatal("colon in hash must be rejected")
	}
}

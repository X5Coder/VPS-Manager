package api

import "testing"

func TestValidateLinuxRootPassword(t *testing.T) {
	if err := validateLinuxRootPassword("short"); err == nil {
		t.Fatal("too short")
	}
	if err := validateLinuxRootPassword("allletters"); err == nil {
		t.Fatal("needs digit")
	}
	if err := validateLinuxRootPassword("12345678"); err == nil {
		t.Fatal("needs letter")
	}
	if err := validateLinuxRootPassword("bad:pass1"); err == nil {
		t.Fatal("colon forbidden")
	}
	if err := validateLinuxRootPassword("GoodPass1"); err != nil {
		t.Fatal(err)
	}
}

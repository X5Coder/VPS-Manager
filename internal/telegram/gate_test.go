package telegram

import (
	"testing"
	"time"
)

func TestOTPTTLIs20Minutes(t *testing.T) {
	if OTPTTL != 20*time.Minute {
		t.Fatalf("OTPTTL=%s", OTPTTL)
	}
}

func TestPendingPasswordOnePerson(t *testing.T) {
	dir := t.TempDir()
	g := NewGate(dir)
	token := "123456:abcdefghijklmnopqrstuvwxyz"
	if err := g.saveSecretsUnlocked(Secrets{BotToken: token, ChatID: "1", Locked: true}); err != nil {
		t.Fatal(err)
	}
	g.pending = &pendingOTP{
		TokenHash: tokenKey(token),
		CodeHash:  hashCode("424242"),
		ExpiresAt: time.Now().Add(20 * time.Minute),
	}
	if err := g.Verify(token, "000000"); err == nil {
		t.Fatal("wrong code must fail")
	}
	if g.pending == nil {
		t.Fatal("wrong code must not burn the password")
	}
	if err := g.Verify(token, "424242"); err != nil {
		t.Fatal(err)
	}
	if g.pending != nil {
		t.Fatal("success must consume the password")
	}
	if err := g.Verify(token, "424242"); err == nil {
		t.Fatal("second person must not reuse")
	}
}

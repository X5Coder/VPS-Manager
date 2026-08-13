package auth

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/x5coder/vps-rooms/internal/store"
)

const (
	KindOwner = "owner"
	KindRoom  = "room"
)

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func CreateSession(st *store.Store, kind, roomID string, hours int) (string, error) {
	tok, err := NewToken()
	if err != nil {
		return "", err
	}
	if hours <= 0 {
		hours = 24
	}
	err = st.SaveSession(store.Session{
		Token:     tok,
		Kind:      kind,
		RoomID:    roomID,
		ExpiresAt: time.Now().UTC().Add(time.Duration(hours) * time.Hour),
	})
	return tok, err
}

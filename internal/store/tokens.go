package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"
)

type APIToken struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	TokenHash   string `json:"-"`
	TokenPlain  string `json:"secret,omitempty"`
	TokenPrefix string `json:"token_prefix"`
	Mode        string `json:"mode"` // always both; kept for backups
	RoomID      string `json:"room_id"`
	CreatedAt   string `json:"created_at"`
	LastUsedAt  string `json:"last_used_at,omitempty"`
}

const tokenCols = `id,name,token_hash,COALESCE(token_plain,''),token_prefix,mode,COALESCE(room_id,''),created_at,last_used_at`

func scanAPIToken(s interface {
	Scan(dest ...any) error
}) (APIToken, error) {
	var t APIToken
	err := s.Scan(&t.ID, &t.Name, &t.TokenHash, &t.TokenPlain, &t.TokenPrefix, &t.Mode, &t.RoomID, &t.CreatedAt, &t.LastUsedAt)
	return t, err
}

func NormalizeTokenMode(mode string) string {
	return "both"
}

func TokenCanWrite(mode string) bool {
	return true
}

func HashAPIToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func NewAPITokenPlain() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "vm_" + hex.EncodeToString(b), nil
}

func (s *Store) CreateAPIToken(name, roomID string) (*APIToken, string, error) {
	name = trimLen(name, 64)
	if name == "" {
		name = "API token"
	}
	plain, err := NewAPITokenPlain()
	if err != nil {
		return nil, "", err
	}
	t := &APIToken{
		ID:          uuid.NewString(),
		Name:        name,
		TokenHash:   HashAPIToken(plain),
		TokenPlain:  plain,
		TokenPrefix: plain[:10] + "…",
		Mode:        "owner",
		RoomID:      "",
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	_, err = s.DB.Exec(`INSERT INTO api_tokens(id,name,token_hash,token_plain,token_prefix,mode,room_id,created_at,last_used_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, t.ID, t.Name, t.TokenHash, plain, t.TokenPrefix, t.Mode, t.RoomID, t.CreatedAt, "")
	if err != nil {
		return nil, "", err
	}
	return t, plain, nil
}

func (s *Store) ListAPITokens() ([]APIToken, error) {
	rows, err := s.DB.Query(`SELECT ` + tokenCols + ` FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetAPIToken(id string) (*APIToken, error) {
	t, err := scanAPIToken(s.DB.QueryRow(`SELECT `+tokenCols+` FROM api_tokens WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) GetAPITokenByName(name string) (*APIToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	t, err := scanAPIToken(s.DB.QueryRow(`SELECT `+tokenCols+` FROM api_tokens WHERE lower(name)=lower(?) ORDER BY created_at DESC LIMIT 1`, name))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) DeleteAPIToken(id string) error {
	_, err := s.DB.Exec(`DELETE FROM api_tokens WHERE id=?`, id)
	return err
}

func (s *Store) UpsertAPIToken(t APIToken) error {
	if t.TokenHash == "" && t.TokenPlain != "" {
		t.TokenHash = HashAPIToken(t.TokenPlain)
	}
	if t.TokenPrefix == "" && len(t.TokenPlain) >= 10 {
		t.TokenPrefix = t.TokenPlain[:10] + "…"
	}
	t.Mode = "both"
	if t.CreatedAt == "" {
		t.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := s.DB.Exec(`INSERT INTO api_tokens(id,name,token_hash,token_plain,token_prefix,mode,room_id,created_at,last_used_at)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name,
			token_hash=excluded.token_hash,
			token_plain=excluded.token_plain,
			token_prefix=excluded.token_prefix,
			mode=excluded.mode,
			room_id=excluded.room_id,
			last_used_at=excluded.last_used_at`,
		t.ID, t.Name, t.TokenHash, t.TokenPlain, t.TokenPrefix, t.Mode, t.RoomID, t.CreatedAt, t.LastUsedAt)
	return err
}

func (s *Store) LookupAPIToken(plain string) (*APIToken, error) {
	if plain == "" {
		return nil, nil
	}
	hash := HashAPIToken(plain)
	t, err := scanAPIToken(s.DB.QueryRow(`SELECT `+tokenCols+` FROM api_tokens WHERE token_hash=?`, hash))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = s.DB.Exec(`UPDATE api_tokens SET last_used_at=? WHERE id=?`, now, t.ID)
	t.LastUsedAt = now
	return &t, nil
}

func (s *Store) TotalQuotaBytes() (int64, error) {
	var n sql.NullInt64
	err := s.DB.QueryRow(`SELECT COALESCE(SUM(quota_bytes),0) FROM rooms`).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n.Int64, nil
}

func trimLen(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

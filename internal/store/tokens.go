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
	Mode        string `json:"mode"` // read | write | both
	CreatedAt   string `json:"created_at"`
	LastUsedAt  string `json:"last_used_at,omitempty"`
}

func NormalizeTokenMode(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	m = strings.ReplaceAll(m, " ", "")
	switch {
	case m == "both" || (strings.Contains(m, "read") && strings.Contains(m, "write")):
		return "both"
	case m == "write":
		return "write"
	default:
		return "read"
	}
}

func TokenCanWrite(mode string) bool {
	m := NormalizeTokenMode(mode)
	return m == "write" || m == "both"
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

func (s *Store) CreateAPIToken(name, mode string) (*APIToken, string, error) {
	name = trimLen(name, 64)
	if name == "" {
		name = "API token"
	}
	mode = NormalizeTokenMode(mode)
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
		Mode:        mode,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	_, err = s.DB.Exec(`INSERT INTO api_tokens(id,name,token_hash,token_plain,token_prefix,mode,created_at,last_used_at)
		VALUES(?,?,?,?,?,?,?,?)`, t.ID, t.Name, t.TokenHash, plain, t.TokenPrefix, t.Mode, t.CreatedAt, "")
	if err != nil {
		return nil, "", err
	}
	return t, plain, nil
}

func (s *Store) ListAPITokens() ([]APIToken, error) {
	rows, err := s.DB.Query(`SELECT id,name,token_hash,COALESCE(token_plain,''),token_prefix,mode,created_at,last_used_at FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.TokenHash, &t.TokenPlain, &t.TokenPrefix, &t.Mode, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetAPIToken(id string) (*APIToken, error) {
	var t APIToken
	err := s.DB.QueryRow(`SELECT id,name,token_hash,COALESCE(token_plain,''),token_prefix,mode,created_at,last_used_at FROM api_tokens WHERE id=?`, id).
		Scan(&t.ID, &t.Name, &t.TokenHash, &t.TokenPlain, &t.TokenPrefix, &t.Mode, &t.CreatedAt, &t.LastUsedAt)
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

// UpsertAPIToken restores an API token with its original secret (full backup).
func (s *Store) UpsertAPIToken(t APIToken) error {
	if t.TokenHash == "" && t.TokenPlain != "" {
		t.TokenHash = HashAPIToken(t.TokenPlain)
	}
	if t.TokenPrefix == "" && len(t.TokenPlain) >= 10 {
		t.TokenPrefix = t.TokenPlain[:10] + "…"
	}
	t.Mode = NormalizeTokenMode(t.Mode)
	if t.CreatedAt == "" {
		t.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := s.DB.Exec(`INSERT INTO api_tokens(id,name,token_hash,token_plain,token_prefix,mode,created_at,last_used_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name,
			token_hash=excluded.token_hash,
			token_plain=excluded.token_plain,
			token_prefix=excluded.token_prefix,
			mode=excluded.mode,
			last_used_at=excluded.last_used_at`,
		t.ID, t.Name, t.TokenHash, t.TokenPlain, t.TokenPrefix, t.Mode, t.CreatedAt, t.LastUsedAt)
	return err
}

func (s *Store) LookupAPIToken(plain string) (*APIToken, error) {
	if plain == "" {
		return nil, nil
	}
	hash := HashAPIToken(plain)
	var t APIToken
	err := s.DB.QueryRow(`SELECT id,name,token_hash,COALESCE(token_plain,''),token_prefix,mode,created_at,last_used_at FROM api_tokens WHERE token_hash=?`, hash).
		Scan(&t.ID, &t.Name, &t.TokenHash, &t.TokenPlain, &t.TokenPrefix, &t.Mode, &t.CreatedAt, &t.LastUsedAt)
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

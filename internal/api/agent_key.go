package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Owner-only recoverable copy of the single agent secret.
//
// The database keeps only the SHA-256 hash (what verifies Bearer tokens).
// This file lets the panel owner reveal/copy the current key on demand
// (key click, copy-full-docs). It lives under data/secrets (0700 dir,
// 0600 file), is (over)written on every regenerate/rotate, and is never
// logged or returned by any other endpoint.
func agentKeyPath(dataDir string) string {
	return filepath.Join(dataDir, "secrets", "agent_key")
}

// saveAgentKeySecret stores the secret before it becomes the active key.
// On failure the caller must abort before touching the database, so the
// file can never disagree with the active key.
func saveAgentKeySecret(dataDir, secret string) error {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets"), 0o700); err != nil {
		return err
	}
	return os.WriteFile(agentKeyPath(dataDir), []byte(secret+"\n"), 0o600)
}

func readAgentKeySecret(dataDir string) (string, error) {
	b, err := os.ReadFile(agentKeyPath(dataDir))
	if err != nil {
		return "", err
	}
	if s := strings.TrimSpace(string(b)); s != "" {
		return s, nil
	}
	return "", os.ErrInvalid
}

// removeAgentKeySecret drops the stored copy so reveal is impossible until
// the next regenerate (used when the last key is revoked or a rotate cannot
// be persisted — a missing file fails closed, a stale one would lie).
func removeAgentKeySecret(dataDir string) {
	_ = os.Remove(agentKeyPath(dataDir))
}

// handleAgentKey reveals the current single API key to the panel owner.
// 404 when there is no active key or no stored copy (keys predating this
// feature) — regenerating once stores it.
func (s *Server) handleAgentKey(w http.ResponseWriter, r *http.Request) {
	if s.requireOwner(w, r) == nil {
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method")
		return
	}
	if tokens, _ := s.Store.ListAgentTokens(); len(tokens) == 0 {
		writeErr(w, 404, "no active key")
		return
	}
	secret, err := readAgentKeySecret(s.Cfg.DataDir)
	if err != nil || secret == "" {
		writeErr(w, 404, "stored key unavailable — regenerate once to enable reveal")
		return
	}
	writeJSON(w, 200, map[string]any{"secret": secret})
}

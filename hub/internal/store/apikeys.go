package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

// APIKey is a sender credential. Only its sha256 is stored.
type APIKey struct {
	ID        int64
	Name      string
	CreatedAt time.Time
}

func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// CreateAPIKey mints a key and returns the plaintext, which is never retrievable again.
func (s *Store) CreateAPIKey(name string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	plaintext := "ck_" + base64.RawURLEncoding.EncodeToString(b)
	_, err := s.exec(`INSERT INTO api_keys(hash, name, created_at) VALUES (?, ?, ?)`,
		hashSecret(plaintext), name, unix(s.Now()))
	if err != nil {
		return "", err
	}
	return plaintext, nil
}

// LookupAPIKey finds an unrevoked key by its plaintext; ErrNotFound otherwise.
func (s *Store) LookupAPIKey(plaintext string) (APIKey, error) {
	if plaintext == "" {
		return APIKey{}, ErrNotFound
	}
	var k APIKey
	var created int64
	err := s.queryRow(`SELECT id, name, created_at FROM api_keys WHERE hash = ? AND revoked_at IS NULL`,
		hashSecret(plaintext)).Scan(&k.ID, &k.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, err
	}
	k.CreatedAt = fromUnix(created)
	return k, nil
}

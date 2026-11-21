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
	RevokedAt time.Time // zero while the key works
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

// ListAPIKeys returns every key, revoked or not, oldest first.
func (s *Store) ListAPIKeys() ([]APIKey, error) {
	rows, err := s.query(`SELECT id, name, created_at, revoked_at FROM api_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		var created int64
		var revoked sql.NullInt64
		if err := rows.Scan(&k.ID, &k.Name, &created, &revoked); err != nil {
			return nil, err
		}
		k.CreatedAt, k.RevokedAt = fromUnix(created), nullTime(revoked)
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeAPIKey stops a key from authenticating. It is idempotent (an already revoked key keeps its first revoked_at)
// and returns ErrNotFound for an id that does not exist.
func (s *Store) RevokeAPIKey(id int64) error {
	res, err := s.exec(`UPDATE api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, unix(s.Now()), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	var one int
	err = s.queryRow(`SELECT 1 FROM api_keys WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

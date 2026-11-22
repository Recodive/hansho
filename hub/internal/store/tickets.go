package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"
)

// CreateTicket mints a single-use pairing ticket (32 random bytes, base64url).
// Only its sha256 is stored.
func (s *Store) CreateTicket(userID string, ttl time.Duration) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	ticket := base64.RawURLEncoding.EncodeToString(b)
	_, err := s.exec(`INSERT INTO tickets(ticket, external_user_id, expires_at) VALUES (?, ?, ?)`,
		hashSecret(ticket), userID, unix(s.Now().Add(ttl)))
	if err != nil {
		return "", err
	}
	return ticket, nil
}

// ExpireTicketsForUser invalidates every ticket of a user, redeemed ones included: an unused one can no longer be
// redeemed, and a redeem already past ConsumeTicket is refused by CreatePairingWithDevice.
func (s *Store) ExpireTicketsForUser(userID string) error {
	_, err := s.exec(`UPDATE tickets SET expires_at = 0 WHERE external_user_id = ?`, userID)
	return err
}

// ConsumeTicket atomically redeems a ticket. Of any number of concurrent calls
// exactly one succeeds; the rest get ErrTicketUsed (or ErrTicketExpired /
// ErrNotFound).
func (s *Store) ConsumeTicket(ticket string, now time.Time) (string, error) {
	h := hashSecret(ticket)
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE tickets SET used_at = ? WHERE ticket = ? AND used_at IS NULL AND expires_at > ?`,
		unix(now), h, unix(now))
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	var userID string
	var expires int64
	var used sql.NullInt64
	err = tx.QueryRow(`SELECT external_user_id, expires_at, used_at FROM tickets WHERE ticket = ?`, h).Scan(&userID, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if n == 1 {
		return userID, tx.Commit()
	}
	if used.Valid {
		return "", ErrTicketUsed
	}
	return "", ErrTicketExpired
}

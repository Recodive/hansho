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

// TicketState is what a pairing ticket can still be used for.
type TicketState string

const (
	TicketValid   TicketState = "valid"
	TicketExpired TicketState = "expired"
	TicketUsed    TicketState = "used"
)

// TicketStatus reports a ticket's state at now without changing it, with the time it expires (ErrNotFound for an unknown ticket). It answers like ConsumeTicket would: a used ticket is used however its expiry stands
// (ExpireTicketsForUser zeroes that too), an unused one past its expiry is expired.
func (s *Store) TicketStatus(ticket string, now time.Time) (TicketState, time.Time, error) {
	var expires int64
	var used sql.NullInt64
	err := s.db.QueryRow(`SELECT expires_at, used_at FROM tickets WHERE ticket = ?`, hashSecret(ticket)).Scan(&expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrNotFound
	}
	if err != nil {
		return "", time.Time{}, err
	}
	exp := fromUnix(expires)
	switch {
	case used.Valid:
		return TicketUsed, exp, nil
	case expires <= unix(now):
		return TicketExpired, exp, nil
	}
	return TicketValid, exp, nil
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

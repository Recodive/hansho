package store

import "time"

// SeenNonce records nonce for ttl and reports whether it was already live.
func (s *Store) SeenNonce(nonce string, ttl time.Duration) (bool, error) {
	now := s.Now()
	// A new nonce, or one whose previous entry expired, is inserted/refreshed
	// (1 row affected). A live duplicate leaves the row alone (0 rows).
	res, err := s.exec(`INSERT INTO seen_nonces(nonce, expires_at) VALUES (?, ?)
		ON CONFLICT(nonce) DO UPDATE SET expires_at = excluded.expires_at
		WHERE seen_nonces.expires_at <= ?`, nonce, unix(now.Add(ttl)), unix(now))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return true, nil
	}
	// Opportunistic cleanup keeps the table bounded.
	_, _ = s.exec(`DELETE FROM seen_nonces WHERE expires_at <= ?`, unix(now))
	return false, nil
}

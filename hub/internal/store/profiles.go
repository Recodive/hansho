package store

import (
	"database/sql"
	"errors"
	"time"
)

// DisplayName returns what other people see for a user instead of their external user id, "" if none is set.
func (s *Store) DisplayName(userID string) (string, error) {
	var n string
	err := s.queryRow(`SELECT display_name FROM user_profiles WHERE external_user_id = ?`, userID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return n, err
}

// DisplayNames returns every set display name by external user id.
func (s *Store) DisplayNames() (map[string]string, error) {
	rows, err := s.query(`SELECT external_user_id, display_name FROM user_profiles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var u, n string
		if err := rows.Scan(&u, &n); err != nil {
			return nil, err
		}
		out[u] = n
	}
	return out, rows.Err()
}

// SetDisplayName sets a user's display name; "" clears it.
func (s *Store) SetDisplayName(userID, name string, now time.Time) error {
	if name == "" {
		_, err := s.exec(`DELETE FROM user_profiles WHERE external_user_id = ?`, userID)
		return err
	}
	_, err := s.exec(`INSERT INTO user_profiles(external_user_id, display_name, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(external_user_id) DO UPDATE SET display_name = excluded.display_name, updated_at = excluded.updated_at`,
		userID, name, unix(now))
	return err
}

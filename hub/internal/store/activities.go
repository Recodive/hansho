package store

import (
	"database/sql"
	"errors"
	"time"
)

// Activity is a Live Activity started by a sender.
type Activity struct {
	ID             string
	ExternalUserID string
	ChannelID      string
	TemplateID     string
	StartedAt      time.Time
	EndsAt         time.Time
	EndedAt        time.Time
	DataJSON       string
	Priority       int
	AlertJSON      string // "" = no alert
	UpdateSeq      int
}

// ActivityToken is a device's sealed Live Activity update token.
type ActivityToken struct {
	ActivityID   string
	DeviceID     string
	SealedUpdate string
	SentSeq      int // last update_seq pushed to this token
}

const activityCols = `id, external_user_id, channel_id, template_id, started_at, ends_at, ended_at, data_json, priority, alert_json, update_seq`

func scanActivity(sc interface{ Scan(...any) error }) (Activity, error) {
	var a Activity
	var started, ends int64
	var ended sql.NullInt64
	err := sc.Scan(&a.ID, &a.ExternalUserID, &a.ChannelID, &a.TemplateID, &started, &ends, &ended, &a.DataJSON, &a.Priority, &a.AlertJSON, &a.UpdateSeq)
	if err != nil {
		return a, err
	}
	a.StartedAt, a.EndsAt, a.EndedAt = fromUnix(started), fromUnix(ends), nullTime(ended)
	return a, nil
}

// CreateActivity inserts an activity.
func (s *Store) CreateActivity(a Activity) error {
	if a.DataJSON == "" {
		a.DataJSON = "null"
	}
	if a.Priority == 0 {
		a.Priority = 5
	}
	_, err := s.exec(`INSERT INTO activities(`+activityCols+`) VALUES (?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, 0)`,
		a.ID, a.ExternalUserID, a.ChannelID, a.TemplateID, unix(a.StartedAt), unix(a.EndsAt), a.DataJSON, a.Priority, a.AlertJSON)
	return err
}

// GetActivity returns an activity (ended or not).
func (s *Store) GetActivity(id string) (Activity, error) {
	a, err := scanActivity(s.queryRow(`SELECT `+activityCols+` FROM activities WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Activity{}, ErrNotFound
	}
	return a, err
}

// UpdateActivityState records the latest state of a running activity and
// returns the new update sequence number. ErrNotFound if it is missing or ended.
func (s *Store) UpdateActivityState(id, dataJSON string, priority int, alertJSON string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE activities SET data_json = ?, priority = ?, alert_json = ?, update_seq = update_seq + 1
		WHERE id = ? AND ended_at IS NULL`, dataJSON, priority, alertJSON, id)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	var seq int
	if err := tx.QueryRow(`SELECT update_seq FROM activities WHERE id = ?`, id).Scan(&seq); err != nil {
		return 0, err
	}
	return seq, tx.Commit()
}

// EndActivity marks an activity ended; changed is false if it already was.
func (s *Store) EndActivity(id string, now time.Time) (bool, error) {
	res, err := s.exec(`UPDATE activities SET ended_at = ? WHERE id = ? AND ended_at IS NULL`, unix(now), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ActivitiesDueToEnd lists running activities whose end time has passed.
func (s *Store) ActivitiesDueToEnd(now time.Time, limit int) ([]Activity, error) {
	rows, err := s.query(`SELECT `+activityCols+` FROM activities WHERE ended_at IS NULL AND ends_at <= ? ORDER BY ends_at LIMIT ?`, unix(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		a, err := scanActivity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpsertActivityToken registers (or replaces) a device's update token. If the
// sealed token is unchanged its sent_seq is kept, so re-registering does not
// cause a second delivery.
func (s *Store) UpsertActivityToken(activityID, deviceID, sealed string) (ActivityToken, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return ActivityToken{}, err
	}
	defer tx.Rollback()
	var cur ActivityToken
	err = tx.QueryRow(`SELECT activity_id, device_id, sealed_update, sent_seq FROM activity_tokens WHERE activity_id = ? AND device_id = ?`,
		activityID, deviceID).Scan(&cur.ActivityID, &cur.DeviceID, &cur.SealedUpdate, &cur.SentSeq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		cur = ActivityToken{ActivityID: activityID, DeviceID: deviceID, SealedUpdate: sealed}
	case err != nil:
		return ActivityToken{}, err
	case cur.SealedUpdate != sealed:
		cur.SealedUpdate, cur.SentSeq = sealed, 0
	}
	if _, err := tx.Exec(`INSERT INTO activity_tokens(activity_id, device_id, sealed_update, sent_seq) VALUES (?, ?, ?, ?)
		ON CONFLICT(activity_id, device_id) DO UPDATE SET sealed_update = excluded.sealed_update, sent_seq = excluded.sent_seq`,
		activityID, deviceID, cur.SealedUpdate, cur.SentSeq); err != nil {
		return ActivityToken{}, err
	}
	return cur, tx.Commit()
}

// ActivityTokens lists the update tokens of an activity.
func (s *Store) ActivityTokens(activityID string) ([]ActivityToken, error) {
	rows, err := s.query(`SELECT activity_id, device_id, sealed_update, sent_seq FROM activity_tokens WHERE activity_id = ? ORDER BY device_id`, activityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActivityToken
	for rows.Next() {
		var t ActivityToken
		if err := rows.Scan(&t.ActivityID, &t.DeviceID, &t.SealedUpdate, &t.SentSeq); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// MarkActivityTokenSent records that update seq reached a token.
func (s *Store) MarkActivityTokenSent(activityID, deviceID string, seq int) error {
	_, err := s.exec(`UPDATE activity_tokens SET sent_seq = ? WHERE activity_id = ? AND device_id = ? AND sent_seq < ?`,
		seq, activityID, deviceID, seq)
	return err
}

// DeleteActivityToken forgets a device's update token (e.g. after Unregistered).
func (s *Store) DeleteActivityToken(activityID, deviceID string) error {
	_, err := s.exec(`DELETE FROM activity_tokens WHERE activity_id = ? AND device_id = ?`, activityID, deviceID)
	return err
}

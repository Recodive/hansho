package store

import (
	"database/sql"
	"errors"
	"time"
)

// Message statuses.
const (
	MsgQueued        = "queued"
	MsgDelivered     = "delivered"
	MsgUndeliverable = "undeliverable"
	MsgPaging        = "paging"
	MsgAcked         = "acked"
	MsgExpired       = "expired"
)

// Delivery statuses.
const (
	DelQueued  = "queued"
	DelSent    = "sent"
	DelFailed  = "failed"
	DelSkipped = "skipped"
)

// Message is one message to one external user.
type Message struct {
	ID             string
	ExternalUserID string
	ChannelID      string
	Level          string
	Status         string
	PageIntervalS  int
	PageMaxRetries int
	Attempts       int // resends of a page so far
	NextAttemptAt  time.Time
	CreatedAt      time.Time
	AckedAt        time.Time
	CollapseID     string
	Sound          string // sound filename, "" = default
	PayloadWrapped []byte // plaintext JSON wrapped with the master key
}

// Delivery is the state of a message on one device.
type Delivery struct {
	MessageID string
	DeviceID  string
	APNsID    string
	Status    string
	Error     string
}

const messageCols = `id, external_user_id, channel_id, level, status, page_interval_s, page_max_retries, attempts,
	next_attempt_at, created_at, acked_at, collapse_id, sound, payload_wrapped`

func scanMessage(sc interface{ Scan(...any) error }) (Message, error) {
	var m Message
	var next, created int64
	var acked sql.NullInt64
	err := sc.Scan(&m.ID, &m.ExternalUserID, &m.ChannelID, &m.Level, &m.Status, &m.PageIntervalS, &m.PageMaxRetries,
		&m.Attempts, &next, &created, &acked, &m.CollapseID, &m.Sound, &m.PayloadWrapped)
	if err != nil {
		return m, err
	}
	m.NextAttemptAt, m.CreatedAt, m.AckedAt = fromUnix(next), fromUnix(created), nullTime(acked)
	return m, nil
}

// CreateMessage inserts a message with its initial deliveries atomically.
func (s *Store) CreateMessage(m Message, ds []Delivery) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO messages(`+messageCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ExternalUserID, m.ChannelID, m.Level, m.Status, m.PageIntervalS, m.PageMaxRetries, m.Attempts,
		unix(m.NextAttemptAt), unix(m.CreatedAt), nullUnix(m.AckedAt), m.CollapseID, m.Sound, m.PayloadWrapped)
	if err != nil {
		return err
	}
	for _, d := range ds {
		if _, err := tx.Exec(`INSERT INTO deliveries(message_id, device_id, apns_id, status, error) VALUES (?, ?, ?, ?, ?)`,
			m.ID, d.DeviceID, d.APNsID, d.Status, d.Error); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetMessage returns a message.
func (s *Store) GetMessage(id string) (Message, error) {
	m, err := scanMessage(s.queryRow(`SELECT `+messageCols+` FROM messages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	return m, err
}

func (s *Store) queryMessages(where string, args ...any) ([]Message, error) {
	rows, err := s.query(`SELECT `+messageCols+` FROM messages WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DueMessages returns messages in status whose next_attempt_at has passed, oldest first.
func (s *Store) DueMessages(status string, now time.Time, limit int) ([]Message, error) {
	return s.queryMessages(`status = ? AND next_attempt_at <= ? ORDER BY next_attempt_at, created_at, id LIMIT ?`,
		status, unix(now), limit)
}

// MessagesByUserStatus returns a user's messages in the given status.
func (s *Store) MessagesByUserStatus(userID, status string) ([]Message, error) {
	return s.queryMessages(`external_user_id = ? AND status = ? ORDER BY created_at, id`, userID, status)
}

// Deliveries lists a message's deliveries ordered by device id.
func (s *Store) Deliveries(messageID string) ([]Delivery, error) {
	rows, err := s.query(`SELECT message_id, device_id, apns_id, status, error FROM deliveries WHERE message_id = ? ORDER BY device_id`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.MessageID, &d.DeviceID, &d.APNsID, &d.Status, &d.Error); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpsertDelivery inserts or updates the delivery row of (message, device).
func (s *Store) UpsertDelivery(d Delivery) error {
	_, err := s.exec(`INSERT INTO deliveries(message_id, device_id, apns_id, status, error) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(message_id, device_id) DO UPDATE SET apns_id = excluded.apns_id, status = excluded.status, error = excluded.error`,
		d.MessageID, d.DeviceID, d.APNsID, d.Status, d.Error)
	return err
}

// TransitionMessage moves a message from one status to another and reports
// whether this call performed the transition (so side effects fire once).
func (s *Store) TransitionMessage(id, from, to string) (bool, error) {
	res, err := s.exec(`UPDATE messages SET status = ? WHERE id = ? AND status = ?`, to, id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ScheduleMessage sets when a message is next due.
func (s *Store) ScheduleMessage(id string, next time.Time) error {
	_, err := s.exec(`UPDATE messages SET next_attempt_at = ? WHERE id = ?`, unix(next), id)
	return err
}

// StartPaging moves a delivered page into the paging state with its first retry due at next.
func (s *Store) StartPaging(id string, next time.Time) (bool, error) {
	res, err := s.exec(`UPDATE messages SET status = ?, next_attempt_at = ? WHERE id = ? AND status = ?`,
		MsgPaging, unix(next), id, MsgQueued)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// BumpPageAttempt records one more resend of a paging message and schedules the next.
// It only applies while the message is still paging.
func (s *Store) BumpPageAttempt(id string, next time.Time) (bool, error) {
	res, err := s.exec(`UPDATE messages SET attempts = attempts + 1, next_attempt_at = ? WHERE id = ? AND status = ?`,
		unix(next), id, MsgPaging)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// AckMessage acknowledges a paging or delivered message. changed is false when
// it was neither (already acked, expired, ...), so callers act at most once.
func (s *Store) AckMessage(id string, now time.Time) (bool, error) {
	res, err := s.exec(`UPDATE messages SET status = ?, acked_at = ? WHERE id = ? AND status IN (?, ?)`,
		MsgAcked, unix(now), id, MsgPaging, MsgDelivered)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SoundFilename returns the on-device filename of a sound.
func (s *Store) SoundFilename(id string) (string, error) {
	var f string
	err := s.queryRow(`SELECT filename FROM sounds WHERE id = ?`, id).Scan(&f)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return f, err
}

// AckedAtForUser returns the acknowledgement time of each of ids that is an acknowledged message of userID. Ids that
// are unknown, unacknowledged or belong to another user are simply absent, so a caller learns nothing about them.
func (s *Store) AckedAtForUser(userID string, ids []string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{userID, MsgAcked}
	marks := make([]byte, 0, len(ids)*2)
	for i, id := range ids {
		if i > 0 {
			marks = append(marks, ',')
		}
		marks = append(marks, '?')
		args = append(args, id)
	}
	rows, err := s.query(`SELECT id, acked_at FROM messages
		WHERE external_user_id = ? AND status = ? AND acked_at IS NOT NULL AND id IN (`+string(marks)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var at int64
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = fromUnix(at)
	}
	return out, rows.Err()
}

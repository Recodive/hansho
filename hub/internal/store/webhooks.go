package store

import (
	"database/sql"
	"errors"
	"time"
)

// Webhook is one outbox row.
type Webhook struct {
	ID            int64
	Event         string
	PayloadJSON   []byte
	Attempts      int
	NextAttemptAt time.Time
	CreatedAt     time.Time
	DeliveredAt   time.Time
	DroppedAt     time.Time
}

// EnqueueWebhook appends an event to the outbox, due immediately.
func (s *Store) EnqueueWebhook(event string, payloadJSON []byte, now time.Time) (int64, error) {
	res, err := s.exec(`INSERT INTO webhook_outbox(event, payload_json, next_attempt_at, created_at) VALUES (?, ?, ?, ?)`,
		event, string(payloadJSON), unix(now), unix(now))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const webhookCols = `id, event, payload_json, attempts, next_attempt_at, created_at, delivered_at, dropped_at`

func scanWebhook(sc interface{ Scan(...any) error }) (Webhook, error) {
	var w Webhook
	var payload string
	var next, created int64
	var delivered, dropped sql.NullInt64
	if err := sc.Scan(&w.ID, &w.Event, &payload, &w.Attempts, &next, &created, &delivered, &dropped); err != nil {
		return w, err
	}
	w.PayloadJSON = []byte(payload)
	w.NextAttemptAt, w.CreatedAt = fromUnix(next), fromUnix(created)
	w.DeliveredAt, w.DroppedAt = nullTime(delivered), nullTime(dropped)
	return w, nil
}

// DueWebhooks returns undelivered, undropped rows whose next attempt is due, oldest first.
func (s *Store) DueWebhooks(now time.Time, limit int) ([]Webhook, error) {
	rows, err := s.query(`SELECT `+webhookCols+` FROM webhook_outbox
		WHERE delivered_at IS NULL AND dropped_at IS NULL AND next_attempt_at <= ? ORDER BY id LIMIT ?`, unix(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// GetWebhook returns one outbox row.
func (s *Store) GetWebhook(id int64) (Webhook, error) {
	w, err := scanWebhook(s.queryRow(`SELECT `+webhookCols+` FROM webhook_outbox WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Webhook{}, ErrNotFound
	}
	return w, err
}

// ListWebhooks returns every outbox row of an event ("" = all), oldest first.
func (s *Store) ListWebhooks(event string) ([]Webhook, error) {
	rows, err := s.query(`SELECT `+webhookCols+` FROM webhook_outbox WHERE ? = '' OR event = ? ORDER BY id`, event, event)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// PruneWebhooks deletes finished (delivered or dropped) outbox rows created before cutoff and returns how many.
func (s *Store) PruneWebhooks(cutoff time.Time) (int64, error) {
	res, err := s.exec(`DELETE FROM webhook_outbox WHERE (delivered_at IS NOT NULL OR dropped_at IS NOT NULL) AND created_at < ?`, unix(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MarkWebhookDelivered records a successful delivery.
func (s *Store) MarkWebhookDelivered(id int64, now time.Time) error {
	_, err := s.exec(`UPDATE webhook_outbox SET delivered_at = ?, attempts = attempts + 1 WHERE id = ?`, unix(now), id)
	return err
}

// RescheduleWebhook records a failed attempt and sets the next due time.
func (s *Store) RescheduleWebhook(id int64, next time.Time) error {
	_, err := s.exec(`UPDATE webhook_outbox SET attempts = attempts + 1, next_attempt_at = ? WHERE id = ?`, unix(next), id)
	return err
}

// DropWebhook gives up on a row after its final failed attempt.
func (s *Store) DropWebhook(id int64, now time.Time) error {
	_, err := s.exec(`UPDATE webhook_outbox SET attempts = attempts + 1, dropped_at = ? WHERE id = ?`, unix(now), id)
	return err
}

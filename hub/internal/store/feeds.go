package store

import (
	"database/sql"
	"errors"
	"time"
)

// Feed is widget data published by a sender, for one user or for all.
type Feed struct {
	ID             string
	ExternalUserID string // "" = feed for all paired users
	TemplateID     string
	Title          string // "" = unnamed, the app shows the id
	DataJSON       string // compact JSON, signed byte for byte
	UpdatedAt      time.Time
}

// UpsertFeed creates or replaces the feed (id, user).
func (s *Store) UpsertFeed(f Feed) error {
	var user any
	if f.ExternalUserID != "" {
		user = f.ExternalUserID
	}
	var title any
	if f.Title != "" {
		title = f.Title
	}
	_, err := s.exec(`INSERT INTO feeds(id, external_user_id, template_id, title, data_json, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id, COALESCE(external_user_id, '')) DO UPDATE SET
		template_id = excluded.template_id, title = excluded.title, data_json = excluded.data_json, updated_at = excluded.updated_at`,
		f.ID, user, f.TemplateID, title, f.DataJSON, unix(f.UpdatedAt))
	return err
}

// GetFeedForUser returns the user's own feed with that id, else the global one.
func (s *Store) GetFeedForUser(id, userID string) (Feed, error) {
	var f Feed
	var user sql.NullString
	var updated int64
	err := s.queryRow(`SELECT id, external_user_id, template_id, data_json, updated_at FROM feeds
		WHERE id = ? AND (external_user_id = ? OR external_user_id IS NULL)
		ORDER BY external_user_id IS NULL LIMIT 1`, id, userID).Scan(&f.ID, &user, &f.TemplateID, &f.DataJSON, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Feed{}, ErrNotFound
	}
	if err != nil {
		return Feed{}, err
	}
	f.ExternalUserID, f.UpdatedAt = user.String, fromUnix(updated)
	return f, nil
}

// FeedListing is one entry of ListFeedsForUser; the data stays behind GetFeedForUser.
type FeedListing struct {
	ID        string
	Title     string
	UpdatedAt time.Time
}

// ListFeedsForUser returns the feeds GetFeedForUser would resolve for the user, one per id, sorted by id: the
// user's own plus the global ones, the user's own winning over a global feed with the same id.
func (s *Store) ListFeedsForUser(userID string) ([]FeedListing, error) {
	rows, err := s.query(`SELECT id, title, updated_at FROM feeds
		WHERE external_user_id = ? OR external_user_id IS NULL
		ORDER BY id, external_user_id IS NULL`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FeedListing
	for rows.Next() {
		var l FeedListing
		var title sql.NullString
		var updated int64
		if err := rows.Scan(&l.ID, &title, &updated); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].ID == l.ID {
			continue // the user's own row sorts first
		}
		l.Title, l.UpdatedAt = title.String, fromUnix(updated)
		out = append(out, l)
	}
	return out, rows.Err()
}

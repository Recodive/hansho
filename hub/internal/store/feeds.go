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
	Title          string    // "" = unnamed, the app shows the id
	ChannelID      string    // "" = no channel; the feed then has no audience to inherit
	Audience       *Audience // the feed's own audience; nil = inherit its channel's
	Effective      Audience  // read side only: Audience, else the channel's, else everyone
	DataJSON       string    // compact JSON, signed byte for byte
	UpdatedAt      time.Time
}

// feedAudienceCols selects a feed's own audience and the audience it ends up with; the feeds table is aliased f.
const feedAudienceCols = `f.audience_json, COALESCE(f.audience_json, c.audience_json, '')`

const feedFrom = `feeds f LEFT JOIN channels c ON c.id = f.channel_id`

// UpsertFeed creates or replaces the feed (id, user).
func (s *Store) UpsertFeed(f Feed) error {
	var user, title, channel, aud any
	if f.ExternalUserID != "" {
		user = f.ExternalUserID
	}
	if f.Title != "" {
		title = f.Title
	}
	if f.ChannelID != "" {
		channel = f.ChannelID
	}
	if f.Audience != nil {
		aud = f.Audience.encode()
	}
	_, err := s.exec(`INSERT INTO feeds(id, external_user_id, template_id, title, channel_id, audience_json, data_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id, COALESCE(external_user_id, '')) DO UPDATE SET
		template_id = excluded.template_id, title = excluded.title, channel_id = excluded.channel_id,
		audience_json = excluded.audience_json, data_json = excluded.data_json, updated_at = excluded.updated_at`,
		f.ID, user, f.TemplateID, title, channel, aud, f.DataJSON, unix(f.UpdatedAt))
	return err
}

type rowScanner interface{ Scan(...any) error }

func scanFeed(sc rowScanner) (Feed, error) {
	var f Feed
	var user, title, channel, own sql.NullString
	var eff string
	var updated int64
	if err := sc.Scan(&f.ID, &user, &f.TemplateID, &title, &channel, &own, &eff, &f.DataJSON, &updated); err != nil {
		return Feed{}, err
	}
	f.ExternalUserID, f.Title, f.ChannelID, f.UpdatedAt = user.String, title.String, channel.String, fromUnix(updated)
	if own.Valid {
		a := decodeAudience(own.String)
		f.Audience = &a
	}
	f.Effective = decodeAudience(eff)
	return f, nil
}

const feedCols = `f.id, f.external_user_id, f.template_id, f.title, f.channel_id, ` + feedAudienceCols + `, f.data_json, f.updated_at`

// GetFeed returns the exact feed (id, user); user "" is the global one.
func (s *Store) GetFeed(id, userID string) (Feed, error) {
	var user any
	if userID != "" {
		user = userID
	}
	f, err := scanFeed(s.queryRow(`SELECT `+feedCols+` FROM `+feedFrom+`
		WHERE f.id = ? AND COALESCE(f.external_user_id, '') = COALESCE(?, '')`, id, user))
	if errors.Is(err, sql.ErrNoRows) {
		return Feed{}, ErrNotFound
	}
	return f, err
}

// GetFeedForUser returns the user's own feed with that id, else the global one.
func (s *Store) GetFeedForUser(id, userID string) (Feed, error) {
	f, err := scanFeed(s.queryRow(`SELECT `+feedCols+` FROM `+feedFrom+`
		WHERE f.id = ? AND (f.external_user_id = ? OR f.external_user_id IS NULL)
		ORDER BY f.external_user_id IS NULL LIMIT 1`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return Feed{}, ErrNotFound
	}
	return f, err
}

// FeedListing is one entry of ListFeedsForUser; the data stays behind GetFeedForUser.
type FeedListing struct {
	ID        string
	Title     string
	Audience  Audience // the audience that applies to the feed (its own, else its channel's)
	UpdatedAt time.Time
}

// ListFeedsForUser returns the feeds GetFeedForUser would resolve for the user, one per id, sorted by id: the
// user's own plus the global ones, the user's own winning over a global feed with the same id. It does not filter by
// audience; callers check Audience.
func (s *Store) ListFeedsForUser(userID string) ([]FeedListing, error) {
	rows, err := s.query(`SELECT f.id, f.title, COALESCE(f.audience_json, c.audience_json, ''), f.updated_at FROM `+feedFrom+`
		WHERE f.external_user_id = ? OR f.external_user_id IS NULL
		ORDER BY f.id, f.external_user_id IS NULL`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FeedListing
	for rows.Next() {
		var l FeedListing
		var title sql.NullString
		var aud string
		var updated int64
		if err := rows.Scan(&l.ID, &title, &aud, &updated); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].ID == l.ID {
			continue // the user's own row sorts first
		}
		l.Title, l.Audience, l.UpdatedAt = title.String, decodeAudience(aud), fromUnix(updated)
		out = append(out, l)
	}
	return out, rows.Err()
}

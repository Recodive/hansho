package store

import (
	"database/sql"
	"errors"
	"time"
)

// ErrVersionConflict means a template version did not increase.
var ErrVersionConflict = errors.New("store: template version must increase")

// Channel is a sender-defined message channel.
type Channel struct {
	ID           string
	Name         string
	DefaultLevel string
	MaxLevel     string
	Sound        string // sound id, "" = default
	TemplateID   string
	Thread       string
	ActionsJSON  string // JSON array
	DeletedAt    time.Time
}

const channelCols = `id, name, default_level, max_level, sound, template_id, thread, actions_json, deleted_at`

func scanChannel(sc interface{ Scan(...any) error }) (Channel, error) {
	var c Channel
	var del sql.NullInt64
	if err := sc.Scan(&c.ID, &c.Name, &c.DefaultLevel, &c.MaxLevel, &c.Sound, &c.TemplateID, &c.Thread, &c.ActionsJSON, &del); err != nil {
		return c, err
	}
	c.DeletedAt = nullTime(del)
	return c, nil
}

// UpsertChannel creates or replaces a channel and undeletes it.
func (s *Store) UpsertChannel(c Channel) error {
	if c.ActionsJSON == "" {
		c.ActionsJSON = "[]"
	}
	_, err := s.exec(`INSERT INTO channels(`+channelCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, default_level = excluded.default_level,
		max_level = excluded.max_level, sound = excluded.sound, template_id = excluded.template_id,
		thread = excluded.thread, actions_json = excluded.actions_json, deleted_at = NULL`,
		c.ID, c.Name, c.DefaultLevel, c.MaxLevel, c.Sound, c.TemplateID, c.Thread, c.ActionsJSON)
	return err
}

// GetChannel returns a channel, deleted or not (check DeletedAt).
func (s *Store) GetChannel(id string) (Channel, error) {
	c, err := scanChannel(s.queryRow(`SELECT `+channelCols+` FROM channels WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	return c, err
}

// ListChannels returns non-deleted channels ordered by id.
func (s *Store) ListChannels() ([]Channel, error) {
	rows, err := s.query(`SELECT ` + channelCols + ` FROM channels WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteChannel soft-deletes a channel; false if it does not exist or was already deleted.
func (s *Store) DeleteChannel(id string, now time.Time) (bool, error) {
	res, err := s.exec(`UPDATE channels SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, unix(now), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Template is a versioned UI template.
type Template struct {
	ID      string
	Version int
	Body    []byte
}

// PutTemplate stores a template; the version must be greater than the stored one.
func (s *Store) PutTemplate(id string, version int, body []byte) error {
	res, err := s.exec(`INSERT INTO templates(id, version, body_json) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET version = excluded.version, body_json = excluded.body_json
		WHERE excluded.version > templates.version`, id, version, string(body))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrVersionConflict
	}
	return nil
}

// GetTemplate returns a template.
func (s *Store) GetTemplate(id string) (Template, error) {
	var t Template
	var body string
	err := s.queryRow(`SELECT id, version, body_json FROM templates WHERE id = ?`, id).Scan(&t.ID, &t.Version, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return Template{}, ErrNotFound
	}
	t.Body = []byte(body)
	return t, err
}

// ListTemplates returns all templates ordered by id.
func (s *Store) ListTemplates() ([]Template, error) {
	rows, err := s.query(`SELECT id, version, body_json FROM templates ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Template
	for rows.Next() {
		var t Template
		var body string
		if err := rows.Scan(&t.ID, &t.Version, &body); err != nil {
			return nil, err
		}
		t.Body = []byte(body)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Sound is an uploaded notification sound.
type Sound struct {
	ID          string
	Filename    string
	ContentType string
	Data        []byte
	SHA256      string
}

// PutSound stores or replaces a sound.
func (s *Store) PutSound(snd Sound) error {
	_, err := s.exec(`INSERT INTO sounds(id, filename, content_type, data, sha256) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET filename = excluded.filename, content_type = excluded.content_type,
		data = excluded.data, sha256 = excluded.sha256`, snd.ID, snd.Filename, snd.ContentType, snd.Data, snd.SHA256)
	return err
}

// GetSound returns a sound including its bytes.
func (s *Store) GetSound(id string) (Sound, error) {
	var snd Sound
	err := s.queryRow(`SELECT id, filename, content_type, data, sha256 FROM sounds WHERE id = ?`, id).
		Scan(&snd.ID, &snd.Filename, &snd.ContentType, &snd.Data, &snd.SHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return Sound{}, ErrNotFound
	}
	return snd, err
}

// ListSounds returns sound metadata (Data left nil) ordered by id.
func (s *Store) ListSounds() ([]Sound, error) {
	rows, err := s.query(`SELECT id, filename, content_type, sha256 FROM sounds ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sound
	for rows.Next() {
		var snd Sound
		if err := rows.Scan(&snd.ID, &snd.Filename, &snd.ContentType, &snd.SHA256); err != nil {
			return nil, err
		}
		out = append(out, snd)
	}
	return out, rows.Err()
}

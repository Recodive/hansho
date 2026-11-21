package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
)

// Audience modes.
const (
	AudienceEveryone = "everyone"
	AudienceOnly     = "only"
	AudienceExcept   = "except"
)

// Audience says who may use a channel or feed: everyone, only the listed users and groups, or everyone except them.
// The zero value is everyone.
type Audience struct {
	Mode   string   `json:"mode"`
	Users  []string `json:"users"`
	Groups []string `json:"groups"`
}

// Normalized returns the audience with its lists non-nil, so it always encodes as arrays.
func (a Audience) Normalized() Audience {
	if a.Mode == "" {
		a.Mode = AudienceEveryone
	}
	if a.Users == nil {
		a.Users = []string{}
	}
	if a.Groups == nil {
		a.Groups = []string{}
	}
	return a
}

// encode returns the stored form: "" for everyone.
func (a Audience) encode() string {
	if a.Mode == "" || a.Mode == AudienceEveryone {
		return ""
	}
	b, _ := json.Marshal(a.Normalized())
	return string(b)
}

func decodeAudience(s string) Audience {
	var a Audience
	if s != "" {
		if err := json.Unmarshal([]byte(s), &a); err != nil {
			// Unreadable means unknown: Allows refuses it rather than opening the channel.
			return Audience{Mode: "invalid"}
		}
	}
	return a.Normalized()
}

// Access answers whether one user may use a channel or feed. It holds the user's groups as read when it was made, so a
// caller makes one per request and never keeps it: an audience or group change then applies to the next request.
type Access struct {
	user   string
	groups map[string]bool
}

// AccessFor reads the groups of a user.
func (s *Store) AccessFor(user string) (Access, error) {
	rows, err := s.query(`SELECT group_id FROM group_members WHERE external_user_id = ?`, user)
	if err != nil {
		return Access{}, err
	}
	defer rows.Close()
	a := Access{user: user, groups: map[string]bool{}}
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return Access{}, err
		}
		a.groups[g] = true
	}
	return a, rows.Err()
}

// Allows is the one rule for access: the user is in the audience. Everything that hides, delivers or refuses
// per audience (sync, send, activities, actions, acks, feeds, the delivery worker) asks it.
func (a Access) Allows(aud Audience) bool {
	switch aud.Mode {
	case "", AudienceEveryone:
		return true
	case AudienceOnly:
		return a.listed(aud)
	case AudienceExcept:
		return !a.listed(aud)
	}
	return false
}

func (a Access) listed(aud Audience) bool {
	for _, u := range aud.Users {
		if u == a.user {
			return true
		}
	}
	for _, g := range aud.Groups {
		if a.groups[g] {
			return true
		}
	}
	return false
}

// CanAccessChannel reports whether the user may use the channel (a deleted channel keeps its audience).
func (s *Store) CanAccessChannel(user, channelID string) (bool, error) {
	ch, err := s.GetChannel(channelID)
	if err != nil {
		return false, err
	}
	acc, err := s.AccessFor(user)
	if err != nil {
		return false, err
	}
	return acc.Allows(ch.Audience), nil
}

// ActiveUsers lists the external user ids that have a non-revoked pairing.
func (s *Store) ActiveUsers() ([]string, error) {
	rows, err := s.query(`SELECT DISTINCT external_user_id FROM pairings WHERE revoked_at IS NULL ORDER BY external_user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// PutGroup creates a group or replaces all its members.
func (s *Store) PutGroup(id string, members []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO groups(id, created_at) VALUES (?, ?) ON CONFLICT(id) DO NOTHING`, id, unix(s.Now())); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM group_members WHERE group_id = ?`, id); err != nil {
		return err
	}
	for _, m := range members {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO group_members(group_id, external_user_id) VALUES (?, ?)`, id, m); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetGroup returns the members of a group, sorted.
func (s *Store) GetGroup(id string) ([]string, error) {
	var one int
	if err := s.queryRow(`SELECT 1 FROM groups WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	rows, err := s.query(`SELECT external_user_id FROM group_members WHERE group_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	sort.Strings(out)
	return out, rows.Err()
}

// DeleteGroup removes a group and its members; false if it does not exist. An audience that still names it
// simply matches nobody through it.
func (s *Store) DeleteGroup(id string) (bool, error) {
	res, err := s.exec(`DELETE FROM groups WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

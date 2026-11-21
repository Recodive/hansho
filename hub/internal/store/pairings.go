package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Pairing links an external user to a device group sharing one pairing key.
type Pairing struct {
	ID             string
	ExternalUserID string
	KeyWrapped     []byte // pairing key wrapped with the hub master key
	CreatedAt      time.Time
	RevokedAt      time.Time // zero when active
}

// Device is one app install within a pairing.
type Device struct {
	ID                string
	PairingID         string
	SignPub           []byte // ECDSA P-256, x9.63 uncompressed
	Platform, Name    string
	SealedAlert       string
	SealedBackground  string
	SealedPushToStart string
	SealedWidgets     string
	CreatedAt         time.Time
	RevokedAt         time.Time
}

// DeviceWithPairing is a device joined with its pairing.
type DeviceWithPairing struct {
	Device
	Pairing Pairing
}

// Tokens is the full set of relay-sealed delivery tokens of a device.
type Tokens struct{ Alert, Background, PushToStart, Widgets string }

// TokenKind names one sealed-token column.
type TokenKind string

const (
	TokenAlert       TokenKind = "alert"
	TokenBackground  TokenKind = "background"
	TokenPushToStart TokenKind = "pushtostart"
	TokenWidgets     TokenKind = "widgets"
)

func (k TokenKind) column() (string, bool) {
	switch k {
	case TokenAlert:
		return "sealed_alert", true
	case TokenBackground:
		return "sealed_background", true
	case TokenPushToStart:
		return "sealed_pushtostart", true
	case TokenWidgets:
		return "sealed_widgets", true
	}
	return "", false
}

// CreatePairing inserts a pairing.
func (s *Store) CreatePairing(p Pairing) error {
	_, err := s.exec(`INSERT INTO pairings(id, external_user_id, key_wrapped, created_at) VALUES (?, ?, ?, ?)`,
		p.ID, p.ExternalUserID, p.KeyWrapped, unix(p.CreatedAt))
	return err
}

// AddDevice inserts a device (and its tokens) into an existing pairing.
func (s *Store) AddDevice(d Device) error {
	_, err := s.exec(`INSERT INTO devices(id, pairing_id, sign_pub, platform, name,
		sealed_alert, sealed_background, sealed_pushtostart, sealed_widgets, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.PairingID, d.SignPub, d.Platform, d.Name,
		d.SealedAlert, d.SealedBackground, d.SealedPushToStart, d.SealedWidgets, unix(d.CreatedAt))
	return err
}

// UpsertDevice registers a device in an existing pairing, or refreshes the registration it already has: one active
// device per pairing and signing key. It returns the device's ID (the existing one when the device re-joined) and
// whether a new device was created.
func (s *Store) UpsertDevice(d Device) (id string, created bool, err error) {
	err = s.queryRow(`INSERT INTO devices(id, pairing_id, sign_pub, platform, name,
		sealed_alert, sealed_background, sealed_pushtostart, sealed_widgets, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pairing_id, sign_pub) WHERE revoked_at IS NULL DO UPDATE SET
			platform = excluded.platform, name = excluded.name,
			sealed_alert = excluded.sealed_alert, sealed_background = excluded.sealed_background,
			sealed_pushtostart = excluded.sealed_pushtostart, sealed_widgets = excluded.sealed_widgets
		RETURNING id`,
		d.ID, d.PairingID, d.SignPub, d.Platform, d.Name,
		d.SealedAlert, d.SealedBackground, d.SealedPushToStart, d.SealedWidgets, unix(d.CreatedAt)).Scan(&id)
	if err != nil {
		return "", false, err
	}
	return id, id == d.ID, nil
}

// CreatePairingWithDevice inserts a pairing and its first device atomically.
func (s *Store) CreatePairingWithDevice(p Pairing, d Device) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO pairings(id, external_user_id, key_wrapped, created_at) VALUES (?, ?, ?, ?)`,
		p.ID, p.ExternalUserID, p.KeyWrapped, unix(p.CreatedAt)); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO devices(id, pairing_id, sign_pub, platform, name,
		sealed_alert, sealed_background, sealed_pushtostart, sealed_widgets, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.PairingID, d.SignPub, d.Platform, d.Name,
		d.SealedAlert, d.SealedBackground, d.SealedPushToStart, d.SealedWidgets, unix(d.CreatedAt)); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateDeviceTokens replaces all sealed tokens of a device.
func (s *Store) UpdateDeviceTokens(deviceID string, t Tokens) error {
	res, err := s.exec(`UPDATE devices SET sealed_alert = ?, sealed_background = ?, sealed_pushtostart = ?, sealed_widgets = ?
		WHERE id = ?`, t.Alert, t.Background, t.PushToStart, t.Widgets, deviceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearDeviceToken blanks one sealed token (e.g. after the relay reports Unregistered).
func (s *Store) ClearDeviceToken(deviceID string, kind TokenKind) error {
	col, ok := kind.column()
	if !ok {
		return fmt.Errorf("store: unknown token kind %q", kind)
	}
	_, err := s.exec(`UPDATE devices SET `+col+` = '' WHERE id = ?`, deviceID)
	return err
}

const deviceCols = `d.id, d.pairing_id, d.sign_pub, d.platform, d.name,
	d.sealed_alert, d.sealed_background, d.sealed_pushtostart, d.sealed_widgets, d.created_at, d.revoked_at,
	p.id, p.external_user_id, p.key_wrapped, p.created_at, p.revoked_at`

func scanDevice(sc interface{ Scan(...any) error }) (DeviceWithPairing, error) {
	var d DeviceWithPairing
	var dc, pc int64
	var dr, pr sql.NullInt64
	err := sc.Scan(&d.ID, &d.PairingID, &d.SignPub, &d.Platform, &d.Name,
		&d.SealedAlert, &d.SealedBackground, &d.SealedPushToStart, &d.SealedWidgets, &dc, &dr,
		&d.Pairing.ID, &d.Pairing.ExternalUserID, &d.Pairing.KeyWrapped, &pc, &pr)
	if err != nil {
		return d, err
	}
	d.CreatedAt, d.RevokedAt = fromUnix(dc), nullTime(dr)
	d.Pairing.CreatedAt, d.Pairing.RevokedAt = fromUnix(pc), nullTime(pr)
	return d, nil
}

func (s *Store) queryDevices(where string, args ...any) ([]DeviceWithPairing, error) {
	rows, err := s.query(`SELECT `+deviceCols+` FROM devices d JOIN pairings p ON p.id = d.pairing_id WHERE `+where+` ORDER BY d.created_at, d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceWithPairing
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ActiveDevicesForUser lists non-revoked devices of non-revoked pairings of a user.
func (s *Store) ActiveDevicesForUser(userID string) ([]DeviceWithPairing, error) {
	return s.queryDevices(`p.external_user_id = ? AND p.revoked_at IS NULL AND d.revoked_at IS NULL`, userID)
}

// ActiveDevicesForPairing lists non-revoked devices of one non-revoked pairing.
func (s *Store) ActiveDevicesForPairing(pairingID string) ([]DeviceWithPairing, error) {
	return s.queryDevices(`p.id = ? AND p.revoked_at IS NULL AND d.revoked_at IS NULL`, pairingID)
}

// AllActiveDevices lists every non-revoked device (used for hub-wide feeds).
func (s *Store) AllActiveDevices() ([]DeviceWithPairing, error) {
	return s.queryDevices(`p.revoked_at IS NULL AND d.revoked_at IS NULL`)
}

// GetDevice returns a device (revoked or not) joined with its pairing.
func (s *Store) GetDevice(id string) (DeviceWithPairing, error) {
	ds, err := s.queryDevices(`d.id = ?`, id)
	if err != nil {
		return DeviceWithPairing{}, err
	}
	if len(ds) == 0 {
		return DeviceWithPairing{}, ErrNotFound
	}
	return ds[0], nil
}

// GetPairing returns a pairing (revoked or not).
func (s *Store) GetPairing(id string) (Pairing, error) {
	var p Pairing
	var c int64
	var r sql.NullInt64
	err := s.queryRow(`SELECT id, external_user_id, key_wrapped, created_at, revoked_at FROM pairings WHERE id = ?`, id).
		Scan(&p.ID, &p.ExternalUserID, &p.KeyWrapped, &c, &r)
	if errors.Is(err, sql.ErrNoRows) {
		return Pairing{}, ErrNotFound
	}
	if err != nil {
		return Pairing{}, err
	}
	p.CreatedAt, p.RevokedAt = fromUnix(c), nullTime(r)
	return p, nil
}

// ActivePairingsForUser lists a user's non-revoked pairings.
func (s *Store) ActivePairingsForUser(userID string) ([]Pairing, error) {
	rows, err := s.query(`SELECT id, external_user_id, key_wrapped, created_at FROM pairings
		WHERE external_user_id = ? AND revoked_at IS NULL ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pairing
	for rows.Next() {
		var p Pairing
		var c int64
		if err := rows.Scan(&p.ID, &p.ExternalUserID, &p.KeyWrapped, &c); err != nil {
			return nil, err
		}
		p.CreatedAt = fromUnix(c)
		out = append(out, p)
	}
	return out, rows.Err()
}

// RevokePairing marks a pairing revoked. changed is false when it already was
// (or does not exist), so callers emit side effects only once.
func (s *Store) RevokePairing(id string) (changed bool, err error) {
	res, err := s.exec(`UPDATE pairings SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, unix(s.Now()), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RevokeDevice marks a single device revoked.
func (s *Store) RevokeDevice(id string) error {
	_, err := s.exec(`UPDATE devices SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, unix(s.Now()), id)
	return err
}

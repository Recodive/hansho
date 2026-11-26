package api

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/recodive/hansho/hub/internal/brand"
	"github.com/recodive/hansho/hub/internal/crypto"
	"github.com/recodive/hansho/hub/internal/store"
	"github.com/recodive/hansho/hub/internal/webhook"
)

const maxFieldLen = 256

// POST /api/v1/pairing-tickets
func (s *server) createTicket(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, maxJSONBody)
	if !ok {
		return
	}
	var req struct {
		ExternalUserID string  `json:"external_user_id"`
		DisplayName    *string `json:"display_name"`
	}
	if !decodeJSON(w, body, &req) {
		return
	}
	if !validExternalUserID(req.ExternalUserID) {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "external_user_id", "required, at most 256 bytes")
		return
	}
	if req.DisplayName != nil { // optional: set along with pairing so the person has a name from the start
		name, ok := cleanDisplayName(*req.DisplayName)
		if !ok {
			writeFieldErr(w, http.StatusBadRequest, "invalid_request", "display_name", displayNameHelp)
			return
		}
		if err := s.Store.SetDisplayName(req.ExternalUserID, name, s.Now()); err != nil {
			s.internal(w, "set display name failed", err)
			return
		}
	}
	ticket, qr, expires, err := s.issueTicket(req.ExternalUserID)
	if err != nil {
		s.internal(w, "create ticket failed", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"ticket":     ticket,
		"qr":         qr,
		"expires_at": expires.UTC().Format(time.RFC3339),
	})
}

// validExternalUserID reports whether id may name a user: 1 to maxFieldLen bytes.
func validExternalUserID(id string) bool { return id != "" && len(id) <= maxFieldLen }

// issueTicket mints a pairing ticket for a user and returns it with the pairing link (the "qr" the sender shows) and
// the time it expires. The admin UI shares it with createTicket.
func (s *server) issueTicket(user string) (ticket, qr string, expires time.Time, err error) {
	ticket, err = s.Store.CreateTicket(user, ticketTTL)
	if err != nil {
		return "", "", time.Time{}, err
	}
	qr = brand.Scheme + "://pair?hub=" + url.QueryEscape(s.Cfg.PublicURL) + "&t=" + ticket + "&fp=" + s.fp
	return ticket, qr, s.Now().Add(ticketTTL), nil
}

type deviceReq struct {
	SignPub  string `json:"sign_pub"`
	KAPub    string `json:"ka_pub"`
	Platform string `json:"platform"`
	Name     string `json:"name"`
}

type tokensReq struct {
	Alert       string `json:"alert"`
	Background  string `json:"background"`
	PushToStart string `json:"pushtostart"`
	Widgets     string `json:"widgets"`
}

func (t tokensReq) store() store.Tokens {
	return store.Tokens{Alert: t.Alert, Background: t.Background, PushToStart: t.PushToStart, Widgets: t.Widgets}
}

// validate checks the tokens; alert and background are mandatory.
func (t tokensReq) validate(w http.ResponseWriter) bool {
	for _, f := range []struct{ name, v string }{{"tokens.alert", t.Alert}, {"tokens.background", t.Background}} {
		if f.v == "" {
			writeFieldErr(w, http.StatusBadRequest, "invalid_request", f.name, "required")
			return false
		}
	}
	for _, f := range []struct{ name, v string }{{"alert", t.Alert}, {"background", t.Background}, {"pushtostart", t.PushToStart}, {"widgets", t.Widgets}} {
		if len(f.v) > 4096 {
			writeFieldErr(w, http.StatusBadRequest, "invalid_request", "tokens."+f.name, "too long")
			return false
		}
	}
	return true
}

// parsedDevice is a validated deviceReq.
type parsedDevice struct {
	signPub  []byte
	kaPub    *ecdh.PublicKey // nil when not supplied (add-device path)
	platform string
	name     string
}

func validateDevice(w http.ResponseWriter, d deviceReq, needKA bool) (parsedDevice, bool) {
	var p parsedDevice
	sp, err := decodeB64(d.SignPub)
	if err == nil {
		var pk, perr = parseSignPub(sp)
		if perr != nil || pk == nil {
			err = errors.New("bad key")
		} else if b, berr := pk.Bytes(); berr == nil {
			sp = b // normalise to 65-byte x9.63
		}
	}
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "device.sign_pub", "must be a base64 P-256 public key")
		return p, false
	}
	p.signPub = sp
	if d.KAPub != "" || needKA {
		kb, err := decodeB64(d.KAPub)
		if err == nil {
			p.kaPub, err = ecdh.P256().NewPublicKey(kb)
		}
		if err != nil {
			writeFieldErr(w, http.StatusBadRequest, "invalid_request", "device.ka_pub", "must be a base64 x9.63 P-256 public key")
			return p, false
		}
	}
	if d.Platform == "" || len(d.Platform) > 32 {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "device.platform", "required, at most 32 bytes")
		return p, false
	}
	if d.Name == "" || len(d.Name) > maxFieldLen {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "device.name", "required, at most 256 bytes")
		return p, false
	}
	p.platform, p.name = d.Platform, d.Name
	return p, true
}

// GET /device/v1/pairing-tickets/{ticket} tells the app whether the ticket of a pairing link is still good, so it can
// say a link has expired before the user is asked to approve it. Unauthenticated like the redeem it precedes: the
// ticket is the secret (256 random bits, stored hashed), the answer holds nothing the holder does not know, and it
// never consumes or extends the ticket. Like the redeem there is no rate limit; guessing a ticket is not feasible.
func (s *server) ticketStatus(w http.ResponseWriter, r *http.Request) {
	state, expires, err := s.Store.TicketStatus(r.PathValue("ticket"), s.Now())
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "unknown_ticket")
		return
	}
	if err != nil {
		s.internal(w, "ticket status failed", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	res := map[string]string{"state": string(state)}
	if state != store.TicketUsed {
		res["expires_at"] = expires.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, res)
}

// POST /device/v1/pairings
func (s *server) redeemTicket(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, maxJSONBody)
	if !ok {
		return
	}
	var req struct {
		Ticket string    `json:"ticket"`
		Device deviceReq `json:"device"`
		Tokens tokensReq `json:"tokens"`
	}
	if !decodeJSON(w, body, &req) {
		return
	}
	if req.Ticket == "" {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "ticket", "required")
		return
	}
	// Validate everything before consuming the ticket so a malformed request
	// does not burn it.
	dev, ok := validateDevice(w, req.Device, true)
	if !ok || !req.Tokens.validate(w) {
		return
	}

	userID, err := s.Store.ConsumeTicket(req.Ticket, s.Now())
	switch {
	case err == nil:
	case errors.Is(err, store.ErrTicketUsed):
		writeErr(w, http.StatusGone, "ticket_used")
		return
	case errors.Is(err, store.ErrTicketExpired):
		writeErr(w, http.StatusGone, "ticket_expired")
		return
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "unknown_ticket")
		return
	default:
		s.internal(w, "consume ticket failed", err)
		return
	}

	pairingKey := make([]byte, 32)
	if _, err := rand.Read(pairingKey); err != nil {
		s.internal(w, "rand failed", err)
		return
	}
	pairingID, deviceID := store.NewID("pair_"), store.NewID("dev_")
	wrapped, err := crypto.WrapKey(dev.kaPub, pairingID, pairingKey)
	if err != nil {
		s.internal(w, "wrap key failed", err)
		return
	}
	atRest, err := crypto.WrapAtRest(s.Cfg.MasterKey, pairingKey)
	if err != nil {
		s.internal(w, "wrap at rest failed", err)
		return
	}
	now := s.Now()
	err = s.Store.CreatePairingWithDevice(req.Ticket,
		store.Pairing{ID: pairingID, ExternalUserID: userID, KeyWrapped: atRest, CreatedAt: now},
		store.Device{
			ID: deviceID, PairingID: pairingID, SignPub: dev.signPub, Platform: dev.platform, Name: dev.name,
			SealedAlert: req.Tokens.Alert, SealedBackground: req.Tokens.Background,
			SealedPushToStart: req.Tokens.PushToStart, SealedWidgets: req.Tokens.Widgets, CreatedAt: now,
		})
	if errors.Is(err, store.ErrTicketExpired) { // the user was unpaired while this redeem was in flight
		writeErr(w, http.StatusGone, "ticket_expired")
		return
	}
	if err != nil {
		s.internal(w, "create pairing failed", err)
		return
	}
	s.emit(r.Context(), webhook.EventPairingCreated, map[string]string{
		"pairing_id": pairingID, "external_user_id": userID, "device_name": dev.name,
	})
	s.log.Info("pairing created", "pairing_id", pairingID, "device_id", deviceID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"pairing_id":  pairingID,
		"device_id":   deviceID,
		"wrapped_key": b64url.EncodeToString(wrapped),
		"hub":         s.hubInfo(),
	})
}

// POST /device/v1/pairings/{id}/devices, authenticated by the pairing proof.
func (s *server) addDevice(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, maxJSONBody)
	if !ok {
		return
	}
	ts, proof := r.Header.Get(headerTimestamp), r.Header.Get(headerPairingProof)
	if ts == "" || proof == "" || !s.checkTimestamp(ts) {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	pairing, err := s.Store.GetPairing(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && !pairing.RevokedAt.IsZero()) {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err != nil {
		s.internal(w, "pairing lookup failed", err)
		return
	}
	key, err := crypto.UnwrapAtRest(s.Cfg.MasterKey, pairing.KeyWrapped)
	if err != nil {
		s.internal(w, "unwrap pairing key failed", err)
		return
	}
	if !crypto.VerifyPairingProof(key, crypto.CanonicalString(ts, r.Method, r.URL.Path, body), proof) {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !s.recordNonce(w, "proof:"+proof) {
		return
	}

	var req struct {
		Device deviceReq `json:"device"`
		Tokens tokensReq `json:"tokens"`
	}
	if !decodeJSON(w, body, &req) {
		return
	}
	dev, ok := validateDevice(w, req.Device, false)
	if !ok || !req.Tokens.validate(w) {
		return
	}
	// The same device (same signing key) joining again, e.g. after an app reinstall or a racing second join, updates
	// its registration instead of becoming a second device that every message would be sent to.
	deviceID, created, err := s.Store.UpsertDevice(store.Device{
		ID: store.NewID("dev_"), PairingID: pairing.ID, SignPub: dev.signPub, Platform: dev.platform, Name: dev.name,
		SealedAlert: req.Tokens.Alert, SealedBackground: req.Tokens.Background,
		SealedPushToStart: req.Tokens.PushToStart, SealedWidgets: req.Tokens.Widgets, CreatedAt: s.Now(),
	})
	if errors.Is(err, store.ErrTooManyDevices) {
		writeErr(w, http.StatusConflict, "too_many_devices")
		return
	}
	if err != nil {
		s.internal(w, "add device failed", err)
		return
	}
	s.log.Info("device joined", "pairing_id", pairing.ID, "device_id", deviceID, "new", created)
	writeJSON(w, http.StatusCreated, map[string]string{"device_id": deviceID})
}

// PUT /device/v1/devices/{id}/tokens
func (s *server) updateTokens(w http.ResponseWriter, r *http.Request, dev store.DeviceWithPairing, body []byte) {
	if r.PathValue("id") != dev.ID {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	var t tokensReq
	if !decodeJSON(w, body, &t) {
		return
	}
	if !t.validate(w) {
		return
	}
	if err := s.Store.UpdateDeviceTokens(dev.ID, t.store()); err != nil {
		s.internal(w, "update tokens failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /device/v1/pairings/{id}
func (s *server) unpairDevice(w http.ResponseWriter, r *http.Request, dev store.DeviceWithPairing, _ []byte) {
	if r.PathValue("id") != dev.PairingID {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if _, err := s.revokePairing(r.Context(), dev.Pairing, "device"); err != nil {
		s.internal(w, "revoke pairing failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/users/{external_user_id}/pairings
func (s *server) unpairUser(w http.ResponseWriter, r *http.Request) {
	n, err := s.kickUser(r.Context(), r.PathValue("external_user_id"))
	if err != nil {
		s.internal(w, "unpair user failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"revoked": n})
}

// kickUser ends every pairing of a user as the sender (the admin UI's Remove shares it with unpairUser) and returns
// how many it revoked.
func (s *server) kickUser(ctx context.Context, user string) (int, error) {
	// Expire the user's tickets first: an outstanding one must not pair again after this, and a redeem in flight
	// either committed its pairing already (listed and revoked below) or is refused when it tries.
	if err := s.Store.ExpireTicketsForUser(user); err != nil {
		return 0, fmt.Errorf("expire tickets: %w", err)
	}
	pairings, err := s.Store.ActivePairingsForUser(user)
	if err != nil {
		return 0, fmt.Errorf("list pairings: %w", err)
	}
	n := 0
	for _, p := range pairings {
		changed, err := s.revokePairing(ctx, p, "sender")
		if err != nil {
			return n, fmt.Errorf("revoke pairing: %w", err)
		}
		if changed {
			n++
		}
	}
	return n, nil
}

// revokePairing revokes a pairing locally, at the relay (best effort, per
// device alert token) and reports it to the sender. It does nothing if the
// pairing was already revoked.
func (s *server) revokePairing(ctx context.Context, p store.Pairing, by string) (bool, error) {
	devices, err := s.Store.ActiveDevicesForPairing(p.ID)
	if err != nil {
		return false, err
	}
	changed, err := s.Store.RevokePairing(p.ID, by)
	if err != nil || !changed {
		return false, err
	}
	for _, d := range devices {
		if d.SealedAlert == "" {
			continue
		}
		if err := s.Relay.Revoke(ctx, d.SealedAlert); err != nil {
			// Hub-side revocation already stops fan-out; the relay list is defense in depth.
			s.log.Warn("relay revoke failed", "pairing_id", p.ID, "device_id", d.ID, "err", err)
		}
	}
	s.emit(ctx, webhook.EventPairingRevoked, map[string]string{
		"pairing_id": p.ID, "external_user_id": p.ExternalUserID, "by": by,
	})
	s.log.Info("pairing revoked", "pairing_id", p.ID, "by", by)
	return true, nil
}

// emit queues a webhook; failures are logged, never surfaced to the caller.
func (s *server) emit(ctx context.Context, event string, payload any) {
	if err := s.Webhooks.Emit(ctx, event, payload); err != nil {
		s.log.Error("webhook emit failed", "event", event, "err", err)
	}
}

package api

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/recodive/hansho/hub/internal/store"
)

// The title is only a label in the app's feed picker, so it stays short.
const maxFeedTitleLen = 64

// PUT /api/v1/feeds/{id}
func (s *server) putFeed(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "id", "must match [A-Za-z0-9][A-Za-z0-9_-]{0,63}")
		return
	}
	body, ok := readBody(w, r, maxJSONBody)
	if !ok {
		return
	}
	var req struct {
		ExternalUserID string          `json:"external_user_id"`
		TemplateID     string          `json:"template_id"`
		Title          string          `json:"title"`
		Channel        string          `json:"channel"`
		Audience       *store.Audience `json:"audience"`
		Data           json.RawMessage `json:"data"`
	}
	if !decodeJSON(w, body, &req) {
		return
	}
	bad := func(field, msg string) { writeFieldErr(w, http.StatusBadRequest, "invalid_request", field, msg) }
	switch {
	case !presentRaw(req.Data):
		bad("data", "required")
		return
	case len(req.ExternalUserID) > maxFieldLen:
		bad("external_user_id", "at most 256 bytes")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if len(req.Title) > maxFeedTitleLen {
		bad("title", "at most 64 bytes")
		return
	}
	// template_id is optional: without one the app draws the data with its automatic layout (gauge, value, list or
	// status grid, chosen from the data's shape). A named template must exist.
	if req.TemplateID != "" {
		if _, err := s.Store.GetTemplate(req.TemplateID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				bad("template_id", "unknown template")
			} else {
				s.internal(w, "template lookup failed", err)
			}
			return
		}
	}
	// channel is optional: a feed inherits the audience of its channel unless it sets its own audience. Leaving
	// both out keeps the stored values.
	if req.Channel != "" {
		if ch, err := s.Store.GetChannel(req.Channel); err != nil || !ch.DeletedAt.IsZero() {
			if err == nil || errors.Is(err, store.ErrNotFound) {
				bad("channel", "unknown channel")
			} else {
				s.internal(w, "channel lookup failed", err)
			}
			return
		}
	}
	f := store.Feed{
		ID: id, ExternalUserID: req.ExternalUserID, TemplateID: req.TemplateID, Title: req.Title, ChannelID: req.Channel,
		DataJSON: compactJSON(req.Data), UpdatedAt: s.Now(),
	}
	if req.Audience != nil {
		a, field, msg := checkAudience(*req.Audience, "audience")
		if field != "" {
			bad(field, msg)
			return
		}
		f.Audience = &a
	} else if req.Channel == "" {
		// Leaving both out keeps what is stored, so an old client republishing data never widens access.
		if old, err := s.Store.GetFeed(id, f.ExternalUserID); err == nil {
			f.ChannelID, f.Audience = old.ChannelID, old.Audience
		}
	}
	if err := s.Store.UpsertFeed(f); err != nil {
		s.internal(w, "store feed failed", err)
		return
	}
	stored, err := s.Store.GetFeed(id, f.ExternalUserID)
	if err != nil {
		s.internal(w, "feed lookup failed", err)
		return
	}

	var devices []store.DeviceWithPairing
	if f.ExternalUserID == "" {
		devices, err = s.Store.AllActiveDevices()
	} else {
		devices, err = s.Store.ActiveDevicesForUser(f.ExternalUserID)
	}
	if err != nil {
		s.internal(w, "list devices failed", err)
		return
	}
	// Only people who can use the feed are told it changed.
	if devices, err = s.devicesInAudience(devices, stored.Effective); err != nil {
		s.internal(w, "access check failed", err)
		return
	}
	n, err := s.dw.ReloadWidgets(r.Context(), devices)
	if err != nil {
		s.log.Error("widget reload failed", "feed_id", id, "err", err)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"feed_id": id, "devices": n})
}

// GET /api/v1/feeds/{id}[?external_user_id=]
//
// The feed's settings, without its data. Without external_user_id it is the global feed with that id.
func (s *server) getFeedMeta(w http.ResponseWriter, r *http.Request) {
	user := r.URL.Query().Get("external_user_id")
	f, err := s.Store.GetFeed(r.PathValue("id"), user)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "unknown_feed")
		return
	}
	if err != nil {
		s.internal(w, "feed lookup failed", err)
		return
	}
	out := map[string]any{"feed_id": f.ID, "template_id": f.TemplateID, "updated_at": f.UpdatedAt.Unix()}
	if f.ExternalUserID != "" {
		out["external_user_id"] = f.ExternalUserID
	}
	if f.Title != "" {
		out["title"] = f.Title
	}
	if f.ChannelID != "" {
		out["channel"] = f.ChannelID
	}
	if f.Audience != nil {
		out["audience"] = f.Audience.Normalized()
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /device/v1/feeds
//
// The feeds this device can fetch with GET /device/v1/feeds/{id}: its pairing's own and the global ones that its
// user may access, one entry per id. A listing only; the signed data still comes from the per-feed route.
func (s *server) listFeeds(w http.ResponseWriter, r *http.Request, dev store.DeviceWithPairing, _ []byte) {
	list, err := s.Store.ListFeedsForUser(dev.Pairing.ExternalUserID)
	if err != nil {
		s.internal(w, "feed list failed", err)
		return
	}
	acc, err := s.Store.AccessFor(dev.Pairing.ExternalUserID)
	if err != nil {
		s.internal(w, "access check failed", err)
		return
	}
	type entry struct {
		FeedID    string `json:"feed_id"`
		Title     string `json:"title,omitempty"`
		UpdatedAt int64  `json:"updated_at"`
	}
	feeds := make([]entry, 0, len(list))
	for _, l := range list {
		if !acc.Allows(l.Audience) {
			continue
		}
		feeds = append(feeds, entry{FeedID: l.ID, Title: l.Title, UpdatedAt: l.UpdatedAt.Unix()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"feeds": feeds})
}

// GET /device/v1/feeds/{id}
//
// sig = Ed25519(hub key, feed_id "\n" updated_at_unix "\n" hex(sha256(data))),
// where data is the exact bytes of the "data" member of the response.
func (s *server) getFeed(w http.ResponseWriter, r *http.Request, dev store.DeviceWithPairing, _ []byte) {
	f, err := s.Store.GetFeedForUser(r.PathValue("id"), dev.Pairing.ExternalUserID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "unknown_feed")
		return
	}
	if err != nil {
		s.internal(w, "feed lookup failed", err)
		return
	}
	acc, err := s.Store.AccessFor(dev.Pairing.ExternalUserID)
	if err != nil {
		s.internal(w, "access check failed", err)
		return
	}
	if !acc.Allows(f.Effective) {
		noAccess(w)
		return
	}
	sum := sha256.Sum256([]byte(f.DataJSON))
	msg := f.ID + "\n" + strconv.FormatInt(f.UpdatedAt.Unix(), 10) + "\n" + hex.EncodeToString(sum[:])
	sig := ed25519.Sign(s.Key, []byte(msg))
	writeJSON(w, http.StatusOK, map[string]any{
		"feed_id": f.ID, "template_id": f.TemplateID, "data": json.RawMessage(f.DataJSON),
		"updated_at": f.UpdatedAt.Unix(), "sig": b64url.EncodeToString(sig),
	})
}

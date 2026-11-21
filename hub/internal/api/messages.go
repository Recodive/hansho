package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/recodive/hansho/hub/internal/delivery"
	"github.com/recodive/hansho/hub/internal/level"
	"github.com/recodive/hansho/hub/internal/store"
)

const maxCollapseIDLen = 64 // APNs limit

// maxRecipients bounds external_user_ids of one send.
const maxRecipients = 1000

// POST /api/v1/messages
//
// The target is exactly one of external_user_id (one user), external_user_ids (several) or to_audience (every paired
// user in the channel's audience, resolved now). A user outside the channel's audience gets the recipient status
// "no_access" and no message; the others are delivered. The response lists one entry per recipient in "recipients"; for
// an external_user_id send it also carries that recipient's message at the top level, as before.
func (s *server) sendMessage(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, maxJSONBody)
	if !ok {
		return
	}
	var req struct {
		ExternalUserID  string          `json:"external_user_id"`
		ExternalUserIDs []string        `json:"external_user_ids"`
		ToAudience      bool            `json:"to_audience"`
		Channel         string          `json:"channel"`
		Level           *string         `json:"level"`
		Title           string          `json:"title"`
		Subtitle        string          `json:"subtitle"`
		Body            string          `json:"body"`
		ImageURL        string          `json:"image_url"`
		TemplateID      string          `json:"template_id"`
		Data            json.RawMessage `json:"data"`
		Sound           string          `json:"sound"`
		Actions         json.RawMessage `json:"actions"`
		CollapseID      string          `json:"collapse_id"`
		Page            *struct {
			IntervalS  *int `json:"interval_s"`
			MaxRetries *int `json:"max_retries"`
		} `json:"page"`
	}
	if !decodeJSON(w, body, &req) {
		return
	}
	bad := func(field, msg string) { writeFieldErr(w, http.StatusBadRequest, "invalid_request", field, msg) }
	targets := 0
	for _, set := range []bool{req.ExternalUserID != "", len(req.ExternalUserIDs) > 0, req.ToAudience} {
		if set {
			targets++
		}
	}
	switch {
	case targets == 0:
		bad("external_user_id", "required (or external_user_ids, or to_audience)")
		return
	case targets > 1:
		bad("external_user_ids", "give only one of external_user_id, external_user_ids and to_audience")
		return
	case len(req.ExternalUserID) > maxFieldLen:
		bad("external_user_id", "at most 256 bytes")
		return
	case len(req.ExternalUserIDs) > maxRecipients:
		bad("external_user_ids", "at most 1000 entries")
		return
	}
	for i, u := range req.ExternalUserIDs {
		if u == "" || len(u) > maxFieldLen {
			bad("external_user_ids["+strconv.Itoa(i)+"]", "required, at most 256 bytes")
			return
		}
	}
	for _, f := range []struct{ name, v string }{
		{"channel", req.Channel}, {"title", req.Title}, {"body", req.Body},
	} {
		if f.v == "" {
			bad(f.name, "required")
			return
		}
	}
	if req.Level != nil && !level.Valid(*req.Level) {
		bad("level", "must be one of passive, active, time-sensitive, critical, page")
		return
	}
	if len(req.CollapseID) > maxCollapseIDLen {
		bad("collapse_id", "at most 64 bytes")
		return
	}

	ch, err := s.Store.GetChannel(req.Channel)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !ch.DeletedAt.IsZero()) {
		writeErr(w, http.StatusNotFound, "unknown_channel")
		return
	}
	if err != nil {
		s.internal(w, "channel lookup failed", err)
		return
	}

	templateID := req.TemplateID
	if templateID == "" {
		templateID = ch.TemplateID
	} else if _, err := s.Store.GetTemplate(templateID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			bad("template_id", "unknown template")
		} else {
			s.internal(w, "template lookup failed", err)
		}
		return
	}
	soundID := req.Sound
	if soundID == "" {
		soundID = ch.Sound
	}
	var soundFile string
	if soundID != "" {
		soundFile, err = s.Store.SoundFilename(soundID)
		if errors.Is(err, store.ErrNotFound) {
			bad("sound", "unknown sound")
			return
		}
		if err != nil {
			s.internal(w, "sound lookup failed", err)
			return
		}
	}
	actions := json.RawMessage(ch.ActionsJSON)
	if len(req.Actions) > 0 && string(req.Actions) != "null" {
		norm, field, msg := checkActions(req.Actions)
		if field != "" {
			bad(field, msg)
			return
		}
		actions = json.RawMessage(norm)
	}
	if string(actions) == "[]" || string(actions) == "" {
		actions = nil
	}
	var data json.RawMessage
	if len(req.Data) > 0 && string(req.Data) != "null" {
		data = req.Data
	}

	lvl := delivery.EffectiveLevel(req.Level, ch)
	in := delivery.EnqueueInput{
		Channel: ch, Level: lvl,
		Plaintext: delivery.Plaintext{
			Title: req.Title, Subtitle: req.Subtitle, Body: req.Body, ImageURL: req.ImageURL,
			TemplateID: templateID, Data: data, Actions: actions, Channel: ch.ID, Level: lvl,
			Sound: soundFile, Page: lvl == level.Page,
		},
		CollapseID: req.CollapseID, Sound: soundFile,
	}
	if lvl == level.Page && req.Page != nil {
		if iv := req.Page.IntervalS; iv != nil {
			if *iv < 1 || *iv > 86400 {
				bad("page.interval_s", "must be between 1 and 86400")
				return
			}
			in.PageIntervalS = *iv
		}
		if mr := req.Page.MaxRetries; mr != nil {
			if *mr < 0 || *mr > 100 {
				bad("page.max_retries", "must be between 0 and 100")
				return
			}
			in.PageMaxRetries = mr
		}
	}

	var users []string
	switch {
	case req.ExternalUserID != "":
		users = []string{req.ExternalUserID}
	case req.ToAudience:
		if users, err = s.Store.ActiveUsers(); err != nil {
			s.internal(w, "list users failed", err)
			return
		}
	default:
		users = dedupe(req.ExternalUserIDs)
	}

	type recipient struct {
		ExternalUserID string         `json:"external_user_id"`
		MessageID      string         `json:"message_id,omitempty"`
		Status         string         `json:"status"`
		Devices        *int           `json:"devices,omitempty"`
		Deliveries     []deliveryView `json:"deliveries,omitempty"`
	}
	recipients := make([]recipient, 0, len(users))
	for _, user := range users {
		acc, err := s.Store.AccessFor(user)
		if err != nil {
			s.internal(w, "access check failed", err)
			return
		}
		if !acc.Allows(ch.Audience) {
			if req.ToAudience {
				continue // not a recipient to begin with
			}
			recipients = append(recipients, recipient{ExternalUserID: user, Status: statusNoAccess})
			continue
		}
		in.ExternalUserID = user
		res, err := delivery.Enqueue(r.Context(), s.Store, s.Webhooks, s.Cfg, s.Key, s.Now, in)
		if errors.Is(err, delivery.ErrPayloadTooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large") // the size does not depend on the user
			return
		}
		if err != nil {
			s.internal(w, "enqueue failed", err)
			return
		}
		dels := make([]deliveryView, 0, len(res.Deliveries))
		for _, d := range res.Deliveries {
			dels = append(dels, deliveryView{DeviceID: d.DeviceID, Status: d.Status})
		}
		n := len(dels)
		recipients = append(recipients, recipient{
			ExternalUserID: user, MessageID: res.Message.ID, Status: res.Message.Status, Devices: &n, Deliveries: dels,
		})
	}
	out := map[string]any{"level": lvl, "recipients": recipients}
	if req.ExternalUserID != "" {
		// The single-user form keeps its original fields. Without access there is no message: message_id is "".
		r0 := recipients[0]
		out["message_id"], out["status"], out["devices"], out["deliveries"] = "", r0.Status, 0, []deliveryView{}
		if r0.MessageID != "" {
			out["message_id"], out["devices"], out["deliveries"] = r0.MessageID, *r0.Devices, r0.Deliveries
		}
	}
	writeJSON(w, http.StatusAccepted, out)
}

// statusNoAccess is the recipient status of a user outside the channel's audience.
const statusNoAccess = "no_access"

type deliveryView struct {
	DeviceID string `json:"device_id"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// GET /api/v1/messages/{id}
func (s *server) getMessage(w http.ResponseWriter, r *http.Request) {
	m, err := s.Store.GetMessage(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "unknown_message")
		return
	}
	if err != nil {
		s.internal(w, "message lookup failed", err)
		return
	}
	dels, err := s.Store.Deliveries(m.ID)
	if err != nil {
		s.internal(w, "deliveries lookup failed", err)
		return
	}
	views := make([]deliveryView, 0, len(dels))
	for _, d := range dels {
		views = append(views, deliveryView{DeviceID: d.DeviceID, Status: d.Status, Error: d.Error})
	}
	out := map[string]any{
		"message_id": m.ID, "status": m.Status, "level": m.Level, "attempts": m.Attempts, "deliveries": views,
	}
	if !m.AckedAt.IsZero() {
		out["acked_at"] = m.AckedAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, out)
}

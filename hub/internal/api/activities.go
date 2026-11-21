package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/recodive/hansho/hub/internal/delivery"
	"github.com/recodive/hansho/hub/internal/store"
)

// presentRaw reports whether a JSON field was supplied and is not null.
func presentRaw(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && !bytes.Equal(t, []byte("null"))
}

func compactJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}

// parseActivityAlert validates an optional alert object and returns it with its stored form.
func parseActivityAlert(raw json.RawMessage) (alert *delivery.ActivityAlert, stored string, ok bool) {
	if !presentRaw(raw) {
		return nil, "", true
	}
	var a delivery.ActivityAlert
	if json.Unmarshal(raw, &a) != nil || (a.Title == "" && a.Body == "") {
		return nil, "", false
	}
	b, _ := json.Marshal(a)
	return &a, string(b), true
}

// mapContentStateErr writes the response for a content-state failure.
func (s *server) contentStateErr(w http.ResponseWriter, err error) {
	if errors.Is(err, delivery.ErrContentStateTooLarge) {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large")
		return
	}
	writeFieldErr(w, http.StatusBadRequest, "invalid_request", "data", "invalid content-state data")
}

// POST /api/v1/activities
func (s *server) startActivity(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, maxJSONBody)
	if !ok {
		return
	}
	var req struct {
		ExternalUserID string          `json:"external_user_id"`
		Channel        string          `json:"channel"`
		TemplateID     string          `json:"template_id"`
		Data           json.RawMessage `json:"data"`
		Alert          json.RawMessage `json:"alert"`
	}
	if !decodeJSON(w, body, &req) {
		return
	}
	bad := func(field, msg string) { writeFieldErr(w, http.StatusBadRequest, "invalid_request", field, msg) }
	switch {
	case req.ExternalUserID == "" || len(req.ExternalUserID) > maxFieldLen:
		bad("external_user_id", "required, at most 256 bytes")
		return
	case req.Channel == "":
		bad("channel", "required")
		return
	case req.TemplateID == "":
		bad("template_id", "required")
		return
	case !presentRaw(req.Data):
		bad("data", "required")
		return
	}
	alert, alertJSON, ok := parseActivityAlert(req.Alert)
	if !ok {
		bad("alert", "must be an object with title and/or body")
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
	if _, err := s.Store.GetTemplate(req.TemplateID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			bad("template_id", "unknown template")
		} else {
			s.internal(w, "template lookup failed", err)
		}
		return
	}
	if !s.channelAccess(w, req.ExternalUserID, ch.ID) {
		return
	}
	if err := s.dw.CheckContentState(req.TemplateID, req.Data); err != nil {
		s.contentStateErr(w, err)
		return
	}
	now := s.Now()
	a := store.Activity{
		ID: store.NewID("act_"), ExternalUserID: req.ExternalUserID, ChannelID: ch.ID, TemplateID: req.TemplateID,
		StartedAt: now, EndsAt: now.Add(delivery.ActivityLifetime), DataJSON: compactJSON(req.Data), Priority: 5, AlertJSON: alertJSON,
	}
	if err := s.Store.CreateActivity(a); err != nil {
		s.internal(w, "create activity failed", err)
		return
	}
	devices, err := s.dw.StartActivity(r.Context(), a, alert)
	if err != nil {
		s.log.Error("push-to-start failed", "activity_id", a.ID, "err", err)
	}
	s.log.Info("activity started", "activity_id", a.ID, "devices", devices)
	writeJSON(w, http.StatusCreated, map[string]any{"activity_id": a.ID, "devices": devices})
}

// activityOr404 loads an activity or writes the error response.
func (s *server) activityOr404(w http.ResponseWriter, id string) (store.Activity, bool) {
	a, err := s.Store.GetActivity(id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "unknown_activity")
		return a, false
	}
	if err != nil {
		s.internal(w, "activity lookup failed", err)
		return a, false
	}
	return a, true
}

// PATCH /api/v1/activities/{id}
func (s *server) updateActivity(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, maxJSONBody)
	if !ok {
		return
	}
	var req struct {
		Data     json.RawMessage `json:"data"`
		Priority *int            `json:"priority"`
		Alert    json.RawMessage `json:"alert"`
	}
	if !decodeJSON(w, body, &req) {
		return
	}
	bad := func(field, msg string) { writeFieldErr(w, http.StatusBadRequest, "invalid_request", field, msg) }
	if !presentRaw(req.Data) {
		bad("data", "required")
		return
	}
	priority := 5
	if req.Priority != nil {
		if *req.Priority != 5 && *req.Priority != 10 {
			bad("priority", "must be 5 or 10")
			return
		}
		priority = *req.Priority
	}
	_, alertJSON, ok := parseActivityAlert(req.Alert)
	if !ok {
		bad("alert", "must be an object with title and/or body")
		return
	}
	a, ok := s.activityOr404(w, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.EndedAt.IsZero() {
		writeErr(w, http.StatusConflict, "activity_ended")
		return
	}
	if err := s.dw.CheckContentState(a.TemplateID, req.Data); err != nil {
		s.contentStateErr(w, err)
		return
	}
	seq, err := s.Store.UpdateActivityState(a.ID, compactJSON(req.Data), priority, alertJSON)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusConflict, "activity_ended")
		return
	}
	if err != nil {
		s.internal(w, "update activity failed", err)
		return
	}
	a, ok = s.activityOr404(w, a.ID)
	if !ok {
		return
	}
	if err := s.dw.PushActivityUpdate(r.Context(), a, seq); err != nil {
		s.log.Error("activity update push failed", "activity_id", a.ID, "err", err)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"activity_id": a.ID, "update": seq})
}

// DELETE /api/v1/activities/{id}
func (s *server) endActivity(w http.ResponseWriter, r *http.Request) {
	a, ok := s.activityOr404(w, r.PathValue("id"))
	if !ok {
		return
	}
	if _, err := s.dw.FinishActivity(r.Context(), a); err != nil {
		s.log.Error("end activity failed", "activity_id", a.ID, "err", err)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"activity_id": a.ID})
}

// POST /device/v1/activities/{id}/token
func (s *server) activityToken(w http.ResponseWriter, r *http.Request, dev store.DeviceWithPairing, body []byte) {
	var req struct {
		SealedUpdate string `json:"sealed_update"`
	}
	if !decodeJSON(w, body, &req) {
		return
	}
	if req.SealedUpdate == "" || len(req.SealedUpdate) > 4096 {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "sealed_update", "required, at most 4096 bytes")
		return
	}
	a, ok := s.activityOr404(w, r.PathValue("id"))
	if !ok {
		return
	}
	if a.ExternalUserID != dev.Pairing.ExternalUserID {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if !s.channelAccess(w, a.ExternalUserID, a.ChannelID) {
		return
	}
	if !a.EndedAt.IsZero() {
		writeErr(w, http.StatusConflict, "activity_ended")
		return
	}
	tok, err := s.Store.UpsertActivityToken(a.ID, dev.ID, req.SealedUpdate)
	if err != nil {
		s.internal(w, "store activity token failed", err)
		return
	}
	if err := s.dw.SyncActivityToken(r.Context(), a, tok); err != nil {
		s.log.Error("activity token sync failed", "activity_id", a.ID, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/recodive/hansho/hub/internal/delivery"
	"github.com/recodive/hansho/hub/internal/level"
	"github.com/recodive/hansho/hub/internal/store"
)

const maxCollapseIDLen = 64 // APNs limit

// POST /api/v1/messages
func (s *server) sendMessage(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r, maxJSONBody)
	if !ok {
		return
	}
	var req struct {
		ExternalUserID string          `json:"external_user_id"`
		Channel        string          `json:"channel"`
		Level          *string         `json:"level"`
		Title          string          `json:"title"`
		Subtitle       string          `json:"subtitle"`
		Body           string          `json:"body"`
		ImageURL       string          `json:"image_url"`
		TemplateID     string          `json:"template_id"`
		Data           json.RawMessage `json:"data"`
		Sound          string          `json:"sound"`
		Actions        json.RawMessage `json:"actions"`
		CollapseID     string          `json:"collapse_id"`
		Page           *struct {
			IntervalS  *int `json:"interval_s"`
			MaxRetries *int `json:"max_retries"`
		} `json:"page"`
	}
	if !decodeJSON(w, body, &req) {
		return
	}
	bad := func(field, msg string) { writeFieldErr(w, http.StatusBadRequest, "invalid_request", field, msg) }
	for _, f := range []struct{ name, v string }{
		{"external_user_id", req.ExternalUserID}, {"channel", req.Channel}, {"title", req.Title}, {"body", req.Body},
	} {
		if f.v == "" {
			bad(f.name, "required")
			return
		}
	}
	if len(req.ExternalUserID) > maxFieldLen {
		bad("external_user_id", "at most 256 bytes")
		return
	}
	if req.Level != nil && !level.Valid(*req.Level) {
		bad("level", "must be one of passive, active, time-sensitive, critical, page")
		return
	}
	if len(req.CollapseID) > maxCollapseIDLen {
		bad("collapse_id", "at most 64 bytes")
		return
	}
	for i := 0; i < len(req.CollapseID); i++ {
		if c := req.CollapseID[i]; c <= ' ' || c > '~' {
			bad("collapse_id", "printable ASCII without spaces")
			return
		}
	}
	if req.ImageURL != "" && !isHTTPSURL(req.ImageURL) {
		bad("image_url", "must be an https URL with a host")
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
		ExternalUserID: req.ExternalUserID, Channel: ch, Level: lvl,
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

	res, err := delivery.Enqueue(r.Context(), s.Store, s.Webhooks, s.Cfg, s.Key, s.Now, in)
	if errors.Is(err, delivery.ErrPayloadTooLarge) {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large")
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
	writeJSON(w, http.StatusAccepted, map[string]any{
		"message_id": res.Message.ID, "level": lvl, "devices": len(dels),
		"status": res.Message.Status, "deliveries": dels,
	})
}

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

package api

import (
	"context"
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

// sendRequest is the body of POST /api/v1/messages, and what the admin UI's test form fills in.
type sendRequest struct {
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
	Page            *sendPage       `json:"page"`
}

type sendPage struct {
	IntervalS  *int    `json:"interval_s"`
	MaxRetries *int    `json:"max_retries"`
	Ack        *string `json:"ack"`        // "any" (default) or "each"
	ShowAcker  bool    `json:"show_acker"` // name the acknowledger in the other recipients' banner
}

// recipient is one entry of a send's "recipients".
type recipient struct {
	ExternalUserID string         `json:"external_user_id"`
	MessageID      string         `json:"message_id,omitempty"`
	Status         string         `json:"status"`
	Devices        *int           `json:"devices,omitempty"`
	Deliveries     []deliveryView `json:"deliveries,omitempty"`
}

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
	var req sendRequest
	if !decodeJSON(w, body, &req) {
		return
	}
	lvl, recipients, e := s.send(r.Context(), req)
	if e != nil {
		s.fail(w, e)
		return
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

// send validates a request and enqueues the message for each recipient. It returns the level the message went out at
// and one recipient per user. The API and the admin UI share it.
func (s *server) send(ctx context.Context, req sendRequest) (string, []recipient, *apiError) {
	targets := 0
	for _, set := range []bool{req.ExternalUserID != "", len(req.ExternalUserIDs) > 0, req.ToAudience} {
		if set {
			targets++
		}
	}
	switch {
	case targets == 0:
		return "", nil, badField("external_user_id", "required (or external_user_ids, or to_audience)")
	case targets > 1:
		return "", nil, badField("external_user_ids", "give only one of external_user_id, external_user_ids and to_audience")
	case len(req.ExternalUserID) > maxFieldLen:
		return "", nil, badField("external_user_id", "at most 256 bytes")
	case len(req.ExternalUserIDs) > maxRecipients:
		return "", nil, badField("external_user_ids", "at most 1000 entries")
	}
	for i, u := range req.ExternalUserIDs {
		if u == "" || len(u) > maxFieldLen {
			return "", nil, badField("external_user_ids["+strconv.Itoa(i)+"]", "required, at most 256 bytes")
		}
	}
	for _, f := range []struct{ name, v string }{
		{"channel", req.Channel}, {"title", req.Title}, {"body", req.Body},
	} {
		if f.v == "" {
			return "", nil, badField(f.name, "required")
		}
	}
	if req.Level != nil && !level.Valid(*req.Level) {
		return "", nil, badField("level", "must be one of passive, active, time-sensitive, critical, page")
	}
	if len(req.CollapseID) > maxCollapseIDLen {
		return "", nil, badField("collapse_id", "at most 64 bytes")
	}
	for i := 0; i < len(req.CollapseID); i++ {
		if c := req.CollapseID[i]; c <= ' ' || c > '~' {
			return "", nil, badField("collapse_id", "printable ASCII without spaces")
		}
	}
	if req.ImageURL != "" && !isHTTPSURL(req.ImageURL) {
		return "", nil, badField("image_url", "must be an https URL with a host")
	}

	ch, err := s.Store.GetChannel(req.Channel)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !ch.DeletedAt.IsZero()) {
		return "", nil, &apiError{Status: http.StatusNotFound, Code: "unknown_channel"}
	}
	if err != nil {
		return "", nil, internalErr("channel lookup failed", err)
	}

	templateID := req.TemplateID
	if templateID == "" {
		templateID = ch.TemplateID
	} else if _, err := s.Store.GetTemplate(templateID); errors.Is(err, store.ErrNotFound) {
		return "", nil, badField("template_id", "unknown template")
	} else if err != nil {
		return "", nil, internalErr("template lookup failed", err)
	}
	soundID := req.Sound
	if soundID == "" {
		soundID = ch.Sound
	}
	var soundFile string
	if soundID != "" {
		soundFile, err = s.Store.SoundFilename(soundID)
		if errors.Is(err, store.ErrNotFound) {
			return "", nil, badField("sound", "unknown sound")
		}
		if err != nil {
			return "", nil, internalErr("sound lookup failed", err)
		}
	}
	actions := json.RawMessage(ch.ActionsJSON)
	if len(req.Actions) > 0 && string(req.Actions) != "null" {
		norm, field, msg := checkActions(req.Actions)
		if field != "" {
			return "", nil, badField(field, msg)
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
				return "", nil, badField("page.interval_s", "must be between 1 and 86400")
			}
			in.PageIntervalS = *iv
		}
		if mr := req.Page.MaxRetries; mr != nil {
			if *mr < 0 || *mr > 100 {
				return "", nil, badField("page.max_retries", "must be between 0 and 100")
			}
			in.PageMaxRetries = mr
		}
		if a := req.Page.Ack; a != nil && *a != store.PageAckAny && *a != store.PageAckEach {
			return "", nil, badField("page.ack", "must be any or each")
		}
	}
	if lvl == level.Page {
		in.PageAck, in.SendID = store.PageAckAny, store.NewID("send_")
		if req.Page != nil {
			in.ShowAcker = req.Page.ShowAcker
			if req.Page.Ack != nil {
				in.PageAck = *req.Page.Ack
			}
		}
	}

	var users []string
	switch {
	case req.ExternalUserID != "":
		users = []string{req.ExternalUserID}
	case req.ToAudience:
		if users, err = s.Store.ActiveUsers(); err != nil {
			return "", nil, internalErr("list users failed", err)
		}
	default:
		users = dedupe(req.ExternalUserIDs)
	}

	recipients := make([]recipient, 0, len(users))
	for _, user := range users {
		acc, err := s.Store.AccessFor(user)
		if err != nil {
			return "", nil, internalErr("access check failed", err)
		}
		if !acc.Allows(ch.Audience) {
			if req.ToAudience {
				continue // not a recipient to begin with
			}
			recipients = append(recipients, recipient{ExternalUserID: user, Status: statusNoAccess})
			continue
		}
		in.ExternalUserID = user
		res, err := delivery.Enqueue(ctx, s.Store, s.Webhooks, s.Cfg, s.Key, s.Now, in)
		if errors.Is(err, delivery.ErrPayloadTooLarge) {
			// The size does not depend on the user.
			return "", nil, &apiError{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large"}
		}
		if err != nil {
			return "", nil, internalErr("enqueue failed", err)
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
	return lvl, recipients, nil
}

// statusNoAccess is the recipient status of a user outside the channel's audience.
const statusNoAccess = "no_access"

type deliveryView struct {
	DeviceID string `json:"device_id"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// loadMessage reads a message and its deliveries. The API and the admin UI's status page share it.
func (s *server) loadMessage(id string) (store.Message, []deliveryView, *apiError) {
	m, err := s.Store.GetMessage(id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Message{}, nil, &apiError{Status: http.StatusNotFound, Code: "unknown_message"}
	}
	if err != nil {
		return store.Message{}, nil, internalErr("message lookup failed", err)
	}
	dels, err := s.Store.Deliveries(m.ID)
	if err != nil {
		return store.Message{}, nil, internalErr("deliveries lookup failed", err)
	}
	views := make([]deliveryView, 0, len(dels))
	for _, d := range dels {
		views = append(views, deliveryView{DeviceID: d.DeviceID, Status: d.Status, Error: d.Error})
	}
	return m, views, nil
}

// GET /api/v1/messages/{id}
func (s *server) getMessage(w http.ResponseWriter, r *http.Request) {
	m, views, e := s.loadMessage(r.PathValue("id"))
	if e != nil {
		s.fail(w, e)
		return
	}
	out := map[string]any{
		"message_id": m.ID, "status": m.Status, "level": m.Level, "attempts": m.Attempts, "deliveries": views,
	}
	if !m.AckedAt.IsZero() {
		out["acked_at"] = m.AckedAt.UTC().Format(time.RFC3339)
		if m.AckedBy != "" {
			out["acked_by"] = m.AckedBy
		}
	}
	writeJSON(w, http.StatusOK, out)
}

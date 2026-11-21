package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/recodive/hansho/hub/internal/crypto"
	"github.com/recodive/hansho/hub/internal/level"
	"github.com/recodive/hansho/hub/internal/store"
	"github.com/recodive/hansho/hub/internal/webhook"
)

const maxActionText = 4096

// dismissActionID is what a device reports when the person swipes a notification away. It becomes a
// message.dismissed webhook, and only for a message that asks to be acknowledged (it has an ack action) and was
// not acknowledged yet: other swipes stay private.
const dismissActionID = "_dismissed"

// POST /device/v1/actions
//
// A device reports a button press. Every press is forwarded to the sender as
// an "action" webhook, except acknowledgements: those are idempotent, so only
// the press that actually acknowledges a message is reported (together with
// message.acked, and for a page the cancel pushes); later acks and acks after
// expiry are accepted and ignored.
func (s *server) deviceAction(w http.ResponseWriter, r *http.Request, dev store.DeviceWithPairing, body []byte) {
	var req struct {
		MessageID string `json:"message_id"`
		ActionID  string `json:"action_id"`
		Text      string `json:"text"`
	}
	if !decodeJSON(w, body, &req) {
		return
	}
	if req.MessageID == "" {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "message_id", "required")
		return
	}
	if req.ActionID == "" {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "action_id", "required")
		return
	}
	if len(req.Text) > maxActionText {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "text", "at most 4096 bytes")
		return
	}
	m, err := s.Store.GetMessage(req.MessageID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "unknown_message")
		return
	}
	if err != nil {
		s.internal(w, "message lookup failed", err)
		return
	}
	if m.ExternalUserID != dev.Pairing.ExternalUserID {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}

	action := map[string]string{
		"message_id": m.ID, "action_id": req.ActionID, "device_id": dev.ID, "external_user_id": m.ExternalUserID,
	}
	if req.Text != "" {
		action["text"] = req.Text
	}

	if req.ActionID == dismissActionID {
		if m.AckedAt.IsZero() && s.hasAckAction(m) {
			s.emit(r.Context(), webhook.EventMessageDismissed, map[string]string{
				"message_id": m.ID, "external_user_id": m.ExternalUserID, "device_id": dev.ID,
			})
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	if s.isAckAction(m, req.ActionID) {
		acked, err := s.Store.AckMessage(m.ID, s.Now())
		if err != nil {
			s.internal(w, "ack failed", err)
			return
		}
		if acked {
			s.emit(r.Context(), webhook.EventActionTaken, action)
			s.emit(r.Context(), webhook.EventMessageAcked, map[string]string{
				"message_id": m.ID, "external_user_id": m.ExternalUserID, "device_id": dev.ID,
			})
			if m.Level == level.Page {
				if _, err := s.dw.CancelPage(r.Context(), m); err != nil {
					s.log.Error("cancel push failed", "message_id", m.ID, "err", err)
				}
				s.log.Info("page acknowledged", "message_id", m.ID, "device_id", dev.ID)
			}
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	s.emit(r.Context(), webhook.EventActionTaken, action)
	w.WriteHeader(http.StatusOK)
}

// isAckAction reports whether pressing actionID acknowledges m: the built-in
// "ack", or an action of the message (as sent) flagged "ack": true.
func (s *server) isAckAction(m store.Message, actionID string) bool {
	if actionID == "ack" {
		return true
	}
	for _, a := range s.messageActions(m) {
		if a.ID == actionID && a.Ack {
			return true
		}
	}
	return false
}

// hasAckAction reports whether m asks to be acknowledged: a page, or a message with the built-in "ack" or an
// action flagged "ack": true.
func (s *server) hasAckAction(m store.Message) bool {
	if m.Level == level.Page {
		return true
	}
	for _, a := range s.messageActions(m) {
		if a.ID == "ack" || a.Ack {
			return true
		}
	}
	return false
}

type messageAction struct {
	ID  string `json:"id"`
	Ack bool   `json:"ack"`
}

// messageActions returns the actions m was sent with (nil when its payload cannot be read).
func (s *server) messageActions(m store.Message) []messageAction {
	pt, err := crypto.UnwrapAtRest(s.Cfg.MasterKey, m.PayloadWrapped)
	if err != nil {
		s.log.Error("unwrap message failed", "message_id", m.ID, "err", err)
		return nil
	}
	var parsed struct {
		Actions []messageAction `json:"actions"`
	}
	if json.Unmarshal(pt, &parsed) != nil {
		return nil
	}
	return parsed.Actions
}

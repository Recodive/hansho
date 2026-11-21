package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

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

// actionBurst and actionRefill bound POST /device/v1/actions per device: a burst of 10 presses, then one per second.
// Every press (including a dismiss) becomes a webhook row, so an unbounded stream would flood the shared outbox.
const (
	actionBurst  = 10
	actionRefill = time.Second
)

type actionBucket struct {
	tokens float64
	at     time.Time
}

// actionLimiter is a token bucket per device id, in memory (a restart refills every bucket). The zero value works.
type actionLimiter struct {
	mu      sync.Mutex
	buckets map[string]*actionBucket
}

func (l *actionLimiter) allow(id string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = map[string]*actionBucket{}
	}
	b := l.buckets[id]
	if b == nil {
		b = &actionBucket{tokens: actionBurst, at: now}
		l.buckets[id] = b
	}
	if el := now.Sub(b.at); el > 0 {
		b.tokens = min(actionBurst, b.tokens+float64(el)/float64(actionRefill))
		b.at = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// POST /device/v1/actions
//
// A device reports a button press. Every press is forwarded to the sender as
// an "action" webhook, except acknowledgements: those are idempotent, so only
// the press that actually acknowledges a message is reported (together with
// message.acked, and for a page the cancel pushes); later acks and acks after
// expiry are accepted and ignored.
func (s *server) deviceAction(w http.ResponseWriter, r *http.Request, dev store.DeviceWithPairing, body []byte) {
	if !s.actionLimit.allow(dev.ID, s.Now()) {
		w.Header().Set("Retry-After", "1")
		writeErr(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
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
	if !s.offersAction(m, req.ActionID) {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "action_id", "not an action of this message")
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

// offersAction reports whether actionID can be pressed on m: the built-ins "ack" and "_dismissed", an action the
// message was sent with, or an action of its channel (the app builds its buttons from the channel it last synced, which
// may have changed since the message was sent).
func (s *server) offersAction(m store.Message, actionID string) bool {
	if actionID == "ack" || actionID == dismissActionID {
		return true
	}
	for _, a := range s.messageActions(m) {
		if a.ID == actionID {
			return true
		}
	}
	if ch, err := s.Store.GetChannel(m.ChannelID); err == nil {
		var list []messageAction
		if json.Unmarshal([]byte(ch.ActionsJSON), &list) == nil {
			for _, a := range list {
				if a.ID == actionID {
					return true
				}
			}
		}
	}
	return false
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

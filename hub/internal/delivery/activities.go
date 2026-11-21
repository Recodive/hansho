package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/recodive/hansho/hub/internal/crypto"
	"github.com/recodive/hansho/hub/internal/relay"
	"github.com/recodive/hansho/hub/internal/store"
	"github.com/recodive/hansho/hub/internal/webhook"
)

const (
	// ActivityLifetime is when the hub ends an activity itself, ahead of Apple's 8 h limit.
	ActivityLifetime = 7*time.Hour + 50*time.Minute
	// MaxContentStateBytes is the Live Activity content-state limit.
	MaxContentStateBytes = 4096
	// ActivityAttributesType is the ActivityKit attributes type the app registers.
	ActivityAttributesType = "HanshoActivityAttributes"
)

// ErrContentStateTooLarge means the (encoded) content-state exceeds 4 KB.
var ErrContentStateTooLarge = errors.New("delivery: content-state exceeds 4096 bytes")

// ActivityAlert is the optional alert shown with a start or update.
type ActivityAlert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// ContentState builds the Live Activity content-state. Plaintext (encryptWith
// nil) is {"t":template_id,"d":data}; otherwise that JSON is encrypted with the
// key derived from the pairing key and wrapped as {"blob":b64url(nonce||ct||tag)}.
func ContentState(templateID string, data json.RawMessage, encryptWith []byte) (json.RawMessage, error) {
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	plain, err := json.Marshal(struct {
		T string          `json:"t"`
		D json.RawMessage `json:"d"`
	}{templateID, data})
	if err != nil {
		return nil, fmt.Errorf("delivery: invalid content-state data: %w", err)
	}
	out := json.RawMessage(plain)
	if encryptWith != nil {
		blob, err := crypto.SealActivityState(encryptWith, plain)
		if err != nil {
			return nil, err
		}
		out, err = json.Marshal(map[string]string{"blob": b64.EncodeToString(blob)})
		if err != nil {
			return nil, err
		}
	}
	if len(out) > MaxContentStateBytes {
		return nil, ErrContentStateTooLarge
	}
	return out, nil
}

// CheckContentState validates that data fits the content-state limit in the
// configured mode (the encrypted size does not depend on the key).
func (w *Worker) CheckContentState(templateID string, data json.RawMessage) error {
	var key []byte
	if w.cfg.LAEncrypt {
		key = make([]byte, 32)
	}
	_, err := ContentState(templateID, data, key)
	return err
}

type liveAPS struct {
	Timestamp      int64           `json:"timestamp"`
	Event          string          `json:"event"`
	ContentState   json.RawMessage `json:"content-state"`
	AttributesType string          `json:"attributes-type,omitempty"`
	Attributes     *attrs          `json:"attributes,omitempty"`
	Alert          *ActivityAlert  `json:"alert,omitempty"`
	DismissalDate  int64           `json:"dismissal-date,omitempty"`
}

type attrs struct {
	ActivityID string `json:"activity_id"`
	TemplateID string `json:"template_id"`
	Channel    string `json:"channel"`
	// PairingID selects the activity key on the device; push-to-start goes to one device, so it is per-device correct.
	PairingID string `json:"pairing_id"`
}

func marshalAPS(a liveAPS) ([]byte, error) {
	return json.Marshal(struct {
		APS liveAPS `json:"aps"`
	}{a})
}

// StartPayload is the push-to-start payload.
func StartPayload(now time.Time, a store.Activity, pairingID string, state json.RawMessage, alert *ActivityAlert) ([]byte, error) {
	return marshalAPS(liveAPS{
		Timestamp: now.Unix(), Event: "start", ContentState: state, AttributesType: ActivityAttributesType,
		Attributes: &attrs{ActivityID: a.ID, TemplateID: a.TemplateID, Channel: a.ChannelID, PairingID: pairingID}, Alert: alert,
	})
}

// UpdatePayload is the update payload.
func UpdatePayload(now time.Time, state json.RawMessage, alert *ActivityAlert) ([]byte, error) {
	return marshalAPS(liveAPS{Timestamp: now.Unix(), Event: "update", ContentState: state, Alert: alert})
}

// EndPayload ends the activity and dismisses it immediately.
func EndPayload(now time.Time, state json.RawMessage) ([]byte, error) {
	return marshalAPS(liveAPS{Timestamp: now.Unix(), Event: "end", ContentState: state, DismissalDate: now.Unix()})
}

func parseAlert(raw string) *ActivityAlert {
	if raw == "" {
		return nil
	}
	var a ActivityAlert
	if json.Unmarshal([]byte(raw), &a) != nil {
		return nil
	}
	return &a
}

// stateFor builds the content-state for a device: encrypted per pairing when enabled.
func (w *Worker) stateFor(a store.Activity, dev store.DeviceWithPairing) (json.RawMessage, error) {
	var key []byte
	if w.cfg.LAEncrypt {
		var err error
		if key, err = crypto.UnwrapAtRest(w.cfg.MasterKey, dev.Pairing.KeyWrapped); err != nil {
			return nil, err
		}
	}
	return ContentState(a.TemplateID, json.RawMessage(a.DataJSON), key)
}

// canAccessChannel reports whether the user may use the channel now.
func (w *Worker) canAccessChannel(user, channelID string) (bool, error) {
	ch, err := w.st.GetChannel(channelID)
	if err != nil {
		return false, err
	}
	return w.canAccess(user, ch)
}

func (w *Worker) usable(dev store.DeviceWithPairing) bool {
	return dev.RevokedAt.IsZero() && dev.Pairing.RevokedAt.IsZero()
}

// StartActivity sends push-to-start to every active device of the activity's
// user that registered a push-to-start token. It returns how many accepted.
func (w *Worker) StartActivity(ctx context.Context, a store.Activity, alert *ActivityAlert) (int, error) {
	allowed, err := w.canAccessChannel(a.ExternalUserID, a.ChannelID)
	if err != nil || !allowed {
		return 0, err
	}
	devices, err := w.st.ActiveDevicesForUser(a.ExternalUserID)
	if err != nil {
		return 0, err
	}
	cache := newEnvCache()
	sent := 0
	for _, dev := range devices {
		if dev.SealedPushToStart == "" || cache.revoked[dev.Pairing.ID] {
			continue
		}
		state, err := w.stateFor(a, dev)
		if err != nil {
			return sent, err
		}
		payload, err := StartPayload(w.now(), a, dev.Pairing.ID, state, alert)
		if err != nil {
			return sent, err
		}
		_, err = w.relay.Push(ctx, relay.PushRequest{Sealed: dev.SealedPushToStart, Kind: "pushtostart", Priority: 10, Payload: payload})
		if err != nil {
			if ctx.Err() != nil {
				return sent, ctx.Err()
			}
			w.classify(ctx, err, dev, cache, func() { w.clearTokens(dev.ID, store.TokenPushToStart) })
			continue
		}
		sent++
	}
	return sent, nil
}

// pushActivityTo sends one event payload to a device's update token.
func (w *Worker) pushActivityTo(ctx context.Context, a store.Activity, tok store.ActivityToken, dev store.DeviceWithPairing, cache *envCache,
	build func(state json.RawMessage) ([]byte, error), priority int, end bool) bool {
	if !w.usable(dev) || cache.revoked[dev.Pairing.ID] {
		return false
	}
	allowed, err := w.canAccessChannel(dev.Pairing.ExternalUserID, a.ChannelID)
	if err != nil {
		w.log.Error("activity access check failed", "activity_id", a.ID, "err", err)
		return false
	}
	if !allowed {
		if !end {
			return false // no more updates for someone who lost access
		}
		// The end still goes out, so the activity leaves their screen, but without the state they no longer may see.
		a.DataJSON = "null"
	}
	state, err := w.stateFor(a, dev)
	if err != nil {
		w.log.Error("activity state failed", "activity_id", a.ID, "err", err)
		return false
	}
	payload, err := build(state)
	if err != nil {
		w.log.Error("activity payload failed", "activity_id", a.ID, "err", err)
		return false
	}
	_, err = w.relay.Push(ctx, relay.PushRequest{Sealed: tok.SealedUpdate, Kind: "liveactivity", Priority: priority, Payload: payload})
	if err == nil {
		return true
	}
	w.classify(ctx, err, dev, cache, func() {
		if derr := w.st.DeleteActivityToken(a.ID, dev.ID); derr != nil {
			w.log.Error("delete activity token failed", "activity_id", a.ID, "err", derr)
		}
	})
	return false
}

// PushActivityUpdate sends the activity's current state (update sequence seq)
// to every registered update token.
func (w *Worker) PushActivityUpdate(ctx context.Context, a store.Activity, seq int) error {
	tokens, err := w.st.ActivityTokens(a.ID)
	if err != nil {
		return err
	}
	cache := newEnvCache()
	alert := parseAlert(a.AlertJSON)
	for _, tok := range tokens {
		dev, err := w.st.GetDevice(tok.DeviceID)
		if err != nil {
			return err
		}
		ok := w.pushActivityTo(ctx, a, tok, dev, cache, func(st json.RawMessage) ([]byte, error) {
			return UpdatePayload(w.now(), st, alert)
		}, a.Priority, false)
		if ok {
			if err := w.st.MarkActivityTokenSent(a.ID, tok.DeviceID, seq); err != nil {
				return err
			}
		}
	}
	return nil
}

// SyncActivityToken brings a newly registered update token up to date: if the
// activity was updated since the token's last delivery, the current state is pushed.
func (w *Worker) SyncActivityToken(ctx context.Context, a store.Activity, tok store.ActivityToken) error {
	if a.UpdateSeq <= tok.SentSeq {
		return nil
	}
	dev, err := w.st.GetDevice(tok.DeviceID)
	if err != nil {
		return err
	}
	alert := parseAlert(a.AlertJSON)
	if w.pushActivityTo(ctx, a, tok, dev, newEnvCache(), func(st json.RawMessage) ([]byte, error) {
		return UpdatePayload(w.now(), st, alert)
	}, a.Priority, false) {
		return w.st.MarkActivityTokenSent(a.ID, tok.DeviceID, a.UpdateSeq)
	}
	return nil
}

// FinishActivity marks the activity ended and sends an end event to all its
// tokens. It reports whether this call ended it (false if it already was).
func (w *Worker) FinishActivity(ctx context.Context, a store.Activity) (bool, error) {
	changed, err := w.st.EndActivity(a.ID, w.now())
	if err != nil || !changed {
		return false, err
	}
	tokens, err := w.st.ActivityTokens(a.ID)
	if err != nil {
		return true, err
	}
	cache := newEnvCache()
	for _, tok := range tokens {
		dev, err := w.st.GetDevice(tok.DeviceID)
		if err != nil {
			return true, err
		}
		w.pushActivityTo(ctx, a, tok, dev, cache, func(st json.RawMessage) ([]byte, error) {
			return EndPayload(w.now(), st)
		}, 10, true)
	}
	return true, nil
}

// endDueActivities ends activities that reached their auto-end time.
func (w *Worker) endDueActivities(ctx context.Context) error {
	due, err := w.st.ActivitiesDueToEnd(w.now(), batchSize)
	if err != nil {
		return err
	}
	var errs []error
	for _, a := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ended, err := w.FinishActivity(ctx, a)
		if err != nil {
			w.log.Error("end activity failed", "activity_id", a.ID, "err", err)
			errs = append(errs, err)
		}
		if ended {
			emit(ctx, w.wh, webhook.EventActivityEnded, map[string]string{
				"activity_id": a.ID, "external_user_id": a.ExternalUserID, "channel": a.ChannelID, "reason": "timeout",
			})
		}
	}
	return errors.Join(errs...)
}

package delivery

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/recodive/hansho/hub/internal/config"
	"github.com/recodive/hansho/hub/internal/crypto"
	"github.com/recodive/hansho/hub/internal/level"
	"github.com/recodive/hansho/hub/internal/store"
	"github.com/recodive/hansho/hub/internal/webhook"
)

// Page defaults.
const (
	DefaultPageIntervalS  = 120
	DefaultPageMaxRetries = 5
)

// EnqueueInput describes a validated message to send.
type EnqueueInput struct {
	ExternalUserID string
	Channel        store.Channel
	Level          string // already clamped by EffectiveLevel
	Plaintext      Plaintext
	CollapseID     string
	Sound          string // sound filename, "" = default
	PageIntervalS  int    // level page only; 0 = default
	PageMaxRetries *int   // level page only; nil = default
	SendID         string // shared by the messages of one send
	PageAck        string // level page only: store.PageAckAny or PageAckEach; "" = each
}

// EnqueueResult is the stored message and its initial deliveries.
type EnqueueResult struct {
	Message    store.Message
	Deliveries []store.Delivery
}

func randomPairingKey() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return k
}

// Enqueue stores a message for the worker. It checks the encrypted payload
// size first (ErrPayloadTooLarge), independent of the user's device state. A
// user with no sendable device yields an undeliverable message and a
// message.undeliverable webhook rather than an error.
func Enqueue(ctx context.Context, st *store.Store, wh *webhook.Dispatcher, cfg config.Config, key ed25519.PrivateKey, now func() time.Time, in EnqueueInput) (EnqueueResult, error) {
	if !level.Valid(in.Level) {
		return EnqueueResult{}, fmt.Errorf("delivery: unknown level %q", in.Level)
	}
	pt, err := json.Marshal(in.Plaintext)
	if err != nil {
		return EnqueueResult{}, err
	}
	msgID := store.NewID("msg_")

	// Size check with a throwaway pairing: ciphertext length does not depend on the key.
	probePairing := store.NewID("pair_")
	probe, err := crypto.SealMessage(key, randomPairingKey(), probePairing, msgID, pt)
	if err != nil {
		return EnqueueResult{}, err
	}
	if _, err := BuildAlertPayload(cfg.DisplayName, in.Channel, in.Level, probe, in.Sound); err != nil {
		return EnqueueResult{}, err
	}

	devices, err := st.ActiveDevicesForUser(in.ExternalUserID)
	if err != nil {
		return EnqueueResult{}, err
	}
	var dels []store.Delivery
	for _, d := range devices {
		if d.SealedAlert != "" {
			dels = append(dels, store.Delivery{MessageID: msgID, DeviceID: d.ID, Status: store.DelQueued})
		}
	}

	wrapped, err := crypto.WrapAtRest(cfg.MasterKey, pt)
	if err != nil {
		return EnqueueResult{}, err
	}
	t := now()
	m := store.Message{
		ID: msgID, ExternalUserID: in.ExternalUserID, ChannelID: in.Channel.ID, Level: in.Level,
		Status: store.MsgQueued, NextAttemptAt: t, CreatedAt: t, CollapseID: in.CollapseID,
		Sound: in.Sound, PayloadWrapped: wrapped,
	}
	if in.Level == level.Page {
		m.PageIntervalS, m.PageMaxRetries = in.PageIntervalS, DefaultPageMaxRetries
		if m.PageIntervalS <= 0 {
			m.PageIntervalS = DefaultPageIntervalS
		}
		if in.PageMaxRetries != nil {
			m.PageMaxRetries = *in.PageMaxRetries
		}
		m.SendID = in.SendID
		if in.PageAck == store.PageAckAny {
			m.PageAck = store.PageAckAny
		} else {
			m.PageAck = store.PageAckEach
		}
		if m.CollapseID == "" {
			// Resends replace the earlier banner instead of stacking up.
			m.CollapseID = msgID
		}
	}
	if len(dels) == 0 {
		m.Status = store.MsgUndeliverable
	}
	if err := st.CreateMessage(m, dels); err != nil {
		return EnqueueResult{}, err
	}
	if len(dels) == 0 {
		emit(ctx, wh, webhook.EventMessageUndeliverable, map[string]any{
			"message_id": m.ID, "external_user_id": m.ExternalUserID, "channel": m.ChannelID, "reason": "no_devices",
		})
	}
	return EnqueueResult{Message: m, Deliveries: dels}, nil
}

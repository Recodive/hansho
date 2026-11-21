package delivery

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/recodive/hansho/hub/internal/config"
	"github.com/recodive/hansho/hub/internal/crypto"
	"github.com/recodive/hansho/hub/internal/level"
	"github.com/recodive/hansho/hub/internal/relay"
	"github.com/recodive/hansho/hub/internal/store"
	"github.com/recodive/hansho/hub/internal/webhook"
)

// RelayClient is the subset of the relay client the hub uses to push and revoke.
type RelayClient interface {
	Push(ctx context.Context, p relay.PushRequest) (string, error)
	Revoke(ctx context.Context, sealed string) error
}

const (
	batchSize = 200
	// retryDelay is how long a queued message waits after a transient relay failure.
	retryDelay = 5 * time.Second
	// maxQueueAge is how long transient failures are retried before a delivery is failed.
	maxQueueAge = 10 * time.Minute
)

// Worker sends queued messages, resends pages and ends Live Activities.
type Worker struct {
	st    *store.Store
	relay RelayClient
	wh    *webhook.Dispatcher
	cfg   config.Config
	key   ed25519.PrivateKey
	pub   ed25519.PublicKey
	now   func() time.Time
	log   *slog.Logger

	mu sync.Mutex // serialises RunOnce
}

// NewWorker creates a worker. now may be nil.
func NewWorker(st *store.Store, rc RelayClient, wh *webhook.Dispatcher, cfg config.Config, key ed25519.PrivateKey, now func() time.Time) *Worker {
	if now == nil {
		now = time.Now
	}
	return &Worker{st: st, relay: rc, wh: wh, cfg: cfg, key: key, pub: key.Public().(ed25519.PublicKey), now: now, log: slog.Default()}
}

// RunOnce performs all due work. Errors from individual items are logged and
// joined; one failing item does not stop the others.
func (w *Worker) RunOnce(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Work is serial, so a 429 must not sleep here: it is rescheduled instead (see sendRetry).
	ctx = relay.NoWait(ctx)
	return errors.Join(
		w.sendQueued(ctx),
		w.runPages(ctx),
		w.endDueActivities(ctx),
	)
}

// Run calls RunOnce every tick until ctx is done.
func (w *Worker) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
				w.log.Error("worker run failed", "err", err)
			}
		}
	}
}

func emit(ctx context.Context, wh *webhook.Dispatcher, event string, payload any) {
	if err := wh.Emit(ctx, event, payload); err != nil {
		slog.Error("webhook emit failed", "event", event, "err", err)
	}
}

type sendKind int

const (
	sendSent sendKind = iota
	sendFailed
	sendSkipped
	sendRetry
)

type sendResult struct {
	kind       sendKind
	apnsID     string
	errCode    string
	retryAfter time.Duration
}

// envCache holds one envelope per pairing for one send pass, and remembers
// pairings revoked after a relay verdict so their other devices are skipped, and
// pairings the relay rate-limited (with its suggested wait) so their remaining
// deliveries wait for a later pass.
type envCache struct {
	envs    map[string]crypto.Envelope
	revoked map[string]bool
	limited map[string]time.Duration
}

func newEnvCache() *envCache {
	return &envCache{envs: map[string]crypto.Envelope{}, revoked: map[string]bool{}, limited: map[string]time.Duration{}}
}

func (w *Worker) envelope(c *envCache, p store.Pairing, messageID string, plaintext []byte) (crypto.Envelope, error) {
	if e, ok := c.envs[p.ID]; ok {
		return e, nil
	}
	pk, err := crypto.UnwrapAtRest(w.cfg.MasterKey, p.KeyWrapped)
	if err != nil {
		return crypto.Envelope{}, err
	}
	e, err := crypto.SealMessage(w.key, pk, p.ID, messageID, plaintext)
	if err != nil {
		return crypto.Envelope{}, err
	}
	c.envs[p.ID] = e
	return e, nil
}

// pushAlert sends the message to one device and classifies the outcome.
// It applies relay verdicts that change hub state (cleared tokens, revoked pairings).
func (w *Worker) pushAlert(ctx context.Context, m store.Message, ch store.Channel, plaintext []byte, dev store.DeviceWithPairing, c *envCache) sendResult {
	if !dev.RevokedAt.IsZero() || !dev.Pairing.RevokedAt.IsZero() || c.revoked[dev.Pairing.ID] {
		return sendResult{kind: sendSkipped, errCode: "pairing_revoked"}
	}
	if dev.SealedAlert == "" {
		return sendResult{kind: sendSkipped, errCode: "no_token"}
	}
	if wait, ok := c.limited[dev.Pairing.ID]; ok {
		return sendResult{kind: sendRetry, errCode: "rate_limited", retryAfter: wait}
	}
	env, err := w.envelope(c, dev.Pairing, m.ID, plaintext)
	if err != nil {
		w.log.Error("seal failed", "message_id", m.ID, "pairing_id", dev.Pairing.ID, "err", err)
		return sendResult{kind: sendFailed, errCode: "seal_failed"}
	}
	payload, err := BuildAlertPayload(w.cfg.DisplayName, ch, m.Level, env, m.Sound)
	if err != nil {
		code := "payload_failed"
		if errors.Is(err, ErrPayloadTooLarge) {
			code = "payload_too_large"
		}
		return sendResult{kind: sendFailed, errCode: code}
	}
	apnsID, err := w.relay.Push(ctx, relay.PushRequest{
		Sealed: dev.SealedAlert, Kind: "alert", Priority: Priority(m.Level),
		CollapseID: m.CollapseID, Payload: payload,
	})
	if err == nil {
		return sendResult{kind: sendSent, apnsID: apnsID}
	}
	return w.classify(ctx, err, dev, c, func() { w.clearTokens(dev.ID, store.TokenAlert, store.TokenBackground) })
}

func (w *Worker) clearTokens(deviceID string, kinds ...store.TokenKind) {
	for _, k := range kinds {
		if err := w.st.ClearDeviceToken(deviceID, k); err != nil {
			w.log.Error("clear token failed", "device_id", deviceID, "err", err)
		}
	}
}

// classify maps a relay error to an outcome and applies verdicts that change hub
// state. onGone forgets the specific token that was used when the relay says it
// is dead (Unregistered).
func (w *Worker) classify(ctx context.Context, err error, dev store.DeviceWithPairing, c *envCache, onGone func()) sendResult {
	var rl *relay.RateLimitedError
	var he *relay.HTTPError
	switch {
	case errors.Is(err, relay.ErrUnregistered):
		// The token is dead: forget it so the device is skipped from now on.
		onGone()
		return sendResult{kind: sendFailed, errCode: "unregistered"}
	case errors.Is(err, relay.ErrPairingRevoked):
		c.revoked[dev.Pairing.ID] = true
		changed, rerr := w.st.RevokePairing(dev.Pairing.ID)
		if rerr != nil {
			w.log.Error("revoke pairing failed", "pairing_id", dev.Pairing.ID, "err", rerr)
		} else if changed {
			emit(ctx, w.wh, webhook.EventPairingRevoked, map[string]string{
				"pairing_id": dev.Pairing.ID, "external_user_id": dev.Pairing.ExternalUserID, "by": "relay",
			})
		}
		return sendResult{kind: sendFailed, errCode: "pairing_revoked"}
	case errors.Is(err, relay.ErrTokenExpired):
		return sendResult{kind: sendFailed, errCode: "token_expired"}
	case errors.As(err, &rl):
		c.limited[dev.Pairing.ID] = max(c.limited[dev.Pairing.ID], rl.RetryAfter)
		return sendResult{kind: sendRetry, errCode: "rate_limited", retryAfter: rl.RetryAfter}
	case errors.As(err, &he) && he.Status >= 400 && he.Status < 500 && he.Status != http.StatusTooManyRequests:
		return sendResult{kind: sendFailed, errCode: he.Code}
	default:
		// 5xx, network errors, anything unknown: try again later.
		w.log.Warn("relay push failed", "device_id", dev.ID, "err", err)
		return sendResult{kind: sendRetry, errCode: "relay_unavailable"}
	}
}

func (w *Worker) sendQueued(ctx context.Context) error {
	msgs, err := w.st.DueMessages(store.MsgQueued, w.now(), batchSize)
	if err != nil {
		return err
	}
	var errs []error
	limited := map[string]time.Duration{} // rate-limited pairings, shared by every message of this pass
	for _, m := range msgs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.processQueued(ctx, m, limited); err != nil {
			w.log.Error("process message failed", "message_id", m.ID, "err", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (w *Worker) processQueued(ctx context.Context, m store.Message, limited map[string]time.Duration) error {
	ch, err := w.st.GetChannel(m.ChannelID)
	if err != nil {
		return err
	}
	plaintext, err := crypto.UnwrapAtRest(w.cfg.MasterKey, m.PayloadWrapped)
	if err != nil {
		return err
	}
	dels, err := w.st.Deliveries(m.ID)
	if err != nil {
		return err
	}
	cache := newEnvCache()
	cache.limited = limited
	var retryAfter time.Duration
	for _, d := range dels {
		if d.Status != store.DelQueued {
			continue
		}
		dev, err := w.st.GetDevice(d.DeviceID)
		if err != nil {
			return err
		}
		res := w.pushAlert(ctx, m, ch, plaintext, dev, cache)
		switch res.kind {
		case sendSent:
			d.Status, d.APNsID, d.Error = store.DelSent, res.apnsID, ""
		case sendFailed:
			d.Status, d.Error = store.DelFailed, res.errCode
		case sendSkipped:
			d.Status, d.Error = store.DelSkipped, res.errCode
		case sendRetry:
			retryAfter = max(retryAfter, res.retryAfter)
			d.Error = res.errCode
		}
		if err := w.st.UpsertDelivery(d); err != nil {
			return err
		}
	}
	return w.finalizeQueued(ctx, m, retryAfter)
}

// finalizeQueued decides what a queued message becomes once its deliveries were attempted.
func (w *Worker) finalizeQueued(ctx context.Context, m store.Message, retryAfter time.Duration) error {
	dels, err := w.st.Deliveries(m.ID)
	if err != nil {
		return err
	}
	now := w.now()
	pending := 0
	for _, d := range dels {
		if d.Status == store.DelQueued {
			pending++
		}
	}
	if pending > 0 {
		if now.Sub(m.CreatedAt) <= maxQueueAge {
			return w.st.ScheduleMessage(m.ID, now.Add(max(retryDelay, retryAfter)))
		}
		for _, d := range dels {
			if d.Status == store.DelQueued {
				d.Status, d.Error = store.DelFailed, "timeout"
				if err := w.st.UpsertDelivery(d); err != nil {
					return err
				}
			}
		}
	}
	sent := 0
	for _, d := range dels {
		if d.Status == store.DelSent {
			sent++
		}
	}
	if sent == 0 {
		changed, err := w.st.TransitionMessage(m.ID, store.MsgQueued, store.MsgUndeliverable)
		if err != nil {
			return err
		}
		if changed {
			emit(ctx, w.wh, webhook.EventMessageUndeliverable, map[string]any{
				"message_id": m.ID, "external_user_id": m.ExternalUserID, "channel": m.ChannelID, "reason": "all_deliveries_failed",
			})
		}
		return nil
	}
	var changed bool
	if m.Level == level.Page {
		changed, err = w.st.StartPaging(m.ID, now.Add(time.Duration(m.PageIntervalS)*time.Second))
	} else {
		changed, err = w.st.TransitionMessage(m.ID, store.MsgQueued, store.MsgDelivered)
	}
	if err != nil {
		return err
	}
	if changed {
		emit(ctx, w.wh, webhook.EventMessageDelivered, map[string]any{
			"message_id": m.ID, "external_user_id": m.ExternalUserID, "channel": m.ChannelID, "level": m.Level, "devices": sent,
		})
	}
	return nil
}

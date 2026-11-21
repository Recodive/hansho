package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/recodive/hansho/hub/internal/crypto"
	"github.com/recodive/hansho/hub/internal/level"
	"github.com/recodive/hansho/hub/internal/relay"
	"github.com/recodive/hansho/hub/internal/store"
	"github.com/recodive/hansho/hub/internal/webhook"
)

// runPages resends due pages and expires exhausted ones. attempts counts
// resends: after PageMaxRetries resends the page gets one more interval to be
// acknowledged, then expires without another push.
func (w *Worker) runPages(ctx context.Context) error {
	msgs, err := w.st.DueMessages(store.MsgPaging, w.now(), batchSize)
	if err != nil {
		return err
	}
	var errs []error
	for _, m := range msgs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.processPage(ctx, m); err != nil {
			w.log.Error("process page failed", "message_id", m.ID, "err", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (w *Worker) processPage(ctx context.Context, m store.Message) error {
	ch, err := w.st.GetChannel(m.ChannelID)
	if err != nil {
		return err
	}
	// A user who lost access to the channel is not paged again.
	allowed, err := w.canAccess(m.ExternalUserID, ch)
	if err != nil {
		return err
	}
	if !ch.DeletedAt.IsZero() || m.Attempts >= m.PageMaxRetries || !allowed {
		changed, err := w.st.TransitionMessage(m.ID, store.MsgPaging, store.MsgExpired)
		if err != nil {
			return err
		}
		if changed {
			emit(ctx, w.wh, webhook.EventMessageExpired, map[string]any{
				"message_id": m.ID, "external_user_id": m.ExternalUserID, "channel": m.ChannelID, "attempts": m.Attempts,
			})
		}
		return nil
	}
	// Claim the resend first: it fails if the page was acknowledged meanwhile.
	next := w.now().Add(time.Duration(m.PageIntervalS) * time.Second)
	claimed, err := w.st.BumpPageAttempt(m.ID, next)
	if err != nil || !claimed {
		return err
	}
	plaintext, err := crypto.UnwrapAtRest(w.cfg.MasterKey, m.PayloadWrapped)
	if err != nil {
		return err
	}
	devices, err := w.st.ActiveDevicesForUser(m.ExternalUserID)
	if err != nil {
		return err
	}
	cache := newEnvCache()
	var sentTo []store.DeviceWithPairing
	for _, dev := range devices {
		res := w.pushAlert(ctx, m, ch, plaintext, dev, cache)
		d := store.Delivery{MessageID: m.ID, DeviceID: dev.ID, APNsID: res.apnsID, Error: res.errCode}
		switch res.kind {
		case sendSent:
			d.Status = store.DelSent
			sentTo = append(sentTo, dev)
		case sendSkipped:
			continue // no token / revoked: leave any earlier record alone
		case sendRetry:
			continue // the next resend retries it
		default:
			d.Status = store.DelFailed
		}
		if err := w.st.UpsertDelivery(d); err != nil {
			return err
		}
	}
	// The page may have been acknowledged while we were pushing; make sure the
	// devices that just received it are told to stop.
	if cur, err := w.st.GetMessage(m.ID); err == nil && cur.Status == store.MsgAcked && len(sentTo) > 0 {
		if _, err := w.CancelPage(ctx, cur); err != nil {
			return err
		}
	}
	return nil
}

// CancelPage tells every active device of the message's user that the page
// was acknowledged. It is an alert push (a silent background push cannot wake a
// force-quit app) collapsed onto the page banner; its encrypted plaintext tells
// the notification extension to cancel the alarm. It returns how many devices
// accepted it.
func (w *Worker) CancelPage(ctx context.Context, m store.Message) (int, error) {
	ch, err := w.st.GetChannel(m.ChannelID)
	if err != nil {
		return 0, err
	}
	devices, err := w.st.ActiveDevicesForUser(m.ExternalUserID)
	if err != nil {
		return 0, err
	}
	title := "Page"
	if pt, err := crypto.UnwrapAtRest(w.cfg.MasterKey, m.PayloadWrapped); err == nil {
		var orig Plaintext
		if json.Unmarshal(pt, &orig) == nil && orig.Title != "" {
			title = orig.Title
		}
	}
	plaintext, err := json.Marshal(Plaintext{
		Cancel: m.ID, Title: "Acknowledged", Body: title + " was acknowledged", Channel: m.ChannelID, Level: level.Passive,
	})
	if err != nil {
		return 0, err
	}
	cache := newEnvCache()
	sent := 0
	for _, dev := range devices {
		if dev.SealedAlert == "" || !dev.RevokedAt.IsZero() || !dev.Pairing.RevokedAt.IsZero() || cache.revoked[dev.Pairing.ID] {
			continue
		}
		env, err := w.envelope(cache, dev.Pairing, m.ID, plaintext)
		if err != nil {
			w.log.Error("seal cancel failed", "message_id", m.ID, "pairing_id", dev.Pairing.ID, "err", err)
			continue
		}
		payload, err := BuildCancelPayload(w.cfg.DisplayName, ch, env)
		if err != nil {
			w.log.Error("build cancel failed", "message_id", m.ID, "err", err)
			continue
		}
		_, err = w.relay.Push(ctx, relay.PushRequest{
			Sealed: dev.SealedAlert, Kind: "alert", Priority: 5, CollapseID: m.ID, Payload: payload,
		})
		if err != nil {
			if ctx.Err() != nil {
				return sent, ctx.Err()
			}
			w.classify(ctx, err, dev, cache, func() { w.clearTokens(dev.ID, store.TokenAlert, store.TokenBackground) })
			continue
		}
		sent++
	}
	return sent, nil
}

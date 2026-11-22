// Package webhook reports hub events to the sender's backend through a signed,
// retried outbox.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/recodive/hansho/hub/internal/brand"
	"github.com/recodive/hansho/hub/internal/store"
)

// Event names (exact strings sent to the backend).
const (
	EventPairingCreated       = "pairing.created"
	EventPairingRevoked       = "pairing.revoked"
	EventMessageDelivered     = "message.delivered"
	EventMessageUndeliverable = "message.undeliverable"
	EventMessageAcked         = "message.acked"
	EventMessageDismissed     = "message.dismissed"
	EventMessageExpired       = "message.expired"
	EventActionTaken          = "action"
	EventActivityEnded        = "activity.ended"
)

var knownEvents = map[string]bool{
	EventPairingCreated: true, EventPairingRevoked: true, EventMessageDelivered: true,
	EventMessageUndeliverable: true, EventMessageAcked: true, EventMessageDismissed: true, EventMessageExpired: true,
	EventActionTaken: true, EventActivityEnded: true,
}

const (
	// retention is how long finished (delivered or dropped) outbox rows are kept; the outbox is pruned once a minute.
	retention     = 7 * 24 * time.Hour
	pruneInterval = time.Minute
	baseBackoff   = 10 * time.Second
	maxAttempts   = 8
	batchSize     = 100
	requestTimout = 10 * time.Second
)

// SignatureHeader carries "t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>".
var SignatureHeader = brand.Header("Webhook-Signature")

// Dispatcher writes events to the outbox and delivers due rows.
type Dispatcher struct {
	st     *store.Store
	url    string
	secret []byte
	hc     *http.Client
	now    func() time.Time

	lastPrune time.Time // only touched by RunOnce
}

// New creates a dispatcher. An empty url disables webhooks. hc and now may be nil.
func New(st *store.Store, url string, secret []byte, hc *http.Client, now func() time.Time) *Dispatcher {
	if hc == nil {
		// A redirect would carry the signed body to a host the operator never configured.
		hc = &http.Client{Timeout: requestTimout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if now == nil {
		now = time.Now
	}
	return &Dispatcher{st: st, url: url, secret: secret, hc: hc, now: now}
}

// Emit queues an event. It is a no-op when no webhook URL is configured.
func (d *Dispatcher) Emit(_ context.Context, event string, payload any) error {
	if d == nil || d.url == "" {
		return nil
	}
	if !knownEvents[event] {
		return fmt.Errorf("webhook: unknown event %q", event)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = d.st.EnqueueWebhook(event, b, d.now())
	return err
}

type body struct {
	ID        string          `json:"id"`
	Event     string          `json:"event"`
	CreatedAt int64           `json:"created_at"`
	Data      json.RawMessage `json:"data"`
}

// RunOnce delivers every due row and returns how many were accepted (2xx).
func (d *Dispatcher) RunOnce(ctx context.Context) (sent int, err error) {
	if d == nil || d.url == "" {
		return 0, nil
	}
	now := d.now()
	d.prune(now)
	due, err := d.st.DueWebhooks(now, batchSize)
	if err != nil {
		return 0, err
	}
	for _, w := range due {
		if ctx.Err() != nil {
			return sent, ctx.Err()
		}
		if d.deliver(ctx, w) {
			sent++
		}
	}
	return sent, nil
}

// prune deletes finished rows older than the retention period, at most once per pruneInterval.
func (d *Dispatcher) prune(now time.Time) {
	if now.Sub(d.lastPrune) < pruneInterval {
		return
	}
	d.lastPrune = now
	if _, err := d.st.PruneWebhooks(now.Add(-retention)); err != nil {
		slog.Error("webhook: prune failed", "err", err)
	}
}

// deliver makes one attempt. The signature timestamp and the stored times come from the clock at the attempt, not from
// the start of the batch: after a slow receiver, later rows would otherwise carry a stale timestamp.
func (d *Dispatcher) deliver(ctx context.Context, w store.Webhook) bool {
	now := d.now()
	payload, _ := json.Marshal(body{ID: strconv.FormatInt(w.ID, 10), Event: w.Event, CreatedAt: w.CreatedAt.Unix(), Data: w.PayloadJSON})
	ok := d.post(ctx, payload, now)
	switch {
	case ok:
		if err := d.st.MarkWebhookDelivered(w.ID, now); err != nil {
			slog.Error("webhook: mark delivered failed", "id", w.ID, "err", err)
		}
	case w.Attempts+1 >= maxAttempts:
		slog.Error("webhook: dropped after max attempts", "id", w.ID, "event", w.Event, "attempts", w.Attempts+1)
		if err := d.st.DropWebhook(w.ID, now); err != nil {
			slog.Error("webhook: drop failed", "id", w.ID, "err", err)
		}
	default:
		next := now.Add(baseBackoff << w.Attempts)
		if err := d.st.RescheduleWebhook(w.ID, next); err != nil {
			slog.Error("webhook: reschedule failed", "id", w.ID, "err", err)
		}
	}
	return ok
}

func (d *Dispatcher) post(ctx context.Context, payload []byte, now time.Time) bool {
	ctx, cancel := context.WithTimeout(ctx, requestTimout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(payload))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	if len(d.secret) > 0 {
		t := strconv.FormatInt(now.Unix(), 10)
		m := hmac.New(sha256.New, d.secret)
		m.Write([]byte(t + "." + string(payload)))
		req.Header.Set(SignatureHeader, "t="+t+",v1="+hex.EncodeToString(m.Sum(nil)))
	}
	resp, err := d.hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

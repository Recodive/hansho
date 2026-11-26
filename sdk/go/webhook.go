package hansho

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// WebhookSignatureHeader carries "t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>".
const WebhookSignatureHeader = HeaderPrefix + "Webhook-Signature"

// DefaultTolerance is how far a webhook timestamp may be from now.
const DefaultTolerance = 300 * time.Second

// Verification errors; test with errors.Is.
var (
	// ErrBadSignature: no v1 signature matches the body and secret.
	ErrBadSignature = errors.New("hansho: webhook signature does not match")
	// ErrStale: the signature is valid but its timestamp is outside the tolerance (a replay).
	ErrStale = errors.New("hansho: webhook timestamp outside tolerance")
	// ErrMalformedHeader: the signature header is missing or unparseable.
	ErrMalformedHeader = errors.New("hansho: malformed webhook signature header")
	// ErrMalformedBody: the signature is valid but the body is not a webhook envelope.
	ErrMalformedBody = errors.New("hansho: malformed webhook body")
)

// Event is a verified webhook.
type Event struct {
	// ID is the hub's event id.
	ID string
	// Type is one of the Event* constants, or EventUnknown.
	Type string
	// Name is the event name as sent; it differs from Type only for EventUnknown.
	Name      string
	CreatedAt time.Time
	// Payload is the event's "data" object, untouched.
	Payload json.RawMessage
	// Raw is the verified request body.
	Raw []byte
}

// VerifyWebhook checks the X-Hansho-Webhook-Signature header against the raw
// request body. body must be the bytes exactly as received: do not parse and
// re-serialise it first. A zero now means time.Now(); a tolerance <= 0 means
// DefaultTolerance. With several v1 values in the header (secret rotation) any
// match is accepted.
func VerifyWebhook(secret []byte, header string, body []byte, now time.Time, tolerance time.Duration) (Event, error) {
	if len(secret) == 0 {
		return Event{}, errors.New("hansho: webhook secret is empty")
	}
	ts, sigs, err := parseSignatureHeader(header)
	if err != nil {
		return Event{}, err
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	want := mac.Sum(nil)
	match := false
	for _, s := range sigs {
		got, err := hex.DecodeString(s)
		if err != nil {
			continue
		}
		if hmac.Equal(got, want) {
			match = true
		}
	}
	if !match {
		return Event{}, ErrBadSignature
	}
	if now.IsZero() {
		now = time.Now()
	}
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	unix, _ := strconv.ParseInt(ts, 10, 64) // validated by parseSignatureHeader
	if d := now.Sub(time.Unix(unix, 0)); d > tolerance || d < -tolerance {
		return Event{}, ErrStale
	}
	return decodeEvent(body)
}

// parseSignatureHeader returns the t value (as sent) and the non-empty v1 values.
func parseSignatureHeader(h string) (ts string, sigs []string, err error) {
	if strings.TrimSpace(h) == "" {
		return "", nil, ErrMalformedHeader
	}
	for _, part := range strings.Split(h, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return "", nil, ErrMalformedHeader
		}
		switch k {
		case "t":
			if ts != "" {
				return "", nil, ErrMalformedHeader
			}
			ts = v
		case "v1":
			if v != "" {
				sigs = append(sigs, v)
			}
		}
	}
	if _, perr := strconv.ParseInt(ts, 10, 64); perr != nil || len(sigs) == 0 {
		return "", nil, ErrMalformedHeader
	}
	return ts, sigs, nil
}

// flexString accepts a JSON string or number.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexString(n.String())
	return nil
}

func decodeEvent(body []byte) (Event, error) {
	var env struct {
		ID        flexString      `json:"id"`
		Event     string          `json:"event"`
		CreatedAt int64           `json:"created_at"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Event == "" {
		return Event{}, ErrMalformedBody
	}
	ev := Event{ID: string(env.ID), Type: env.Event, Name: env.Event, CreatedAt: time.Unix(env.CreatedAt, 0), Payload: env.Data, Raw: body}
	if !isEvent(env.Event) {
		ev.Type = EventUnknown
	}
	return ev, nil
}

// Data decodes the payload into the typed struct for e.Type (PairingCreated,
// MessageAcked, ...). For EventUnknown it returns the payload as json.RawMessage.
func (e Event) Data() (any, error) {
	v, ok, err := decodePayload(e.Type, e.Payload)
	if !ok {
		return e.Payload, nil
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

const maxWebhookBody = 1 << 20

// NewWebhookHandler returns an http.Handler for the hub's webhook endpoint. It
// reads the raw body, verifies it and calls handle. It answers 400 for a
// request that does not verify, and 500 if handle returns an error, so the hub
// retries. Mount it with mux.Handle("POST /hansho-hook", ...).
func NewWebhookHandler(secret []byte, handle func(ctx context.Context, e Event) error) http.Handler {
	return newWebhookHandler(secret, 0, time.Now, handle)
}

func newWebhookHandler(secret []byte, tolerance time.Duration, now func() time.Time, handle func(context.Context, Event) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
		if err != nil {
			http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
			return
		}
		ev, err := VerifyWebhook(secret, r.Header.Get(WebhookSignatureHeader), body, now(), tolerance)
		if err != nil {
			http.Error(w, "invalid webhook", http.StatusBadRequest)
			return
		}
		if err := handle(r.Context(), ev); err != nil {
			http.Error(w, "handler failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

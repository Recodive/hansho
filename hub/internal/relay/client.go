// Package relay is the hub's signed client for the hansho relay.
// See relay/README.md for the contract.
package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/recodive/hansho/hub/internal/crypto"
)

var (
	// ErrUnregistered: APNs no longer knows the device token (410 Unregistered).
	ErrUnregistered = errors.New("relay: device token unregistered")
	// ErrPairingRevoked: the relay holds a revocation for the pairing (410 PairingRevoked).
	ErrPairingRevoked = errors.New("relay: pairing revoked")
	// ErrRateLimited: 429 after all retries. See RateLimitedError for RetryAfter.
	ErrRateLimited = errors.New("relay: rate limited")
	// ErrTokenExpired: the sealed token's key version was rotated out (401 token_expired).
	ErrTokenExpired = errors.New("relay: sealed token expired")
)

// RateLimitedError carries the relay's suggested wait; errors.Is(err, ErrRateLimited) holds.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string        { return ErrRateLimited.Error() }
func (e *RateLimitedError) Is(target error) bool { return target == ErrRateLimited }

// HTTPError is any other non-success relay response.
type HTTPError struct {
	Status int
	Code   string // the relay's "error" or "reason" field
}

func (e *HTTPError) Error() string { return fmt.Sprintf("relay: http %d %s", e.Status, e.Code) }

// PushRequest is one push to forward to APNs. Payload must be a JSON object.
type PushRequest struct {
	Sealed, Kind string
	Priority     int
	Expiration   *time.Time
	CollapseID   string
	Payload      json.RawMessage
}

// noWaitKey marks a context under which a 429 is not retried in-line (see NoWait).
type noWaitKey struct{}

// NoWait returns a context under which a 429 comes back at once as a RateLimitedError instead of being retried after
// an in-line sleep, for callers that reschedule the work themselves (the delivery worker).
func NoWait(ctx context.Context) context.Context { return context.WithValue(ctx, noWaitKey{}, true) }

const (
	maxRateLimitRetries = 3
	maxResponseBytes    = 64 << 10
	maxSignatureDrift   = 120 // seconds a timestamp may be bumped to keep signatures unique
)

// Client talks to the relay, signing requests with the hub's Ed25519 key.
type Client struct {
	base  string
	hubID string
	priv  ed25519.PrivateKey
	hc    *http.Client

	now   func() time.Time
	sleep func(time.Duration)

	mu   sync.Mutex
	sigs map[string]int64 // signatures used recently -> their timestamp
}

// New creates a client. hc may be nil.
func New(baseURL, hubID string, priv ed25519.PrivateKey, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		base: strings.TrimRight(baseURL, "/"), hubID: hubID, priv: priv, hc: hc,
		now: time.Now, sleep: time.Sleep, sigs: map[string]int64{},
	}
}

// sign builds auth headers. The relay rejects a repeated signature as a
// replay and Ed25519 is deterministic, so identical requests within one second
// get their timestamp bumped until the signature is new.
func (c *Client) sign(method, path string, body []byte) http.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().Unix()
	for k, ts := range c.sigs {
		if ts < now-60 {
			delete(c.sigs, k)
		}
	}
	var h http.Header
	for d := int64(0); ; d++ {
		h = crypto.SignHubRequest(c.priv, c.hubID, time.Unix(now+d, 0), method, path, body)
		sig := h.Get("X-Hansho-Signature")
		if _, dup := c.sigs[sig]; !dup || d >= maxSignatureDrift {
			c.sigs[sig] = now + d
			return h
		}
	}
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, signed bool) (*http.Response, []byte, error) {
	u, err := url.Parse(c.base + path)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	if signed {
		for k, v := range c.sign(method, u.Path, body) {
			req.Header[k] = v
		}
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	return resp, b, err
}

// call performs a request, retrying 429s with backoff (unless ctx is NoWait), and maps failures to typed errors.
func (c *Client) call(ctx context.Context, method, path string, body []byte, signed bool) ([]byte, error) {
	backoff := 500 * time.Millisecond
	for attempt := 0; ; attempt++ {
		resp, b, err := c.do(ctx, method, path, body, signed)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return b, nil
		}
		var e struct{ Error, Reason string }
		_ = json.Unmarshal(b, &e)
		code := e.Error
		if code == "" {
			code = e.Reason
		}
		switch {
		case resp.StatusCode == http.StatusGone && e.Reason == "Unregistered":
			return nil, ErrUnregistered
		case resp.StatusCode == http.StatusGone && e.Reason == "PairingRevoked":
			return nil, ErrPairingRevoked
		case resp.StatusCode == http.StatusUnauthorized && e.Error == "token_expired":
			return nil, ErrTokenExpired
		case resp.StatusCode == http.StatusTooManyRequests:
			wait := backoff
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
				wait = time.Duration(s) * time.Second
			}
			if attempt >= maxRateLimitRetries || ctx.Value(noWaitKey{}) != nil {
				return nil, &RateLimitedError{RetryAfter: wait}
			}
			c.sleep(wait)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			backoff *= 2
			continue
		}
		return nil, &HTTPError{Status: resp.StatusCode, Code: code}
	}
}

// Push forwards a push through the relay and returns the APNs id.
func (c *Client) Push(ctx context.Context, p PushRequest) (string, error) {
	req := struct {
		Sealed     string          `json:"sealed"`
		Kind       string          `json:"kind"`
		Priority   int             `json:"priority"`
		Expiration *int64          `json:"expiration,omitempty"`
		CollapseID string          `json:"collapse_id,omitempty"`
		Payload    json.RawMessage `json:"payload"`
	}{Sealed: p.Sealed, Kind: p.Kind, Priority: p.Priority, CollapseID: p.CollapseID, Payload: p.Payload}
	if p.Expiration != nil {
		e := p.Expiration.Unix()
		req.Expiration = &e
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	out, err := c.call(ctx, http.MethodPost, "/v1/push", body, true)
	if err != nil {
		return "", err
	}
	var res struct {
		APNsID string `json:"apns_id"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("relay: bad push response: %w", err)
	}
	return res.APNsID, nil
}

// Revoke tells the relay to revoke the pairing behind a sealed token.
func (c *Client) Revoke(ctx context.Context, sealed string) error {
	body, _ := json.Marshal(map[string]string{"sealed": sealed})
	_, err := c.call(ctx, http.MethodPost, "/v1/revocations", body, true)
	return err
}

// Register announces the hub (unsigned) and returns its hub_id and domain challenge.
func (c *Client) Register(ctx context.Context, domain string, pub ed25519.PublicKey) (hubID, challenge string, err error) {
	body, _ := json.Marshal(map[string]string{
		"domain":     domain,
		"public_key": base64.RawURLEncoding.EncodeToString(pub),
	})
	out, err := c.call(ctx, http.MethodPost, "/v1/hubs", body, false)
	if err != nil {
		return "", "", err
	}
	var res struct {
		HubID     string `json:"hub_id"`
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", "", fmt.Errorf("relay: bad register response: %w", err)
	}
	return res.HubID, res.Challenge, nil
}

// Verify asks the relay to check the hub's domain challenge.
func (c *Client) Verify(ctx context.Context) error {
	_, err := c.call(ctx, http.MethodPost, "/v1/hubs/"+url.PathEscape(c.hubID)+"/verify", nil, true)
	return err
}

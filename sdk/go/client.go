// Package hansho is the official Go client for a Hansho hub's sender API: pair
// users, define channels, templates and sounds, send messages, pages, Live
// Activities and widget feeds, and verify the hub's webhooks.
//
// The package depends only on the standard library.
package hansho

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Name is the product codename; HeaderPrefix is the prefix of every header the
// hub sends.
const (
	Name         = "hansho"
	HeaderPrefix = "X-Hansho-"
)

const maxResponseBody = 4 << 20

// Client talks to one hub. It is safe for concurrent use.
type Client struct {
	baseURL    string
	apiKey     string
	hc         *http.Client
	userAgent  string
	maxRetries int

	// sleep and now are replaced by tests.
	sleep func(context.Context, time.Duration) error
	now   func() time.Time
}

// noRedirect surfaces a 3xx response as an API error instead of following it.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client (timeouts, proxies, TLS). The default has
// a 30 second timeout and does not follow redirects (the hub never redirects, and
// following one would hand the API key to another host); a client you pass in
// keeps its own redirect policy.
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.hc = hc } }

// WithUserAgent overrides the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// WithMaxRetries sets how often a retryable call (GET, PUT, DELETE on 429 or
// 503; a message with an IdempotencyKey) is repeated. The default is 3; zero
// disables retries.
func WithMaxRetries(n int) Option {
	return func(c *Client) {
		if n < 0 {
			n = 0
		}
		c.maxRetries = n
	}
}

// New returns a client for the hub at baseURL (its public URL) authenticating
// with apiKey, as printed by `hub apikey create`.
func New(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		hc:         &http.Client{Timeout: 30 * time.Second, CheckRedirect: noRedirect},
		userAgent:  Name + "-go/" + Version,
		maxRetries: defaultMaxRetries,
		sleep:      sleepCtx,
		now:        time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// String and GoString keep the API key out of logs and %v output.
func (c *Client) String() string   { return "hansho.Client{" + c.baseURL + "}" }
func (c *Client) GoString() string { return c.String() }

type request struct {
	method      string
	path        string // escaped, relative to the base URL
	body        []byte
	contentType string
	// retry allows repeating the call on 429 and 503.
	retry bool
	// header carries extra headers (Idempotency-Key).
	header map[string]string
}

// seg path-escapes one URL path segment. The dot segments are percent-encoded
// as well so an id can never climb out of its collection.
func seg(s string) string {
	switch s {
	case ".":
		return "%2E"
	case "..":
		return "%2E%2E"
	}
	return url.PathEscape(s)
}

// jsonRequest marshals v as the body.
func jsonRequest(method, path string, v any) (request, error) {
	r := request{method: method, path: path, retry: idempotent(method)}
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			return r, fmt.Errorf("hansho: encoding request: %w", err)
		}
		r.body, r.contentType = b, "application/json"
	}
	return r, nil
}

// call sends the request and, for a 2xx answer with a body, decodes it into out
// (which may be nil).
func (c *Client) call(ctx context.Context, r request, out any) error {
	for attempt := 0; ; attempt++ {
		status, header, body, err := c.roundTrip(ctx, r)
		if err != nil {
			return &NetworkError{Method: r.method, Path: r.path, Err: err}
		}
		if status >= 200 && status < 300 {
			if out == nil || len(bytes.TrimSpace(body)) == 0 {
				return nil
			}
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("hansho: %s %s: decoding response: %w", r.method, r.path, err)
			}
			return nil
		}
		apiErr := c.apiError(r, status, header, body)
		if !r.retry || !retryableStatus(status) || attempt >= c.maxRetries {
			return apiErr
		}
		wait := apiErr.RetryAfter
		if wait == 0 {
			wait = backoff(attempt)
		}
		if wait > maxRetryWait {
			return apiErr
		}
		if err := c.sleep(ctx, wait); err != nil {
			return &NetworkError{Method: r.method, Path: r.path, Err: err}
		}
	}
}

func (c *Client) roundTrip(ctx context.Context, r request) (int, http.Header, []byte, error) {
	var body io.Reader
	if r.body != nil {
		body = bytes.NewReader(r.body)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, c.baseURL+r.path, body)
	if err != nil {
		return 0, nil, nil, c.redact(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	for k, v := range r.header {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, nil, c.redact(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return 0, nil, nil, c.redact(err)
	}
	return resp.StatusCode, resp.Header, b, nil
}

// redactedError replaces the API key in an error text. The key is only ever sent
// in a header, so this is defence in depth.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

func (c *Client) redact(err error) error {
	if c.apiKey == "" || !strings.Contains(err.Error(), c.apiKey) {
		return err
	}
	return &redactedError{strings.ReplaceAll(err.Error(), c.apiKey, "[redacted]"), err}
}

func (c *Client) apiError(r request, status int, h http.Header, body []byte) *APIError {
	e := &APIError{Method: r.method, Path: r.path, Status: status}
	var payload struct {
		Error   string `json:"error"`
		Field   string `json:"field"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &payload) == nil {
		e.Code, e.Field = payload.Error, payload.Field
		e.Message = payload.Message
		if c.apiKey != "" {
			e.Message = strings.ReplaceAll(e.Message, c.apiKey, "[redacted]")
		}
	}
	e.RetryAfter = parseRetryAfter(h.Get("Retry-After"), c.now())
	return e
}

package hansho

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHeaders(t *testing.T) {
	h := newHub(t, nil, WithUserAgent("acme-backend/2"))
	if err := h.client.PutChannel(ctx(), "ops", specChannel); err != nil {
		t.Fatal(err)
	}
	r := h.requests()[0]
	if r.Header.Get("User-Agent") != "acme-backend/2" || r.Header.Get("Accept") != "application/json" ||
		r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer "+testKey {
		t.Fatalf("headers %v", r.Header)
	}
	h = newHub(t, nil)
	_ = h.client.EndActivity(ctx(), "a")
	if ua := h.requests()[0].Header.Get("User-Agent"); !strings.HasPrefix(ua, "hansho-go/") {
		t.Fatalf("default user agent %q", ua)
	}
	if h.requests()[0].Header.Get("Content-Type") != "" {
		t.Fatal("bodiless request must not set Content-Type")
	}
}

func TestBaseURLTrailingSlashAndPrefix(t *testing.T) {
	h := newHub(t, nil)
	c := New(h.srv.URL+"/hansho/", testKey, WithHTTPClient(h.srv.Client()))
	if _, err := c.GetMessage(ctx(), "m1"); err != nil {
		t.Fatal(err)
	}
	if got := h.requests()[0].EscapedPath; got != "/hansho/api/v1/messages/m1" {
		t.Fatal(got)
	}
}

func TestConstants(t *testing.T) {
	if Name != "hansho" || HeaderPrefix != "X-Hansho-" {
		t.Fatal(Name, HeaderPrefix)
	}
}

func TestLevelValues(t *testing.T) {
	for l, w := range map[Level]string{LevelPassive: "passive", LevelActive: "active",
		LevelTimeSensitive: "time-sensitive", LevelCritical: "critical", LevelPage: "page"} {
		if string(l) != w || !l.Valid() {
			t.Errorf("%q", l)
		}
	}
	if Level("pager").Valid() {
		t.Error("pager is not a level")
	}
}

// Review focus 3: a field the SDK does not know is ignored.
func TestUnknownResponseFieldsIgnored(t *testing.T) {
	h := newHub(t, func(_ *recorded, w *respond) {
		w.Body = `{"message_id":"m","status":"delivered","level":"page","attempts":1,"deliveries":[{"device_id":"d","status":"sent","brand_new":{"x":[1]}}],"brand_new":"v","acked_at":"2026-11-08T12:05:00Z"}`
	})
	ms, err := h.client.GetMessage(ctx(), "m")
	if err != nil || ms.MessageID != "m" || len(ms.Deliveries) != 1 {
		t.Fatalf("%+v %v", ms, err)
	}
}

// Review focus 4: PUT 429 with Retry-After is retried and honoured.
func TestPutRetriesAndHonoursRetryAfter(t *testing.T) {
	var n atomic.Int32
	h := newHub(t, func(_ *recorded, w *respond) {
		if n.Add(1) <= 2 {
			w.Status, w.Headers, w.Body = 429, map[string]string{"Retry-After": "2"}, `{"error":"rate_limited"}`
		}
	})
	if err := h.client.PutChannel(ctx(), "ops", specChannel); err != nil {
		t.Fatal(err)
	}
	if got := len(h.requests()); got != 3 {
		t.Fatalf("%d requests, want 3", got)
	}
	if w := h.waits(); len(w) != 2 || w[0] != 2*time.Second || w[1] != 2*time.Second {
		t.Fatalf("waits %v, want [2s 2s]", w)
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	var n atomic.Int32
	h := newHub(t, func(_ *recorded, w *respond) {
		if n.Add(1) == 1 {
			w.Status, w.Headers = 429, map[string]string{"Retry-After": time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)}
		}
	})
	if err := h.client.DeleteChannel(ctx(), "ops"); err != nil {
		t.Fatal(err)
	}
	if w := h.waits(); len(w) != 1 || w[0] < 20*time.Second || w[0] > 31*time.Second {
		t.Fatalf("waits %v", w)
	}
}

func TestRetriesStopAtMaxAndSurfaceError(t *testing.T) {
	h := newHub(t, func(_ *recorded, w *respond) {
		w.Status, w.Headers, w.Body = 429, map[string]string{"Retry-After": "1"}, `{"error":"rate_limited"}`
	})
	err := h.client.PutChannel(ctx(), "ops", specChannel)
	ae, ok := IsAPIError(err)
	if !ok || ae.Status != 429 || ae.RetryAfter != time.Second {
		t.Fatalf("%v", err)
	}
	if got := len(h.requests()); got != 4 { // 1 + 3 retries
		t.Fatalf("%d requests, want 4", got)
	}
	h = newHub(t, func(_ *recorded, w *respond) { w.Status = 429 }, WithMaxRetries(0))
	_ = h.client.PutChannel(ctx(), "ops", specChannel)
	if got := len(h.requests()); got != 1 {
		t.Fatalf("WithMaxRetries(0): %d requests", got)
	}
	h = newHub(t, func(_ *recorded, w *respond) { w.Status = 429 }, WithMaxRetries(1))
	_ = h.client.PutChannel(ctx(), "ops", specChannel)
	if got := len(h.requests()); got != 2 {
		t.Fatalf("WithMaxRetries(1): %d requests", got)
	}
}

func TestRetry503WithBackoffOnGetAndDelete(t *testing.T) {
	for _, call := range []func(*Client) error{
		func(c *Client) error { _, err := c.GetChannel(ctx(), "x"); return err },
		func(c *Client) error { return c.DeleteChannel(ctx(), "x") },
		func(c *Client) error { return c.EndActivity(ctx(), "x") },
	} {
		var n atomic.Int32
		h := newHub(t, func(_ *recorded, w *respond) {
			if n.Add(1) == 1 {
				w.Status, w.Body = 503, `{"error":"unavailable"}`
			}
		})
		if err := call(h.client); err != nil {
			t.Fatal(err)
		}
		if w := h.waits(); len(w) != 1 || w[0] < baseBackoff/2 || w[0] > baseBackoff {
			t.Fatalf("backoff waits %v", w)
		}
	}
}

func TestBackoffGrowsAndIsBounded(t *testing.T) {
	for i := 0; i < 50; i++ {
		for a, max := range []time.Duration{250, 500, 1000, 2000, 4000, 8000, 8000, 8000} {
			d := backoff(a)
			max *= time.Millisecond
			if d < max/2 || d > max {
				t.Fatalf("attempt %d: %v not in [%v,%v]", a, d, max/2, max)
			}
		}
	}
}

func TestOtherStatusesNotRetried(t *testing.T) {
	for _, st := range []int{400, 401, 404, 409, 500, 502} {
		h := newHub(t, func(_ *recorded, w *respond) { w.Status = st })
		if err := h.client.PutChannel(ctx(), "ops", specChannel); err == nil {
			t.Fatalf("%d: no error", st)
		}
		if got := len(h.requests()); got != 1 {
			t.Fatalf("%d: %d requests", st, got)
		}
	}
}

// The hub never redirects; following one would send the API key to another host.
func TestDefaultClientDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(target.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	err := New(srv.URL, testKey).PutChannel(ctx(), "ops", specChannel)
	if ae, ok := IsAPIError(err); !ok || ae.Status != http.StatusTemporaryRedirect {
		t.Fatalf("got %T %v", err, err)
	}
	if hits.Load() != 0 {
		t.Fatal("the redirect was followed")
	}
	// A client the caller supplies keeps its own policy.
	if err := New(srv.URL, testKey, WithHTTPClient(&http.Client{})).PutChannel(ctx(), "ops", specChannel); err != nil || hits.Load() != 1 {
		t.Fatalf("custom client: %v, %d hits", err, hits.Load())
	}
}

// Review focus 4: POST /messages 429 is surfaced untouched.
func TestPostMessageIsNotRetried(t *testing.T) {
	for _, st := range []int{429, 503} {
		h := newHub(t, func(_ *recorded, w *respond) {
			w.Status, w.Headers, w.Body = st, map[string]string{"Retry-After": "3"}, `{"error":"rate_limited"}`
		})
		_, err := h.client.SendMessage(ctx(), Message{ExternalUserID: "u", Channel: "c", Title: "t", Body: "b"})
		ae, ok := IsAPIError(err)
		if !ok || ae.Status != st || ae.Code != "rate_limited" || ae.RetryAfter != 3*time.Second {
			t.Fatalf("%d: %v", st, err)
		}
		if len(h.requests()) != 1 || len(h.waits()) != 0 {
			t.Fatalf("%d: %d requests, waits %v", st, len(h.requests()), h.waits())
		}
	}
}

func TestPostMessageWithIdempotencyKeyIsRetried(t *testing.T) {
	var n atomic.Int32
	h := newHub(t, func(_ *recorded, w *respond) {
		if n.Add(1) == 1 {
			w.Status, w.Headers, w.Body = 429, map[string]string{"Retry-After": "1"}, `{"error":"rate_limited"}`
		} else {
			w.Status, w.Body = 202, `{"message_id":"m1","status":"queued"}`
		}
	})
	res, err := h.client.SendMessage(ctx(), Message{ExternalUserID: "u", Channel: "c", Title: "t", Body: "b", IdempotencyKey: "order-7"})
	if err != nil || res.MessageID != "m1" {
		t.Fatalf("%+v %v", res, err)
	}
	reqs := h.requests()
	if len(reqs) != 2 || reqs[0].Header.Get("Idempotency-Key") != "order-7" || reqs[1].Header.Get("Idempotency-Key") != "order-7" {
		t.Fatalf("%d requests / headers", len(reqs))
	}
	if strings.Contains(string(reqs[0].Body), "order-7") || strings.Contains(string(reqs[0].Body), "idempotency") {
		t.Fatalf("idempotency key leaked into the body: %s", reqs[0].Body)
	}
	if string(reqs[0].Body) != string(reqs[1].Body) {
		t.Fatal("retry must resend the same body")
	}
}

func TestMessageWithoutKeySendsNoIdempotencyHeader(t *testing.T) {
	h := newHub(t, nil)
	_, _ = h.client.SendMessage(ctx(), Message{ExternalUserID: "u", Channel: "c", Title: "t", Body: "b"})
	if v := h.requests()[0].Header.Get("Idempotency-Key"); v != "" {
		t.Fatal(v)
	}
}

func TestPatchIsNotRetried(t *testing.T) {
	h := newHub(t, func(_ *recorded, w *respond) { w.Status = 429 })
	_, err := h.client.UpdateActivity(ctx(), "a", ActivityUpdate{Data: 1})
	if _, ok := IsAPIError(err); !ok || len(h.requests()) != 1 {
		t.Fatalf("%v / %d requests", err, len(h.requests()))
	}
}

func TestRetryAfterBeyondCapIsSurfaced(t *testing.T) {
	h := newHub(t, func(_ *recorded, w *respond) {
		w.Status, w.Headers = 429, map[string]string{"Retry-After": "3600"}
	})
	err := h.client.PutChannel(ctx(), "ops", specChannel)
	ae, ok := IsAPIError(err)
	if !ok || ae.RetryAfter != time.Hour || len(h.requests()) != 1 {
		t.Fatalf("%v / %d requests", err, len(h.requests()))
	}
}

// Review focus 5: ids with / ? space and unicode are path-escaped.
func TestIDsArePathEscaped(t *testing.T) {
	ids := []string{"a/b", "a?b=c", "a b", "ü", "日本", "a#b", "100%", "a/../b", "..", "a%2Fb"}
	for _, id := range ids {
		h := newHub(t, nil)
		c := h.client
		cases := []struct {
			prefix string
			call   func() error
		}{
			{"/api/v1/channels/", func() error { return c.PutChannel(ctx(), id, specChannel) }},
			{"/api/v1/channels/", func() error { _, err := c.GetChannel(ctx(), id); return err }},
			{"/api/v1/channels/", func() error { return c.DeleteChannel(ctx(), id) }},
			{"/api/v1/templates/", func() error { return c.PutTemplate(ctx(), id, 1, rawJSON(`{}`)) }},
			{"/api/v1/sounds/", func() error { _, err := c.PutSound(ctx(), id, "audio/wav", bytes.NewReader(wav)); return err }},
			{"/api/v1/feeds/", func() error { _, err := c.PutFeed(ctx(), id, Feed{TemplateID: "t", Data: 1}); return err }},
			{"/api/v1/messages/", func() error { _, err := c.GetMessage(ctx(), id); return err }},
			{"/api/v1/activities/", func() error { _, err := c.UpdateActivity(ctx(), id, ActivityUpdate{Data: 1}); return err }},
			{"/api/v1/activities/", func() error { return c.EndActivity(ctx(), id) }},
		}
		for _, tc := range cases {
			before := len(h.requests())
			if err := tc.call(); err != nil {
				t.Fatalf("%q: %v", id, err)
			}
			r := h.requests()[before]
			seg := strings.TrimPrefix(r.EscapedPath, tc.prefix)
			if strings.ContainsAny(seg, "/?# ") || seg == "." || seg == ".." {
				t.Errorf("%q: unescaped segment %q in %s", id, seg, r.RequestURI)
			}
			if r.Path != tc.prefix+id {
				t.Errorf("%q: server decoded path %q", id, r.Path)
			}
			if r.RawQuery != "" {
				t.Errorf("%q: leaked into the query: %q", id, r.RawQuery)
			}
		}
		before := len(h.requests())
		if err := c.Unpair(ctx(), id); err != nil {
			t.Fatal(err)
		}
		r := h.requests()[before]
		if want := "/api/v1/users/" + id + "/pairings"; r.Path != want || r.RawQuery != "" || strings.Count(r.EscapedPath, "/") != 5 {
			t.Errorf("unpair %q: path %q escaped %q", id, r.Path, r.EscapedPath)
		}
	}
	if h := newHub(t, nil); true {
		_ = h.client.PutChannel(ctx(), "ü x/y?z", specChannel)
		if got := h.requests()[0].RequestURI; got != "/api/v1/channels/%C3%BC%20x%2Fy%3Fz" {
			t.Fatal(got)
		}
	}
}

func TestEmptyIDsRejectedBeforeSending(t *testing.T) {
	h := newHub(t, nil)
	c := h.client
	for name, err := range map[string]error{
		"PutChannel":     c.PutChannel(ctx(), "", specChannel),
		"DeleteChannel":  c.DeleteChannel(ctx(), ""),
		"Unpair":         c.Unpair(ctx(), ""),
		"EndActivity":    c.EndActivity(ctx(), ""),
		"PutTemplate":    c.PutTemplate(ctx(), "", 1, rawJSON(`{}`)),
		"CreateTicket":   func() error { _, e := c.CreatePairingTicket(ctx(), ""); return e }(),
		"GetMessage":     func() error { _, e := c.GetMessage(ctx(), ""); return e }(),
		"UpdateActivity": func() error { _, e := c.UpdateActivity(ctx(), "", ActivityUpdate{}); return e }(),
	} {
		if !errors.Is(err, ErrEmptyID) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(h.requests()) != 0 {
		t.Fatal("requests were sent")
	}
}

func TestPutSoundTooLarge(t *testing.T) {
	h := newHub(t, nil)
	_, err := h.client.PutSound(ctx(), "big", "audio/wav", bytes.NewReader(make([]byte, 1<<20+1)))
	if !errors.Is(err, ErrSoundTooLarge) || len(h.requests()) != 0 {
		t.Fatalf("%v", err)
	}
	if _, err := h.client.PutSound(ctx(), "ok", "audio/wav", bytes.NewReader(make([]byte, 1<<20))); err != nil {
		t.Fatal(err)
	}
}

func TestContextCancellationAbortsRequest(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	c := New(srv.URL, testKey, WithHTTPClient(srv.Client()))
	cctx, cancel := context.WithTimeout(ctx(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.SendMessage(cctx, Message{ExternalUserID: "u", Channel: "c", Title: "t", Body: "b"})
	var ne *NetworkError
	if !errors.As(err, &ne) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %T %v", err, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("did not abort promptly")
	}
	pre, cancel2 := context.WithCancel(ctx())
	cancel2()
	if err := c.PutChannel(pre, "ops", specChannel); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

func TestContextCancellationAbortsBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(429)
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, testKey, WithHTTPClient(srv.Client())) // real sleep
	cctx, cancel := context.WithTimeout(ctx(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.PutChannel(cctx, "ops", specChannel)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}

func TestNetworkErrorIsDistinctAndLeaksNoKey(t *testing.T) {
	srv := httptest.NewServer(nil)
	url := srv.URL
	srv.Close() // connection refused
	c := New(url, testKey)
	err := c.PutChannel(ctx(), "ops", specChannel)
	var ne *NetworkError
	var ae *APIError
	if !errors.As(err, &ne) || errors.As(err, &ae) {
		t.Fatalf("got %T %v", err, err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("api key in %q", err)
	}
}

func TestAPIKeyNeverInErrors(t *testing.T) {
	// A hub that (wrongly) echoes the Authorization header must not leak it through the SDK.
	h := newHub(t, func(r *recorded, w *respond) {
		w.Status = 401
		w.Body = `{"error":"unauthorized","message":"bad key ` + r.Header.Get("Authorization") + `"}`
	})
	err := h.client.PutChannel(ctx(), "ops", specChannel)
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatalf("%v", err)
	}
	ae, _ := IsAPIError(err)
	if strings.Contains(ae.Message, testKey) {
		t.Fatalf("message retained the key: %q", ae.Message)
	}
	if strings.Contains(New("http://x", testKey).String(), testKey) {
		t.Fatal("Client.String leaks the key")
	}
}

func TestAPIErrorMessage(t *testing.T) {
	e := &APIError{Method: "POST", Path: "/api/v1/messages", Status: 400, Code: "invalid_request", Field: "title"}
	if got := e.Error(); got != "hansho: POST /api/v1/messages: 400 invalid_request (field title)" {
		t.Fatal(got)
	}
	e = &APIError{Method: "PUT", Path: "/x", Status: 429, Code: "rate_limited", RetryAfter: 2 * time.Second}
	if !strings.Contains(e.Error(), "retry after 2s") {
		t.Fatal(e.Error())
	}
}

func TestWireOmitsEmptyOptionals(t *testing.T) {
	b, _ := json.Marshal(Message{ExternalUserID: "u", Channel: "c", Title: "t", Body: "b"})
	if string(b) != `{"external_user_id":"u","channel":"c","title":"t","body":"b"}` {
		t.Fatal(string(b))
	}
	b, _ = json.Marshal(Channel{Name: "n", DefaultLevel: LevelActive, MaxLevel: LevelPage})
	if string(b) != `{"name":"n","default_level":"active","max_level":"page"}` {
		t.Fatal(string(b))
	}
}

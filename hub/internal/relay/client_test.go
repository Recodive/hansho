package relay

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

type recorded struct {
	Method, Path string
	Body         []byte
	Header       http.Header
}

type fakeRelay struct {
	t    *testing.T
	pub  ed25519.PublicKey
	hub  string
	mu   sync.Mutex
	reqs []recorded
	seen map[string]bool
	// handler produces the response for the n-th request (0-based) after auth passed.
	respond func(n int, w http.ResponseWriter, r *http.Request, body []byte)
	// unsigned endpoints skip auth.
	unsigned map[string]bool
}

func (f *fakeRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	n := len(f.reqs)
	f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, body, r.Header.Clone()})
	f.mu.Unlock()
	if !f.unsigned[r.URL.Path] {
		// Independent re-implementation of the relay's hubauth.Verify.
		ts := r.Header.Get("X-Hansho-Timestamp")
		sigStr := r.Header.Get("X-Hansho-Signature")
		sig, err := base64.RawURLEncoding.DecodeString(sigStr)
		sum := sha256.Sum256(body)
		msg := ts + "\n" + r.Method + "\n" + r.URL.Path + "\n" + hex.EncodeToString(sum[:])
		if err != nil || r.Header.Get("X-Hansho-Hub") != f.hub || !ed25519.Verify(f.pub, []byte(msg), sig) {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"unauthorized"}`)
			return
		}
		f.mu.Lock()
		dup := f.seen[sigStr]
		f.seen[sigStr] = true
		f.mu.Unlock()
		if dup {
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"error":"replay"}`)
			return
		}
	}
	f.respond(n, w, r, body)
}

func newFake(t *testing.T, respond func(n int, w http.ResponseWriter, r *http.Request, body []byte)) (*Client, *fakeRelay) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f := &fakeRelay{t: t, pub: pub, hub: "hub_test", seen: map[string]bool{}, respond: respond, unsigned: map[string]bool{"/v1/hubs": true}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New(srv.URL, "hub_test", priv, srv.Client())
	c.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	c.sleep = func(time.Duration) {}
	return c, f
}

func reply(code int, body string) func(int, http.ResponseWriter, *http.Request, []byte) {
	return func(_ int, w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, body)
	}
}

func TestPushSignedAndShaped(t *testing.T) {
	c, f := newFake(t, reply(202, `{"apns_id":"abc-123"}`))
	exp := time.Unix(1_700_003_600, 0)
	id, err := c.Push(context.Background(), PushRequest{
		Sealed: "sealed-token", Kind: "alert", Priority: 10, Expiration: &exp, CollapseID: "col",
		Payload: json.RawMessage(`{"aps":{"alert":"x"}}`),
	})
	if err != nil || id != "abc-123" {
		t.Fatalf("%q %v", id, err)
	}
	r := f.reqs[0]
	if r.Method != "POST" || r.Path != "/v1/push" {
		t.Fatalf("%s %s", r.Method, r.Path)
	}
	var got map[string]any
	if err := json.Unmarshal(r.Body, &got); err != nil {
		t.Fatal(err)
	}
	if got["sealed"] != "sealed-token" || got["kind"] != "alert" || got["priority"] != float64(10) ||
		got["expiration"] != float64(1_700_003_600) || got["collapse_id"] != "col" {
		t.Fatalf("body: %s", r.Body)
	}
	if p, ok := got["payload"].(map[string]any); !ok || p["aps"] == nil {
		t.Fatalf("payload must be an object: %s", r.Body)
	}
	if r.Header.Get("X-Hansho-Timestamp") != "1700000000" || r.Header.Get("X-Hansho-Hub") != "hub_test" {
		t.Fatalf("headers %v", r.Header)
	}
}

func TestPushOmitsOptionalFields(t *testing.T) {
	c, f := newFake(t, reply(202, `{"apns_id":"a"}`))
	if _, err := c.Push(context.Background(), PushRequest{Sealed: "s", Kind: "widgets", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal(f.reqs[0].Body, &got)
	if _, ok := got["expiration"]; ok {
		t.Error("expiration should be omitted")
	}
	if _, ok := got["collapse_id"]; ok {
		t.Error("collapse_id should be omitted")
	}
}

func TestStatusMapping(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		is   error
	}{
		{"unregistered", 410, `{"reason":"Unregistered"}`, ErrUnregistered},
		{"pairing revoked", 410, `{"reason":"PairingRevoked"}`, ErrPairingRevoked},
		{"token expired", 401, `{"error":"token_expired"}`, ErrTokenExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newFake(t, reply(tc.code, tc.body))
			_, err := c.Push(context.Background(), PushRequest{Sealed: "s", Kind: "alert", Payload: json.RawMessage(`{}`)})
			if !errors.Is(err, tc.is) {
				t.Fatalf("got %v", err)
			}
			var he *HTTPError
			if errors.As(err, &he) {
				t.Fatalf("typed error should not be HTTPError: %v", err)
			}
		})
	}

	for _, tc := range []struct {
		code int
		body string
		want string
	}{
		{403, `{"error":"token_hub_mismatch"}`, "token_hub_mismatch"},
		{413, `{"error":"payload_too_large"}`, "payload_too_large"},
		{409, `{"error":"replay"}`, "replay"},
		{502, `{"reason":"BadDeviceToken"}`, "BadDeviceToken"},
		{410, `{"reason":"SomethingElse"}`, "SomethingElse"},
	} {
		c, _ := newFake(t, reply(tc.code, tc.body))
		_, err := c.Push(context.Background(), PushRequest{Sealed: "s", Kind: "alert", Payload: json.RawMessage(`{}`)})
		var he *HTTPError
		if !errors.As(err, &he) || he.Status != tc.code || he.Code != tc.want {
			t.Errorf("%d: got %v", tc.code, err)
		}
	}
}

func TestRateLimitRetriedThenTyped(t *testing.T) {
	c, f := newFake(t, reply(429, `{"error":"rate_limited"}`))
	var slept []time.Duration
	c.sleep = func(d time.Duration) { slept = append(slept, d) }
	_, err := c.Push(context.Background(), PushRequest{Sealed: "s", Kind: "alert", Payload: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("got %v", err)
	}
	if len(f.reqs) != 4 {
		t.Fatalf("want 1 try + 3 retries, got %d requests", len(f.reqs))
	}
	if len(slept) != 3 || !(slept[0] < slept[1] && slept[1] < slept[2]) {
		t.Fatalf("backoff not increasing: %v", slept)
	}
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatal("no RateLimitedError")
	}
}

func TestRateLimitRecovers(t *testing.T) {
	c, f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request, b []byte) {
		if n < 2 {
			w.Header().Set("Retry-After", "7")
			reply(429, `{"error":"rate_limited"}`)(n, w, r, b)
			return
		}
		reply(202, `{"apns_id":"ok"}`)(n, w, r, b)
	})
	var slept []time.Duration
	c.sleep = func(d time.Duration) { slept = append(slept, d) }
	id, err := c.Push(context.Background(), PushRequest{Sealed: "s", Kind: "alert", Payload: json.RawMessage(`{}`)})
	if err != nil || id != "ok" || len(f.reqs) != 3 {
		t.Fatalf("%q %v %d", id, err, len(f.reqs))
	}
	if slept[0] != 7*time.Second {
		t.Fatalf("Retry-After ignored: %v", slept)
	}
}

// The relay's replay cache is keyed by signature, and Ed25519 is deterministic,
// so two identical requests in one second would be rejected as replays.
func TestIdenticalRequestsGetDistinctSignatures(t *testing.T) {
	c, f := newFake(t, reply(202, `{"apns_id":"a"}`))
	req := PushRequest{Sealed: "s", Kind: "widgets", Payload: json.RawMessage(`{"aps":{"content-changed":true}}`)}
	for range 3 {
		if _, err := c.Push(context.Background(), req); err != nil {
			t.Fatalf("identical push rejected: %v", err)
		}
	}
	seen := map[string]bool{}
	for _, r := range f.reqs {
		s := r.Header.Get("X-Hansho-Signature")
		if seen[s] {
			t.Fatal("duplicate signature")
		}
		seen[s] = true
	}
	// Timestamps stay within the relay's skew window.
	for _, r := range f.reqs {
		ts, _ := strconv.ParseInt(r.Header.Get("X-Hansho-Timestamp"), 10, 64)
		if d := ts - 1_700_000_000; d < 0 || d > 30 {
			t.Fatalf("timestamp drift %d", d)
		}
	}
}

func TestRevoke(t *testing.T) {
	c, f := newFake(t, reply(204, ``))
	if err := c.Revoke(context.Background(), "sealed-x"); err != nil {
		t.Fatal(err)
	}
	r := f.reqs[0]
	if r.Path != "/v1/revocations" || string(r.Body) != `{"sealed":"sealed-x"}` {
		t.Fatalf("%s %s", r.Path, r.Body)
	}
}

func TestRegister(t *testing.T) {
	c, f := newFake(t, reply(201, `{"hub_id":"hub_1","challenge":"chal"}`))
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	id, ch, err := c.Register(context.Background(), "hub.example.com", pub)
	if err != nil || id != "hub_1" || ch != "chal" {
		t.Fatalf("%q %q %v", id, ch, err)
	}
	r := f.reqs[0]
	if r.Path != "/v1/hubs" || r.Header.Get("X-Hansho-Signature") != "" {
		t.Fatalf("register must be unsigned: %s %v", r.Path, r.Header)
	}
	var body struct{ Domain, PublicKey string }
	var raw map[string]string
	json.Unmarshal(r.Body, &raw)
	body.Domain, body.PublicKey = raw["domain"], raw["public_key"]
	got, err := base64.RawURLEncoding.DecodeString(body.PublicKey)
	if err != nil || !ed25519.PublicKey(got).Equal(pub) || body.Domain != "hub.example.com" {
		t.Fatalf("body %s", r.Body)
	}
}

func TestVerify(t *testing.T) {
	c, f := newFake(t, reply(200, `{"hub_id":"hub_test","status":"active"}`))
	if err := c.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.reqs[0].Path != "/v1/hubs/hub_test/verify" || f.reqs[0].Method != "POST" {
		t.Fatalf("%s %s", f.reqs[0].Method, f.reqs[0].Path)
	}
	c2, _ := newFake(t, reply(422, `{"error":"challenge_mismatch"}`))
	var he *HTTPError
	if err := c2.Verify(context.Background()); !errors.As(err, &he) || he.Status != 422 || he.Code != "challenge_mismatch" {
		t.Fatalf("%v", err)
	}
}

func TestContextCancelStopsRetries(t *testing.T) {
	c, f := newFake(t, reply(429, `{}`))
	ctx, cancel := context.WithCancel(context.Background())
	c.sleep = func(time.Duration) { cancel() }
	_, err := c.Push(ctx, PushRequest{Sealed: "s", Kind: "alert", Payload: json.RawMessage(`{}`)})
	if err == nil || len(f.reqs) > 2 {
		t.Fatalf("%v after %d requests", err, len(f.reqs))
	}
}

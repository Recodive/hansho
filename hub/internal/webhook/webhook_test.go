package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/recodive/hansho/hub/internal/store"
)

var t0 = time.Unix(1_700_000_000, 0)

type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

type sink struct {
	mu     sync.Mutex
	status int
	got    []struct {
		Header http.Header
		Body   []byte
	}
}

func (s *sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.got = append(s.got, struct {
		Header http.Header
		Body   []byte
	}{r.Header.Clone(), b})
	code := s.status
	s.mu.Unlock()
	w.WriteHeader(code)
}

func setup(t *testing.T, status int) (*Dispatcher, *store.Store, *sink, *clock) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sk := &sink{status: status}
	srv := httptest.NewServer(sk)
	t.Cleanup(srv.Close)
	clk := &clock{t0}
	return New(st, srv.URL, []byte("whsec"), srv.Client(), clk.Now), st, sk, clk
}

func TestSignatureVerifies(t *testing.T) {
	d, _, sk, _ := setup(t, 200)
	if err := d.Emit(context.Background(), EventPairingCreated, map[string]string{"pairing_id": "p1"}); err != nil {
		t.Fatal(err)
	}
	n, err := d.RunOnce(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	h := sk.got[0].Header.Get("X-Hansho-Webhook-Signature")
	parts := strings.Split(h, ",")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "t=") || !strings.HasPrefix(parts[1], "v1=") {
		t.Fatalf("header %q", h)
	}
	ts := strings.TrimPrefix(parts[0], "t=")
	if ts != strconv.FormatInt(t0.Unix(), 10) {
		t.Fatalf("t=%s", ts)
	}
	m := hmac.New(sha256.New, []byte("whsec"))
	m.Write([]byte(ts + "." + string(sk.got[0].Body)))
	if hex.EncodeToString(m.Sum(nil)) != strings.TrimPrefix(parts[1], "v1=") {
		t.Fatal("signature mismatch")
	}
	var body struct {
		ID    string
		Event string
		Data  map[string]string
	}
	if err := json.Unmarshal(sk.got[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Event != "pairing.created" || body.Data["pairing_id"] != "p1" || body.ID == "" {
		t.Fatalf("body %s", sk.got[0].Body)
	}
	if sk.got[0].Header.Get("Content-Type") != "application/json" {
		t.Fatal("content type")
	}
}

func TestDeliveredMarked(t *testing.T) {
	d, st, _, _ := setup(t, 204)
	d.Emit(context.Background(), EventMessageAcked, map[string]string{"message_id": "m"})
	d.RunOnce(context.Background())
	rows, _ := st.ListWebhooks("")
	if len(rows) != 1 || rows[0].DeliveredAt.IsZero() {
		t.Fatalf("%+v", rows)
	}
	if n, _ := d.RunOnce(context.Background()); n != 0 {
		t.Fatal("delivered row resent")
	}
}

func TestRetryWithBackoff(t *testing.T) {
	d, st, sk, clk := setup(t, 500)
	d.Emit(context.Background(), EventMessageExpired, map[string]string{})
	if n, _ := d.RunOnce(context.Background()); n != 0 {
		t.Fatal("500 counted as sent")
	}
	rows, _ := st.ListWebhooks("")
	r := rows[0]
	if r.Attempts != 1 || !r.DeliveredAt.IsZero() || !r.NextAttemptAt.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("after 1st failure: %+v", r)
	}
	// Not due yet: no request.
	d.RunOnce(context.Background())
	if len(sk.got) != 1 {
		t.Fatalf("retried too early: %d requests", len(sk.got))
	}
	clk.t = t0.Add(10 * time.Second)
	d.RunOnce(context.Background())
	rows, _ = st.ListWebhooks("")
	if rows[0].Attempts != 2 || !rows[0].NextAttemptAt.Equal(clk.t.Add(20*time.Second)) {
		t.Fatalf("after 2nd failure: %+v", rows[0])
	}
	// Recovers.
	sk.mu.Lock()
	sk.status = 200
	sk.mu.Unlock()
	clk.t = clk.t.Add(20 * time.Second)
	if n, _ := d.RunOnce(context.Background()); n != 1 {
		t.Fatal("not delivered after recovery")
	}
	rows, _ = st.ListWebhooks("")
	if rows[0].DeliveredAt.IsZero() {
		t.Fatal("delivered_at not set")
	}
}

func TestDroppedAfterMaxAttempts(t *testing.T) {
	d, st, sk, clk := setup(t, 500)
	d.Emit(context.Background(), EventActionTaken, map[string]string{})
	for range 12 {
		d.RunOnce(context.Background())
		clk.t = clk.t.Add(time.Hour) // past the longest backoff (10 s << 7), well inside the pruning retention
	}
	if len(sk.got) != 8 {
		t.Fatalf("want 8 attempts, got %d", len(sk.got))
	}
	rows, _ := st.ListWebhooks("")
	if rows[0].DroppedAt.IsZero() || rows[0].Attempts != 8 {
		t.Fatalf("%+v", rows[0])
	}
}

func TestEmptyURLStoresNothing(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	defer st.Close()
	d := New(st, "", []byte("s"), nil, func() time.Time { return t0 })
	if err := d.Emit(context.Background(), EventPairingCreated, map[string]string{"a": "b"}); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.ListWebhooks("")
	if len(rows) != 0 {
		t.Fatalf("stored %d rows", len(rows))
	}
	if n, err := d.RunOnce(context.Background()); n != 0 || err != nil {
		t.Fatal(n, err)
	}
}

func TestUnknownEventRejected(t *testing.T) {
	d, st, _, _ := setup(t, 200)
	if err := d.Emit(context.Background(), "message.bogus", nil); err == nil {
		t.Fatal("unknown event accepted")
	}
	if rows, _ := st.ListWebhooks(""); len(rows) != 0 {
		t.Fatal("stored unknown event")
	}
}

func TestEventNames(t *testing.T) {
	want := []string{"pairing.created", "pairing.revoked", "message.delivered", "message.undeliverable",
		"message.acked", "message.expired", "action", "activity.ended"}
	got := []string{EventPairingCreated, EventPairingRevoked, EventMessageDelivered, EventMessageUndeliverable,
		EventMessageAcked, EventMessageExpired, EventActionTaken, EventActivityEnded}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d: %q != %q", i, got[i], want[i])
		}
	}
}

func TestPrunesFinishedRowsAfterRetention(t *testing.T) {
	d, st, sk, clk := setup(t, 200)
	ctx := context.Background()
	d.Emit(ctx, EventMessageAcked, map[string]string{"n": "delivered"})
	d.RunOnce(ctx) // delivered
	sk.mu.Lock()
	sk.status = 500
	sk.mu.Unlock()
	d.Emit(ctx, EventMessageAcked, map[string]string{"n": "pending"}) // never finishes: the receiver keeps failing
	clk.t = t0.Add(retention - time.Hour)
	d.RunOnce(ctx)
	if rows, _ := st.ListWebhooks(""); len(rows) != 2 {
		t.Fatalf("pruned inside the retention period: %d rows", len(rows))
	}
	clk.t = t0.Add(retention + time.Hour)
	d.RunOnce(ctx)
	rows, _ := st.ListWebhooks("")
	if len(rows) != 1 || !rows[0].DeliveredAt.IsZero() {
		t.Fatalf("want only the unfinished row left: %+v", rows)
	}
}

func TestPruneRunsOncePerInterval(t *testing.T) {
	d, st, _, clk := setup(t, 200)
	ctx := context.Background()
	d.Emit(ctx, EventMessageAcked, map[string]string{})
	d.RunOnce(ctx) // delivered at t0; this run also prunes
	clk.t = t0.Add(retention + time.Hour)
	d.lastPrune = clk.t.Add(-pruneInterval / 2)
	d.RunOnce(ctx)
	if rows, _ := st.ListWebhooks(""); len(rows) != 1 {
		t.Fatalf("pruned again within the interval: %d rows", len(rows))
	}
	clk.t = clk.t.Add(pruneInterval)
	d.RunOnce(ctx)
	if rows, _ := st.ListWebhooks(""); len(rows) != 0 {
		t.Fatalf("not pruned after the interval: %d rows", len(rows))
	}
}

// The default client (hc == nil, as in production) must not follow a redirect: the signed body would reach a host
// the operator never configured.
func TestDefaultClientDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	t.Cleanup(redirector.Close)
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := New(st, redirector.URL, []byte("whsec"), nil, (&clock{t0}).Now)
	d.Emit(context.Background(), EventMessageAcked, map[string]string{})
	if n, err := d.RunOnce(context.Background()); n != 0 || err != nil {
		t.Fatalf("a redirect counted as delivered: %d %v", n, err)
	}
	if hits.Load() != 0 {
		t.Fatal("the redirect was followed")
	}
	rows, _ := st.ListWebhooks("")
	if len(rows) != 1 || rows[0].Attempts != 1 || !rows[0].DeliveredAt.IsZero() {
		t.Fatalf("a redirect is a failed attempt: %+v", rows)
	}
}

// Each attempt is signed with the time of that attempt, not the time the batch started.
func TestSignatureTimestampIsPerAttempt(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var mu sync.Mutex
	now := t0
	clk := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	var stamps []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, rest, _ := strings.Cut(r.Header.Get(SignatureHeader), "t=")
		ts, _, _ := strings.Cut(rest, ",")
		stamps = append(stamps, ts)
		mu.Lock()
		now = now.Add(5 * time.Minute) // a slow receiver
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	d := New(st, srv.URL, []byte("whsec"), srv.Client(), clk)
	d.Emit(context.Background(), EventMessageAcked, map[string]string{})
	d.Emit(context.Background(), EventMessageAcked, map[string]string{})
	if n, err := d.RunOnce(context.Background()); n != 2 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
	want := []string{strconv.FormatInt(t0.Unix(), 10), strconv.FormatInt(t0.Add(5*time.Minute).Unix(), 10)}
	if len(stamps) != 2 || stamps[0] != want[0] || stamps[1] != want[1] {
		t.Fatalf("timestamps %v, want %v", stamps, want)
	}
	rows, _ := st.ListWebhooks("")
	if !rows[1].DeliveredAt.Equal(t0.Add(5 * time.Minute)) {
		t.Fatalf("delivered_at %v is not the attempt time", rows[1].DeliveredAt)
	}
}

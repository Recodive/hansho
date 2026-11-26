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
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type vectorsFile struct {
	ToleranceSeconds int `json:"tolerance_seconds"`
	Vectors          []struct {
		Name   string `json:"name"`
		Secret string `json:"secret"`
		Header string `json:"header"`
		Body   string `json:"body"`
		Now    int64  `json:"now"`
		Expect struct {
			OK        bool            `json:"ok"`
			Error     string          `json:"error"`
			Type      string          `json:"type"`
			Name      string          `json:"name"`
			ID        string          `json:"id"`
			CreatedAt int64           `json:"created_at"`
			Data      json.RawMessage `json:"data"`
		} `json:"expect"`
	} `json:"vectors"`
}

func loadVectors(t *testing.T) vectorsFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "spec", "webhook-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vectorsFile
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Vectors) < 20 {
		t.Fatalf("only %d vectors", len(v.Vectors))
	}
	return v
}

var errByCode = map[string]error{
	"bad_signature":    ErrBadSignature,
	"stale":            ErrStale,
	"malformed_header": ErrMalformedHeader,
}

// The vectors were signed by the hub's own dispatcher (hub/internal/webhook).
func TestSharedVectors(t *testing.T) {
	v := loadVectors(t)
	tol := time.Duration(v.ToleranceSeconds) * time.Second
	var sawStale, sawBad, sawMalformed, sawUnknown, sawRotation bool
	for _, c := range v.Vectors {
		t.Run(c.Name, func(t *testing.T) {
			ev, err := VerifyWebhook([]byte(c.Secret), c.Header, []byte(c.Body), time.Unix(c.Now, 0), tol)
			if !c.Expect.OK {
				want := errByCode[c.Expect.Error]
				if want == nil {
					t.Fatalf("unknown error code %q in vectors", c.Expect.Error)
				}
				if !errors.Is(err, want) {
					t.Fatalf("err = %v, want %v", err, want)
				}
				if ev.Payload != nil || ev.Raw != nil {
					t.Fatal("event returned alongside an error")
				}
				switch c.Expect.Error {
				case "stale":
					sawStale = true
				case "bad_signature":
					sawBad = true
				case "malformed_header":
					sawMalformed = true
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ev.Type != c.Expect.Type || ev.Name != orElse(c.Expect.Name, c.Expect.Type) || ev.ID != c.Expect.ID ||
				ev.CreatedAt.Unix() != c.Expect.CreatedAt {
				t.Fatalf("event = %+v, want %+v", ev, c.Expect)
			}
			sameJSON(t, ev.Payload, c.Expect.Data)
			if string(ev.Raw) != c.Body {
				t.Fatal("Raw is not the verified body")
			}
			if c.Expect.Type == EventUnknown {
				sawUnknown = true
			}
			if strings.HasPrefix(c.Name, "rotation") {
				sawRotation = true
			}
		})
	}
	if !(sawStale && sawBad && sawMalformed && sawUnknown && sawRotation) {
		t.Fatal("vector file lacks a required class of case")
	}
}

func orElse(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Review focus 2: a validly signed old request is a replay.
func TestReplayOutsideToleranceRejected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"id":"1","event":"message.acked","created_at":1,"data":{}}`)
	h := signedHeader("s", now, body)
	for _, tc := range []struct {
		at  time.Time
		tol time.Duration
		ok  bool
	}{
		{now, 0, true},
		{now.Add(300 * time.Second), 0, true},
		{now.Add(301 * time.Second), 0, false},
		{now.Add(-301 * time.Second), 0, false},
		{now.Add(301 * time.Second), 10 * time.Minute, true},
		{now.Add(11 * time.Second), 10 * time.Second, false},
		{now.Add(10 * time.Second), 10 * time.Second, true},
	} {
		_, err := VerifyWebhook([]byte("s"), h, body, tc.at, tc.tol)
		if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, ErrStale)) {
			t.Errorf("at %v tol %v: %v", tc.at.Sub(now), tc.tol, err)
		}
	}
}

func TestZeroNowUsesTheClock(t *testing.T) {
	body := []byte(`{"id":"1","event":"action","created_at":1,"data":{}}`)
	if _, err := VerifyWebhook([]byte("s"), signedHeader("s", time.Now(), body), body, time.Time{}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyWebhook([]byte("s"), signedHeader("s", time.Now().Add(-time.Hour), body), body, time.Time{}, 0); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
}

func TestEmptySecretRefused(t *testing.T) {
	body := []byte(`{"id":"1","event":"action","created_at":1,"data":{}}`)
	h := signedHeader("", time.Now(), body)
	if _, err := VerifyWebhook(nil, h, body, time.Now(), 0); err == nil || errors.Is(err, ErrBadSignature) {
		t.Fatalf("an empty secret must be a configuration error, got %v", err)
	}
}

func signedHeader(secret string, at time.Time, body []byte) string {
	t := strconv.FormatInt(at.Unix(), 10)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(t + "." + string(body)))
	return "t=" + t + ",v1=" + hex.EncodeToString(m.Sum(nil))
}

func TestMalformedBodyAfterValidSignature(t *testing.T) {
	now := time.Now()
	for _, body := range []string{"not json", "[]", `{"id":"1"}`, `{"event":""}`, ""} {
		_, err := VerifyWebhook([]byte("s"), signedHeader("s", now, []byte(body)), []byte(body), now, 0)
		if !errors.Is(err, ErrMalformedBody) {
			t.Errorf("%q: %v", body, err)
		}
	}
}

// Review focus 3: unknown fields are ignored, an unknown event is "unknown", not an error.
func TestForwardCompatibility(t *testing.T) {
	now := time.Now()
	body := []byte(`{"id":7,"event":"message.holographic","created_at":5,"extra":{"x":1},"data":{"message_id":"m","new_field":[1,2]}}`)
	ev, err := VerifyWebhook([]byte("s"), signedHeader("s", now, body), body, now, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != EventUnknown || ev.Name != "message.holographic" || ev.ID != "7" {
		t.Fatalf("%+v", ev)
	}
	d, err := ev.Data()
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := d.(json.RawMessage)
	if !ok {
		t.Fatalf("Data() of an unknown event = %T", d)
	}
	sameJSON(t, raw, []byte(`{"message_id":"m","new_field":[1,2]}`))

	// A known event with extra payload fields still decodes.
	body = []byte(`{"id":"8","event":"message.acked","created_at":5,"data":{"message_id":"m","device_id":"d","external_user_id":"u","from_the_future":true}}`)
	ev, err = VerifyWebhook([]byte("s"), signedHeader("s", now, body), body, now, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := ev.Data(); err != nil || d != (MessageAcked{MessageID: "m", DeviceID: "d", ExternalUserID: "u"}) {
		t.Fatalf("%#v %v", d, err)
	}
}

func TestMessageAckedCarriesWhoAcked(t *testing.T) {
	now := time.Unix(1700000000, 0)
	body := []byte(`{"id":"9","event":"message.acked","created_at":5,"data":{"message_id":"m","device_id":"d","external_user_id":"u","acked_by":"bob","acked_by_name":"Bob ボブ"}}`)
	ev, err := VerifyWebhook([]byte("s"), signedHeader("s", now, body), body, now, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := MessageAcked{MessageID: "m", DeviceID: "d", ExternalUserID: "u", AckedBy: "bob", AckedByName: "Bob ボブ"}
	if d, err := ev.Data(); err != nil || d != want {
		t.Fatalf("%#v %v", d, err)
	}
}

func TestTypedPayloads(t *testing.T) {
	want := map[string]any{
		EventPairingCreated:       PairingCreated{PairingID: "pair_1", ExternalUserID: "user-42", DeviceName: "Bas’s iPhone <Ünï>"},
		EventPairingRevoked:       PairingRevoked{PairingID: "pair_1", ExternalUserID: "user-42", By: "sender"},
		EventMessageDelivered:     MessageDelivered{MessageID: "msg_1", ExternalUserID: "user-42", Channel: "ops", Level: LevelPage, Devices: 2},
		EventMessageUndeliverable: MessageUndeliverable{MessageID: "msg_2", ExternalUserID: "user-42", Channel: "ops", Reason: "no_devices"},
		EventMessageAcked:         MessageAcked{MessageID: "msg_1", ExternalUserID: "user-42", DeviceID: "dev_1"},
		EventMessageDismissed:     MessageDismissed{MessageID: "msg_1", ExternalUserID: "user-42", DeviceID: "dev_1"},
		EventMessageExpired:       MessageExpired{MessageID: "msg_3", ExternalUserID: "user-42", Channel: "ops", Attempts: 6},
		EventAction:               ActionTaken{MessageID: "msg_1", ActionID: "approve", DeviceID: "dev_1", ExternalUserID: "user-42", Text: "ship it"},
		EventActivityEnded:        ActivityEnded{ActivityID: "act_1", ExternalUserID: "user-42", Channel: "ops", Reason: "timeout"},
	}
	seen := map[string]bool{}
	for _, c := range loadVectors(t).Vectors {
		if !c.Expect.OK || c.Expect.Type == EventUnknown || !strings.HasPrefix(c.Name, "valid ") || seen[c.Expect.Type] {
			continue
		}
		seen[c.Expect.Type] = true
		ev, err := VerifyWebhook([]byte(c.Secret), c.Header, []byte(c.Body), time.Unix(c.Now, 0), 0)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ev.Data()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want[ev.Type]) {
			t.Errorf("%s: %#v, want %#v", ev.Type, got, want[ev.Type])
		}
	}
	if len(seen) != 9 {
		t.Fatalf("vectors cover %d of 9 events", len(seen))
	}
}

func TestEventTypesAreTheNineHubEvents(t *testing.T) {
	got := []string{EventPairingCreated, EventPairingRevoked, EventMessageDelivered, EventMessageUndeliverable,
		EventMessageAcked, EventMessageDismissed, EventMessageExpired, EventAction, EventActivityEnded}
	want := []string{"pairing.created", "pairing.revoked", "message.delivered", "message.undeliverable",
		"message.acked", "message.dismissed", "message.expired", "action", "activity.ended"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if WebhookSignatureHeader != "X-Hansho-Webhook-Signature" {
		t.Fatal(WebhookSignatureHeader)
	}
}

// Review focus 1: verification runs on the raw bytes; the net/http handler never re-serialises.
func TestNetHTTPHandlerUsesRawBody(t *testing.T) {
	v := loadVectors(t)
	pick := func(name string) (hdr, body string) {
		for _, c := range v.Vectors {
			if c.Name == name {
				return c.Header, c.Body
			}
		}
		t.Fatalf("no vector %q", name)
		return
	}
	var got []Event
	var handlerErr error
	// A fixed clock: the vectors are from 2023.
	h := newWebhookHandler([]byte("whsec_sdk_vectors"), 0, func() time.Time { return time.Unix(1_700_000_000, 0) },
		func(_ context.Context, e Event) error { got = append(got, e); return handlerErr })
	post := func(hdr, body string) int {
		r := httptest.NewRequest("POST", "/hook", strings.NewReader(body))
		if hdr != "" {
			r.Header.Set(WebhookSignatureHeader, hdr)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	hdr, body := pick("valid message.acked")
	if code := post(hdr, body); code != 200 || len(got) != 1 || got[0].Type != EventMessageAcked {
		t.Fatalf("valid: %d %+v", code, got)
	}
	_, pretty := pick("re-serialised body (pretty-printed JSON) does not verify")
	if code := post(hdr, pretty); code != 400 || len(got) != 1 {
		t.Fatalf("re-serialised body accepted: %d", code)
	}
	if code := post("", body); code != 400 {
		t.Fatalf("missing header: %d", code)
	}
	handlerErr = errors.New("db down")
	if code := post(hdr, body); code != 500 {
		t.Fatalf("handler failure must be 500 so the hub retries, got %d", code)
	}
	r := httptest.NewRequest("GET", "/hook", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatal(w.Code)
	}
	big := httptest.NewRequest("POST", "/hook", io.LimitReader(zeroReader{}, 2<<20))
	big.Header.Set(WebhookSignatureHeader, hdr)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, big)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: %d", w.Code)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { return len(p), nil }

func TestPublicHandlerConstructor(t *testing.T) {
	var called bool
	h := NewWebhookHandler([]byte("s"), func(context.Context, Event) error { called = true; return nil })
	body := `{"id":"1","event":"action","created_at":1,"data":{}}`
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.Header.Set(WebhookSignatureHeader, signedHeader("s", time.Now(), []byte(body)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !called {
		t.Fatal(w.Code, called)
	}
}

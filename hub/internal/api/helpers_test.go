package api

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/recodive/hansho/hub/internal/config"
	"github.com/recodive/hansho/hub/internal/crypto"
	"github.com/recodive/hansho/hub/internal/delivery"
	"github.com/recodive/hansho/hub/internal/relay"
	"github.com/recodive/hansho/hub/internal/store"
	"github.com/recodive/hansho/hub/internal/webhook"
)

var t0 = time.Unix(1_700_000_000, 0)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeRelay implements RelayClient and records everything.
type fakeRelay struct {
	mu      sync.Mutex
	Pushes  []relay.PushRequest
	Revokes []string
	// PushErr maps a sealed token to the error Push returns for it.
	PushErr map[string]error
	// RevokeErr is returned by Revoke when non-nil.
	RevokeErr error
}

func (f *fakeRelay) Push(_ context.Context, p relay.PushRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.PushErr[p.Sealed]; err != nil {
		return "", err
	}
	f.Pushes = append(f.Pushes, p)
	return "apns-" + strconv.Itoa(len(f.Pushes)), nil
}

func (f *fakeRelay) Revoke(_ context.Context, sealed string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Revokes = append(f.Revokes, sealed)
	return f.RevokeErr
}

func (f *fakeRelay) pushes() []relay.PushRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]relay.PushRequest(nil), f.Pushes...)
}

func (f *fakeRelay) revokes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Revokes...)
}

type env struct {
	t      *testing.T
	st     *store.Store
	h      http.Handler
	clk    *clock
	relay  *fakeRelay
	hubKey ed25519.PrivateKey
	cfg    config.Config
	apiKey string
	worker *delivery.Worker
}

func newEnv(t *testing.T) *env { return newEnvCfg(t, nil) }

func newEnvCfg(t *testing.T, mod func(*config.Config)) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	_, hubKey, _ := ed25519.GenerateKey(rand.Reader)
	clk := &clock{t: t0}
	cfg := config.Config{
		PublicURL: "https://hub.example.com", DisplayName: "Example Hub", MasterKey: bytes.Repeat([]byte{0x42}, 32),
		WebhookURL: "https://backend.example.com/hooks", WebhookSecret: []byte("whsec"), ChallengeToken: "chal-token",
	}
	if mod != nil {
		mod(&cfg)
	}
	fr := &fakeRelay{PushErr: map[string]error{}}
	wh := webhook.New(st, cfg.WebhookURL, cfg.WebhookSecret, nil, clk.Now)
	h := New(Deps{Cfg: cfg, Store: st, Key: hubKey, Relay: fr, Webhooks: wh, Now: clk.Now})
	key, err := st.CreateAPIKey("test")
	if err != nil {
		t.Fatal(err)
	}
	wk := delivery.NewWorker(st, fr, wh, cfg, hubKey, clk.Now)
	return &env{t: t, st: st, h: h, clk: clk, relay: fr, hubKey: hubKey, cfg: cfg, apiKey: key, worker: wk}
}

func (e *env) do(method, path string, body any, hdr map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = b
	case string:
		rd = []byte(b)
	default:
		var err error
		if rd, err = json.Marshal(b); err != nil {
			e.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(rd))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	return w
}

// api performs an API-key authenticated request.
func (e *env) api(method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(method, path, body, map[string]string{"Authorization": "Bearer " + e.apiKey})
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}

// events returns the decoded "data" of every queued webhook of an event.
func (e *env) events(event string) []map[string]any {
	e.t.Helper()
	rows, err := e.st.ListWebhooks(event)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []map[string]any
	for _, r := range rows {
		var m map[string]any
		if err := json.Unmarshal(r.PayloadJSON, &m); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// testDevice is a software stand-in for the app: P-256 signing and key-agreement keys.
type testDevice struct {
	e          *env
	sign       *ecdsa.PrivateKey
	ka         *ecdh.PrivateKey
	Name       string
	ID         string
	PairingID  string
	PairingKey []byte
	Tokens     map[string]string
}

func (e *env) newDevice(name string) *testDevice {
	sign, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		e.t.Fatal(err)
	}
	ka, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		e.t.Fatal(err)
	}
	return &testDevice{e: e, sign: sign, ka: ka, Name: name, Tokens: map[string]string{
		"alert": "alert-" + name, "background": "bg-" + name, "pushtostart": "p2s-" + name, "widgets": "wid-" + name,
	}}
}

func (d *testDevice) signPubB64() string {
	b, err := d.sign.PublicKey.Bytes()
	if err != nil {
		d.e.t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func (d *testDevice) deviceJSON() map[string]any {
	return map[string]any{
		"sign_pub": d.signPubB64(), "ka_pub": base64.StdEncoding.EncodeToString(d.ka.PublicKey().Bytes()),
		"platform": "ios", "name": d.Name,
	}
}

func (d *testDevice) redeemBody(ticket string) map[string]any {
	return map[string]any{"ticket": ticket, "device": d.deviceJSON(), "tokens": d.Tokens}
}

func rawSign(t *testing.T, priv *ecdsa.PrivateKey, canonical string) string {
	t.Helper()
	dg := sha256.Sum256([]byte(canonical))
	r, s, err := ecdsa.Sign(rand.Reader, priv, dg[:])
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 64)
	r.FillBytes(out[:32])
	s.FillBytes(out[32:])
	return base64.RawURLEncoding.EncodeToString(out)
}

// signedHeaders builds device-signature headers for a request.
func (d *testDevice) signedHeaders(method, path string, body []byte, ts time.Time) map[string]string {
	tsS := strconv.FormatInt(ts.Unix(), 10)
	return map[string]string{
		"X-Hansho-Device":    d.ID,
		"X-Hansho-Timestamp": tsS,
		"X-Hansho-Signature": rawSign(d.e.t, d.sign, crypto.CanonicalString(tsS, method, path, body)),
	}
}

func (d *testDevice) request(method, path string, body any) *httptest.ResponseRecorder {
	d.e.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			d.e.t.Fatal(err)
		}
	}
	// The signature covers the path only, never the query string.
	signed, _, _ := strings.Cut(path, "?")
	return d.e.do(method, path, raw, d.signedHeaders(method, signed, raw, d.e.clk.Now()))
}

// pair runs the full ticket + redeem flow for user and records ids and the unwrapped key.
func (d *testDevice) pair(user string) {
	d.e.t.Helper()
	w := d.e.api("POST", "/api/v1/pairing-tickets", map[string]string{"external_user_id": user})
	if w.Code != 201 {
		d.e.t.Fatalf("ticket: %d %s", w.Code, w.Body)
	}
	tk := decode[struct{ Ticket string }](d.e.t, w)
	w = d.e.do("POST", "/device/v1/pairings", d.redeemBody(tk.Ticket), nil)
	if w.Code != 201 {
		d.e.t.Fatalf("redeem: %d %s", w.Code, w.Body)
	}
	res := decode[struct {
		PairingID  string `json:"pairing_id"`
		DeviceID   string `json:"device_id"`
		WrappedKey string `json:"wrapped_key"`
	}](d.e.t, w)
	wrapped, err := base64.RawURLEncoding.DecodeString(res.WrappedKey)
	if err != nil {
		d.e.t.Fatal(err)
	}
	key, err := crypto.UnwrapKey(d.ka, res.PairingID, wrapped)
	if err != nil {
		d.e.t.Fatal(err)
	}
	d.ID, d.PairingID, d.PairingKey = res.DeviceID, res.PairingID, key
}

// addTo joins an existing pairing as another device using the pairing proof.
func (d *testDevice) addTo(pairingID string, key []byte) *httptest.ResponseRecorder {
	d.e.t.Helper()
	path := "/device/v1/pairings/" + pairingID + "/devices"
	raw, _ := json.Marshal(map[string]any{"device": d.deviceJSON(), "tokens": d.Tokens})
	return d.e.do("POST", path, raw, d.e.proofHeaders("POST", path, raw, key, d.e.clk.Now()))
}

func (e *env) proofHeaders(method, path string, body, key []byte, ts time.Time) map[string]string {
	tsS := strconv.FormatInt(ts.Unix(), 10)
	return map[string]string{
		"X-Hansho-Timestamp":     tsS,
		"X-Hansho-Pairing-Proof": crypto.PairingProof(key, crypto.CanonicalString(tsS, method, path, body)),
	}
}

func jsonBytes(v any) ([]byte, error) { return json.Marshal(v) }

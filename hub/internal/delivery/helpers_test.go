package delivery

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/recodive/hansho/hub/internal/config"
	"github.com/recodive/hansho/hub/internal/crypto"
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

type fakeRelay struct {
	mu      sync.Mutex
	pushes  []relay.PushRequest
	revokes []string
	// errs maps a sealed token to the error returned by Push; the entry stays until cleared.
	errs map[string]error
}

func (f *fakeRelay) Push(_ context.Context, p relay.PushRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs[p.Sealed]; err != nil {
		return "", err
	}
	f.pushes = append(f.pushes, p)
	return "apns-" + strconv.Itoa(len(f.pushes)), nil
}

func (f *fakeRelay) Revoke(_ context.Context, sealed string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokes = append(f.revokes, sealed)
	return nil
}

func (f *fakeRelay) Pushes() []relay.PushRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]relay.PushRequest(nil), f.pushes...)
}

func (f *fakeRelay) setErr(sealed string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.errs, sealed)
	} else {
		f.errs[sealed] = err
	}
}

type fixture struct {
	t      *testing.T
	st     *store.Store
	relay  *fakeRelay
	wh     *webhook.Dispatcher
	w      *Worker
	clk    *clock
	hubKey ed25519.PrivateKey
	cfg    config.Config
	keys   map[string][]byte // pairing id -> pairing key
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	_, hubKey, _ := ed25519.GenerateKey(rand.Reader)
	clk := &clock{t: t0}
	st.Now = clk.Now
	cfg := config.Config{DisplayName: "Example Hub", MasterKey: bytes.Repeat([]byte{0x42}, 32), WebhookURL: "http://backend.invalid/hook"}
	fr := &fakeRelay{errs: map[string]error{}}
	wh := webhook.New(st, cfg.WebhookURL, []byte("s"), nil, clk.Now)
	f := &fixture{t: t, st: st, relay: fr, wh: wh, clk: clk, hubKey: hubKey, cfg: cfg, keys: map[string][]byte{}}
	f.w = NewWorker(st, fr, wh, cfg, hubKey, clk.Now)
	if err := st.UpsertChannel(store.Channel{ID: "ops", Name: "Ops", DefaultLevel: "active", MaxLevel: "page", Thread: "ops-thread"}); err != nil {
		t.Fatal(err)
	}
	return f
}

// pairing creates a pairing for user with devices named by `names`; each device
// gets alert token "alert-<name>" and all token kinds.
func (f *fixture) pairing(id, user string, names ...string) {
	f.t.Helper()
	key := bytes.Repeat([]byte{byte(len(f.keys) + 1)}, 32)
	f.keys[id] = key
	wrapped, err := crypto.WrapAtRest(f.cfg.MasterKey, key)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.st.CreatePairing(store.Pairing{ID: id, ExternalUserID: user, KeyWrapped: wrapped, CreatedAt: f.clk.Now()}); err != nil {
		f.t.Fatal(err)
	}
	for _, n := range names {
		err := f.st.AddDevice(store.Device{
			ID: n, PairingID: id, SignPub: []byte("sign-" + n), Platform: "ios", Name: n,
			SealedAlert: "alert-" + n, SealedBackground: "bg-" + n, SealedPushToStart: "p2s-" + n, SealedWidgets: "wid-" + n,
			CreatedAt: f.clk.Now(),
		})
		if err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fixture) enqueue(user, level string, mod ...func(*EnqueueInput)) EnqueueResult {
	f.t.Helper()
	ch, err := f.st.GetChannel("ops")
	if err != nil {
		f.t.Fatal(err)
	}
	in := EnqueueInput{
		ExternalUserID: user, Channel: ch, Level: level,
		Plaintext: Plaintext{Title: "Disk full", Body: "db-1 at 97%", Channel: "ops", Level: level},
	}
	for _, m := range mod {
		m(&in)
	}
	res, err := Enqueue(context.Background(), f.st, f.wh, f.cfg, f.hubKey, f.clk.Now, in)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func (f *fixture) run() {
	f.t.Helper()
	if err := f.w.RunOnce(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) events(event string) []map[string]any {
	f.t.Helper()
	rows, err := f.st.ListWebhooks(event)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []map[string]any
	for _, r := range rows {
		var m map[string]any
		if err := json.Unmarshal(r.PayloadJSON, &m); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func (f *fixture) msg(id string) store.Message {
	f.t.Helper()
	m, err := f.st.GetMessage(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

// pushesTo returns pushes sent with the given sealed token.
func (f *fixture) pushesTo(sealed string) []relay.PushRequest {
	var out []relay.PushRequest
	for _, p := range f.relay.Pushes() {
		if p.Sealed == sealed {
			out = append(out, p)
		}
	}
	return out
}

type alertWire struct {
	APS struct {
		InterruptionLevel string `json:"interruption-level"`
	} `json:"aps"`
	C   crypto.Envelope `json:"-"`
	Raw struct {
		V  int    `json:"v"`
		P  string `json:"p"`
		M  string `json:"m"`
		CT string `json:"ct"`
		S  string `json:"s"`
		PG int    `json:"pg"`
	} `json:"c"`
}

func (f *fixture) openPush(p relay.PushRequest, pairing string) (alertWire, Plaintext) {
	f.t.Helper()
	var w alertWire
	if err := json.Unmarshal(p.Payload, &w); err != nil {
		f.t.Fatal(err)
	}
	env := crypto.Envelope{V: w.Raw.V, P: w.Raw.P, M: w.Raw.M, CT: w.Raw.CT, S: w.Raw.S}
	pt, err := crypto.OpenMessage(f.hubKey.Public().(ed25519.PublicKey), f.keys[pairing], env)
	if err != nil {
		f.t.Fatalf("open envelope: %v", err)
	}
	var out Plaintext
	if err := json.Unmarshal(pt, &out); err != nil {
		f.t.Fatal(err)
	}
	return w, out
}

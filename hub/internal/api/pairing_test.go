package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/elliptic"
	"encoding/base64"
	"maps"
	"math/big"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/recodive/hansho/hub/internal/identity"
	"github.com/recodive/hansho/hub/internal/relay"
	"net/http/httptest"
)

func TestChallengeEndpoint(t *testing.T) {
	e := newEnv(t)
	w := e.do("GET", "/.well-known/hansho-challenge", nil, nil)
	if w.Code != 200 || w.Body.String() != "chal-token" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("%d %q %s", w.Code, w.Body, w.Header().Get("Content-Type"))
	}
	e2 := newEnv(t)
	e2.cfg.ChallengeToken = ""
	h := New(Deps{Cfg: e2.cfg, Store: e2.st, Key: e2.hubKey, Relay: e2.relay, Now: e2.clk.Now})
	e2.h = h
	if w := e2.do("GET", "/.well-known/hansho-challenge", nil, nil); w.Code != 404 {
		t.Fatalf("empty token should 404, got %d", w.Code)
	}
}

func TestQRFormat(t *testing.T) {
	e := newEnv(t)
	w := e.api("POST", "/api/v1/pairing-tickets", map[string]string{"external_user_id": "alice"})
	if w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	res := decode[struct {
		Ticket    string `json:"ticket"`
		QR        string `json:"qr"`
		ExpiresAt string `json:"expires_at"`
	}](t, w)
	fp := identity.Fingerprint(e.hubKey.Public().(ed25519.PublicKey))
	want := "hansho://pair?hub=" + url.QueryEscape("https://hub.example.com") + "&t=" + res.Ticket + "&fp=" + fp
	if res.QR != want {
		t.Fatalf("qr\n got %s\nwant %s", res.QR, want)
	}
	if want := "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t="; !strings.HasPrefix(res.QR, want) {
		t.Fatalf("hub param not url-encoded: %s", res.QR)
	}
	b, err := base64.RawURLEncoding.DecodeString(res.Ticket)
	if err != nil || len(b) != 32 {
		t.Fatalf("ticket is not 32 bytes base64url: %q", res.Ticket)
	}
	exp, err := time.Parse(time.RFC3339, res.ExpiresAt)
	if err != nil || !exp.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("expires_at %q (%v)", res.ExpiresAt, err)
	}
}

func TestTicketRequiresAPIKey(t *testing.T) {
	e := newEnv(t)
	body := map[string]string{"external_user_id": "alice"}
	for name, hdr := range map[string]map[string]string{
		"none":  nil,
		"wrong": {"Authorization": "Bearer ck_nope"},
		"basic": {"Authorization": "Basic " + e.apiKey},
	} {
		if w := e.do("POST", "/api/v1/pairing-tickets", body, hdr); w.Code != 401 {
			t.Errorf("%s: want 401, got %d", name, w.Code)
		}
	}
	if w := e.api("POST", "/api/v1/pairing-tickets", map[string]string{}); w.Code != 400 {
		t.Errorf("missing external_user_id: %d", w.Code)
	}
}

func TestPairAndAddSecondDevice(t *testing.T) {
	e := newEnv(t)
	phone := e.newDevice("phone")
	phone.pair("alice")
	if len(phone.PairingKey) != 32 {
		t.Fatalf("unwrapped key is %d bytes", len(phone.PairingKey))
	}

	// Hub description in the response.
	w := e.api("POST", "/api/v1/pairing-tickets", map[string]string{"external_user_id": "bob"})
	tk := decode[struct{ Ticket string }](t, w).Ticket
	w = e.do("POST", "/device/v1/pairings", e.newDevice("x").redeemBody(tk), nil)
	hub := decode[struct {
		Hub struct {
			Name, Domain, Fingerprint string
			PublicKey                 string `json:"public_key"`
		} `json:"hub"`
	}](t, w).Hub
	pub, err := base64.RawURLEncoding.DecodeString(hub.PublicKey)
	if err != nil || !bytes.Equal(pub, e.hubKey.Public().(ed25519.PublicKey)) ||
		hub.Name != "Example Hub" || hub.Domain != "hub.example.com" ||
		hub.Fingerprint != identity.Fingerprint(e.hubKey.Public().(ed25519.PublicKey)) {
		t.Fatalf("hub info %+v", hub)
	}

	// Second device of alice proves possession of the pairing key.
	watch := e.newDevice("watch")
	w = watch.addTo(phone.PairingID, phone.PairingKey)
	if w.Code != 201 {
		t.Fatalf("add device: %d %s", w.Code, w.Body)
	}
	watch.ID = decode[struct {
		DeviceID string `json:"device_id"`
	}](t, w).DeviceID

	ds, err := e.st.ActiveDevicesForUser("alice")
	if err != nil || len(ds) != 2 {
		t.Fatalf("devices: %v %v", ds, err)
	}
	got := map[string]string{}
	for _, d := range ds {
		got[d.ID] = d.SealedAlert
		if d.PairingID != phone.PairingID {
			t.Errorf("device %s in wrong pairing", d.ID)
		}
	}
	if got[phone.ID] != "alert-phone" || got[watch.ID] != "alert-watch" {
		t.Fatalf("tokens: %v", got)
	}
	// The pairing key is stored wrapped, never in the clear.
	p, _ := e.st.GetPairing(phone.PairingID)
	if bytes.Contains(p.KeyWrapped, phone.PairingKey) || len(p.KeyWrapped) == 0 {
		t.Fatal("pairing key stored in plaintext")
	}
	if ev := e.events("pairing.created"); len(ev) != 2 {
		t.Fatalf("pairing.created events: %d", len(ev))
	}
}

func TestPairingCreatedWebhook(t *testing.T) {
	e := newEnv(t)
	d := e.newDevice("phone")
	d.pair("alice")
	ev := e.events("pairing.created")
	if len(ev) != 1 {
		t.Fatalf("%d events", len(ev))
	}
	if ev[0]["pairing_id"] != d.PairingID || ev[0]["external_user_id"] != "alice" || ev[0]["device_name"] != "phone" {
		t.Fatalf("%v", ev[0])
	}
}

func TestTicketReuseGone(t *testing.T) {
	e := newEnv(t)
	tk := decode[struct{ Ticket string }](t, e.api("POST", "/api/v1/pairing-tickets", map[string]string{"external_user_id": "alice"})).Ticket
	if w := e.do("POST", "/device/v1/pairings", e.newDevice("a").redeemBody(tk), nil); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	w := e.do("POST", "/device/v1/pairings", e.newDevice("b").redeemBody(tk), nil)
	if w.Code != 410 || !strings.Contains(w.Body.String(), `"ticket_used"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if ps, _ := e.st.ActivePairingsForUser("alice"); len(ps) != 1 {
		t.Fatalf("pairings: %d", len(ps))
	}
}

// Review focus 1: concurrent redemption creates exactly one pairing.
func TestTicketConcurrentRedeemCreatesOnePairing(t *testing.T) {
	e := newEnv(t)
	tk := decode[struct{ Ticket string }](t, e.api("POST", "/api/v1/pairing-tickets", map[string]string{"external_user_id": "alice"})).Ticket
	const n = 12
	devs := make([]*testDevice, n)
	for i := range devs {
		devs[i] = e.newDevice("d" + string(rune('a'+i)))
	}
	codes := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range devs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w := e.do("POST", "/device/v1/pairings", devs[i].redeemBody(tk), nil)
			codes[i] = w.Code
		}()
	}
	close(start)
	wg.Wait()
	created, gone := 0, 0
	for _, c := range codes {
		switch c {
		case 201:
			created++
		case 410:
			gone++
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if created != 1 || gone != n-1 {
		t.Fatalf("created=%d gone=%d", created, gone)
	}
	if ps, _ := e.st.ActivePairingsForUser("alice"); len(ps) != 1 {
		t.Fatalf("pairings: %d", len(ps))
	}
	if ev := e.events("pairing.created"); len(ev) != 1 {
		t.Fatalf("pairing.created emitted %d times", len(ev))
	}
}

func TestTicketExpired(t *testing.T) {
	e := newEnv(t)
	tk := decode[struct{ Ticket string }](t, e.api("POST", "/api/v1/pairing-tickets", map[string]string{"external_user_id": "alice"})).Ticket
	e.clk.Advance(10*time.Minute + time.Second)
	w := e.do("POST", "/device/v1/pairings", e.newDevice("a").redeemBody(tk), nil)
	if w.Code != 410 || !strings.Contains(w.Body.String(), `"ticket_expired"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w = e.do("POST", "/device/v1/pairings", e.newDevice("a").redeemBody("bogus"), nil)
	if w.Code != 404 {
		t.Fatalf("unknown ticket: %d %s", w.Code, w.Body)
	}
}

func TestInvalidRedeemBodyDoesNotBurnTicket(t *testing.T) {
	e := newEnv(t)
	tk := decode[struct{ Ticket string }](t, e.api("POST", "/api/v1/pairing-tickets", map[string]string{"external_user_id": "alice"})).Ticket
	d := e.newDevice("a")
	bad := d.redeemBody(tk)
	bad["tokens"] = map[string]string{"alert": ""}
	if w := e.do("POST", "/device/v1/pairings", bad, nil); w.Code != 400 {
		t.Fatalf("missing tokens: %d", w.Code)
	}
	bad = d.redeemBody(tk)
	bad["device"].(map[string]any)["ka_pub"] = "AAAA"
	if w := e.do("POST", "/device/v1/pairings", bad, nil); w.Code != 400 {
		t.Fatalf("bad ka_pub: %d", w.Code)
	}
	if w := e.do("POST", "/device/v1/pairings", d.redeemBody(tk), nil); w.Code != 201 {
		t.Fatalf("ticket burned by invalid requests: %d %s", w.Code, w.Body)
	}
}

func TestDeviceSignatureAuth(t *testing.T) {
	e := newEnv(t)
	d := e.newDevice("phone")
	d.pair("alice")
	path := "/device/v1/devices/" + d.ID + "/tokens"
	body := []byte(`{"alert":"a2","background":"b2"}`)

	// Valid.
	hdr := d.signedHeaders("PUT", path, body, e.clk.Now())
	if w := e.do("PUT", path, body, hdr); w.Code != 204 {
		t.Fatalf("valid: %d %s", w.Code, w.Body)
	}
	// Replay of the exact same request.
	if w := e.do("PUT", path, body, hdr); w.Code != 409 {
		t.Fatalf("replay: want 409, got %d", w.Code)
	}
	// Replay with the malleated twin (r, n-s), which verifies too.
	raw, err := base64.RawURLEncoding.DecodeString(hdr[headerSignature])
	if err != nil {
		t.Fatal(err)
	}
	n := elliptic.P256().Params().N
	s := new(big.Int).Sub(n, new(big.Int).SetBytes(raw[32:]))
	twin := append([]byte{}, raw[:32]...)
	twin = append(twin, s.FillBytes(make([]byte, 32))...)
	mal := maps.Clone(hdr)
	mal[headerSignature] = base64.RawURLEncoding.EncodeToString(twin)
	if w := e.do("PUT", path, body, mal); w.Code != 409 {
		t.Fatalf("malleated replay: want 409, got %d", w.Code)
	}
	// Signed with another device's key.
	other := e.newDevice("other")
	other.ID = d.ID
	if w := e.do("PUT", path, body, other.signedHeaders("PUT", path, body, e.clk.Now())); w.Code != 401 {
		t.Fatalf("wrong key: want 401, got %d", w.Code)
	}
	// Old timestamp (outside +-300 s), correctly signed.
	if w := e.do("PUT", path, body, d.signedHeaders("PUT", path, body, e.clk.Now().Add(-301*time.Second))); w.Code != 401 {
		t.Fatalf("old ts: want 401, got %d", w.Code)
	}
	if w := e.do("PUT", path, body, d.signedHeaders("PUT", path, body, e.clk.Now().Add(301*time.Second))); w.Code != 401 {
		t.Fatalf("future ts: want 401, got %d", w.Code)
	}
	// Inside the window is fine.
	if w := e.do("PUT", path, body, d.signedHeaders("PUT", path, body, e.clk.Now().Add(-299*time.Second))); w.Code != 204 {
		t.Fatalf("299s skew: %d", w.Code)
	}
	// Body tampered after signing.
	h2 := d.signedHeaders("PUT", path, body, e.clk.Now().Add(time.Second))
	if w := e.do("PUT", path, []byte(`{"alert":"evil","background":"b2"}`), h2); w.Code != 401 {
		t.Fatalf("tampered body: %d", w.Code)
	}
	// Unknown device and missing headers.
	h3 := d.signedHeaders("PUT", path, body, e.clk.Now().Add(2*time.Second))
	h3["X-Hansho-Device"] = "dev_nope"
	if w := e.do("PUT", path, body, h3); w.Code != 401 {
		t.Fatalf("unknown device: %d", w.Code)
	}
	if w := e.do("PUT", path, body, nil); w.Code != 401 {
		t.Fatalf("no headers: %d", w.Code)
	}
	// Replay cache expires after 600 s, but then the timestamp is stale too.
}

func TestUpdateTokens(t *testing.T) {
	e := newEnv(t)
	d := e.newDevice("phone")
	d.pair("alice")
	w := d.request("PUT", "/device/v1/devices/"+d.ID+"/tokens", map[string]string{
		"alert": "new-alert", "background": "new-bg", "pushtostart": "new-p2s", "widgets": "",
	})
	if w.Code != 204 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	got, _ := e.st.GetDevice(d.ID)
	if got.SealedAlert != "new-alert" || got.SealedPushToStart != "new-p2s" || got.SealedWidgets != "" {
		t.Fatalf("%+v", got)
	}
	// A device cannot replace another device's tokens.
	o := e.newDevice("other")
	o.pair("bob")
	w = d.request("PUT", "/device/v1/devices/"+o.ID+"/tokens", map[string]string{"alert": "x", "background": "y"})
	if w.Code != 403 {
		t.Fatalf("cross-device: %d", w.Code)
	}
	if w := d.request("PUT", "/device/v1/devices/"+d.ID+"/tokens", map[string]string{"alert": ""}); w.Code != 400 {
		t.Fatalf("missing alert: %d", w.Code)
	}
}

func TestBadPairingProof(t *testing.T) {
	e := newEnv(t)
	phone := e.newDevice("phone")
	phone.pair("alice")
	watch := e.newDevice("watch")

	// Wrong key.
	if w := watch.addTo(phone.PairingID, bytes.Repeat([]byte{1}, 32)); w.Code != 401 {
		t.Fatalf("wrong key: %d", w.Code)
	}
	// Missing proof.
	path := "/device/v1/pairings/" + phone.PairingID + "/devices"
	raw := []byte(`{}`)
	if w := e.do("POST", path, raw, nil); w.Code != 401 {
		t.Fatalf("no proof: %d", w.Code)
	}
	// Valid proof but for another pairing's path.
	other := e.newDevice("o")
	other.pair("bob")
	hdr := e.proofHeaders("POST", "/device/v1/pairings/"+other.PairingID+"/devices", raw, phone.PairingKey, e.clk.Now())
	if w := e.do("POST", "/device/v1/pairings/"+other.PairingID+"/devices", raw, hdr); w.Code != 401 {
		t.Fatalf("proof with wrong pairing key: %d", w.Code)
	}
	// Stale timestamp.
	raw, _ = jsonBytes(map[string]any{"device": watch.deviceJSON(), "tokens": watch.Tokens})
	hdr = e.proofHeaders("POST", path, raw, phone.PairingKey, e.clk.Now().Add(-400*time.Second))
	if w := e.do("POST", path, raw, hdr); w.Code != 401 {
		t.Fatalf("stale proof: %d", w.Code)
	}
	// Valid proof replayed.
	hdr = e.proofHeaders("POST", path, raw, phone.PairingKey, e.clk.Now())
	if w := e.do("POST", path, raw, hdr); w.Code != 201 {
		t.Fatalf("valid proof: %d", w.Code)
	}
	if w := e.do("POST", path, raw, hdr); w.Code != 409 {
		t.Fatalf("replayed proof: %d", w.Code)
	}
	if ds, _ := e.st.ActiveDevicesForUser("alice"); len(ds) != 2 {
		t.Fatalf("devices: %d", len(ds))
	}
	// Unknown pairing.
	if w := e.do("POST", "/device/v1/pairings/pair_nope/devices", raw, e.proofHeaders("POST", "/device/v1/pairings/pair_nope/devices", raw, phone.PairingKey, e.clk.Now())); w.Code != 404 && w.Code != 401 {
		t.Fatalf("unknown pairing: %d", w.Code)
	}
}

func TestUnpairFromDevice(t *testing.T) {
	e := newEnv(t)
	phone := e.newDevice("phone")
	phone.pair("alice")
	watch := e.newDevice("watch")
	w := watch.addTo(phone.PairingID, phone.PairingKey)
	watch.ID = decode[struct {
		DeviceID string `json:"device_id"`
	}](t, w).DeviceID

	if w := phone.request("DELETE", "/device/v1/pairings/"+phone.PairingID, nil); w.Code != 204 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	rev := e.relay.revokes()
	if len(rev) != 2 || !(contains(rev, "alert-phone") && contains(rev, "alert-watch")) {
		t.Fatalf("relay revokes: %v", rev)
	}
	ev := e.events("pairing.revoked")
	if len(ev) != 1 || ev[0]["by"] != "device" || ev[0]["pairing_id"] != phone.PairingID || ev[0]["external_user_id"] != "alice" {
		t.Fatalf("events: %v", ev)
	}
	if ds, _ := e.st.ActiveDevicesForUser("alice"); len(ds) != 0 {
		t.Fatal("devices still active")
	}
	// Revoked pairing's devices can no longer authenticate.
	if w := phone.request("PUT", "/device/v1/devices/"+phone.ID+"/tokens", map[string]string{"alert": "a", "background": "b"}); w.Code != 401 {
		t.Fatalf("revoked device auth: %d", w.Code)
	}
}

func TestUnpairOtherPairingForbidden(t *testing.T) {
	e := newEnv(t)
	a, b := e.newDevice("a"), e.newDevice("b")
	a.pair("alice")
	b.pair("bob")
	if w := a.request("DELETE", "/device/v1/pairings/"+b.PairingID, nil); w.Code != 403 {
		t.Fatalf("%d", w.Code)
	}
	if ds, _ := e.st.ActiveDevicesForUser("bob"); len(ds) != 1 {
		t.Fatal("bob's pairing affected")
	}
}

func TestUnpairFromSender(t *testing.T) {
	e := newEnv(t)
	phone := e.newDevice("phone")
	phone.pair("alice")
	laptop := e.newDevice("laptop")
	laptop.pair("alice") // second pairing for the same user
	bob := e.newDevice("bob")
	bob.pair("bob")

	if w := e.do("DELETE", "/api/v1/users/alice/pairings", nil, nil); w.Code != 401 {
		t.Fatalf("no key: %d", w.Code)
	}
	w := e.api("DELETE", "/api/v1/users/alice/pairings", nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	rev := e.relay.revokes()
	if len(rev) != 2 || !contains(rev, "alert-phone") || !contains(rev, "alert-laptop") {
		t.Fatalf("relay revokes: %v", rev)
	}
	ev := e.events("pairing.revoked")
	if len(ev) != 2 || ev[0]["by"] != "sender" || ev[1]["by"] != "sender" {
		t.Fatalf("events: %v", ev)
	}
	if ds, _ := e.st.ActiveDevicesForUser("alice"); len(ds) != 0 {
		t.Fatal("alice devices still active")
	}
	if ds, _ := e.st.ActiveDevicesForUser("bob"); len(ds) != 1 {
		t.Fatal("bob affected")
	}
	// Idempotent: nothing more to revoke, no new events.
	if w := e.api("DELETE", "/api/v1/users/alice/pairings", nil); w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	if len(e.events("pairing.revoked")) != 2 {
		t.Fatal("duplicate revoke events")
	}
}

func TestUnpairSurvivesRelayRevokeFailure(t *testing.T) {
	e := newEnv(t)
	d := e.newDevice("phone")
	d.pair("alice")
	e.relay.RevokeErr = relay.ErrRateLimited
	if w := d.request("DELETE", "/device/v1/pairings/"+d.PairingID, nil); w.Code != 204 {
		t.Fatalf("%d", w.Code)
	}
	if ds, _ := e.st.ActiveDevicesForUser("alice"); len(ds) != 0 {
		t.Fatal("pairing not revoked locally")
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// The same device (same signing key) registering again for a pairing it already belongs to is one device, not two:
// after an app reinstall the Secure Enclave key survives and iCloud restores the pairing, and the app may even join
// twice in the same second. Before, each join added a device and one phone got every message several times.
func TestSameDeviceJoiningAgainUpdatesItInsteadOfAddingOne(t *testing.T) {
	e := newEnv(t)
	phone := e.newDevice("phone")
	phone.pair("alice")

	watch := e.newDevice("watch")
	w1 := watch.addTo(phone.PairingID, phone.PairingKey)
	if w1.Code != 201 {
		t.Fatalf("first join: %d %s", w1.Code, w1.Body)
	}
	first := decode[struct {
		DeviceID string `json:"device_id"`
	}](t, w1).DeviceID

	e.clk.Advance(time.Second)
	watch.Tokens["alert"] = "alert-after-reinstall"
	w2 := watch.addTo(phone.PairingID, phone.PairingKey)
	if w2.Code != 201 && w2.Code != 200 {
		t.Fatalf("second join: %d %s", w2.Code, w2.Body)
	}
	second := decode[struct {
		DeviceID string `json:"device_id"`
	}](t, w2).DeviceID
	if second != first {
		t.Fatalf("rejoin created device %s, want the existing %s", second, first)
	}

	devices, err := e.st.ActiveDevicesForUser("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 { // the phone and the watch, not three
		t.Fatalf("%d active devices, want 2", len(devices))
	}
	for _, d := range devices {
		if d.ID == first && d.SealedAlert != "alert-after-reinstall" {
			t.Fatalf("tokens not refreshed on rejoin: %q", d.SealedAlert)
		}
	}
}

// The app used to treat any 401 on sync as "the pairing was revoked" and deleted its key everywhere (the revocation
// syncs through iCloud). A single retired device registration is not a revoked pairing, so the hub says which it is,
// but only to a caller that proves it holds the device's key.
func TestAuthFailureNamesTheCauseOnlyToTheKeyHolder(t *testing.T) {
	code := func(w *httptest.ResponseRecorder) string {
		return decode[struct {
			Error string `json:"error"`
		}](t, w).Error
	}

	// Retired device, pairing still active.
	e := newEnv(t)
	phone := e.newDevice("phone")
	phone.pair("alice")
	watch := e.newDevice("watch")
	wj := watch.addTo(phone.PairingID, phone.PairingKey)
	if wj.Code != 201 {
		t.Fatal(wj.Body)
	}
	watch.ID = decode[struct {
		DeviceID string `json:"device_id"`
	}](t, wj).DeviceID
	if err := e.st.RevokeDevice(watch.ID); err != nil {
		t.Fatal(err)
	}
	w := watch.request("GET", "/device/v1/sync", nil)
	if w.Code != 401 || code(w) != "device_revoked" {
		t.Fatalf("retired device: %d %s, want 401 device_revoked", w.Code, w.Body)
	}
	// The pairing itself is untouched: the phone still works.
	if w := phone.request("GET", "/device/v1/sync", nil); w.Code != 200 {
		t.Fatalf("phone after the watch was retired: %d %s", w.Code, w.Body)
	}

	// Revoked pairing.
	if _, err := e.st.RevokePairing(phone.PairingID); err != nil {
		t.Fatal(err)
	}
	if w := phone.request("GET", "/device/v1/sync", nil); w.Code != 401 || code(w) != "pairing_revoked" {
		t.Fatalf("revoked pairing: %d %s, want 401 pairing_revoked", w.Code, w.Body)
	}

	// A caller without the key (wrong signature) learns nothing about the state, revoked or not.
	imposter := e.newDevice("imposter")
	imposter.ID = phone.ID
	if w := imposter.request("GET", "/device/v1/sync", nil); w.Code != 401 || code(w) != "unauthorized" {
		t.Fatalf("wrong key on a revoked pairing: %d %s, want generic unauthorized", w.Code, w.Body)
	}
	// An unknown device id is generic too.
	ghost := e.newDevice("ghost")
	ghost.ID = "dev_doesnotexist"
	if w := ghost.request("GET", "/device/v1/sync", nil); w.Code != 401 || code(w) != "unauthorized" {
		t.Fatalf("unknown device: %d %s, want generic unauthorized", w.Code, w.Body)
	}
}

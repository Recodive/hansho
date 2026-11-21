package delivery

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/recodive/hansho/hub/internal/relay"
	"github.com/recodive/hansho/hub/internal/store"
)

func TestContentStatePlaintext(t *testing.T) {
	cs, err := ContentState("deploy", json.RawMessage(`{"progress": 0.4}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(cs) != `{"t":"deploy","d":{"progress":0.4}}` {
		t.Fatalf("%s", cs)
	}
}

func TestContentStateEncryptedRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	cs, err := ContentState("deploy", json.RawMessage(`{"progress":0.4}`), key)
	if err != nil {
		t.Fatal(err)
	}
	var wrapper map[string]string
	if err := json.Unmarshal(cs, &wrapper); err != nil || len(wrapper) != 1 || wrapper["blob"] == "" {
		t.Fatalf("shape: %s", cs)
	}
	blob, err := base64.RawURLEncoding.DecodeString(wrapper["blob"])
	if err != nil {
		t.Fatal(err)
	}
	// Independent decryption: HKDF(pairing_key, info="hansho/v1/activity"), AES-256-GCM, nonce || ct || tag.
	k, err := hkdf.Key(sha256.New, key, nil, "hansho/v1/activity", 32)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := aes.NewCipher(k)
	g, _ := cipher.NewGCM(b)
	pt, err := g.Open(nil, blob[:12], blob[12:], nil)
	if err != nil {
		t.Fatalf("derived key does not decrypt: %v", err)
	}
	if string(pt) != `{"t":"deploy","d":{"progress":0.4}}` {
		t.Fatalf("%s", pt)
	}
	if strings.Contains(string(cs), "progress") {
		t.Fatal("plaintext leaked into the encrypted content-state")
	}
}

func TestContentStateSizeLimit(t *testing.T) {
	big := json.RawMessage(`{"x":"` + strings.Repeat("a", 4100) + `"}`)
	if _, err := ContentState("t", big, nil); !errors.Is(err, ErrContentStateTooLarge) {
		t.Fatalf("plaintext: %v", err)
	}
	if _, err := ContentState("t", big, make([]byte, 32)); !errors.Is(err, ErrContentStateTooLarge) {
		t.Fatalf("encrypted: %v", err)
	}
	if _, err := ContentState("t", json.RawMessage(`{"x":"`+strings.Repeat("a", 3000)+`"}`), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ContentState("t", json.RawMessage(`{`), nil); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestPayloadBuilders(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := store.Activity{ID: "act_1", TemplateID: "deploy", ChannelID: "ops"}
	state := json.RawMessage(`{"t":"deploy","d":{}}`)

	got, err := StartPayload(now, a, "p1", state, &ActivityAlert{Title: "T", Body: "B"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"aps":{"timestamp":1700000000,"event":"start","content-state":{"t":"deploy","d":{}},"attributes-type":"HanshoActivityAttributes","attributes":{"activity_id":"act_1","template_id":"deploy","channel":"ops","pairing_id":"p1"},"alert":{"title":"T","body":"B"}}}`
	if string(got) != want {
		t.Fatalf("start\n got %s\nwant %s", got, want)
	}
	got, _ = UpdatePayload(now, state, nil)
	if string(got) != `{"aps":{"timestamp":1700000000,"event":"update","content-state":{"t":"deploy","d":{}}}}` {
		t.Fatalf("update %s", got)
	}
	got, _ = EndPayload(now, state)
	if string(got) != `{"aps":{"timestamp":1700000000,"event":"end","content-state":{"t":"deploy","d":{}},"dismissal-date":1700000000}}` {
		t.Fatalf("end %s", got)
	}
}

func TestActivityLifetimeConstant(t *testing.T) {
	if ActivityLifetime != 7*time.Hour+50*time.Minute {
		t.Fatal(ActivityLifetime)
	}
}

func newActivity(f *fixture, user string, devices ...string) store.Activity {
	f.t.Helper()
	a := store.Activity{
		ID: "act_1", ExternalUserID: user, ChannelID: "ops", TemplateID: "deploy",
		StartedAt: f.clk.Now(), EndsAt: f.clk.Now().Add(ActivityLifetime), DataJSON: `{"progress":0.1}`, Priority: 5,
	}
	if err := f.st.CreateActivity(a); err != nil {
		f.t.Fatal(err)
	}
	for _, d := range devices {
		if _, err := f.st.UpsertActivityToken(a.ID, d, "la-"+d); err != nil {
			f.t.Fatal(err)
		}
	}
	return a
}

func TestAutoEndAtSevenHoursFifty(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone", "watch")
	newActivity(f, "alice", "phone", "watch")

	f.clk.Advance(7*time.Hour + 49*time.Minute + 59*time.Second)
	f.run()
	if len(f.relay.Pushes()) != 0 || len(f.events("activity.ended")) != 0 {
		t.Fatal("ended too early")
	}
	f.clk.Advance(time.Second)
	f.run()
	ps := f.relay.Pushes()
	if len(ps) != 2 {
		t.Fatalf("pushes %d", len(ps))
	}
	for _, p := range ps {
		if p.Kind != "liveactivity" || !strings.Contains(string(p.Payload), `"event":"end"`) || !strings.Contains(string(p.Payload), `"dismissal-date":`) {
			t.Fatalf("%+v %s", p, p.Payload)
		}
	}
	ev := f.events("activity.ended")
	if len(ev) != 1 || ev[0]["activity_id"] != "act_1" || ev[0]["reason"] != "timeout" || ev[0]["external_user_id"] != "alice" {
		t.Fatalf("events %v", ev)
	}
	a, _ := f.st.GetActivity("act_1")
	if a.EndedAt.IsZero() {
		t.Fatal("not marked ended")
	}
	f.clk.Advance(time.Hour)
	f.run()
	if len(f.relay.Pushes()) != 2 || len(f.events("activity.ended")) != 1 {
		t.Fatal("ended twice")
	}
}

func TestAutoEndEncryptsPerPairing(t *testing.T) {
	f := newFixture(t)
	f.cfg.LAEncrypt = true
	f.w = NewWorker(f.st, f.relay, f.wh, f.cfg, f.hubKey, f.clk.Now)
	f.pairing("p1", "alice", "phone")
	newActivity(f, "alice", "phone")
	f.clk.Advance(ActivityLifetime)
	f.run()
	p := f.relay.Pushes()[0]
	var wire struct {
		APS struct {
			CS map[string]string `json:"content-state"`
		} `json:"aps"`
	}
	json.Unmarshal(p.Payload, &wire)
	blob, err := base64.RawURLEncoding.DecodeString(wire.APS.CS["blob"])
	if err != nil {
		t.Fatalf("%s", p.Payload)
	}
	k, _ := hkdf.Key(sha256.New, f.keys["p1"], nil, "hansho/v1/activity", 32)
	b, _ := aes.NewCipher(k)
	g, _ := cipher.NewGCM(b)
	pt, err := g.Open(nil, blob[:12], blob[12:], nil)
	if err != nil || !strings.Contains(string(pt), `"progress":0.1`) {
		t.Fatalf("%v %s", err, pt)
	}
}

func TestEndSkipsRevokedAndDropsUnregisteredTokens(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	f.pairing("p2", "alice", "ipad")
	newActivity(f, "alice", "phone", "ipad")
	f.st.RevokePairing("p2")
	f.relay.setErr("la-phone", relay.ErrUnregistered)
	f.clk.Advance(ActivityLifetime)
	f.run()
	if len(f.relay.Pushes()) != 0 {
		t.Fatalf("pushes %d", len(f.relay.Pushes()))
	}
	if ev := f.events("activity.ended"); len(ev) != 1 {
		t.Fatalf("activity.ended must still be reported: %v", ev)
	}
	// The phone's alert token must not be cleared by a Live Activity token failure.
	if d, _ := f.st.GetDevice("phone"); d.SealedAlert == "" {
		t.Fatal("unregistered Live Activity token cleared the alert token")
	}
	if toks, _ := f.st.ActivityTokens("act_1"); len(toks) != 1 || toks[0].DeviceID != "ipad" {
		t.Fatalf("tokens %+v", toks)
	}
}

type startWire struct {
	APS struct {
		Event string                     `json:"event"`
		CS    map[string]json.RawMessage `json:"content-state"`
		Attrs map[string]string          `json:"attributes"`
	} `json:"aps"`
}

func TestStartActivityIncludesPairingIDPerDevice(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	f.pairing("p2", "alice", "ipad")
	a := newActivity(f, "alice")
	n, err := f.w.StartActivity(t.Context(), a, nil)
	if err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	want := map[string]string{"p2s-phone": "p1", "p2s-ipad": "p2"}
	for sealed, pairing := range want {
		ps := f.pushesTo(sealed)
		if len(ps) != 1 || ps[0].Kind != "pushtostart" {
			t.Fatalf("%s: %+v", sealed, ps)
		}
		var w startWire
		if err := json.Unmarshal(ps[0].Payload, &w); err != nil {
			t.Fatal(err)
		}
		if w.APS.Event != "start" || w.APS.Attrs["pairing_id"] != pairing || w.APS.Attrs["activity_id"] != "act_1" ||
			w.APS.Attrs["template_id"] != "deploy" || w.APS.Attrs["channel"] != "ops" {
			t.Fatalf("%s attrs %v", sealed, w.APS.Attrs)
		}
	}
}

func TestStartActivityEncryptedStateOpensWithPairingActivityKey(t *testing.T) {
	f := newFixture(t)
	f.cfg.LAEncrypt = true
	f.w = NewWorker(f.st, f.relay, f.wh, f.cfg, f.hubKey, f.clk.Now)
	f.pairing("p1", "alice", "phone")
	f.pairing("p2", "alice", "ipad")
	a := newActivity(f, "alice")
	if n, err := f.w.StartActivity(t.Context(), a, nil); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	for sealed, pairing := range map[string]string{"p2s-phone": "p1", "p2s-ipad": "p2"} {
		var w startWire
		json.Unmarshal(f.pushesTo(sealed)[0].Payload, &w)
		if len(w.APS.CS) != 1 || w.APS.CS["blob"] == nil {
			t.Fatalf("encrypted mode must yield only blob: %v", w.APS.CS)
		}
		var b64 string
		json.Unmarshal(w.APS.CS["blob"], &b64)
		blob, err := base64.RawURLEncoding.DecodeString(b64)
		if err != nil {
			t.Fatal(err)
		}
		open := func(pairingKey []byte) ([]byte, error) {
			k, _ := hkdf.Key(sha256.New, pairingKey, nil, "hansho/v1/activity", 32)
			bc, _ := aes.NewCipher(k)
			g, _ := cipher.NewGCM(bc)
			return g.Open(nil, blob[:12], blob[12:], nil)
		}
		pt, err := open(f.keys[pairing])
		if err != nil || string(pt) != `{"t":"deploy","d":{"progress":0.1}}` {
			t.Fatalf("%s: %v %s", sealed, err, pt)
		}
		other := "p1"
		if pairing == "p1" {
			other = "p2"
		}
		if _, err := open(f.keys[other]); err == nil {
			t.Fatalf("%s opens with the other pairing's key", sealed)
		}
		if w.APS.Attrs["pairing_id"] != pairing {
			t.Fatalf("attrs %v", w.APS.Attrs)
		}
	}
}

func TestStartActivityPlaintextStateWhenEncryptOff(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	a := newActivity(f, "alice")
	if n, err := f.w.StartActivity(t.Context(), a, nil); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	var w startWire
	json.Unmarshal(f.pushesTo("p2s-phone")[0].Payload, &w)
	if w.APS.CS["blob"] != nil || string(w.APS.CS["t"]) != `"deploy"` || string(w.APS.CS["d"]) != `{"progress":0.1}` {
		t.Fatalf("%v", w.APS.CS)
	}
}

// A channel capped at passive must never produce a sound or banner, so its Live Activities go out without an alert
// (push-to-start, update and the catch-up push for a newly registered token); a louder channel keeps it.
func TestActivityAlertDroppedOnPassiveChannel(t *testing.T) {
	alertOf := func(p relay.PushRequest) *ActivityAlert {
		var w struct {
			APS struct {
				Alert *ActivityAlert `json:"alert"`
			} `json:"aps"`
		}
		if err := json.Unmarshal(p.Payload, &w); err != nil {
			t.Fatal(err)
		}
		return w.APS.Alert
	}
	for _, tc := range []struct {
		maxLevel string
		want     bool
	}{{"passive", false}, {"active", true}} {
		t.Run(tc.maxLevel, func(t *testing.T) {
			f := newFixture(t)
			if err := f.st.UpsertChannel(store.Channel{ID: "ops", Name: "Ops", DefaultLevel: "passive", MaxLevel: tc.maxLevel, Thread: "ops-thread"}); err != nil {
				t.Fatal(err)
			}
			f.pairing("p1", "alice", "phone", "watch")
			a := newActivity(f, "alice", "phone")
			if _, err := f.w.StartActivity(t.Context(), a, &ActivityAlert{Title: "T", Body: "B"}); err != nil {
				t.Fatal(err)
			}
			ps := f.pushesTo("p2s-phone")
			if len(ps) != 1 || (alertOf(ps[0]) != nil) != tc.want {
				t.Fatalf("start alert present = %v, want %v: %s", alertOf(ps[0]) != nil, tc.want, ps[0].Payload)
			}

			if _, err := f.st.UpdateActivityState(a.ID, `{"progress":0.5}`, 5, `{"title":"T","body":"B"}`); err != nil {
				t.Fatal(err)
			}
			cur, err := f.st.GetActivity(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.w.PushActivityUpdate(t.Context(), cur, cur.UpdateSeq); err != nil {
				t.Fatal(err)
			}
			if ps := f.pushesTo("la-phone"); len(ps) != 1 || (alertOf(ps[0]) != nil) != tc.want {
				t.Fatalf("update alert wrong: %+v", ps)
			}

			tok, err := f.st.UpsertActivityToken(a.ID, "watch", "la-watch")
			if err != nil {
				t.Fatal(err)
			}
			if err := f.w.SyncActivityToken(t.Context(), cur, tok); err != nil {
				t.Fatal(err)
			}
			if ps := f.pushesTo("la-watch"); len(ps) != 1 || (alertOf(ps[0]) != nil) != tc.want {
				t.Fatalf("sync alert wrong: %+v", ps)
			}
		})
	}
}

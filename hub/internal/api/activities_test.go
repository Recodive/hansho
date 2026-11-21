package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/recodive/hansho/hub/internal/config"
	"github.com/recodive/hansho/hub/internal/crypto"
	"github.com/recodive/hansho/hub/internal/relay"
)

type liveFixture struct {
	e     *env
	phone *testDevice
}

func newLive(t *testing.T, mod func(*config.Config)) *liveFixture {
	t.Helper()
	e := newEnvCfg(t, mod)
	e.channel("ops", "active", "active")
	if w := e.api("PUT", "/api/v1/templates/deploy", map[string]any{"version": 1, "body": json.RawMessage(goodTemplate)}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	phone := e.newDevice("phone")
	phone.pair("alice")
	return &liveFixture{e: e, phone: phone}
}

func startBody(data any) map[string]any {
	return map[string]any{"external_user_id": "alice", "channel": "ops", "template_id": "deploy", "data": data}
}

func (l *liveFixture) start(data any) string {
	l.e.t.Helper()
	w := l.e.api("POST", "/api/v1/activities", startBody(data))
	if w.Code != 201 {
		l.e.t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	return decode[struct {
		ActivityID string `json:"activity_id"`
	}](l.e.t, w).ActivityID
}

func (l *liveFixture) registerToken(d *testDevice, actID, sealed string) int {
	l.e.t.Helper()
	return d.request("POST", "/device/v1/activities/"+actID+"/token", map[string]string{"sealed_update": sealed}).Code
}

func (l *liveFixture) pushesOfKind(kind string) []relay.PushRequest {
	var out []relay.PushRequest
	for _, p := range l.e.relay.pushes() {
		if p.Kind == kind {
			out = append(out, p)
		}
	}
	return out
}

type apsLive struct {
	APS struct {
		Timestamp      int64           `json:"timestamp"`
		Event          string          `json:"event"`
		ContentState   json.RawMessage `json:"content-state"`
		AttributesType string          `json:"attributes-type"`
		Attributes     map[string]string
		Alert          map[string]string `json:"alert"`
		DismissalDate  int64             `json:"dismissal-date"`
	} `json:"aps"`
}

func parseLive(t *testing.T, p relay.PushRequest) apsLive {
	t.Helper()
	var a apsLive
	if err := json.Unmarshal(p.Payload, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestStartSendsPushToStartToCapableDevicesOnly(t *testing.T) {
	l := newLive(t, nil)
	e := l.e
	// A second device of alice without a push-to-start token, and a device of bob.
	watch := e.newDevice("watch")
	watch.Tokens["pushtostart"] = ""
	if w := watch.addTo(l.phone.PairingID, l.phone.PairingKey); w.Code != 201 {
		t.Fatal(w.Body)
	}
	bob := e.newDevice("bobphone")
	bob.pair("bob")

	w := e.api("POST", "/api/v1/activities", map[string]any{
		"external_user_id": "alice", "channel": "ops", "template_id": "deploy",
		"data": map[string]any{"progress": 0.1}, "alert": map[string]string{"title": "Deploy", "body": "started"},
	})
	if w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	id := decode[struct {
		ActivityID string `json:"activity_id"`
	}](t, w).ActivityID
	if !strings.HasPrefix(id, "act_") {
		t.Fatal(id)
	}
	ps := l.pushesOfKind("pushtostart")
	if len(ps) != 1 || ps[0].Sealed != "p2s-phone" || len(e.relay.pushes()) != 1 {
		t.Fatalf("pushes: %+v", e.relay.pushes())
	}
	a := parseLive(t, ps[0])
	if a.APS.Event != "start" || a.APS.AttributesType != "HanshoActivityAttributes" ||
		a.APS.Attributes["activity_id"] != id || a.APS.Attributes["template_id"] != "deploy" || a.APS.Attributes["channel"] != "ops" ||
		a.APS.Timestamp != t0.Unix() || a.APS.Alert["title"] != "Deploy" || a.APS.Alert["body"] != "started" {
		t.Fatalf("%s", ps[0].Payload)
	}
	if string(a.APS.ContentState) != `{"t":"deploy","d":{"progress":0.1}}` {
		t.Fatalf("content-state %s", a.APS.ContentState)
	}
	if ps[0].Priority != 10 {
		t.Fatalf("priority %d", ps[0].Priority)
	}
}

func TestStartValidation(t *testing.T) {
	l := newLive(t, nil)
	e := l.e
	post := func(m map[string]any) (int, string) {
		w := e.api("POST", "/api/v1/activities", m)
		return w.Code, w.Body.String()
	}
	m := startBody(map[string]any{})
	m["channel"] = "nope"
	if c, b := post(m); c != 404 || !strings.Contains(b, "unknown_channel") {
		t.Errorf("unknown channel: %d %s", c, b)
	}
	m = startBody(map[string]any{})
	m["template_id"] = "nope"
	if c, b := post(m); c != 400 || !strings.Contains(b, "template_id") {
		t.Errorf("unknown template: %d %s", c, b)
	}
	for _, f := range []string{"external_user_id", "channel", "template_id", "data"} {
		m = startBody(map[string]any{})
		delete(m, f)
		if c, b := post(m); c != 400 || !strings.Contains(b, `"`+f+`"`) {
			t.Errorf("missing %s: %d %s", f, c, b)
		}
	}
	if w := e.do("POST", "/api/v1/activities", startBody(map[string]any{}), nil); w.Code != 401 {
		t.Errorf("unauthenticated: %d", w.Code)
	}
	big := map[string]any{"x": strings.Repeat("a", 4100)}
	if c, b := post(startBody(big)); c != 413 || !strings.Contains(b, "payload_too_large") {
		t.Errorf("4 KB cap: %d %s", c, b)
	}
}

// Update before any update token exists is accepted and delivered when the token arrives, once.
func TestUpdateBeforeTokenIsQueuedUntilTokenArrives(t *testing.T) {
	l := newLive(t, nil)
	e := l.e
	id := l.start(map[string]any{"progress": 0.1})

	w := e.api("PATCH", "/api/v1/activities/"+id, map[string]any{"data": map[string]any{"progress": 0.5}})
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(l.pushesOfKind("liveactivity")) != 0 {
		t.Fatal("pushed without a token")
	}

	if code := l.registerToken(l.phone, id, "la-phone"); code != 204 {
		t.Fatalf("token: %d", code)
	}
	ps := l.pushesOfKind("liveactivity")
	if len(ps) != 1 || ps[0].Sealed != "la-phone" || ps[0].Priority != 5 {
		t.Fatalf("%+v", ps)
	}
	a := parseLive(t, ps[0])
	if a.APS.Event != "update" || string(a.APS.ContentState) != `{"t":"deploy","d":{"progress":0.5}}` {
		t.Fatalf("%s", ps[0].Payload)
	}
	// Re-registering the same token must not deliver again.
	if code := l.registerToken(l.phone, id, "la-phone"); code != 204 {
		t.Fatalf("token again: %d", code)
	}
	if len(l.pushesOfKind("liveactivity")) != 1 {
		t.Fatal("update delivered twice")
	}
}

func TestTokenForActivityWithoutUpdatesSendsNothing(t *testing.T) {
	l := newLive(t, nil)
	id := l.start(map[string]any{"progress": 0.1})
	if code := l.registerToken(l.phone, id, "la-phone"); code != 204 {
		t.Fatalf("%d", code)
	}
	if len(l.pushesOfKind("liveactivity")) != 0 {
		t.Fatal("the start push already carried this state")
	}
}

func TestUpdatePushesToRegisteredTokensWithPriority(t *testing.T) {
	l := newLive(t, nil)
	e := l.e
	id := l.start(map[string]any{"progress": 0.1})
	l.registerToken(l.phone, id, "la-phone")

	if w := e.api("PATCH", "/api/v1/activities/"+id, map[string]any{"data": map[string]any{"progress": 0.2}}); w.Code != 202 {
		t.Fatal(w.Body)
	}
	if w := e.api("PATCH", "/api/v1/activities/"+id, map[string]any{
		"data": map[string]any{"progress": 0.3}, "priority": 10, "alert": map[string]string{"title": "Almost", "body": "done"},
	}); w.Code != 202 {
		t.Fatal(w.Body)
	}
	ps := l.pushesOfKind("liveactivity")
	if len(ps) != 2 || ps[0].Priority != 5 || ps[1].Priority != 10 {
		t.Fatalf("%+v", ps)
	}
	if a := parseLive(t, ps[1]); a.APS.Alert["title"] != "Almost" || string(a.APS.ContentState) != `{"t":"deploy","d":{"progress":0.3}}` {
		t.Fatalf("%s", ps[1].Payload)
	}
	if w := e.api("PATCH", "/api/v1/activities/"+id, map[string]any{"data": map[string]any{}, "priority": 7}); w.Code != 400 {
		t.Fatalf("priority 7: %d", w.Code)
	}
	big := map[string]any{"x": strings.Repeat("a", 4100)}
	if w := e.api("PATCH", "/api/v1/activities/"+id, map[string]any{"data": big}); w.Code != 413 {
		t.Fatalf("4 KB cap on update: %d", w.Code)
	}
	if w := e.api("PATCH", "/api/v1/activities/act_nope", map[string]any{"data": map[string]any{}}); w.Code != 404 {
		t.Fatalf("unknown: %d", w.Code)
	}
	if w := e.api("PATCH", "/api/v1/activities/"+id, map[string]any{}); w.Code != 400 {
		t.Fatalf("missing data: %d", w.Code)
	}
}

func TestUnregisteredUpdateTokenIsDropped(t *testing.T) {
	l := newLive(t, nil)
	e := l.e
	id := l.start(map[string]any{})
	l.registerToken(l.phone, id, "la-phone")
	e.relay.PushErr["la-phone"] = relay.ErrUnregistered
	if w := e.api("PATCH", "/api/v1/activities/"+id, map[string]any{"data": map[string]any{"a": 1}}); w.Code != 202 {
		t.Fatalf("%d", w.Code)
	}
	if toks, _ := e.st.ActivityTokens(id); len(toks) != 0 {
		t.Fatalf("dead token kept: %+v", toks)
	}
	if d, _ := e.st.GetDevice(l.phone.ID); d.SealedAlert == "" || d.SealedPushToStart == "" {
		t.Fatal("device tokens were cleared by a Live Activity token failure")
	}
}

func TestEndActivity(t *testing.T) {
	l := newLive(t, nil)
	e := l.e
	id := l.start(map[string]any{"progress": 1})
	l.registerToken(l.phone, id, "la-phone")
	if w := e.api("DELETE", "/api/v1/activities/"+id, nil); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	ps := l.pushesOfKind("liveactivity")
	if len(ps) != 1 {
		t.Fatalf("%d pushes", len(ps))
	}
	a := parseLive(t, ps[0])
	if a.APS.Event != "end" || a.APS.DismissalDate != t0.Unix() {
		t.Fatalf("%s", ps[0].Payload)
	}
	// Idempotent; further updates conflict.
	if w := e.api("DELETE", "/api/v1/activities/"+id, nil); w.Code != 202 {
		t.Fatalf("second delete: %d", w.Code)
	}
	if len(l.pushesOfKind("liveactivity")) != 1 {
		t.Fatal("second delete pushed again")
	}
	if w := e.api("PATCH", "/api/v1/activities/"+id, map[string]any{"data": map[string]any{}}); w.Code != 409 {
		t.Fatalf("update after end: %d", w.Code)
	}
	if code := l.registerToken(l.phone, id, "la-new"); code != 409 {
		t.Fatalf("token after end: %d", code)
	}
	if w := e.api("DELETE", "/api/v1/activities/act_nope", nil); w.Code != 404 {
		t.Fatalf("unknown: %d", w.Code)
	}
}

func TestAutoEndWithWebhook(t *testing.T) {
	l := newLive(t, nil)
	e := l.e
	id := l.start(map[string]any{})
	l.registerToken(l.phone, id, "la-phone")

	e.clk.Advance(7*time.Hour + 49*time.Minute)
	e.runWorker()
	if len(l.pushesOfKind("liveactivity")) != 0 || len(e.events("activity.ended")) != 0 {
		t.Fatal("ended early")
	}
	e.clk.Advance(time.Minute)
	e.runWorker()
	e.runWorker()
	ps := l.pushesOfKind("liveactivity")
	if len(ps) != 1 || parseLive(t, ps[0]).APS.Event != "end" {
		t.Fatalf("%+v", ps)
	}
	ev := e.events("activity.ended")
	if len(ev) != 1 || ev[0]["activity_id"] != id || ev[0]["reason"] != "timeout" {
		t.Fatalf("%v", ev)
	}
	if w := e.api("PATCH", "/api/v1/activities/"+id, map[string]any{"data": map[string]any{}}); w.Code != 409 {
		t.Fatalf("update after auto-end: %d", w.Code)
	}
}

func TestTokenAuthorisation(t *testing.T) {
	l := newLive(t, nil)
	e := l.e
	id := l.start(map[string]any{})
	bob := e.newDevice("bobphone")
	bob.pair("bob")
	if code := l.registerToken(bob, id, "la-bob"); code != 403 {
		t.Fatalf("other user's activity: %d", code)
	}
	if code := l.registerToken(l.phone, "act_nope", "x"); code != 404 {
		t.Fatalf("unknown activity: %d", code)
	}
	if w := l.phone.request("POST", "/device/v1/activities/"+id+"/token", map[string]string{}); w.Code != 400 {
		t.Fatalf("missing token: %d", w.Code)
	}
	if w := e.do("POST", "/device/v1/activities/"+id+"/token", `{"sealed_update":"x"}`, nil); w.Code != 401 {
		t.Fatalf("unsigned: %d", w.Code)
	}
}

func TestEncryptedContentStateModeRoundTrip(t *testing.T) {
	l := newLive(t, func(c *config.Config) { c.LAEncrypt = true })
	e := l.e
	id := l.start(map[string]any{"progress": 0.1})
	start := l.pushesOfKind("pushtostart")[0]
	var wire apsLive
	json.Unmarshal(start.Payload, &wire)
	var cs map[string]string
	if err := json.Unmarshal(wire.APS.ContentState, &cs); err != nil || cs["blob"] == "" {
		t.Fatalf("%s", wire.APS.ContentState)
	}
	blob, err := b64url.DecodeString(cs["blob"])
	if err != nil {
		t.Fatal(err)
	}
	pt, err := crypto.OpenActivityState(l.phone.PairingKey, blob)
	if err != nil || string(pt) != `{"t":"deploy","d":{"progress":0.1}}` {
		t.Fatalf("%v %s", err, pt)
	}
	if strings.Contains(string(start.Payload), "progress") {
		t.Fatal("plaintext in encrypted mode")
	}
	// Updates are encrypted too.
	l.registerToken(l.phone, id, "la-phone")
	e.api("PATCH", "/api/v1/activities/"+id, map[string]any{"data": map[string]any{"progress": 0.9}})
	up := l.pushesOfKind("liveactivity")[0]
	if strings.Contains(string(up.Payload), "0.9") {
		t.Fatal("update leaked plaintext")
	}
}

package api

import (
	"strings"
	"testing"
	"time"

	"github.com/recodive/hansho/hub/internal/relay"
)

type pageFixture struct {
	e            *env
	phone, watch *testDevice
	id           string
}

func newPage(t *testing.T, channelActions []any) *pageFixture {
	t.Helper()
	e := newEnv(t)
	body := map[string]any{"name": "oncall", "default_level": "page", "max_level": "page"}
	if channelActions != nil {
		body["actions"] = channelActions
	}
	if w := e.api("PUT", "/api/v1/channels/oncall", body); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	phone, watch := e.newDevice("phone"), e.newDevice("watch")
	phone.pair("alice")
	if w := watch.addTo(phone.PairingID, phone.PairingKey); w.Code != 201 {
		t.Fatal(w.Body)
	} else {
		watch.ID = decode[struct {
			DeviceID string `json:"device_id"`
		}](t, w).DeviceID
	}
	code, r, raw := e.send(msg("alice", "oncall"))
	if code != 202 || r.Level != "page" {
		t.Fatalf("%d %s", code, raw)
	}
	e.runWorker()
	return &pageFixture{e: e, phone: phone, watch: watch, id: r.MessageID}
}

func (p *pageFixture) status() msgResp {
	p.e.t.Helper()
	return decode[msgResp](p.e.t, p.e.api("GET", "/api/v1/messages/"+p.id, nil))
}

func (p *pageFixture) cancelPushes() []relay.PushRequest {
	var out []relay.PushRequest
	for _, ps := range p.e.relay.pushes() {
		// The acknowledge broadcast is a passive alert collapsed onto the page banner.
		if ps.Kind == "alert" && ps.Priority == 5 && ps.CollapseID == p.id &&
			strings.Contains(string(ps.Payload), `"interruption-level":"passive"`) {
			out = append(out, ps)
		}
	}
	return out
}

func (p *pageFixture) action(d *testDevice, actionID string, extra map[string]any) int {
	p.e.t.Helper()
	body := map[string]any{"message_id": p.id, "action_id": actionID}
	for k, v := range extra {
		body[k] = v
	}
	return d.request("POST", "/device/v1/actions", body).Code
}

func TestAckStopsPageAndCancelsEveryDevice(t *testing.T) {
	p := newPage(t, nil)
	e := p.e
	if len(e.relay.pushes()) != 2 {
		t.Fatalf("initial pushes %d", len(e.relay.pushes()))
	}
	e.clk.Advance(120 * time.Second)
	e.runWorker() // first resend
	e.clk.Advance(10 * time.Second)
	before := len(e.relay.pushes())
	if before != 4 {
		t.Fatalf("pushes before ack: %d", before)
	}

	if code := p.action(p.phone, "ack", nil); code != 200 {
		t.Fatalf("ack: %d", code)
	}
	cancels := p.cancelPushes()
	if len(cancels) != 2 {
		t.Fatalf("want one cancel per device, got %d", len(cancels))
	}
	tokens := map[string]bool{}
	for _, c := range cancels {
		tokens[c.Sealed] = true
		if strings.Contains(string(c.Payload), `"sound"`) || strings.Contains(string(c.Payload), "content-available") {
			t.Fatalf("cancel push %+v %s", c, c.Payload)
		}
	}
	if !tokens["alert-phone"] || !tokens["alert-watch"] {
		t.Fatalf("cancel tokens %v", tokens)
	}
	st := p.status()
	if st.Status != "acked" {
		t.Fatalf("%+v", st)
	}
	w := e.api("GET", "/api/v1/messages/"+p.id, nil)
	if !strings.Contains(w.Body.String(), `"acked_at"`) {
		t.Fatalf("no acked_at: %s", w.Body)
	}
	if ev := e.events("message.acked"); len(ev) != 1 || ev[0]["message_id"] != p.id || ev[0]["external_user_id"] != "alice" {
		t.Fatalf("acked events %v", ev)
	}
	if ev := e.events("action"); len(ev) != 1 || ev[0]["action_id"] != "ack" || ev[0]["device_id"] != p.phone.ID ||
		ev[0]["message_id"] != p.id || ev[0]["external_user_id"] != "alice" {
		t.Fatalf("action events %v", ev)
	}

	// No further resends and no expiry.
	e.clk.Advance(time.Hour)
	e.runWorker()
	if len(e.relay.pushes()) != before+2 {
		t.Fatalf("pushes after ack: %d", len(e.relay.pushes()))
	}
	if len(e.events("message.expired")) != 0 {
		t.Fatal("acked page expired")
	}
}

// Review focus 4: late and duplicate acks are idempotent.
func TestSecondAckFromOtherDeviceIsIdempotent(t *testing.T) {
	p := newPage(t, nil)
	p.action(p.phone, "ack", nil)
	pushes, acked, actions := len(p.e.relay.pushes()), len(p.e.events("message.acked")), len(p.e.events("action"))

	if code := p.action(p.watch, "ack", nil); code != 200 {
		t.Fatalf("second ack: %d", code)
	}
	if code := p.action(p.phone, "ack", nil); code != 200 {
		t.Fatalf("third ack: %d", code)
	}
	if len(p.e.relay.pushes()) != pushes {
		t.Fatal("second ack caused another cancel push")
	}
	if len(p.e.events("message.acked")) != acked || len(p.e.events("action")) != actions {
		t.Fatal("second ack caused another webhook")
	}
	if p.status().Status != "acked" {
		t.Fatal("status changed")
	}
}

func TestAckAfterExpiryIsIdempotent(t *testing.T) {
	p := newPage(t, nil)
	e := p.e
	for range 6 { // 5 resends, then expiry
		e.clk.Advance(120 * time.Second)
		e.runWorker()
	}
	if p.status().Status != "expired" {
		t.Fatalf("setup: %s", p.status().Status)
	}
	pushes, expired, actions := len(e.relay.pushes()), len(e.events("message.expired")), len(e.events("action"))
	if code := p.action(p.phone, "ack", nil); code != 200 {
		t.Fatalf("ack after expiry: %d", code)
	}
	if st := p.status().Status; st != "expired" {
		t.Fatalf("expired page changed to %s", st)
	}
	if len(e.relay.pushes()) != pushes || len(e.events("message.acked")) != 0 ||
		len(e.events("message.expired")) != expired || len(e.events("action")) != actions {
		t.Fatal("ack after expiry had side effects")
	}
}

func TestChannelAckActionStopsPage(t *testing.T) {
	p := newPage(t, []any{
		map[string]any{"id": "approve", "title": "Approve", "ack": true},
		map[string]any{"id": "snooze", "title": "Snooze"},
	})
	// A non-ack channel action reports to the sender but keeps paging.
	if code := p.action(p.phone, "snooze", map[string]any{"text": "10 min"}); code != 200 {
		t.Fatalf("snooze: %d", code)
	}
	ev := p.e.events("action")
	if len(ev) != 1 || ev[0]["action_id"] != "snooze" || ev[0]["text"] != "10 min" {
		t.Fatalf("%v", ev)
	}
	if p.status().Status != "paging" || len(p.cancelPushes()) != 0 {
		t.Fatalf("snooze must not stop the page: %+v", p.status())
	}
	if code := p.action(p.watch, "approve", nil); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	if p.status().Status != "acked" || len(p.cancelPushes()) != 2 || len(p.e.events("message.acked")) != 1 {
		t.Fatalf("ack-flagged action did not ack: %+v", p.status())
	}
}

func TestMessageLevelAckActionOverridesChannel(t *testing.T) {
	e := newEnv(t)
	e.channel("oncall", "page", "page")
	d := e.newDevice("phone")
	d.pair("alice")
	m := msg("alice", "oncall")
	m["actions"] = []any{map[string]any{"id": "got-it", "title": "Got it", "ack": true}}
	_, r, _ := e.send(m)
	e.runWorker()
	w := d.request("POST", "/device/v1/actions", map[string]any{"message_id": r.MessageID, "action_id": "got-it"})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if decode[msgResp](t, e.api("GET", "/api/v1/messages/"+r.MessageID, nil)).Status != "acked" {
		t.Fatal("message-level ack action ignored")
	}
}

func TestAckOnNonPageMessageAcksOnceWithoutCancel(t *testing.T) {
	e := newEnv(t)
	e.channel("ops", "active", "active")
	d := e.newDevice("phone")
	d.pair("alice")
	_, r, _ := e.send(msg("alice", "ops"))
	e.runWorker()
	for range 2 {
		w := d.request("POST", "/device/v1/actions", map[string]any{"message_id": r.MessageID, "action_id": "ack"})
		if w.Code != 200 {
			t.Fatalf("%d", w.Code)
		}
	}
	if len(e.events("action")) != 1 || len(e.events("message.acked")) != 1 {
		t.Fatal("the first ack is reported once with message.acked; a repeat is ignored")
	}
	for _, ps := range e.relay.pushes() {
		if ps.Kind == "background" || ps.Priority == 5 {
			t.Fatal("cancel push for a non-page message")
		}
	}
	if decode[msgResp](t, e.api("GET", "/api/v1/messages/"+r.MessageID, nil)).Status != "acked" {
		t.Fatal("status not acked")
	}
}

func TestActionAuthorisationAndValidation(t *testing.T) {
	p := newPage(t, nil)
	e := p.e
	bob := e.newDevice("bobphone")
	bob.pair("bob")

	foreign := bob.request("POST", "/device/v1/actions", map[string]any{"message_id": p.id, "action_id": "ack"})
	unknown := bob.request("POST", "/device/v1/actions", map[string]any{"message_id": "msg_nope", "action_id": "ack"})
	if foreign.Code != 404 || foreign.Code != unknown.Code || foreign.Body.String() != unknown.Body.String() {
		t.Fatalf("another user's message must look like an unknown one: %d %s vs %d %s", foreign.Code, foreign.Body, unknown.Code, unknown.Body)
	}
	if p.status().Status != "paging" || len(e.events("message.acked")) != 0 {
		t.Fatal("foreign ack had an effect")
	}
	w := p.phone.request("POST", "/device/v1/actions", map[string]any{"message_id": "msg_nope", "action_id": "ack"})
	if w.Code != 404 {
		t.Fatalf("unknown message: %d", w.Code)
	}
	for _, body := range []map[string]any{{"action_id": "ack"}, {"message_id": p.id}} {
		if w := p.phone.request("POST", "/device/v1/actions", body); w.Code != 400 {
			t.Fatalf("%v: %d", body, w.Code)
		}
	}
	if w := e.do("POST", "/device/v1/actions", `{"message_id":"x","action_id":"ack"}`, nil); w.Code != 401 {
		t.Fatalf("unsigned: %d", w.Code)
	}
}

func TestAckSurvivesCancelPushFailures(t *testing.T) {
	p := newPage(t, nil)
	p.e.relay.PushErr["alert-watch"] = relay.ErrUnregistered
	if code := p.action(p.phone, "ack", nil); code != 200 {
		t.Fatalf("ack: %d", code)
	}
	if p.status().Status != "acked" {
		t.Fatal("not acked")
	}
	if d, _ := p.e.st.GetDevice(p.watch.ID); d.SealedAlert != "" {
		t.Fatal("dead alert token not cleared")
	}
}

func TestDismissReportedOnlyForMessagesAskingForAck(t *testing.T) {
	e := newEnv(t)
	e.channel("ops", "active", "active")
	d := e.newDevice("phone")
	d.pair("alice")
	_, rPlain, _ := e.send(msg("alice", "ops"))
	withAck := msg("alice", "ops")
	withAck["actions"] = []any{map[string]any{"id": "got-it", "title": "Got it", "ack": true}}
	_, rAck, _ := e.send(withAck)
	e.runWorker()
	for _, id := range []string{rPlain.MessageID, rAck.MessageID} {
		if w := d.request("POST", "/device/v1/actions", map[string]any{"message_id": id, "action_id": "_dismissed"}); w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	ev := e.events("message.dismissed")
	if len(ev) != 1 || ev[0]["message_id"] != rAck.MessageID {
		t.Fatalf("want one message.dismissed for the ack message, got %v", ev)
	}
	if len(e.events("action")) != 0 {
		t.Fatal("a dismiss must not be reported as a button press")
	}
}

func TestDismissAfterAckIsSilent(t *testing.T) {
	p := newPage(t, nil)
	if code := p.action(p.phone, "ack", nil); code != 200 {
		t.Fatalf("ack: %d", code)
	}
	if code := p.action(p.watch, "_dismissed", nil); code != 200 {
		t.Fatalf("dismiss: %d", code)
	}
	if len(p.e.events("message.dismissed")) != 0 {
		t.Fatal("dismissing an acknowledged page must not be reported")
	}
}

func TestDismissAfterNonPageAckIsSilent(t *testing.T) {
	e := newEnv(t)
	e.channel("ops", "active", "active")
	phone, watch := e.newDevice("phone"), e.newDevice("watch")
	phone.pair("alice")
	watch.pair("alice")
	m := msg("alice", "ops")
	m["actions"] = []any{map[string]any{"id": "got-it", "title": "Got it", "ack": true}}
	_, r, _ := e.send(m)
	e.runWorker()
	if w := phone.request("POST", "/device/v1/actions", map[string]any{"message_id": r.MessageID, "action_id": "got-it"}); w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	if w := watch.request("POST", "/device/v1/actions", map[string]any{"message_id": r.MessageID, "action_id": "_dismissed"}); w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	if len(e.events("message.acked")) != 1 || len(e.events("message.dismissed")) != 0 {
		t.Fatal("acknowledged on the phone, then cleared on the watch: acked once, never dismissed")
	}
}

func TestActionIDMustBeOffered(t *testing.T) {
	p := newPage(t, []any{map[string]any{"id": "snooze", "title": "Snooze"}})
	before := len(p.e.events("action"))
	if code := p.action(p.phone, "bogus", nil); code != 400 {
		t.Fatalf("unknown action id: %d", code)
	}
	if code := p.action(p.phone, "snooze", nil); code != 200 {
		t.Fatalf("offered action id: %d", code)
	}
	if got := len(p.e.events("action")); got != before+1 {
		t.Fatalf("only the offered press may be forwarded: %d -> %d action events", before, got)
	}
	if code := p.action(p.watch, "_dismissed", nil); code != 200 {
		t.Fatalf("built-in _dismissed: %d", code)
	}
}

func TestActionRateLimitPerDevice(t *testing.T) {
	p := newPage(t, []any{map[string]any{"id": "snooze", "title": "Snooze"}})
	for i := range actionBurst {
		if code := p.action(p.phone, "snooze", nil); code != 200 {
			t.Fatalf("press %d: %d", i, code)
		}
	}
	w := p.phone.request("POST", "/device/v1/actions", map[string]any{"message_id": p.id, "action_id": "snooze"})
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("past the burst: %d %v", w.Code, w.Header())
	}
	if code := p.action(p.watch, "snooze", nil); code != 200 {
		t.Fatalf("another device shares no bucket: %d", code)
	}
	if got := len(p.e.events("action")); got != actionBurst+1 {
		t.Fatalf("a refused press must not reach the outbox: %d action events", got)
	}
	p.e.clk.Advance(time.Second)
	if code := p.action(p.phone, "snooze", nil); code != 200 {
		t.Fatalf("one token refills per second: %d", code)
	}
	if code := p.action(p.phone, "snooze", nil); code != 429 {
		t.Fatalf("only one: %d", code)
	}
}

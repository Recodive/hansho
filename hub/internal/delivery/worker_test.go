package delivery

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/recodive/hansho/hub/internal/relay"
	"github.com/recodive/hansho/hub/internal/store"
)

func TestFanOutOneEnvelopePerPairing(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone", "watch")
	f.pairing("p2", "alice", "ipad")
	f.pairing("p3", "bob", "bobphone")
	res := f.enqueue("alice", "time-sensitive", func(in *EnqueueInput) {
		in.CollapseID = "disk"
		in.Sound = "ding.caf"
		in.Plaintext.Sound = "ding.caf"
	})
	if len(res.Deliveries) != 3 {
		t.Fatalf("deliveries: %d", len(res.Deliveries))
	}
	f.run()

	pushes := f.relay.Pushes()
	if len(pushes) != 3 {
		t.Fatalf("pushes: %d", len(pushes))
	}
	for _, p := range pushes {
		if p.Kind != "alert" || p.Priority != 10 || p.CollapseID != "disk" {
			t.Fatalf("push shape %+v", p)
		}
		if p.Sealed == "alert-bobphone" {
			t.Fatal("bob's device received alice's message")
		}
	}
	phone, watch, ipad := f.pushesTo("alert-phone")[0], f.pushesTo("alert-watch")[0], f.pushesTo("alert-ipad")[0]
	if !bytes.Equal(phone.Payload, watch.Payload) {
		t.Fatal("devices of one pairing must share the same envelope")
	}
	if bytes.Equal(phone.Payload, ipad.Payload) {
		t.Fatal("different pairings must have different envelopes")
	}
	w1, pt := f.openPush(phone, "p1")
	if w1.Raw.P != "p1" || w1.Raw.M != res.Message.ID || w1.APS.InterruptionLevel != "time-sensitive" {
		t.Fatalf("%+v", w1)
	}
	if pt.Title != "Disk full" || pt.Body != "db-1 at 97%" || pt.Channel != "ops" || pt.Level != "time-sensitive" || pt.Sound != "ding.caf" {
		t.Fatalf("plaintext %+v", pt)
	}
	if w2, _ := f.openPush(ipad, "p2"); w2.Raw.P != "p2" {
		t.Fatalf("ipad envelope for wrong pairing: %+v", w2)
	}

	m := f.msg(res.Message.ID)
	if m.Status != store.MsgDelivered {
		t.Fatalf("status %s", m.Status)
	}
	dels, _ := f.st.Deliveries(m.ID)
	for _, d := range dels {
		if d.Status != store.DelSent || d.APNsID == "" {
			t.Fatalf("delivery %+v", d)
		}
	}
}

func TestPassiveUsesPriority5(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	f.enqueue("alice", "passive")
	f.run()
	if p := f.relay.Pushes(); len(p) != 1 || p[0].Priority != 5 {
		t.Fatalf("%+v", p)
	}
}

func TestPageLevelMarksPlaintextAndClearPG(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	f.enqueue("alice", "page", func(in *EnqueueInput) { in.Plaintext.Page = true })
	f.run()
	w, pt := f.openPush(f.relay.Pushes()[0], "p1")
	if w.Raw.PG != 1 || !pt.Page || w.APS.InterruptionLevel != "critical" {
		t.Fatalf("%+v %+v", w, pt)
	}
}

// Review focus 2: no devices at all -> undeliverable, not an error.
func TestNoDevicesUndeliverable(t *testing.T) {
	f := newFixture(t)
	res := f.enqueue("nobody", "active")
	if res.Message.Status != store.MsgUndeliverable || len(res.Deliveries) != 0 {
		t.Fatalf("%+v", res)
	}
	ev := f.events("message.undeliverable")
	if len(ev) != 1 || ev[0]["message_id"] != res.Message.ID || ev[0]["external_user_id"] != "nobody" {
		t.Fatalf("events %v", ev)
	}
	f.run()
	if len(f.relay.Pushes()) != 0 {
		t.Fatal("pushed without devices")
	}
	if got := f.msg(res.Message.ID).Status; got != store.MsgUndeliverable {
		t.Fatalf("status %s", got)
	}
}

func TestDevicesWithoutAlertTokenAreNotTargets(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	if err := f.st.ClearDeviceToken("phone", store.TokenAlert); err != nil {
		t.Fatal(err)
	}
	res := f.enqueue("alice", "active")
	if res.Message.Status != store.MsgUndeliverable {
		t.Fatalf("status %s", res.Message.Status)
	}
}

func TestDeliveredWebhookOncePerMessage(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone", "watch")
	res := f.enqueue("alice", "active")
	f.run()
	f.run()
	f.clk.Advance(time.Hour)
	f.run()
	ev := f.events("message.delivered")
	if len(ev) != 1 || ev[0]["message_id"] != res.Message.ID || ev[0]["external_user_id"] != "alice" || ev[0]["devices"] != float64(2) {
		t.Fatalf("events %v", ev)
	}
	if len(f.relay.Pushes()) != 2 {
		t.Fatalf("pushes %d", len(f.relay.Pushes()))
	}
}

// Review focus 3a: Unregistered clears the token and the device is skipped from then on.
func TestUnregisteredClearsTokenAndSkipsNextTime(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone", "watch")
	f.relay.setErr("alert-phone", relay.ErrUnregistered)
	res := f.enqueue("alice", "active")
	f.run()

	d, _ := f.st.GetDevice("phone")
	if d.SealedAlert != "" || d.SealedBackground != "" {
		t.Fatalf("token not cleared: %+v", d)
	}
	if w, _ := f.st.GetDevice("watch"); w.SealedAlert == "" {
		t.Fatal("healthy device lost its token")
	}
	if len(f.pushesTo("alert-watch")) != 1 {
		t.Fatal("healthy device not delivered")
	}
	dels, _ := f.st.Deliveries(res.Message.ID)
	got := map[string]store.Delivery{}
	for _, d := range dels {
		got[d.DeviceID] = d
	}
	if got["phone"].Status != store.DelFailed || got["phone"].Error != "unregistered" || got["watch"].Status != store.DelSent {
		t.Fatalf("%+v", got)
	}
	if f.msg(res.Message.ID).Status != store.MsgDelivered {
		t.Fatal("message should be delivered via the healthy device")
	}

	// Not retried, and the next message does not even target it.
	f.relay.setErr("alert-phone", nil)
	f.run()
	res2 := f.enqueue("alice", "active")
	if len(res2.Deliveries) != 1 || res2.Deliveries[0].DeviceID != "watch" {
		t.Fatalf("second message deliveries %+v", res2.Deliveries)
	}
	f.run()
	if len(f.pushesTo("alert-phone")) != 0 {
		t.Fatal("unregistered device was pushed to")
	}
}

func TestOnlyDeviceUnregisteredMakesMessageUndeliverable(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	f.relay.setErr("alert-phone", relay.ErrUnregistered)
	res := f.enqueue("alice", "active")
	f.run()
	if got := f.msg(res.Message.ID).Status; got != store.MsgUndeliverable {
		t.Fatalf("status %s", got)
	}
	if len(f.events("message.undeliverable")) != 1 || len(f.events("message.delivered")) != 0 {
		t.Fatal("webhooks")
	}
}

// The relay's PairingRevoked covers one device's tokens (each device seals under its own relay pairing id, and
// "Remove from this watch" revokes only the watch's): that device is retired, the rest of the pairing keeps working,
// and the pairing is revoked, with one pairing.revoked, only when its last device goes.
func TestPairingRevokedFromRelayRetiresOnlyThatDevice(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone", "watch")
	f.relay.setErr("alert-watch", relay.ErrPairingRevoked)
	res := f.enqueue("alice", "active")
	f.run()

	if w, _ := f.st.GetDevice("watch"); w.RevokedAt.IsZero() {
		t.Fatal("watch not retired")
	}
	if p, _ := f.st.GetPairing("p1"); !p.RevokedAt.IsZero() {
		t.Fatal("pairing revoked although the phone is still active")
	}
	if len(f.events("pairing.revoked")) != 0 {
		t.Fatal("pairing.revoked for a single device")
	}
	if len(f.pushesTo("alert-phone")) != 1 || f.msg(res.Message.ID).Status != store.MsgDelivered {
		t.Fatal("phone not delivered")
	}
	res2 := f.enqueue("alice", "active")
	if len(res2.Deliveries) != 1 || res2.Deliveries[0].DeviceID != "phone" {
		t.Fatalf("%+v", res2.Deliveries)
	}
	f.run()

	// The last device goes too: now the pairing is revoked, once.
	f.relay.setErr("alert-phone", relay.ErrPairingRevoked)
	f.enqueue("alice", "active")
	f.run()
	if p, _ := f.st.GetPairing("p1"); p.RevokedAt.IsZero() {
		t.Fatal("pairing not revoked after its last device")
	}
	ev := f.events("pairing.revoked")
	if len(ev) != 1 || ev[0]["pairing_id"] != "p1" || ev[0]["by"] != "relay" || ev[0]["external_user_id"] != "alice" {
		t.Fatalf("events %v", ev)
	}
}

func TestOnlyPairingRevokedMakesUndeliverable(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	f.relay.setErr("alert-phone", relay.ErrPairingRevoked)
	res := f.enqueue("alice", "active")
	f.run()
	if got := f.msg(res.Message.ID).Status; got != store.MsgUndeliverable {
		t.Fatalf("status %s", got)
	}
}

func TestRateLimitedStaysQueuedThenDelivers(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	f.relay.setErr("alert-phone", &relay.RateLimitedError{RetryAfter: 2 * time.Second})
	res := f.enqueue("alice", "active")
	f.run()
	m := f.msg(res.Message.ID)
	if m.Status != store.MsgQueued || !m.NextAttemptAt.After(f.clk.Now()) {
		t.Fatalf("%+v", m)
	}
	f.run() // not due yet
	f.relay.setErr("alert-phone", nil)
	f.run()
	if len(f.relay.Pushes()) != 0 {
		t.Fatal("retried before it was due")
	}
	f.clk.Advance(30 * time.Second)
	f.run()
	if got := f.msg(res.Message.ID).Status; got != store.MsgDelivered {
		t.Fatalf("status %s", got)
	}
	if len(f.relay.Pushes()) != 1 {
		t.Fatalf("pushes %d", len(f.relay.Pushes()))
	}
}

// countingRelay counts push attempts per sealed token, failed ones included.
type countingRelay struct {
	RelayClient
	tries map[string]int
}

func (c *countingRelay) Push(ctx context.Context, p relay.PushRequest) (string, error) {
	c.tries[p.Sealed]++
	return c.RelayClient.Push(ctx, p)
}

// A 429 does not hold up the serial worker: the rest of that pairing's deliveries in the pass wait for a later pass
// without another relay call, and other pairings are delivered.
func TestRateLimitedPairingSkippedForTheRestOfThePass(t *testing.T) {
	f := newFixture(t)
	cr := &countingRelay{RelayClient: f.relay, tries: map[string]int{}}
	f.w = NewWorker(f.st, cr, f.wh, f.cfg, f.hubKey, f.clk.Now)
	f.pairing("p1", "alice", "phone", "watch")
	f.pairing("p2", "bob", "bobphone")
	f.relay.setErr("alert-phone", &relay.RateLimitedError{RetryAfter: 20 * time.Second})
	m1 := f.enqueue("alice", "active")
	m2 := f.enqueue("alice", "active")
	m3 := f.enqueue("bob", "active")
	f.run()

	if cr.tries["alert-phone"] != 1 || cr.tries["alert-watch"] != 0 {
		t.Fatalf("relay tries %v, want one for the phone and none for the watch", cr.tries)
	}
	for _, id := range []string{m1.Message.ID, m2.Message.ID} {
		if m := f.msg(id); m.Status != store.MsgQueued || m.NextAttemptAt.Before(f.clk.Now().Add(20*time.Second)) {
			t.Fatalf("%s: %+v, want queued for at least the Retry-After", id, m)
		}
	}
	if f.msg(m3.Message.ID).Status != store.MsgDelivered {
		t.Fatal("other pairing held up")
	}

	f.relay.setErr("alert-phone", nil)
	f.clk.Advance(30 * time.Second)
	f.run()
	for _, id := range []string{m1.Message.ID, m2.Message.ID} {
		if got := f.msg(id).Status; got != store.MsgDelivered {
			t.Fatalf("%s: %s", id, got)
		}
	}
}

func TestTransientFailuresGiveUpEventually(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	f.relay.setErr("alert-phone", errors.New("connection refused"))
	res := f.enqueue("alice", "active")
	f.run()
	if f.msg(res.Message.ID).Status != store.MsgQueued {
		t.Fatal("should wait for retry")
	}
	f.clk.Advance(11 * time.Minute)
	f.run()
	m := f.msg(res.Message.ID)
	if m.Status != store.MsgUndeliverable {
		t.Fatalf("status %s", m.Status)
	}
	dels, _ := f.st.Deliveries(m.ID)
	if dels[0].Status != store.DelFailed || dels[0].Error != "timeout" {
		t.Fatalf("%+v", dels[0])
	}
}

func TestPermanentRelayErrorsFailDelivery(t *testing.T) {
	for name, err := range map[string]error{
		"token_expired": relay.ErrTokenExpired,
		"forbidden":     &relay.HTTPError{Status: 403, Code: "forbidden"},
	} {
		f := newFixture(t)
		f.pairing("p1", "alice", "phone")
		f.relay.setErr("alert-phone", err)
		res := f.enqueue("alice", "active")
		f.run()
		dels, _ := f.st.Deliveries(res.Message.ID)
		if dels[0].Status != store.DelFailed || f.msg(res.Message.ID).Status != store.MsgUndeliverable {
			t.Errorf("%s: %+v", name, dels[0])
		}
		if name == "token_expired" && dels[0].Error != "token_expired" {
			t.Errorf("error %q", dels[0].Error)
		}
	}
}

func TestEnqueueRejectsOversizedPayload(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	ch, _ := f.st.GetChannel("ops")
	_, err := Enqueue(context.Background(), f.st, f.wh, f.cfg, f.hubKey, f.clk.Now, EnqueueInput{
		ExternalUserID: "alice", Channel: ch, Level: "active",
		Plaintext: Plaintext{Title: "t", Body: strings.Repeat("x", 5000), Channel: "ops", Level: "active"},
	})
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("got %v", err)
	}
	// Also when the user has no devices: the verdict must not depend on device state.
	_, err = Enqueue(context.Background(), f.st, f.wh, f.cfg, f.hubKey, f.clk.Now, EnqueueInput{
		ExternalUserID: "nobody", Channel: ch, Level: "active",
		Plaintext: Plaintext{Title: "t", Body: strings.Repeat("x", 5000), Channel: "ops", Level: "active"},
	})
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("no-device case: %v", err)
	}
}

func TestPayloadAtRestIsWrapped(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	res := f.enqueue("alice", "active")
	if bytes.Contains(res.Message.PayloadWrapped, []byte("Disk full")) {
		t.Fatal("message plaintext stored unwrapped")
	}
	m := f.msg(res.Message.ID)
	if bytes.Contains(m.PayloadWrapped, []byte("Disk full")) || len(m.PayloadWrapped) == 0 {
		t.Fatal("message plaintext stored unwrapped")
	}
}

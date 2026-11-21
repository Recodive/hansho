package delivery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/recodive/hansho/hub/internal/relay"
	"github.com/recodive/hansho/hub/internal/store"
)

func page(f *fixture, user string, mod ...func(*EnqueueInput)) store.Message {
	f.t.Helper()
	mods := append([]func(*EnqueueInput){func(in *EnqueueInput) { in.Plaintext.Page = true }}, mod...)
	return f.enqueue(user, "page", mods...).Message
}

func TestPageResendSchedule(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	m := page(f, "alice")
	if m.PageIntervalS != 120 || m.PageMaxRetries != 5 || m.CollapseID != m.ID {
		t.Fatalf("defaults: %+v", m)
	}

	f.run() // initial send at t0
	if got := f.msg(m.ID); got.Status != store.MsgPaging || !got.NextAttemptAt.Equal(t0.Add(120*time.Second)) || got.Attempts != 0 {
		t.Fatalf("after first send: %+v", got)
	}
	if len(f.relay.Pushes()) != 1 {
		t.Fatalf("pushes %d", len(f.relay.Pushes()))
	}
	if ev := f.events("message.delivered"); len(ev) != 1 {
		t.Fatalf("delivered events %d", len(ev))
	}

	f.clk.Advance(119 * time.Second)
	f.run()
	if len(f.relay.Pushes()) != 1 {
		t.Fatal("resent too early")
	}

	// Resends at +120, +240, +360, +480, +600.
	for i := 1; i <= 5; i++ {
		f.clk.Advance(time.Duration(map[bool]int{true: 1, false: 120}[i == 1]) * time.Second)
		f.run()
		got := f.msg(m.ID)
		if len(f.relay.Pushes()) != 1+i || got.Attempts != i || got.Status != store.MsgPaging {
			t.Fatalf("resend %d: pushes=%d %+v", i, len(f.relay.Pushes()), got)
		}
		if want := f.clk.Now().Add(120 * time.Second); !got.NextAttemptAt.Equal(want) {
			t.Fatalf("resend %d: next %v want %v", i, got.NextAttemptAt, want)
		}
	}
	if !f.clk.Now().Equal(t0.Add(600 * time.Second)) {
		t.Fatalf("test clock drifted: %v", f.clk.Now())
	}
	if len(f.events("message.expired")) != 0 {
		t.Fatal("expired too early")
	}

	// After the 5th resend the page gets one more interval, then expires without a resend.
	f.clk.Advance(120 * time.Second)
	f.run()
	if got := f.msg(m.ID); got.Status != store.MsgExpired {
		t.Fatalf("status %s", got.Status)
	}
	if len(f.relay.Pushes()) != 6 {
		t.Fatalf("pushes %d, want 6 (1 + 5 resends)", len(f.relay.Pushes()))
	}
	ev := f.events("message.expired")
	if len(ev) != 1 || ev[0]["message_id"] != m.ID || ev[0]["external_user_id"] != "alice" {
		t.Fatalf("expired events %v", ev)
	}
	f.clk.Advance(time.Hour)
	f.run()
	if len(f.relay.Pushes()) != 6 || len(f.events("message.expired")) != 1 {
		t.Fatal("activity after expiry")
	}
}

func TestPageResendsAreFreshEnvelopesWithSameMessageID(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	m := page(f, "alice")
	f.run()
	f.clk.Advance(120 * time.Second)
	f.run()
	ps := f.relay.Pushes()
	if len(ps) != 2 || string(ps[0].Payload) == string(ps[1].Payload) {
		t.Fatalf("resend should use a fresh nonce: %d", len(ps))
	}
	for _, p := range ps {
		w, pt := f.openPush(p, "p1")
		if w.Raw.M != m.ID || !pt.Page || pt.Title != "Disk full" || p.CollapseID != m.ID {
			t.Fatalf("%+v %+v collapse=%q", w, pt, p.CollapseID)
		}
	}
}

func TestPageCustomIntervalAndZeroRetries(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	zero := 0
	m := page(f, "alice", func(in *EnqueueInput) { in.PageIntervalS = 30; in.PageMaxRetries = &zero })
	f.run()
	if got := f.msg(m.ID); !got.NextAttemptAt.Equal(t0.Add(30 * time.Second)) {
		t.Fatalf("%+v", got)
	}
	f.clk.Advance(30 * time.Second)
	f.run()
	if got := f.msg(m.ID); got.Status != store.MsgExpired || len(f.relay.Pushes()) != 1 {
		t.Fatalf("%s pushes=%d", got.Status, len(f.relay.Pushes()))
	}
}

// Review focus 5: a channel deleted while a page is in flight lets it expire cleanly.
func TestChannelDeletedWhilePagingExpires(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	m := page(f, "alice")
	f.run()
	f.clk.Advance(120 * time.Second)
	f.run() // one resend while the channel exists
	if len(f.relay.Pushes()) != 2 {
		t.Fatal("setup")
	}
	if ok, err := f.st.DeleteChannel("ops", f.clk.Now()); err != nil || !ok {
		t.Fatal(err)
	}
	f.clk.Advance(120 * time.Second)
	f.run()
	if got := f.msg(m.ID); got.Status != store.MsgExpired {
		t.Fatalf("status %s", got.Status)
	}
	if len(f.relay.Pushes()) != 2 {
		t.Fatalf("resent after channel deletion: %d pushes", len(f.relay.Pushes()))
	}
	if len(f.events("message.expired")) != 1 {
		t.Fatal("message.expired not emitted exactly once")
	}
}

func TestChannelDeletedBeforeFirstSendStillDelivers(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	m := f.enqueue("alice", "active").Message
	f.st.DeleteChannel("ops", f.clk.Now())
	f.run()
	if got := f.msg(m.ID); got.Status != store.MsgDelivered {
		t.Fatalf("queued message to a since-deleted channel: %s", got.Status)
	}
}

func TestAckedPageIsNotResent(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	m := page(f, "alice")
	f.run()
	if ok, err := f.st.AckMessage(m.ID, f.clk.Now()); err != nil || !ok {
		t.Fatal(err)
	}
	f.clk.Advance(10 * time.Minute)
	f.run()
	if len(f.relay.Pushes()) != 1 || len(f.events("message.expired")) != 0 {
		t.Fatalf("acked page was resent or expired: pushes=%d", len(f.relay.Pushes()))
	}
}

func TestPageResendReachesNewDevicesAndSkipsRevoked(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	f.pairing("p2", "alice", "ipad")
	m := page(f, "alice")
	f.run()
	// A device joins p1 after the first send; p2 gets revoked.
	if err := f.st.AddDevice(store.Device{ID: "watch", PairingID: "p1", SignPub: []byte("sign-watch"), Platform: "watchos", Name: "watch",
		SealedAlert: "alert-watch", SealedBackground: "bg-watch", CreatedAt: f.clk.Now()}); err != nil {
		t.Fatal(err)
	}
	f.st.RevokePairing("p2")
	f.clk.Advance(120 * time.Second)
	f.run()
	if len(f.pushesTo("alert-watch")) != 1 {
		t.Fatal("new device missed the resend")
	}
	if len(f.pushesTo("alert-ipad")) != 1 { // only the initial send
		t.Fatalf("revoked pairing received a resend: %d", len(f.pushesTo("alert-ipad")))
	}
	if len(f.pushesTo("alert-phone")) != 2 {
		t.Fatal("phone resend")
	}
	dels, _ := f.st.Deliveries(m.ID)
	if len(dels) != 3 {
		t.Fatalf("deliveries %+v", dels)
	}
}

func TestPageResendUnregisteredClearsToken(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone", "watch")
	page(f, "alice")
	f.run()
	f.relay.setErr("alert-phone", relay.ErrUnregistered)
	f.clk.Advance(120 * time.Second)
	f.run()
	if d, _ := f.st.GetDevice("phone"); d.SealedAlert != "" {
		t.Fatal("token not cleared on resend")
	}
	f.relay.setErr("alert-phone", nil)
	f.clk.Advance(120 * time.Second)
	f.run()
	if len(f.pushesTo("alert-phone")) != 1 {
		t.Fatalf("cleared device was pushed again: %d", len(f.pushesTo("alert-phone")))
	}
	if len(f.pushesTo("alert-watch")) != 3 {
		t.Fatalf("watch pushes %d", len(f.pushesTo("alert-watch")))
	}
}

func TestPageThatNeverDeliveredIsNotPaged(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	f.relay.setErr("alert-phone", relay.ErrUnregistered)
	m := page(f, "alice")
	f.run()
	if got := f.msg(m.ID); got.Status != store.MsgUndeliverable {
		t.Fatalf("status %s", got.Status)
	}
	f.clk.Advance(time.Hour)
	f.run()
	if len(f.events("message.expired")) != 0 {
		t.Fatal("undeliverable page expired")
	}
}

func TestCancelPagePush(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone", "watch")
	f.pairing("p2", "alice", "ipad")
	f.pairing("p3", "bob", "bobphone")
	m := page(f, "alice")
	f.run()
	before := len(f.relay.Pushes())
	f.relay.setErr("alert-watch", relay.ErrUnregistered)
	n, err := f.w.CancelPage(context.Background(), f.msg(m.ID))
	if err != nil || n != 2 {
		t.Fatalf("sent %d err %v", n, err)
	}
	ps := f.relay.Pushes()[before:]
	if len(ps) != 2 {
		t.Fatalf("pushes %d", len(ps))
	}
	for _, p := range ps {
		// Review focus 3: an alert push (not background), so a force-quit app still gets it.
		if p.Kind != "alert" || p.Priority != 5 || p.CollapseID != m.ID {
			t.Fatalf("%+v", p)
		}
		if p.Sealed != "alert-phone" && p.Sealed != "alert-ipad" {
			t.Fatalf("sealed %s", p.Sealed)
		}
		var wire map[string]map[string]any
		if err := json.Unmarshal(p.Payload, &wire); err != nil {
			t.Fatal(err)
		}
		aps := wire["aps"]
		if _, has := aps["sound"]; has {
			t.Fatalf("cancel push carries a sound: %s", p.Payload)
		}
		if aps["interruption-level"] != "passive" || aps["mutable-content"] != float64(1) {
			t.Fatalf("aps %v", aps)
		}
		if _, has := aps["category"]; has {
			t.Fatalf("cancel banner must not carry action buttons: %s", p.Payload)
		}
		if strings.Contains(string(p.Payload), "Disk full") {
			t.Fatalf("page title leaked in the clear: %s", p.Payload)
		}
		pairing := "p1"
		if p.Sealed == "alert-ipad" {
			pairing = "p2"
		}
		w, pt := f.openPush(p, pairing)
		if w.Raw.M != m.ID || w.Raw.PG != 0 {
			t.Fatalf("envelope %+v", w.Raw)
		}
		if pt.Cancel != m.ID || pt.Title != "Acknowledged" || pt.Body != "Disk full was acknowledged" ||
			pt.Channel != "ops" || pt.Level != "passive" {
			t.Fatalf("plaintext %+v", pt)
		}
	}
	if d, _ := f.st.GetDevice("watch"); d.SealedAlert != "" {
		t.Fatal("unregistered alert token not cleared")
	}
	for _, p := range f.relay.Pushes() {
		if p.Kind == "background" {
			t.Fatal("silent background push cannot wake a force-quit app")
		}
	}
}

func TestCancelPageSkipsDevicesWithoutAlertToken(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	m := page(f, "alice")
	f.run()
	f.st.ClearDeviceToken("phone", store.TokenAlert)
	n, err := f.w.CancelPage(context.Background(), f.msg(m.ID))
	if err != nil || n != 0 {
		t.Fatalf("sent %d err %v", n, err)
	}
}

// A title that nearly fills the alert payload would make the cancel push (which repeats it in its body, with more
// text around it) too big to send, and the alarm on the device would keep ringing after the acknowledgement.
func TestCancelPageWithHugeTitleIsStillSent(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	title := strings.Repeat("a", 2700) // fits as an alert; "<title> was acknowledged" plus the cancel envelope does not
	m := f.enqueue("alice", "passive", func(in *EnqueueInput) { in.Plaintext.Title, in.Plaintext.Body = title, "x" }).Message
	n, err := f.w.CancelPage(context.Background(), f.msg(m.ID))
	if err != nil || n != 1 {
		t.Fatalf("sent %d err %v", n, err)
	}
	p := f.relay.Pushes()[len(f.relay.Pushes())-1]
	if len(p.Payload) > MaxPayloadBytes {
		t.Fatalf("payload %d bytes", len(p.Payload))
	}
	_, pt := f.openPush(p, "p1")
	if pt.Cancel != m.ID || !strings.HasSuffix(pt.Body, "… was acknowledged") || len(pt.Body) > 200 {
		t.Fatalf("cancel plaintext %q", pt.Body)
	}
}

func TestCancelTitleIsTruncatedOnARuneBoundary(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	m := page(f, "alice", func(in *EnqueueInput) { in.Plaintext.Title = strings.Repeat("é", 300) })
	if n, err := f.w.CancelPage(context.Background(), f.msg(m.ID)); err != nil || n != 1 {
		t.Fatalf("sent %d err %v", n, err)
	}
	_, pt := f.openPush(f.relay.Pushes()[len(f.relay.Pushes())-1], "p1")
	if want := strings.Repeat("é", maxCancelTitleRunes) + "… was acknowledged"; pt.Body != want || !utf8.ValidString(pt.Body) {
		t.Fatalf("body %q", pt.Body)
	}
}

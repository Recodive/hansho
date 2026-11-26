package api

import (
	"testing"

	"github.com/recodive/hansho/hub/internal/store"
)

// sendToTwo pages alice and bob in one send (ack is "" for the default) and returns their message ids and devices.
func sendToTwo(t *testing.T, ack string) (e *env, alice, bob *testDevice, aliceMsg, bobMsg string) {
	t.Helper()
	e = newEnv(t)
	e.channel("oncall", "page", "page")
	alice, bob = e.newDevice("alice-phone"), e.newDevice("bob-phone")
	alice.pair("alice")
	bob.pair("bob")
	m := msg("alice", "oncall")
	delete(m, "external_user_id")
	m["external_user_ids"] = []string{"alice", "bob"}
	if ack != "" {
		m["page"] = map[string]any{"ack": ack}
	}
	w := e.api("POST", "/api/v1/messages", m)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, r := range decode[struct {
		Recipients []struct {
			ExternalUserID string `json:"external_user_id"`
			MessageID      string `json:"message_id"`
		} `json:"recipients"`
	}](t, w).Recipients {
		if r.ExternalUserID == "alice" {
			aliceMsg = r.MessageID
		} else {
			bobMsg = r.MessageID
		}
	}
	e.runWorker()
	return
}

func ack(t *testing.T, d *testDevice, id string) {
	t.Helper()
	if w := d.request("POST", "/device/v1/actions", map[string]any{"message_id": id, "action_id": "ack"}); w.Code != 200 {
		t.Fatalf("ack: %d %s", w.Code, w.Body)
	}
}

func TestPageAckAnyStopsThePageForEveryRecipient(t *testing.T) {
	e, alice, _, am, bm := sendToTwo(t, "") // "any" is the default
	ack(t, alice, am)
	b, _ := e.st.GetMessage(bm)
	if b.Status != store.MsgAcked || b.AckedBy != "alice" || b.AckedAt.IsZero() {
		t.Fatalf("bob's page: %+v", b)
	}
	if ev := e.events("message.acked"); len(ev) != 2 {
		t.Fatalf("want an acked event per recipient, got %v", ev)
	} else if ev[1]["message_id"] != bm || ev[1]["acked_by"] != "alice" {
		t.Fatalf("sibling event %v", ev[1])
	}
	before := len(e.relay.pushes())
	e.clk.Advance(3600e9)
	e.runWorker()
	if len(e.relay.pushes()) != before {
		t.Fatal("an acknowledged page was resent")
	}
}

func TestPageAckEachKeepsPagingTheOthers(t *testing.T) {
	e, alice, bob, am, bm := sendToTwo(t, "each")
	ack(t, alice, am)
	if b, _ := e.st.GetMessage(bm); b.Status != store.MsgPaging {
		t.Fatalf("bob's page should keep paging: %+v", b)
	}
	ack(t, bob, bm)
	if b, _ := e.st.GetMessage(bm); b.Status != store.MsgAcked || b.AckedBy != "bob" {
		t.Fatalf("%+v", b)
	}
}

func TestPageAckIsValidated(t *testing.T) {
	e := newEnv(t)
	e.channel("oncall", "page", "page")
	m := msg("alice", "oncall")
	m["page"] = map[string]any{"ack": "everyone"}
	if code, _, _ := e.send(m); code != 400 {
		t.Fatalf("bad ack mode: %d", code)
	}
}

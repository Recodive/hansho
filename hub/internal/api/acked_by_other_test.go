package api

import (
	"strings"
	"testing"
)

type acksByOtherResp struct {
	Acked        map[string]int64             `json:"acked"`
	AckedByOther map[string]map[string]string `json:"acked_by_other"`
}

func TestAcksSaysWhoAcknowledgedWhenItWasSomeoneElse(t *testing.T) {
	e, alice, bob, am, bm := sendToTwo(t, "")
	e.api("PUT", "/api/v1/users/alice", map[string]any{"display_name": "Alice"})
	ack(t, alice, am) // bob's sibling page is acknowledged by alice

	w := bob.request("GET", "/device/v1/acks?ids="+bm, nil)
	got := decode[acksByOtherResp](t, w)
	if got.Acked[bm] == 0 || got.AckedByOther[bm]["name"] != "Alice" {
		t.Fatalf("bob sees %s", w.Body)
	}
	// Alice acknowledged herself: no acked_by_other.
	w = alice.request("GET", "/device/v1/acks?ids="+am, nil)
	if got = decode[acksByOtherResp](t, w); got.Acked[am] == 0 || len(got.AckedByOther) != 0 || strings.Contains(w.Body.String(), "acked_by_other") {
		t.Fatalf("alice sees %s", w.Body)
	}
}

func TestAcksOtherWithoutNameIsAnEmptyObjectAndNeverLeaksTheUserID(t *testing.T) {
	e, alice, bob, am, bm := sendToTwo(t, "")
	ack(t, alice, am)
	w := bob.request("GET", "/device/v1/acks?ids="+bm+","+am, nil)
	if !strings.Contains(w.Body.String(), `"acked_by_other":{"`+bm+`":{}}`) {
		t.Fatalf("want an empty object for a nameless acknowledger: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), "alice") || strings.Contains(w.Body.String(), am) {
		t.Fatalf("leaks the acknowledger's id or message: %s", w.Body)
	}
	_ = e
}

func TestAcksEachMeansEveryoneAcknowledgedThemselves(t *testing.T) {
	e, alice, bob, am, bm := sendToTwo(t, "each")
	e.api("PUT", "/api/v1/users/alice", map[string]any{"display_name": "Alice"})
	ack(t, alice, am)
	ack(t, bob, bm)
	for d, id := range map[*testDevice]string{alice: am, bob: bm} {
		if w := d.request("GET", "/device/v1/acks?ids="+id, nil); strings.Contains(w.Body.String(), "acked_by_other") {
			t.Fatalf("ack-each carries acked_by_other: %s", w.Body)
		}
	}
}

func TestAcksByOtherOmitsChannelsTheUserCannotAccess(t *testing.T) {
	e, alice, bob, am, bm := sendToTwo(t, "")
	ack(t, alice, am)
	e.audienceChannel("oncall", map[string]any{"mode": "only", "users": []string{"alice"}}) // bob loses access
	w := bob.request("GET", "/device/v1/acks?ids="+bm, nil)
	if strings.Contains(w.Body.String(), bm) {
		t.Fatalf("inaccessible channel leaks: %s", w.Body)
	}
}

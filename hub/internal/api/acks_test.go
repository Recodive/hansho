package api

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

type acksResp struct {
	Acked map[string]int64 `json:"acked"`
}

func TestAcksReturnsOnlyAcknowledgedMessagesOfTheCallersUser(t *testing.T) {
	p := newPage(t, nil)
	e := p.e

	// A second, still paging message of the same user.
	code, second, raw := e.send(msg("alice", "oncall"))
	if code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	e.runWorker()

	e.clk.Advance(30 * time.Second)
	if c := p.action(p.watch, "ack", nil); c != 200 {
		t.Fatalf("ack: %d", c)
	}
	ackedAt := e.clk.Now().Unix()

	// Another user's acknowledged page.
	bob := e.newDevice("bobphone")
	bob.pair("bob")
	code, bobMsg, raw := e.send(msg("bob", "oncall"))
	if code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	e.runWorker()
	if c := bob.request("POST", "/device/v1/actions", map[string]any{"message_id": bobMsg.MessageID, "action_id": "ack"}).Code; c != 200 {
		t.Fatalf("bob ack: %d", c)
	}

	ids := strings.Join([]string{p.id, second.MessageID, bobMsg.MessageID, "msg_doesnotexist"}, ",")
	w := p.phone.request("GET", "/device/v1/acks?ids="+ids, nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	got := decode[acksResp](t, w)
	if len(got.Acked) != 1 || got.Acked[p.id] != ackedAt {
		t.Fatalf("acked %v, want only %s at %d (unacked, other user's and unknown ids omitted)", got.Acked, p.id, ackedAt)
	}
	if strings.Contains(w.Body.String(), bobMsg.MessageID) || strings.Contains(w.Body.String(), "doesnotexist") {
		t.Fatalf("leaks ids it must omit: %s", w.Body)
	}

	// Bob sees his own and nothing of alice's.
	got = decode[acksResp](t, bob.request("GET", "/device/v1/acks?ids="+ids, nil))
	if len(got.Acked) != 1 || got.Acked[bobMsg.MessageID] == 0 {
		t.Fatalf("bob acked %v", got.Acked)
	}
}

func TestAcksIsEmptyObjectWhenNothingMatches(t *testing.T) {
	p := newPage(t, nil)
	w := p.phone.request("GET", "/device/v1/acks?ids="+p.id+",,nope", nil)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"acked":{}}` {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// No ids at all is an empty answer, not an error.
	w = p.phone.request("GET", "/device/v1/acks", nil)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"acked":{}}` {
		t.Fatalf("no ids: %d %s", w.Code, w.Body)
	}
}

func TestAcksCapsAtOneHundredIDs(t *testing.T) {
	p := newPage(t, nil)
	ids := func(n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = "msg_" + strconv.Itoa(i)
		}
		return strings.Join(out, ",")
	}
	if w := p.phone.request("GET", "/device/v1/acks?ids="+ids(100), nil); w.Code != 200 {
		t.Fatalf("100 ids: %d %s", w.Code, w.Body)
	}
	if w := p.phone.request("GET", "/device/v1/acks?ids="+ids(101), nil); w.Code != 400 {
		t.Fatalf("101 ids: %d %s, want 400", w.Code, w.Body)
	}
}

func TestAcksRequiresADeviceSignature(t *testing.T) {
	p := newPage(t, nil)
	e := p.e
	p.action(p.phone, "ack", nil)
	path := "/device/v1/acks?ids=" + p.id

	if w := e.do("GET", path, nil, nil); w.Code != 401 || !strings.Contains(w.Body.String(), `"unauthorized"`) {
		t.Fatalf("unsigned: %d %s", w.Code, w.Body)
	}
	imposter := e.newDevice("imposter")
	imposter.ID = p.phone.ID
	if w := imposter.request("GET", path, nil); w.Code != 401 || !strings.Contains(w.Body.String(), `"unauthorized"`) {
		t.Fatalf("wrong key: %d %s", w.Code, w.Body)
	}
	// A stale timestamp is refused too.
	hdr := p.phone.signedHeaders("GET", "/device/v1/acks", nil, e.clk.Now().Add(-time.Hour))
	if w := e.do("GET", path, nil, hdr); w.Code != 401 {
		t.Fatalf("stale: %d", w.Code)
	}
}

func TestAcksRespectsRevokedDeviceAndPairing(t *testing.T) {
	p := newPage(t, nil)
	e := p.e
	p.action(p.phone, "ack", nil)
	path := "/device/v1/acks?ids=" + p.id
	errCode := func(body string) string {
		switch {
		case strings.Contains(body, "device_revoked"):
			return "device_revoked"
		case strings.Contains(body, "pairing_revoked"):
			return "pairing_revoked"
		}
		return body
	}

	if err := e.st.RevokeDevice(p.watch.ID); err != nil {
		t.Fatal(err)
	}
	if w := p.watch.request("GET", path, nil); w.Code != 401 || errCode(w.Body.String()) != "device_revoked" {
		t.Fatalf("retired device: %d %s", w.Code, w.Body)
	}
	if w := p.phone.request("GET", path, nil); w.Code != 200 {
		t.Fatalf("phone: %d %s", w.Code, w.Body)
	}
	if _, err := e.st.RevokePairing(p.phone.PairingID); err != nil {
		t.Fatal(err)
	}
	if w := p.phone.request("GET", path, nil); w.Code != 401 || errCode(w.Body.String()) != "pairing_revoked" {
		t.Fatalf("revoked pairing: %d %s", w.Code, w.Body)
	}
}

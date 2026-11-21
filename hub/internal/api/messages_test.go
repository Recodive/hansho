package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"github.com/recodive/hansho/hub/internal/crypto"
	"github.com/recodive/hansho/hub/internal/relay"
)

func (e *env) channel(id, def, max string) {
	e.t.Helper()
	w := e.api("PUT", "/api/v1/channels/"+id, map[string]any{"name": id, "default_level": def, "max_level": max})
	if w.Code != 200 {
		e.t.Fatalf("channel: %d %s", w.Code, w.Body)
	}
}

func (e *env) runWorker() {
	e.t.Helper()
	if err := e.worker.RunOnce(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

type msgResp struct {
	MessageID  string `json:"message_id"`
	Level      string `json:"level"`
	Devices    int    `json:"devices"`
	Status     string `json:"status"`
	Deliveries []struct {
		DeviceID string `json:"device_id"`
		Status   string `json:"status"`
		Error    string `json:"error"`
	} `json:"deliveries"`
}

func (e *env) send(body map[string]any) (int, msgResp, string) {
	e.t.Helper()
	w := e.api("POST", "/api/v1/messages", body)
	var r msgResp
	_ = json.Unmarshal(w.Body.Bytes(), &r)
	return w.Code, r, w.Body.String()
}

func msg(user, channel string) map[string]any {
	return map[string]any{"external_user_id": user, "channel": channel, "title": "Disk full", "body": "db-1 at 97%"}
}

func TestSendAndDeliverEndToEnd(t *testing.T) {
	e := newEnv(t)
	e.channel("ops", "active", "critical")
	phone := e.newDevice("phone")
	phone.pair("alice")

	m := msg("alice", "ops")
	m["subtitle"], m["image_url"] = "prod", "https://x/y.png"
	m["data"] = map[string]any{"host": "db-1"}
	m["collapse_id"] = "disk-db-1"
	code, r, raw := e.send(m)
	if code != 202 || r.Level != "active" || r.Devices != 1 || !strings.HasPrefix(r.MessageID, "msg_") {
		t.Fatalf("%d %s", code, raw)
	}
	if len(e.relay.pushes()) != 0 {
		t.Fatal("pushed synchronously; sends go through the outbox/worker")
	}
	e.runWorker()
	pushes := e.relay.pushes()
	if len(pushes) != 1 || pushes[0].Sealed != "alert-phone" || pushes[0].Kind != "alert" || pushes[0].CollapseID != "disk-db-1" {
		t.Fatalf("%+v", pushes)
	}
	var wire struct {
		APS struct {
			Alert struct{ Title, Body string }
			Level string `json:"interruption-level"`
		} `json:"aps"`
		C crypto.Envelope `json:"c"`
	}
	if err := json.Unmarshal(pushes[0].Payload, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.APS.Alert.Title != "Example Hub" || wire.APS.Alert.Body != "New message" || wire.APS.Level != "active" {
		t.Fatalf("%s", pushes[0].Payload)
	}
	pt, err := crypto.OpenMessage(e.hubKey.Public().(ed25519.PublicKey), phone.PairingKey, wire.C)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal(pt, &got)
	if got["title"] != "Disk full" || got["body"] != "db-1 at 97%" || got["subtitle"] != "prod" || got["channel"] != "ops" ||
		got["level"] != "active" || got["image_url"] != "https://x/y.png" || got["data"].(map[string]any)["host"] != "db-1" {
		t.Fatalf("plaintext %s", pt)
	}
	if strings.Contains(string(pushes[0].Payload), "Disk full") {
		t.Fatal("message content visible in the APNs payload")
	}

	w := e.api("GET", "/api/v1/messages/"+r.MessageID, nil)
	st := decode[msgResp](t, w)
	if w.Code != 200 || st.Status != "delivered" || st.Level != "active" || len(st.Deliveries) != 1 ||
		st.Deliveries[0].DeviceID != phone.ID || st.Deliveries[0].Status != "sent" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if ev := e.events("message.delivered"); len(ev) != 1 {
		t.Fatalf("delivered events: %d", len(ev))
	}
}

func TestLevelClampedToChannelMax(t *testing.T) {
	e := newEnv(t)
	e.channel("chat", "passive", "active")
	m := msg("alice", "chat")
	m["level"] = "page"
	code, r, raw := e.send(m)
	if code != 202 || r.Level != "active" {
		t.Fatalf("%d %s", code, raw)
	}
	m["level"] = "critical"
	if _, r, _ = e.send(m); r.Level != "active" {
		t.Fatalf("critical -> %s", r.Level)
	}
	delete(m, "level")
	if _, r, _ = e.send(m); r.Level != "passive" {
		t.Fatalf("default -> %s", r.Level)
	}
	m["level"] = "nonsense"
	if code, _, _ := e.send(m); code != 400 {
		t.Fatalf("bad level: %d", code)
	}
}

func TestSendValidation(t *testing.T) {
	e := newEnv(t)
	e.channel("ops", "active", "active")
	if code, _, raw := e.send(msg("alice", "nope")); code != 404 || !strings.Contains(raw, `"unknown_channel"`) {
		t.Fatalf("unknown channel: %d %s", code, raw)
	}
	e.api("DELETE", "/api/v1/channels/ops", nil)
	if code, _, raw := e.send(msg("alice", "ops")); code != 404 || !strings.Contains(raw, `"unknown_channel"`) {
		t.Fatalf("deleted channel: %d %s", code, raw)
	}
	e.channel("ops", "active", "active")
	for _, field := range []string{"external_user_id", "channel", "title", "body"} {
		m := msg("alice", "ops")
		delete(m, field)
		code, _, raw := e.send(m)
		if code != 400 || !strings.Contains(raw, `"`+field+`"`) {
			t.Errorf("missing %s: %d %s", field, code, raw)
		}
	}
	m := msg("alice", "ops")
	m["template_id"] = "nope"
	if code, _, raw := e.send(m); code != 400 || !strings.Contains(raw, "template_id") {
		t.Errorf("unknown template: %d %s", code, raw)
	}
	m = msg("alice", "ops")
	m["sound"] = "nope"
	if code, _, raw := e.send(m); code != 400 || !strings.Contains(raw, "sound") {
		t.Errorf("unknown sound: %d %s", code, raw)
	}
	if w := e.do("POST", "/api/v1/messages", msg("alice", "ops"), nil); w.Code != 401 {
		t.Errorf("unauthenticated: %d", w.Code)
	}
	if w := e.api("GET", "/api/v1/messages/msg_nope", nil); w.Code != 404 {
		t.Errorf("unknown message: %d", w.Code)
	}
}

func TestPayloadTooLarge(t *testing.T) {
	e := newEnv(t)
	e.channel("ops", "active", "active")
	m := msg("alice", "ops")
	m["body"] = strings.Repeat("x", 5000)
	code, _, raw := e.send(m)
	if code != 413 || !strings.Contains(raw, `"payload_too_large"`) {
		t.Fatalf("%d %s", code, raw)
	}
	// Even for a user with a paired device.
	d := e.newDevice("phone")
	d.pair("alice")
	m["external_user_id"] = "alice"
	if code, _, _ := e.send(m); code != 413 {
		t.Fatalf("paired user: %d", code)
	}
	if rows, _ := e.st.DueMessages("queued", e.clk.Now(), 10); len(rows) != 0 {
		t.Fatal("oversized message was stored")
	}
}

// Review focus 2.
func TestNoActiveDevicesIsAcceptedNotAnError(t *testing.T) {
	e := newEnv(t)
	e.channel("ops", "active", "active")
	code, r, raw := e.send(msg("ghost", "ops"))
	if code != 202 || r.Devices != 0 || r.Status != "undeliverable" || r.Deliveries == nil || len(r.Deliveries) != 0 {
		t.Fatalf("%d %s", code, raw)
	}
	if !strings.Contains(raw, `"deliveries":[]`) {
		t.Fatalf("deliveries must be an empty array: %s", raw)
	}
	ev := e.events("message.undeliverable")
	if len(ev) != 1 || ev[0]["message_id"] != r.MessageID {
		t.Fatalf("events %v", ev)
	}
	st := decode[msgResp](t, e.api("GET", "/api/v1/messages/"+r.MessageID, nil))
	if st.Status != "undeliverable" || len(st.Deliveries) != 0 {
		t.Fatalf("%+v", st)
	}
	e.runWorker()
	if len(e.relay.pushes()) != 0 {
		t.Fatal("push without devices")
	}
}

// Review focus 3, through the HTTP layer.
func TestRelayVerdictsThroughAPI(t *testing.T) {
	e := newEnv(t)
	e.channel("ops", "active", "active")
	a, b := e.newDevice("a"), e.newDevice("b")
	a.pair("alice")
	b.pair("alice")
	e.relay.PushErr["alert-a"] = relay.ErrUnregistered
	e.relay.PushErr["alert-b"] = relay.ErrPairingRevoked
	_, r, _ := e.send(msg("alice", "ops"))
	e.runWorker()

	st := decode[msgResp](t, e.api("GET", "/api/v1/messages/"+r.MessageID, nil))
	if st.Status != "undeliverable" {
		t.Fatalf("%+v", st)
	}
	got := map[string]string{}
	for _, d := range st.Deliveries {
		got[d.DeviceID] = d.Status + ":" + d.Error
	}
	if got[a.ID] != "failed:unregistered" || got[b.ID] != "failed:pairing_revoked" {
		t.Fatalf("%v", got)
	}
	if da, _ := e.st.GetDevice(a.ID); da.SealedAlert != "" {
		t.Fatal("token not cleared")
	}
	if ev := e.events("pairing.revoked"); len(ev) != 1 || ev[0]["by"] != "relay" {
		t.Fatalf("%v", ev)
	}
	// Both are gone for subsequent sends.
	code, r2, _ := e.send(msg("alice", "ops"))
	if code != 202 || r2.Devices != 0 || r2.Status != "undeliverable" {
		t.Fatalf("%d %+v", code, r2)
	}
}

func TestSoundResolvedToFilename(t *testing.T) {
	e := newEnv(t)
	e.putSound("ding", "audio/wav", wavBytes(1))
	e.channel("ops", "active", "active")
	d := e.newDevice("phone")
	d.pair("alice")
	m := msg("alice", "ops")
	m["sound"] = "ding"
	if code, _, raw := e.send(m); code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	e.runWorker()
	p := e.relay.pushes()[0]
	if !strings.Contains(string(p.Payload), `"sound":"ding.wav"`) {
		t.Fatalf("%s", p.Payload)
	}
}

func TestMessageActionsAndTemplateInPlaintext(t *testing.T) {
	e := newEnv(t)
	e.api("PUT", "/api/v1/templates/t1", map[string]any{"version": 1, "body": json.RawMessage(goodTemplate)})
	w := e.api("PUT", "/api/v1/channels/ops", map[string]any{
		"name": "ops", "default_level": "active", "max_level": "active", "template_id": "t1",
		"actions": []any{map[string]any{"id": "ack", "title": "Ack", "ack": true}},
	})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	d := e.newDevice("phone")
	d.pair("alice")
	e.send(msg("alice", "ops"))
	e.runWorker()
	var wire struct {
		C crypto.Envelope `json:"c"`
	}
	json.Unmarshal(e.relay.pushes()[0].Payload, &wire)
	pt, _ := crypto.OpenMessage(e.hubKey.Public().(ed25519.PublicKey), d.PairingKey, wire.C)
	var got struct {
		TemplateID string `json:"template_id"`
		Actions    []struct {
			ID  string
			Ack bool
		}
	}
	json.Unmarshal(pt, &got)
	if got.TemplateID != "t1" || len(got.Actions) != 1 || got.Actions[0].ID != "ack" || !got.Actions[0].Ack {
		t.Fatalf("channel defaults not applied: %s", pt)
	}
}

func TestPageOptionsValidatedAndStored(t *testing.T) {
	e := newEnv(t)
	e.channel("oncall", "active", "page")
	m := msg("alice", "oncall")
	m["level"] = "page"
	m["page"] = map[string]any{"interval_s": 30, "max_retries": 2}
	code, r, raw := e.send(m)
	if code != 202 || r.Level != "page" {
		t.Fatalf("%d %s", code, raw)
	}
	stored, _ := e.st.GetMessage(r.MessageID)
	if stored.PageIntervalS != 30 || stored.PageMaxRetries != 2 {
		t.Fatalf("%+v", stored)
	}
	m["page"] = map[string]any{"interval_s": -5}
	if code, _, _ := e.send(m); code != 400 {
		t.Fatalf("negative interval: %d", code)
	}
	// Defaults.
	delete(m, "page")
	_, r, _ = e.send(m)
	stored, _ = e.st.GetMessage(r.MessageID)
	if stored.PageIntervalS != 120 || stored.PageMaxRetries != 5 {
		t.Fatalf("defaults: %+v", stored)
	}
}

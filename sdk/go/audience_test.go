package hansho

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestPutGroupSendsTheWholeList(t *testing.T) {
	h := newHub(t, func(_ *recorded, w *respond) { w.Body = `{"id":"oncall","members":["alice","bob"]}` })
	g, err := h.client.PutGroup(ctx(), "oncall", []string{"bob", "alice"})
	if err != nil {
		t.Fatal(err)
	}
	r := h.requests()[0]
	if r.Method != "PUT" || r.EscapedPath != "/api/v1/groups/oncall" || string(r.Body) != `{"members":["bob","alice"]}` {
		t.Fatalf("%s %s %s", r.Method, r.EscapedPath, r.Body)
	}
	if !reflect.DeepEqual(g, Group{ID: "oncall", Members: []string{"alice", "bob"}}) {
		t.Fatalf("%+v", g)
	}
	// An empty group still sends the list: the hub requires it.
	if _, err := h.client.PutGroup(ctx(), "oncall", nil); err != nil {
		t.Fatal(err)
	}
	if got := string(h.requests()[1].Body); got != `{"members":[]}` {
		t.Fatal(got)
	}
}

func TestGetAndDeleteGroup(t *testing.T) {
	h := newHub(t, func(r *recorded, w *respond) {
		if r.Method == "GET" {
			w.Body = `{"id":"a b","members":[]}`
		} else {
			w.Status, w.Body = 204, ""
		}
	})
	g, err := h.client.GetGroup(ctx(), "a b")
	if err != nil || g.ID != "a b" || g.Members == nil || len(g.Members) != 0 {
		t.Fatalf("%+v %v", g, err)
	}
	if err := h.client.DeleteGroup(ctx(), "a b"); err != nil {
		t.Fatal(err)
	}
	rs := h.requests()
	if rs[0].EscapedPath != "/api/v1/groups/a%20b" || rs[1].Method != "DELETE" || rs[1].EscapedPath != "/api/v1/groups/a%20b" {
		t.Fatalf("%+v", rs)
	}
	for name, err := range map[string]error{
		"PutGroup":    func() error { _, e := h.client.PutGroup(ctx(), "", nil); return e }(),
		"GetGroup":    func() error { _, e := h.client.GetGroup(ctx(), ""); return e }(),
		"DeleteGroup": h.client.DeleteGroup(ctx(), ""),
		"GetFeed":     func() error { _, e := h.client.GetFeed(ctx(), "", ""); return e }(),
	} {
		if !errors.Is(err, ErrEmptyID) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(h.requests()) != 2 {
		t.Fatal("an empty id was sent")
	}
}

func TestUnknownGroupIsAnAPIError(t *testing.T) {
	h := newHub(t, func(_ *recorded, w *respond) { w.Status, w.Body = 404, `{"error":"unknown_group"}` })
	_, err := h.client.GetGroup(ctx(), "gone")
	if ae, ok := IsAPIError(err); !ok || ae.Code != CodeUnknownGroup || ae.Status != 404 {
		t.Fatalf("%v", err)
	}
}

func TestChannelAndFeedAudienceOnTheWire(t *testing.T) {
	h := newHub(t, func(r *recorded, w *respond) {
		if r.Method == "GET" {
			w.Body = `{"name":"Ops","default_level":"active","max_level":"page","audience":{"mode":"except","users":["bob"],"groups":[]}}`
		}
	})
	aud := &Audience{Mode: AudienceOnly, Users: []string{"alice"}, Groups: []string{"oncall"}}
	if err := h.client.PutChannel(ctx(), "ops", Channel{Name: "Ops", DefaultLevel: LevelActive, MaxLevel: LevelPage, Audience: aud}); err != nil {
		t.Fatal(err)
	}
	if got := string(h.requests()[0].Body); got != `{"name":"Ops","default_level":"active","max_level":"page","audience":{"mode":"only","users":["alice"],"groups":["oncall"]}}` {
		t.Fatal(got)
	}
	ch, err := h.client.GetChannel(ctx(), "ops")
	if err != nil || !reflect.DeepEqual(ch.Audience, &Audience{Mode: AudienceExcept, Users: []string{"bob"}, Groups: []string{}}) {
		t.Fatalf("%+v %v", ch, err)
	}
	if _, err := h.client.PutFeed(ctx(), "uptime", Feed{TemplateID: "status", Channel: "ops", Audience: aud, Data: 1}); err != nil {
		t.Fatal(err)
	}
	var feed map[string]any
	if err := json.Unmarshal(h.requests()[2].Body, &feed); err != nil || feed["channel"] != "ops" || feed["audience"] == nil {
		t.Fatalf("%s %v", h.requests()[2].Body, err)
	}
	// Without them nothing is sent, which the hub reads as "everyone" / "inherit".
	if _, err := h.client.PutFeed(ctx(), "uptime", Feed{TemplateID: "status", Data: 1}); err != nil {
		t.Fatal(err)
	}
	if got := string(h.requests()[3].Body); got != `{"template_id":"status","data":1}` {
		t.Fatal(got)
	}
}

func TestGetFeedSettings(t *testing.T) {
	h := newHub(t, func(_ *recorded, w *respond) {
		w.Body = `{"feed_id":"own","external_user_id":"a&b","template_id":"status","channel":"ops","updated_at":7,"audience":{"mode":"everyone","users":[],"groups":[]}}`
	})
	f, err := h.client.GetFeed(ctx(), "own", "a&b")
	if err != nil {
		t.Fatal(err)
	}
	r := h.requests()[0]
	if r.Path != "/api/v1/feeds/own" || r.RawQuery != "external_user_id=a%26b" {
		t.Fatalf("%s?%s", r.Path, r.RawQuery)
	}
	if f.FeedID != "own" || f.ExternalUserID != "a&b" || f.Channel != "ops" || f.Audience == nil || f.Audience.Mode != AudienceEveryone || f.UpdatedAt != 7 {
		t.Fatalf("%+v", f)
	}
	if _, err := h.client.GetFeed(ctx(), "own", ""); err != nil {
		t.Fatal(err)
	}
	if got := h.requests()[1].RawQuery; got != "" {
		t.Fatalf("query %q for the shared feed", got)
	}
}

func TestSendToSeveralUsersAndAnAudience(t *testing.T) {
	h := newHub(t, func(_ *recorded, w *respond) {
		w.Status = 202
		w.Body = `{"level":"active","recipients":[
			{"external_user_id":"alice","message_id":"msg_1","status":"queued","devices":1,"deliveries":[{"device_id":"dev_1","status":"queued"}]},
			{"external_user_id":"carol","status":"no_access"}]}`
	})
	res, err := h.client.SendMessage(ctx(), Message{ExternalUserIDs: []string{"alice", "carol"}, Channel: "ops", Title: "t", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(h.requests()[0].Body); got != `{"external_user_ids":["alice","carol"],"channel":"ops","title":"t","body":"b"}` {
		t.Fatal(got)
	}
	want := []Recipient{
		{ExternalUserID: "alice", MessageID: "msg_1", Status: "queued", Devices: 1, Deliveries: []Delivery{{DeviceID: "dev_1", Status: "queued"}}},
		{ExternalUserID: "carol", Status: RecipientNoAccess},
	}
	if res.MessageID != "" || res.Level != LevelActive || !reflect.DeepEqual(res.Recipients, want) {
		t.Fatalf("%+v", res)
	}
	if _, err := h.client.SendMessage(ctx(), Message{ToAudience: true, Channel: "ops", Title: "t", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if got := string(h.requests()[1].Body); got != `{"to_audience":true,"channel":"ops","title":"t","body":"b"}` {
		t.Fatal(got)
	}
}

func TestKickIsAnUnpair(t *testing.T) {
	h := newHub(t, func(_ *recorded, w *respond) { w.Body = `{"revoked":1}` })
	if err := h.client.Kick(ctx(), "user 42"); err != nil {
		t.Fatal(err)
	}
	r := h.requests()[0]
	if r.Method != "DELETE" || r.EscapedPath != "/api/v1/users/user%2042/pairings" {
		t.Fatalf("%s %s", r.Method, r.EscapedPath)
	}
	if err := h.client.Kick(ctx(), ""); !errors.Is(err, ErrEmptyID) {
		t.Fatal(err)
	}
}

func TestNoAccessIsAnAPIError(t *testing.T) {
	h := newHub(t, func(_ *recorded, w *respond) { w.Status, w.Body = 403, `{"error":"no_access"}` })
	_, err := h.client.StartActivity(ctx(), Activity{ExternalUserID: "u", Channel: "c", TemplateID: "t", Data: 1})
	if ae, ok := IsAPIError(err); !ok || ae.Code != CodeNoAccess || ae.Status != 403 {
		t.Fatalf("%v", err)
	}
}

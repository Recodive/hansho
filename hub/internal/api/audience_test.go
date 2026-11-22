package api

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	return decode[struct {
		Error string `json:"error"`
	}](t, w).Error
}

// audienceChannel publishes a channel with an audience.
func (e *env) audienceChannel(id string, aud map[string]any) {
	e.t.Helper()
	body := okChannel()
	if aud != nil {
		body["audience"] = aud
	}
	if w := e.api("PUT", "/api/v1/channels/"+id, body); w.Code != 200 {
		e.t.Fatalf("channel %s: %d %s", id, w.Code, w.Body)
	}
}

func (e *env) group(id string, members ...string) {
	e.t.Helper()
	if members == nil {
		members = []string{}
	}
	if w := e.api("PUT", "/api/v1/groups/"+id, map[string]any{"members": members}); w.Code != 200 {
		e.t.Fatalf("group %s: %d %s", id, w.Code, w.Body)
	}
}

func only(users []string, groups ...string) map[string]any {
	return map[string]any{"mode": "only", "users": users, "groups": groups}
}

func except(users []string, groups ...string) map[string]any {
	return map[string]any{"mode": "except", "users": users, "groups": groups}
}

func syncedChannels(t *testing.T, d *testDevice) string {
	t.Helper()
	w := d.request("GET", "/device/v1/sync", nil)
	if w.Code != 200 {
		t.Fatalf("sync: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "audience") {
		t.Fatalf("sync leaks an audience: %s", w.Body)
	}
	res := decode[struct {
		Channels []struct {
			ID string `json:"id"`
		} `json:"channels"`
	}](t, w)
	var ids []string
	for _, c := range res.Channels {
		ids = append(ids, c.ID)
	}
	return strings.Join(ids, ",")
}

func TestGroupCRUD(t *testing.T) {
	e := newEnv(t)
	if w := e.api("GET", "/api/v1/groups/oncall", nil); w.Code != 404 || errCode(t, w) != "unknown_group" {
		t.Fatalf("unknown: %d %s", w.Code, w.Body)
	}
	w := e.api("PUT", "/api/v1/groups/oncall", map[string]any{"members": []string{"bob", "alice", "bob"}})
	if w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	want := map[string]any{"id": "oncall", "members": []any{"alice", "bob"}}
	if got := decode[map[string]any](t, w); !reflect.DeepEqual(got, want) {
		t.Fatalf("put answered %v", got)
	}
	if got := decode[map[string]any](t, e.api("GET", "/api/v1/groups/oncall", nil)); !reflect.DeepEqual(got, want) {
		t.Fatalf("get answered %v", got)
	}
	// A second PUT replaces the whole list; an empty one is allowed.
	e.api("PUT", "/api/v1/groups/oncall", map[string]any{"members": []string{"carol"}})
	if got := decode[map[string]any](t, e.api("GET", "/api/v1/groups/oncall", nil)); !reflect.DeepEqual(got["members"], []any{"carol"}) {
		t.Fatalf("not replaced: %v", got)
	}
	e.group("oncall")
	if got := decode[map[string]any](t, e.api("GET", "/api/v1/groups/oncall", nil)); !reflect.DeepEqual(got["members"], []any{}) {
		t.Fatalf("not emptied: %v", got)
	}
	if w := e.api("DELETE", "/api/v1/groups/oncall", nil); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := e.api("DELETE", "/api/v1/groups/oncall", nil); w.Code != 404 {
		t.Fatalf("second delete: %d", w.Code)
	}
	for _, c := range []struct {
		path, body, field string
	}{
		{"/api/v1/groups/bad%20id", `{"members":[]}`, "id"},
		{"/api/v1/groups/g", `{}`, "members"},
		{"/api/v1/groups/g", `{"members":null}`, "members"},
		{"/api/v1/groups/g", `{"members":["a",""]}`, "members[1]"},
		{"/api/v1/groups/g", `{"members":[1]}`, ""},
	} {
		w := e.api("PUT", c.path, c.body)
		if w.Code != 400 {
			t.Errorf("%s %s: %d %s", c.path, c.body, w.Code, w.Body)
		} else if got := decode[map[string]string](t, w)["field"]; got != c.field {
			t.Errorf("%s %s: field %q, want %q", c.path, c.body, got, c.field)
		}
	}
	if w := e.do("PUT", "/api/v1/groups/g", `{"members":[]}`, nil); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
}

func TestChannelAudienceRoundTripAndValidation(t *testing.T) {
	e := newEnv(t)
	e.group("oncall")
	e.audienceChannel("ops", only([]string{"alice"}, "oncall"))
	got := decode[map[string]any](t, e.api("GET", "/api/v1/channels/ops", nil))
	if !reflect.DeepEqual(got["audience"], map[string]any{"mode": "only", "users": []any{"alice"}, "groups": []any{"oncall"}}) {
		t.Fatalf("audience %v", got["audience"])
	}
	// Absent keeps the stored audience, so an old client republishing never widens access.
	e.audienceChannel("ops", nil)
	got = decode[map[string]any](t, e.api("GET", "/api/v1/channels/ops", nil))
	if got["audience"].(map[string]any)["mode"] != "only" {
		t.Fatalf("an absent audience must keep the stored one: %v", got["audience"])
	}
	e.audienceChannel("ops", map[string]any{"mode": "everyone"})
	got = decode[map[string]any](t, e.api("GET", "/api/v1/channels/ops", nil))
	if !reflect.DeepEqual(got["audience"], map[string]any{"mode": "everyone", "users": []any{}, "groups": []any{}}) {
		t.Fatalf("default audience %v", got["audience"])
	}
	for _, c := range []struct {
		aud   map[string]any
		field string
	}{
		{map[string]any{}, "audience.mode"},
		{map[string]any{"mode": "some"}, "audience.mode"},
		{map[string]any{"mode": "everyone", "users": []string{"a"}}, "audience.users"},
		{map[string]any{"mode": "only", "users": []string{""}}, "audience.users[0]"},
		{map[string]any{"mode": "only", "groups": []string{"ok", "not ok"}}, "audience.groups[1]"},
	} {
		body := okChannel()
		body["audience"] = c.aud
		w := e.api("PUT", "/api/v1/channels/bad", body)
		if w.Code != 400 || decode[map[string]string](t, w)["field"] != c.field {
			t.Errorf("%v: %d %s, want field %s", c.aud, w.Code, w.Body, c.field)
		}
	}
}

func TestAudienceNamingAnUnknownGroupIsRefused(t *testing.T) {
	e := newEnv(t)
	body := okChannel()
	body["audience"] = except(nil, "typo")
	w := e.api("PUT", "/api/v1/channels/ops", body)
	if got := decode[map[string]string](t, w); w.Code != 400 || got["field"] != "audience.groups" || got["message"] != "unknown group" {
		t.Fatalf("channel: %d %s", w.Code, w.Body)
	}
	w = e.api("PUT", "/api/v1/feeds/f", map[string]any{"audience": except(nil, "typo"), "data": map[string]any{}})
	if got := decode[map[string]string](t, w); w.Code != 400 || got["field"] != "audience.groups" || got["message"] != "unknown group" {
		t.Fatalf("feed: %d %s", w.Code, w.Body)
	}
	// Users are never looked up, and deleting a group that is still named stays allowed.
	e.group("oncall", "alice")
	e.audienceChannel("ops", only([]string{"not-paired-yet"}, "oncall"))
	if w := e.api("DELETE", "/api/v1/groups/oncall", nil); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
}

func TestSyncOmitsChannelsTheUserCannotAccess(t *testing.T) {
	e := newEnv(t)
	e.group("oncall", "alice")
	e.audienceChannel("open", nil)
	e.audienceChannel("everyone", map[string]any{"mode": "everyone"})
	e.audienceChannel("only-alice", only([]string{"alice"}))
	e.audienceChannel("only-oncall", only(nil, "oncall"))
	e.audienceChannel("not-bob", except([]string{"bob"}))
	e.audienceChannel("not-oncall", except(nil, "oncall"))
	e.audienceChannel("only-early", only([]string{"carol"})) // carol has not paired yet
	alice, bob := e.newDevice("alice-phone"), e.newDevice("bob-phone")
	alice.pair("alice")
	bob.pair("bob")

	if got := syncedChannels(t, alice); got != "everyone,not-bob,only-alice,only-oncall,open" {
		t.Fatalf("alice: %s", got)
	}
	if got := syncedChannels(t, bob); got != "everyone,not-oncall,open" {
		t.Fatalf("bob: %s", got)
	}

	// Changes apply to the very next sync: nothing is cached.
	e.group("oncall", "bob")
	if got := syncedChannels(t, alice); got != "everyone,not-bob,not-oncall,only-alice,open" {
		t.Fatalf("alice after the group changed: %s", got)
	}
	if got := syncedChannels(t, bob); got != "everyone,only-oncall,open" {
		t.Fatalf("bob after the group changed: %s", got)
	}
	e.audienceChannel("only-alice", only([]string{"bob"}))
	if got := syncedChannels(t, alice); strings.Contains(got, "only-alice") {
		t.Fatalf("alice kept a channel she was removed from: %s", got)
	}
	// Carol pairs later and is then in the audience she was named in beforehand.
	carol := e.newDevice("carol-phone")
	carol.pair("carol")
	if got := syncedChannels(t, carol); !strings.Contains(got, "only-early") {
		t.Fatalf("carol: %s", got)
	}
}

func TestFeedAudience(t *testing.T) {
	e := newFeedEnv(t)
	e.audienceChannel("ops", only([]string{"alice"}))
	alice, bob := e.newDevice("alice-phone"), e.newDevice("bob-phone")
	alice.pair("alice")
	bob.pair("bob")
	put := func(id string, body map[string]any) int {
		body["template_id"], body["data"] = "status", map[string]any{"v": 1}
		return e.putFeed(id, body)
	}
	listed := func(d *testDevice) string {
		t.Helper()
		w := d.request("GET", "/device/v1/feeds", nil)
		var ids []string
		for _, f := range decode[struct {
			Feeds []struct {
				FeedID string `json:"feed_id"`
			} `json:"feeds"`
		}](t, w).Feeds {
			ids = append(ids, f.FeedID)
		}
		return strings.Join(ids, ",")
	}

	put("inherits", map[string]any{"channel": "ops"})
	put("own", map[string]any{"channel": "ops", "audience": except([]string{"alice"})})
	put("open", map[string]any{"channel": "ops", "audience": map[string]any{"mode": "everyone"}})
	put("free", map[string]any{})
	if got := listed(alice); got != "free,inherits,open" {
		t.Fatalf("alice lists %s", got)
	}
	if got := listed(bob); got != "free,open,own" {
		t.Fatalf("bob lists %s", got)
	}
	if w := bob.request("GET", "/device/v1/feeds/inherits", nil); w.Code != 403 || errCode(t, w) != "no_access" {
		t.Fatalf("bob fetches an inherited feed: %d %s", w.Code, w.Body)
	}
	if w := bob.request("GET", "/device/v1/feeds/own", nil); w.Code != 200 {
		t.Fatalf("bob fetches a feed with its own audience: %d %s", w.Code, w.Body)
	}
	if w := alice.request("GET", "/device/v1/feeds/own", nil); w.Code != 403 {
		t.Fatalf("alice fetches a feed that excludes her: %d", w.Code)
	}
	if w := alice.request("GET", "/device/v1/feeds/none", nil); w.Code != 404 {
		t.Fatalf("unknown feed: %d", w.Code)
	}

	// The channel's audience changes at once for the feeds that inherit it.
	e.audienceChannel("ops", map[string]any{"mode": "everyone"})
	if w := bob.request("GET", "/device/v1/feeds/inherits", nil); w.Code != 200 {
		t.Fatalf("bob after the channel opened: %d %s", w.Code, w.Body)
	}

	// The sender reads the settings back.
	w := e.api("GET", "/api/v1/feeds/own", nil)
	got := decode[map[string]any](t, w)
	if w.Code != 200 || got["channel"] != "ops" || !reflect.DeepEqual(got["audience"], map[string]any{"mode": "except", "users": []any{"alice"}, "groups": []any{}}) {
		t.Fatalf("get feed: %d %v", w.Code, got)
	}
	if got := decode[map[string]any](t, e.api("GET", "/api/v1/feeds/inherits", nil)); got["audience"] != nil {
		t.Fatalf("an inheriting feed reports an audience: %v", got)
	}
	if w := e.api("GET", "/api/v1/feeds/none", nil); w.Code != 404 {
		t.Fatalf("unknown feed: %d", w.Code)
	}

	// Validation.
	if w := e.api("PUT", "/api/v1/feeds/x", map[string]any{"data": 1, "channel": "nope"}); w.Code != 400 || decode[map[string]string](t, w)["field"] != "channel" {
		t.Fatalf("unknown channel: %d %s", w.Code, w.Body)
	}
	if w := e.api("PUT", "/api/v1/feeds/x", map[string]any{"data": 1, "audience": map[string]any{"mode": "nope"}}); w.Code != 400 {
		t.Fatalf("bad audience: %d %s", w.Code, w.Body)
	}
}

func TestFeedWidgetPushesOnlyReachPeopleInTheAudience(t *testing.T) {
	e := newFeedEnv(t)
	alice, bob := e.newDevice("alice-phone"), e.newDevice("bob-phone")
	alice.pair("alice")
	bob.pair("bob")
	w := e.api("PUT", "/api/v1/feeds/status", map[string]any{"template_id": "status", "data": 1, "audience": only([]string{"alice"})})
	if w.Code != 202 || decode[struct {
		Devices int `json:"devices"`
	}](t, w).Devices != 1 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, p := range widgetPushes(e) {
		if p.Sealed != "wid-alice-phone" {
			t.Fatalf("push to %s", p.Sealed)
		}
	}
	if len(widgetPushes(e)) != 1 {
		t.Fatalf("pushes %d", len(widgetPushes(e)))
	}
}

func TestDeviceCallsOnAnInaccessibleChannelAreRefused(t *testing.T) {
	e := newEnv(t)
	e.audienceChannel("ops", map[string]any{"mode": "everyone"})
	if w := e.api("PUT", "/api/v1/templates/deploy", map[string]any{"version": 1, "body": json.RawMessage(goodTemplate)}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	phone := e.newDevice("phone")
	phone.pair("alice")

	m := msg("alice", "ops")
	m["actions"] = []any{map[string]any{"id": "ack", "title": "Ack", "ack": true}}
	code, sent, raw := e.send(m)
	if code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	w := e.api("POST", "/api/v1/activities", startBody(map[string]any{"progress": 1}))
	if w.Code != 201 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	act := decode[struct {
		ActivityID string `json:"activity_id"`
	}](t, w).ActivityID
	// Alice acknowledges nothing yet; first check she can still use everything.
	if w := phone.request("POST", "/device/v1/activities/"+act+"/token", map[string]string{"sealed_update": "tok"}); w.Code != 204 {
		t.Fatalf("token with access: %d %s", w.Code, w.Body)
	}

	// Remove her from the channel.
	e.audienceChannel("ops", only([]string{"bob"}))
	if w := phone.request("POST", "/device/v1/actions", map[string]any{"message_id": sent.MessageID, "action_id": "ack"}); w.Code != 403 || errCode(t, w) != "no_access" {
		t.Fatalf("action: %d %s", w.Code, w.Body)
	}
	if got := e.api("GET", "/api/v1/messages/"+sent.MessageID, nil); decode[msgResp](t, got).Status == "acked" {
		t.Fatal("a refused ack still acknowledged the message")
	}
	if w := phone.request("POST", "/device/v1/activities/"+act+"/token", map[string]string{"sealed_update": "tok2"}); w.Code != 403 || errCode(t, w) != "no_access" {
		t.Fatalf("activity token: %d %s", w.Code, w.Body)
	}
	// The sender cannot start an activity for her either.
	if w := e.api("POST", "/api/v1/activities", startBody(map[string]any{"progress": 1})); w.Code != 403 || errCode(t, w) != "no_access" {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}

	// Another user's message is still unknown to them, not no_access: the ownership check comes first.
	bob := e.newDevice("bob-phone")
	bob.pair("bob")
	if w := bob.request("POST", "/device/v1/actions", map[string]any{"message_id": sent.MessageID, "action_id": "ack"}); w.Code != 404 || errCode(t, w) != "unknown_message" {
		t.Fatalf("other user's message: %d %s", w.Code, w.Body)
	}

	// Access back: it works again.
	e.audienceChannel("ops", map[string]any{"mode": "everyone"})
	if w := phone.request("POST", "/device/v1/actions", map[string]any{"message_id": sent.MessageID, "action_id": "ack"}); w.Code != 200 {
		t.Fatalf("action after access came back: %d %s", w.Code, w.Body)
	}
}

func TestAcksOmitMessagesOfInaccessibleChannels(t *testing.T) {
	e := newEnv(t)
	e.audienceChannel("ops", map[string]any{"mode": "everyone"})
	phone := e.newDevice("phone")
	phone.pair("alice")
	m := msg("alice", "ops")
	m["actions"] = []any{map[string]any{"id": "ack", "title": "Ack", "ack": true}}
	_, sent, _ := e.send(m)
	e.runWorker()
	if w := phone.request("POST", "/device/v1/actions", map[string]any{"message_id": sent.MessageID, "action_id": "ack"}); w.Code != 200 {
		t.Fatalf("ack: %d", w.Code)
	}
	acked := func() int {
		w := phone.request("GET", "/device/v1/acks?ids="+sent.MessageID, nil)
		if w.Code != 200 {
			t.Fatalf("acks: %d %s", w.Code, w.Body)
		}
		return len(decode[struct {
			Acked map[string]int64 `json:"acked"`
		}](t, w).Acked)
	}
	if acked() != 1 {
		t.Fatal("acknowledged message missing")
	}
	e.audienceChannel("ops", only([]string{"bob"}))
	if acked() != 0 {
		t.Fatal("acks listed a message of a channel the user lost")
	}
}

type recipientResp struct {
	ExternalUserID string `json:"external_user_id"`
	MessageID      string `json:"message_id"`
	Status         string `json:"status"`
	Devices        int    `json:"devices"`
}

type multiResp struct {
	msgResp
	Recipients []recipientResp `json:"recipients"`
}

func TestSendReportsNoAccessPerRecipient(t *testing.T) {
	e := newEnv(t)
	e.group("oncall", "alice")
	e.audienceChannel("ops", only([]string{"bob"}, "oncall"))
	alice, bob, carol := e.newDevice("alice-phone"), e.newDevice("bob-phone"), e.newDevice("carol-phone")
	alice.pair("alice")
	bob.pair("bob")
	carol.pair("carol")
	send := func(body map[string]any) multiResp {
		t.Helper()
		w := e.api("POST", "/api/v1/messages", body)
		if w.Code != 202 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		return decode[multiResp](t, w)
	}
	rec := func(r multiResp) map[string]recipientResp {
		out := map[string]recipientResp{}
		for _, x := range r.Recipients {
			out[x.ExternalUserID] = x
		}
		return out
	}

	// Specific users: the ones outside the audience get no_access and no message, the rest are delivered.
	body := msg("", "ops")
	delete(body, "external_user_id")
	body["external_user_ids"] = []string{"alice", "carol", "bob", "alice"}
	r := send(body)
	got := rec(r)
	if len(r.Recipients) != 3 || got["carol"].Status != "no_access" || got["carol"].MessageID != "" ||
		got["alice"].Status != "queued" || got["alice"].Devices != 1 || !strings.HasPrefix(got["alice"].MessageID, "msg_") ||
		got["bob"].Status != "queued" {
		t.Fatalf("%+v", r.Recipients)
	}
	if r.MessageID != "" {
		t.Fatalf("a multi-user send reports one message_id: %s", r.MessageID)
	}
	e.runWorker()
	var sealed []string
	for _, p := range e.relay.pushes() {
		sealed = append(sealed, p.Sealed)
	}
	sort.Strings(sealed) // messages created in the same second are sent in no fixed order
	if strings.Join(sealed, ",") != "alert-alice-phone,alert-bob-phone" {
		t.Fatalf("pushed to %v", sealed)
	}

	// The channel's audience: every paired user in it, resolved now.
	body = msg("", "ops")
	delete(body, "external_user_id")
	body["to_audience"] = true
	r = send(body)
	if got := rec(r); len(r.Recipients) != 2 || got["alice"].Status != "queued" || got["bob"].Status != "queued" {
		t.Fatalf("%+v", r.Recipients)
	}
	e.group("oncall") // alice leaves the group: the next send no longer reaches her
	if r = send(body); len(r.Recipients) != 1 || r.Recipients[0].ExternalUserID != "bob" {
		t.Fatalf("%+v", r.Recipients)
	}
	e.audienceChannel("ops", except([]string{"bob"}))
	if r = send(body); len(r.Recipients) != 2 || r.Recipients[0].ExternalUserID != "alice" || r.Recipients[1].ExternalUserID != "carol" {
		t.Fatalf("except mode: %+v", r.Recipients)
	}

	// The single-user form keeps its fields, and now also lists the recipient. Without access there is no message.
	e.audienceChannel("ops", only([]string{"bob"}))
	r = send(msg("bob", "ops"))
	if r.Status != "queued" || r.Devices != 1 || !strings.HasPrefix(r.MessageID, "msg_") || len(r.Recipients) != 1 || r.Recipients[0].MessageID != r.MessageID {
		t.Fatalf("%+v", r)
	}
	if raw := e.api("POST", "/api/v1/messages", msg("alice", "ops")).Body.String(); !strings.Contains(raw, `"message_id":""`) {
		t.Fatalf("older SDKs expect a message_id string: %s", raw)
	}
	r = send(msg("alice", "ops"))
	if r.Status != "no_access" || r.MessageID != "" || r.Devices != 0 || len(r.Recipients) != 1 || r.Recipients[0].Status != "no_access" {
		t.Fatalf("%+v", r)
	}
	if ms, _ := e.st.MessagesByUserStatus("alice", "queued"); len(ms) != 2 { // the two sent while she had access
		t.Fatalf("alice has %d queued messages", len(ms))
	}
}

func TestSendTargetValidation(t *testing.T) {
	e := newEnv(t)
	e.channel("ops", "active", "active")
	base := func(extra map[string]any) map[string]any {
		m := msg("", "ops")
		delete(m, "external_user_id")
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for name, c := range map[string]struct {
		body  map[string]any
		field string
	}{
		"no target":       {base(nil), "external_user_id"},
		"empty body":      {map[string]any{}, "external_user_id"},
		"two targets":     {base(map[string]any{"external_user_id": "a", "to_audience": true}), "external_user_ids"},
		"empty id":        {base(map[string]any{"external_user_ids": []string{"a", ""}}), "external_user_ids[1]"},
		"too many":        {base(map[string]any{"external_user_ids": make([]string, 1001)}), "external_user_ids"},
		"missing channel": {map[string]any{"external_user_id": "a", "title": "t", "body": "b"}, "channel"},
	} {
		w := e.api("POST", "/api/v1/messages", c.body)
		if w.Code != 400 || decode[map[string]string](t, w)["field"] != c.field {
			t.Errorf("%s: %d %s, want field %s", name, w.Code, w.Body, c.field)
		}
	}
	// An audience nobody paired for is an empty send, not an error.
	body := base(map[string]any{"to_audience": true})
	if w := e.api("POST", "/api/v1/messages", body); w.Code != 202 || len(decode[multiResp](t, w).Recipients) != 0 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestQueuedMessageAndPagesStopWhenAccessIsLost(t *testing.T) {
	e := newEnv(t)
	e.audienceChannel("ops", map[string]any{"mode": "everyone"})
	if w := e.api("PUT", "/api/v1/channels/oncall", map[string]any{"name": "oncall", "default_level": "page", "max_level": "page"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	phone := e.newDevice("phone")
	phone.pair("alice")

	// A message accepted while she had access, not yet pushed, is not pushed after she lost it.
	_, queued, _ := e.send(msg("alice", "ops"))
	e.audienceChannel("ops", only([]string{"bob"}))
	e.runWorker()
	if n := len(e.relay.pushes()); n != 0 {
		t.Fatalf("pushed %d times to a user without access", n)
	}
	got := decode[msgResp](t, e.api("GET", "/api/v1/messages/"+queued.MessageID, nil))
	if got.Status != "undeliverable" || len(got.Deliveries) != 1 || got.Deliveries[0].Status != "skipped" || got.Deliveries[0].Error != "no_access" {
		t.Fatalf("%+v", got)
	}

	// A page that is being resent stops being resent.
	_, page, _ := e.send(msg("alice", "oncall"))
	e.runWorker()
	if n := len(e.relay.pushes()); n != 1 {
		t.Fatalf("initial page pushes %d", n)
	}
	if w := e.api("PUT", "/api/v1/channels/oncall", map[string]any{"name": "oncall", "default_level": "page", "max_level": "page",
		"audience": only([]string{"bob"})}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	e.clk.Advance(130 * time.Second)
	e.runWorker()
	if n := len(e.relay.pushes()); n != 1 {
		t.Fatalf("page resent to a user without access: %d pushes", n)
	}
	if got := decode[msgResp](t, e.api("GET", "/api/v1/messages/"+page.MessageID, nil)); got.Status != "expired" {
		t.Fatalf("page status %s", got.Status)
	}
}

func TestLiveActivityUpdatesStopWhenAccessIsLost(t *testing.T) {
	l := newLive(t, nil)
	act := l.start(map[string]any{"progress": 0.1})
	if code := l.registerToken(l.phone, act, "sealed-1"); code != 204 {
		t.Fatalf("token: %d", code)
	}
	patch := func() {
		t.Helper()
		if w := l.e.api("PATCH", "/api/v1/activities/"+act, map[string]any{"data": map[string]any{"progress": 0.5}}); w.Code != 202 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	patch()
	if n := len(l.pushesOfKind("liveactivity")); n != 1 {
		t.Fatalf("update pushes %d", n)
	}
	l.e.audienceChannel("ops", only([]string{"bob"}))
	patch()
	if n := len(l.pushesOfKind("liveactivity")); n != 1 {
		t.Fatalf("an update reached a user without access: %d pushes", n)
	}
	// The end still goes out, so the activity leaves the screen.
	if w := l.e.api("DELETE", "/api/v1/activities/"+act, nil); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	ps := l.pushesOfKind("liveactivity")
	if len(ps) != 2 || parseLive(t, ps[1]).APS.Event != "end" {
		t.Fatalf("pushes %d", len(ps))
	}
}

func TestKickedPairingAnswersWithItsReason(t *testing.T) {
	e := newEnv(t)
	kicked, left := e.newDevice("kicked"), e.newDevice("left")
	kicked.pair("alice")
	left.pair("bob")
	reason := func(d *testDevice) (int, map[string]string) {
		t.Helper()
		w := d.request("GET", "/device/v1/sync", nil)
		return w.Code, decode[map[string]string](t, w)
	}

	if w := e.api("DELETE", "/api/v1/users/alice/pairings", nil); w.Code != 200 {
		t.Fatalf("kick: %d %s", w.Code, w.Body)
	}
	if code, body := reason(kicked); code != 401 || !reflect.DeepEqual(body, map[string]string{"error": "pairing_revoked", "reason": "kicked"}) {
		t.Fatalf("kicked: %d %v", code, body)
	}
	if w := left.request("DELETE", "/device/v1/pairings/"+left.PairingID, nil); w.Code != 204 {
		t.Fatalf("unpair: %d", w.Code)
	}
	if code, body := reason(left); code != 401 || !reflect.DeepEqual(body, map[string]string{"error": "pairing_revoked"}) {
		t.Fatalf("unpaired by the device: %d %v", code, body)
	}
}

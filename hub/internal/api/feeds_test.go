package api

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/recodive/hansho/hub/internal/relay"
)

type feedResp struct {
	FeedID     string          `json:"feed_id"`
	TemplateID string          `json:"template_id"`
	Data       json.RawMessage `json:"data"`
	UpdatedAt  int64           `json:"updated_at"`
	Sig        string          `json:"sig"`
}

func newFeedEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	if w := e.api("PUT", "/api/v1/templates/status", map[string]any{"version": 1, "body": json.RawMessage(goodTemplate)}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return e
}

func (e *env) putFeed(id string, body map[string]any) int {
	e.t.Helper()
	return e.api("PUT", "/api/v1/feeds/"+id, body).Code
}

func widgetPushes(e *env) []relay.PushRequest {
	var out []relay.PushRequest
	for _, p := range e.relay.pushes() {
		if p.Kind == "widgets" {
			out = append(out, p)
		}
	}
	return out
}

func TestUserFeedOverridesGlobal(t *testing.T) {
	e := newFeedEnv(t)
	alice, bob := e.newDevice("alice-phone"), e.newDevice("bob-phone")
	alice.pair("alice")
	bob.pair("bob")

	if c := e.putFeed("status", map[string]any{"template_id": "status", "data": map[string]any{"v": "global"}}); c != 202 {
		t.Fatalf("global: %d", c)
	}
	if c := e.putFeed("status", map[string]any{"external_user_id": "alice", "template_id": "status", "data": map[string]any{"v": "alice"}}); c != 202 {
		t.Fatalf("user: %d", c)
	}
	get := func(d *testDevice) feedResp {
		w := d.request("GET", "/device/v1/feeds/status", nil)
		if w.Code != 200 {
			t.Fatalf("get: %d %s", w.Code, w.Body)
		}
		return decode[feedResp](t, w)
	}
	if got := get(alice); string(got.Data) != `{"v":"alice"}` || got.FeedID != "status" || got.TemplateID != "status" {
		t.Fatalf("alice sees %+v", got)
	}
	if got := get(bob); string(got.Data) != `{"v":"global"}` {
		t.Fatalf("bob sees %+v", got)
	}
	// Updating the global feed does not disturb alice's override.
	e.putFeed("status", map[string]any{"template_id": "status", "data": map[string]any{"v": "global2"}})
	if got := get(alice); string(got.Data) != `{"v":"alice"}` {
		t.Fatalf("override lost: %+v", got)
	}
	if got := get(bob); string(got.Data) != `{"v":"global2"}` {
		t.Fatalf("global not updated: %+v", got)
	}
}

func TestOtherUsersFeedIsNotReadable(t *testing.T) {
	e := newFeedEnv(t)
	alice, bob := e.newDevice("a"), e.newDevice("b")
	alice.pair("alice")
	bob.pair("bob")
	e.putFeed("private", map[string]any{"external_user_id": "alice", "template_id": "status", "data": map[string]any{"secret": 1}})
	if w := alice.request("GET", "/device/v1/feeds/private", nil); w.Code != 200 {
		t.Fatalf("owner: %d", w.Code)
	}
	if w := bob.request("GET", "/device/v1/feeds/private", nil); w.Code != 404 {
		t.Fatalf("other user: %d %s", w.Code, w.Body)
	}
	if w := alice.request("GET", "/device/v1/feeds/none", nil); w.Code != 404 {
		t.Fatalf("unknown: %d", w.Code)
	}
	if w := e.do("GET", "/device/v1/feeds/private", nil, nil); w.Code != 401 {
		t.Fatalf("unsigned: %d", w.Code)
	}
}

func TestWidgetPushOnlyToDevicesWithWidgetTokens(t *testing.T) {
	e := newFeedEnv(t)
	phone := e.newDevice("phone")
	phone.pair("alice")
	watch := e.newDevice("watch")
	watch.Tokens["widgets"] = ""
	if w := watch.addTo(phone.PairingID, phone.PairingKey); w.Code != 201 {
		t.Fatal(w.Body)
	}
	bob := e.newDevice("bobphone")
	bob.pair("bob")
	carol := e.newDevice("carol")
	carol.Tokens["widgets"] = ""
	carol.pair("carol")

	// User feed: only alice's capable device.
	e.putFeed("status", map[string]any{"external_user_id": "alice", "template_id": "status", "data": map[string]any{}})
	ps := widgetPushes(e)
	if len(ps) != 1 || ps[0].Sealed != "wid-phone" || ps[0].Priority != 5 || string(ps[0].Payload) != `{"aps":{"content-changed":true}}` {
		t.Fatalf("%+v", e.relay.pushes())
	}
	// Global feed: every paired user's capable devices.
	e.putFeed("news", map[string]any{"template_id": "status", "data": map[string]any{}})
	got := map[string]int{}
	for _, p := range widgetPushes(e) {
		got[p.Sealed]++
	}
	if got["wid-phone"] != 2 || got["wid-bobphone"] != 1 || got["wid-watch"] != 0 || got["wid-carol"] != 0 || len(widgetPushes(e)) != 3 {
		t.Fatalf("%v", got)
	}
}

func TestFeedSignatureVerifies(t *testing.T) {
	e := newFeedEnv(t)
	d := e.newDevice("phone")
	d.pair("alice")
	// Characters that Go's JSON encoder would escape by default must survive byte for byte.
	w0 := e.do("PUT", "/api/v1/feeds/status", `{"external_user_id":"alice","template_id":"status","data":{"msg":"a<b>&c","n":1.50}}`,
		map[string]string{"Authorization": "Bearer " + e.apiKey})
	if w0.Code != 202 {
		t.Fatalf("%d %s", w0.Code, w0.Body)
	}
	e.clk.Advance(42 * time.Second)

	w := d.request("GET", "/device/v1/feeds/status", nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var raw struct {
		Data json.RawMessage `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &raw)
	got := decode[feedResp](t, w)
	if got.UpdatedAt != t0.Unix() {
		t.Fatalf("updated_at %d", got.UpdatedAt)
	}
	sum := sha256.Sum256(raw.Data)
	msg := "status\n" + strconv.FormatInt(got.UpdatedAt, 10) + "\n" + hex.EncodeToString(sum[:])
	sig, err := b64url.DecodeString(got.Sig)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(e.hubKey.Public().(ed25519.PublicKey), []byte(msg), sig) {
		t.Fatalf("signature does not verify over %q (data %s)", msg, raw.Data)
	}
	if string(raw.Data) != `{"msg":"a<b>&c","n":1.50}` {
		t.Fatalf("data bytes altered: %s", raw.Data)
	}
	// Re-publishing moves updated_at and changes the signature.
	e.putFeed("status", map[string]any{"external_user_id": "alice", "template_id": "status", "data": map[string]any{"x": 1}})
	got2 := decode[feedResp](t, d.request("GET", "/device/v1/feeds/status", nil))
	if got2.UpdatedAt != t0.Add(42*time.Second).Unix() || got2.Sig == got.Sig {
		t.Fatalf("%+v", got2)
	}
}

func TestFeedValidation(t *testing.T) {
	e := newFeedEnv(t)
	if c := e.putFeed("status", map[string]any{"template_id": "nope", "data": map[string]any{}}); c != 400 {
		t.Errorf("unknown template: %d", c)
	}
	if c := e.putFeed("status", map[string]any{"data": map[string]any{}}); c != 202 {
		t.Errorf("missing template is allowed (automatic layout): %d", c)
	}
	if c := e.putFeed("status", map[string]any{"template_id": "status"}); c != 400 {
		t.Errorf("missing data: %d", c)
	}
	if c := e.putFeed("bad$id", map[string]any{"template_id": "status", "data": map[string]any{}}); c != 400 {
		t.Errorf("bad id: %d", c)
	}
	if w := e.do("PUT", "/api/v1/feeds/status", `{"template_id":"status","data":{}}`, nil); w.Code != 401 {
		t.Errorf("unauthenticated: %d", w.Code)
	}
	big := map[string]any{"template_id": "status", "data": map[string]any{"x": strings.Repeat("a", 70000)}}
	if c := e.putFeed("status", big); c != 413 {
		t.Errorf("oversized: %d", c)
	}
}

func TestDeadWidgetTokenCleared(t *testing.T) {
	e := newFeedEnv(t)
	d := e.newDevice("phone")
	d.pair("alice")
	e.relay.PushErr["wid-phone"] = relay.ErrUnregistered
	if c := e.putFeed("status", map[string]any{"external_user_id": "alice", "template_id": "status", "data": map[string]any{}}); c != 202 {
		t.Fatalf("%d", c)
	}
	got, _ := e.st.GetDevice(d.ID)
	if got.SealedWidgets != "" || got.SealedAlert == "" {
		t.Fatalf("%+v", got)
	}
}

// A service that just publishes data (and lets the app draw it with its automatic layout) must not have to create a
// template first. template_id is optional; when given it still has to exist.
func TestFeedWithoutTemplateIsAccepted(t *testing.T) {
	e := newFeedEnv(t)
	d := e.newDevice("phone")
	d.pair("alice")

	if c := e.putFeed("cpu", map[string]any{"external_user_id": "alice", "data": map[string]any{"value": 64, "label": "CPU"}}); c != 202 {
		t.Fatalf("feed without template_id: status %d", c)
	}
	w := d.request("GET", "/device/v1/feeds/cpu", nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	got := decode[feedResp](t, w)
	if got.TemplateID != "" {
		t.Fatalf("template_id %q, want empty", got.TemplateID)
	}
	var raw struct {
		Data json.RawMessage `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &raw)
	sum := sha256.Sum256(raw.Data)
	msg := "cpu\n" + strconv.FormatInt(got.UpdatedAt, 10) + "\n" + hex.EncodeToString(sum[:])
	sig, _ := b64url.DecodeString(got.Sig)
	if !ed25519.Verify(e.hubKey.Public().(ed25519.PublicKey), []byte(msg), sig) {
		t.Fatal("signature must still verify for a feed without a template")
	}

	// A named template must still exist.
	if c := e.putFeed("cpu", map[string]any{"template_id": "nope", "data": map[string]any{}}); c != 400 {
		t.Fatalf("unknown template_id: status %d, want 400", c)
	}
}

type feedListResp struct {
	Feeds []struct {
		FeedID    string `json:"feed_id"`
		Title     string `json:"title"`
		UpdatedAt int64  `json:"updated_at"`
	} `json:"feeds"`
}

// A device lists exactly what it can fetch: its own feeds and the global ones, one entry per id, never another
// person's feed, and its own winning over a global feed with the same id.
func TestListFeeds(t *testing.T) {
	e := newFeedEnv(t)
	alice, bob := e.newDevice("alice-phone"), e.newDevice("bob-phone")
	alice.pair("alice")
	bob.pair("bob")

	e.putFeed("news", map[string]any{"title": "  News  ", "data": map[string]any{}})
	e.putFeed("status", map[string]any{"title": "Global status", "data": map[string]any{}})
	e.putFeed("status", map[string]any{"external_user_id": "alice", "title": "Alice status", "data": map[string]any{}})
	e.putFeed("private", map[string]any{"external_user_id": "bob", "data": map[string]any{}})

	list := func(d *testDevice) (feedListResp, string) {
		w := d.request("GET", "/device/v1/feeds", nil)
		if w.Code != 200 {
			t.Fatalf("list: %d %s", w.Code, w.Body)
		}
		return decode[feedListResp](t, w), w.Body.String()
	}
	got, _ := list(alice)
	if len(got.Feeds) != 2 || got.Feeds[0].FeedID != "news" || got.Feeds[0].Title != "News" ||
		got.Feeds[1].FeedID != "status" || got.Feeds[1].Title != "Alice status" || got.Feeds[1].UpdatedAt != t0.Unix() {
		t.Fatalf("alice sees %+v", got)
	}
	got, raw := list(bob)
	if len(got.Feeds) != 3 || got.Feeds[1].FeedID != "private" || got.Feeds[2].Title != "Global status" {
		t.Fatalf("bob sees %+v", got)
	}
	if strings.Contains(raw, `"title":""`) {
		t.Fatalf("an unset title should be omitted: %s", raw)
	}

	// Someone with no feeds of their own still sees the global ones.
	carol := e.newDevice("carol")
	carol.pair("carol")
	if got, _ := list(carol); len(got.Feeds) != 2 {
		t.Fatalf("carol sees %+v", got)
	}
	if w := e.do("GET", "/device/v1/feeds", nil, nil); w.Code != 401 {
		t.Fatalf("unsigned: %d", w.Code)
	}
}

func TestFeedTitleValidation(t *testing.T) {
	e := newFeedEnv(t)
	if c := e.putFeed("a", map[string]any{"title": strings.Repeat("x", 64), "data": map[string]any{}}); c != 202 {
		t.Errorf("64 bytes: %d", c)
	}
	if c := e.putFeed("a", map[string]any{"title": strings.Repeat("x", 65), "data": map[string]any{}}); c != 400 {
		t.Errorf("65 bytes: %d", c)
	}
	if c := e.putFeed("a", map[string]any{"title": "  " + strings.Repeat("x", 64) + "  ", "data": map[string]any{}}); c != 202 {
		t.Errorf("padding is trimmed before the limit: %d", c)
	}
	if c := e.putFeed("a", map[string]any{"title": 5, "data": map[string]any{}}); c != 400 {
		t.Errorf("non-string title: %d", c)
	}
	w := e.api("PUT", "/api/v1/feeds/a", map[string]any{"title": strings.Repeat("é", 33), "data": map[string]any{}})
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"field":"title"`) {
		t.Errorf("66 bytes of two-byte characters: %d %s", w.Code, w.Body)
	}
}

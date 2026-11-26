package api

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/recodive/hansho/hub/internal/store"
)

const iacNotice = "If you manage this hub with Terraform or scripts, the next apply overwrites changes made here."

var textareaRe = regexp.MustCompile(`(?s)<textarea id="([^"]+)"[^>]*>(.*?)</textarea>`)

// textarea returns the unescaped text of the text area with that id.
func textarea(t *testing.T, page, id string) string {
	t.Helper()
	for _, m := range textareaRe.FindAllStringSubmatch(page, -1) {
		if m[1] == id {
			return html.UnescapeString(m[2])
		}
	}
	t.Fatalf("no text area %q in:\n%s", id, page)
	return ""
}

// postForm sends a form with the session's token and several values per field.
func (b *browser) postForm(path string, form url.Values) *httptest.ResponseRecorder {
	b.e.t.Helper()
	form.Set("csrf", b.csrf)
	return b.do("POST", path, form, nil)
}

func (e *env) apiChannel(id string, body map[string]any) {
	e.t.Helper()
	if w := e.api("PUT", "/api/v1/channels/"+id, body); w.Code != 200 {
		e.t.Fatalf("PUT channel %s: %d %s", id, w.Code, w.Body)
	}
}

func channelBody(name, max string, aud any) map[string]any {
	m := map[string]any{"name": name, "default_level": "active", "max_level": max}
	if aud != nil {
		m["audience"] = aud
	}
	return m
}

type storedChannel struct {
	Name         string           `json:"name"`
	DefaultLevel string           `json:"default_level"`
	MaxLevel     string           `json:"max_level"`
	Thread       string           `json:"thread"`
	Actions      []map[string]any `json:"actions"`
	Audience     store.Audience   `json:"audience"`
}

func TestAdminChannels(t *testing.T) {
	logs := captureLog(t)
	e := newAdminEnv(t)
	b := e.browser().signIn()
	e.newDevice("phone").pair("alex")
	if w := e.api("PUT", "/api/v1/groups/oncall", map[string]any{"members": []string{"sam"}}); w.Code != 200 {
		t.Fatal(w.Body)
	}

	list := b.get("/admin/channels")
	if list.Code != 200 || !strings.Contains(list.Body.String(), "There are no channels yet") || !strings.Contains(list.Body.String(), iacNotice) {
		t.Fatalf("empty list: %d %s", list.Code, list.Body)
	}
	checkAdminHeaders(t, list, "channels")

	form := b.get("/admin/channels/new")
	body := form.Body.String()
	for _, want := range []string{iacNotice, "Starting level", "Highest level", "Arrives quietly in the inbox", "Breaks through Focus modes",
		"Rings like an alarm", `value="alex"`, `value="oncall"`, "Someone who has not paired yet", "Add a button"} {
		if form.Code != 200 || !strings.Contains(body, want) {
			t.Fatalf("new form lacks %q: %d\n%s", want, form.Code, body)
		}
	}
	if strings.Contains(body, "Copy as API call") {
		t.Error("a channel that does not exist yet has no API call")
	}

	// Create: two buttons (the second row is blank and dropped), people ticked, one typed, a group.
	created := b.postForm("/admin/channels/new", url.Values{
		"id": {" billing "}, "name": {" Billing alerts "}, "default_level": {"active"}, "max_level": {"critical"}, "thread": {""},
		"mode": {"only"}, "user": {"alex"}, "group": {"oncall"}, "extra": {"bob\r\n  carol \r\n\r\nalex"},
		"actions": {"3"},
		"a0_id":   {"ack"}, "a0_title": {"Acknowledge"}, "a0_ack": {"1"},
		"a1_id": {"open"}, "a1_title": {"Runbook"}, "a1_kind": {"url"}, "a1_url": {"https://example.com/r"},
	})
	if created.Code != 200 || !strings.Contains(created.Body.String(), "Channel saved") {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	got := e.api("GET", "/api/v1/channels/billing", nil)
	v := decode[storedChannel](t, got)
	if v.Name != "Billing alerts" || v.DefaultLevel != "active" || v.MaxLevel != "critical" || v.Thread != "billing" {
		t.Errorf("stored channel: %+v", v)
	}
	if len(v.Actions) != 2 || v.Actions[0]["id"] != "ack" || v.Actions[0]["ack"] != true || v.Actions[1]["kind"] != "url" || v.Actions[1]["url"] != "https://example.com/r" {
		t.Errorf("stored actions: %v", v.Actions)
	}
	if a := v.Audience; a.Mode != "only" || strings.Join(a.Users, ",") != "alex,bob,carol" || strings.Join(a.Groups, ",") != "oncall" {
		t.Errorf("stored audience: %+v", a)
	}
	if !strings.Contains(logs.String(), "admin: channel created") || !strings.Contains(logs.String(), "id=billing") {
		t.Errorf("no create log line:\n%s", logs)
	}

	list = b.get("/admin/channels")
	for _, want := range []string{"Billing alerts", "/admin/channels/billing/edit", "/admin/channels/billing/delete", "Starts at Active, never above Critical", "Only 3 people and 1 group"} {
		if !strings.Contains(list.Body.String(), want) {
			t.Errorf("list lacks %q:\n%s", want, list.Body)
		}
	}

	// Edit shows what is stored: a person who has not paired yet stays ticked, and the API call matches the channel.
	edit := b.get("/admin/channels/billing/edit")
	eb := edit.Body.String()
	for _, want := range []string{iacNotice, `value="bob" checked`, "bob (not paired yet)", `value="alex" checked`, `value="oncall" checked`,
		`name="a0_ack" value="1" checked`, `<option value="url" selected>`, "Copy as API call", "This channel's id is"} {
		if edit.Code != 200 || !strings.Contains(eb, want) {
			t.Fatalf("edit lacks %q: %d\n%s", want, edit.Code, eb)
		}
	}
	curl, jsonBody := textarea(t, eb, "apicall-curl"), textarea(t, eb, "apicall-body")
	if !strings.HasPrefix(curl, "curl -X PUT 'https://hub.example.com/api/v1/channels/billing' \\\n") || !strings.Contains(curl, `-H "Authorization: Bearer $KEY"`) ||
		!strings.Contains(curl, "-d '"+jsonBody+"'") {
		t.Errorf("curl:\n%s", curl)
	}
	if w := e.api("PUT", "/api/v1/channels/billing", jsonBody); w.Code != 200 {
		t.Errorf("the JSON body is not accepted by the API: %d %s", w.Code, w.Body)
	}
	if w := e.api("GET", "/api/v1/channels/billing", nil); w.Body.String() != got.Body.String() {
		t.Errorf("replaying the copied body changed the channel:\n%s\n%s", got.Body, w.Body)
	}

	// Save an edit: everyone again, a different name; the id comes from the path.
	if w := b.postForm("/admin/channels/billing/edit", url.Values{
		"id": {"other"}, "name": {"Billing"}, "default_level": {"passive"}, "max_level": {"page"}, "mode": {"everyone"}, "user": {"alex"}, "actions": {"0"},
	}); w.Code != 200 || !strings.Contains(w.Body.String(), "Channel saved") {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	v = decode[storedChannel](t, e.api("GET", "/api/v1/channels/billing", nil))
	if v.Name != "Billing" || v.MaxLevel != "page" || v.Audience.Mode != "everyone" || len(v.Audience.Users) != 0 || len(v.Actions) != 0 {
		t.Errorf("after edit: %+v", v)
	}
	if e.api("GET", "/api/v1/channels/other", nil).Code != 404 {
		t.Error("the id in the form of an edit made another channel")
	}
	if !strings.Contains(logs.String(), "admin: channel saved") {
		t.Errorf("no save log line:\n%s", logs)
	}

	// Validation errors come back next to the field, in words, and nothing is stored.
	ok := func() url.Values {
		return url.Values{"id": {"alerts"}, "name": {"Alerts"}, "default_level": {"active"}, "max_level": {"critical"}, "mode": {"everyone"}, "actions": {"0"}}
	}
	for name, tc := range map[string]struct {
		mod  func(url.Values)
		want string
	}{
		"no name":           {func(f url.Values) { f.Set("name", "  ") }, "Enter a name"},
		"default too high":  {func(f url.Values) { f.Set("default_level", "page") }, "starting level cannot be higher"},
		"no level":          {func(f url.Values) { f.Set("max_level", "") }, "Choose one of the levels"},
		"unknown template":  {func(f url.Values) { f.Set("template", "nope") }, "That template does not exist"},
		"unknown sound":     {func(f url.Values) { f.Set("sound", "nope") }, "That sound does not exist"},
		"unknown group":     {func(f url.Values) { f.Set("mode", "except"); f.Set("group", "ghost") }, "does not exist any more"},
		"only with nobody":  {func(f url.Values) { f.Set("mode", "only") }, "Pick at least one person or group"},
		"no mode":           {func(f url.Values) { f.Del("mode") }, "Choose who may use this channel"},
		"long thread":       {func(f url.Values) { f.Set("thread", strings.Repeat("t", 257)) }, "Keep the thread name"},
		"bad id":            {func(f url.Values) { f.Set("id", "no spaces!") }, "Use letters, numbers"},
		"existing id":       {func(f url.Values) { f.Set("id", "billing") }, "already exists"},
		"button without id": {func(f url.Values) { f.Set("actions", "1"); f.Set("a0_title", "Go") }, "Give the button an id"},
		"button no label":   {func(f url.Values) { f.Set("actions", "1"); f.Set("a0_id", "go") }, "Give the button a label"},
	} {
		f := ok()
		tc.mod(f)
		w := b.postForm("/admin/channels/new", f)
		if w.Code != 400 || !strings.Contains(w.Body.String(), tc.want) || !strings.Contains(w.Body.String(), "The channel was not saved") {
			t.Errorf("%s: %d, want %q in:\n%s", name, w.Code, tc.want, w.Body)
		}
	}
	if e.api("GET", "/api/v1/channels/alerts", nil).Code != 404 {
		t.Error("a refused form made a channel")
	}
	// The error of a button is shown on its own row, counting the blank rows that are dropped.
	f := ok()
	f.Set("actions", "3")
	f.Set("a1_id", "open")
	f.Set("a1_title", "Open")
	f.Set("a1_kind", "url")
	f.Set("a1_url", "http://example.com")
	w := b.postForm("/admin/channels/new", f)
	if w.Code != 400 || !regexp.MustCompile(`(?s)Button 2</h3>\s*<p class="field-error"[^>]*>A link button needs a link that starts with https://`).MatchString(w.Body.String()) {
		t.Errorf("button error: %d\n%s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `value="open"`) || !strings.Contains(w.Body.String(), `value="http://example.com"`) {
		t.Error("the form lost what was typed")
	}

	// Add and remove buttons without saving.
	f = ok()
	f.Set("id", "draft")
	f.Set("add", "1")
	if w := b.postForm("/admin/channels/new", f); w.Code != 200 || !strings.Contains(w.Body.String(), "Button 1") || !strings.Contains(w.Body.String(), `name="actions" value="1"`) {
		t.Errorf("add: %d\n%s", w.Code, w.Body)
	}
	f = ok()
	f.Set("id", "draft")
	f.Set("actions", "2")
	f.Set("a0_id", "first")
	f.Set("a1_id", "second")
	f.Set("remove", "0")
	if w := b.postForm("/admin/channels/new", f); w.Code != 200 || strings.Contains(w.Body.String(), "Button 2") || !strings.Contains(w.Body.String(), `value="second"`) || strings.Contains(w.Body.String(), `value="first"`) {
		t.Errorf("remove: %d\n%s", w.Code, w.Body)
	}
	if e.api("GET", "/api/v1/channels/draft", nil).Code != 404 {
		t.Error("add or remove saved the channel")
	}

	// A button kind set through the API is kept.
	e.apiChannel("odd", map[string]any{"name": "Odd", "default_level": "active", "max_level": "active",
		"actions": []map[string]any{{"id": "x", "title": "X", "kind": "custom"}}})
	if w := b.get("/admin/channels/odd/edit"); !strings.Contains(w.Body.String(), `<option value="custom" selected>`) {
		t.Errorf("unknown kind lost:\n%s", w.Body)
	}

	// A quote in a name stays valid in the shell command.
	e.apiChannel("quoted", channelBody("Bob's alerts", "active", nil))
	q := b.get("/admin/channels/quoted/edit").Body.String()
	if curl := textarea(t, q, "apicall-curl"); !strings.Contains(curl, `"name": "Bob'\''s alerts"`) {
		t.Errorf("curl quoting:\n%s", curl)
	}
	if body := textarea(t, q, "apicall-body"); !strings.Contains(body, `"name": "Bob's alerts"`) {
		t.Errorf("JSON body:\n%s", body)
	}

	// Delete asks first.
	c := b.get("/admin/channels/billing/delete")
	if c.Code != 200 || !strings.Contains(c.Body.String(), "Delete Billing?") || !strings.Contains(c.Body.String(), iacNotice) {
		t.Fatalf("confirm: %d %s", c.Code, c.Body)
	}
	if e.api("GET", "/api/v1/channels/billing", nil).Code != 200 {
		t.Fatal("the confirm page deleted the channel")
	}
	if w := b.post("/admin/channels/billing/delete"); w.Code != 200 || !strings.Contains(w.Body.String(), "Channel deleted") {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if e.api("GET", "/api/v1/channels/billing", nil).Code != 404 {
		t.Error("the channel is still there")
	}
	if !strings.Contains(logs.String(), "admin: channel deleted") {
		t.Errorf("no delete log line:\n%s", logs)
	}
	for _, p := range []string{"/admin/channels/billing/edit", "/admin/channels/billing/delete", "/admin/channels/none/edit", "/admin/channels/none/delete"} {
		if w := b.get(p); w.Code != 404 {
			t.Errorf("GET %s: %d", p, w.Code)
		}
	}
	for _, p := range []string{"/admin/channels/billing/edit", "/admin/channels/billing/delete", "/admin/channels/none/delete"} {
		if w := b.post(p); w.Code != 404 {
			t.Errorf("POST %s: %d", p, w.Code)
		}
	}
	// The id of a deleted channel can be used again.
	f = ok()
	f.Set("id", "billing")
	if w := b.postForm("/admin/channels/new", f); w.Code != 200 {
		t.Errorf("re-create: %d %s", w.Code, w.Body)
	}
	if strings.Contains(logs.String(), "render failed") {
		t.Errorf("a page did not render:\n%s", logs)
	}
}

func TestAdminGroups(t *testing.T) {
	logs := captureLog(t)
	e := newAdminEnv(t)
	b := e.browser().signIn()

	list := b.get("/admin/groups")
	if list.Code != 200 || !strings.Contains(list.Body.String(), "There are no groups yet") || !strings.Contains(list.Body.String(), iacNotice) {
		t.Fatalf("empty list: %d %s", list.Code, list.Body)
	}
	checkAdminHeaders(t, list, "groups")
	if w := b.get("/admin/groups/new"); w.Code != 200 || !strings.Contains(w.Body.String(), "People in the group") || strings.Contains(w.Body.String(), "Copy as API call") {
		t.Fatalf("new: %d %s", w.Code, w.Body)
	}

	// Names are trimmed, CRLF is not part of a name, blanks and repeats go.
	if w := b.postForm("/admin/groups/new", url.Values{"id": {" oncall "}, "members": {"alex\r\n  bob \r\n\r\nalex\r\n"}}); w.Code != 200 || !strings.Contains(w.Body.String(), "now has 2 people") {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	g := decode[groupView](t, e.api("GET", "/api/v1/groups/oncall", nil))
	if strings.Join(g.Members, ",") != "alex,bob" {
		t.Errorf("members: %q", g.Members)
	}
	if !strings.Contains(logs.String(), "admin: group created") || !strings.Contains(logs.String(), "id=oncall") {
		t.Errorf("no create log line:\n%s", logs)
	}
	if w := b.get("/admin/groups"); !strings.Contains(w.Body.String(), "2 people: alex, bob") || !strings.Contains(w.Body.String(), "/admin/groups/oncall/delete") {
		t.Errorf("list:\n%s", w.Body)
	}

	edit := b.get("/admin/groups/oncall/edit")
	if edit.Code != 200 || textarea(t, edit.Body.String(), "members") != "alex\nbob" || !strings.Contains(edit.Body.String(), iacNotice) {
		t.Fatalf("edit: %d\n%s", edit.Code, edit.Body)
	}
	curl, jsonBody := textarea(t, edit.Body.String(), "apicall-curl"), textarea(t, edit.Body.String(), "apicall-body")
	if !strings.HasPrefix(curl, "curl -X PUT 'https://hub.example.com/api/v1/groups/oncall'") || !strings.Contains(curl, "$KEY") || !strings.Contains(curl, "-d '"+jsonBody+"'") {
		t.Errorf("curl:\n%s", curl)
	}
	var body struct{ Members []string }
	if json.Unmarshal([]byte(jsonBody), &body) != nil || strings.Join(body.Members, ",") != "alex,bob" {
		t.Errorf("body: %s", jsonBody)
	}
	if w := b.postForm("/admin/groups/oncall/edit", url.Values{"id": {"other"}, "members": {"carol"}}); w.Code != 200 {
		t.Fatalf("edit save: %d %s", w.Code, w.Body)
	}
	if g := decode[groupView](t, e.api("GET", "/api/v1/groups/oncall", nil)); strings.Join(g.Members, ",") != "carol" {
		t.Errorf("after edit: %q", g.Members)
	}
	if !strings.Contains(logs.String(), "admin: group saved") {
		t.Errorf("no save log line:\n%s", logs)
	}
	if w := b.postForm("/admin/groups/oncall/edit", url.Values{"members": {""}}); w.Code != 200 {
		t.Errorf("an empty group is allowed: %d", w.Code)
	}

	// Errors.
	long := strings.Repeat("n", maxFieldLen+1)
	for name, tc := range map[string]struct {
		form url.Values
		path string
		want string
	}{
		"bad id":      {url.Values{"id": {"no spaces"}, "members": {"a"}}, "/admin/groups/new", "Use letters, numbers"},
		"existing id": {url.Values{"id": {"oncall"}, "members": {"a"}}, "/admin/groups/new", "already exists"},
		"long name":   {url.Values{"id": {"team"}, "members": {"a\n" + long}}, "/admin/groups/new", "is longer than 256 bytes"},
		"long edit":   {url.Values{"members": {long}}, "/admin/groups/oncall/edit", "is longer than 256 bytes"},
	} {
		w := b.postForm(tc.path, tc.form)
		if w.Code != 400 || !strings.Contains(w.Body.String(), tc.want) || !strings.Contains(w.Body.String(), "The group was not saved") {
			t.Errorf("%s: %d, want %q:\n%s", name, w.Code, tc.want, w.Body)
		}
	}
	if e.api("GET", "/api/v1/groups/team", nil).Code != 404 {
		t.Error("a refused form made a group")
	}
	// A big group fits in a form.
	var names []string
	for i := 0; i < 3000; i++ {
		names = append(names, fmt.Sprintf("member-%04d", i))
	}
	if w := b.postForm("/admin/groups/new", url.Values{"id": {"big"}, "members": {strings.Join(names, "\r\n")}}); w.Code != 200 || !strings.Contains(w.Body.String(), "now has 3000 people") {
		t.Errorf("big group: %d", w.Code)
	}

	// Delete says what happens to the channels that name the group.
	e.apiChannel("a-only", channelBody("Only channel", "active", map[string]any{"mode": "only", "groups": []string{"oncall"}}))
	e.apiChannel("b-except", channelBody("Except channel", "active", map[string]any{"mode": "except", "groups": []string{"oncall"}}))
	e.apiChannel("c-free", channelBody("Free channel", "active", nil))
	c := b.get("/admin/groups/oncall/delete")
	cb := c.Body.String()
	for _, want := range []string{"Delete oncall?", "Only channel", "lose access", "Except channel", "get access", "Feeds can name a group too", iacNotice} {
		if c.Code != 200 || !strings.Contains(cb, want) {
			t.Fatalf("confirm lacks %q: %d\n%s", want, c.Code, cb)
		}
	}
	if strings.Contains(cb, "Free channel") {
		t.Error("a channel that does not name the group is listed")
	}
	if e.api("GET", "/api/v1/groups/oncall", nil).Code != 200 {
		t.Fatal("the confirm page deleted the group")
	}
	if strings.Contains(logs.String(), "render failed") {
		t.Errorf("a page did not render:\n%s", logs)
	}
	if w := b.get("/admin/groups/big/delete"); !strings.Contains(w.Body.String(), "No channel uses this group") {
		t.Errorf("unused group:\n%s", w.Body)
	}
	if w := b.post("/admin/groups/oncall/delete"); w.Code != 200 || !strings.Contains(w.Body.String(), "Group deleted") {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if e.api("GET", "/api/v1/groups/oncall", nil).Code != 404 {
		t.Error("the group is still there")
	}
	if !strings.Contains(logs.String(), "admin: group deleted") {
		t.Errorf("no delete log line:\n%s", logs)
	}
	for _, p := range []string{"/admin/groups/oncall/edit", "/admin/groups/oncall/delete"} {
		if w := b.get(p); w.Code != 404 {
			t.Errorf("GET %s: %d", p, w.Code)
		}
		if w := b.post(p); w.Code != 404 {
			t.Errorf("POST %s: %d", p, w.Code)
		}
	}
}

var messageIDRe = regexp.MustCompile(`<code>(msg_[A-Za-z0-9_-]+)</code>`)

func TestAdminSend(t *testing.T) {
	logs := captureLog(t)
	e := newAdminEnv(t)
	b := e.browser().signIn()
	e.newDevice("phone").pair("alex")
	e.apiChannel("alerts", channelBody("Alerts", "critical", nil))
	e.apiChannel("pager", channelBody("Pager", "page", nil))
	e.apiChannel("private", channelBody("Private", "active", map[string]any{"mode": "only", "users": []string{"bob"}}))

	who := b.get("/admin/send")
	if who.Code != 200 || !strings.Contains(who.Body.String(), `<option value="alex">alex</option>`) || !strings.Contains(who.Body.String(), "Or type a name") {
		t.Fatalf("step one: %d\n%s", who.Code, who.Body)
	}
	checkAdminHeaders(t, who, "send")
	if w := b.get("/admin/send?who=&other="); w.Code != 400 || !strings.Contains(w.Body.String(), "Choose a person") {
		t.Errorf("nobody chosen: %d", w.Code)
	}

	step := b.get("/admin/send?who=alex&other=")
	sb := step.Body.String()
	if step.Code != 200 || !strings.Contains(sb, "Alerts (alerts), up to Critical") || !strings.Contains(sb, "Pager (pager), up to Page") || strings.Contains(sb, "Private") {
		t.Fatalf("step two lists the channels alex may use: %d\n%s", step.Code, sb)
	}
	if strings.Contains(sb, "No device is paired") {
		t.Error("alex has a device")
	}
	if w := b.get("/admin/send?who=alex&other=zed"); !strings.Contains(w.Body.String(), "No device is paired for zed") {
		t.Errorf("a name that is not paired:\n%s", w.Body)
	}

	send := func(kv ...string) (*httptest.ResponseRecorder, string) {
		form := url.Values{"person": {"alex"}, "channel": {"alerts"}, "level": {""}, "title": {"Disk full"}, "body": {"db-1 is at 98%"}}
		for i := 0; i < len(kv); i += 2 {
			form.Set(kv[i], kv[i+1])
		}
		w := b.postForm("/admin/send", form)
		if m := messageIDRe.FindStringSubmatch(w.Body.String()); m != nil {
			return w, m[1]
		}
		return w, ""
	}

	// The happy path: the status page shows what GET /api/v1/messages/{id} returns.
	w, id := send("level", "time-sensitive", "data", `{"name": "Ada"}`)
	if w.Code != 200 || id == "" || !strings.Contains(w.Body.String(), "Message sent") || !strings.Contains(w.Body.String(), "Time-sensitive") {
		t.Fatalf("send: %d\n%s", w.Code, w.Body)
	}
	checkAdminHeaders(t, w, "sent")
	api := decode[struct {
		Status, Level string
		Attempts      int
		Deliveries    []deliveryView
	}](t, e.api("GET", "/api/v1/messages/"+id, nil))
	if api.Status != "queued" || api.Level != "time-sensitive" || len(api.Deliveries) != 1 {
		t.Fatalf("api view: %+v", api)
	}
	if err := e.worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(e.relay.pushes()) != 1 {
		t.Errorf("pushes: %d", len(e.relay.pushes()))
	}
	st := b.get("/admin/messages/" + id)
	for _, want := range []string{id, "delivered: Sent to the person", "Time-sensitive", "Repeats so far", api.Deliveries[0].DeviceID, "sent", `href="/admin/messages/` + id + `">Refresh`} {
		if st.Code != 200 || !strings.Contains(st.Body.String(), want) {
			t.Fatalf("status lacks %q: %d\n%s", want, st.Code, st.Body)
		}
	}
	checkAdminHeaders(t, st, "status")
	if !strings.Contains(logs.String(), "admin: test message sent") || !strings.Contains(logs.String(), "message_id="+id) ||
		strings.Contains(logs.String(), "Disk full") || strings.Contains(logs.String(), "98%") || strings.Contains(logs.String(), "Ada") {
		t.Errorf("log:\n%s", logs)
	}

	// The channel's highest level applies, and the page says so.
	w, id = send("level", "page")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "You asked for Page, but this channel never goes above Critical, so it was sent as Critical.") {
		t.Errorf("clamped: %d\n%s", w.Code, w.Body)
	}
	if m, _ := e.st.GetMessage(id); m.Level != "critical" {
		t.Errorf("level: %s", m.Level)
	}
	// The channel's usual level when none is asked for.
	if w, id = send(); w.Code != 200 {
		t.Fatalf("usual level: %d", w.Code)
	} else if m, _ := e.st.GetMessage(id); m.Level != "active" {
		t.Errorf("level: %s", m.Level)
	}

	// A page: the defaults, then the admin's own numbers.
	w, id = send("channel", "pager", "level", "page")
	if m, err := e.st.GetMessage(id); err != nil || w.Code != 200 || m.Level != "page" || m.PageIntervalS != 120 || m.PageMaxRetries != 5 {
		t.Errorf("page defaults: %d %+v %v", w.Code, m, err)
	}
	w, id = send("channel", "pager", "level", "page", "interval", "30", "retries", "2")
	if m, err := e.st.GetMessage(id); err != nil || w.Code != 200 || m.PageIntervalS != 30 || m.PageMaxRetries != 2 {
		t.Errorf("page numbers: %d %+v %v", w.Code, m, err)
	}

	// Errors are shown next to the fields and nothing is sent.
	before := len(e.events("message.undeliverable"))
	for name, tc := range map[string]struct {
		kv   []string
		want string
	}{
		"no title":      {[]string{"title", " "}, "Enter a title."},
		"no body":       {[]string{"body", ""}, "Enter the message text."},
		"no channel":    {[]string{"channel", ""}, "Choose a channel."},
		"gone channel":  {[]string{"channel", "ghost"}, "does not exist any more"},
		"no access":     {[]string{"channel", "private"}, "alex may not use this channel"},
		"bad level":     {[]string{"level", "loud"}, "Choose one of the levels."},
		"bad json":      {[]string{"data", "{nope"}, "This must be a JSON object"},
		"json array":    {[]string{"data", "[1]"}, "This must be a JSON object"},
		"bad interval":  {[]string{"channel", "pager", "level", "page", "interval", "soon"}, "Enter a whole number"},
		"zero interval": {[]string{"channel", "pager", "level", "page", "interval", "0"}, "from 1 to 86400"},
		"many retries":  {[]string{"channel", "pager", "level", "page", "retries", "101"}, "from 0 to 100"},
	} {
		w, id := send(tc.kv...)
		if w.Code != 400 || id != "" || !strings.Contains(w.Body.String(), tc.want) || !strings.Contains(w.Body.String(), "The message was not sent") {
			t.Errorf("%s: %d, want %q:\n%s", name, w.Code, tc.want, w.Body)
		}
	}
	if w, _ := send("title", ""); !strings.Contains(w.Body.String(), `value="db-1 is at 98%"`) && !strings.Contains(w.Body.String(), "db-1 is at 98%") {
		t.Error("the form lost what was typed")
	}
	if n := len(e.events("message.undeliverable")); n != before {
		t.Errorf("a refused form sent %d messages", n-before)
	}

	// Someone who has not paired gets a message that cannot be delivered, and the page says so.
	w, id = send("person", "zed")
	if w.Code != 200 || id == "" || !strings.Contains(w.Body.String(), "undeliverable: No device could be reached") {
		t.Errorf("unpaired: %d\n%s", w.Code, w.Body)
	}
	if w := b.get("/admin/messages/msg_nope"); w.Code != 404 || !strings.Contains(w.Body.String(), "Message not found") {
		t.Errorf("unknown message: %d", w.Code)
	}
	// A person who may use no channel.
	if w := b.get("/admin/send?other=bob"); !strings.Contains(w.Body.String(), "Private (private)") {
		t.Errorf("bob may use the private channel:\n%s", w.Body)
	}
	e.apiChannel("alerts", channelBody("Alerts", "critical", map[string]any{"mode": "only", "users": []string{"bob"}}))
	e.apiChannel("pager", channelBody("Pager", "page", map[string]any{"mode": "only", "users": []string{"bob"}}))
	if w := b.get("/admin/send?who=alex"); !strings.Contains(w.Body.String(), "alex cannot use any channel yet") {
		t.Errorf("no channels:\n%s", w.Body)
	}
	if strings.Contains(logs.String(), "render failed") {
		t.Errorf("a page did not render:\n%s", logs)
	}
}

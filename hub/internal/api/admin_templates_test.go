package api

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"html"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/recodive/hansho/hub/internal/config"
	"github.com/recodive/hansho/hub/internal/identity"
	"github.com/recodive/hansho/hub/internal/template"
)

const notDeletable = "Yes, delete it"

func TestAdminStartersAreValid(t *testing.T) {
	if len(adminStarters) == 0 {
		t.Fatal("no starters")
	}
	for _, st := range adminStarters {
		if err := template.Validate(json.RawMessage(st.Template)); err != nil {
			t.Errorf("starter %s: %v", st.ID, err)
		}
		if !json.Valid([]byte(st.Data)) {
			t.Errorf("starter %s: its data is not JSON", st.ID)
		}
	}
}

// TestAdminAIPromptCoversTheValidator fails when a component the validator knows is missing from the prompt.
func TestAdminAIPromptCoversTheValidator(t *testing.T) {
	prompt := adminAIPrompt("", "")
	rows := template.Reference()
	if len(rows) < 8 {
		t.Fatalf("only %d reference rows", len(rows))
	}
	for _, r := range rows {
		if !strings.Contains(prompt, "- "+r[0]+": "+r[1]+"\n") {
			t.Errorf("the prompt lacks the reference row %q", r[0])
		}
	}
	described := map[string]bool{}
	for _, r := range rows {
		for _, n := range strings.Split(r[0], ", ") {
			described[n] = true
		}
	}
	for _, name := range []string{"vstack", "hstack", "spacer", "text", "symbol", "image", "progress", "timer", "gauge", "badge", "foreach", "divider"} {
		if !described[name] {
			t.Errorf("the prompt does not describe %s", name)
		}
	}
	if strings.Contains(prompt, "My current template") || strings.Contains(prompt, "rejects my current template") {
		t.Errorf("an empty editor and no error must not appear in the prompt:\n%s", prompt)
	}
	full := adminAIPrompt("  {\"type\":\"text\"}\n", "body.type: nope")
	for _, want := range []string{"My current template:\n```json\n{\"type\":\"text\"}\n```", "The hub rejects my current template with: body.type: nope\nFix that and keep everything else.", "What I want: <describe the template here>"} {
		if !strings.Contains(full, want) {
			t.Errorf("prompt lacks %q:\n%s", want, full)
		}
	}
}

func TestAdminTemplates(t *testing.T) {
	logs := captureLog(t)
	e := newEnvCfg(t, func(c *config.Config) {
		c.Admin, c.AdminPassword, c.HomeURL = true, adminPW, "https://home.example.org"
	})
	b := e.browser().signIn()

	list := b.get("/admin/templates")
	if list.Code != 200 || !strings.Contains(list.Body.String(), "There are no templates yet") || !strings.Contains(list.Body.String(), iacNotice) {
		t.Fatalf("empty list: %d %s", list.Code, list.Body)
	}
	checkAdminHeaders(t, list, "templates")

	form := b.get("/admin/templates/new")
	fb := form.Body.String()
	for _, want := range []string{"Ask an AI to write it", "Prompt to copy", "Deploys list", "Order progress", "Use this example", "Check without saving",
		"Preview it in the template builder", "see how it looks on a phone", `href="https://home.example.org/templates"`, "Nothing is sent from this hub"} {
		if form.Code != 200 || !strings.Contains(fb, want) {
			t.Fatalf("new form lacks %q: %d\n%s", want, form.Code, fb)
		}
	}
	if strings.Contains(fb, "Copy as API call") {
		t.Error("a template that does not exist yet has no API call")
	}
	if p := textarea(t, fb, "ai-prompt"); !strings.Contains(p, "vstack, hstack:") || !strings.Contains(p, "What I want:") {
		t.Errorf("prompt:\n%s", p)
	}

	// A starter fills the editor and the prompt.
	if w := b.postForm("/admin/templates/new", url.Values{"id": {"deploys"}, "starter": {"order"}, "load": {"1"}}); w.Code != 200 ||
		textarea(t, w.Body.String(), "body") != strings.TrimSpace(adminStarters[1].Template) ||
		!strings.Contains(textarea(t, w.Body.String(), "ai-prompt"), "My current template:") || !strings.Contains(w.Body.String(), `value="deploys"`) {
		t.Fatalf("starter: %d\n%s", w.Code, w.Body)
	}

	// Problems are shown with their path and put in the prompt; nothing is stored.
	bad := b.postForm("/admin/templates/new", url.Values{"id": {"deploys"}, "body": {`{"type":"vstack","children":[{"type":"web"}]}`}})
	if bad.Code != 400 || !strings.Contains(html2(bad), "The template is not valid. First problem: <code>body.children[0].type: unknown component web</code>") {
		t.Fatalf("invalid template: %d\n%s", bad.Code, bad.Body)
	}
	if p := textarea(t, bad.Body.String(), "ai-prompt"); !strings.Contains(p, "The hub rejects my current template with: body.children[0].type: unknown component web") ||
		!strings.Contains(p, `"type":"web"`) {
		t.Errorf("prompt after an error:\n%s", p)
	}
	for body, want := range map[string]string{
		"":                  "Write or paste the template first.",
		"{not json":         "body: this is not valid JSON (the hub stopped reading at character",
		`{"type":"vstack"}`: "body.children: containers require a children array",
		`[1]`:               "body: node must be an object",
	} {
		w := b.postForm("/admin/templates/new", url.Values{"id": {"x"}, "body": {body}})
		if w.Code != 400 || !strings.Contains(html2(w), want) {
			t.Errorf("body %q: %d, want %q\n%s", body, w.Code, want, w.Body)
		}
	}
	for _, id := range []string{"", "has space", "-lead", strings.Repeat("a", 65)} {
		if w := b.postForm("/admin/templates/new", url.Values{"id": {id}, "body": {adminStarters[0].Template}}); w.Code != 400 || !strings.Contains(w.Body.String(), "Use letters, numbers") {
			t.Errorf("id %q: %d", id, w.Code)
		}
	}
	if w := e.api("GET", "/api/v1/templates", nil); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("a refused form stored something: %s", w.Body)
	}

	// Check only: valid, not saved.
	if w := b.postForm("/admin/templates/new", url.Values{"id": {"deploys"}, "body": {adminStarters[0].Template}, "check": {"1"}}); w.Code != 200 ||
		!strings.Contains(w.Body.String(), "This template is valid. It is not saved yet.") {
		t.Fatalf("check: %d\n%s", w.Code, w.Body)
	}
	if w := e.api("GET", "/api/v1/templates/deploys", nil); w.Code != 404 {
		t.Fatalf("a check stored the template: %d", w.Code)
	}

	// Create.
	created := b.postForm("/admin/templates/new", url.Values{"id": {" deploys "}, "body": {strings.ReplaceAll(adminStarters[0].Template, "\n", "\r\n")}})
	if created.Code != 200 || !strings.Contains(created.Body.String(), "Template saved") || !strings.Contains(created.Body.String(), "version 1") {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	got := decode[templateView](t, e.api("GET", "/api/v1/templates/deploys", nil))
	if got.Version != 1 || string(got.Body) != string(adminCompact([]byte(adminStarters[0].Template))) {
		t.Errorf("stored: %d %s", got.Version, got.Body)
	}
	if !strings.Contains(logs.String(), "admin: template saved") || !strings.Contains(logs.String(), "id=deploys") || !strings.Contains(logs.String(), "version=1") {
		t.Errorf("no log line:\n%s", logs)
	}
	if w := b.postForm("/admin/templates/new", url.Values{"id": {"deploys"}, "body": {adminStarters[1].Template}}); w.Code != 400 || !strings.Contains(w.Body.String(), "already exists") {
		t.Errorf("a second template with the id: %d", w.Code)
	}

	// Edit: indented, the version, and the API call with the next version, which the API accepts.
	edit := b.get("/admin/templates/deploys/edit")
	eb := edit.Body.String()
	for _, want := range []string{iacNotice, "This template's id is", "It is at version 1", "Copy as API call", "uses the next one"} {
		if edit.Code != 200 || !strings.Contains(eb, want) {
			t.Fatalf("edit lacks %q: %d\n%s", want, edit.Code, eb)
		}
	}
	if body := textarea(t, eb, "body"); !strings.Contains(body, "\n  \"type\": \"vstack\"") || !json.Valid([]byte(body)) {
		t.Errorf("editor text:\n%s", body)
	}
	curl, jsonBody := textarea(t, eb, "apicall-curl"), textarea(t, eb, "apicall-body")
	if !strings.HasPrefix(curl, "curl -X PUT 'https://hub.example.com/api/v1/templates/deploys'") || !strings.Contains(curl, "-d '"+jsonBody+"'") || !strings.Contains(jsonBody, `"version": 2`) {
		t.Errorf("curl:\n%s\nbody:\n%s", curl, jsonBody)
	}
	if w := e.api("PUT", "/api/v1/templates/deploys", jsonBody); w.Code != 200 {
		t.Errorf("the API call is not accepted: %d %s", w.Code, w.Body)
	}
	if v := decode[templateView](t, e.api("GET", "/api/v1/templates/deploys", nil)).Version; v != 2 {
		t.Fatalf("version after the API call: %d", v)
	}

	// The same template, laid out differently, is no change: the version stays.
	same := b.postForm("/admin/templates/deploys/edit", url.Values{"body": {textarea(t, eb, "body")}})
	if same.Code != 200 || !strings.Contains(same.Body.String(), "Nothing to save") || !strings.Contains(same.Body.String(), "version 2") {
		t.Fatalf("unchanged save: %d %s", same.Code, same.Body)
	}
	// A change makes the next version, also when the API raised it meanwhile.
	changed := strings.Replace(adminStarters[0].Template, "headline", "title", 1)
	if w := b.postForm("/admin/templates/deploys/edit", url.Values{"id": {"other"}, "body": {changed}}); w.Code != 200 || !strings.Contains(w.Body.String(), "version 3") {
		t.Fatalf("changed save: %d %s", w.Code, w.Body)
	}
	if v := decode[templateView](t, e.api("GET", "/api/v1/templates/deploys", nil)); v.Version != 3 || !strings.Contains(string(v.Body), `"style":"title"`) {
		t.Errorf("after the change: %d %s", v.Version, v.Body)
	}
	if w := e.api("GET", "/api/v1/templates/other", nil); w.Code != 404 {
		t.Error("the id in the form of an edit must not make another template")
	}
	// A bad edit changes nothing.
	if w := b.postForm("/admin/templates/deploys/edit", url.Values{"body": {`{"type":"text","text":5}`}}); w.Code != 400 || !strings.Contains(html2(w), "body.text: must be a string") {
		t.Errorf("bad edit: %d %s", w.Code, w.Body)
	}
	if v := decode[templateView](t, e.api("GET", "/api/v1/templates/deploys", nil)).Version; v != 3 {
		t.Errorf("version after a bad edit: %d", v)
	}
	if w := b.get("/admin/templates/nope/edit"); w.Code != 404 {
		t.Errorf("unknown template: %d", w.Code)
	}
	if w := b.postForm("/admin/templates/nope/edit", url.Values{"body": {changed}}); w.Code != 404 {
		t.Errorf("save of an unknown template: %d", w.Code)
	}

	// The list.
	e.apiChannel("billing", map[string]any{"name": "Billing", "default_level": "active", "max_level": "active", "template_id": "deploys"})
	list = b.get("/admin/templates")
	for _, want := range []string{"deploys", "Version 3, 11 parts", "Used by 1 channel", "/admin/templates/deploys/edit", "/admin/templates/deploys/delete", iacNotice} {
		if !strings.Contains(list.Body.String(), want) {
			t.Errorf("list lacks %q:\n%s", want, list.Body)
		}
	}

	// Delete: the page names the channels and offers no button while one uses it; the store refuses a direct post.
	del := b.get("/admin/templates/deploys/delete")
	if del.Code != 200 || !strings.Contains(del.Body.String(), "/admin/channels/billing/edit") || strings.Contains(del.Body.String(), notDeletable) {
		t.Fatalf("delete page of a template in use: %d\n%s", del.Code, del.Body)
	}
	if w := b.post("/admin/templates/deploys/delete"); w.Code != 409 || !strings.Contains(w.Body.String(), "/admin/channels/billing/edit") || strings.Contains(w.Body.String(), notDeletable) {
		t.Fatalf("delete of a template in use: %d\n%s", w.Code, w.Body)
	}
	if w := e.api("GET", "/api/v1/templates/deploys", nil); w.Code != 200 {
		t.Fatalf("the template went: %d", w.Code)
	}
	e.apiChannel("billing", map[string]any{"name": "Billing", "default_level": "active", "max_level": "active"})
	del = b.get("/admin/templates/deploys/delete")
	if del.Code != 200 || !strings.Contains(del.Body.String(), notDeletable) || !strings.Contains(del.Body.String(), iacNotice) {
		t.Fatalf("delete page: %d\n%s", del.Code, del.Body)
	}
	if w := b.post("/admin/templates/deploys/delete"); w.Code != 200 || !strings.Contains(w.Body.String(), "Template deleted") {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if w := e.api("GET", "/api/v1/templates/deploys", nil); w.Code != 404 {
		t.Errorf("after delete: %d", w.Code)
	}
	if !strings.Contains(logs.String(), "admin: template deleted") {
		t.Errorf("no delete log line:\n%s", logs)
	}
	if w := b.post("/admin/templates/deploys/delete"); w.Code != 404 {
		t.Errorf("second delete: %d", w.Code)
	}
	if w := b.get("/admin/templates/deploys/delete"); w.Code != 404 {
		t.Errorf("delete page of a deleted template: %d", w.Code)
	}
}

// html2 is the page with its character references decoded, for text that holds quotes or angle brackets.
func html2(w *httptest.ResponseRecorder) string { return html.UnescapeString(w.Body.String()) }

// upload sends a multipart form with the session's token and a file.
func (b *browser) upload(path string, fields map[string]string, filename string, data []byte, hdr map[string]string) *httptest.ResponseRecorder {
	b.e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("csrf", b.csrf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	if filename != "" {
		fw, _ := mw.CreateFormFile("file", filename)
		fw.Write(data)
	}
	mw.Close()
	req := httptest.NewRequest("POST", path, &buf)
	req.RemoteAddr = b.addr
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", "https://hub.example.com")
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	req.AddCookie(b.cookie)
	w := httptest.NewRecorder()
	b.e.h.ServeHTTP(w, req)
	return w
}

func TestAdminSounds(t *testing.T) {
	logs := captureLog(t)
	e := newAdminEnv(t)
	b := e.browser().signIn()

	list := b.get("/admin/sounds")
	for _, want := range []string{"There are no sounds yet", iacNotice, "Upload a sound", `enctype="multipart/form-data"`, `type="file"`, "at most 1 MB", "shorter than 30 seconds"} {
		if list.Code != 200 || !strings.Contains(list.Body.String(), want) {
			t.Fatalf("empty list lacks %q: %d\n%s", want, list.Code, list.Body)
		}
	}
	checkAdminHeaders(t, list, "sounds")

	// The same files and verdicts as the API's PUT.
	wav, long := wavBytes(2), wavBytes(31)
	for name, tc := range map[string]struct {
		file string
		data []byte
		ct   string
		code int
		want string
	}{
		"wav":          {"chime.wav", wav, "audio/wav", 200, "Sound saved"},
		"sniffed":      {"noext", wavBytes(1), "audio/wav", 200, "Sound saved"},
		"aiff":         {"a.aif", aiffBytes(8000*10, 100), "audio/aiff", 200, "Sound saved"},
		"caf":          {"c.caf", cafBytes(), "audio/x-caf", 200, "Sound saved"},
		"too long":     {"long.wav", long, "audio/wav", 400, "30 seconds or longer"},
		"broken wav":   {"bad.wav", []byte("RIFF....WAVE nonsense"), "audio/wav", 400, "valid WAV file"},
		"broken caf":   {"bad.caf", []byte("not a caf file"), "audio/x-caf", 400, "valid CAF file"},
		"an mp3":       {"song.mp3", []byte("ID3\x03\x00data"), "audio/mpeg", 400, "not a CAF, WAV or AIFF file"},
		"over 1 MB":    {"big.wav", append(wavBytes(1), make([]byte, 1<<20)...), "audio/wav", 400, "larger than 1 MB"},
		"far over 1MB": {"huge.wav", make([]byte, 3<<20), "audio/wav", 413, "too large"},
	} {
		id := "s-" + strings.ReplaceAll(name, " ", "-")
		w := b.upload("/admin/sounds", map[string]string{"id": id}, tc.file, tc.data, nil)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s: %d, want %d with %q\n%s", name, w.Code, tc.code, tc.want, w.Body)
		}
		api := e.putSound("api-"+id, tc.ct, tc.data)
		if (api.Code == 200) != (tc.code == 200) {
			t.Errorf("%s: the admin says %d, the API %d %s", name, tc.code, api.Code, api.Body)
		}
	}
	if w := b.upload("/admin/sounds", map[string]string{"id": "has space"}, "a.wav", wav, nil); w.Code != 400 || !strings.Contains(w.Body.String(), "Use letters, numbers") ||
		!strings.Contains(w.Body.String(), `value="has space"`) {
		t.Errorf("bad id: %d\n%s", w.Code, w.Body)
	}
	if w := b.upload("/admin/sounds", map[string]string{"id": "nofile"}, "", nil, nil); w.Code != 400 || !strings.Contains(w.Body.String(), "Choose a sound file.") {
		t.Errorf("no file: %d\n%s", w.Code, w.Body)
	}
	if w := b.post("/admin/sounds", "id", "plainform"); w.Code != 400 || !strings.Contains(w.Body.String(), "Choose a sound file.") {
		t.Errorf("a form without a file: %d", w.Code)
	}
	if w := e.api("GET", "/api/v1/sounds/s-too-long", nil); w.Code != 404 {
		t.Errorf("a refused sound was stored: %d", w.Code)
	}
	if !strings.Contains(logs.String(), "admin: sound saved") || !strings.Contains(logs.String(), "id=s-wav") {
		t.Errorf("no log line:\n%s", logs)
	}

	// The list: id, type, size, short fingerprint, use.
	snd := decode[soundView](t, e.api("GET", "/api/v1/sounds/s-wav", nil))
	if snd.ContentType != "audio/wav" || snd.Bytes != len(wav) {
		t.Fatalf("stored: %+v", snd)
	}
	e.apiChannel("billing", map[string]any{"name": "Billing", "default_level": "active", "max_level": "active", "sound": "s-wav"})
	list = b.get("/admin/sounds")
	for _, want := range []string{"<h2>s-wav</h2>", "WAV file, 31.3 KB", "AIFF file", "CAF file", "<code>" + snd.SHA256[:12] + "</code>", "Used by 1 channel", "/admin/sounds/s-wav/delete"} {
		if !strings.Contains(list.Body.String(), want) {
			t.Errorf("list lacks %q:\n%s", want, list.Body)
		}
	}
	if strings.Contains(list.Body.String(), snd.SHA256) {
		t.Error("the list shows the whole hash")
	}

	// Delete: confirmation, and the in-use rule from the store.
	del := b.get("/admin/sounds/s-wav/delete")
	if del.Code != 200 || !strings.Contains(del.Body.String(), "/admin/channels/billing/edit") || strings.Contains(del.Body.String(), notDeletable) {
		t.Fatalf("delete page of a sound in use: %d\n%s", del.Code, del.Body)
	}
	if w := b.post("/admin/sounds/s-wav/delete"); w.Code != 409 || !strings.Contains(w.Body.String(), "/admin/channels/billing/edit") {
		t.Fatalf("delete of a sound in use: %d\n%s", w.Code, w.Body)
	}
	e.apiChannel("billing", map[string]any{"name": "Billing", "default_level": "active", "max_level": "active"})
	if del = b.get("/admin/sounds/s-wav/delete"); del.Code != 200 || !strings.Contains(del.Body.String(), notDeletable) {
		t.Fatalf("delete page: %d\n%s", del.Code, del.Body)
	}
	if w := b.post("/admin/sounds/s-wav/delete"); w.Code != 200 || !strings.Contains(w.Body.String(), "Sound deleted") {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if w := e.api("GET", "/api/v1/sounds/s-wav", nil); w.Code != 404 {
		t.Errorf("after delete: %d", w.Code)
	}
	if !strings.Contains(logs.String(), "admin: sound deleted") {
		t.Errorf("no delete log line:\n%s", logs)
	}
	if w := b.get("/admin/sounds/s-wav/delete"); w.Code != 404 {
		t.Errorf("delete page of a deleted sound: %d", w.Code)
	}
	if w := b.post("/admin/sounds/s-wav/delete"); w.Code != 404 {
		t.Errorf("second delete: %d", w.Code)
	}
}

func TestAdminUploadChecksOriginAndToken(t *testing.T) {
	e := newAdminEnv(t)
	b := e.browser().signIn()
	good := map[string]string{"id": "chime"}
	for name, tc := range map[string]struct {
		hdr  map[string]string
		csrf string
	}{
		"other origin": {map[string]string{"Origin": "https://evil.example"}, b.csrf},
		"no origin":    {map[string]string{"Origin": ""}, b.csrf},
		"wrong token":  {nil, "nope"},
		"no token":     {nil, ""},
	} {
		saved := b.csrf
		b.csrf = tc.csrf
		w := b.upload("/admin/sounds", good, "chime.wav", wavBytes(1), tc.hdr)
		b.csrf = saved
		if w.Code != 403 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if w := e.api("GET", "/api/v1/sounds/chime", nil); w.Code != 404 {
		t.Fatalf("a refused upload was stored: %d", w.Code)
	}
	if w := b.upload("/admin/sounds", good, "chime.wav", wavBytes(1), nil); w.Code != 200 {
		t.Fatalf("the same upload with the token: %d %s", w.Code, w.Body)
	}
	// Without a session an upload does not even get read.
	anon := e.browser()
	anon.csrf = b.csrf
	anon.cookie = &http.Cookie{Name: adminCookie, Value: "x.y"}
	if w := anon.upload("/admin/sounds", good, "chime.wav", wavBytes(1), nil); w.Code != 303 || w.Header().Get("Location") != "/admin/login" {
		t.Errorf("upload without a session: %d", w.Code)
	}
}

func TestAdminFeeds(t *testing.T) {
	e := newAdminEnv(t)
	b := e.browser().signIn()

	empty := b.get("/admin/feeds")
	for _, want := range []string{"has not published any feeds yet", "published by your own software through the API"} {
		if empty.Code != 200 || !strings.Contains(empty.Body.String(), want) {
			t.Fatalf("empty list lacks %q: %d\n%s", want, empty.Code, empty.Body)
		}
	}
	checkAdminHeaders(t, empty, "feeds")
	if strings.Contains(empty.Body.String(), `<form method="post" action="/admin/feeds`) {
		t.Error("the feeds list is read-only")
	}

	e.apiChannel("ops", map[string]any{"name": "Ops", "default_level": "active", "max_level": "active",
		"audience": map[string]any{"mode": "only", "users": []string{"alex"}}})
	if w := e.api("PUT", "/api/v1/templates/card", map[string]any{"version": 1, "body": map[string]any{"type": "vstack", "children": []any{map[string]any{"type": "text", "text": "{{v}}"}}}}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	for id, body := range map[string]map[string]any{
		"status": {"title": "Service status", "template_id": "card", "channel": "ops", "data": map[string]any{"v": "TOPSECRETDATA"}},
		"alpha":  {"data": map[string]any{"v": 1}},
		"mine":   {"external_user_id": "sam", "title": "Sam's", "audience": map[string]any{"mode": "except", "users": []string{"bob"}}, "data": map[string]any{"v": 2}},
		"own":    {"audience": map[string]any{"mode": "except", "users": []string{"bob"}}, "data": map[string]any{"v": 3}},
	} {
		if w := e.api("PUT", "/api/v1/feeds/"+id, body); w.Code != 202 {
			t.Fatalf("PUT feed %s: %d %s", id, w.Code, w.Body)
		}
	}
	list := b.get("/admin/feeds")
	lb := html2(list)
	for _, want := range []string{
		"<td><code>status</code></td><td>Service status</td><td>card</td><td>Only alex (from channel ops)</td><td>",
		"<td><code>alpha</code></td><td>Not named</td><td>Automatic layout</td><td>Everyone</td>",
		"<td><code>mine</code></td><td>Sam's</td><td>Automatic layout</td><td>Only sam</td>",
		"<td><code>own</code></td><td>Not named</td><td>Automatic layout</td><td>Everyone except bob</td>",
	} {
		if !strings.Contains(lb, want) {
			t.Errorf("list lacks %q:\n%s", want, lb)
		}
	}
	if strings.Contains(lb, "TOPSECRETDATA") {
		t.Error("the list shows feed data")
	}
	if strings.Index(lb, "<code>alpha</code>") > strings.Index(lb, "<code>status</code>") {
		t.Error("feeds are not in id order")
	}
}

func TestAdminSettings(t *testing.T) {
	const secret = "whsec-do-not-show"
	e := newEnvCfg(t, func(c *config.Config) {
		c.Admin, c.AdminPassword, c.HubID = true, adminPW, "hub_abc123"
		c.WebhookURL, c.WebhookSecret, c.ChallengeToken = "https://user:pw@backend.example.com:8443/hooks/secret-path?token=abc", []byte(secret), "challenge-token-xyz"
		c.RelayURL, c.HomeURL, c.LAEncrypt = "https://relay.example.net", "https://home.example.org", false
		c.Logo = bytes.Repeat([]byte{1}, 2048)
	})
	b := e.browser().signIn()
	w := b.get("/admin/settings")
	page := html2(w)
	checkAdminHeaders(t, w, "settings")
	for _, want := range []string{
		"These are set where the hub is deployed (environment variables or Helm values). Ask whoever manages the hub to change them.",
		"<dd>Example Hub</dd>", "<code>HUB_DISPLAY_NAME</code>",
		"<dd>https://hub.example.com</dd>", "<code>HUB_PUBLIC_URL</code>",
		"<dd>hub_abc123</dd>", "<code>HUB_ID</code>",
		"<dd>https://relay.example.net</dd>", "<code>HUB_RELAY_URL</code>",
		"<dd>https://home.example.org</dd>", "<code>HUB_HOME_URL</code>",
		"Set (PNG, 2.0 KB)", `<img class="logo" src="/_hub/logo.png"`, "<code>HUB_LOGO_FILE</code> or <code>HUB_LOGO_URL</code>",
		"<dd>backend.example.com</dd>", "<code>HUB_WEBHOOK_URL</code>", "<dd>Set (never shown)</dd>", "<code>HUB_WEBHOOK_SECRET</code>",
		"Encrypted Live Activities", "<dd>Off</dd>", "<code>HUB_LA_ENCRYPT</code>",
		"Admin pages", "<dd>On</dd>", "<code>HUB_ADMIN</code> or <code>HUB_ADMIN_PASSWORD</code>",
		"Pairing fingerprint", "Build",
	} {
		if w.Code != 200 || !strings.Contains(page, want) {
			t.Errorf("settings lack %q:\n%s", want, page)
		}
	}
	fp := strings.Join(groups(identity.Fingerprint(e.hubKey.Public().(ed25519.PublicKey))), " ")
	if !strings.Contains(page, "<dd>"+fp+"</dd>") {
		t.Errorf("the fingerprint is not shown: want %q", fp)
	}
	// Nothing secret is on the page.
	for _, leak := range []string{secret, adminPW, "challenge-token-xyz", "user:pw", "secret-path", "token=abc", "8443", string(e.cfg.MasterKey),
		base64.StdEncoding.EncodeToString(e.cfg.MasterKey), e.apiKey, b.cookie.Value} {
		if strings.Contains(page, leak) {
			t.Errorf("the settings page shows %q", leak)
		}
	}

	// A hub without a logo, webhook or id.
	bare := newEnvCfg(t, func(c *config.Config) {
		c.Admin, c.AdminPassword, c.WebhookURL, c.WebhookSecret, c.LAEncrypt = true, adminPW, "", nil, true
	}).browser()
	bare.e.t.Helper()
	page = html2(bare.signIn().get("/admin/settings"))
	for _, want := range []string{"<dd>Not set</dd>", "<dd>On</dd>"} {
		if !strings.Contains(page, want) {
			t.Errorf("bare settings lack %q", want)
		}
	}
	if strings.Contains(page, "logo.png") {
		t.Error("a logo preview without a logo")
	}
}

// TestAdminNewPagesLoad opens every new page once, signed in, with the security headers.
func TestAdminNewPagesLoad(t *testing.T) {
	e := newAdminEnv(t)
	b := e.browser().signIn()
	for _, p := range []string{"/admin/templates", "/admin/templates/new", "/admin/sounds", "/admin/feeds", "/admin/settings"} {
		w := b.get(p)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `<main id="main">`) {
			t.Errorf("GET %s: %d", p, w.Code)
		}
		checkAdminHeaders(t, w, p)
	}
	home := b.get("/admin/").Body.String()
	for _, p := range []string{"/admin/templates", "/admin/sounds", "/admin/feeds", "/admin/settings"} {
		if strings.Count(home, `href="`+p+`"`) != 2 { // the navigation and the card
			t.Errorf("home links to %s %d times", p, strings.Count(home, `href="`+p+`"`))
		}
	}
}

package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const goodTemplate = `{"type":"vstack","tint":{"light":"#0A84FF","dark":"#64D2FF"},"children":[{"type":"text","style":"headline","text":"{{title}}"},{"type":"spacer"}]}`

func putChannel(e *env, id string, body map[string]any) *httpResp {
	w := e.api("PUT", "/api/v1/channels/"+id, body)
	return &httpResp{w.Code, w.Body.String()}
}

type httpResp struct {
	Code int
	Body string
}

func okChannel() map[string]any {
	return map[string]any{"name": "Ops", "default_level": "active", "max_level": "critical"}
}

func TestChannelCRUD(t *testing.T) {
	e := newEnv(t)
	if r := putChannel(e, "ops", okChannel()); r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	w := e.api("GET", "/api/v1/channels/ops", nil)
	ch := decode[struct {
		ID, Name, Thread       string
		DefaultLevel, MaxLevel string `json:"-"`
		DL                     string `json:"default_level"`
		ML                     string `json:"max_level"`
		Actions                []any  `json:"actions"`
	}](t, w)
	if w.Code != 200 || ch.ID != "ops" || ch.Name != "Ops" || ch.DL != "active" || ch.ML != "critical" || ch.Thread != "ops" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := e.api("DELETE", "/api/v1/channels/ops", nil); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := e.api("GET", "/api/v1/channels/ops", nil); w.Code != 404 {
		t.Fatalf("get deleted: %d", w.Code)
	}
	if w := e.api("DELETE", "/api/v1/channels/ops", nil); w.Code != 404 {
		t.Fatalf("delete twice: %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/channels/ops", nil, nil); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	// Re-creating a deleted channel brings it back.
	if r := putChannel(e, "ops", okChannel()); r.Code != 200 {
		t.Fatal(r.Code)
	}
	if w := e.api("GET", "/api/v1/channels/ops", nil); w.Code != 200 {
		t.Fatalf("recreated: %d", w.Code)
	}
}

func TestChannelValidation(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name  string
		mut   func(m map[string]any)
		field string
	}{
		{"default above max", func(m map[string]any) { m["default_level"] = "page"; m["max_level"] = "active" }, "default_level"},
		{"bad default level", func(m map[string]any) { m["default_level"] = "urgent" }, "default_level"},
		{"bad max level", func(m map[string]any) { m["max_level"] = "" }, "max_level"},
		{"missing name", func(m map[string]any) { delete(m, "name") }, "name"},
		{"unknown template", func(m map[string]any) { m["template_id"] = "nope" }, "template_id"},
		{"unknown sound", func(m map[string]any) { m["sound"] = "nope" }, "sound"},
		{"action without id", func(m map[string]any) { m["actions"] = []any{map[string]any{"title": "Ack"}} }, "actions[0].id"},
		{"action without title", func(m map[string]any) { m["actions"] = []any{map[string]any{"id": "ack"}} }, "actions[0].title"},
		{"url action without url", func(m map[string]any) {
			m["actions"] = []any{map[string]any{"id": "o", "title": "Open", "kind": "url"}}
		}, "actions[0].url"},
		{"url action over http", func(m map[string]any) {
			m["actions"] = []any{map[string]any{"id": "o", "title": "Open", "kind": "url", "url": "http://example.com"}}
		}, "actions[0].url"},
		{"url action without host", func(m map[string]any) {
			m["actions"] = []any{map[string]any{"id": "o", "title": "Open", "kind": "url", "url": "https:///x"}}
		}, "actions[0].url"},
		{"url action javascript", func(m map[string]any) {
			m["actions"] = []any{map[string]any{"id": "ack", "title": "Ack"}, map[string]any{"id": "o", "title": "Open", "kind": "url", "url": "javascript:alert(1)"}}
		}, "actions[1].url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := okChannel()
			tc.mut(m)
			r := putChannel(e, "c1", m)
			if r.Code != 400 {
				t.Fatalf("%d %s", r.Code, r.Body)
			}
			var got struct{ Field string }
			json.Unmarshal([]byte(r.Body), &got)
			if got.Field != tc.field {
				t.Fatalf("field %q, want %q (%s)", got.Field, tc.field, r.Body)
			}
		})
	}
	if r := putChannel(e, "bad$id", okChannel()); r.Code != 400 {
		t.Fatalf("bad id: %d", r.Code)
	}
	// default == max is fine.
	m := okChannel()
	m["default_level"], m["max_level"] = "active", "active"
	if r := putChannel(e, "c2", m); r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
}

func TestChannelReferencesExistingTemplateAndSound(t *testing.T) {
	e := newEnv(t)
	if w := e.api("PUT", "/api/v1/templates/t1", map[string]any{"version": 1, "body": json.RawMessage(goodTemplate)}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w := e.do("PUT", "/api/v1/sounds/ding", wavBytes(2), map[string]string{"Authorization": "Bearer " + e.apiKey, "Content-Type": "audio/wav"})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	m := okChannel()
	m["template_id"], m["sound"], m["thread"] = "t1", "ding", "ops-thread"
	m["actions"] = []any{map[string]any{"id": "ack", "title": "Acknowledge", "ack": true}}
	if r := putChannel(e, "ops", m); r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	got := decode[map[string]any](t, e.api("GET", "/api/v1/channels/ops", nil))
	if got["template_id"] != "t1" || got["sound"] != "ding" || got["thread"] != "ops-thread" {
		t.Fatalf("%v", got)
	}
	if a, _ := got["actions"].([]any); len(a) != 1 {
		t.Fatalf("actions %v", got["actions"])
	}
}

func TestTemplateValidationReportsFieldPath(t *testing.T) {
	e := newEnv(t)
	body := `{"type":"vstack","children":[{"type":"text"},{"type":"spacer"},{"type":"web"}]}`
	w := e.api("PUT", "/api/v1/templates/t1", map[string]any{"version": 1, "body": json.RawMessage(body)})
	if w.Code != 400 || decode[map[string]string](t, w)["field"] != "body.children[2].type" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := e.api("PUT", "/api/v1/templates/t1", map[string]any{"version": 0, "body": json.RawMessage(goodTemplate)}); w.Code != 400 {
		t.Fatalf("version 0: %d", w.Code)
	}
	if w := e.api("PUT", "/api/v1/templates/t1", map[string]any{"version": 1}); w.Code != 400 {
		t.Fatalf("missing body: %d", w.Code)
	}
	// 9-deep tree.
	deep := `{"type":"text"}`
	for range 8 {
		deep = `{"type":"vstack","children":[` + deep + `]}`
	}
	if w := e.api("PUT", "/api/v1/templates/t2", map[string]any{"version": 1, "body": json.RawMessage(deep)}); w.Code != 400 {
		t.Fatalf("9-deep: %d %s", w.Code, w.Body)
	}
}

func TestTemplateVersionMustIncrease(t *testing.T) {
	e := newEnv(t)
	put := func(v int) int {
		return e.api("PUT", "/api/v1/templates/t1", map[string]any{"version": v, "body": json.RawMessage(goodTemplate)}).Code
	}
	if c := put(2); c != 200 {
		t.Fatalf("v2: %d", c)
	}
	if c := put(2); c != 409 {
		t.Fatalf("same version: %d", c)
	}
	if c := put(1); c != 409 {
		t.Fatalf("lower version: %d", c)
	}
	if c := put(3); c != 200 {
		t.Fatalf("v3: %d", c)
	}
}

// ---- sounds ----

func wavBytes(seconds int) []byte {
	const rate = 8000 // 8 kHz mono 16-bit -> 16000 bytes/s
	data := make([]byte, seconds*rate*2)
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint32(rate))
	binary.Write(&b, binary.LittleEndian, uint32(rate*2))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

// aiffBytes builds a mono 16-bit AIFF; the header claims `frames` sample frames at 8 kHz.
func aiffBytes(frames uint32, realFrames int) []byte {
	var comm bytes.Buffer
	binary.Write(&comm, binary.BigEndian, uint16(1))
	binary.Write(&comm, binary.BigEndian, frames)
	binary.Write(&comm, binary.BigEndian, uint16(16))
	// 80-bit extended float for 8000: exponent 16383+12, mantissa 8000<<51.
	binary.Write(&comm, binary.BigEndian, uint16(16383+12))
	binary.Write(&comm, binary.BigEndian, uint64(8000)<<51)
	ssnd := make([]byte, 8+realFrames*2)
	var b bytes.Buffer
	b.WriteString("FORM")
	binary.Write(&b, binary.BigEndian, uint32(4+8+comm.Len()+8+len(ssnd)))
	b.WriteString("AIFFCOMM")
	binary.Write(&b, binary.BigEndian, uint32(comm.Len()))
	b.Write(comm.Bytes())
	b.WriteString("SSND")
	binary.Write(&b, binary.BigEndian, uint32(len(ssnd)))
	b.Write(ssnd)
	return b.Bytes()
}

func cafBytes() []byte {
	return append([]byte("caff\x00\x01\x00\x00"), make([]byte, 64)...)
}

func (e *env) putSound(id, ct string, body []byte) *httpResp {
	w := e.do("PUT", "/api/v1/sounds/"+id, body, map[string]string{"Authorization": "Bearer " + e.apiKey, "Content-Type": ct})
	return &httpResp{w.Code, w.Body.String()}
}

func TestSoundUploadRules(t *testing.T) {
	e := newEnv(t)
	if r := e.putSound("ok-wav", "audio/wav", wavBytes(29)); r.Code != 200 {
		t.Fatalf("29 s wav: %d %s", r.Code, r.Body)
	}
	if r := e.putSound("long-wav", "audio/wav", wavBytes(31)); r.Code != 400 || !strings.Contains(r.Body, "sound_too_long") {
		t.Fatalf("31 s wav: %d %s", r.Code, r.Body)
	}
	if r := e.putSound("exact-wav", "audio/wav", wavBytes(30)); r.Code != 400 {
		t.Fatalf("30 s wav must be rejected (< 30 s): %d", r.Code)
	}
	if r := e.putSound("ok-aiff", "audio/aiff", aiffBytes(8000*10, 100)); r.Code != 200 {
		t.Fatalf("10 s aiff: %d %s", r.Code, r.Body)
	}
	if r := e.putSound("long-aiff", "audio/aiff", aiffBytes(8000*31, 100)); r.Code != 400 {
		t.Fatalf("31 s aiff: %d %s", r.Code, r.Body)
	}
	if r := e.putSound("ok-caf", "audio/x-caf", cafBytes()); r.Code != 200 {
		t.Fatalf("caf: %d %s", r.Code, r.Body)
	}
	if r := e.putSound("mp3", "audio/mpeg", []byte("ID3....")); r.Code != 415 || !strings.Contains(r.Body, "unsupported_media_type") {
		t.Fatalf("mp3: %d %s", r.Code, r.Body)
	}
	if r := e.putSound("fake", "audio/wav", []byte("not a wav at all")); r.Code != 400 {
		t.Fatalf("garbage wav: %d", r.Code)
	}
	if r := e.putSound("big", "audio/x-caf", append(cafBytes(), make([]byte, 1<<20)...)); r.Code != 413 {
		t.Fatalf("over 1 MB: %d", r.Code)
	}
	if r := e.putSound("bad$id", "audio/x-caf", cafBytes()); r.Code != 400 {
		t.Fatalf("bad id: %d", r.Code)
	}
	if w := e.do("PUT", "/api/v1/sounds/x", cafBytes(), map[string]string{"Content-Type": "audio/x-caf"}); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	// Stored with a filename carrying the right extension and the content hash.
	snd, err := e.st.GetSound("ok-wav")
	sum := sha256.Sum256(wavBytes(29))
	if err != nil || snd.Filename != "ok-wav.wav" || snd.SHA256 != hex.EncodeToString(sum[:]) || snd.ContentType != "audio/wav" {
		t.Fatalf("%+v %v", snd, err)
	}
	if s, _ := e.st.GetSound("ok-caf"); s.Filename != "ok-caf.caf" {
		t.Fatalf("caf filename %q", s.Filename)
	}
}

// ---- device sync ----

func TestSyncReturnsOnlyLiveChannels(t *testing.T) {
	e := newEnv(t)
	e.api("PUT", "/api/v1/templates/t1", map[string]any{"version": 3, "body": json.RawMessage(goodTemplate)})
	e.putSound("ding", "audio/wav", wavBytes(1))
	putChannel(e, "ops", okChannel())
	putChannel(e, "gone", okChannel())
	putChannel(e, "chat", okChannel())
	e.api("DELETE", "/api/v1/channels/gone", nil)

	d := e.newDevice("phone")
	d.pair("alice")
	w := d.request("GET", "/device/v1/sync", nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	res := decode[struct {
		Hub      map[string]string `json:"hub"`
		Channels []struct {
			ID string `json:"id"`
		} `json:"channels"`
		Templates []struct {
			ID      string          `json:"id"`
			Version int             `json:"version"`
			Body    json.RawMessage `json:"body"`
		} `json:"templates"`
		Sounds []struct {
			ID     string `json:"id"`
			URL    string `json:"url"`
			SHA256 string `json:"sha256"`
		} `json:"sounds"`
	}](t, w)
	var ids []string
	for _, c := range res.Channels {
		ids = append(ids, c.ID)
	}
	if strings.Join(ids, ",") != "chat,ops" {
		t.Fatalf("channels %v", ids)
	}
	if res.Hub["fingerprint"] == "" || res.Hub["name"] != "Example Hub" {
		t.Fatalf("hub %v", res.Hub)
	}
	if len(res.Templates) != 1 || res.Templates[0].Version != 3 || !bytes.Contains(res.Templates[0].Body, []byte("vstack")) {
		t.Fatalf("templates %+v", res.Templates)
	}
	sum := sha256.Sum256(wavBytes(1))
	if len(res.Sounds) != 1 || res.Sounds[0].URL != "https://hub.example.com/device/v1/sounds/ding" || res.Sounds[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sounds %+v", res.Sounds)
	}

	// Unauthenticated sync is refused.
	if w := e.do("GET", "/device/v1/sync", nil, nil); w.Code != 401 {
		t.Fatalf("unauthenticated sync: %d", w.Code)
	}

	// Sound download needs a device signature and returns the bytes.
	w = d.request("GET", "/device/v1/sounds/ding", nil)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), wavBytes(1)) || w.Header().Get("Content-Type") != "audio/wav" {
		t.Fatalf("download: %d ct=%s len=%d", w.Code, w.Header().Get("Content-Type"), w.Body.Len())
	}
	if w := d.request("GET", "/device/v1/sounds/none", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing sound: %d", w.Code)
	}
	if w := e.do("GET", "/device/v1/sounds/ding", nil, nil); w.Code != 401 {
		t.Fatalf("unauthenticated download: %d", w.Code)
	}
}

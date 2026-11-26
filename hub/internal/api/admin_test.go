package api

import (
	"bytes"
	"crypto/ed25519"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/recodive/hansho/hub/internal/config"
	"github.com/recodive/hansho/hub/internal/identity"
)

const adminPW = "correct horse battery"

func newAdminEnv(t *testing.T) *env {
	t.Helper()
	return newEnvCfg(t, func(c *config.Config) { c.Admin, c.AdminPassword = true, adminPW })
}

// browser is a stand-in for an admin's browser: a cookie jar of one cookie and a client address.
type browser struct {
	e      *env
	addr   string
	cookie *http.Cookie
	csrf   string
}

func (e *env) browser() *browser { return &browser{e: e, addr: "192.0.2.10:5000"} }

// do sends a request. A POST carries a same-origin Origin header unless hdr overrides it (an empty value removes it).
func (b *browser) do(method, path string, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	b.e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	req.RemoteAddr = b.addr
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method == "POST" {
		req.Header.Set("Origin", "https://hub.example.com")
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	if b.cookie != nil {
		req.AddCookie(b.cookie)
	}
	w := httptest.NewRecorder()
	b.e.h.ServeHTTP(w, req)
	return w
}

func (b *browser) get(path string) *httptest.ResponseRecorder { return b.do("GET", path, nil, nil) }

// post sends a form with the session's form token added.
func (b *browser) post(path string, kv ...string) *httptest.ResponseRecorder {
	b.e.t.Helper()
	form := url.Values{"csrf": {b.csrf}}
	for i := 0; i < len(kv); i += 2 {
		form.Set(kv[i], kv[i+1])
	}
	return b.do("POST", path, form, nil)
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (b *browser) signIn() *browser {
	b.e.t.Helper()
	w := b.do("POST", "/admin/login", url.Values{"password": {adminPW}}, nil)
	if w.Code != 303 || w.Header().Get("Location") != "/admin/" {
		b.e.t.Fatalf("sign-in: %d %s", w.Code, w.Body)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == adminCookie {
			b.cookie = c
		}
	}
	if b.cookie == nil {
		b.e.t.Fatal("sign-in set no session cookie")
	}
	home := b.get("/admin/")
	if m := csrfRe.FindStringSubmatch(home.Body.String()); home.Code != 200 || m == nil {
		b.e.t.Fatalf("home after sign-in: %d %s", home.Code, home.Body)
	} else {
		b.csrf = m[1]
	}
	return b
}

func checkAdminHeaders(t *testing.T, w *httptest.ResponseRecorder, what string) {
	t.Helper()
	h := w.Header()
	if h.Get("Cache-Control") != "no-store" || h.Get("X-Frame-Options") != "DENY" || h.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Errorf("%s: missing security headers: %v", what, h)
	}
}

// captureLog sends the hub's log to a buffer for the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil))) })
	return &buf
}

func TestAdminOffByDefault(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/admin", "/admin/", "/admin/login", "/admin/keys", "/admin/admin.css"} {
		if w := e.do("GET", p, nil, nil); w.Code != 404 {
			t.Errorf("GET %s with HUB_ADMIN off: %d", p, w.Code)
		}
	}
}

func TestAdminSignInAndOut(t *testing.T) {
	logs := captureLog(t)
	e := newAdminEnv(t)
	b := e.browser()

	login := b.get("/admin/login")
	if login.Code != 200 || !strings.Contains(login.Body.String(), "Example Hub") || !strings.Contains(login.Body.String(), `type="password"`) {
		t.Fatalf("login page: %d %s", login.Code, login.Body)
	}
	checkAdminHeaders(t, login, "login page")

	for _, pw := range []string{"", "wrong", adminPW + " ", strings.ToUpper(adminPW)} {
		w := b.do("POST", "/admin/login", url.Values{"password": {pw}}, nil)
		if w.Code != 401 || len(w.Result().Cookies()) != 0 || !strings.Contains(w.Body.String(), "not right") {
			t.Fatalf("wrong password %q: %d %v", pw, w.Code, w.Result().Cookies())
		}
		checkAdminHeaders(t, w, "failed sign-in")
	}

	b.signIn()
	c := b.cookie
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.MaxAge != 12*3600 || c.Path != "/admin" {
		t.Errorf("session cookie attributes: %+v", c)
	}
	home := b.get("/admin/")
	if home.Code != 200 || !strings.Contains(home.Body.String(), "Pair a person") {
		t.Fatalf("home: %d %s", home.Code, home.Body)
	}
	checkAdminHeaders(t, home, "home")
	if w := b.get("/admin/login"); w.Code != 303 || w.Header().Get("Location") != "/admin/" {
		t.Errorf("sign-in page while signed in: %d", w.Code)
	}
	if w := b.get("/admin"); w.Code != 303 {
		t.Errorf("/admin: %d", w.Code)
	}
	if w := b.get("/admin/nothing-here"); w.Code != 404 || !strings.Contains(w.Body.String(), "Page not found") {
		t.Errorf("unknown page: %d", w.Code)
	} else {
		checkAdminHeaders(t, w, "404")
	}
	for _, f := range []string{"/admin/admin.css", "/admin/admin.js"} {
		w := e.do("GET", f, nil, nil)
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Errorf("%s: %d", f, w.Code)
		}
		checkAdminHeaders(t, w, f)
	}

	out := b.post("/admin/logout")
	if out.Code != 303 || out.Header().Get("Location") != "/admin/login" {
		t.Fatalf("logout: %d", out.Code)
	}
	if cs := out.Result().Cookies(); len(cs) != 1 || cs[0].Name != adminCookie || cs[0].MaxAge >= 0 {
		t.Errorf("logout must clear the cookie: %v", cs)
	}
	for _, want := range []string{"admin: sign-in failed", "admin: signed in", "admin: signed out"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs.String(), adminPW) || strings.Contains(logs.String(), c.Value) {
		t.Error("the log holds the password or the session")
	}
}

func TestAdminLoginChecksOrigin(t *testing.T) {
	e := newAdminEnv(t)
	b := e.browser()
	form := url.Values{"password": {adminPW}}
	for name, hdr := range map[string]map[string]string{
		"no origin":      {"Origin": ""},
		"other origin":   {"Origin": "https://evil.example"},
		"null origin":    {"Origin": "null"},
		"other referer":  {"Origin": "", "Referer": "https://evil.example/admin/login"},
		"origin on port": {"Origin": "https://hub.example.com:8443"},
	} {
		if w := b.do("POST", "/admin/login", form, hdr); w.Code != 403 || len(w.Result().Cookies()) != 0 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	for name, hdr := range map[string]map[string]string{
		"public url":   {"Origin": "https://hub.example.com"},
		"request host": {"Origin": "http://example.com"}, // httptest requests are for example.com
		"referer":      {"Origin": "", "Referer": "https://hub.example.com/admin/login"},
	} {
		if w := b.do("POST", "/admin/login", form, hdr); w.Code != 303 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
}

func TestAdminLoginRateLimit(t *testing.T) {
	e := newAdminEnv(t)
	b := e.browser()
	try := func(b *browser, pw string) *httptest.ResponseRecorder {
		return b.do("POST", "/admin/login", url.Values{"password": {pw}}, nil)
	}
	for i := 0; i < adminFreeTries; i++ {
		if w := try(b, "wrong"); w.Code != 401 {
			t.Fatalf("try %d: %d", i, w.Code)
		}
	}
	// Locked: even the right password is refused, and the refusal says when to come back.
	w := try(b, adminPW)
	if w.Code != 429 || w.Header().Get("Retry-After") != "30" || len(w.Result().Cookies()) != 0 || !strings.Contains(w.Body.String(), "30 seconds") {
		t.Fatalf("locked: %d retry-after=%q %s", w.Code, w.Header().Get("Retry-After"), w.Body)
	}
	checkAdminHeaders(t, w, "locked sign-in")
	// Another address is not affected, and nor is the same one in IPv6 form of a different address.
	other := e.browser()
	other.addr = "192.0.2.11:5000"
	if w := try(other, adminPW); w.Code != 303 {
		t.Errorf("other address: %d", w.Code)
	}
	// The lock ends, the next wrong password doubles it.
	e.clk.Advance(31 * time.Second)
	if w := try(b, "wrong"); w.Code != 401 {
		t.Fatalf("after the first lock: %d", w.Code)
	}
	if w := try(b, adminPW); w.Code != 429 || w.Header().Get("Retry-After") != "60" {
		t.Fatalf("second lock: %d retry-after=%q", w.Code, w.Header().Get("Retry-After"))
	}
	e.clk.Advance(61 * time.Second)
	if w := try(b, adminPW); w.Code != 303 {
		t.Fatalf("after the second lock: %d", w.Code)
	}
	// A good sign-in clears the count.
	for i := 0; i < adminFreeTries-1; i++ {
		try(b, "wrong")
	}
	if w := try(b, adminPW); w.Code != 303 {
		t.Errorf("below the limit: %d", w.Code)
	}
	// IPv6 addresses are limited by their /64.
	v6 := e.browser()
	for i := 0; i < adminFreeTries; i++ {
		v6.addr = "[2001:db8::" + strconv.Itoa(i+1) + "]:5000"
		try(v6, "wrong")
	}
	v6.addr = "[2001:db8::ff]:5000"
	if w := try(v6, adminPW); w.Code != 429 {
		t.Errorf("same /64: %d", w.Code)
	}
}

func TestAdminSessionChecks(t *testing.T) {
	e := newAdminEnv(t)
	b := e.browser().signIn()
	good := *b.cookie

	e.clk.Advance(12*time.Hour - time.Second)
	if w := b.get("/admin/"); w.Code != 200 {
		t.Errorf("just before expiry: %d", w.Code)
	}
	e.clk.Advance(2 * time.Second)
	if w := b.get("/admin/"); w.Code != 303 {
		t.Errorf("after 12 hours: %d", w.Code)
	}

	b.cookie = &good
	e.clk.Advance(-12 * time.Hour)
	if w := b.get("/admin/"); w.Code != 200 {
		t.Fatalf("restored: %d", w.Code)
	}
	for name, v := range map[string]string{
		"flipped mac":  good.Value[:len(good.Value)-1] + map[bool]string{true: "B", false: "A"}[good.Value[len(good.Value)-1] == 'A'],
		"no mac":       strings.Split(good.Value, ".")[0],
		"empty":        "",
		"extended exp": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA." + strings.Split(good.Value, ".")[1],
		"garbage":      "x.y",
	} {
		b.cookie = &http.Cookie{Name: adminCookie, Value: v}
		if w := b.get("/admin/"); w.Code != 303 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}

	// Changing the password signs everyone out.
	e2 := newEnvCfg(t, func(c *config.Config) { c.Admin, c.AdminPassword = true, "another password!" })
	b2 := e2.browser()
	b2.cookie = &good
	if w := b2.get("/admin/"); w.Code != 303 {
		t.Errorf("cookie under another password: %d", w.Code)
	}
}

func TestAdminRoutesNeedSignIn(t *testing.T) {
	e := newAdminEnv(t)
	b := e.browser()
	routes := (&server{Deps: Deps{Cfg: config.Config{Admin: true}}}).adminRoutes()
	if len(routes) < 10 {
		t.Fatalf("only %d admin routes", len(routes))
	}
	public := map[string]bool{"GET /admin/admin.css": true, "GET /admin/admin.js": true, "GET /admin/login": true, "POST /admin/login": true}
	for _, rt := range routes {
		if public[rt.pattern] != (rt.kind == adminPublic || rt.kind == adminPublicPost) {
			t.Errorf("%s: unexpected public/protected kind %d", rt.pattern, rt.kind)
		}
		if rt.kind == adminPublic || rt.kind == adminPublicPost {
			continue
		}
		method, path, ok := strings.Cut(rt.pattern, " ")
		if !ok {
			method, path = "GET", rt.pattern
		}
		path = strings.NewReplacer("{id}", "1", "{$}", "").Replace(path)
		var form url.Values
		if method == "POST" {
			form = url.Values{"csrf": {"x"}, "name": {"x"}}
		}
		w := b.do(method, path, form, nil)
		if w.Code != 303 || w.Header().Get("Location") != "/admin/login" {
			t.Errorf("%s: %d %s, want a redirect to the sign-in page", rt.pattern, w.Code, w.Header().Get("Location"))
		}
	}
	if w := b.get("/admin/does/not/exist"); w.Code != 303 {
		t.Errorf("unknown path while signed out: %d", w.Code)
	}
}

func TestAdminPostsNeedCSRFToken(t *testing.T) {
	e := newAdminEnv(t)
	b := e.browser().signIn()
	routes := (&server{Deps: Deps{Cfg: config.Config{Admin: true}}}).adminRoutes()
	n := 0
	for _, rt := range routes {
		if rt.kind != adminSignedInPost && rt.kind != adminSignedInUpload {
			continue
		}
		n++
		path := strings.NewReplacer("{id}", "1").Replace(strings.TrimPrefix(rt.pattern, "POST "))
		form := func(csrf string) url.Values { return url.Values{"csrf": {csrf}, "name": {"Alex"}, "person": {"Alex"}} }
		for name, tc := range map[string]struct {
			form url.Values
			hdr  map[string]string
		}{
			"no token":      {url.Values{"name": {"Alex"}}, nil},
			"wrong token":   {form("nope"), nil},
			"empty token":   {form(""), nil},
			"other origin":  {form(b.csrf), map[string]string{"Origin": "https://evil.example"}},
			"no origin":     {form(b.csrf), map[string]string{"Origin": ""}},
			"other referer": {form(b.csrf), map[string]string{"Origin": "", "Referer": "https://evil.example/"}},
		} {
			if w := b.do("POST", path, tc.form, tc.hdr); w.Code != 403 {
				t.Errorf("%s, %s: %d", rt.pattern, name, w.Code)
			}
		}
	}
	if n < 4 {
		t.Fatalf("only %d signed-in POST routes", n)
	}
	// A refused pair request made no ticket: nothing was shown and the log is the only trace.
	if w := b.do("POST", "/admin/pair", url.Values{"name": {"Alex"}}, nil); strings.Contains(w.Body.String(), "hansho://") {
		t.Error("a request without the token got a pair code")
	}
	// The token of one session is no good in another.
	other := e.browser().signIn()
	if w := other.do("POST", "/admin/pair", url.Values{"csrf": {b.csrf}, "name": {"Alex"}}, nil); w.Code != 403 {
		t.Errorf("another session's token: %d", w.Code)
	}
}

var linkRe = regexp.MustCompile(`id="link" type="text" value="([^"]*)"`)

func TestAdminPair(t *testing.T) {
	logs := captureLog(t)
	e := newAdminEnv(t)
	b := e.browser().signIn()

	if w := b.get("/admin/pair"); w.Code != 200 || !strings.Contains(w.Body.String(), `name="name"`) || !strings.Contains(w.Body.String(), "<label") {
		t.Fatalf("form: %d %s", w.Code, w.Body)
	}
	w := b.post("/admin/pair", "name", "  Alex  ")
	if w.Code != 200 {
		t.Fatalf("pair: %d %s", w.Code, w.Body)
	}
	checkAdminHeaders(t, w, "pair code")
	body := w.Body.String()
	m := linkRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no pair link in %s", body)
	}
	link := html.UnescapeString(m[1])
	pre := "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t="
	if !strings.HasPrefix(link, pre) || !strings.HasSuffix(link, "&fp="+identity.Fingerprint(e.hubKey.Public().(ed25519.PublicKey))) {
		t.Fatalf("link %q", link)
	}
	ticket, _, _ := strings.Cut(strings.TrimPrefix(link, pre), "&")
	for _, want := range []string{"<svg", "Alex", "14 Nov 2023, 22:23 UTC", "10 minutes", `datetime="2023-11-14T22:23:20Z"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "style=") {
		t.Error("inline style attribute would be blocked by the CSP")
	}
	if strings.Contains(logs.String(), ticket) {
		t.Error("the log holds the ticket")
	}
	if !strings.Contains(logs.String(), "admin: pair code created") {
		t.Errorf("no log line:\n%s", logs)
	}

	// The ticket is the API's: it pairs the person under the trimmed name, once.
	d := e.newDevice("Alex phone")
	if r := e.do("POST", "/device/v1/pairings", d.redeemBody(ticket), nil); r.Code != 201 {
		t.Fatalf("redeem: %d %s", r.Code, r.Body)
	}
	if ds, err := e.st.ActiveDevicesForUser("Alex"); err != nil || len(ds) != 1 {
		t.Fatalf("paired devices: %v %v", ds, err)
	}
	if r := e.do("POST", "/device/v1/pairings", e.newDevice("Alex tablet").redeemBody(ticket), nil); r.Code != 410 || !strings.Contains(r.Body.String(), "ticket_used") {
		t.Errorf("second redeem: %d %s", r.Code, r.Body)
	}

	// An unused ticket runs out after the API's lifetime.
	w = b.post("/admin/pair", "name", "Sam")
	m = linkRe.FindStringSubmatch(w.Body.String())
	sam, _, _ := strings.Cut(strings.TrimPrefix(html.UnescapeString(m[1]), pre), "&")
	e.clk.Advance(ticketTTL + time.Second)
	if r := e.do("POST", "/device/v1/pairings", e.newDevice("Sam phone").redeemBody(sam), nil); r.Code != 410 || !strings.Contains(r.Body.String(), "ticket_expired") {
		t.Errorf("late redeem: %d %s", r.Code, r.Body)
	}

	for name, v := range map[string]string{"empty": "", "blanks": "   ", "too long": strings.Repeat("a", maxFieldLen+1)} {
		w := b.post("/admin/pair", "name", v)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "Enter a name") || strings.Contains(w.Body.String(), "hansho://") {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if w := b.post("/admin/pair", "name", strings.Repeat("a", maxFieldLen)); w.Code != 200 {
		t.Errorf("256 bytes: %d", w.Code)
	}
}

func TestAdminPeople(t *testing.T) {
	logs := captureLog(t)
	e := newAdminEnv(t)
	b := e.browser().signIn()

	if w := b.get("/admin/people"); w.Code != 200 || !strings.Contains(w.Body.String(), "No one is paired yet") {
		t.Fatalf("empty list: %d %s", w.Code, w.Body)
	}
	phone, watch := e.newDevice("Alice's iPhone"), e.newDevice("Alice's Watch")
	phone.pair("alice")
	if r := watch.addTo(phone.PairingID, phone.PairingKey); r.Code != 201 {
		t.Fatalf("add device: %d %s", r.Code, r.Body)
	}
	bob := e.newDevice("Bob <b>phone</b>")
	bob.pair("bob")
	// A pairing that ended is not listed.
	gone := e.newDevice("Gone phone")
	gone.pair("gone")
	if r := gone.request("DELETE", "/device/v1/pairings/"+gone.PairingID, nil); r.Code != 204 {
		t.Fatalf("unpair: %d", r.Code)
	}

	w := b.get("/admin/people")
	body := w.Body.String()
	checkAdminHeaders(t, w, "people")
	for _, want := range []string{"alice", "bob", "Alice&#39;s iPhone", "Alice&#39;s Watch", "Bob &lt;b&gt;phone&lt;/b&gt;", "ios", "14 Nov 2023, 22:13 UTC", `href="/admin/people/remove?person=alice"`, `aria-label="Remove alice"`} {
		if !strings.Contains(body, want) {
			t.Errorf("list lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Gone phone") || strings.Contains(body, "<b>phone") {
		t.Error("revoked pairing listed or markup not escaped")
	}
	if w := b.post("/admin/people/name", "person", "alice", "display_name", "Álice M."); w.Code != http.StatusSeeOther {
		t.Fatalf("save name: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(b.get("/admin/people").Body.String(), "Álice M.") {
		t.Error("display name not listed")
	}
	if w := b.post("/admin/people/name", "person", "alice", "display_name", strings.Repeat("x", 41)); w.Code != http.StatusBadRequest {
		t.Errorf("too long: %d", w.Code)
	}
	if strings.Index(body, ">alice<") > strings.Index(body, ">bob<") {
		t.Error("people are not in name order")
	}

	// An unused ticket of alice's is dead after the removal, like with the API's unpair call.
	tk := decode[struct{ Ticket string }](t, e.api("POST", "/api/v1/pairing-tickets", map[string]string{"external_user_id": "alice"}))

	// Asking is not removing.
	revoked := len(e.relay.revokes()) // gone's phone
	c := b.get("/admin/people/remove?person=alice")
	if c.Code != 200 || !strings.Contains(c.Body.String(), "Remove alice?") || !strings.Contains(c.Body.String(), "Alice&#39;s Watch") || !strings.Contains(c.Body.String(), `name="person" value="alice"`) {
		t.Fatalf("confirm page: %d %s", c.Code, c.Body)
	}
	if ds, _ := e.st.ActiveDevicesForUser("alice"); len(ds) != 2 || len(e.relay.revokes()) != revoked {
		t.Fatal("the confirm page removed the person")
	}
	if w := b.get("/admin/people/remove?person=nobody"); w.Code != 404 {
		t.Errorf("unknown person: %d", w.Code)
	}

	r := b.post("/admin/people/remove", "person", "alice")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "alice was removed") {
		t.Fatalf("remove: %d %s", r.Code, r.Body)
	}
	if ds, _ := e.st.ActiveDevicesForUser("alice"); len(ds) != 0 {
		t.Error("alice still has devices")
	}
	if got := e.relay.revokes(); len(got) != revoked+2 {
		t.Errorf("relay revokes %v, want both of alice's devices", got)
	}
	ev := e.events("pairing.revoked")
	var kicked bool
	for _, x := range ev {
		kicked = kicked || (x["external_user_id"] == "alice" && x["by"] == "sender")
	}
	if !kicked {
		t.Errorf("no pairing.revoked event by the sender: %v", ev)
	}
	if res := e.do("POST", "/device/v1/pairings", e.newDevice("late").redeemBody(tk.Ticket), nil); res.Code != 410 {
		t.Errorf("alice's ticket after the removal: %d", res.Code)
	}
	if res := phone.request("GET", "/device/v1/sync", nil); res.Code == 200 {
		t.Errorf("alice's phone still syncs: %d", res.Code)
	}
	after := b.get("/admin/people").Body.String()
	if strings.Contains(after, ">alice<") || !strings.Contains(after, ">bob<") {
		t.Errorf("list after removal:\n%s", after)
	}
	if again := b.post("/admin/people/remove", "person", "alice"); again.Code != 200 || !strings.Contains(again.Body.String(), "nothing to remove") {
		t.Errorf("second removal: %d %s", again.Code, again.Body)
	}
	if !strings.Contains(logs.String(), "admin: person removed") {
		t.Errorf("no log line:\n%s", logs)
	}
}

var keyRe = regexp.MustCompile(`id="key" type="text" value="([^"]*)"`)

func TestAdminKeys(t *testing.T) {
	logs := captureLog(t)
	e := newAdminEnv(t)
	b := e.browser().signIn()

	list := b.get("/admin/keys")
	if list.Code != 200 || !strings.Contains(list.Body.String(), "<td>test</td>") || !strings.Contains(list.Body.String(), "Working") {
		t.Fatalf("list: %d %s", list.Code, list.Body)
	}
	checkAdminHeaders(t, list, "keys")

	w := b.post("/admin/keys", "name", "  billing  ")
	m := keyRe.FindStringSubmatch(w.Body.String())
	if w.Code != 200 || m == nil || !strings.HasPrefix(m[1], "ck_") || !strings.Contains(w.Body.String(), "cannot show it again") {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	checkAdminHeaders(t, w, "new key")
	key := m[1]
	if !strings.Contains(w.Body.String(), "readonly") || !strings.Contains(w.Body.String(), `data-copy="key" hidden`) {
		t.Error("the key needs a read-only field and a copy button that only JavaScript shows")
	}
	if r := e.do("GET", "/api/v1/channels", nil, map[string]string{"Authorization": "Bearer " + key}); r.Code != 200 {
		t.Fatalf("the new key does not work: %d", r.Code)
	}
	if strings.Contains(logs.String(), key) {
		t.Error("the log holds the key")
	}
	if !strings.Contains(logs.String(), "admin: api key created") || !strings.Contains(logs.String(), "id=2") {
		t.Errorf("log:\n%s", logs)
	}

	again := b.get("/admin/keys").Body.String()
	if strings.Contains(again, key) || !strings.Contains(again, "<td>billing</td>") || !strings.Contains(again, "/admin/keys/2/revoke") {
		t.Errorf("list after create:\n%s", again)
	}
	for name, v := range map[string]string{"empty": "", "blanks": "  ", "too long": strings.Repeat("k", maxFieldLen+1)} {
		if w := b.post("/admin/keys", "name", v); w.Code != 400 || !strings.Contains(w.Body.String(), "Enter a name") || keyRe.MatchString(w.Body.String()) {
			t.Errorf("%s: %d", name, w.Code)
		}
	}

	c := b.get("/admin/keys/2/revoke")
	if c.Code != 200 || !strings.Contains(c.Body.String(), "Revoke billing?") {
		t.Fatalf("confirm: %d %s", c.Code, c.Body)
	}
	if r := e.do("GET", "/api/v1/channels", nil, map[string]string{"Authorization": "Bearer " + key}); r.Code != 200 {
		t.Fatal("the confirm page revoked the key")
	}
	if w := b.post("/admin/keys/2/revoke"); w.Code != 200 || !strings.Contains(w.Body.String(), "Key revoked") {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	if r := e.do("GET", "/api/v1/channels", nil, map[string]string{"Authorization": "Bearer " + key}); r.Code != 401 {
		t.Errorf("revoked key: %d", r.Code)
	}
	if r := e.api("GET", "/api/v1/channels", nil); r.Code != 200 {
		t.Errorf("another key stopped working: %d", r.Code)
	}
	if !strings.Contains(logs.String(), "admin: api key revoked") {
		t.Errorf("no revoke log line:\n%s", logs)
	}
	if after := b.get("/admin/keys").Body.String(); !strings.Contains(after, "Switched off") {
		t.Errorf("list after revoke:\n%s", after)
	}
	if c := b.get("/admin/keys/2/revoke"); !strings.Contains(c.Body.String(), "Already revoked") {
		t.Errorf("confirm for a revoked key: %d %s", c.Code, c.Body)
	}
	for _, p := range []string{"/admin/keys/99/revoke", "/admin/keys/abc/revoke"} {
		if w := b.get(p); w.Code != 404 {
			t.Errorf("GET %s: %d", p, w.Code)
		}
		if w := b.post(p); w.Code != 404 {
			t.Errorf("POST %s: %d", p, w.Code)
		}
	}
}

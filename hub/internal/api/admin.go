package api

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/binary"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/recodive/hansho/hub/internal/brand"
)

// The admin UI lives under /admin and is off unless HUB_ADMIN is set. Its pages, style and script are embedded, and the
// fonts and icons are the status page's (the pages make no external request). Handlers call the store and the same
// helpers as the sender API; they never go through HTTP or an API key.
//
//go:embed admin_ui
var adminFS embed.FS

const (
	adminCookie     = brand.Name + "_admin"
	adminSessionTTL = 12 * time.Hour
	// adminMaxForm bounds a form body: the largest field is a 256-byte name.
	adminMaxForm = 8 << 10

	// After adminFreeTries wrong passwords from one address, sign-in is refused for adminLockBase, doubling with every
	// further failure up to adminLockMax. A record is forgotten adminLockMemory after its last failure.
	adminFreeTries  = 5
	adminLockBase   = 30 * time.Second
	adminLockMax    = 15 * time.Minute
	adminLockMemory = time.Hour
	adminMaxTracked = 10000
)

// adminHandler is a page handler. csrf is the signed-in session's form token, "" on the public pages.
type adminHandler func(w http.ResponseWriter, r *http.Request, csrf string)

type adminKind int

const (
	adminPublic       adminKind = iota // assets and the sign-in page
	adminPublicPost                    // sign-in: no session yet, so only the origin is checked
	adminSignedIn                      // needs a session
	adminSignedInPost                  // needs a session, a same-origin request and the session's form token
)

type adminRoute struct {
	pattern string
	kind    adminKind
	h       adminHandler
}

// adminRoutes is every route of the admin UI. The catch-all is last so unknown paths get the same headers and sign-in
// check as the rest.
func (s *server) adminRoutes() []adminRoute {
	return []adminRoute{
		{"GET /admin/admin.css", adminPublic, adminAsset("admin.css", "text/css; charset=utf-8")},
		{"GET /admin/admin.js", adminPublic, adminAsset("admin.js", "text/javascript; charset=utf-8")},
		{"GET /admin/login", adminPublic, s.adminLoginPage},
		{"POST /admin/login", adminPublicPost, s.adminLogin},
		{"POST /admin/logout", adminSignedInPost, s.adminLogout},
		{"GET /admin", adminSignedIn, func(w http.ResponseWriter, r *http.Request, _ string) {
			http.Redirect(w, r, "/admin/", http.StatusSeeOther)
		}},
		{"GET /admin/{$}", adminSignedIn, s.adminHome},
		{"GET /admin/pair", adminSignedIn, s.adminPairPage},
		{"POST /admin/pair", adminSignedInPost, s.adminPair},
		{"GET /admin/people", adminSignedIn, s.adminPeople},
		{"GET /admin/people/remove", adminSignedIn, s.adminRemovePage},
		{"POST /admin/people/remove", adminSignedInPost, s.adminRemove},
		{"GET /admin/keys", adminSignedIn, s.adminKeys},
		{"POST /admin/keys", adminSignedInPost, s.adminCreateKey},
		{"GET /admin/keys/{id}/revoke", adminSignedIn, s.adminRevokePage},
		{"POST /admin/keys/{id}/revoke", adminSignedInPost, s.adminRevoke},
		{"/admin/", adminSignedIn, func(w http.ResponseWriter, _ *http.Request, csrf string) {
			s.adminNotice(w, http.StatusNotFound, csrf, "Page not found", "There is nothing at this address.", "/admin/", "Back to the start")
		}},
	}
}

// mountAdmin registers the admin UI on mux.
func (s *server) mountAdmin(mux *http.ServeMux) {
	// The session key comes from the master key under its own label and the password, so no other use of the master key
	// shares it and changing the password signs everyone out.
	salt := sha256.Sum256([]byte(s.Cfg.AdminPassword))
	key, err := hkdf.Key(sha256.New, s.Cfg.MasterKey, salt[:], brand.Label("admin/session/v1"), 32)
	if err != nil {
		panic(err)
	}
	s.adminKey = key
	for _, rt := range s.adminRoutes() {
		mux.HandleFunc(rt.pattern, s.adminWrap(rt))
	}
}

// adminWrap adds the headers every admin response carries and the checks of the route's kind.
func (s *server) adminWrap(rt adminRoute) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; font-src 'self'; img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		csrf := ""
		if rt.kind == adminSignedIn || rt.kind == adminSignedInPost {
			var ok bool
			if csrf, ok = s.adminSession(r); !ok {
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}
		}
		if rt.kind == adminPublicPost || rt.kind == adminSignedInPost {
			if !s.adminSameOrigin(r) || !adminParseForm(w, r) {
				s.adminNotice(w, http.StatusForbidden, csrf, "Request refused", "This form did not come from this page. Go back, reload the page and try again.", "/admin/", "Back to the start")
				return
			}
			if rt.kind == adminSignedInPost && subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(csrf)) != 1 {
				s.adminNotice(w, http.StatusForbidden, csrf, "Request refused", "This form has expired. Go back, reload the page and try again.", "/admin/", "Back to the start")
				return
			}
		}
		rt.h(w, r, csrf)
	}
}

// adminParseForm reads the size-limited form body.
func adminParseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, adminMaxForm)
	return r.ParseForm() == nil
}

// adminSameOrigin accepts a request whose Origin (or, without one, Referer) is this hub's host or the host of
// HUB_PUBLIC_URL. A request with neither header is refused.
func (s *server) adminSameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		o = r.Referer()
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Host == r.Host {
		return true
	}
	pu, err := url.Parse(s.Cfg.PublicURL)
	return err == nil && u.Host == pu.Host
}

// The session cookie is "<payload>.<mac>": the payload is the expiry (unix seconds) and 16 random bytes, the mac is
// HMAC-SHA256 under the admin key. The form token is another mac of the same payload, so it is bound to this session
// and the page can show it without exposing the cookie.

func (s *server) adminMAC(label, payload string) string {
	m := hmac.New(sha256.New, s.adminKey)
	m.Write([]byte(label + "\x00" + payload))
	return b64url.EncodeToString(m.Sum(nil))
}

func (s *server) adminNewCookie() (*http.Cookie, error) {
	b := make([]byte, 24)
	binary.BigEndian.PutUint64(b, uint64(s.Now().Add(adminSessionTTL).Unix()))
	if _, err := rand.Read(b[8:]); err != nil {
		return nil, err
	}
	payload := b64url.EncodeToString(b)
	return &http.Cookie{
		Name: adminCookie, Value: payload + "." + s.adminMAC("session", payload), Path: "/admin",
		MaxAge: int(adminSessionTTL / time.Second), HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	}, nil
}

// adminSession checks the session cookie and returns the session's form token.
func (s *server) adminSession(r *http.Request) (csrf string, ok bool) {
	c, err := r.Cookie(adminCookie)
	if err != nil {
		return "", false
	}
	payload, mac, found := strings.Cut(c.Value, ".")
	if !found || !hmac.Equal([]byte(mac), []byte(s.adminMAC("session", payload))) {
		return "", false
	}
	b, err := b64url.DecodeString(payload)
	if err != nil || len(b) != 24 || !s.Now().Before(time.Unix(int64(binary.BigEndian.Uint64(b)), 0)) {
		return "", false
	}
	return s.adminMAC("csrf", payload), true
}

// adminLimiter counts wrong passwords per client address, in memory (a restart forgets them). The zero value works.
type adminLimiter struct {
	mu sync.Mutex
	m  map[string]*adminTries
}

type adminTries struct {
	fails       int
	until, last time.Time
}

// wait is how long sign-in is refused for this client, 0 when it may try.
func (l *adminLimiter) wait(client string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t := l.m[client]; t != nil && now.Before(t.until) {
		return t.until.Sub(now)
	}
	return 0
}

func (l *adminLimiter) fail(client string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string]*adminTries{}
	}
	if len(l.m) >= adminMaxTracked {
		for k, t := range l.m {
			if now.Sub(t.last) > adminLockMemory {
				delete(l.m, k)
			}
		}
		if len(l.m) >= adminMaxTracked { // addresses are being sprayed: start over rather than grow without bound
			clear(l.m)
		}
	}
	t := l.m[client]
	if t == nil || now.Sub(t.last) > adminLockMemory {
		t = &adminTries{}
		l.m[client] = t
	}
	t.fails, t.last = t.fails+1, now
	if t.fails >= adminFreeTries {
		t.until = now.Add(min(adminLockBase<<min(t.fails-adminFreeTries, 10), adminLockMax))
	}
}

func (l *adminLimiter) clear(client string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, client)
}

// adminClient is the address a request is limited by: its peer, an IPv6 one by its /64. A proxy's own address counts
// as the peer, so behind one every admin shares a limit; X-Forwarded-For is not trusted.
func adminClient(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	if ip = ip.Unmap(); ip.Is6() {
		p, _ := ip.Prefix(64)
		return p.String()
	}
	return ip.String()
}

// adminSeconds is d in whole seconds, rounded up.
func adminSeconds(d time.Duration) int { return int((d + time.Second - 1) / time.Second) }

// adminWaitText says how long to wait in words.
func adminWaitText(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d seconds", adminSeconds(d))
	}
	return fmt.Sprintf("%d minutes", (adminSeconds(d)+59)/60)
}

func (s *server) adminLoginPage(w http.ResponseWriter, r *http.Request, _ string) {
	if _, ok := s.adminSession(r); ok {
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
		return
	}
	s.adminRender(w, http.StatusOK, "login", "", "Sign in", "", nil)
}

// adminPasswordOK compares in constant time; hashing first hides the password's length.
func (s *server) adminPasswordOK(pw string) bool {
	a, b := sha256.Sum256([]byte(pw)), sha256.Sum256([]byte(s.Cfg.AdminPassword))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func (s *server) adminLogin(w http.ResponseWriter, r *http.Request, _ string) {
	client, now := adminClient(r), s.Now()
	if d := s.adminLimit.wait(client, now); d > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(adminSeconds(d)))
		s.adminRender(w, http.StatusTooManyRequests, "login", "", "Sign in", "", "Too many wrong passwords. Try again in "+adminWaitText(d)+".")
		return
	}
	if !s.adminPasswordOK(r.PostForm.Get("password")) {
		s.adminLimit.fail(client, now)
		s.log.Warn("admin: sign-in failed", "client", client)
		s.adminRender(w, http.StatusUnauthorized, "login", "", "Sign in", "", "That password is not right.")
		return
	}
	s.adminLimit.clear(client)
	c, err := s.adminNewCookie()
	if err != nil {
		s.log.Error("admin: session failed", "err", err)
		s.adminNotice(w, http.StatusInternalServerError, "", "Something went wrong", "The hub could not start your session. Try again.", "/admin/login", "Back to sign in")
		return
	}
	http.SetCookie(w, c)
	s.log.Info("admin: signed in", "client", client)
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (s *server) adminLogout(w http.ResponseWriter, r *http.Request, _ string) {
	http.SetCookie(w, &http.Cookie{
		Name: adminCookie, Path: "/admin", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	s.log.Info("admin: signed out", "client", adminClient(r))
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (s *server) adminHome(w http.ResponseWriter, _ *http.Request, csrf string) {
	s.adminRender(w, http.StatusOK, "home", "home", "Home", csrf, nil)
}

// adminAsset serves one embedded file.
func adminAsset(file, contentType string) adminHandler {
	data, err := adminFS.ReadFile("admin_ui/" + file)
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
	}
}

type adminNavItem struct{ Label, Href, Key string }

var adminNav = []adminNavItem{
	{"Home", "/admin/", "home"},
	{"Pair a person", "/admin/pair", "pair"},
	{"People", "/admin/people", "people"},
	{"API keys", "/admin/keys", "keys"},
}

// adminPage is what every template receives: the page frame's fields and the page's own Data.
type adminPage struct {
	Title, Active, Hub, CSRF string
	SignedIn                 bool
	Nav                      []adminNavItem
	Data                     any
}

var adminTmpl = func() map[string]*template.Template {
	m := map[string]*template.Template{}
	for _, name := range []string{"login", "home", "notice", "pair", "pair_done", "people", "person_remove", "keys", "key_new", "key_revoke"} {
		m[name] = template.Must(template.ParseFS(adminFS, "admin_ui/layout.html", "admin_ui/"+name+".html"))
	}
	return m
}()

// adminRender writes a page. active names the nav entry to mark; csrf is "" for a page without a session, which then
// has no navigation.
func (s *server) adminRender(w http.ResponseWriter, code int, name, active, title, csrf string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	p := adminPage{Title: title, Active: active, Hub: s.Cfg.DisplayName, CSRF: csrf, SignedIn: csrf != "", Nav: adminNav, Data: data}
	if err := adminTmpl[name].ExecuteTemplate(w, "layout.html", p); err != nil {
		s.log.Error("admin: render failed", "page", name, "err", err)
	}
}

type adminNoticeData struct{ Message, Href, Label string }

// adminNotice shows a short message with one link on.
func (s *server) adminNotice(w http.ResponseWriter, code int, csrf, title, msg, href, label string) {
	s.adminRender(w, code, "notice", "", title, csrf, adminNoticeData{msg, href, label})
}

// adminTime formats a time for people.
func adminTime(t time.Time) string { return t.UTC().Format("2 Jan 2006, 15:04 UTC") }

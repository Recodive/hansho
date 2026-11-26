package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"

	"github.com/recodive/hansho/hub/internal/config"
	"github.com/recodive/hansho/hub/internal/identity"
)

func TestPublicHubInfo(t *testing.T) {
	e := newEnvCfg(t, func(c *config.Config) { c.HubID = "hub_abc" })
	w := e.do("GET", "/device/v1/hub", nil, nil) // unauthenticated
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	got := decode[map[string]string](t, w)
	pub := e.hubKey.Public().(ed25519.PublicKey)
	raw, err := base64.RawURLEncoding.DecodeString(got["public_key"])
	if err != nil || string(raw) != string(pub) || len(raw) != 32 {
		t.Fatalf("public_key %q", got["public_key"])
	}
	if got["hub_id"] != "hub_abc" || got["name"] != "Example Hub" || got["domain"] != "hub.example.com" ||
		got["fingerprint"] != identity.Fingerprint(pub) || len(got) != 5 {
		t.Fatalf("info %v", got)
	}
}

func TestRedeemHubInfoIncludesHubID(t *testing.T) {
	e := newEnvCfg(t, func(c *config.Config) { c.HubID = "hub_abc" })
	w := e.api("POST", "/api/v1/pairing-tickets", map[string]string{"external_user_id": "bob"})
	tk := decode[struct{ Ticket string }](t, w).Ticket
	w = e.do("POST", "/device/v1/pairings", e.newDevice("x").redeemBody(tk), nil)
	hub := decode[struct {
		Hub struct {
			HubID string `json:"hub_id"`
		} `json:"hub"`
	}](t, w).Hub
	if hub.HubID != "hub_abc" {
		t.Fatalf("hub_id = %q", hub.HubID)
	}
}

// The hub is an API. Someone who opens its address in a browser gets a small page saying what it is, who runs it and
// where to learn more, instead of a bare 404. It shows only what /device/v1/hub already makes public.
func TestRootShowsAStatusPage(t *testing.T) {
	e := newEnvCfg(t, func(c *config.Config) { c.ChallengeToken = "super-secret-challenge-token" })
	w := e.do("GET", "/", nil, nil)
	if w.Code != 200 {
		t.Fatalf("GET /: %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type %q", ct)
	}
	body := w.Body.String()
	plain := regexp.MustCompile(`</span><span>`).ReplaceAllString(body, "")
	for _, want := range []string{"Example Hub", "hub.example.com", "https://hansho.app", ">hansho.app<", "no web interface", "https://recodive.com/"} {
		if !strings.Contains(body, want) {
			t.Fatalf("page does not mention %q:\n%s", want, body)
		}
	}
	if !strings.Contains(plain, identity.Fingerprint(e.hubKey.Public().(ed25519.PublicKey))) {
		t.Fatalf("page lacks the fingerprint:\n%s", body)
	}
	for _, secret := range []string{"super-secret-challenge-token", e.apiKey} {
		if strings.Contains(body, secret) {
			t.Fatalf("page leaks a secret: %q", secret)
		}
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("missing security headers: %v", w.Header())
	}
	for _, p := range []string{"/nope", "/api", "/robots.txt"} {
		if w := e.do("GET", p, nil, nil); w.Code != 404 {
			t.Fatalf("GET %s: %d, want 404", p, w.Code)
		}
	}
}

func TestStatusPageEscapesTheDisplayName(t *testing.T) {
	e := newEnvCfg(t, func(c *config.Config) { c.DisplayName = `<script>alert("x")</script> & Co` })
	body := e.do("GET", "/", nil, nil).Body.String()
	if strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("display name not escaped:\n%s", body)
	}
}

func TestStatusPageLinksToTheConfiguredHomeURL(t *testing.T) {
	e := newEnvCfg(t, func(c *config.Config) { c.HomeURL = "https://example.org/about" })
	if body := e.do("GET", "/", nil, nil).Body.String(); !strings.Contains(body, "https://example.org/about") {
		t.Fatalf("page ignores HomeURL:\n%s", body)
	}
}

// /health is the liveness endpoint (the project's convention); the old /healthz is gone on purpose (no alias).
func TestHealthEndpoint(t *testing.T) {
	e := newEnv(t)
	if w := e.do("GET", "/health", nil, nil); w.Code != 200 || w.Body.String() != "ok" {
		t.Fatalf("GET /health: %d %q", w.Code, w.Body)
	}
	if w := e.do("GET", "/healthz", nil, nil); w.Code != 404 {
		t.Fatalf("GET /healthz: %d, want 404", w.Code)
	}
}

func TestStatusPageServesItsFontsAndIconsFromTheHub(t *testing.T) {
	e := newEnv(t)
	for p, ct := range map[string]string{
		"/_hub/display.woff2": "font/woff2", "/_hub/body.woff2": "font/woff2", "/_hub/favicon.svg": "image/svg+xml",
		"/_hub/apple-touch-icon.png": "image/png", "/favicon.ico": "image/x-icon",
	} {
		w := e.do("GET", p, nil, nil)
		if w.Code != 200 || w.Header().Get("Content-Type") != ct || w.Body.Len() < 500 {
			t.Fatalf("GET %s: %d %q (%d bytes)", p, w.Code, w.Header().Get("Content-Type"), w.Body.Len())
		}
	}
	page := e.do("GET", "/", nil, nil).Body.String()
	for _, link := range []string{`href="/favicon.ico"`, `href="/_hub/favicon.svg"`, `href="/_hub/apple-touch-icon.png"`} {
		if !strings.Contains(page, link) {
			t.Fatalf("page has no %s", link)
		}
	}
	csp := e.do("GET", "/", nil, nil).Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "font-src 'self'") || strings.Contains(csp, "http") {
		t.Fatalf("CSP %q", csp)
	}
}

func TestLogoIsServedAndNamedInTheInfoOnlyWhenConfigured(t *testing.T) {
	e := newEnvCfg(t, nil)
	if w := e.do("GET", "/_hub/logo.png", nil, nil); w.Code != 404 {
		t.Fatalf("no logo: %d", w.Code)
	}
	if got := decode[map[string]string](t, e.do("GET", "/device/v1/hub", nil, nil)); got["logo"] != "" || len(got) != 5 {
		t.Fatalf("info without a logo %v", got)
	}

	png := []byte("\x89PNG stand-in")
	e = newEnvCfg(t, func(c *config.Config) { c.Logo = png })
	w := e.do("GET", "/_hub/logo.png", nil, nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), png) {
		t.Fatalf("logo: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	sum := sha256.Sum256(png)
	if got := decode[map[string]string](t, e.do("GET", "/device/v1/hub", nil, nil)); got["logo"] != hex.EncodeToString(sum[:8]) {
		t.Fatalf("info %v", got)
	}
}

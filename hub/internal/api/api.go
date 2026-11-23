// Package api is the hub's HTTP surface: the sender API (/api/v1, API-key auth)
// and the device API (/device/v1, device-signature auth). Handlers never log
// message content, tokens or keys.
package api

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/recodive/hansho/hub/internal/brand"
	"github.com/recodive/hansho/hub/internal/config"
	"github.com/recodive/hansho/hub/internal/delivery"
	"github.com/recodive/hansho/hub/internal/identity"
	"github.com/recodive/hansho/hub/internal/store"
	"github.com/recodive/hansho/hub/internal/webhook"
)

// RelayClient is the subset of the relay client the API needs.
type RelayClient = delivery.RelayClient

// Deps are the API's collaborators.
type Deps struct {
	Cfg      config.Config
	Store    *store.Store
	Key      ed25519.PrivateKey
	Relay    RelayClient
	Webhooks *webhook.Dispatcher
	Now      func() time.Time
}

const (
	// maxJSONBody bounds ordinary JSON request bodies.
	maxJSONBody = 64 << 10
	// ticketTTL is the pairing ticket lifetime.
	ticketTTL = 10 * time.Minute
)

type server struct {
	Deps
	log    *slog.Logger
	dw     *delivery.Worker // used for pushes triggered by requests (cancel, activities, feeds)
	pub    ed25519.PublicKey
	fp     string
	domain string
	logo   string // first 16 hex digits of the logo's SHA-256, "" without a logo

	actionLimit actionLimiter // per-device press rate for POST /device/v1/actions

	adminKey   []byte       // signs admin sessions; set when HUB_ADMIN is on
	adminLimit adminLimiter // wrong admin passwords per address
}

// New returns the hub's HTTP handler.
func New(d Deps) http.Handler {
	if d.Now == nil {
		d.Now = d.Store.Now
	} else {
		d.Store.Now = d.Now
	}
	s := &server{Deps: d, log: slog.Default()}
	s.dw = delivery.NewWorker(d.Store, d.Relay, d.Webhooks, d.Cfg, d.Key, d.Now)
	s.pub = d.Key.Public().(ed25519.PublicKey)
	s.fp = identity.Fingerprint(s.pub)
	if u, err := url.Parse(d.Cfg.PublicURL); err == nil {
		s.domain = u.Hostname()
	}

	if len(d.Cfg.Logo) > 0 {
		sum := sha256.Sum256(d.Cfg.Logo)
		s.logo = hex.EncodeToString(sum[:8])
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /_hub/logo.png", s.serveLogo)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok")
	})
	mux.HandleFunc("GET "+brand.ChallengePath, s.challenge)
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /_hub/display.woff2", homeAsset("BigShouldersDisplay-ExtraBold.woff2", "font/woff2"))
	mux.HandleFunc("GET /_hub/body.woff2", homeAsset("atkinson-hyperlegible-400.woff2", "font/woff2"))
	mux.HandleFunc("GET /_hub/favicon.svg", homeAsset("favicon.svg", "image/svg+xml"))
	mux.HandleFunc("GET /_hub/apple-touch-icon.png", homeAsset("apple-touch-icon.png", "image/png"))
	mux.HandleFunc("GET /favicon.ico", homeAsset("favicon.ico", "image/x-icon"))

	// Sender API.
	mux.HandleFunc("POST /api/v1/pairing-tickets", s.withAPIKey(s.createTicket))
	mux.HandleFunc("DELETE /api/v1/users/{external_user_id}/pairings", s.withAPIKey(s.unpairUser))

	mux.HandleFunc("GET /api/v1/channels", s.withAPIKey(s.listChannels))
	mux.HandleFunc("PUT /api/v1/channels/{id}", s.withAPIKey(s.putChannel))
	mux.HandleFunc("GET /api/v1/channels/{id}", s.withAPIKey(s.getChannel))
	mux.HandleFunc("DELETE /api/v1/channels/{id}", s.withAPIKey(s.deleteChannel))
	mux.HandleFunc("GET /api/v1/templates", s.withAPIKey(s.listTemplates))
	mux.HandleFunc("PUT /api/v1/templates/{id}", s.withAPIKey(s.putTemplate))
	mux.HandleFunc("GET /api/v1/templates/{id}", s.withAPIKey(s.getTemplate))
	mux.HandleFunc("DELETE /api/v1/templates/{id}", s.withAPIKey(s.deleteTemplate))
	mux.HandleFunc("GET /api/v1/sounds", s.withAPIKey(s.listSounds))
	mux.HandleFunc("PUT /api/v1/sounds/{id}", s.withAPIKey(s.putSound))
	mux.HandleFunc("GET /api/v1/sounds/{id}", s.withAPIKey(s.getSound))
	mux.HandleFunc("DELETE /api/v1/sounds/{id}", s.withAPIKey(s.deleteSound))
	mux.HandleFunc("GET /api/v1/groups", s.withAPIKey(s.listGroups))
	mux.HandleFunc("PUT /api/v1/groups/{id}", s.withAPIKey(s.putGroup))
	mux.HandleFunc("GET /api/v1/groups/{id}", s.withAPIKey(s.getGroup))
	mux.HandleFunc("DELETE /api/v1/groups/{id}", s.withAPIKey(s.deleteGroup))
	mux.HandleFunc("PUT /api/v1/feeds/{id}", s.withAPIKey(s.putFeed))
	mux.HandleFunc("GET /api/v1/feeds/{id}", s.withAPIKey(s.getFeedMeta))
	mux.HandleFunc("POST /api/v1/activities", s.withAPIKey(s.startActivity))
	mux.HandleFunc("PATCH /api/v1/activities/{id}", s.withAPIKey(s.updateActivity))
	mux.HandleFunc("DELETE /api/v1/activities/{id}", s.withAPIKey(s.endActivity))
	mux.HandleFunc("POST /api/v1/messages", s.withAPIKey(s.sendMessage))
	mux.HandleFunc("GET /api/v1/messages/{id}", s.withAPIKey(s.getMessage))

	// Device API.
	mux.HandleFunc("POST /device/v1/activities/{id}/token", s.withDevice(s.activityToken))
	mux.HandleFunc("GET /device/v1/feeds", s.withDevice(s.listFeeds))
	mux.HandleFunc("GET /device/v1/feeds/{id}", s.withDevice(s.getFeed))
	mux.HandleFunc("POST /device/v1/actions", s.withDevice(s.deviceAction))
	mux.HandleFunc("GET /device/v1/acks", s.withDevice(s.deviceAcks))
	mux.HandleFunc("GET /device/v1/sync", s.withDevice(s.sync))
	mux.HandleFunc("GET /device/v1/sounds/{id}", s.withDevice(s.downloadSound))
	mux.HandleFunc("GET /device/v1/hub", s.publicHubInfo)
	mux.HandleFunc("POST /device/v1/pairings", s.redeemTicket)
	mux.HandleFunc("POST /device/v1/pairings/{id}/devices", s.addDevice)
	mux.HandleFunc("PUT /device/v1/devices/{id}/tokens", s.withDevice(s.updateTokens))
	mux.HandleFunc("DELETE /device/v1/pairings/{id}", s.withDevice(s.unpairDevice))
	if d.Cfg.Admin {
		s.mountAdmin(mux)
	}
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	// No HTML escaping: signed feed data must reach the device byte for byte.
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, errCode string) {
	writeJSON(w, code, map[string]string{"error": errCode})
}

// writeFieldErr reports a validation failure with the path of the offending field.
func writeFieldErr(w http.ResponseWriter, code int, errCode, field, msg string) {
	writeJSON(w, code, map[string]string{"error": errCode, "field": field, "message": msg})
}

// apiError is a failed request in the API's terms, so a handler and the admin UI can share the code that fails. A
// validation failure names the field and why (400 invalid_request); Err is an internal failure to log (500); other
// failures carry their own status and error code.
type apiError struct {
	Status     int
	Code       string
	Field, Msg string
	What       string
	Err        error
}

func badField(field, msg string) *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: "invalid_request", Field: field, Msg: msg}
}

func internalErr(what string, err error) *apiError {
	return &apiError{Status: http.StatusInternalServerError, Code: "internal", What: what, Err: err}
}

// fail answers a request with e.
func (s *server) fail(w http.ResponseWriter, e *apiError) {
	switch {
	case e.Err != nil:
		s.internal(w, e.What, e.Err)
	case e.Field != "":
		writeFieldErr(w, e.Status, e.Code, e.Field, e.Msg)
	default:
		writeErr(w, e.Status, e.Code)
	}
}

func (s *server) internal(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	writeErr(w, http.StatusInternalServerError, "internal")
}

// readBody reads the size-limited body. On failure it writes the response.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge, "body_too_large")
		} else {
			writeErr(w, http.StatusBadRequest, "bad_request")
		}
		return nil, false
	}
	return b, true
}

// decodeJSON unmarshals body into v, writing a 400 on failure.
func decodeJSON(w http.ResponseWriter, body []byte, v any) bool {
	if err := json.Unmarshal(body, v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return false
	}
	return true
}

// decodeB64 accepts standard or URL-safe base64, padded or not.
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	s = strings.NewReplacer("+", "-", "/", "_").Replace(s)
	return base64.RawURLEncoding.DecodeString(s)
}

var b64url = base64.RawURLEncoding

func (s *server) challenge(w http.ResponseWriter, _ *http.Request) {
	if s.Cfg.ChallengeToken == "" {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, s.Cfg.ChallengeToken)
}

// hubInfo is the hub description returned to devices.
func (s *server) hubInfo() map[string]string {
	info := map[string]string{
		"hub_id": s.Cfg.HubID, "name": s.Cfg.DisplayName, "domain": s.domain,
		"public_key":  b64url.EncodeToString(s.pub),
		"fingerprint": s.fp,
	}
	if s.logo != "" {
		info["logo"] = s.logo // the app downloads GET /_hub/logo.png when it does not hold this one
	}
	return info
}

func (s *server) serveLogo(w http.ResponseWriter, _ *http.Request) {
	if s.logo == "" {
		http.NotFound(w, nil)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(s.Cfg.Logo)
}

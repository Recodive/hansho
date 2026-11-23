// Package config loads the hub's environment configuration.
package config

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// DefaultRelayURL is the relay Hansho runs. HUB_RELAY_URL overrides it only for testing against another relay.
const DefaultRelayURL = "https://relay.hansho.app"

// Config is the hub's runtime configuration.
type Config struct {
	Addr, PublicURL, DisplayName, DBPath, RelayURL, HubID, KeyPath, WebhookURL string
	MasterKey, WebhookSecret                                                   []byte
	ChallengeToken                                                             string
	// HomeURL is the project site the hub's status page links to (HUB_HOME_URL, default https://hansho.app). https only.
	HomeURL string
	// Logo is the PNG at HUB_LOGO_FILE (at most MaxLogoBytes, 64 to 1024 px each way), or nil. The app shows it next to
	// the hub's name once the person has paired.
	Logo []byte
	// LAEncrypt selects encrypted Live Activity content-state (HUB_LA_ENCRYPT). Default true: the app's activity key
	// mirror was verified on a device with the app force-quit (2026-11-08). Set false for plaintext content state.
	LAEncrypt bool
	// Admin turns on the admin UI under /admin (HUB_ADMIN). AdminPassword is its shared sign-in (HUB_ADMIN_PASSWORD, at
	// least MinAdminPassword characters, required with Admin).
	Admin         bool
	AdminPassword string
}

// MinAdminPassword is the shortest HUB_ADMIN_PASSWORD the hub accepts, in characters.
const MinAdminPassword = 12

// Load reads configuration through getenv and validates it.
func Load(getenv func(string) string) (Config, error) {
	def := func(k, d string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return d
	}
	c := Config{
		HomeURL:        strings.TrimRight(def("HUB_HOME_URL", "https://hansho.app"), "/"),
		Addr:           def("HUB_ADDR", ":8090"),
		PublicURL:      strings.TrimRight(getenv("HUB_PUBLIC_URL"), "/"),
		DisplayName:    getenv("HUB_DISPLAY_NAME"),
		DBPath:         def("HUB_DB_PATH", "./hub.db"),
		RelayURL:       strings.TrimRight(def("HUB_RELAY_URL", DefaultRelayURL), "/"),
		HubID:          getenv("HUB_ID"),
		KeyPath:        def("HUB_KEY_PATH", "./hub.key"),
		WebhookURL:     getenv("HUB_WEBHOOK_URL"),
		ChallengeToken: getenv("HUB_CHALLENGE_TOKEN"),
	}
	if s := getenv("HUB_WEBHOOK_SECRET"); s != "" {
		c.WebhookSecret = []byte(s)
	}
	c.LAEncrypt = true
	switch strings.ToLower(getenv("HUB_LA_ENCRYPT")) {
	case "", "1", "true":
	case "0", "false":
		c.LAEncrypt = false
	default:
		return Config{}, errors.New("config: HUB_LA_ENCRYPT must be true or false")
	}
	switch strings.ToLower(getenv("HUB_ADMIN")) {
	case "", "0", "false":
	case "1", "true":
		c.Admin = true
		c.AdminPassword = getenv("HUB_ADMIN_PASSWORD")
		if n := utf8.RuneCountInString(c.AdminPassword); n < MinAdminPassword {
			return Config{}, fmt.Errorf("config: HUB_ADMIN_PASSWORD must be at least %d characters when HUB_ADMIN is on", MinAdminPassword)
		}
	default:
		return Config{}, errors.New("config: HUB_ADMIN must be true or false")
	}
	if getenv("HUB_ADMIN_ADDR") != "" {
		return Config{}, errors.New("config: HUB_ADMIN_ADDR is not supported yet: the admin UI is served on HUB_ADDR")
	}
	for _, r := range []struct{ k, v string }{
		{"HUB_PUBLIC_URL", c.PublicURL},
		{"HUB_DISPLAY_NAME", c.DisplayName},
		{"HUB_MASTER_KEY", getenv("HUB_MASTER_KEY")},
	} {
		if r.v == "" {
			return Config{}, fmt.Errorf("config: %s is required", r.k)
		}
	}
	if hu, err := url.Parse(c.HomeURL); err != nil || hu.Scheme != "https" || hu.Host == "" {
		return Config{}, errors.New("config: HUB_HOME_URL must be an https URL with a host")
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return Config{}, errors.New("config: HUB_PUBLIC_URL must be an https URL with a host")
	}
	if c.WebhookURL != "" {
		if err := checkWebhook(c.WebhookURL, len(c.WebhookSecret) > 0); err != nil {
			return Config{}, err
		}
	}
	switch f, u := getenv("HUB_LOGO_FILE"), getenv("HUB_LOGO_URL"); {
	case f != "" && u != "":
		return Config{}, errors.New("config: set HUB_LOGO_FILE or HUB_LOGO_URL, not both")
	case f != "":
		if c.Logo, err = loadLogo(f); err != nil {
			return Config{}, err
		}
	case u != "":
		// A logo that cannot be fetched must not keep the hub from starting: it runs without one until the next start.
		if c.Logo, err = fetchLogo(u); err != nil {
			slog.Warn("hub logo skipped", "err", err)
		}
	}
	mk, err := base64.StdEncoding.DecodeString(getenv("HUB_MASTER_KEY"))
	if err != nil {
		mk, err = base64.RawStdEncoding.DecodeString(getenv("HUB_MASTER_KEY"))
	}
	if err != nil {
		return Config{}, errors.New("config: HUB_MASTER_KEY is not valid base64")
	}
	if len(mk) != 32 {
		return Config{}, fmt.Errorf("config: HUB_MASTER_KEY must decode to 32 bytes, got %d", len(mk))
	}
	c.MasterKey = mk
	return c, nil
}

// MaxLogoBytes bounds the logo the hub serves and the app downloads.
const MaxLogoBytes = 256 << 10

func loadLogo(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: HUB_LOGO_FILE: %w", err)
	}
	if len(b) > MaxLogoBytes {
		return nil, fmt.Errorf("config: HUB_LOGO_FILE is %d bytes, at most %d", len(b), MaxLogoBytes)
	}
	if err := checkLogo(b); err != nil {
		return nil, fmt.Errorf("config: HUB_LOGO_FILE: %w", err)
	}
	return b, nil
}

func checkLogo(b []byte) error {
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return errors.New("must be a PNG")
	}
	if cfg.Width < 64 || cfg.Height < 64 || cfg.Width > 1024 || cfg.Height > 1024 {
		return fmt.Errorf("is %dx%d, it must be 64 to 1024 pixels each way", cfg.Width, cfg.Height)
	}
	return nil
}

// logoClient fetches HUB_LOGO_URL; a variable so a test can trust its own server.
var logoClient = &http.Client{Timeout: 10 * time.Second}

// fetchLogo downloads the logo once, at start. The hub serves it from then on, so devices never contact the logo's host.
func fetchLogo(raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("config: HUB_LOGO_URL must be an https URL with a host")
	}
	resp, err := logoClient.Get(raw)
	if err != nil {
		return nil, fmt.Errorf("config: HUB_LOGO_URL: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("config: HUB_LOGO_URL answered %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxLogoBytes+1))
	if err != nil {
		return nil, fmt.Errorf("config: HUB_LOGO_URL: %w", err)
	}
	if len(b) > MaxLogoBytes {
		return nil, fmt.Errorf("config: HUB_LOGO_URL is larger than %d bytes", MaxLogoBytes)
	}
	if err := checkLogo(b); err != nil {
		return nil, fmt.Errorf("config: HUB_LOGO_URL %w", err)
	}
	return b, nil
}

// checkWebhook requires a signing secret and an https URL, except for a destination that stays inside the cluster or
// host: a loopback address, a single-label name (a Kubernetes service in the same namespace, a compose service) or a
// .svc / .cluster.local name.
func checkWebhook(raw string, hasSecret bool) error {
	if !hasSecret {
		return errors.New("config: HUB_WEBHOOK_SECRET is required when HUB_WEBHOOK_URL is set")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("config: HUB_WEBHOOK_URL must be an http(s) URL with a host")
	}
	if u.Scheme == "http" && !internalHost(u.Hostname()) {
		return errors.New("config: HUB_WEBHOOK_URL must be https unless its host is loopback or an in-cluster name")
	}
	return nil
}

func internalHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return h == "localhost" || !strings.Contains(h, ".") || strings.HasSuffix(h, ".svc") || strings.HasSuffix(h, ".cluster.local")
}

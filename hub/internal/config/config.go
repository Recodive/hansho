// Package config loads the hub's environment configuration.
package config

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"
	"net"
	"net/url"
	"os"
	"strings"
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
}

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
	if f := getenv("HUB_LOGO_FILE"); f != "" {
		if c.Logo, err = loadLogo(f); err != nil {
			return Config{}, err
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
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("config: HUB_LOGO_FILE must be a PNG")
	}
	if cfg.Width < 64 || cfg.Height < 64 || cfg.Width > 1024 || cfg.Height > 1024 {
		return nil, fmt.Errorf("config: HUB_LOGO_FILE is %dx%d, it must be 64 to 1024 pixels each way", cfg.Width, cfg.Height)
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

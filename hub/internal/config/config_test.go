package config

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validEnv() map[string]string {
	return map[string]string{
		"HUB_PUBLIC_URL":   "https://hub.example.com",
		"HUB_DISPLAY_NAME": "Example",
		"HUB_RELAY_URL":    "https://relay.example.com",
		"HUB_MASTER_KEY":   base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8090" || c.DBPath != "./hub.db" || c.KeyPath != "./hub.key" {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if len(c.MasterKey) != 32 {
		t.Fatalf("master key len %d", len(c.MasterKey))
	}
	if c.PublicURL != "https://hub.example.com" || c.DisplayName != "Example" {
		t.Fatalf("%+v", c)
	}
	if !c.LAEncrypt {
		t.Fatal("HUB_LA_ENCRYPT must default to true (verified on device 2026-11-08)")
	}
}

func TestLoadMissingRequired(t *testing.T) {
	for _, k := range []string{"HUB_PUBLIC_URL", "HUB_DISPLAY_NAME", "HUB_MASTER_KEY"} {
		m := validEnv()
		delete(m, k)
		_, err := Load(env(m))
		if err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s: want error naming it, got %v", k, err)
		}
	}
}

func TestRelayURLDefaultsToHanshoRelay(t *testing.T) {
	m := validEnv()
	delete(m, "HUB_RELAY_URL")
	if c, err := Load(env(m)); err != nil || c.RelayURL != DefaultRelayURL {
		t.Fatalf("default RelayURL %q (%v)", c.RelayURL, err)
	}
}

func TestLoadRejectsNonHTTPSPublicURL(t *testing.T) {
	for _, u := range []string{"http://hub.example.com", "hub.example.com", "https://"} {
		m := validEnv()
		m["HUB_PUBLIC_URL"] = u
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "HUB_PUBLIC_URL") {
			t.Errorf("%q: want HUB_PUBLIC_URL error, got %v", u, err)
		}
	}
}

func TestLoadMasterKeyLength(t *testing.T) {
	for _, n := range []int{16, 31, 33} {
		m := validEnv()
		m["HUB_MASTER_KEY"] = base64.StdEncoding.EncodeToString(make([]byte, n))
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "HUB_MASTER_KEY") {
			t.Errorf("len %d: want HUB_MASTER_KEY error, got %v", n, err)
		}
	}
	m := validEnv()
	m["HUB_MASTER_KEY"] = "!!!not base64!!!"
	if _, err := Load(env(m)); err == nil {
		t.Error("invalid base64 accepted")
	}
}

func TestLoadOptionalFields(t *testing.T) {
	m := validEnv()
	m["HUB_ADDR"] = ":9000"
	m["HUB_ID"] = "hub_abc"
	m["HUB_WEBHOOK_URL"] = "https://backend/hooks"
	m["HUB_WEBHOOK_SECRET"] = "s3cret"
	m["HUB_CHALLENGE_TOKEN"] = "tok"
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":9000" || c.HubID != "hub_abc" || c.WebhookURL != "https://backend/hooks" ||
		string(c.WebhookSecret) != "s3cret" || c.ChallengeToken != "tok" {
		t.Fatalf("%+v", c)
	}
}

func TestLAEncryptCanBeDisabledExplicitly(t *testing.T) {
	for _, v := range []string{"false", "0", "FALSE"} {
		m := validEnv()
		m["HUB_LA_ENCRYPT"] = v
		c, err := Load(env(m))
		if err != nil {
			t.Fatal(err)
		}
		if c.LAEncrypt {
			t.Fatalf("HUB_LA_ENCRYPT=%q should disable encryption", v)
		}
	}
}

func TestHomeURLDefaultsAndMustBeHTTPS(t *testing.T) {
	c, err := Load(env(validEnv()))
	if err != nil || c.HomeURL != "https://hansho.app" {
		t.Fatalf("default HomeURL %q (%v)", c.HomeURL, err)
	}
	m := validEnv()
	m["HUB_HOME_URL"] = "https://example.org/about"
	if c, err := Load(env(m)); err != nil || c.HomeURL != "https://example.org/about" {
		t.Fatalf("custom HomeURL %q (%v)", c.HomeURL, err)
	}
	for _, bad := range []string{"javascript:alert(1)", "http://example.org", "//example.org", "not a url", "ftp://example.org"} {
		m := validEnv()
		m["HUB_HOME_URL"] = bad
		if _, err := Load(env(m)); err == nil {
			t.Fatalf("HUB_HOME_URL=%q was accepted", bad)
		}
	}
}

func TestWebhookURLNeedsSecretAndHTTPS(t *testing.T) {
	ok := []string{
		"https://backend.example.com/hook",
		"http://127.0.0.1:8080/hook", "http://localhost:8080/hook", "http://[::1]:8080/hook",
		"http://hansho-demo:8080/hooks/hansho",
		"http://hansho-demo.hansho.svc.cluster.local:8080/hooks/hansho", "http://demo.hansho.svc/hook",
	}
	for _, u := range ok {
		m := validEnv()
		m["HUB_WEBHOOK_URL"], m["HUB_WEBHOOK_SECRET"] = u, "s3cret"
		if _, err := Load(env(m)); err != nil {
			t.Errorf("%q refused: %v", u, err)
		}
	}
	for _, u := range []string{"http://backend.example.com/hook", "http://10.0.0.5/hook", "http://localhost.evil.com/hook",
		"ftp://backend/hook", "https://", "backend.example.com/hook"} {
		m := validEnv()
		m["HUB_WEBHOOK_URL"], m["HUB_WEBHOOK_SECRET"] = u, "s3cret"
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "HUB_WEBHOOK_URL") {
			t.Errorf("%q: want HUB_WEBHOOK_URL error, got %v", u, err)
		}
	}
	m := validEnv()
	m["HUB_WEBHOOK_URL"] = "https://backend.example.com/hook"
	if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "HUB_WEBHOOK_SECRET") {
		t.Errorf("a webhook URL without a secret must be refused, got %v", err)
	}
	if _, err := Load(env(validEnv())); err != nil {
		t.Errorf("no webhook at all stays valid: %v", err)
	}
}

func TestLogoURLIsFetchedOnceAndAFailureDoesNotStopTheHub(t *testing.T) {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 128, 128)))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/logo.png" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b.Bytes())
	}))
	defer srv.Close()
	old := logoClient
	logoClient = srv.Client()
	defer func() { logoClient = old }()

	load := func(m map[string]string) (Config, error) {
		e := validEnv()
		for k, v := range m {
			e[k] = v
		}
		return Load(env(e))
	}
	if c, err := load(map[string]string{"HUB_LOGO_URL": srv.URL + "/logo.png"}); err != nil || !bytes.Equal(c.Logo, b.Bytes()) {
		t.Fatalf("fetch: %v", err)
	}
	if c, err := load(map[string]string{"HUB_LOGO_URL": srv.URL + "/gone.png"}); err != nil || c.Logo != nil {
		t.Fatalf("a 404 must leave the hub running without a logo: %v", err)
	}
	if _, err := load(map[string]string{"HUB_LOGO_URL": "http://insecure.example/logo.png"}); err != nil {
		t.Fatalf("a bad URL is skipped like any failed fetch: %v", err)
	}
	if _, err := load(map[string]string{"HUB_LOGO_URL": srv.URL, "HUB_LOGO_FILE": "x.png"}); err == nil {
		t.Fatal("both set must be an error")
	}
}

func TestLogoFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, w, h int) string {
		var b bytes.Buffer
		if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	load := func(path string) (Config, error) {
		e := validEnv()
		e["HUB_LOGO_FILE"] = path
		return Load(env(e))
	}
	if c, err := load(write("ok.png", 256, 256)); err != nil || len(c.Logo) == 0 {
		t.Fatalf("valid logo: %v", err)
	}
	if c, err := Load(env(validEnv())); err != nil || c.Logo != nil {
		t.Fatalf("no logo: %v", err)
	}
	notPNG := filepath.Join(dir, "x.png")
	_ = os.WriteFile(notPNG, []byte("GIF89a"), 0o600)
	for name, path := range map[string]string{
		"small": write("small.png", 32, 32), "huge": write("huge.png", 2000, 2000), "not a png": notPNG, "missing": filepath.Join(dir, "none.png"),
	} {
		if _, err := load(path); err == nil || !strings.Contains(err.Error(), "HUB_LOGO_FILE") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

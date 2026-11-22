package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/recodive/hansho/hub/internal/config"
	"github.com/recodive/hansho/hub/internal/store"
)

func testEnv(t *testing.T, extra map[string]string) func(string) string {
	t.Helper()
	dir := t.TempDir()
	m := map[string]string{
		"HUB_PUBLIC_URL":   "https://hub.example.com",
		"HUB_DISPLAY_NAME": "Example",
		"HUB_RELAY_URL":    "https://relay.example.com",
		"HUB_MASTER_KEY":   base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
		"HUB_DB_PATH":      filepath.Join(dir, "hub.db"),
		"HUB_KEY_PATH":     filepath.Join(dir, "hub.key"),
		"HUB_ID":           "hub_test",
	}
	for k, v := range extra {
		m[k] = v
	}
	return func(k string) string { return m[k] }
}

func TestAPIKeyCreatePrintsAcceptedKey(t *testing.T) {
	env := testEnv(t, nil)
	var out, errb bytes.Buffer
	if code := run([]string{"apikey", "create", "ci"}, env, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	key := strings.TrimSpace(out.String())
	if key == "" || strings.ContainsAny(key, " \n") {
		t.Fatalf("output should be exactly the key, got %q", out.String())
	}
	cfg, _ := config.Load(env)
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	k, err := st.LookupAPIKey(key)
	if err != nil || k.Name != "ci" {
		t.Fatalf("LookupAPIKey: %+v %v", k, err)
	}
}

func TestAPIKeyListAndRevoke(t *testing.T) {
	env := testEnv(t, nil)
	runOK := func(args ...string) string {
		t.Helper()
		var out, errb bytes.Buffer
		if code := run(args, env, &out, &errb); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errb.String())
		}
		return out.String()
	}
	keyA, keyB := strings.TrimSpace(runOK("apikey", "create", "ci")), strings.TrimSpace(runOK("apikey", "create", "backend"))

	rows := strings.Split(strings.TrimSpace(runOK("apikey", "list")), "\n")
	if len(rows) != 3 || rows[0] != "ID\tNAME\tCREATED\tREVOKED" {
		t.Fatalf("list: %q", rows)
	}
	for i, name := range []string{"ci", "backend"} {
		f := strings.Split(rows[i+1], "\t")
		if len(f) != 4 || f[0] != strconv.Itoa(i+1) || f[1] != name || f[3] != "-" {
			t.Fatalf("row %q", rows[i+1])
		}
		if strings.Contains(rows[i+1], "ck_") {
			t.Fatal("list must not print key material")
		}
	}

	if got := strings.TrimSpace(runOK("apikey", "revoke", "1")); got != "revoked 1" {
		t.Fatalf("revoke: %q", got)
	}
	runOK("apikey", "revoke", "1") // idempotent
	rows = strings.Split(strings.TrimSpace(runOK("apikey", "list")), "\n")
	if f := strings.Split(rows[1], "\t"); f[3] == "-" {
		t.Fatalf("revoked_at not shown: %q", rows[1])
	}
	if f := strings.Split(rows[2], "\t"); f[3] != "-" {
		t.Fatalf("the other key must stay active: %q", rows[2])
	}
	cfg, _ := config.Load(env)
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.LookupAPIKey(keyA); err != store.ErrNotFound {
		t.Fatalf("revoked key still authenticates: %v", err)
	}
	if _, err := st.LookupAPIKey(keyB); err != nil {
		t.Fatalf("other key broken: %v", err)
	}

	var out, errb bytes.Buffer
	if code := run([]string{"apikey", "revoke", "99"}, env, &out, &errb); code != 1 || !strings.Contains(errb.String(), "no API key with id 99") {
		t.Fatalf("unknown id: exit %d %s", code, errb.String())
	}
	errb.Reset()
	if code := run([]string{"apikey", "revoke", "abc"}, env, &out, &errb); code != 2 || !strings.Contains(errb.String(), "usage") {
		t.Fatalf("bad id: exit %d %s", code, errb.String())
	}
}

func TestAPIKeyCreateNeedsName(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"apikey", "create"}, testEnv(t, nil), &out, &errb); code != 2 || !strings.Contains(errb.String(), "usage") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func TestUnknownSubcommandIsUsageError(t *testing.T) {
	for _, args := range [][]string{{"bogus"}, {}, {"apikey", "delete"}, {"relay", "bogus"}, {"relay"}} {
		var out, errb bytes.Buffer
		if code := run(args, testEnv(t, nil), &out, &errb); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
		if !strings.Contains(strings.ToLower(errb.String()), "usage") {
			t.Errorf("%v: no usage on stderr: %q", args, errb.String())
		}
		if out.Len() != 0 {
			t.Errorf("%v: stdout not empty", args)
		}
	}
}

func TestConfigErrorsExitOne(t *testing.T) {
	var out, errb bytes.Buffer
	env := func(string) string { return "" }
	if code := run([]string{"serve"}, env, &out, &errb); code != 1 || !strings.Contains(errb.String(), "HUB_PUBLIC_URL") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func fakeRelay(t *testing.T, verifyStatus int) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path+" hub="+r.Header.Get("X-Hansho-Hub"))
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/hubs":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"domain":"hub.example.com"`) {
				http.Error(w, "bad domain", 400)
				return
			}
			w.WriteHeader(201)
			io.WriteString(w, `{"hub_id":"hub_abc123","challenge":"deadbeef"}`)
		default:
			w.WriteHeader(verifyStatus)
			io.WriteString(w, `{"error":"challenge_mismatch"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &paths
}

func TestRelayRegisterPrintsEnv(t *testing.T) {
	srv, paths := fakeRelay(t, 200)
	var out, errb bytes.Buffer
	code := run([]string{"relay", "register"}, testEnv(t, map[string]string{"HUB_RELAY_URL": srv.URL}), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "HUB_ID=hub_abc123\n") || !strings.Contains(out.String(), "HUB_CHALLENGE_TOKEN=deadbeef\n") {
		t.Fatalf("%q", out.String())
	}
	if len(*paths) != 1 || !strings.HasPrefix((*paths)[0], "POST /v1/hubs ") {
		t.Fatalf("%v", *paths)
	}
}

func TestRelayVerify(t *testing.T) {
	srv, paths := fakeRelay(t, 200)
	env := testEnv(t, map[string]string{"HUB_RELAY_URL": srv.URL, "HUB_ID": "hub_abc123"})
	var out, errb bytes.Buffer
	if code := run([]string{"relay", "verify"}, env, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if len(*paths) != 1 || (*paths)[0] != "POST /v1/hubs/hub_abc123/verify hub=hub_abc123" {
		t.Fatalf("%v", *paths)
	}

	srv2, _ := fakeRelay(t, 422)
	env = testEnv(t, map[string]string{"HUB_RELAY_URL": srv2.URL, "HUB_ID": "hub_abc123"})
	out.Reset()
	errb.Reset()
	if code := run([]string{"relay", "verify"}, env, &out, &errb); code != 1 || !strings.Contains(errb.String(), "422") {
		t.Fatalf("failed verify: exit %d %s", code, errb.String())
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"relay", "verify"}, testEnv(t, map[string]string{"HUB_RELAY_URL": srv.URL, "HUB_ID": ""}), &out, &errb); code != 1 || !strings.Contains(errb.String(), "HUB_ID") {
		t.Fatalf("missing HUB_ID: exit %d %s", code, errb.String())
	}
}

func TestServeAnswersChallengeAndShutsDownGracefully(t *testing.T) {
	env := testEnv(t, map[string]string{"HUB_CHALLENGE_TOKEN": "tok-123"})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, ln, io.Discard) }()

	url := "http://" + ln.Addr().String()
	var body string
	for range 50 {
		resp, err := http.Get(url + "/.well-known/hansho-challenge")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				body = string(b)
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if body != "tok-123" {
		t.Fatalf("challenge body %q", body)
	}
	if resp, err := http.Get(url + "/health"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("health: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not shut down")
	}
}

func TestServeCreatesKeyAndFingerprintIsStable(t *testing.T) {
	env := testEnv(t, nil)
	cfg, _ := config.Load(env)
	run1 := func() string {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		ctx, cancel := context.WithCancel(context.Background())
		var out bytes.Buffer
		done := make(chan error, 1)
		go func() { done <- serve(ctx, cfg, ln, &out) }()
		time.Sleep(200 * time.Millisecond)
		cancel()
		<-done
		return out.String()
	}
	fp := func(s string) string { _, after, _ := strings.Cut(s, "fingerprint="); return strings.TrimSpace(after) }
	a, b := run1(), run1()
	if len(fp(a)) != 64 || fp(a) != fp(b) {
		t.Fatalf("startup output should include a stable fingerprint:\n%q\n%q", a, b)
	}
}

func TestServeRefusesToStartWithoutHubID(t *testing.T) {
	env := testEnv(t, map[string]string{"HUB_ID": ""})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	err = serve(context.Background(), cfg, ln, io.Discard)
	if err == nil {
		t.Fatal("serve started without HUB_ID")
	}
	for _, want := range []string{"HUB_ID", "hub relay register"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q should mention %q", err, want)
		}
	}
	// Nothing may have been created before the check failed.
	if _, statErr := os.Stat(cfg.KeyPath); !os.IsNotExist(statErr) {
		t.Fatalf("hub key was created despite the startup error: %v", statErr)
	}
}

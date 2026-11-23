package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewChecksEndpoint(t *testing.T) {
	for endpoint, ok := range map[string]bool{
		"https://hub.example.com": true, "https://hub.example.com/": true, "http://127.0.0.1:8080": true, "http://localhost": true,
		"http://hub.example.com": false, "hub.example.com": false, "ftp://hub.example.com": false, "https://": false, "": false,
	} {
		if _, err := New(endpoint, "key"); (err == nil) != ok {
			t.Errorf("New(%q): err = %v, want ok = %v", endpoint, err, ok)
		}
	}
	if _, err := New("https://hub.example.com", ""); err == nil {
		t.Error("an empty api key was accepted")
	}
}

func TestRequestsAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-key" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"unauthorized"}`)
			return
		}
		switch {
		case r.Method == "PUT" && r.URL.Path == "/api/v1/sounds/ding":
			b, _ := io.ReadAll(r.Body)
			if r.Header.Get("Content-Type") != "audio/wav" || string(b) != "RIFF" {
				t.Errorf("sound upload: content type %q body %q", r.Header.Get("Content-Type"), b)
			}
			io.WriteString(w, `{"id":"ding","sha256":"abc","size":4}`)
		case r.URL.Path == "/api/v1/groups/gone":
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"unknown_group"}`)
		case r.URL.Path == "/api/v1/channels/bad":
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"invalid_request","field":"default_level","message":"must not exceed max_level"}`)
		case r.URL.Path == "/api/v1/templates/html":
			w.WriteHeader(502)
			io.WriteString(w, "<html>bad gateway</html>")
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	c, err := New(srv.URL, "secret-key")
	if err != nil {
		t.Fatal(err)
	}

	if sum, size, err := c.PutSound(ctx, "ding", "audio/wav", []byte("RIFF")); err != nil || sum != "abc" || size != 4 {
		t.Errorf("PutSound = %q %d %v", sum, size, err)
	}

	_, err = c.GetGroup(ctx, "gone")
	if !IsNotFound(err) {
		t.Errorf("GetGroup of a missing group: %v is not a not-found", err)
	}

	_, err = c.PutChannel(ctx, "bad", Channel{})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Field != "default_level" || !strings.Contains(err.Error(), "must not exceed max_level") || IsNotFound(err) {
		t.Errorf("PutChannel error = %v", err)
	}

	if _, err = c.GetTemplate(ctx, "html"); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("a non-JSON error body: %v", err)
	}

	wrong, _ := New(srv.URL, "wrong-key")
	err = wrong.DeleteChannel(ctx, "x")
	if !errors.As(err, &ae) || ae.Status != 401 || strings.Contains(err.Error(), "wrong-key") {
		t.Errorf("a refused key: %v", err)
	}
}

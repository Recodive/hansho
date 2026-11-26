package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeHub is an in-memory stand-in for the parts of the hub's sender API the provider uses. It keeps the hub's rules
// that matter here: bearer auth, template versions that must increase, and the {"error","field","message"} answers.
type fakeHub struct {
	mu        sync.Mutex
	key       string
	channels  map[string]json.RawMessage
	groups    map[string][]string
	templates map[string]fakeTemplate
	sounds    map[string]map[string]any
	puts      map[string]int // PUT count per "kind/id"
}

type fakeTemplate struct {
	Version int             `json:"version"`
	Body    json.RawMessage `json:"body"`
}

func newFakeHub(t *testing.T, key string) (*fakeHub, string) {
	h := &fakeHub{key: key, channels: map[string]json.RawMessage{}, groups: map[string][]string{},
		templates: map[string]fakeTemplate{}, sounds: map[string]map[string]any{}, puts: map[string]int{}}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return h, srv.URL
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}

func (h *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+h.key {
		reply(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // api v1 <kind> <id>
	if len(parts) != 4 {
		reply(w, 404, map[string]string{"error": "not_found"})
		return
	}
	kind, id := parts[2], parts[3]
	body, _ := io.ReadAll(r.Body)
	missing := map[string]string{"error": "unknown_" + strings.TrimSuffix(kind, "s")}
	has := func(ok bool) bool {
		if !ok && r.Method != http.MethodPut {
			reply(w, 404, missing)
		}
		return ok
	}
	if r.Method == http.MethodPut {
		h.puts[kind+"/"+id]++
	}
	switch kind {
	case "channels":
		cur, ok := h.channels[id]
		switch r.Method {
		case "PUT":
			var c map[string]json.RawMessage
			if json.Unmarshal(body, &c) != nil || len(c["name"]) == 0 || string(c["name"]) == `""` {
				reply(w, 400, map[string]string{"error": "invalid_request", "field": "name", "message": "required, at most 256 bytes"})
				return
			}
			c["id"] = json.RawMessage(`"` + id + `"`)
			if s := string(c["thread"]); s == "" || s == `""` {
				c["thread"] = c["id"]
			}
			h.channels[id], _ = json.Marshal(c)
			reply(w, 200, json.RawMessage(h.channels[id]))
		case "GET":
			if has(ok) {
				reply(w, 200, cur)
			}
		case "DELETE":
			if has(ok) {
				delete(h.channels, id)
				w.WriteHeader(204)
			}
		}
	case "groups":
		cur, ok := h.groups[id]
		switch r.Method {
		case "PUT":
			var g struct{ Members []string }
			json.Unmarshal(body, &g)
			h.groups[id] = g.Members
			reply(w, 200, map[string]any{"id": id, "members": g.Members})
		case "GET":
			if has(ok) {
				reply(w, 200, map[string]any{"id": id, "members": cur})
			}
		case "DELETE":
			if has(ok) {
				delete(h.groups, id)
				w.WriteHeader(204)
			}
		}
	case "templates":
		cur, ok := h.templates[id]
		switch r.Method {
		case "PUT":
			var t fakeTemplate
			json.Unmarshal(body, &t)
			if ok && t.Version <= cur.Version {
				reply(w, 409, map[string]string{"error": "version_conflict"})
				return
			}
			h.templates[id] = t
			reply(w, 200, map[string]any{"id": id, "version": t.Version})
		case "GET":
			if has(ok) {
				reply(w, 200, map[string]any{"id": id, "version": cur.Version, "body": cur.Body})
			}
		case "DELETE":
			if has(ok) {
				delete(h.templates, id)
				w.WriteHeader(204)
			}
		}
	case "sounds":
		cur, ok := h.sounds[id]
		switch r.Method {
		case "PUT":
			sum := sha256.Sum256(body)
			h.sounds[id] = map[string]any{"id": id, "content_type": r.Header.Get("Content-Type"), "bytes": len(body), "sha256": hex.EncodeToString(sum[:])}
			reply(w, 200, map[string]any{"id": id, "sha256": h.sounds[id]["sha256"], "size": len(body)})
		case "GET":
			if has(ok) {
				reply(w, 200, cur)
			}
		case "DELETE":
			if has(ok) {
				delete(h.sounds, id)
				w.WriteHeader(204)
			}
		}
	default:
		reply(w, 404, map[string]string{"error": "not_found"})
	}
}

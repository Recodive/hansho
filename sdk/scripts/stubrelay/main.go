// Command stubrelay is a minimal stand-in for the relay used by the app's integration tests.
// It accepts hub pushes and revocations without checking anything and lets the test read back
// what the hub sent. It prints "listening <addr>" once ready.
//
// For running the app against a local hub it also answers the device-facing calls: attestations registered
// with POST /stub/hubs ({"hub_id","domain","status","key_fingerprint"}) are served at GET /v1/hubs/{id}, and
// POST /v1/tokens "seals" a token by returning a placeholder.
//
// Simulator dev loop (app/scripts/sim.sh): the placeholder carries the token's APNs topic, so
// GET /captured?since=N&topic=T returns only the pushes for that app (the iPhone and the Watch app poll it with
// -debugPollRelay), each with its sequence number, plus "next" (the cursor to ask with next time) and "epoch" (changes
// when the relay restarts, so a stored cursor is not reused). POST /stub/inject {"topic","kind","payload"} adds an entry
// to that stream without the hub (a silent sync push, or a "simcommand" for the app: {"ack"|"dismiss": message id}).
// POST /stub/webhooks appends the hub's webhook events to the file named by STUBRELAY_WEBHOOK_LOG.
// GET /captured without parameters is unchanged.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// entry is one push as the apps' poll loop sees it.
type entry struct {
	Seq        int             `json:"seq"`
	Kind       string          `json:"kind"`
	CollapseID string          `json:"collapse_id,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	topic      string
}

func main() {
	addr := "127.0.0.1:0"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	var (
		mu       sync.Mutex
		captured []json.RawMessage
		stream   []entry
		revoked  []json.RawMessage
		hubs     = map[string]json.RawMessage{}
		sealed   int
		epoch    = strconv.FormatInt(time.Now().UnixNano(), 36)
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/push", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p struct {
			Sealed     string          `json:"sealed"`
			Kind       string          `json:"kind"`
			CollapseID string          `json:"collapse_id"`
			Payload    json.RawMessage `json:"payload"`
		}
		json.Unmarshal(body, &p)
		mu.Lock()
		captured = append(captured, json.RawMessage(body))
		stream = append(stream, entry{Seq: len(stream), Kind: p.Kind, CollapseID: p.CollapseID, Payload: p.Payload, topic: topicOf(p.Sealed)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"apns_id":"stub"}`)
	})
	mux.HandleFunc("POST /stub/inject", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Topic   string          `json:"topic"`
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.Topic == "" || p.Kind == "" || len(p.Payload) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		stream = append(stream, entry{Seq: len(stream), Kind: p.Kind, Payload: p.Payload, topic: p.Topic})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /stub/webhooks", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if path := os.Getenv("STUBRELAY_WEBHOOK_LOG"); path != "" {
			mu.Lock()
			if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.TimeOnly), strings.TrimSpace(string(body)))
				f.Close()
			}
			mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/revocations", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		revoked = append(revoked, json.RawMessage(body))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /stub/hubs", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var h struct {
			HubID string `json:"hub_id"`
		}
		if json.Unmarshal(body, &h) != nil || h.HubID == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		hubs[h.HubID] = json.RawMessage(body)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/hubs/{id}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		h, ok := hubs[r.PathValue("id")]
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"unknown_hub"}`)
			return
		}
		w.Write(h)
	})
	mux.HandleFunc("POST /v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Kind  string `json:"kind"`
			Topic string `json:"topic"`
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &req)
		mu.Lock()
		sealed++
		n := sealed
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		// The topic rides along in the placeholder, so it survives a relay restart (the hub keeps the sealed token).
		fmt.Fprintf(w, `{"sealed":"stub-%s-%d.%s"}`, req.Kind, n, base64.RawURLEncoding.EncodeToString([]byte(req.Topic)))
	})
	mux.HandleFunc("GET /captured", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()
		if !q.Has("since") && !q.Has("topic") {
			json.NewEncoder(w).Encode(map[string]any{"pushes": nonNil(captured), "revocations": nonNil(revoked)})
			return
		}
		since, _ := strconv.Atoi(q.Get("since"))
		topic := q.Get("topic")
		out := []entry{}
		for _, e := range stream {
			if e.Seq >= since && (topic == "" || e.topic == topic) {
				out = append(out, e)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"pushes": out, "next": len(stream), "epoch": epoch})
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("listening %s\n", ln.Addr())
	os.Stdout.Sync()
	if err := http.Serve(ln, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// topicOf reads the topic back out of a placeholder made by POST /v1/tokens ("stub-<kind>-<n>.<base64url topic>").
func topicOf(sealed string) string {
	i := strings.LastIndexByte(sealed, '.')
	if i < 0 {
		return ""
	}
	t, err := base64.RawURLEncoding.DecodeString(sealed[i+1:])
	if err != nil {
		return ""
	}
	return string(t)
}

func nonNil(v []json.RawMessage) []json.RawMessage {
	if v == nil {
		return []json.RawMessage{}
	}
	return v
}

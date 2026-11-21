package hansho

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const testKey = "ck_test_SECRET_do_not_leak"

func ctx() context.Context { return context.Background() }

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// recorded is what the fake hub saw for one request.
type recorded struct {
	Method      string
	EscapedPath string
	RequestURI  string
	Path        string
	RawQuery    string
	Header      http.Header
	Body        []byte
}

// respond is the answer the fake hub gives.
type respond struct {
	Status  int
	Headers map[string]string
	Body    string
}

type hub struct {
	srv    *httptest.Server
	client *Client
	mu     sync.Mutex
	reqs   []recorded
	sleeps []time.Duration
}

func (h *hub) requests() []recorded {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]recorded(nil), h.reqs...)
}

func (h *hub) waits() []time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]time.Duration(nil), h.sleeps...)
}

// newHub starts a fake hub. fn is called for every request and fills the answer
// (default 200 {}). The client never really sleeps: waits are recorded.
func newHub(t *testing.T, fn func(r *recorded, w *respond), opts ...Option) *hub {
	t.Helper()
	h := &hub{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec := recorded{r.Method, r.URL.EscapedPath(), r.RequestURI, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body}
		h.mu.Lock()
		h.reqs = append(h.reqs, rec)
		h.mu.Unlock()
		out := respond{Status: 200, Body: "{}"}
		if fn != nil {
			fn(&rec, &out)
		}
		if out.Body != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		for k, v := range out.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(out.Status)
		io.WriteString(w, out.Body)
	}))
	t.Cleanup(h.srv.Close)
	h.client = New(h.srv.URL, testKey, append([]Option{WithHTTPClient(h.srv.Client())}, opts...)...)
	h.client.sleep = func(c context.Context, d time.Duration) error {
		h.mu.Lock()
		h.sleeps = append(h.sleeps, d)
		h.mu.Unlock()
		return c.Err()
	}
	return h
}

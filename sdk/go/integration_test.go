//go:build integration

package hansho_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	hansho "github.com/recodive/hansho/sdk/go"
)

// Run through sdk/scripts/run-integration.sh, which starts the real hub binary
// and a stub relay and exports the HANSHO_* variables used here.

type receiver struct {
	mu       sync.Mutex
	events   []hansho.Event
	rejected int
}

func startReceiver(t *testing.T, addr string, secret []byte) *receiver {
	t.Helper()
	r := &receiver{}
	h := hansho.NewWebhookHandler(secret, func(_ context.Context, e hansho.Event) error {
		r.mu.Lock()
		r.events = append(r.events, e)
		r.mu.Unlock()
		return nil
	})
	// Count what the SDK refuses: the hub must never sign something we reject.
	counting := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		h.ServeHTTP(rec, req)
		if rec.code != 200 {
			r.mu.Lock()
			r.rejected++
			r.mu.Unlock()
		}
	})
	var ln net.Listener
	var err error
	for i := 0; i < 50; i++ { // the previous suite's receiver may still be closing
		if ln, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: counting}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return r
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }

// wait polls for a webhook event of the given type whose payload satisfies match.
func (r *receiver) wait(t *testing.T, typ string, match func(any) bool) any {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		evs := append([]hansho.Event(nil), r.events...)
		r.mu.Unlock()
		for _, e := range evs {
			if e.Type != typ {
				continue
			}
			d, err := e.Data()
			if err != nil {
				t.Fatal(err)
			}
			if match(d) {
				return d
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no %s webhook arrived", typ)
	return nil
}

func tinyWAV() []byte {
	data := make([]byte, 800) // 0.05 s of 8 kHz 8-bit mono silence
	b := make([]byte, 0, 44+len(data))
	b = append(b, "RIFF"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(36+len(data)))
	b = append(b, "WAVEfmt "...)
	b = binary.LittleEndian.AppendUint32(b, 16)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint32(b, 8000)
	b = binary.LittleEndian.AppendUint32(b, 8000)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint16(b, 8)
	b = append(b, "data"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(data)))
	return append(b, data...)
}

func env(t *testing.T, k string) string {
	t.Helper()
	v := os.Getenv(k)
	if v == "" {
		t.Skipf("%s is not set; run sdk/scripts/run-integration.sh", k)
	}
	return v
}

func fakeDevice(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command(env(t, "HANSHO_FAKEDEVICE_BIN"), args...).CombinedOutput()
	if err != nil {
		t.Fatalf("fakedevice %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func waitStatus(t *testing.T, c *hansho.Client, id string, want ...string) hansho.MessageStatus {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	var last hansho.MessageStatus
	for time.Now().Before(deadline) {
		ms, err := c.GetMessage(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		last = ms
		for _, w := range want {
			if ms.Status == w {
				return ms
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("message %s: status %q, wanted one of %v", id, last.Status, want)
	return last
}

func TestIntegration(t *testing.T) {
	hubURL := env(t, "HANSHO_HUB_URL")
	secret := []byte(env(t, "HANSHO_WEBHOOK_SECRET"))
	c := hansho.New(hubURL, env(t, "HANSHO_API_KEY"))
	rx := startReceiver(t, env(t, "HANSHO_WEBHOOK_ADDR"), secret)
	ctx := context.Background()

	const p = "go"
	user := "u-" + p + " ü/42?x"
	ch, tpl, snd := p+"-ops", p+"-status", p+"-ding"
	statePath := filepath.Join(t.TempDir(), "device.json")

	t.Run("errors", func(t *testing.T) {
		bad := hansho.New(hubURL, "ck_not_a_key")
		_, err := bad.CreatePairingTicket(ctx, "x")
		if ae, ok := hansho.IsAPIError(err); !ok || ae.Status != 401 || ae.Code != "unauthorized" {
			t.Fatalf("%v", err)
		}
		_, err = c.GetChannel(ctx, "no-such-channel")
		if ae, ok := hansho.IsAPIError(err); !ok || ae.Status != 404 || ae.Code != "unknown_channel" {
			t.Fatalf("%v", err)
		}
		_, err = c.SendMessage(ctx, hansho.Message{ExternalUserID: user, Channel: ch, Title: "", Body: "b"})
		if ae, ok := hansho.IsAPIError(err); !ok || ae.Status != 400 || ae.Field != "title" {
			t.Fatalf("%v", err)
		}
	})

	t.Run("catalog", func(t *testing.T) {
		if _, err := c.PutSound(ctx, snd, "audio/wav", strings.NewReader(string(tinyWAV()))); err != nil {
			t.Fatal(err)
		}
		tmpl := json.RawMessage(`{"type":"vstack","tint":{"light":"#0A84FF","dark":"#64D2FF"},"children":[{"type":"text","style":"headline","text":"{{title}}"},{"type":"spacer"}]}`)
		if err := c.PutTemplate(ctx, tpl, 1, tmpl); err != nil {
			t.Fatal(err)
		}
		err := c.PutTemplate(ctx, tpl, 1, tmpl)
		if ae, ok := hansho.IsAPIError(err); !ok || ae.Status != 409 || ae.Code != "version_conflict" {
			t.Fatalf("same version twice: %v", err)
		}
		if err := c.PutTemplate(ctx, tpl, 2, tmpl); err != nil {
			t.Fatal(err)
		}
		in := hansho.Channel{
			Name: "Operations " + p, DefaultLevel: hansho.LevelActive, MaxLevel: hansho.LevelPage,
			Sound: snd, TemplateID: tpl, Thread: "ops",
			Actions: []hansho.Action{{ID: "ack", Title: "Acknowledge", Ack: true}},
		}
		if err := c.PutChannel(ctx, ch, in); err != nil {
			t.Fatal(err)
		}
		got, err := c.GetChannel(ctx, ch)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != in.Name || got.DefaultLevel != in.DefaultLevel || got.MaxLevel != in.MaxLevel || got.Sound != snd ||
			got.TemplateID != tpl || len(got.Actions) != 1 || !got.Actions[0].Ack {
			t.Fatalf("%+v", got)
		}
		err = c.PutChannel(ctx, "bad id", in)
		if ae, ok := hansho.IsAPIError(err); !ok || ae.Status != 400 || ae.Field != "id" {
			t.Fatalf("id with a space: %v", err)
		}
	})

	t.Run("pair", func(t *testing.T) {
		tk, err := c.CreatePairingTicket(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(tk.QR, "hansho://pair?") || !strings.Contains(tk.QR, tk.Ticket) || !tk.ExpiresAt.After(time.Now()) {
			t.Fatalf("%+v", tk)
		}
		fakeDevice(t, "pair", "-hub", hubURL, "-ticket", tk.Ticket, "-state", statePath, "-name", "SDK phone "+p)
		rx.wait(t, hansho.EventPairingCreated, func(d any) bool {
			pc := d.(hansho.PairingCreated)
			return pc.ExternalUserID == user && pc.DeviceName == "SDK phone "+p
		})
	})

	t.Run("messages at every level", func(t *testing.T) {
		for _, lvl := range []hansho.Level{hansho.LevelPassive, hansho.LevelActive, hansho.LevelTimeSensitive, hansho.LevelCritical} {
			res, err := c.SendMessage(ctx, hansho.Message{
				ExternalUserID: user, Channel: ch, Level: lvl, Title: "Disk full", Body: "db-1 is at 97%",
				Data: map[string]any{"disk": 97}, Sound: snd, CollapseID: "disk-" + string(lvl),
			})
			if err != nil {
				t.Fatalf("%s: %v", lvl, err)
			}
			if res.Level != lvl || res.Devices != 1 || !strings.HasPrefix(res.MessageID, "msg_") {
				t.Fatalf("%s: %+v", lvl, res)
			}
			ms := waitStatus(t, c, res.MessageID, "delivered")
			if ms.Level != lvl || len(ms.Deliveries) != 1 || ms.Deliveries[0].Status != "sent" {
				t.Fatalf("%s: %+v", lvl, ms)
			}
			rx.wait(t, hansho.EventMessageDelivered, func(d any) bool {
				md := d.(hansho.MessageDelivered)
				return md.MessageID == res.MessageID && md.Level == lvl && md.Devices == 1
			})
		}
		// The channel default applies when no level is given.
		res, err := c.SendMessage(ctx, hansho.Message{ExternalUserID: user, Channel: ch, Title: "Default", Body: "b"})
		if err != nil || res.Level != hansho.LevelActive {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("page is acknowledged", func(t *testing.T) {
		res, err := c.SendMessage(ctx, hansho.Message{
			ExternalUserID: user, Channel: ch, Level: hansho.LevelPage, Title: "Primary DB down", Body: "Failover failed",
			Page: &hansho.PageOptions{IntervalSeconds: 60, MaxRetries: 3},
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Level != hansho.LevelPage {
			t.Fatalf("%+v", res)
		}
		waitStatus(t, c, res.MessageID, "paging")
		fakeDevice(t, "ack", "-state", statePath, "-message", res.MessageID)
		ms := waitStatus(t, c, res.MessageID, "acked")
		if ms.AckedAt.IsZero() {
			t.Fatalf("%+v", ms)
		}
		d := rx.wait(t, hansho.EventMessageAcked, func(d any) bool { return d.(hansho.MessageAcked).MessageID == res.MessageID })
		if d.(hansho.MessageAcked).ExternalUserID != user {
			t.Fatalf("%+v", d)
		}
		rx.wait(t, hansho.EventAction, func(d any) bool {
			a := d.(hansho.ActionTaken)
			return a.MessageID == res.MessageID && a.ActionID == "ack"
		})
	})

	t.Run("live activity", func(t *testing.T) {
		started, err := c.StartActivity(ctx, hansho.Activity{
			ExternalUserID: user, Channel: ch, TemplateID: tpl,
			Data: map[string]any{"title": "Deploy", "progress": 0.1}, Alert: &hansho.ActivityAlert{Title: "Deploy started"},
		})
		if err != nil || started.Devices != 1 || !strings.HasPrefix(started.ActivityID, "act_") {
			t.Fatalf("%+v %v", started, err)
		}
		fakeDevice(t, "activity-token", "-state", statePath, "-activity", started.ActivityID)
		up, err := c.UpdateActivity(ctx, started.ActivityID, hansho.ActivityUpdate{Data: map[string]any{"title": "Deploy", "progress": 0.6}, Priority: 10})
		if err != nil || up.ActivityID != started.ActivityID || up.Update < 1 {
			t.Fatalf("%+v %v", up, err)
		}
		if err := c.EndActivity(ctx, started.ActivityID); err != nil {
			t.Fatal(err)
		}
		_, err = c.UpdateActivity(ctx, started.ActivityID, hansho.ActivityUpdate{Data: map[string]any{"title": "x"}})
		if ae, ok := hansho.IsAPIError(err); !ok || ae.Status != 409 || ae.Code != "activity_ended" {
			t.Fatalf("update after end: %v", err)
		}
	})

	t.Run("feeds", func(t *testing.T) {
		own, err := c.PutFeed(ctx, p+"-uptime", hansho.Feed{ExternalUserID: user, TemplateID: tpl, Data: map[string]any{"title": "up", "ms": 41}})
		if err != nil || own.FeedID != p+"-uptime" || own.Devices < 1 {
			t.Fatalf("%+v %v", own, err)
		}
		shared, err := c.PutFeed(ctx, p+"-shared", hansho.Feed{TemplateID: tpl, Data: map[string]any{"title": "all"}})
		if err != nil || shared.Devices < 1 {
			t.Fatalf("%+v %v", shared, err)
		}
		_, err = c.PutFeed(ctx, p+"-bad", hansho.Feed{TemplateID: "missing", Data: 1})
		if ae, ok := hansho.IsAPIError(err); !ok || ae.Status != 400 || ae.Field != "template_id" {
			t.Fatalf("%v", err)
		}
	})

	t.Run("audiences", func(t *testing.T) {
		g, ac := p+"-oncall", p+"-aud"
		in := hansho.Channel{Name: "Audience " + p, DefaultLevel: hansho.LevelActive, MaxLevel: hansho.LevelActive,
			Audience: &hansho.Audience{Mode: hansho.AudienceOnly, Groups: []string{g}}}
		err := c.PutChannel(ctx, ac, in)
		if ae, ok := hansho.IsAPIError(err); !ok || ae.Status != 400 || ae.Field != "audience.groups" {
			t.Fatalf("an unknown group: %v", err)
		}
		if _, err := c.PutGroup(ctx, g, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.PutChannel(ctx, ac, in); err != nil {
			t.Fatal(err)
		}
		res, err := c.SendMessage(ctx, hansho.Message{ExternalUserID: user, Channel: ac, Title: "Hidden", Body: "b"})
		if err != nil || res.Status != hansho.RecipientNoAccess || res.MessageID != "" || res.Devices != 0 {
			t.Fatalf("outside the group: %+v %v", res, err)
		}
		if _, err := c.PutGroup(ctx, g, []string{user}); err != nil {
			t.Fatal(err)
		}
		res, err = c.SendMessage(ctx, hansho.Message{ExternalUserID: user, Channel: ac, Title: "Visible", Body: "b"})
		if err != nil || res.MessageID == "" || res.Devices < 1 {
			t.Fatalf("inside the group: %+v %v", res, err)
		}
		// A republish without an audience keeps the stored one.
		in.Audience = nil
		if err := c.PutChannel(ctx, ac, in); err != nil {
			t.Fatal(err)
		}
		if got, err := c.GetChannel(ctx, ac); err != nil || got.Audience == nil || got.Audience.Mode != hansho.AudienceOnly {
			t.Fatalf("%+v %v", got, err)
		}
		res, err = c.SendMessage(ctx, hansho.Message{ToAudience: true, Channel: ac, Title: "Everyone in", Body: "b"})
		if err != nil || len(res.Recipients) != 1 || res.Recipients[0].ExternalUserID != user || res.Recipients[0].MessageID == "" {
			t.Fatalf("to the audience: %+v %v", res, err)
		}
		if err := c.DeleteChannel(ctx, ac); err != nil {
			t.Fatal(err)
		}
		if err := c.DeleteGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unpair", func(t *testing.T) {
		if err := c.Unpair(ctx, user); err != nil {
			t.Fatal(err)
		}
		rx.wait(t, hansho.EventPairingRevoked, func(d any) bool {
			pr := d.(hansho.PairingRevoked)
			return pr.ExternalUserID == user && pr.By == "sender"
		})
		res, err := c.SendMessage(ctx, hansho.Message{ExternalUserID: user, Channel: ch, Title: "Anyone?", Body: "b"})
		if err != nil || res.Devices != 0 {
			t.Fatalf("%+v %v", res, err)
		}
		waitStatus(t, c, res.MessageID, "undeliverable")
		rx.wait(t, hansho.EventMessageUndeliverable, func(d any) bool { return d.(hansho.MessageUndeliverable).MessageID == res.MessageID })
		if err := c.DeleteChannel(ctx, ch); err != nil {
			t.Fatal(err)
		}
		_, err = c.GetChannel(ctx, ch)
		if ae, ok := hansho.IsAPIError(err); !ok || ae.Code != "unknown_channel" {
			t.Fatalf("%v", err)
		}
	})

	rx.mu.Lock()
	defer rx.mu.Unlock()
	if rx.rejected != 0 {
		t.Fatalf("the SDK rejected %d webhook(s) the hub signed", rx.rejected)
	}
	if len(rx.events) == 0 {
		t.Fatal("no webhooks at all")
	}
}

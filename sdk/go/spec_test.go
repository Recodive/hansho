package hansho

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The shared contract's x-conformance cases, which every SDK's tests replay.
type specFile struct {
	Operations []specOp  `json:"operations"`
	Errors     []specErr `json:"errors"`
}

type specOp struct {
	Name    string `json:"name"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Request *struct {
		ContentType string          `json:"content_type"`
		Body        json.RawMessage `json:"body"`
		RawBase64   string          `json:"raw_base64"`
	} `json:"request"`
	Response struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	} `json:"response"`
}

type specErr struct {
	Status       int               `json:"status"`
	Headers      map[string]string `json:"headers"`
	Body         json.RawMessage   `json:"body"`
	Raw          string            `json:"raw"`
	Code         string            `json:"code"`
	Field        string            `json:"field"`
	RetryAfterMS int64             `json:"retry_after_ms"`
}

func loadSpec(t *testing.T) specFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "spec", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Conformance specFile `json:"x-conformance"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Conformance
}

func (o specOp) wantBody(t *testing.T) []byte {
	t.Helper()
	if o.Request == nil {
		return nil
	}
	if o.Request.RawBase64 != "" {
		b, err := base64.StdEncoding.DecodeString(o.Request.RawBase64)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	return o.Request.Body
}

func sameJSON(t *testing.T, got, want []byte) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("body is not JSON: %q", got)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("body mismatch\n got  %s\n want %s", got, want)
	}
}

func rawJSON(s string) json.RawMessage { return json.RawMessage(s) }

var wav = func() []byte { b, _ := base64.StdEncoding.DecodeString("UklGRgAAAABXQVZF"); return b }()

// calls maps every operation of the shared spec to the SDK call and the value
// it must produce.
var calls = map[string]struct {
	call func(c *Client) (any, error)
	want any
}{
	"createPairingTicket": {
		func(c *Client) (any, error) { return c.CreatePairingTicket(ctx(), "user-42") },
		PairingTicket{Ticket: "tk_abc", QR: "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=tk_abc&fp=ff00",
			ExpiresAt: mustTime("2026-11-08T12:10:00Z")},
	},
	"createPairingTicketWithName": {
		func(c *Client) (any, error) {
			return c.CreatePairingTicket(ctx(), "user-42", WithDisplayName("Zoë 日本語"))
		},
		PairingTicket{Ticket: "tk_def", QR: "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=tk_def&fp=ff00",
			ExpiresAt: mustTime("2026-11-08T12:10:00Z")},
	},
	"setUserDisplayName": {
		func(c *Client) (any, error) { return nil, c.SetUserDisplayName(ctx(), "user-42", "Zoë 日本語") }, nil,
	},
	"clearUserDisplayName": {
		func(c *Client) (any, error) { return nil, c.SetUserDisplayName(ctx(), "user 42", "") }, nil,
	},
	"unpair": {
		func(c *Client) (any, error) { return nil, c.Unpair(ctx(), "user-42") }, nil,
	},
	"putChannel": {
		func(c *Client) (any, error) { return nil, c.PutChannel(ctx(), "ops", specChannel) }, nil,
	},
	"getChannel": {
		func(c *Client) (any, error) { return c.GetChannel(ctx(), "ops") },
		Channel{ID: "ops", Name: "Operations", DefaultLevel: LevelActive, MaxLevel: LevelCritical, Thread: "ops", Actions: []Action{}},
	},
	"deleteChannel": {
		func(c *Client) (any, error) { return nil, c.DeleteChannel(ctx(), "ops") }, nil,
	},
	"putTemplate": {
		func(c *Client) (any, error) {
			return nil, c.PutTemplate(ctx(), "status", 2,
				rawJSON(`{"type":"vstack","children":[{"type":"text","style":"headline","text":"{{title}}"}]}`))
		}, nil,
	},
	"putSound": {
		func(c *Client) (any, error) { return c.PutSound(ctx(), "ding", "audio/wav", bytes.NewReader(wav)) },
		SoundResult{ID: "ding", Filename: "ding.wav", SHA256: "00", Size: 12},
	},
	"sendMessage": {
		func(c *Client) (any, error) {
			return c.SendMessage(ctx(), Message{
				ExternalUserID: "user-42", Channel: "ops", Level: LevelTimeSensitive,
				Title: "Disk full", Subtitle: "db-1", Body: "db-1 is at 97%",
				ImageURL: "https://example.com/i.png", TemplateID: "status",
				Data:  map[string]any{"disk": 97, "nested": map[string]any{"snake_case_key": []int{1, 2}}},
				Sound: "ding", Actions: []Action{{ID: "ack", Title: "Ack", Ack: true}}, CollapseID: "disk-db-1",
			})
		},
		MessageResult{MessageID: "msg_1", Level: LevelTimeSensitive, Devices: 2, Status: "queued",
			Deliveries: []Delivery{{DeviceID: "dev_1", Status: "queued"}, {DeviceID: "dev_2", Status: "queued"}}},
	},
	"sendPage": {
		func(c *Client) (any, error) {
			return c.SendMessage(ctx(), Message{
				ExternalUserID: "user-42", Channel: "ops", Level: LevelPage,
				Title: "Primary DB down", Body: "Failover did not complete",
				Page: &PageOptions{IntervalSeconds: 60, MaxRetries: 3},
			})
		},
		MessageResult{MessageID: "msg_2", Level: LevelPage, Devices: 1, Status: "queued",
			Deliveries: []Delivery{{DeviceID: "dev_1", Status: "queued"}}},
	},
	"sendPageEach": {
		func(c *Client) (any, error) {
			return c.SendMessage(ctx(), Message{
				ExternalUserIDs: []string{"alice", "bob"}, Channel: "ops", Level: LevelPage,
				Title: "Primary DB down", Body: "Failover did not complete",
				Page: &PageOptions{IntervalSeconds: 60, MaxRetries: 3, Ack: "each"},
			})
		},
		MessageResult{Level: LevelPage, Recipients: []Recipient{
			{ExternalUserID: "alice", MessageID: "msg_3", Status: "queued", Devices: 1,
				Deliveries: []Delivery{{DeviceID: "dev_1", Status: "queued"}}},
			{ExternalUserID: "bob", MessageID: "msg_4", Status: "queued", Devices: 1,
				Deliveries: []Delivery{{DeviceID: "dev_2", Status: "queued"}}},
		}},
	},
	"getMessageAckedNoName": {
		func(c *Client) (any, error) { return c.GetMessage(ctx(), "msg_3") },
		MessageStatus{MessageID: "msg_3", Status: "acked", Level: LevelPage, Attempts: 1,
			Deliveries: []Delivery{{DeviceID: "dev_1", Status: "sent"}},
			AckedAt:    mustTime("2026-11-08T12:05:00Z"), AckedBy: "alice"},
	},
	"getMessage": {
		func(c *Client) (any, error) { return c.GetMessage(ctx(), "msg_2") },
		MessageStatus{MessageID: "msg_2", Status: "acked", Level: LevelPage, Attempts: 2,
			Deliveries: []Delivery{{DeviceID: "dev_1", Status: "sent"}, {DeviceID: "dev_2", Status: "failed", Error: "unregistered"}},
			AckedAt:    mustTime("2026-11-08T12:05:00Z"), AckedBy: "user-42", AckedByName: "Zoë 日本語"},
	},
	"startActivity": {
		func(c *Client) (any, error) {
			return c.StartActivity(ctx(), Activity{ExternalUserID: "user-42", Channel: "ops", TemplateID: "deploy",
				Data: map[string]any{"progress": 0.1}, Alert: &ActivityAlert{Title: "Deploy started", Body: "v1.2.3"}})
		},
		ActivityStarted{ActivityID: "act_1", Devices: 1},
	},
	"updateActivity": {
		func(c *Client) (any, error) {
			return c.UpdateActivity(ctx(), "act_1", ActivityUpdate{Data: map[string]any{"progress": 0.6}, Priority: 10,
				Alert: &ActivityAlert{Title: "Halfway", Body: "60%"}})
		},
		ActivityUpdated{ActivityID: "act_1", Update: 3},
	},
	"endActivity": {
		func(c *Client) (any, error) { return nil, c.EndActivity(ctx(), "act_1") }, nil,
	},
	"putFeed": {
		func(c *Client) (any, error) {
			return c.PutFeed(ctx(), "uptime", Feed{ExternalUserID: "user-42", TemplateID: "status",
				Title: "Uptime", Data: map[string]any{"up": true, "ms": 41}})
		},
		FeedResult{FeedID: "uptime", Devices: 2},
	},
	"putGroup": {
		func(c *Client) (any, error) { return c.PutGroup(ctx(), "oncall", []string{"alice", "bob"}) },
		Group{ID: "oncall", Members: []string{"alice", "bob"}},
	},
	"getGroup": {
		func(c *Client) (any, error) { return c.GetGroup(ctx(), "oncall") },
		Group{ID: "oncall", Members: []string{"alice", "bob"}},
	},
	"deleteGroup": {
		func(c *Client) (any, error) { return nil, c.DeleteGroup(ctx(), "oncall") }, nil,
	},
	"kick": {
		func(c *Client) (any, error) { return nil, c.Kick(ctx(), "user-42") }, nil,
	},
	"putChannelWithAudience": {
		func(c *Client) (any, error) {
			return nil, c.PutChannel(ctx(), "ops", Channel{
				Name: "Operations", DefaultLevel: LevelActive, MaxLevel: LevelCritical,
				Audience: &Audience{Mode: AudienceModeOnly, Users: []string{"alice"}, Groups: []string{"oncall"}},
			})
		}, nil,
	},
	"putFeedWithChannel": {
		func(c *Client) (any, error) {
			return c.PutFeed(ctx(), "uptime", Feed{TemplateID: "status", Channel: "ops",
				Audience: &Audience{Mode: AudienceModeOnly, Users: []string{"alice"}, Groups: []string{"oncall"}},
				Data:     map[string]any{"up": true}})
		},
		FeedResult{FeedID: "uptime", Devices: 1},
	},
	"listChannels": {
		func(c *Client) (any, error) { return c.ListChannels(ctx()) },
		[]Channel{{ID: "ops", Name: "Operations", DefaultLevel: LevelActive, MaxLevel: LevelCritical, Thread: "ops",
			Actions: []Action{}, Audience: &Audience{Mode: AudienceModeEveryone}}},
	},
	"listTemplates": {
		func(c *Client) (any, error) { return c.ListTemplates(ctx()) },
		[]Template{{ID: "status", Version: 2, Body: map[string]any{"type": "text", "text": "{{title}}"}}},
	},
	"getTemplate": {
		func(c *Client) (any, error) { return c.GetTemplate(ctx(), "status") },
		Template{ID: "status", Version: 2, Body: map[string]any{"type": "text", "text": "{{title}}"}},
	},
	"deleteTemplate": {
		func(c *Client) (any, error) { return nil, c.DeleteTemplate(ctx(), "status") }, nil,
	},
	"listSounds": {
		func(c *Client) (any, error) { return c.ListSounds(ctx()) },
		[]Sound{{ID: "ding", ContentType: "audio/wav", Bytes: 12, SHA256: "00"}},
	},
	"getSound": {
		func(c *Client) (any, error) { return c.GetSound(ctx(), "ding") },
		Sound{ID: "ding", ContentType: "audio/wav", Bytes: 12, SHA256: "00"},
	},
	"deleteSound": {
		func(c *Client) (any, error) { return nil, c.DeleteSound(ctx(), "ding") }, nil,
	},
	"listGroups": {
		func(c *Client) (any, error) { return c.ListGroups(ctx()) },
		[]Group{{ID: "oncall", Members: []string{"alice", "bob"}}},
	},
	"getFeed": {
		func(c *Client) (any, error) { return c.GetFeed(ctx(), "uptime", "user-42") },
		FeedSettings{FeedID: "uptime", ExternalUserID: "user-42", TemplateID: "status", Title: "Uptime", Channel: "ops",
			Audience: &Audience{Mode: AudienceModeOnly, Users: []string{"alice"}}, UpdatedAt: 1762603200},
	},
	"sendToAudience": {
		func(c *Client) (any, error) {
			return c.SendMessage(ctx(), Message{Channel: "ops", ToAudience: true, Title: "Deploy done", Body: "v1.2.3 is live"})
		},
		MessageResult{Level: LevelActive, Recipients: []Recipient{
			{ExternalUserID: "alice", MessageID: "msg_7", Status: "queued", Devices: 1,
				Deliveries: []Delivery{{DeviceID: "dev_1", Status: "queued"}}},
			{ExternalUserID: "carol", Status: RecipientNoAccess},
		}},
	},
}

var specChannel = Channel{
	Name: "Operations", DefaultLevel: LevelActive, MaxLevel: LevelCritical,
	Sound: "ding", TemplateID: "status", Thread: "ops-thread",
	Actions: []Action{{ID: "ack", Title: "Acknowledge", Ack: true}, {ID: "reply", Title: "Reply", Kind: "text"}},
}

func TestSpecOperations(t *testing.T) {
	spec := loadSpec(t)
	seen := map[string]bool{}
	for _, op := range spec.Operations {
		t.Run(op.Name, func(t *testing.T) {
			seen[op.Name] = true
			tc, ok := calls[op.Name]
			if !ok {
				t.Fatalf("no SDK call for spec operation %q", op.Name)
			}
			h := newHub(t, func(r *recorded, w *respond) {
				got := r.EscapedPath
				if r.RawQuery != "" {
					got += "?" + r.RawQuery
				}
				if r.Method != op.Method || got != op.Path {
					t.Errorf("request line = %s %s, want %s %s", r.Method, got, op.Method, op.Path)
				}
				if r.Header.Get("Authorization") != "Bearer "+testKey {
					t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
				}
				if want := op.wantBody(t); want == nil {
					if len(r.Body) != 0 {
						t.Errorf("unexpected body %q", r.Body)
					}
				} else {
					if ct := r.Header.Get("Content-Type"); ct != op.Request.ContentType {
						t.Errorf("Content-Type = %q, want %q", ct, op.Request.ContentType)
					}
					if op.Request.RawBase64 != "" {
						if !bytes.Equal(r.Body, want) {
							t.Errorf("raw body = %q, want %q", r.Body, want)
						}
					} else {
						sameJSON(t, r.Body, want)
					}
				}
				w.Status = op.Response.Status
				w.Body = string(op.Response.Body)
			})
			got, err := tc.call(h.client)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("result = %#v\nwant     %#v", got, tc.want)
			}
			if n := len(h.requests()); n != 1 {
				t.Fatalf("%d requests, want 1", n)
			}
		})
	}
	for name := range calls {
		if !seen[name] {
			t.Errorf("SDK call %q has no x-conformance case in sdk/spec/openapi.json", name)
		}
	}
}

// TestSpecCoverage wants a Client method for every operation in the spec, named after its operationId.
func TestSpecCoverage(t *testing.T) {
	ct := reflect.TypeFor[*Client]()
	for _, id := range specOperations {
		if _, ok := ct.MethodByName(strings.ToUpper(id[:1]) + id[1:]); !ok {
			t.Errorf("spec operation %s: no Client method %s", id, strings.ToUpper(id[:1])+id[1:])
		}
	}
}

func TestSpecErrors(t *testing.T) {
	for _, e := range loadSpec(t).Errors {
		h := newHub(t, func(r *recorded, w *respond) {
			w.Status = e.Status
			w.Headers = e.Headers
			if e.Raw != "" {
				w.Body = e.Raw
			} else {
				w.Body = string(e.Body)
			}
		})
		_, err := h.client.SendMessage(ctx(), Message{ExternalUserID: "u", Channel: "c", Title: "t", Body: "b"})
		ae, ok := IsAPIError(err)
		if !ok {
			t.Fatalf("status %d: got %T %v, want *APIError", e.Status, err, err)
		}
		if ae.Status != e.Status || ae.Code != e.Code || ae.Field != e.Field || ae.RetryAfter.Milliseconds() != e.RetryAfterMS {
			t.Errorf("status %d: got %+v", e.Status, *ae)
		}
		if n := len(h.requests()); n != 1 {
			t.Errorf("status %d: POST /messages made %d requests, want 1", e.Status, n)
		}
	}
}

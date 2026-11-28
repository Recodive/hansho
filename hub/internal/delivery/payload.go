// Package delivery builds APNs payloads and runs the worker that fans messages
// out through the relay, drives page retries and expires Live Activities.
package delivery

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/recodive/hansho/hub/internal/crypto"
	"github.com/recodive/hansho/hub/internal/level"
	"github.com/recodive/hansho/hub/internal/store"
)

var b64 = base64.RawURLEncoding

// MaxPayloadBytes is the APNs alert payload limit.
const MaxPayloadBytes = 4096

// ErrPayloadTooLarge means the encrypted alert payload exceeds MaxPayloadBytes.
var ErrPayloadTooLarge = errors.New("delivery: payload exceeds 4096 bytes")

// Plaintext is the JSON that is encrypted into the envelope.
type Plaintext struct {
	// Cancel is set only on the acknowledge broadcast: the id of the page to stop.
	Cancel     string          `json:"cancel,omitempty"`
	Title      string          `json:"title"`
	Subtitle   string          `json:"subtitle,omitempty"`
	Body       string          `json:"body"`
	ImageURL   string          `json:"image_url,omitempty"`
	TemplateID string          `json:"template_id,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	Actions    json.RawMessage `json:"actions,omitempty"`
	Channel    string          `json:"channel"`
	Level      string          `json:"level"`
	Sound      string          `json:"sound,omitempty"`
	// Page is set for level "page" so the app can schedule an alarm without the critical-alert entitlement.
	Page bool `json:"page,omitempty"`
	// AckedByOther is set on a cancel only when another recipient acknowledged the page. Never an external user id.
	AckedByOther *AckedByOther `json:"acked_by_other,omitempty"`
}

// AckedByOther says that someone else acknowledged a page; Name is their display name, omitted when they have none.
type AckedByOther struct {
	Name string `json:"name,omitempty"`
}

// EffectiveLevel is min(requested ?? channel default, channel max). An
// unknown requested level is ignored in favour of the default.
func EffectiveLevel(requested *string, ch store.Channel) string {
	l := ch.DefaultLevel
	if requested != nil && level.Valid(*requested) {
		l = *requested
	}
	return level.Min(l, ch.MaxLevel)
}

// Priority is the apns-priority for a level: 5 for passive, 10 otherwise.
func Priority(l string) int {
	if l == level.Passive {
		return 5
	}
	return 10
}

type apsAlert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type criticalSound struct {
	Critical int     `json:"critical"`
	Name     string  `json:"name"`
	Volume   float64 `json:"volume"`
}

type aps struct {
	Alert             apsAlert `json:"alert"`
	MutableContent    int      `json:"mutable-content"`
	InterruptionLevel string   `json:"interruption-level"`
	Sound             any      `json:"sound,omitempty"`
	ThreadID          string   `json:"thread-id"`
	Category          string   `json:"category"`
}

type clearEnvelope struct {
	V  int    `json:"v"`
	P  string `json:"p"`
	M  string `json:"m"`
	CT string `json:"ct"`
	S  string `json:"s"`
	PG int    `json:"pg,omitempty"`
}

type alertPayload struct {
	APS aps           `json:"aps"`
	C   clearEnvelope `json:"c"`
}

// BuildAlertPayload renders the APNs alert JSON for an encrypted envelope.
// sound is the on-device sound filename ("" = default). It returns
// ErrPayloadTooLarge if the result exceeds MaxPayloadBytes.
func BuildAlertPayload(hubName string, ch store.Channel, lvl string, env crypto.Envelope, sound string) ([]byte, error) {
	if !level.Valid(lvl) {
		return nil, fmt.Errorf("delivery: unknown level %q", lvl)
	}
	name := sound
	if name == "" {
		name = "default"
	}
	p := alertPayload{
		APS: aps{
			Alert:          apsAlert{Title: hubName, Body: "New message"},
			MutableContent: 1,
			ThreadID:       ch.Thread,
			Category:       ch.ID,
		},
		C: clearEnvelope{V: env.V, P: env.P, M: env.M, CT: env.CT, S: env.S},
	}
	switch lvl {
	case level.Passive:
		p.APS.InterruptionLevel = "passive"
	case level.Active:
		p.APS.InterruptionLevel, p.APS.Sound = "active", name
	case level.TimeSensitive:
		p.APS.InterruptionLevel, p.APS.Sound = "time-sensitive", name
	case level.Critical, level.Page:
		p.APS.InterruptionLevel = "critical"
		p.APS.Sound = criticalSound{Critical: 1, Name: name, Volume: 1.0}
		if lvl == level.Page {
			p.C.PG = 1
		}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxPayloadBytes {
		return nil, ErrPayloadTooLarge
	}
	return b, nil
}

type cancelAPS struct {
	Alert             apsAlert `json:"alert"`
	MutableContent    int      `json:"mutable-content"`
	InterruptionLevel string   `json:"interruption-level"`
	ThreadID          string   `json:"thread-id"`
}

type cancelAlertPayload struct {
	APS cancelAPS     `json:"aps"`
	C   clearEnvelope `json:"c"`
}

// BuildCancelPayload renders the acknowledge broadcast: a silent, passive alert
// push the notification extension turns into "cancel the alarm and replace the
// page banner". It carries no sound and no category (no action buttons).
func BuildCancelPayload(hubName string, ch store.Channel, env crypto.Envelope) ([]byte, error) {
	p := cancelAlertPayload{
		APS: cancelAPS{
			Alert:             apsAlert{Title: hubName, Body: "Page acknowledged"},
			MutableContent:    1,
			InterruptionLevel: "passive",
			ThreadID:          ch.Thread,
		},
		C: clearEnvelope{V: env.V, P: env.P, M: env.M, CT: env.CT, S: env.S},
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxPayloadBytes {
		return nil, ErrPayloadTooLarge
	}
	return b, nil
}

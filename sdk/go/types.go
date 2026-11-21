package hansho

import (
	"encoding/json"
	"time"
)

// Level is how loudly a message is presented, lowest to highest. The hub
// delivers min(requested ?? channel default, channel max).
type Level string

// The notification levels.
const (
	LevelPassive       Level = "passive"
	LevelActive        Level = "active"
	LevelTimeSensitive Level = "time-sensitive"
	LevelCritical      Level = "critical"
	LevelPage          Level = "page"
)

// Valid reports whether l is one of the five known levels.
func (l Level) Valid() bool {
	switch l {
	case LevelPassive, LevelActive, LevelTimeSensitive, LevelCritical, LevelPage:
		return true
	}
	return false
}

// Action is a button shown with a message or channel. Pressing an Ack action on
// a page acknowledges it.
type Action struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Ack   bool   `json:"ack,omitempty"`
	Kind  string `json:"kind,omitempty"`
}

// PairingTicket is returned by CreatePairingTicket. Render QR as a QR code for
// the user to scan in the app; the ticket is single use.
type PairingTicket struct {
	Ticket    string    `json:"ticket"`
	QR        string    `json:"qr"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Channel groups messages and gives them defaults.
type Channel struct {
	Name         string   `json:"name"`
	DefaultLevel Level    `json:"default_level"`
	MaxLevel     Level    `json:"max_level"`
	Sound        string   `json:"sound,omitempty"`
	TemplateID   string   `json:"template_id,omitempty"`
	Thread       string   `json:"thread,omitempty"`
	Actions      []Action `json:"actions,omitempty"`
}

// PageOptions tunes a page (Level == LevelPage). A zero field means the hub
// default (120 s interval, 5 resends), so a page cannot be configured to stop
// after zero resends.
type PageOptions struct {
	IntervalSeconds int `json:"interval_s,omitempty"`
	MaxRetries      int `json:"max_retries,omitempty"`
}

// Message is a notification for one user.
//
// IdempotencyKey is not part of the body. When set it is sent as the
// Idempotency-Key header and it lets the client retry a 429 or 503. The hub
// ignores the header today, so a retried send is at-least-once.
type Message struct {
	ExternalUserID string       `json:"external_user_id"`
	Channel        string       `json:"channel"`
	Level          Level        `json:"level,omitempty"`
	Title          string       `json:"title"`
	Subtitle       string       `json:"subtitle,omitempty"`
	Body           string       `json:"body"`
	ImageURL       string       `json:"image_url,omitempty"`
	TemplateID     string       `json:"template_id,omitempty"`
	Data           any          `json:"data,omitempty"`
	Sound          string       `json:"sound,omitempty"`
	Actions        []Action     `json:"actions,omitempty"`
	CollapseID     string       `json:"collapse_id,omitempty"`
	Page           *PageOptions `json:"page,omitempty"`

	IdempotencyKey string `json:"-"`
}

// Delivery is the state of a message on one device.
type Delivery struct {
	DeviceID string `json:"device_id"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// MessageResult is the answer to SendMessage.
type MessageResult struct {
	MessageID  string     `json:"message_id"`
	Level      Level      `json:"level"`
	Devices    int        `json:"devices"`
	Status     string     `json:"status"`
	Deliveries []Delivery `json:"deliveries"`
}

// MessageStatus is the answer to GetMessage. Status is one of queued,
// delivered, undeliverable, paging, acked, expired. AckedAt is zero until a
// page is acknowledged.
type MessageStatus struct {
	MessageID  string     `json:"message_id"`
	Status     string     `json:"status"`
	Level      Level      `json:"level"`
	Attempts   int        `json:"attempts"`
	Deliveries []Delivery `json:"deliveries"`
	AckedAt    time.Time  `json:"acked_at"`
}

// ActivityAlert is the optional banner shown with a Live Activity change.
type ActivityAlert struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

// Activity starts a Live Activity from a template.
type Activity struct {
	ExternalUserID string         `json:"external_user_id"`
	Channel        string         `json:"channel"`
	TemplateID     string         `json:"template_id"`
	Data           any            `json:"data"`
	Alert          *ActivityAlert `json:"alert,omitempty"`
}

// ActivityUpdate changes a running Live Activity. Priority is 5 or 10; zero
// leaves the hub default (5).
type ActivityUpdate struct {
	Data     any            `json:"data"`
	Priority int            `json:"priority,omitempty"`
	Alert    *ActivityAlert `json:"alert,omitempty"`
}

// ActivityStarted is the answer to StartActivity.
type ActivityStarted struct {
	ActivityID string `json:"activity_id"`
	Devices    int    `json:"devices"`
}

// ActivityUpdated is the answer to UpdateActivity.
type ActivityUpdated struct {
	ActivityID string `json:"activity_id"`
	Update     int    `json:"update"`
}

// Feed is the data behind a widget. Leave ExternalUserID empty for a feed
// shared by all paired users.
type Feed struct {
	ExternalUserID string `json:"external_user_id,omitempty"`
	TemplateID     string `json:"template_id"`
	// Title is the name the app's feed picker shows instead of the id (at most 64 bytes).
	Title string `json:"title,omitempty"`
	Data  any    `json:"data"`
}

// FeedResult is the answer to PutFeed.
type FeedResult struct {
	FeedID  string `json:"feed_id"`
	Devices int    `json:"devices"`
}

// SoundResult is the answer to PutSound.
type SoundResult struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Size     int    `json:"size"`
}

type templateRequest struct {
	Version int             `json:"version"`
	Body    json.RawMessage `json:"body"`
}

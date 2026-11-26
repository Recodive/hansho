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

// AudienceMode is how an Audience reads its lists.
type AudienceMode string

// The audience modes.
const (
	// AudienceEveryone lets every paired user in.
	AudienceEveryone AudienceMode = "everyone"
	// AudienceOnly lets in just the listed users and the members of the listed groups.
	AudienceOnly AudienceMode = "only"
	// AudienceExcept lets in everyone but the listed users and the members of the listed groups.
	AudienceExcept AudienceMode = "except"
)

// Audience says who may use a channel or feed. Users are your external user ids (they may not have paired yet) and
// Groups are group ids (see PutGroup; a group that does not exist has no members). Users and Groups must be empty
// for AudienceEveryone.
type Audience struct {
	Mode   AudienceMode `json:"mode"`
	Users  []string     `json:"users,omitempty"`
	Groups []string     `json:"groups,omitempty"`
}

// Group is a named list of users, used in audiences.
type Group struct {
	ID      string   `json:"id"`
	Members []string `json:"members"`
}

// Channel groups messages and gives them defaults.
//
// Audience decides who may use the channel. A nil Audience keeps the stored one (send AudienceEveryone to
// open the channel up); GetChannel always returns one.
type Channel struct {
	Name         string    `json:"name"`
	DefaultLevel Level     `json:"default_level"`
	MaxLevel     Level     `json:"max_level"`
	Sound        string    `json:"sound,omitempty"`
	TemplateID   string    `json:"template_id,omitempty"`
	Thread       string    `json:"thread,omitempty"`
	Actions      []Action  `json:"actions,omitempty"`
	Audience     *Audience `json:"audience,omitempty"`
}

// PageOptions tunes a page (Level == LevelPage). A zero field means the hub
// default (120 s interval, 5 resends), so a page cannot be configured to stop
// after zero resends.
type PageOptions struct {
	IntervalSeconds int `json:"interval_s,omitempty"`
	MaxRetries      int `json:"max_retries,omitempty"`
	// Ack is "any" (the hub's default: the first acknowledgement stops the page for everyone it was sent to) or
	// "each" (every recipient must acknowledge for themselves). "" means the hub default.
	Ack string `json:"ack,omitempty"`
}

// Message is a notification for one user, for several, or for a channel's audience.
//
// Set exactly one of ExternalUserID, ExternalUserIDs and ToAudience. Users outside the channel's audience are not
// delivered to: their entry in MessageResult.Recipients has Status RecipientNoAccess.
//
// IdempotencyKey is not part of the body. When set it is sent as the
// Idempotency-Key header and it lets the client retry a 429 or 503. The hub
// ignores the header today, so a retried send is at-least-once.
type Message struct {
	ExternalUserID string `json:"external_user_id,omitempty"`
	// ExternalUserIDs sends to several users at once (at most 1000).
	ExternalUserIDs []string `json:"external_user_ids,omitempty"`
	// ToAudience sends to every paired user in the channel's audience, resolved when you send.
	ToAudience bool         `json:"to_audience,omitempty"`
	Channel    string       `json:"channel"`
	Level      Level        `json:"level,omitempty"`
	Title      string       `json:"title"`
	Subtitle   string       `json:"subtitle,omitempty"`
	Body       string       `json:"body"`
	ImageURL   string       `json:"image_url,omitempty"`
	TemplateID string       `json:"template_id,omitempty"`
	Data       any          `json:"data,omitempty"`
	Sound      string       `json:"sound,omitempty"`
	Actions    []Action     `json:"actions,omitempty"`
	CollapseID string       `json:"collapse_id,omitempty"`
	Page       *PageOptions `json:"page,omitempty"`

	IdempotencyKey string `json:"-"`
}

// Delivery is the state of a message on one device.
type Delivery struct {
	DeviceID string `json:"device_id"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// RecipientNoAccess is the Status of a recipient outside the channel's audience: it got no message.
const RecipientNoAccess = "no_access"

// Recipient is the outcome of a send for one user. Status is the message's status (queued, undeliverable) or
// RecipientNoAccess, in which case there is no MessageID.
type Recipient struct {
	ExternalUserID string     `json:"external_user_id"`
	MessageID      string     `json:"message_id,omitempty"`
	Status         string     `json:"status"`
	Devices        int        `json:"devices,omitempty"`
	Deliveries     []Delivery `json:"deliveries,omitempty"`
}

// MessageResult is the answer to SendMessage. Recipients has one entry per user. For a send to ExternalUserID the
// other fields describe that one recipient (MessageID is empty, Status is RecipientNoAccess and Devices is 0 when
// the user is outside the audience); for several users or an audience, MessageID, Devices and Deliveries are empty
// and each message is under Recipients.
type MessageResult struct {
	MessageID  string      `json:"message_id"`
	Level      Level       `json:"level"`
	Devices    int         `json:"devices"`
	Status     string      `json:"status"`
	Deliveries []Delivery  `json:"deliveries"`
	Recipients []Recipient `json:"recipients,omitempty"`
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
//
// Channel and Audience say who may see the feed. Without an Audience it uses the audience of its Channel, and with
// neither it is for everyone. Like Title, both are replaced by every PutFeed, so send them with each update.
type Feed struct {
	ExternalUserID string `json:"external_user_id,omitempty"`
	TemplateID     string `json:"template_id"`
	// Title is the name the app's feed picker shows instead of the id (at most 64 bytes).
	Title    string    `json:"title,omitempty"`
	Channel  string    `json:"channel,omitempty"`
	Audience *Audience `json:"audience,omitempty"`
	Data     any       `json:"data"`
}

// FeedSettings is the answer to GetFeed: a feed without its data. Audience is nil while the feed inherits the
// audience of its Channel.
type FeedSettings struct {
	FeedID         string    `json:"feed_id"`
	ExternalUserID string    `json:"external_user_id,omitempty"`
	TemplateID     string    `json:"template_id"`
	Title          string    `json:"title,omitempty"`
	Channel        string    `json:"channel,omitempty"`
	Audience       *Audience `json:"audience,omitempty"`
	UpdatedAt      int64     `json:"updated_at"`
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

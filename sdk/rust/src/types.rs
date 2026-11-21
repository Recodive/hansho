//! Request and response types. Field names are Rust-style; the wire names are the hub's.

use std::fmt;
use std::time::SystemTime;

use serde::de::{self, Deserializer};
use serde::{Deserialize, Serialize, Serializer};
use serde_json::Value;

use crate::retry::parse_rfc3339;

/// How loudly a message is presented, lowest to highest.
///
/// A level this SDK does not know (from a newer hub) is kept as [`Level::Other`].
#[derive(Debug, Clone, PartialEq, Eq, Hash, Default)]
pub enum Level {
    /// `passive`
    Passive,
    /// `active`, the hub's default.
    #[default]
    Active,
    /// `time-sensitive`
    TimeSensitive,
    /// `critical`
    Critical,
    /// `page`: repeats until acknowledged.
    Page,
    /// A level this version of the SDK does not know.
    Other(String),
}

impl Level {
    /// The wire name, e.g. `"time-sensitive"`.
    pub fn as_str(&self) -> &str {
        match self {
            Level::Passive => "passive",
            Level::Active => "active",
            Level::TimeSensitive => "time-sensitive",
            Level::Critical => "critical",
            Level::Page => "page",
            Level::Other(s) => s,
        }
    }
}

impl From<&str> for Level {
    fn from(s: &str) -> Self {
        match s {
            "passive" => Level::Passive,
            "active" => Level::Active,
            "time-sensitive" => Level::TimeSensitive,
            "critical" => Level::Critical,
            "page" => Level::Page,
            other => Level::Other(other.to_owned()),
        }
    }
}

impl fmt::Display for Level {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

impl Serialize for Level {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_str(self.as_str())
    }
}

impl<'de> Deserialize<'de> for Level {
    fn deserialize<D: Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
        Ok(Level::from(String::deserialize(d)?.as_str()))
    }
}

/// A button shown with a message or channel. An `ack` action acknowledges a page.
#[derive(Debug, Clone, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct Action {
    /// Identifier sent back in the `action` webhook.
    pub id: String,
    /// Button label.
    pub title: String,
    /// Acknowledges a page when pressed.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ack: Option<bool>,
    /// `"text"` for a reply field.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub kind: Option<String>,
}

/// A single-use pairing ticket (valid 10 minutes).
#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(default)]
pub struct PairingTicket {
    /// The ticket value.
    pub ticket: String,
    /// Render this string as a QR code for the user to scan in the app.
    pub qr: String,
    /// When the ticket stops working.
    #[serde(deserialize_with = "time_required")]
    pub expires_at: SystemTime,
}

impl Default for PairingTicket {
    fn default() -> Self {
        PairingTicket { ticket: String::new(), qr: String::new(), expires_at: SystemTime::UNIX_EPOCH }
    }
}

/// A channel: the defaults and ceiling for a stream of messages.
///
/// The hub answers an unset `sound`, `template_id` or `thread` with an empty string;
/// that reads back as `None`.
#[derive(Debug, Clone, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct Channel {
    /// Display name.
    pub name: String,
    /// Level used when a message does not set one.
    pub default_level: Level,
    /// Highest level a message on this channel may use.
    pub max_level: Level,
    /// Id of an uploaded sound.
    #[serde(skip_serializing_if = "Option::is_none", deserialize_with = "empty_as_none")]
    pub sound: Option<String>,
    /// Id of a default template.
    #[serde(skip_serializing_if = "Option::is_none", deserialize_with = "empty_as_none")]
    pub template_id: Option<String>,
    /// Notification thread id.
    #[serde(skip_serializing_if = "Option::is_none", deserialize_with = "empty_as_none")]
    pub thread: Option<String>,
    /// Buttons shown with every message of the channel.
    #[serde(skip_serializing_if = "Vec::is_empty", deserialize_with = "null_as_default")]
    pub actions: Vec<Action>,
}

/// Resend schedule of a page.
#[derive(Debug, Clone, PartialEq, Eq, Default, Serialize)]
pub struct PageOptions {
    /// Seconds between resends (hub default 120).
    #[serde(rename = "interval_s", skip_serializing_if = "Option::is_none")]
    pub interval_seconds: Option<u32>,
    /// Resends before the page expires (hub default 5).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_retries: Option<u32>,
}

/// A notification, or a page when `level` is [`Level::Page`].
#[derive(Debug, Clone, PartialEq, Default, Serialize)]
pub struct Message {
    /// The paired user to notify.
    pub external_user_id: String,
    /// Channel id.
    pub channel: String,
    /// Defaults to the channel's default level and is capped at its maximum.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub level: Option<Level>,
    /// Title line.
    pub title: String,
    /// Subtitle line.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub subtitle: Option<String>,
    /// Body text.
    pub body: String,
    /// HTTPS image shown with the notification.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub image_url: Option<String>,
    /// Template to render; `data` fills it.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub template_id: Option<String>,
    /// Template data; sent as given (keys are never renamed).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<Value>,
    /// Id of an uploaded sound.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub sound: Option<String>,
    /// Buttons.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub actions: Vec<Action>,
    /// A later message with the same id replaces this one.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub collapse_id: Option<String>,
    /// Resend schedule for a page.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub page: Option<PageOptions>,
    /// Opt in to retries of 429 and 503 for this send. Sent as the `Idempotency-Key`
    /// header, never in the body. The hub ignores it today, so a retried send is at-least-once.
    #[serde(skip)]
    pub idempotency_key: Option<String>,
}

impl Message {
    /// A message with the four required fields.
    pub fn new(external_user_id: impl Into<String>, channel: impl Into<String>, title: impl Into<String>, body: impl Into<String>) -> Self {
        Message {
            external_user_id: external_user_id.into(),
            channel: channel.into(),
            title: title.into(),
            body: body.into(),
            ..Default::default()
        }
    }
}

/// Delivery state of one device.
#[derive(Debug, Clone, PartialEq, Eq, Default, Deserialize)]
#[serde(default)]
pub struct Delivery {
    /// The device.
    pub device_id: String,
    /// e.g. `queued`, `sent`, `failed`.
    pub status: String,
    /// Failure reason, when there is one.
    #[serde(deserialize_with = "empty_as_none")]
    pub error: Option<String>,
}

/// What the hub answers to a send.
#[derive(Debug, Clone, PartialEq, Eq, Default, Deserialize)]
#[serde(default)]
pub struct MessageResult {
    /// Id to poll with [`Client::get_message`](crate::Client::get_message).
    pub message_id: String,
    /// The level the hub applied.
    pub level: Level,
    /// Devices the message was queued for (0 means nobody is paired).
    pub devices: u32,
    /// `queued` etc.
    pub status: String,
    /// Per-device state.
    pub deliveries: Vec<Delivery>,
}

/// State of a sent message. `status` is one of queued, delivered, undeliverable, paging, acked, expired.
#[derive(Debug, Clone, PartialEq, Eq, Default, Deserialize)]
#[serde(default)]
pub struct MessageStatus {
    /// The message.
    pub message_id: String,
    /// Current status.
    pub status: String,
    /// Its level.
    pub level: Level,
    /// Delivery attempts so far.
    pub attempts: u32,
    /// Per-device state.
    pub deliveries: Vec<Delivery>,
    /// When a page was acknowledged.
    #[serde(deserialize_with = "time_optional")]
    pub acked_at: Option<SystemTime>,
}

/// An alert shown when a Live Activity starts or updates.
#[derive(Debug, Clone, PartialEq, Eq, Default, Serialize)]
pub struct ActivityAlert {
    /// Alert title.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    /// Alert body.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub body: Option<String>,
}

/// Starts a Live Activity.
#[derive(Debug, Clone, PartialEq, Default, Serialize)]
pub struct Activity {
    /// The paired user.
    pub external_user_id: String,
    /// Channel id.
    pub channel: String,
    /// Template rendering the activity.
    pub template_id: String,
    /// Template data.
    pub data: Value,
    /// Optional alert.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub alert: Option<ActivityAlert>,
}

/// Updates a Live Activity.
#[derive(Debug, Clone, PartialEq, Default, Serialize)]
pub struct ActivityUpdate {
    /// Template data.
    pub data: Value,
    /// 5 (default) or 10.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub priority: Option<u8>,
    /// Optional alert.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub alert: Option<ActivityAlert>,
}

/// Answer to starting an activity.
#[derive(Debug, Clone, PartialEq, Eq, Default, Deserialize)]
#[serde(default)]
pub struct ActivityStarted {
    /// Id for later updates.
    pub activity_id: String,
    /// Devices it was started on.
    pub devices: u32,
}

/// Answer to updating an activity.
#[derive(Debug, Clone, PartialEq, Eq, Default, Deserialize)]
#[serde(default)]
pub struct ActivityUpdated {
    /// The activity.
    pub activity_id: String,
    /// Sequence number of this update.
    pub update: u32,
}

/// A widget feed.
#[derive(Debug, Clone, PartialEq, Default, Serialize)]
pub struct Feed {
    /// Leave unset for a feed shared by all paired users.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub external_user_id: Option<String>,
    /// Template rendering the feed.
    pub template_id: String,
    /// The name the app's feed picker shows instead of the id (at most 64 bytes).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    /// Template data.
    pub data: Value,
}

/// Answer to publishing a feed.
#[derive(Debug, Clone, PartialEq, Eq, Default, Deserialize)]
#[serde(default)]
pub struct FeedResult {
    /// The feed.
    pub feed_id: String,
    /// Devices it reached.
    pub devices: u32,
}

/// Answer to uploading a sound.
#[derive(Debug, Clone, PartialEq, Eq, Default, Deserialize)]
#[serde(default)]
pub struct SoundResult {
    /// The sound id.
    pub id: String,
    /// Stored file name.
    pub filename: String,
    /// Hex SHA-256 of the file.
    pub sha256: String,
    /// Size in bytes.
    pub size: u64,
}

fn empty_as_none<'de, D: Deserializer<'de>>(d: D) -> Result<Option<String>, D::Error> {
    Ok(Option::<String>::deserialize(d)?.filter(|s| !s.is_empty()))
}

fn null_as_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Deserialize<'de> + Default,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

fn time_optional<'de, D: Deserializer<'de>>(d: D) -> Result<Option<SystemTime>, D::Error> {
    match Option::<String>::deserialize(d)?.filter(|s| !s.is_empty()) {
        None => Ok(None),
        Some(s) => parse_rfc3339(&s).map(Some).ok_or_else(|| de::Error::custom("invalid timestamp")),
    }
}

fn time_required<'de, D: Deserializer<'de>>(d: D) -> Result<SystemTime, D::Error> {
    time_optional(d)?.ok_or_else(|| de::Error::custom("missing timestamp"))
}

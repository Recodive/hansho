//! Webhook verification and parsing.

use std::fmt;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use hmac::{Hmac, Mac};
use serde_json::{Map, Value};
use sha2::Sha256;

use crate::retry::from_unix;
use crate::types::Level;

/// `X-Hansho-Webhook-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>`
pub const WEBHOOK_SIGNATURE_HEADER: &str = "X-Hansho-Webhook-Signature";

/// Default allowed distance between the signed timestamp and now: 300 s.
pub const DEFAULT_TOLERANCE: Duration = Duration::from_secs(300);

/// Why a webhook was rejected.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum WebhookErrorCode {
    /// The header is missing or not `t=<unix>,v1=<hex>`.
    MalformedHeader,
    /// No `v1` signature matches the body.
    BadSignature,
    /// A valid signature outside the tolerance (a replay).
    Stale,
    /// A valid signature over a body that is not a webhook envelope.
    MalformedBody,
    /// The secret is empty.
    InvalidSecret,
}

impl WebhookErrorCode {
    /// The code shared with the other SDKs, e.g. `"bad_signature"`.
    pub fn as_str(self) -> &'static str {
        match self {
            WebhookErrorCode::MalformedHeader => "malformed_header",
            WebhookErrorCode::BadSignature => "bad_signature",
            WebhookErrorCode::Stale => "stale",
            WebhookErrorCode::MalformedBody => "malformed_body",
            WebhookErrorCode::InvalidSecret => "invalid_secret",
        }
    }
}

/// Returned by [`verify_webhook`] and [`Verifier::verify`]; check `code`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct WebhookError {
    /// What failed.
    pub code: WebhookErrorCode,
    message: &'static str,
}

impl fmt::Display for WebhookError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "hansho webhook: {}", self.message)
    }
}

impl std::error::Error for WebhookError {}

fn fail(code: WebhookErrorCode, message: &'static str) -> WebhookError {
    WebhookError { code, message }
}

/// `pairing.created`
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PairingCreated {
    /// The pairing.
    pub pairing_id: String,
    /// The paired user.
    pub external_user_id: String,
    /// The device's name.
    pub device_name: String,
}

/// `pairing.revoked`
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PairingRevoked {
    /// The pairing.
    pub pairing_id: String,
    /// The user.
    pub external_user_id: String,
    /// Who revoked: `"sender"`, `"device"` or `"relay"`.
    pub by: String,
}

/// `message.delivered`
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MessageDelivered {
    /// The message.
    pub message_id: String,
    /// The user.
    pub external_user_id: String,
    /// The channel.
    pub channel: String,
    /// The level.
    pub level: Level,
    /// Devices it reached.
    pub devices: i64,
}

/// `message.undeliverable`
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MessageUndeliverable {
    /// The message.
    pub message_id: String,
    /// The user.
    pub external_user_id: String,
    /// The channel.
    pub channel: String,
    /// `"no_devices"` or `"all_deliveries_failed"`.
    pub reason: String,
}

/// `message.acked`
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MessageAcked {
    /// The message.
    pub message_id: String,
    /// The user.
    pub external_user_id: String,
    /// The device that acknowledged.
    pub device_id: String,
}

/// `message.dismissed`: the person swiped away a message that asked to be acknowledged, without acknowledging it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MessageDismissed {
    /// The message.
    pub message_id: String,
    /// The user.
    pub external_user_id: String,
    /// The device.
    pub device_id: String,
}

/// `message.expired`
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MessageExpired {
    /// The message.
    pub message_id: String,
    /// The user.
    pub external_user_id: String,
    /// The channel.
    pub channel: String,
    /// Attempts made.
    pub attempts: i64,
}

/// `action`: a button press.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ActionTaken {
    /// The message.
    pub message_id: String,
    /// The pressed action.
    pub action_id: String,
    /// The device.
    pub device_id: String,
    /// The user.
    pub external_user_id: String,
    /// Reply text of a text action.
    pub text: Option<String>,
}

/// `activity.ended`
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ActivityEnded {
    /// The activity.
    pub activity_id: String,
    /// The user.
    pub external_user_id: String,
    /// The channel.
    pub channel: String,
    /// Why it ended.
    pub reason: String,
}

/// The payload of a verified webhook. An event this SDK does not know is [`EventData::Unknown`].
#[derive(Debug, Clone, PartialEq)]
#[non_exhaustive]
pub enum EventData {
    /// `pairing.created`
    PairingCreated(PairingCreated),
    /// `pairing.revoked`
    PairingRevoked(PairingRevoked),
    /// `message.delivered`
    MessageDelivered(MessageDelivered),
    /// `message.undeliverable`
    MessageUndeliverable(MessageUndeliverable),
    /// `message.acked`
    MessageAcked(MessageAcked),
    /// `message.dismissed`
    MessageDismissed(MessageDismissed),
    /// `message.expired`
    MessageExpired(MessageExpired),
    /// `action`
    Action(ActionTaken),
    /// `activity.ended`
    ActivityEnded(ActivityEnded),
    /// An event name this SDK does not know.
    Unknown {
        /// The event name as sent.
        name: String,
        /// The payload as parsed JSON, untouched (`Null` when absent).
        data: Value,
    },
}

/// A verified webhook.
#[derive(Debug, Clone, PartialEq)]
pub struct Event {
    /// The hub's event id.
    pub id: String,
    /// When the hub created the event.
    pub created_at: SystemTime,
    /// The typed payload.
    pub data: EventData,
    /// The verified request body.
    pub raw: Vec<u8>,
}

impl Event {
    /// The event name as sent, e.g. `"message.acked"`; for an unknown event the name the hub used.
    pub fn name(&self) -> &str {
        match &self.data {
            EventData::PairingCreated(_) => "pairing.created",
            EventData::PairingRevoked(_) => "pairing.revoked",
            EventData::MessageDelivered(_) => "message.delivered",
            EventData::MessageUndeliverable(_) => "message.undeliverable",
            EventData::MessageAcked(_) => "message.acked",
            EventData::MessageDismissed(_) => "message.dismissed",
            EventData::MessageExpired(_) => "message.expired",
            EventData::Action(_) => "action",
            EventData::ActivityEnded(_) => "activity.ended",
            EventData::Unknown { name, .. } => name,
        }
    }
}

/// Verifies webhooks signed with one secret.
///
/// With several `v1=` values in the header (secret rotation) any match is accepted;
/// the comparison is constant-time; timestamps more than the tolerance (300 s by
/// default) from now are rejected.
#[derive(Clone)]
pub struct Verifier {
    secret: Vec<u8>,
    tolerance: Duration,
}

/// Keeps the secret out of logs.
impl fmt::Debug for Verifier {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Verifier").field("tolerance", &self.tolerance).finish_non_exhaustive()
    }
}

impl Verifier {
    /// A verifier for the hub's `HUB_WEBHOOK_SECRET`. An empty secret rejects everything with `invalid_secret`.
    pub fn new(secret: impl AsRef<[u8]>) -> Self {
        Verifier { secret: secret.as_ref().to_vec(), tolerance: DEFAULT_TOLERANCE }
    }

    /// Allowed distance between the signed timestamp and now. `Duration::ZERO` means the default (300 s).
    pub fn tolerance(mut self, tolerance: Duration) -> Self {
        self.tolerance = if tolerance.is_zero() { DEFAULT_TOLERANCE } else { tolerance };
        self
    }

    /// Verifies a webhook against its raw body and returns the event.
    ///
    /// `header` is the `X-Hansho-Webhook-Signature` request header (use `""` when it
    /// is absent); `body` the request body exactly as received. A body that a
    /// framework parsed and you re-serialised will not verify.
    pub fn verify(&self, header: &str, body: impl AsRef<[u8]>) -> Result<Event, WebhookError> {
        self.verify_at(header, body, SystemTime::now())
    }

    /// Like [`verify`](Self::verify) with an explicit clock, for tests.
    pub fn verify_at(&self, header: &str, body: impl AsRef<[u8]>, now: SystemTime) -> Result<Event, WebhookError> {
        let raw = body.as_ref();
        if self.secret.is_empty() {
            return Err(fail(WebhookErrorCode::InvalidSecret, "webhook secret is empty"));
        }
        let (t, sigs) = parse_header(header)?;
        let mut mac = Hmac::<Sha256>::new_from_slice(&self.secret)
            .map_err(|_| fail(WebhookErrorCode::InvalidSecret, "webhook secret is unusable"))?;
        mac.update(t.as_bytes());
        mac.update(b".");
        mac.update(raw);
        let mut matched = false;
        for sig in sigs {
            if let Some(bytes) = decode_sha256_hex(sig) {
                // verify_slice compares in constant time.
                matched |= mac.clone().verify_slice(&bytes).is_ok();
            }
        }
        if !matched {
            return Err(fail(WebhookErrorCode::BadSignature, "signature does not match"));
        }

        let signed_ms = t.parse::<i128>().ok().and_then(|s| s.checked_mul(1000));
        let now_ms = match now.duration_since(UNIX_EPOCH) {
            Ok(d) => d.as_millis() as i128,
            Err(e) => -(e.duration().as_millis() as i128),
        };
        if !signed_ms.is_some_and(|s| (now_ms - s).unsigned_abs() <= self.tolerance.as_millis()) {
            return Err(fail(WebhookErrorCode::Stale, "timestamp outside tolerance"));
        }
        parse_event(raw)
    }
}

/// Verifies a webhook with the default 300 s tolerance. See [`Verifier`].
pub fn verify_webhook(secret: impl AsRef<[u8]>, header: &str, body: impl AsRef<[u8]>) -> Result<Event, WebhookError> {
    Verifier::new(secret).verify(header, body)
}

fn parse_header(header: &str) -> Result<(&str, Vec<&str>), WebhookError> {
    use WebhookErrorCode::MalformedHeader;
    if header.trim().is_empty() {
        return Err(fail(MalformedHeader, "missing signature header"));
    }
    let mut t: Option<&str> = None;
    let mut sigs = Vec::new();
    for part in header.split(',') {
        let Some((k, v)) = part.trim().split_once('=') else {
            return Err(fail(MalformedHeader, "signature header part is not key=value"));
        };
        if k == "t" {
            if t.replace(v).is_some() {
                return Err(fail(MalformedHeader, "duplicate timestamp"));
            }
        } else if k == "v1" && !v.is_empty() {
            sigs.push(v);
        }
    }
    let digits =
        |t: &str| !t.strip_prefix('-').unwrap_or(t).is_empty() && t.strip_prefix('-').unwrap_or(t).bytes().all(|b| b.is_ascii_digit());
    match t {
        Some(t) if digits(t) && !sigs.is_empty() => Ok((t, sigs)),
        _ => Err(fail(MalformedHeader, "signature header needs t=<unix> and v1=<hex>")),
    }
}

/// 64 hex digits (either case) to 32 bytes.
fn decode_sha256_hex(s: &str) -> Option<[u8; 32]> {
    let b = s.as_bytes();
    if b.len() != 64 {
        return None;
    }
    let nibble = |c: u8| match c {
        b'0'..=b'9' => Some(c - b'0'),
        b'a'..=b'f' => Some(c - b'a' + 10),
        b'A'..=b'F' => Some(c - b'A' + 10),
        _ => None,
    };
    let mut out = [0u8; 32];
    for (i, pair) in b.chunks_exact(2).enumerate() {
        out[i] = nibble(pair[0])? << 4 | nibble(pair[1])?;
    }
    Some(out)
}

fn parse_event(raw: &[u8]) -> Result<Event, WebhookError> {
    use WebhookErrorCode::MalformedBody;
    let Ok(Value::Object(env)) = serde_json::from_slice::<Value>(raw) else {
        return Err(fail(MalformedBody, "body is not a webhook envelope"));
    };
    let name = match env.get("event") {
        Some(Value::String(n)) if !n.is_empty() => n.clone(),
        _ => return Err(fail(MalformedBody, "body has no event")),
    };
    let created = num(env.get("created_at"));
    let created_at = if created.is_finite() && created.abs() < 1e15 {
        // Whole seconds; a fraction is dropped.
        from_unix(created as i64)
    } else {
        UNIX_EPOCH
    };
    let data = match env.get("data") {
        Some(Value::Object(d)) => d.clone(),
        _ => Map::new(),
    };
    let d = Fields(&data);
    let payload = match name.as_str() {
        "pairing.created" => EventData::PairingCreated(PairingCreated {
            pairing_id: d.s("pairing_id"),
            external_user_id: d.s("external_user_id"),
            device_name: d.s("device_name"),
        }),
        "pairing.revoked" => EventData::PairingRevoked(PairingRevoked {
            pairing_id: d.s("pairing_id"),
            external_user_id: d.s("external_user_id"),
            by: d.s("by"),
        }),
        "message.delivered" => EventData::MessageDelivered(MessageDelivered {
            message_id: d.s("message_id"),
            external_user_id: d.s("external_user_id"),
            channel: d.s("channel"),
            level: Level::from(d.s("level").as_str()),
            devices: d.n("devices"),
        }),
        "message.undeliverable" => EventData::MessageUndeliverable(MessageUndeliverable {
            message_id: d.s("message_id"),
            external_user_id: d.s("external_user_id"),
            channel: d.s("channel"),
            reason: d.s("reason"),
        }),
        "message.acked" => EventData::MessageAcked(MessageAcked {
            message_id: d.s("message_id"),
            external_user_id: d.s("external_user_id"),
            device_id: d.s("device_id"),
        }),
        "message.dismissed" => EventData::MessageDismissed(MessageDismissed {
            message_id: d.s("message_id"),
            external_user_id: d.s("external_user_id"),
            device_id: d.s("device_id"),
        }),
        "message.expired" => EventData::MessageExpired(MessageExpired {
            message_id: d.s("message_id"),
            external_user_id: d.s("external_user_id"),
            channel: d.s("channel"),
            attempts: d.n("attempts"),
        }),
        "action" => EventData::Action(ActionTaken {
            message_id: d.s("message_id"),
            action_id: d.s("action_id"),
            device_id: d.s("device_id"),
            external_user_id: d.s("external_user_id"),
            text: match data.get("text") {
                Some(Value::String(t)) => Some(t.clone()),
                _ => None,
            },
        }),
        "activity.ended" => EventData::ActivityEnded(ActivityEnded {
            activity_id: d.s("activity_id"),
            external_user_id: d.s("external_user_id"),
            channel: d.s("channel"),
            reason: d.s("reason"),
        }),
        _ => EventData::Unknown { name, data: env.get("data").cloned().unwrap_or(Value::Null) },
    };
    Ok(Event { id: text(env.get("id")), created_at, data: payload, raw: raw.to_vec() })
}

/// Lenient field access: a missing field is empty/zero, a number or bool is stringified.
struct Fields<'a>(&'a Map<String, Value>);

impl Fields<'_> {
    fn s(&self, key: &str) -> String {
        text(self.0.get(key))
    }

    fn n(&self, key: &str) -> i64 {
        num(self.0.get(key)) as i64
    }
}

fn text(v: Option<&Value>) -> String {
    match v {
        None | Some(Value::Null) => String::new(),
        Some(Value::String(s)) => s.clone(),
        Some(other) => other.to_string(),
    }
}

fn num(v: Option<&Value>) -> f64 {
    match v {
        Some(Value::Number(n)) => n.as_f64().unwrap_or(0.0),
        Some(Value::String(s)) => s.trim().parse().unwrap_or(0.0),
        _ => 0.0,
    }
}

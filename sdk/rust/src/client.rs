use std::fmt;
use std::sync::Arc;
use std::time::{Duration, SystemTime};

use serde::de::DeserializeOwned;
use serde_json::{json, Map, Value};

use crate::error::{ApiError, Error, NetworkError, Result};
use crate::retry::{backoff, is_idempotent, is_retryable_status, parse_retry_after, DEFAULT_MAX_RETRIES, MAX_RETRY_WAIT};
use crate::types::{
    Activity, ActivityStarted, ActivityUpdate, ActivityUpdated, Channel, Feed, FeedResult, Message, MessageResult, MessageStatus,
    PairingTicket, SoundResult,
};

/// Product codename.
pub const NAME: &str = "hansho";
/// SDK version, sent in the default `User-Agent`.
pub const VERSION: &str = env!("CARGO_PKG_VERSION");

const MAX_SOUND_BYTES: usize = 1 << 20;
const DEFAULT_TIMEOUT: Duration = Duration::from_secs(30);

type Sleeper = Arc<dyn Fn(Duration) + Send + Sync>;

/// Configures and builds a [`Client`].
#[derive(Clone)]
pub struct ClientBuilder {
    base_url: String,
    api_key: String,
    max_retries: u32,
    timeout: Duration,
    user_agent: Option<String>,
    sleeper: Option<Sleeper>,
}

impl ClientBuilder {
    /// Retries for GET/PUT/DELETE on 429 and 503. Default 3; 0 disables.
    pub fn max_retries(mut self, n: u32) -> Self {
        self.max_retries = n;
        self
    }

    /// Timeout for each attempt, connect to last byte. Default 30 s; `Duration::ZERO` disables it.
    pub fn timeout(mut self, timeout: Duration) -> Self {
        self.timeout = timeout;
        self
    }

    /// Replaces the default `hansho-rust/<version>` `User-Agent`.
    pub fn user_agent(mut self, user_agent: impl Into<String>) -> Self {
        self.user_agent = Some(user_agent.into());
        self
    }

    /// Replaces the thread sleep used between retries; meant for tests.
    pub fn sleeper(mut self, sleeper: impl Fn(Duration) + Send + Sync + 'static) -> Self {
        self.sleeper = Some(Arc::new(sleeper));
        self
    }

    /// Validates the settings and builds the client.
    pub fn build(self) -> Result<Client> {
        let base_url = self.base_url.trim_end_matches('/').to_owned();
        if base_url.is_empty() {
            return Err(Error::InvalidArgument("base URL is required".into()));
        }
        if self.api_key.is_empty() {
            return Err(Error::InvalidArgument("API key is required".into()));
        }
        let uri: ureq::http::Uri = base_url.parse().map_err(|_| Error::InvalidArgument("base URL is not a valid URL".into()))?;
        if !matches!(uri.scheme_str(), Some("http" | "https")) || uri.authority().is_none() {
            return Err(Error::InvalidArgument("base URL must be an http:// or https:// URL".into()));
        }
        let agent: ureq::Agent = ureq::Agent::config_builder()
            .timeout_global((!self.timeout.is_zero()).then_some(self.timeout))
            .http_status_as_error(false)
            // The API key must never follow a redirect to another host; a 3xx is an API error.
            .max_redirects(0)
            .max_redirects_will_error(false)
            .build()
            .into();
        Ok(Client {
            base_url,
            api_key: self.api_key,
            agent,
            max_retries: self.max_retries,
            user_agent: self.user_agent.unwrap_or_else(|| format!("{NAME}-rust/{VERSION}")),
            sleeper: self.sleeper,
        })
    }
}

/// Client for a Hansho hub's sender API. Cheap to clone; share one across threads.
#[derive(Clone)]
pub struct Client {
    base_url: String,
    api_key: String,
    agent: ureq::Agent,
    max_retries: u32,
    user_agent: String,
    sleeper: Option<Sleeper>,
}

/// Keeps the API key out of logs.
impl fmt::Debug for Client {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Client").field("base_url", &self.base_url).finish_non_exhaustive()
    }
}

struct Call {
    method: &'static str,
    /// Escaped path relative to the base URL.
    path: String,
    body: Vec<u8>,
    content_type: Option<String>,
    retry: bool,
    headers: Vec<(&'static str, String)>,
}

struct Answer {
    status: u16,
    retry_after: Option<String>,
    text: String,
}

/// Escapes one path segment (everything but RFC 3986 unreserved characters);
/// rejects ids that would not stay in their collection.
fn seg(id: &str) -> Result<String> {
    if id.is_empty() {
        return Err(Error::InvalidArgument("id must not be empty".into()));
    }
    if id == "." || id == ".." {
        return Err(Error::InvalidArgument(format!("id must not be \"{id}\"")));
    }
    let mut out = String::with_capacity(id.len());
    for b in id.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.' | b'~') {
            out.push(char::from(b));
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    Ok(out)
}

impl Client {
    /// A client with the default settings. `base_url` is the hub's public URL.
    pub fn new(base_url: impl Into<String>, api_key: impl Into<String>) -> Result<Client> {
        Client::builder(base_url, api_key).build()
    }

    /// Starts configuring a client.
    pub fn builder(base_url: impl Into<String>, api_key: impl Into<String>) -> ClientBuilder {
        ClientBuilder {
            base_url: base_url.into(),
            api_key: api_key.into(),
            max_retries: DEFAULT_MAX_RETRIES,
            timeout: DEFAULT_TIMEOUT,
            user_agent: None,
            sleeper: None,
        }
    }

    // --- pairing -------------------------------------------------------------

    /// Creates a single-use pairing ticket (valid 10 minutes). Render `qr` as a QR
    /// code for the user to scan in the Hansho app.
    pub fn create_pairing_ticket(&self, external_user_id: &str) -> Result<PairingTicket> {
        if external_user_id.is_empty() {
            return Err(Error::InvalidArgument("id must not be empty".into()));
        }
        self.json("POST", "/api/v1/pairing-tickets".into(), Some(json!({ "external_user_id": external_user_id })))
    }

    /// Revokes every pairing of the user.
    pub fn unpair(&self, external_user_id: &str) -> Result<()> {
        self.json::<serde::de::IgnoredAny>("DELETE", format!("/api/v1/users/{}/pairings", seg(external_user_id)?), None).map(drop)
    }

    // --- catalog -------------------------------------------------------------

    /// Creates or replaces a channel.
    pub fn put_channel(&self, id: &str, channel: &Channel) -> Result<()> {
        let body = serde_json::to_value(channel).map_err(encode_error)?;
        self.json::<serde::de::IgnoredAny>("PUT", format!("/api/v1/channels/{}", seg(id)?), Some(body)).map(drop)
    }

    /// Reads a channel.
    pub fn get_channel(&self, id: &str) -> Result<Channel> {
        self.json("GET", format!("/api/v1/channels/{}", seg(id)?), None)
    }

    /// Deletes a channel.
    pub fn delete_channel(&self, id: &str) -> Result<()> {
        self.json::<serde::de::IgnoredAny>("DELETE", format!("/api/v1/channels/{}", seg(id)?), None).map(drop)
    }

    /// Stores a template version; the version must increase.
    pub fn put_template(&self, id: &str, version: u32, body: &Value) -> Result<()> {
        let path = format!("/api/v1/templates/{}", seg(id)?);
        self.json::<serde::de::IgnoredAny>("PUT", path, Some(json!({ "version": version, "body": body }))).map(drop)
    }

    /// Uploads a sound (`audio/x-caf`, `audio/wav` or `audio/aiff`, at most 1 MB).
    pub fn put_sound(&self, id: &str, content_type: &str, bytes: &[u8]) -> Result<SoundResult> {
        let path = format!("/api/v1/sounds/{}", seg(id)?);
        if bytes.len() > MAX_SOUND_BYTES {
            return Err(Error::InvalidArgument("sound exceeds 1 MB".into()));
        }
        let call = Call {
            method: "PUT",
            path,
            body: bytes.to_vec(),
            content_type: Some(content_type.to_owned()),
            retry: true,
            headers: Vec::new(),
        };
        self.decode(&call, self.send(&call)?)
    }

    // --- messages ------------------------------------------------------------

    /// Sends a notification, or a page when `level` is [`Level::Page`](crate::Level::Page).
    /// It is never retried automatically (a repeat would notify twice) unless the
    /// message has an `idempotency_key`.
    pub fn send_message(&self, message: &Message) -> Result<MessageResult> {
        let mut call = Call {
            method: "POST",
            path: "/api/v1/messages".into(),
            body: serde_json::to_vec(message).map_err(encode_error)?,
            content_type: Some("application/json".into()),
            retry: false,
            headers: Vec::new(),
        };
        if let Some(key) = message.idempotency_key.as_deref().filter(|k| !k.is_empty()) {
            call.retry = true;
            call.headers.push(("idempotency-key", key.to_owned()));
        }
        self.decode(&call, self.send(&call)?)
    }

    /// Reads the state of a sent message.
    pub fn get_message(&self, id: &str) -> Result<MessageStatus> {
        self.json("GET", format!("/api/v1/messages/{}", seg(id)?), None)
    }

    // --- live activities and feeds -------------------------------------------

    /// Starts a Live Activity.
    pub fn start_activity(&self, activity: &Activity) -> Result<ActivityStarted> {
        self.json("POST", "/api/v1/activities".into(), Some(serde_json::to_value(activity).map_err(encode_error)?))
    }

    /// Updates a Live Activity. Not retried automatically.
    pub fn update_activity(&self, id: &str, update: &ActivityUpdate) -> Result<ActivityUpdated> {
        let path = format!("/api/v1/activities/{}", seg(id)?);
        self.json("PATCH", path, Some(serde_json::to_value(update).map_err(encode_error)?))
    }

    /// Ends a Live Activity.
    pub fn end_activity(&self, id: &str) -> Result<()> {
        self.json::<serde::de::IgnoredAny>("DELETE", format!("/api/v1/activities/{}", seg(id)?), None).map(drop)
    }

    /// Publishes a widget feed.
    pub fn put_feed(&self, id: &str, feed: &Feed) -> Result<FeedResult> {
        let path = format!("/api/v1/feeds/{}", seg(id)?);
        self.json("PUT", path, Some(serde_json::to_value(feed).map_err(encode_error)?))
    }

    // --- plumbing ------------------------------------------------------------

    fn json<T: DeserializeOwned>(&self, method: &'static str, path: String, body: Option<Value>) -> Result<T> {
        let mut call = Call { method, path, body: Vec::new(), content_type: None, retry: is_idempotent(method), headers: Vec::new() };
        if let Some(b) = body {
            call.body = serde_json::to_vec(&b).map_err(encode_error)?;
            call.content_type = Some("application/json".into());
        }
        self.decode(&call, self.send(&call)?)
    }

    /// Parses a 2xx body. An empty or non-object body counts as `{}`.
    fn decode<T: DeserializeOwned>(&self, call: &Call, text: String) -> Result<T> {
        let value = match text.trim() {
            "" => Value::Object(Map::new()),
            t => match serde_json::from_str::<Value>(t) {
                Ok(v @ Value::Object(_)) => v,
                Ok(_) => Value::Object(Map::new()),
                Err(_) => return Err(self.network(call, "invalid JSON in response".into(), false)),
            },
        };
        // Never put the body (or serde's echo of it) into the error.
        serde_json::from_value(value).map_err(|_| self.network(call, "unexpected JSON in response".into(), false))
    }

    /// Runs the call with the retry policy and returns the body of the first 2xx answer.
    fn send(&self, call: &Call) -> Result<String> {
        let mut attempt = 0;
        loop {
            let answer = self.round_trip(call)?;
            if (200..300).contains(&answer.status) {
                return Ok(answer.text);
            }
            let err = self.api_error(call, &answer);
            if !call.retry || !is_retryable_status(answer.status) || attempt >= self.max_retries {
                return Err(err.into());
            }
            let wait = err.retry_after.unwrap_or_else(|| backoff(attempt));
            if wait > MAX_RETRY_WAIT {
                return Err(err.into());
            }
            match &self.sleeper {
                Some(sleep) => sleep(wait),
                None => std::thread::sleep(wait),
            }
            attempt += 1;
        }
    }

    fn round_trip(&self, call: &Call) -> Result<Answer> {
        let mut req = ureq::http::Request::builder()
            .method(call.method)
            .uri(format!("{}{}", self.base_url, call.path))
            .header("authorization", format!("Bearer {}", self.api_key))
            .header("accept", "application/json")
            .header("user-agent", &self.user_agent);
        if let Some(ct) = &call.content_type {
            req = req.header("content-type", ct);
        }
        for (k, v) in &call.headers {
            req = req.header(*k, v);
        }
        let req =
            req.body(call.body.clone()).map_err(|_| Error::InvalidArgument("request contains an invalid header value or URL".into()))?;
        let mut resp = self.agent.run(req).map_err(|e| self.transport(call, &e))?;
        let status = resp.status().as_u16();
        let retry_after = resp.headers().get("retry-after").and_then(|v| v.to_str().ok()).map(str::to_owned);
        let text = resp.body_mut().read_to_string().map_err(|e| self.transport(call, &e))?;
        Ok(Answer { status, retry_after, text })
    }

    fn transport(&self, call: &Call, e: &ureq::Error) -> Error {
        self.network(call, e.to_string(), matches!(e, ureq::Error::Timeout(_)))
    }

    fn network(&self, call: &Call, message: String, timeout: bool) -> Error {
        NetworkError { method: call.method.into(), path: call.path.clone(), message: self.redact(&message), timeout }.into()
    }

    fn redact(&self, s: &str) -> String {
        s.replace(&self.api_key, "[redacted]")
    }

    fn api_error(&self, call: &Call, answer: &Answer) -> ApiError {
        let (mut code, mut field, mut detail) = (String::new(), None, None);
        if let Ok(Value::Object(o)) = serde_json::from_str::<Value>(&answer.text) {
            if let Some(Value::String(c)) = o.get("error") {
                code = self.redact(c);
            }
            if let Some(Value::String(f)) = o.get("field").filter(|f| f.as_str() != Some("")) {
                field = Some(self.redact(f));
            }
            if let Some(Value::String(m)) = o.get("message") {
                detail = Some(self.redact(m));
            }
        }
        ApiError {
            method: call.method.into(),
            path: call.path.clone(),
            status: answer.status,
            code,
            field,
            retry_after: parse_retry_after(answer.retry_after.as_deref(), SystemTime::now()),
            detail,
        }
    }
}

fn encode_error(_: serde_json::Error) -> Error {
    Error::InvalidArgument("request cannot be encoded as JSON".into())
}

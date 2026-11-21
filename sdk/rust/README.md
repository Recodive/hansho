# hansho (Rust)

Rust client for a Hansho hub. Blocking (no async runtime needed), thread-safe, Rust 1.85 or newer.
Dependencies: `ureq` (rustls), `serde`, `serde_json`, `hmac`, `sha2`.

```toml
[dependencies]
hansho = "0.1"
```

See [../README.md](../README.md) for the model shared with the Go and Node SDKs.

## Quick start

```rust
use hansho::{Channel, Client, Level, Message};

fn main() -> Result<(), hansho::Error> {
    let hansho = Client::new("https://hub.example.com", std::env::var("HANSHO_API_KEY").unwrap())?;

    hansho.put_channel("ops", &Channel {
        name: "Operations".into(),
        default_level: Level::Active,
        max_level: Level::Critical,
        ..Default::default()
    })?;

    let res = hansho.send_message(&Message::new("user-42", "ops", "Disk full", "db-1 is at 97%"))?;
    println!("{} {}", res.message_id, res.status); // msg_... queued
    Ok(())
}
```

Configure with the builder: `Client::builder(url, key).max_retries(3).timeout(Duration::from_secs(30)).user_agent("acme/1").build()?`.
`max_retries` defaults to 3 (0 disables), `timeout` applies to each attempt (default 30 s, `Duration::ZERO` disables).
A `Client` is cheap to clone and safe to share across threads.

`Level` is an enum (`Passive`, `Active`, `TimeSensitive`, `Critical`, `Page`); a level from a newer hub arrives as
`Level::Other(String)` instead of failing. Structs have public fields and implement `Default`, so build them with
`..Default::default()`. Timestamps are `std::time::SystemTime`. `hansho::json!` and `hansho::Value` are
`serde_json`'s, re-exported for template bodies and data.

## Pairing

```rust
let ticket = hansho.create_pairing_ticket("user-42")?;
// Render ticket.qr as a QR code; the user scans it in the Hansho app.
// ticket.expires_at is 10 minutes out; the ticket works once.
// The hub then sends a pairing.created webhook.

hansho.unpair("user-42")?; // revokes every pairing of the user
```

## Sending

```rust
use hansho::{json, Action, Activity, ActivityUpdate, Feed, Level, Message, PageOptions};

// A notification. The level defaults to the channel's default and is capped at its max.
hansho.send_message(&Message {
    level: Some(Level::TimeSensitive),
    actions: vec![Action { id: "retry".into(), title: "Retry".into(), ..Default::default() }],
    collapse_id: Some("deploy-web".into()), // a later message with the same id replaces this one
    ..Message::new("user-42", "ops", "Deploy failed", "web-3 rolled back")
})?;

// A page repeats every interval_seconds until someone acknowledges it on any
// device, then the hub sends message.acked. Defaults: 120 s, 5 resends.
let page = hansho.send_message(&Message {
    level: Some(Level::Page),
    page: Some(PageOptions { interval_seconds: Some(60), max_retries: Some(10) }),
    ..Message::new("user-42", "ops", "Primary DB down", "Failover did not complete")
})?;
let status = hansho.get_message(&page.message_id)?; // queued, delivered, undeliverable, paging, acked, expired

// A templated message; data is sent exactly as given (its keys are never renamed).
hansho.put_template("status", 1, &json!({"type": "vstack", "children": [{"type": "text", "style": "headline", "text": "{{title}}"}]}))?;
hansho.send_message(&Message {
    template_id: Some("status".into()),
    data: Some(json!({"title": "Uptime"})),
    ..Message::new("user-42", "ops", "Uptime", "99.98%")
})?;

// A Live Activity.
let act = hansho.start_activity(&Activity {
    external_user_id: "user-42".into(),
    channel: "ops".into(),
    template_id: "deploy".into(),
    data: json!({"progress": 0.1}),
    ..Default::default()
})?;
hansho.update_activity(&act.activity_id, &ActivityUpdate { data: json!({"progress": 0.6}), priority: Some(10), alert: None })?;
hansho.end_activity(&act.activity_id)?;

// A widget feed; leave external_user_id unset for one shared by all paired users. The title is the name the
// app's feed picker shows (it lists the feeds published for that person plus the shared ones).
hansho.put_feed("uptime", &Feed {
    external_user_id: Some("user-42".into()),
    template_id: "status".into(),
    title: Some("Uptime".into()),
    data: json!({"title": "up"}),
})?;

// A sound (audio/x-caf, audio/wav or audio/aiff, at most 1 MB).
hansho.put_sound("ding", "audio/wav", &wav_bytes)?;
```

Ids may contain any characters except `.` and `..` (and must not be empty): they are path-escaped. A bad id is
refused with `Error::InvalidArgument` before anything is sent.

## Webhooks

Set `HUB_WEBHOOK_URL` and `HUB_WEBHOOK_SECRET` on the hub. Verify the **raw** request body as bytes: a body a
framework parsed and you re-serialised will not match its signature.

```rust
use hansho::{EventData, Verifier, WEBHOOK_SIGNATURE_HEADER};

let verifier = Verifier::new(std::env::var("HANSHO_WEBHOOK_SECRET").unwrap());

// In your handler (here axum-style: headers: HeaderMap, body: Bytes):
let header = headers.get(WEBHOOK_SIGNATURE_HEADER).and_then(|v| v.to_str().ok()).unwrap_or("");
match verifier.verify(header, &body) {
    Ok(event) => {
        match &event.data {
            EventData::MessageAcked(d) => println!("acknowledged: {}", d.message_id),
            EventData::Action(d) => println!("button {} on {}", d.action_id, d.message_id),
            EventData::Unknown { name, .. } => println!("newer hub event: {name}"),
            _ => {}
        }
        StatusCode::OK
    }
    Err(_) => StatusCode::BAD_REQUEST, // non-2xx: the hub retries
}
```

`Verifier` accepts several `v1=` signatures (secret rotation), compares in constant time and rejects
timestamps more than 300 s from now (`.tolerance(Duration)` changes it; `verify_at` takes the clock).
`WebhookError.code` is a `WebhookErrorCode`: `MalformedHeader`, `BadSignature`, `Stale` (a valid
signature, replayed too late), `MalformedBody`, `InvalidSecret` (`as_str()` gives the names the other SDKs use).
Events: `pairing.created`, `pairing.revoked`, `message.delivered`, `message.undeliverable`, `message.acked`,
`message.dismissed`, `message.expired`, `action`, `activity.ended`; anything else is `EventData::Unknown` with the
parsed payload (`EventData` is `#[non_exhaustive]`, so keep a `_` arm). `Event::raw` is the verified body.

## Errors

```rust
use hansho::Error;

match hansho.send_message(&msg) {
    Ok(res) => println!("{}", res.message_id),
    Err(Error::Api(e)) => {
        // e.status, e.code ("unknown_channel", "invalid_request", ...), e.field (Some("title")), e.retry_after
    }
    Err(Error::Network(e)) => {
        // no answer at all: connection, timeout (e.timeout) or an unreadable answer
    }
    Err(Error::InvalidArgument(why)) => { /* refused before sending */ }
}
```

The API key, ticket values and message bodies never appear in an error's `Display`, and the hub's own message
(`ApiError::detail`, with the key redacted) is kept out of it too. `Client`'s `Debug` leaves the key out. HTTP redirects are not followed, so the key cannot be sent to another host.

## Retries

`GET`, `PUT` and `DELETE` are retried on 429 and 503, up to `max_retries` times, waiting for `Retry-After`
(delay-seconds or HTTP date; a wait over 60 s is returned to you as the error) or an exponential backoff with
jitter. `send_message` is never retried unless the message has an `idempotency_key`; the hub does not
deduplicate on it yet, so a retried send is at-least-once. Activity updates (`PATCH`) are not retried, and
neither are connection errors. Retry waits block the calling thread.

## Testing

```sh
cargo test                                   # unit tests; they read ../spec/*.json
cargo test --test integration -- --nocapture # end to end; needs the environment below
```

The tests use a small in-process HTTP server (no extra dev-dependencies). `tests/integration.rs` is skipped
unless `HANSHO_HUB_URL` is set (with `HANSHO_API_KEY`, `HANSHO_WEBHOOK_SECRET`, `HANSHO_WEBHOOK_ADDR`,
`HANSHO_FAKEDEVICE_BIN`); `sdk/scripts/run-integration.sh` starts a real hub and sets them.

## Licence

MIT, see [LICENSE](LICENSE). The hub itself is AGPL-3.0.

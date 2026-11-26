# hansho (Rust)

Rust client for a Hansho hub. Blocking (no async runtime needed), thread-safe, Rust 1.85 or newer.
Dependencies: `ureq` (rustls), `serde`, `serde_json`, `hmac`, `sha2`.

```toml
[dependencies]
hansho = "0.1"
```

See [../README.md](https://github.com/Recodive/hansho/blob/main/sdk/README.md) for the model shared with the Go and Node SDKs.

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
hansho.kick("user-42")?; // the same call; the app tells the person that you ended it

// Optionally give the user a display name with the ticket:
hansho.create_pairing_ticket_with_name("user-42", "Ada Lovelace")?;
```

## Users

```rust
// What other people see for a user instead of their external id (acknowledged-by, ...): any language,
// trimmed, at most 40 characters, no control characters. The user need not be paired yet.
hansho.set_user_display_name("user-42", "Ada Lovelace")?;
hansho.set_user_display_name("user-42", "")?; // clears it
```

## Audiences and groups

A channel or feed is open to everyone unless it has an `Audience`: `AudienceMode::Only` the listed users and groups, or
`AudienceMode::Except` them. Users are your external user ids (they may not have paired yet); groups are named lists you
keep on the hub. The hub decides who has access and checks it on every request, so a change applies at once.

```rust
use hansho::{json, Audience, AudienceMode, Channel, Feed, Level, Message, RECIPIENT_NO_ACCESS};

hansho.put_group("oncall", &["alice", "bob"])?; // replaces the whole member list; also get_group, delete_group
hansho.put_channel("pager", &Channel {
    name: "Pager".into(),
    default_level: Level::Page,
    max_level: Level::Page,
    audience: Some(Audience { mode: AudienceMode::Only, groups: vec!["oncall".into()], ..Default::default() }),
    ..Default::default()
})?;
// `audience: None` keeps the audience stored on the hub; `Audience::default()` (everyone) opens the channel up again.

// A feed uses its channel's audience unless it sets its own. Leaving both out keeps what is stored; naming a channel
// without an audience makes the feed use the channel's.
hansho.put_feed("queue", &Feed {
    template_id: "status".into(),
    channel: Some("pager".into()),
    data: json!({"value": 3}),
    ..Default::default()
})?;
let settings = hansho.get_feed("queue", None)?; // settings without the data; Some("user-42") for a per-user feed

// Send to several users, or to everyone in the channel's audience. People outside it are not delivered to.
let res = hansho.send_message(&Message {
    to_audience: true, // or external_user_ids: vec![...]; give exactly one target
    channel: "pager".into(),
    title: "DB down".into(),
    body: "...".into(),
    ..Default::default()
})?;
for r in &res.recipients {
    if r.status == RECIPIENT_NO_ACCESS { /* only with external_user(_ids): not in the audience, no message_id */ }
}
```

`MessageResult::recipients` has one entry per user. A send to one `external_user_id` keeps filling the top-level
`message_id`, `devices`, `status` and `deliveries` too (for a user without access: empty `message_id`, `devices` 0, status
`no_access`); for several users or an audience the top-level `message_id` is empty and each message is under `recipients`.
An audience that names a group that does not exist is refused with `400 invalid_request`. Error codes: `CODE_NO_ACCESS`
(403, `start_activity` for a user outside the channel's audience) and `CODE_UNKNOWN_GROUP` (404, `get_group`, `delete_group`).

## Sending

```rust
use hansho::{json, Action, Activity, ActivityUpdate, Feed, Level, Message, PageAck, PageOptions};

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
    page: Some(PageOptions { interval_seconds: Some(60), max_retries: Some(10), ..Default::default() }),
    ..Message::new("user-42", "ops", "Primary DB down", "Failover did not complete")
})?;
let status = hansho.get_message(&page.message_id)?; // queued, delivered, undeliverable, paging, acked, expired
// status.acked_by: who acknowledged (their external user id); status.acked_by_name: their display name, if they
// have one. The message.acked webhook (MessageAcked) carries both too.

// A page to several people stops for everyone at the first acknowledgement (PageAck::Any, the default).
// With PageAck::Each every person keeps being paged until they acknowledge for themselves.
hansho.send_message(&Message {
    level: Some(Level::Page),
    external_user_ids: vec!["alice".into(), "bob".into()],
    page: Some(PageOptions { ack: Some(PageAck::Each), ..Default::default() }),
    ..Message::new("", "ops", "DB down", "...")
})?;

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

//! Official Rust client for a Hansho hub's sender API: pair users, define channels,
//! templates and sounds, send notifications, pages, Live Activities and widget feeds,
//! and verify the hub's webhooks.
//!
//! The client is blocking (no async runtime needed) and thread-safe.
//!
//! ```no_run
//! use hansho::{Channel, Client, Level, Message};
//!
//! # fn main() -> Result<(), hansho::Error> {
//! let hansho = Client::new("https://hub.example.com", std::env::var("HANSHO_API_KEY").unwrap())?;
//!
//! hansho.put_channel("ops", &Channel {
//!     name: "Operations".into(),
//!     default_level: Level::Active,
//!     max_level: Level::Critical,
//!     ..Default::default()
//! })?;
//!
//! let res = hansho.send_message(&Message::new("user-42", "ops", "Disk full", "db-1 is at 97%"))?;
//! println!("{} {}", res.message_id, res.status); // msg_... queued
//! # Ok(())
//! # }
//! ```
//!
//! Verify a webhook against the **raw** request body:
//!
//! ```
//! # let (secret, header, body) = ("s", "t=1,v1=00", b"{}");
//! match hansho::verify_webhook(secret, header, body) {
//!     Ok(event) => println!("{}", event.name()),
//!     Err(e) => println!("rejected: {} ({})", e, e.code.as_str()),
//! }
//! ```

mod client;
mod error;
mod retry;
mod types;
mod webhook;

pub use client::{Client, ClientBuilder, NAME, VERSION};
pub use error::{ApiError, Error, NetworkError, Result, CODE_NO_ACCESS, CODE_UNKNOWN_GROUP};
pub use serde_json::{json, Value};
pub use types::{
    Action, Activity, ActivityAlert, ActivityStarted, ActivityUpdate, ActivityUpdated, Audience, AudienceMode, Channel, Delivery, Feed,
    FeedResult, FeedSettings, Group, Level, Message, MessageResult, MessageStatus, PageOptions, PairingTicket, Recipient, SoundResult,
    RECIPIENT_NO_ACCESS,
};
pub use webhook::{
    verify_webhook, ActionTaken, ActivityEnded, Event, EventData, MessageAcked, MessageDelivered, MessageDismissed, MessageExpired,
    MessageUndeliverable, PairingCreated, PairingRevoked, Verifier, WebhookError, WebhookErrorCode, DEFAULT_TOLERANCE,
    WEBHOOK_SIGNATURE_HEADER,
};

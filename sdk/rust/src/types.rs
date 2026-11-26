//! Conveniences on the generated types.

use crate::types_gen::Message;

/// The `status` of a [`Recipient`](crate::Recipient) outside the channel's audience: it got no message.
pub const RECIPIENT_NO_ACCESS: &str = "no_access";

impl Message {
    /// A message with the four required fields. An empty `external_user_id` leaves it unset, so you can name the targets
    /// (`external_user_ids` or `to_audience`) yourself.
    pub fn new(external_user_id: impl Into<String>, channel: impl Into<String>, title: impl Into<String>, body: impl Into<String>) -> Self {
        Message {
            external_user_id: Some(external_user_id.into()).filter(|id| !id.is_empty()),
            channel: channel.into(),
            title: title.into(),
            body: body.into(),
            ..Default::default()
        }
    }
}

# frozen_string_literal: true

module Hansho
  # How loudly a message is presented, lowest to highest.
  LEVELS = %w[passive active time-sensitive critical page].freeze

  # A pairing ticket. Render #qr as a QR code for the user to scan in the Hansho app.
  PairingTicket = Struct.new(:ticket, :qr, :expires_at, keyword_init: true)

  # A channel as stored on the hub. #actions is an Array of Hashes with the keys :id, :title and optionally :ack, :kind.
  # #audience is a Hash `{ mode:, users:, groups: }` (see Client#put_channel); the hub always returns one.
  Channel = Struct.new(:name, :default_level, :max_level, :sound, :template_id, :thread, :actions, :audience, keyword_init: true)

  # A named list of users, used in audiences. #members are external user ids.
  Group = Struct.new(:id, :members, keyword_init: true)

  # The #status of a Recipient outside the channel's audience: it got no message.
  RECIPIENT_NO_ACCESS = "no_access"

  # The outcome of a send for one user. #status is the message status (queued, undeliverable) or RECIPIENT_NO_ACCESS,
  # in which case #message_id is nil. #devices is 0 and #deliveries empty when there is no message.
  Recipient = Struct.new(:external_user_id, :message_id, :status, :devices, :deliveries, keyword_init: true)

  # One device a message was handed to. #error is nil unless the delivery failed.
  Delivery = Struct.new(:device_id, :status, :error, keyword_init: true)

  # The answer to #send_message. #recipients has one Recipient per user (empty when the hub predates audiences).
  # For a send to +external_user_id+ the other fields describe that one recipient (#message_id is "", #status is
  # "no_access" and #devices is 0 when the user is outside the audience); for +external_user_ids+ or +to_audience+
  # #message_id, #devices and #status are nil and #deliveries is empty: each message is under #recipients.
  MessageResult = Struct.new(:message_id, :level, :devices, :status, :deliveries, :recipients, keyword_init: true)

  # The answer to #get_message. #status is one of queued, delivered, undeliverable, paging, acked, expired.
  # #acked_at is a Time or nil.
  MessageStatus = Struct.new(:message_id, :status, :level, :attempts, :deliveries, :acked_at, keyword_init: true)

  ActivityStarted = Struct.new(:activity_id, :devices, keyword_init: true)
  ActivityUpdated = Struct.new(:activity_id, :update, keyword_init: true)
  FeedResult = Struct.new(:feed_id, :devices, keyword_init: true)

  # A feed without its data, from Client#get_feed. #audience is nil while the feed inherits its channel's.
  FeedSettings = Struct.new(:feed_id, :external_user_id, :template_id, :title, :channel, :audience, :updated_at,
                            keyword_init: true)
  SoundResult = Struct.new(:id, :filename, :sha256, :size, keyword_init: true)
end

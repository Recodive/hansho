# frozen_string_literal: true

module Hansho
  # How loudly a message is presented, lowest to highest.
  LEVELS = %w[passive active time-sensitive critical page].freeze

  # A pairing ticket. Render #qr as a QR code for the user to scan in the Hansho app.
  PairingTicket = Struct.new(:ticket, :qr, :expires_at, keyword_init: true)

  # A channel as stored on the hub. #actions is an Array of Hashes with the keys :id, :title and optionally :ack, :kind.
  Channel = Struct.new(:name, :default_level, :max_level, :sound, :template_id, :thread, :actions, keyword_init: true)

  # One device a message was handed to. #error is nil unless the delivery failed.
  Delivery = Struct.new(:device_id, :status, :error, keyword_init: true)

  # The answer to #send_message.
  MessageResult = Struct.new(:message_id, :level, :devices, :status, :deliveries, keyword_init: true)

  # The answer to #get_message. #status is one of queued, delivered, undeliverable, paging, acked, expired.
  # #acked_at is a Time or nil.
  MessageStatus = Struct.new(:message_id, :status, :level, :attempts, :deliveries, :acked_at, keyword_init: true)

  ActivityStarted = Struct.new(:activity_id, :devices, keyword_init: true)
  ActivityUpdated = Struct.new(:activity_id, :update, keyword_init: true)
  FeedResult = Struct.new(:feed_id, :devices, keyword_init: true)
  SoundResult = Struct.new(:id, :filename, :sha256, :size, keyword_init: true)
end

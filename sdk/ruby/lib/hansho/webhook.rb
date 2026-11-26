# frozen_string_literal: true

require "json"
require "openssl"

module Hansho
  # Verifies and parses the hub's webhooks.
  #
  #   event = Hansho::Webhook.verify(secret: ENV.fetch("HANSHO_WEBHOOK_SECRET"), header: signature_header, body: raw_body)
  #   event.type # => "message.acked"
  #   event.data.message_id
  module Webhook
    # `X-Hansho-Webhook-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>`
    SIGNATURE_HEADER = "X-Hansho-Webhook-Signature"
    # Default allowed distance between the signed timestamp and now, in seconds.
    DEFAULT_TOLERANCE = 300

    # The payload types are generated (Hansho::PairingCreated, Hansho::MessageAcked, ...); these names are kept.
    EVENTS.each_value { |type| const_set(type.name.split("::").last, type) }

    # A verified webhook. +type+ is the event name ("message.acked", ...) or "unknown"
    # for an event this SDK does not know; then +name+ is the name as sent and +data+
    # the parsed JSON payload, untouched. +raw+ is the verified request body.
    Event = Struct.new(:type, :id, :created_at, :data, :raw, :name, keyword_init: true) do
      def unknown?
        type == "unknown"
      end
    end

    HEX_SIGNATURE = /\A[0-9a-fA-F]{64}\z/
    private_constant :HEX_SIGNATURE

    module_function

    # Verifies a webhook against its raw body and returns the Event. Raises WebhookError
    # (+code+: malformed_header, bad_signature, stale, malformed_body, invalid_body,
    # invalid_secret). With several v1 values in the header (secret rotation) any match
    # is accepted.
    #
    # +secret+:: the shared secret (HUB_WEBHOOK_SECRET).
    # +header+:: the X-Hansho-Webhook-Signature request header.
    # +body+:: the request body exactly as received (a String). A body that a framework
    #          parsed and you re-serialised will not verify, and a Hash is rejected.
    # +now+:: a Time or epoch seconds; default the current time.
    # +tolerance+:: allowed distance between the signed timestamp and +now+ in seconds;
    #               default 300, a value <= 0 means the default.
    def verify(secret:, header:, body:, now: nil, tolerance: nil)
      raise WebhookError.new("invalid_secret", "webhook secret is empty") unless secret.is_a?(String) && !secret.empty?
      unless body.is_a?(String)
        raise WebhookError.new("invalid_body", "body must be the raw request body (a String), not a parsed object")
      end

      t, sigs = parse_header(header)
      raw = body.b
      mac = OpenSSL::HMAC.new(secret.b, "SHA256")
      mac.update("#{t}.")
      mac.update(raw)
      want = mac.digest
      match = false
      sigs.each do |sig|
        next unless HEX_SIGNATURE.match?(sig)

        match = true if OpenSSL.fixed_length_secure_compare([sig].pack("H*"), want)
      end
      raise WebhookError.new("bad_signature", "signature does not match") unless match

      now_s = now.nil? ? Time.now.to_f : now.to_f
      tol = tolerance && tolerance > 0 ? tolerance : DEFAULT_TOLERANCE
      raise WebhookError.new("stale", "timestamp outside tolerance") if (now_s - t.to_i).abs > tol

      build_event(raw)
    end

    def parse_header(header)
      raise WebhookError.new("malformed_header", "missing signature header") unless header.is_a?(String) && !header.strip.empty?

      t = nil
      sigs = []
      header.split(",", -1).each do |part|
        part = part.strip
        eq = part.index("=")
        raise WebhookError.new("malformed_header", "signature header part is not key=value") unless eq

        k = part[0...eq]
        v = part[(eq + 1)..]
        if k == "t"
          raise WebhookError.new("malformed_header", "duplicate timestamp") unless t.nil?

          t = v
        elsif k == "v1" && !v.empty?
          sigs << v
        end
      end
      if t.nil? || !t.match?(/\A-?\d+\z/) || sigs.empty?
        raise WebhookError.new("malformed_header", "signature header needs t=<unix> and v1=<hex>")
      end

      [t, sigs]
    end

    def build_event(raw)
      text = raw.dup.force_encoding(Encoding::UTF_8)
      env = begin
        parsed = text.valid_encoding? ? JSON.parse(text) : nil
        parsed.is_a?(Hash) ? parsed : nil
      rescue JSON::ParserError
        nil
      end
      raise WebhookError.new("malformed_body", "body is not a webhook envelope") unless env

      name = env["event"]
      raise WebhookError.new("malformed_body", "body has no event") unless name.is_a?(String) && !name.empty?

      created_at = Time.at(num(env["created_at"]))
      id = str(env["id"])
      payload = env["data"].is_a?(Hash) ? env["data"] : {}
      klass = EVENTS[name]
      return Event.new(type: "unknown", id: id, created_at: created_at, data: env["data"], raw: raw, name: name) unless klass

      Event.new(type: name, id: id, created_at: created_at, data: klass.from_wire(payload), raw: raw)
    end

    def str(value)
      value.nil? ? "" : value.to_s
    end

    def num(value)
      case value
      when Integer then value
      when Float then value == value.to_i ? value.to_i : value
      when String then Integer(value, exception: false) || 0
      else 0
      end
    end
    private_class_method :parse_header, :build_event, :str, :num
  end
end

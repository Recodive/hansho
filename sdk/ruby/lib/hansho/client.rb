# frozen_string_literal: true

require "json"
require "time"
require "uri"

module Hansho
  # Client for a Hansho hub's sender API.
  #
  #   hansho = Hansho::Client.new(base_url: "https://hub.example.com", api_key: ENV.fetch("HANSHO_API_KEY"))
  #   hansho.send_message(external_user_id: "user-42", channel: "ops", title: "Disk full", body: "db-1 is at 97%")
  #
  # Failures raise APIError (the hub answered with a non-2xx status) or NetworkError
  # (no answer). Mistakes in the arguments raise ArgumentError before anything is sent.
  class Client
    MAX_SOUND_BYTES = 1 << 20

    Call = Struct.new(:verb, :path, :body, :content_type, :retryable, :headers, keyword_init: true)
    private_constant :Call

    # +base_url+:: the hub's public URL.
    # +api_key+:: a sender API key from `hub apikey create`.
    # +max_retries+:: retries of GET/PUT/DELETE on 429 and 503. Default 3; 0 disables.
    # +timeout+:: per-attempt connect/read/write timeout in seconds. Default 30; nil or 0 disables.
    # +user_agent+:: replaces the default `hansho-ruby/VERSION`.
    # +transport+:: see NetHTTPTransport; for proxies and tests.
    # +sleeper+:: called with the number of seconds to wait between retries; for tests.
    def initialize(base_url:, api_key:, max_retries: Retry::DEFAULT_MAX_RETRIES, timeout: 30, user_agent: nil, transport: nil, sleeper: nil)
      raise ArgumentError, "hansho: base_url is required" unless base_url.is_a?(String) && !base_url.empty?
      raise ArgumentError, "hansho: api_key is required" unless api_key.is_a?(String) && !api_key.empty?
      raise ArgumentError, "hansho: api_key must not contain control characters" if control?(api_key)

      uri = URI.parse(base_url)
      raise ArgumentError, "hansho: base_url must be an http or https URL" unless %w[http https].include?(uri.scheme) && uri.host

      @base_url = base_url.sub(%r{/+\z}, "")
      @uri = uri
      @prefix = uri.path.sub(%r{/+\z}, "")
      @api_key = api_key
      @max_retries = [max_retries.to_i, 0].max
      @timeout = timeout
      @user_agent = user_agent || "#{NAME}-ruby/#{VERSION}"
      @transport = transport || NetHTTPTransport.new
      @sleeper = sleeper || ->(seconds) { sleep(seconds) }
    end

    # Keeps the API key out of logs.
    def inspect
      "#<#{self.class.name} base_url=#{@base_url.inspect}>"
    end
    alias to_s inspect

    def to_json(*args)
      { base_url: @base_url }.to_json(*args)
    end

    # --- pairing -----------------------------------------------------------

    # Creates a single-use pairing ticket (valid 10 minutes). Render +qr+ as a QR
    # code for the user to scan in the Hansho app. Returns a PairingTicket.
    def create_pairing_ticket(external_user_id)
      raise ArgumentError, "hansho: id must not be empty" if external_user_id.to_s.empty?

      w = json(:post, "/api/v1/pairing-tickets", { external_user_id: external_user_id })
      PairingTicket.new(ticket: w["ticket"], qr: w["qr"], expires_at: time(w["expires_at"]))
    end

    # Revokes every pairing of the user.
    def unpair(external_user_id)
      json(:delete, "/api/v1/users/#{seg(external_user_id)}/pairings")
      nil
    end

    # Ends the user's whole pairing, on every device: +unpair+ under its sender-side name. The app tells the person
    # that the sender ended it (a 401 pairing_revoked with reason "kicked"), where an unpair they did themselves
    # carries no reason. There is no block list; a new ticket pairs them again.
    def kick(external_user_id)
      unpair(external_user_id)
    end

    # --- catalog -----------------------------------------------------------

    # Creates or replaces a channel. +actions+ is an Array of Hashes
    # (`{ id: "ack", title: "Acknowledge", ack: true }`). +audience+ says who may use the channel:
    # `{ mode: :only, users: ["alice"], groups: ["oncall"] }` (+mode+ is :everyone, :only or :except; users are your
    # external user ids, groups must exist). Leaving it out keeps the audience stored on the hub (a new channel is
    # open to everyone); pass `{ mode: :everyone }` to open a channel up again.
    def put_channel(id, name:, default_level:, max_level:, sound: nil, template_id: nil, thread: nil, actions: nil, audience: nil)
      wire = {
        name: name, default_level: level_to_wire(default_level), max_level: level_to_wire(max_level),
        sound: sound, template_id: template_id, thread: thread, actions: actions_to_wire(actions),
        audience: audience_to_wire(audience)
      }.compact
      json(:put, "/api/v1/channels/#{seg(id)}", wire)
      nil
    end

    # Returns a Channel.
    def get_channel(id)
      w = json(:get, "/api/v1/channels/#{seg(id)}")
      Channel.new(
        name: w["name"], default_level: w["default_level"], max_level: w["max_level"], sound: w["sound"],
        template_id: w["template_id"], thread: w["thread"], actions: actions_from_wire(w["actions"]),
        audience: audience_from_wire(w["audience"])
      )
    end

    # --- groups ------------------------------------------------------------

    # Creates a group or replaces its whole member list (external user ids, who may not have paired yet).
    # Audiences that name the group follow at once. Ids match `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`. Returns a Group.
    def put_group(id, members)
      raise ArgumentError, "hansho: members must be an Array" unless members.is_a?(Array)

      group_from_wire(json(:put, "/api/v1/groups/#{seg(id)}", { members: members }), id)
    end

    # Returns a Group; a missing one is an APIError with code APIError::UNKNOWN_GROUP.
    def get_group(id)
      group_from_wire(json(:get, "/api/v1/groups/#{seg(id)}"), id)
    end

    # An audience that still names the group matches nobody through it.
    def delete_group(id)
      json(:delete, "/api/v1/groups/#{seg(id)}")
      nil
    end

    def delete_channel(id)
      json(:delete, "/api/v1/channels/#{seg(id)}")
      nil
    end

    # Stores a template version; the version must increase. +body+ is the template
    # as Hash/Array (anything JSON can express).
    def put_template(id, version, body)
      json(:put, "/api/v1/templates/#{seg(id)}", { version: version, body: body })
      nil
    end

    # Uploads a sound (+audio/x-caf+, +audio/wav+ or +audio/aiff+, at most 1 MB). Returns a SoundResult.
    def put_sound(id, content_type, bytes)
      path = "/api/v1/sounds/#{seg(id)}"
      raise ArgumentError, "hansho: bytes must be a String" unless bytes.is_a?(String)
      raise ArgumentError, "hansho: sound exceeds 1 MB" if bytes.bytesize > MAX_SOUND_BYTES
      raise ArgumentError, "hansho: content_type is required" unless content_type.is_a?(String) && !content_type.empty? && !control?(content_type)

      w = send_call(Call.new(verb: "PUT", path: path, body: bytes.b, content_type: content_type, retryable: true))
      SoundResult.new(id: w["id"], filename: w["filename"], sha256: w["sha256"], size: w["size"])
    end

    # --- messages ----------------------------------------------------------

    # Sends a notification, or a page when +level+ is +:page+. +level+ is a String or
    # Symbol (+:time_sensitive+ is read as "time-sensitive"); it defaults to the
    # channel's default and is capped at its max. +data+ is sent as given (its keys
    # are never renamed). +page+ is `{ interval_seconds: 60, max_retries: 3 }`.
    #
    # It is never retried automatically (a repeat would notify twice) unless you pass
    # an +idempotency_key+, which is sent as the Idempotency-Key header, never in the
    # body. The hub does not deduplicate on it yet, so a retried send is at-least-once.
    #
    # Give exactly one target: +external_user_id+, +external_user_ids+ (an Array, at most 1000) or +to_audience: true+
    # (every paired user in the channel's audience, resolved when you send). Users outside the audience are not
    # delivered to: their MessageResult#recipients entry has status RECIPIENT_NO_ACCESS.
    # Returns a MessageResult.
    def send_message(channel:, title:, body:, external_user_id: nil, external_user_ids: nil, to_audience: nil, level: nil,
                     subtitle: nil, image_url: nil, template_id: nil, data: nil, sound: nil, actions: nil, collapse_id: nil,
                     page: nil, idempotency_key: nil)
      unless external_user_ids.nil? || external_user_ids.is_a?(Array)
        raise ArgumentError, "hansho: external_user_ids must be an Array"
      end

      wire = {
        external_user_id: external_user_id, external_user_ids: external_user_ids, to_audience: to_audience ? true : nil,
        channel: channel, level: level_to_wire(level), title: title, subtitle: subtitle,
        body: body, image_url: image_url, template_id: template_id, data: data, sound: sound,
        actions: actions_to_wire(actions), collapse_id: collapse_id, page: page_to_wire(page)
      }.compact
      call = Call.new(verb: "POST", path: "/api/v1/messages", body: JSON.generate(wire), content_type: "application/json", retryable: false)
      unless idempotency_key.nil? || idempotency_key == ""
        raise ArgumentError, "hansho: idempotency_key must not contain control characters" if control?(idempotency_key.to_s)

        call.retryable = true
        call.headers = { "Idempotency-Key" => idempotency_key.to_s }
      end
      w = send_call(call)
      MessageResult.new(message_id: w["message_id"], level: w["level"], devices: w["devices"], status: w["status"],
                        deliveries: deliveries_from_wire(w["deliveries"]), recipients: recipients_from_wire(w["recipients"]))
    end

    # Returns a MessageStatus.
    def get_message(id)
      w = json(:get, "/api/v1/messages/#{seg(id)}")
      MessageStatus.new(message_id: w["message_id"], status: w["status"], level: w["level"], attempts: w["attempts"],
                        deliveries: deliveries_from_wire(w["deliveries"]), acked_at: time(w["acked_at"]))
    end

    # --- live activities and feeds -----------------------------------------

    # Starts a Live Activity. +alert+ is `{ title:, body: }`. Returns an ActivityStarted.
    def start_activity(external_user_id:, channel:, template_id:, data:, alert: nil)
      wire = { external_user_id: external_user_id, channel: channel, template_id: template_id, data: data,
               alert: alert_to_wire(alert) }.compact
      w = json(:post, "/api/v1/activities", wire)
      ActivityStarted.new(activity_id: w["activity_id"], devices: w["devices"])
    end

    # Not retried automatically. +priority+ is 5 (default) or 10. Returns an ActivityUpdated.
    def update_activity(id, data:, priority: nil, alert: nil)
      wire = { data: data, priority: priority, alert: alert_to_wire(alert) }.compact
      w = json(:patch, "/api/v1/activities/#{seg(id)}", wire)
      ActivityUpdated.new(activity_id: w["activity_id"], update: w["update"])
    end

    def end_activity(id)
      json(:delete, "/api/v1/activities/#{seg(id)}")
      nil
    end

    # Publishes a widget feed; omit +external_user_id+ for one shared by all paired
    # users. +title+ is the name the app's feed picker shows. The feed uses the audience of +channel+ unless it sets its
    # own +audience+ (see #put_channel); leaving both out keeps the values stored on the hub. Returns a FeedResult.
    def put_feed(id, template_id:, data:, external_user_id: nil, title: nil, channel: nil, audience: nil)
      wire = { external_user_id: external_user_id, template_id: template_id, title: title, channel: channel,
               audience: audience_to_wire(audience), data: data }.compact
      w = json(:put, "/api/v1/feeds/#{seg(id)}", wire)
      FeedResult.new(feed_id: w["feed_id"], devices: w["devices"])
    end

    # A feed's settings without its data; omit +external_user_id+ for the feed shared by all users. Returns a FeedSettings.
    def get_feed(id, external_user_id: nil)
      query = external_user_id.nil? || external_user_id == "" ? "" : "?external_user_id=#{URI.encode_www_form_component(external_user_id.to_s)}"
      w = json(:get, "/api/v1/feeds/#{seg(id)}#{query}")
      FeedSettings.new(feed_id: w["feed_id"], external_user_id: w["external_user_id"], template_id: w["template_id"],
                       title: w["title"], channel: w["channel"], audience: audience_from_wire(w["audience"]),
                       updated_at: w["updated_at"])
    end

    private

    # Escapes one path segment; rejects ids that would not stay in their collection.
    def seg(id)
      raise ArgumentError, "hansho: id must be a String" unless id.is_a?(String)
      raise ArgumentError, "hansho: id must not be empty" if id.empty?
      raise ArgumentError, %(hansho: id must not be "#{id}") if [".", ".."].include?(id)

      id.b.gsub(/[^A-Za-z0-9\-_.~]/n) { |c| format("%%%02X", c.ord) }
    end

    def control?(string)
      string.match?(/[\x00-\x1f\x7f]/)
    end

    def symbolize(hash, what)
      raise ArgumentError, "hansho: #{what} must be a Hash" unless hash.is_a?(Hash)

      hash.transform_keys(&:to_sym)
    end

    def level_to_wire(level)
      level&.to_s&.tr("_", "-")
    end

    def actions_to_wire(actions)
      return nil if actions.nil?
      raise ArgumentError, "hansho: actions must be an Array" unless actions.is_a?(Array)

      actions.map do |a|
        a = symbolize(a, "action")
        { id: a[:id], title: a[:title], ack: a[:ack], kind: a[:kind] }.compact
      end
    end

    def actions_from_wire(raw)
      return nil unless raw.is_a?(Array)

      raw.map { |a| { id: a["id"], title: a["title"], ack: a["ack"], kind: a["kind"] }.compact }
    end

    def page_to_wire(page)
      return nil if page.nil?

      p = symbolize(page, "page")
      { interval_s: p[:interval_seconds], max_retries: p[:max_retries] }.compact
    end

    def alert_to_wire(alert)
      return nil if alert.nil?

      a = symbolize(alert, "alert")
      { title: a[:title], body: a[:body] }.compact
    end

    def audience_to_wire(audience)
      return nil if audience.nil?

      a = symbolize(audience, "audience")
      %i[users groups].each do |key|
        raise ArgumentError, "hansho: audience #{key} must be an Array" unless a[key].nil? || a[key].is_a?(Array)
      end
      { mode: a[:mode]&.to_s, users: non_empty(a[:users]), groups: non_empty(a[:groups]) }.compact
    end

    def non_empty(list)
      list.nil? || list.empty? ? nil : list
    end

    def audience_from_wire(raw)
      return nil unless raw.is_a?(Hash)

      { mode: raw["mode"], users: raw["users"].is_a?(Array) ? raw["users"] : [], groups: raw["groups"].is_a?(Array) ? raw["groups"] : [] }
    end

    def group_from_wire(w, id)
      Group.new(id: w["id"] || id, members: w["members"].is_a?(Array) ? w["members"] : [])
    end

    def recipients_from_wire(raw)
      return [] unless raw.is_a?(Array)

      raw.select { |r| r.is_a?(Hash) }.map do |r|
        Recipient.new(external_user_id: r["external_user_id"], message_id: r["message_id"], status: r["status"],
                      devices: r["devices"] || 0, deliveries: deliveries_from_wire(r["deliveries"]))
      end
    end

    def deliveries_from_wire(raw)
      return [] unless raw.is_a?(Array)

      raw.map do |d|
        err = d["error"]
        Delivery.new(device_id: d["device_id"], status: d["status"], error: err.nil? || err == "" ? nil : err)
      end
    end

    def time(value)
      value.is_a?(String) ? Time.iso8601(value) : nil
    rescue ArgumentError
      nil
    end

    def json(method, path, body = nil)
      verb = method.to_s.upcase
      call = Call.new(verb: verb, path: path, retryable: Retry.idempotent?(verb))
      unless body.nil?
        call.body = JSON.generate(body)
        call.content_type = "application/json"
      end
      send_call(call)
    end

    def send_call(call)
      attempt = 0
      loop do
        res = round_trip(call)
        return parse_success(call, res) if res.status.between?(200, 299)

        err = api_error(call, res)
        raise err if !call.retryable || !Retry.retryable_status?(res.status) || attempt >= @max_retries

        wait_ms = err.retry_after_ms&.positive? ? err.retry_after_ms : Retry.backoff_ms(attempt)
        raise err if wait_ms > Retry::MAX_WAIT_MS

        @sleeper.call(wait_ms / 1000.0)
        attempt += 1
      end
    end

    def round_trip(call)
      headers = { "Authorization" => "Bearer #{@api_key}", "Accept" => "application/json", "User-Agent" => @user_agent }
      headers.merge!(call.headers) if call.headers
      headers["Content-Type"] = call.content_type if call.content_type
      begin
        @transport.call(method: call.verb, url: @uri, target: @prefix + call.path, headers: headers, body: call.body, timeout: @timeout)
      rescue StandardError => e
        safe = redact(e)
        raise NetworkError.new(call.verb, call.path, safe), cause: safe
      end
    end

    def redact(error)
      return error unless error.message.include?(@api_key)

      RuntimeError.new(error.message.gsub(@api_key, "[redacted]"))
    end

    # The body as valid UTF-8 text; bytes that are not UTF-8 become U+FFFD instead of raising.
    def text_of(res)
      res.body.to_s.dup.force_encoding(Encoding::UTF_8).scrub
    end

    def parse_success(call, res)
      text = text_of(res)
      return {} if text.strip.empty?

      parsed = JSON.parse(text)
      parsed.is_a?(Hash) ? parsed : {}
    rescue JSON::ParserError
      raise NetworkError.new(call.verb, call.path, "invalid JSON in response")
    end

    def api_error(call, res)
      code = field = detail = nil
      begin
        p = JSON.parse(text_of(res))
        if p.is_a?(Hash)
          code = p["error"] if p["error"].is_a?(String)
          field = p["field"] if p["field"].is_a?(String) && !p["field"].empty?
          detail = p["message"].gsub(@api_key, "[redacted]") if p["message"].is_a?(String)
        end
      rescue JSON::ParserError
        nil # not JSON (proxy error page): status only
      end
      retry_after = Retry.parse_retry_after(res.headers["retry-after"])
      APIError.new(method: call.verb, path: call.path, status: res.status, code: code, field: field,
                   retry_after_ms: retry_after&.positive? ? retry_after : nil, detail: detail)
    end
  end
end

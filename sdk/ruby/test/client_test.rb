# frozen_string_literal: true

require_relative "test_helper"
require "time"

class ClientTest < Minitest::Test
  include HubHelper

  CHANNEL = { name: "Ops", default_level: "active", max_level: "critical" }.freeze
  MSG = { external_user_id: "u", channel: "c", title: "t", body: "b" }.freeze

  def body_of(request)
    JSON.parse(request.body)
  end

  def throttle(status = 429, headers = { "Retry-After" => "2" })
    ->(_r) { reply(status, '{"error":"rate_limited"}', headers) }
  end

  # --- basics ---------------------------------------------------------------

  def test_sends_headers_and_uses_the_default_user_agent
    with_hub do |h|
      h.client.put_channel("ops", **CHANNEL)
      r = h.requests[0]
      assert_equal "Bearer #{TEST_KEY}", r.headers["authorization"]
      assert_equal "application/json", r.headers["accept"]
      assert_equal "application/json", r.headers["content-type"]
      assert_match(%r{\Ahansho-ruby/#{Regexp.escape(Hansho::VERSION)}\z}, r.headers["user-agent"])
      h.client(user_agent: "acme/2").end_activity("a")
      assert_equal "acme/2", h.requests[1].headers["user-agent"]
      assert_nil h.requests[1].headers["content-type"]
    end
  end

  def test_trailing_slash_and_path_prefix_in_base_url
    with_hub do |h|
      Hansho::Client.new(base_url: "#{h.url}/hansho/", api_key: TEST_KEY).get_message("m1")
      assert_equal "/hansho/api/v1/messages/m1", h.requests[0].target
    end
  end

  def test_exports_constants
    assert_equal "hansho", Hansho::NAME
    assert_match(/\A\d+\.\d+\.\d+/, Hansho::VERSION)
    assert_equal %w[passive active time-sensitive critical page], Hansho::LEVELS
  end

  def test_requires_base_url_and_api_key
    assert_raises(ArgumentError) { Hansho::Client.new(base_url: "", api_key: "k") }
    assert_raises(ArgumentError) { Hansho::Client.new(base_url: "http://x", api_key: "") }
    assert_raises(ArgumentError) { Hansho::Client.new(base_url: "http://x", api_key: nil) }
    assert_raises(ArgumentError) { Hansho::Client.new(base_url: "ftp://x", api_key: "k") }
    assert_raises(ArgumentError) { Hansho::Client.new(base_url: "hub.example.com", api_key: "k") }
    assert_raises(ArgumentError) { Hansho::Client.new(base_url: "http://x", api_key: "k\r\nX: y") }
  end

  def test_does_not_expose_the_key_when_inspected_or_serialised
    c = Hansho::Client.new(base_url: "http://x", api_key: TEST_KEY)
    [c.inspect, c.to_s, c.to_json, JSON.generate(c), "#{c}"].each { |s| refute_includes s, TEST_KEY }
    assert_equal({ "base_url" => "http://x" }, JSON.parse(c.to_json))
  end

  def test_omits_unset_optional_fields_from_the_wire
    with_hub do |h|
      h.client.send_message(**MSG)
      assert_equal({ "external_user_id" => "u", "channel" => "c", "title" => "t", "body" => "b" }, body_of(h.requests[0]))
      h.client.put_channel("ops", **CHANNEL)
      assert_equal({ "name" => "Ops", "default_level" => "active", "max_level" => "critical" }, body_of(h.requests[1]))
    end
  end

  def test_sends_empty_strings_and_zero_as_given
    with_hub do |h|
      h.client.send_message(**MSG, title: "", level: :page, page: { max_retries: 0 })
      wire = body_of(h.requests[0])
      assert_equal "", wire["title"]
      assert_equal({ "max_retries" => 0 }, wire["page"])
    end
  end

  def test_levels_may_be_symbols_with_underscores
    with_hub do |h|
      h.client.send_message(**MSG, level: :time_sensitive)
      h.client.send_message(**MSG, level: "critical")
      h.client.put_channel("ops", name: "n", default_level: :active, max_level: :time_sensitive)
      assert_equal "time-sensitive", body_of(h.requests[0])["level"]
      assert_equal "critical", body_of(h.requests[1])["level"]
      assert_equal %w[active time-sensitive], body_of(h.requests[2]).values_at("default_level", "max_level")
    end
  end

  def test_data_keys_are_never_renamed
    with_hub do |h|
      h.client.send_message(**MSG, data: { "camelCase" => 1, snake_case: { "Deep_Key" => [{ a_b: 1 }] } })
      assert_equal({ "camelCase" => 1, "snake_case" => { "Deep_Key" => [{ "a_b" => 1 }] } }, body_of(h.requests[0])["data"])
    end
  end

  def test_hashes_may_use_string_keys
    with_hub do |h|
      h.client.send_message(**MSG, actions: [{ "id" => "a", "title" => "A", "ack" => true, "ignored" => 1 }], page: { "interval_seconds" => 5 })
      wire = body_of(h.requests[0])
      assert_equal [{ "id" => "a", "title" => "A", "ack" => true }], wire["actions"]
      assert_equal({ "interval_s" => 5 }, wire["page"])
    end
  end

  def test_rejects_malformed_nested_arguments_before_sending
    with_hub do |h|
      assert_raises(ArgumentError) { h.client.send_message(**MSG, actions: "x") }
      assert_raises(ArgumentError) { h.client.send_message(**MSG, actions: ["x"]) }
      assert_raises(ArgumentError) { h.client.send_message(**MSG, page: 5) }
      assert_raises(ArgumentError) { h.client.start_activity(external_user_id: "u", channel: "c", template_id: "t", data: 1, alert: "x") }
      assert_raises(ArgumentError) { h.client.send_message(**MSG, idempotency_key: "a\nb") }
      assert_empty h.requests
    end
  end

  def test_sound_limits_and_bytes
    with_hub do |h|
      assert_raises(ArgumentError) { h.client.put_sound("s", "audio/wav", "x" * ((1 << 20) + 1)) }
      assert_raises(ArgumentError) { h.client.put_sound("s", "audio/wav", nil) }
      assert_raises(ArgumentError) { h.client.put_sound("s", "", "x") }
      assert_empty h.requests
      h.client.put_sound("s", "audio/wav", "x" * (1 << 20))
      assert_equal 1 << 20, h.requests[0].body.bytesize
      binary = (0..255).map(&:chr).join.b
      h.client.put_sound("s", "audio/x-caf", binary)
      assert_equal binary, h.requests[1].body
      assert_equal "audio/x-caf", h.requests[1].headers["content-type"]
    end
  end

  def test_empty_2xx_body_and_non_object_json_are_fine
    answers = ["", "  ", "[1]", "null"]
    with_hub(->(_r) { reply(200, answers.shift) }) do |h|
      4.times { assert_nil h.client.end_activity("a") }
    end
  end

  def test_bytes_that_are_not_utf8_never_raise_a_bare_error
    with_hub(->(_r) { reply(200, "{\"message_id\":\"m\xff\",\"status\":\"queued\"}") }) do |h|
      assert_equal "m\xEF\xBF\xBD", h.client.get_message("m").message_id
    end
    with_hub(->(_r) { reply(502, "\xff\xfe") }) do |h|
      e = assert_raises(Hansho::APIError) { h.client.get_message("m") }
      assert_equal 502, e.status
    end
    with_hub(->(_r) { reply(400, "{\"error\":\"bad\",\"message\":\"x\xff\"}") }) do |h|
      assert_equal "bad", assert_raises(Hansho::APIError) { h.client.get_message("m") }.code
    end
  end

  def test_connection_resets_are_not_repeated_by_net_http
    raw = TCPServer.new("127.0.0.1", 0)
    hits = 0
    Thread.new do
      loop do
        s = raw.accept
        hits += 1
        s.close
      end
    rescue IOError
      nil
    end
    c = Hansho::Client.new(base_url: "http://127.0.0.1:#{raw.addr[1]}", api_key: TEST_KEY)
    assert_raises(Hansho::NetworkError) { c.delete_channel("a") }
    assert_equal 1, hits
  ensure
    raw&.close
  end

  def test_invalid_json_in_a_2xx_answer_is_a_network_error
    with_hub(->(_r) { reply(200, "<html>") }) do |h|
      e = assert_raises(Hansho::NetworkError) { h.client.get_message("m") }
      assert_equal "hansho: GET /api/v1/messages/m: invalid JSON in response", e.message
    end
  end

  # --- forward compatibility ------------------------------------------------

  def test_ignores_response_fields_it_does_not_know
    answer = '{"message_id":"m","status":"delivered","level":"page","attempts":1,"deliveries":[{"device_id":"d","status":"sent","brand_new":{"x":[1]}}],"brand_new":"v","acked_at":"2026-11-08T12:05:00Z"}'
    with_hub(->(_r) { reply(200, answer) }) do |h|
      ms = h.client.get_message("m")
      assert_equal "m", ms.message_id
      assert_equal 1, ms.deliveries.length
      assert_nil ms.deliveries[0].error
      refute_respond_to ms, :brand_new
      assert_equal Time.utc(2026, 11, 8, 12, 5), ms.acked_at
    end
  end

  def test_display_name_put_path_escaping_and_empty_id
    with_hub(->(_r) { reply(204, "") }) do |h|
      assert_nil h.client.set_user_display_name("a/b c", "Ada")
      h.client.set_user_display_name("u", "")
      r = h.requests
      assert_equal "PUT", r[0].method
      assert_equal "/api/v1/users/a%2Fb%20c", r[0].target
      assert_equal({ "display_name" => "Ada" }, body_of(r[0]))
      assert_equal({ "display_name" => "" }, body_of(r[1]))
      assert_raises(ArgumentError) { h.client.set_user_display_name("", "x") }
      assert_equal 2, h.requests.length
    end
  end

  def test_pairing_ticket_body_with_and_without_display_name
    with_hub(->(_r) { reply(201, '{"ticket":"t","qr":"q","expires_at":"2026-11-08T12:10:00Z"}') }) do |h|
      h.client.create_pairing_ticket("u")
      h.client.create_pairing_ticket("u", display_name: "")
      h.client.create_pairing_ticket("u", display_name: "Ada")
      assert_equal({ "external_user_id" => "u" }, body_of(h.requests[0]))
      assert_equal({ "external_user_id" => "u", "display_name" => "" }, body_of(h.requests[1]))
      assert_equal "Ada", body_of(h.requests[2])["display_name"]
    end
  end

  def test_page_ack_option_is_sent_only_when_set
    with_hub do |h|
      h.client.send_message(**MSG, level: :page, page: { interval_seconds: 60 })
      h.client.send_message(**MSG, level: :page, page: { ack: :each })
      h.client.send_message(**MSG, level: :page, page: { ack: "any" })
      assert_equal({ "interval_s" => 60 }, body_of(h.requests[0])["page"])
      assert_equal({ "ack" => "each" }, body_of(h.requests[1])["page"])
      assert_equal({ "ack" => "any" }, body_of(h.requests[2])["page"])
    end
  end

  def test_message_status_acked_by_present_and_absent
    with_hub(->(_r) { reply(200, '{"message_id":"m","status":"acked","acked_by":"bob","acked_by_name":"Bob ボブ"}') }) do |h|
      ms = h.client.get_message("m")
      assert_equal "bob", ms.acked_by
      assert_equal "Bob ボブ", ms.acked_by_name
    end
    with_hub(->(_r) { reply(200, '{"message_id":"m","status":"acked"}') }) do |h|
      ms = h.client.get_message("m")
      assert_nil ms.acked_by
      assert_nil ms.acked_by_name
    end
  end

  def test_missing_optional_response_fields_are_nil_or_empty
    with_hub(->(_r) { reply(200, '{"message_id":"m","status":"queued"}') }) do |h|
      ms = h.client.get_message("m")
      assert_nil ms.acked_at
      assert_equal [], ms.deliveries
      ch = h.client.get_channel("c")
      assert_nil ch.actions
      assert_nil ch.sound
    end
  end

  # --- retries --------------------------------------------------------------

  def test_retries_put_on_429_and_honours_retry_after
    n = 0
    with_hub(->(_r) { (n += 1) <= 2 ? throttle.call(nil) : reply(200) }) do |h|
      h.client.put_channel("ops", **CHANNEL)
      assert_equal 3, h.requests.length
      assert_equal [2.0, 2.0], h.waits
    end
  end

  def test_understands_an_http_date_retry_after
    n = 0
    answer = lambda { |_r|
      (n += 1) == 1 ? reply(429, "{}", { "Retry-After" => (Time.now + 30).httpdate }) : reply(200)
    }
    with_hub(answer) do |h|
      h.client.delete_channel("ops")
      assert_equal 1, h.waits.length
      assert_operator h.waits[0], :>=, 20
      assert_operator h.waits[0], :<=, 31
    end
  end

  def test_stops_after_max_retries_and_surfaces_the_error
    with_hub(->(_r) { reply(429, '{"error":"rate_limited"}', { "Retry-After" => "1" }) }) do |h|
      e = assert_raises(Hansho::APIError) { h.client.put_channel("ops", **CHANNEL) }
      assert_equal [429, 1000, 1.0], [e.status, e.retry_after_ms, e.retry_after]
      assert_equal 4, h.requests.length
      assert_raises(Hansho::APIError) { h.client(max_retries: 0).put_channel("ops", **CHANNEL) }
      assert_equal 5, h.requests.length
      assert_raises(Hansho::APIError) { h.client(max_retries: 1).put_channel("ops", **CHANNEL) }
      assert_equal 7, h.requests.length
    end
  end

  def test_retries_get_and_delete_on_503_with_jittered_backoff
    seen = {}
    answer = lambda { |r|
      key = "#{r.method} #{r.target}"
      seen[key] ? reply(200) : (seen[key] = reply(503, '{"error":"unavailable"}'))
    }
    with_hub(answer) do |h|
      h.client.get_channel("x")
      h.client.delete_channel("x")
      h.client.end_activity("x")
      assert_equal 3, h.waits.length
      h.waits.each { |w| assert w >= 0.125 && w <= 0.25, w.to_s }
    end
  end

  def test_backoff_grows_and_is_capped
    waits = (0..12).map { |a| Hansho::Retry.backoff_ms(a) }
    [[250, 0], [500, 1], [1000, 2], [8000, 5], [8000, 12]].each do |d, a|
      assert_operator waits[a], :>=, d / 2.0
      assert_operator waits[a], :<=, d.to_f
    end
    assert_equal 125.0, Hansho::Retry.backoff_ms(0, random: Class.new { def self.rand = 0.0 })
  end

  def test_does_not_retry_statuses_other_than_429_and_503
    [400, 401, 404, 409, 500, 502].each do |status|
      with_hub(->(_r) { reply(status) }) do |h|
        assert_raises(Hansho::APIError) { h.client.put_channel("ops", **CHANNEL) }
        assert_equal 1, h.requests.length, "status #{status}"
        assert_empty h.waits
      end
    end
  end

  def test_post_messages_is_not_retried
    [429, 503].each do |status|
      with_hub(throttle(status, { "Retry-After" => "3" })) do |h|
        e = assert_raises(Hansho::APIError) { h.client.send_message(**MSG) }
        assert_equal [status, "rate_limited", 3000], [e.status, e.code, e.retry_after_ms]
        assert_equal 1, h.requests.length
        assert_empty h.waits
      end
    end
  end

  def test_post_pairing_tickets_and_activities_are_not_retried
    with_hub(throttle) do |h|
      assert_raises(Hansho::APIError) { h.client.create_pairing_ticket("u") }
      assert_raises(Hansho::APIError) { h.client.start_activity(external_user_id: "u", channel: "c", template_id: "t", data: 1) }
      assert_equal 2, h.requests.length
      assert_empty h.waits
    end
  end

  def test_idempotency_key_makes_a_message_retryable_without_leaking_into_the_body
    n = 0
    answer = lambda { |_r|
      (n += 1) == 1 ? reply(429, "{}", { "Retry-After" => "1" }) : reply(202, '{"message_id":"m1","status":"queued"}')
    }
    with_hub(answer) do |h|
      res = h.client.send_message(**MSG, idempotency_key: "order-7")
      assert_equal "m1", res.message_id
      assert_equal 2, h.requests.length
      h.requests.each do |r|
        assert_equal "order-7", r.headers["idempotency-key"]
        refute_includes r.body, "order-7"
      end
      assert_equal h.requests[0].body, h.requests[1].body
    end
  end

  def test_no_idempotency_key_header_by_default
    with_hub do |h|
      h.client.send_message(**MSG)
      h.client.send_message(**MSG, idempotency_key: "")
      assert_nil h.requests[0].headers["idempotency-key"]
      assert_nil h.requests[1].headers["idempotency-key"]
    end
  end

  def test_patch_is_not_retried
    with_hub(->(_r) { reply(429) }) do |h|
      assert_raises(Hansho::APIError) { h.client.update_activity("a", data: 1) }
      assert_equal 1, h.requests.length
    end
  end

  def test_a_retry_after_beyond_the_cap_is_surfaced
    with_hub(throttle(429, { "Retry-After" => "3600" })) do |h|
      e = assert_raises(Hansho::APIError) { h.client.put_channel("ops", **CHANNEL) }
      assert_equal 3_600_000, e.retry_after_ms
      assert_equal 1, h.requests.length
      assert_empty h.waits
    end
  end

  def test_a_retry_after_of_exactly_60_seconds_is_waited_out
    n = 0
    with_hub(->(_r) { (n += 1) == 1 ? throttle(429, { "Retry-After" => "60" }).call(nil) : reply(200) }) do |h|
      h.client.put_channel("ops", **CHANNEL)
      assert_equal [60.0], h.waits
    end
  end

  def test_retry_after_zero_or_garbage_falls_back_to_backoff
    ["0", "soon", ""].each do |value|
      n = 0
      with_hub(->(_r) { (n += 1) == 1 ? reply(503, "{}", { "Retry-After" => value }) : reply(200) }) do |h|
        h.client.delete_channel("x")
        assert_equal 1, h.waits.length
        assert h.waits[0] >= 0.125 && h.waits[0] <= 0.25, h.waits[0].to_s
      end
    end
  end

  def test_parse_retry_after
    now = Time.utc(2026, 1, 1)
    assert_equal 7000, Hansho::Retry.parse_retry_after("7")
    assert_equal 1500, Hansho::Retry.parse_retry_after(" 1.5 ")
    assert_equal 30_000, Hansho::Retry.parse_retry_after((now + 30).httpdate, now: now)
    assert_equal 0, Hansho::Retry.parse_retry_after((now - 30).httpdate, now: now)
    assert_nil Hansho::Retry.parse_retry_after(nil)
    assert_nil Hansho::Retry.parse_retry_after("-3")
    assert_nil Hansho::Retry.parse_retry_after("tomorrow")
  end

  # --- path escaping --------------------------------------------------------

  ESCAPE_IDS = ["a/b", "a?b=c", "a b", "ü", "日本", "a#b", "100%", "a/../b", "a%2Fb", "a+b", "x\ny"].freeze

  def unescape(segment)
    segment.gsub(/%\h\h/) { |m| m[1..].hex.chr }.force_encoding(Encoding::UTF_8)
  end

  def test_escapes_every_id
    with_hub do |h|
      c = h.client
      ESCAPE_IDS.each do |id|
        cases = [
          ["/api/v1/channels/", -> { c.put_channel(id, **CHANNEL) }],
          ["/api/v1/channels/", -> { c.get_channel(id) }],
          ["/api/v1/channels/", -> { c.delete_channel(id) }],
          ["/api/v1/templates/", -> { c.put_template(id, 1, {}) }],
          ["/api/v1/sounds/", -> { c.put_sound(id, "audio/wav", "x") }],
          ["/api/v1/feeds/", -> { c.put_feed(id, template_id: "t", data: 1) }],
          ["/api/v1/messages/", -> { c.get_message(id) }],
          ["/api/v1/activities/", -> { c.update_activity(id, data: 1) }],
          ["/api/v1/activities/", -> { c.end_activity(id) }]
        ]
        cases.each do |prefix, call|
          before = h.requests.length
          call.call
          target = h.requests[before].target
          assert target.start_with?(prefix), target
          seg = target[prefix.length..]
          refute_match(%r{[/?# ]}, seg, "unescaped segment in #{target}")
          assert_equal id, unescape(seg), target
        end
        before = h.requests.length
        c.unpair(id)
        m = %r{\A/api/v1/users/([^/?#]+)/pairings\z}.match(h.requests[before].target)
        assert m, h.requests[before].target
        assert_equal id, unescape(m[1])
      end
    end
  end

  def test_exact_encoding
    with_hub do |h|
      h.client.put_channel("ü x/y?z", **CHANNEL)
      assert_equal "/api/v1/channels/%C3%BC%20x%2Fy%3Fz", h.requests[0].target
      h.client.get_channel("A-z_0.9~")
      assert_equal "/api/v1/channels/A-z_0.9~", h.requests[1].target
    end
  end

  def test_rejects_dot_segments_empty_and_non_string_ids_before_sending
    with_hub do |h|
      c = h.client
      ["", ".", "..", nil, 5, :sym].each do |bad|
        assert_raises(ArgumentError, bad.inspect) { c.get_channel(bad) }
        assert_raises(ArgumentError) { c.unpair(bad) }
        assert_raises(ArgumentError) { c.end_activity(bad) }
      end
      assert_raises(ArgumentError) { c.create_pairing_ticket("") }
      assert_empty h.requests
    end
  end

  # --- errors ---------------------------------------------------------------

  def test_network_errors_are_not_api_errors_and_carry_no_key
    hub = HubFixture.new
    url = hub.url
    hub.stop
    c = Hansho::Client.new(base_url: url, api_key: TEST_KEY, max_retries: 0)
    e = assert_raises(Hansho::NetworkError) { c.put_channel("ops", **CHANNEL) }
    refute_kind_of Hansho::APIError, e
    assert_kind_of Hansho::Error, e
    refute_includes e.message, TEST_KEY
    refute_nil e.cause
    assert_match(%r{\Ahansho: PUT /api/v1/channels/ops: }, e.message)
  end

  def test_network_errors_are_not_retried
    calls = 0
    transport = lambda { |**_|
      calls += 1
      raise Errno::ECONNRESET
    }
    c = Hansho::Client.new(base_url: "http://x", api_key: TEST_KEY, transport: transport, sleeper: ->(_) { flunk "slept" })
    assert_raises(Hansho::NetworkError) { c.get_channel("a") }
    assert_equal 1, calls
  end

  def test_a_transport_error_that_mentions_the_key_is_redacted
    transport = ->(**kw) { raise IOError, "boom #{kw[:headers]["Authorization"]}" }
    c = Hansho::Client.new(base_url: "http://x", api_key: TEST_KEY, transport: transport)
    e = assert_raises(Hansho::NetworkError) { c.get_channel("a") }
    refute_includes e.message, TEST_KEY
    refute_includes e.cause.message, TEST_KEY
    assert_includes e.message, "[redacted]"
  end

  def test_times_out_with_a_network_error
    server = MiniServer.new { |_r| sleep(3) && [200, {}, "{}"] }
    c = Hansho::Client.new(base_url: server.url, api_key: TEST_KEY, timeout: 0.3, max_retries: 0)
    t0 = Process.clock_gettime(Process::CLOCK_MONOTONIC)
    e = assert_raises(Hansho::NetworkError) { c.get_channel("a") }
    assert_operator Process.clock_gettime(Process::CLOCK_MONOTONIC) - t0, :<, 2.5
    assert_kind_of Timeout::Error, e.cause
    assert_equal 1, server.requests.length, "Net::HTTP must not repeat the request on its own"
  ensure
    server&.stop
  end

  def test_the_key_never_reaches_errors
    answer = ->(r) { reply(401, JSON.generate({ error: "unauthorized", message: "bad key #{r.headers["authorization"]}" })) }
    with_hub(answer) do |h|
      e = assert_raises(Hansho::APIError) { h.client.put_channel("ops", **CHANNEL) }
      refute_includes e.message, TEST_KEY
      refute_includes e.detail, TEST_KEY
      refute_includes e.inspect, TEST_KEY
      assert_includes e.detail, "[redacted]"
    end
  end

  def test_message_bodies_never_reach_error_messages
    with_hub(->(_r) { reply(400, '{"error":"invalid_request","field":"title","message":"required"}') }) do |h|
      e = assert_raises(Hansho::APIError) { h.client.send_message(**MSG, body: "super secret body", title: "") }
      refute_includes e.message, "super secret body"
      assert_equal "required", e.detail
    end
  end

  def test_api_error_messages
    e = Hansho::APIError.new(method: "POST", path: "/api/v1/messages", status: 400, code: "invalid_request", field: "title")
    assert_equal "hansho: POST /api/v1/messages: 400 invalid_request (field title)", e.message
    assert_kind_of StandardError, e
    assert_kind_of Hansho::Error, e
    plain = Hansho::APIError.new(method: "GET", path: "/x", status: 502)
    assert_equal "hansho: GET /x: 502", plain.message
    assert_equal ["", nil, nil], [plain.code, plain.field, plain.retry_after]
    limited = Hansho::APIError.new(method: "GET", path: "/x", status: 429, code: "rate_limited", retry_after_ms: 7000)
    assert_equal "hansho: GET /x: 429 rate_limited (retry after 7000 ms)", limited.message
  end

  def test_non_json_error_pages_give_status_only
    with_hub(->(_r) { reply(502, "<html>bad gateway</html>") }) do |h|
      e = assert_raises(Hansho::APIError) { h.client.get_message("m") }
      assert_equal [502, ""], [e.status, e.code]
      assert_nil e.field
      assert_nil e.detail
    end
  end

  def test_unknown_levels_are_left_to_the_hub
    with_hub do |h|
      h.client.send_message(**MSG, level: "pager")
      assert_equal "pager", body_of(h.requests[0])["level"]
    end
  end
end

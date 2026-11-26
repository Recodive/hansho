# frozen_string_literal: true

require_relative "test_helper"
require "openssl"
require "net/http"

class WebhookTest < Minitest::Test
  VECTORS = load_spec_file("webhook-vectors.json")

  def sign(secret, at, body)
    "t=#{at},v1=#{OpenSSL::HMAC.hexdigest("SHA256", secret, "#{at}.#{body}")}"
  end

  def verify(v)
    Hansho::Webhook.verify(secret: v["secret"], header: v["header"], body: v["body"], now: v["now"], tolerance: VECTORS["tolerance_seconds"])
  end

  def test_has_enough_vectors
    assert_operator VECTORS["vectors"].length, :>=, 20
  end

  VECTORS["vectors"].each_with_index do |v, i|
    define_method("test_vector_#{i}_#{v["name"].gsub(/[^A-Za-z0-9]+/, "_")}") do
      expect = v["expect"]
      unless expect["ok"]
        err = assert_raises(Hansho::WebhookError) { verify(v) }
        assert_equal expect["error"], err.code
        return
      end
      ev = verify(v)
      assert_equal expect["type"], ev.type
      assert_equal expect["id"], ev.id
      assert_equal Time.at(expect["created_at"]), ev.created_at
      assert_equal v["body"].b, ev.raw.b
      if expect["type"] == "unknown"
        assert ev.unknown?
        assert_equal expect["name"], ev.name
        assert_equal expect["data"], ev.data
      else
        refute ev.unknown?
        assert_equal expect["data"], ev.data.to_h.compact.transform_keys(&:to_s)
      end
      assert_equal expect["data"], JSON.parse(ev.raw)["data"]
    end
  end

  def test_vectors_cover_every_error_class_and_unknown_events
    classes = VECTORS["vectors"].map { |v| v["expect"]["ok"] ? (v["expect"]["type"] == "unknown" && "unknown") : v["expect"]["error"] }
    %w[stale bad_signature malformed_header unknown].each { |c| assert_includes classes, c }
  end

  BODY = '{"id":"1","event":"message.acked","created_at":1,"data":{}}'
  T = 1_700_000_000

  def check(offset, tolerance = nil)
    Hansho::Webhook.verify(secret: "s", header: sign("s", T, BODY), body: BODY, now: T + offset, tolerance: tolerance)
  end

  def assert_error(code, &block)
    assert_equal code, assert_raises(Hansho::WebhookError, &block).code
  end

  def test_replay_window_is_300_seconds_either_side
    [0, 300, -300].each { |o| check(o) }
    [301, -301, 86_400].each { |o| assert_error("stale") { check(o) } }
  end

  def test_tolerance_is_configurable_and_non_positive_means_default
    check(301, 600)
    assert_error("stale") { check(11, 10) }
    check(10, 10)
    check(300, 0)
    check(300, -5)
    assert_error("stale") { check(301, 0) }
  end

  def test_now_may_be_a_time_and_defaults_to_the_clock
    Hansho::Webhook.verify(secret: "s", header: sign("s", T, BODY), body: BODY, now: Time.at(T))
    fresh = sign("s", Time.now.to_i, BODY)
    assert_equal "message.acked", Hansho.verify_webhook(secret: "s", header: fresh, body: BODY).type
    assert_error("stale") { Hansho::Webhook.verify(secret: "s", header: sign("s", T, BODY), body: BODY) }
  end

  def test_accepts_binary_and_utf8_strings
    v = VECTORS["vectors"].find { |x| x["name"] == "valid pairing.created" }
    [v["body"], v["body"].b, v["body"].dup.force_encoding("ASCII-8BIT")].each do |body|
      ev = Hansho::Webhook.verify(secret: v["secret"], header: v["header"], body: body, now: v["now"])
      assert_equal "Bas’s iPhone <Ünï>", ev.data.device_name
    end
  end

  def test_message_acked_carries_who_acked_when_present_and_nil_when_absent
    full = '{"id":"9","event":"message.acked","created_at":5,"data":{"message_id":"m","device_id":"d","external_user_id":"u","acked_by":"bob","acked_by_name":"Bob ボブ"}}'
    d = Hansho::Webhook.verify(secret: "s", header: sign("s", T, full), body: full, now: T).data
    assert_equal ["bob", "Bob ボブ"], [d.acked_by, d.acked_by_name]
    bare = '{"id":"9","event":"message.acked","created_at":5,"data":{"message_id":"m","device_id":"d","external_user_id":"u"}}'
    d = Hansho::Webhook.verify(secret: "s", header: sign("s", T, bare), body: bare, now: T).data
    assert_nil d.acked_by
    assert_nil d.acked_by_name
  end

  def test_rejects_a_body_that_was_parsed_and_reserialised
    v = VECTORS["vectors"].find { |x| x["name"] == "valid message.acked" }
    parsed = JSON.parse(v["body"])
    [JSON.pretty_generate(parsed), "#{v["body"]}\n"].each do |body|
      assert_error("bad_signature") { Hansho::Webhook.verify(secret: v["secret"], header: v["header"], body: body, now: v["now"]) }
    end
  end

  def test_refuses_anything_but_a_string_body
    v = VECTORS["vectors"].find { |x| x["name"] == "valid message.acked" }
    [JSON.parse(v["body"]), nil, 42, {}, ["x"]].each do |body|
      assert_error("invalid_body") { Hansho::Webhook.verify(secret: v["secret"], header: v["header"], body: body, now: v["now"]) }
    end
  end

  def test_refuses_an_empty_or_missing_secret
    [nil, "", 5].each do |secret|
      assert_error("invalid_secret") { Hansho::Webhook.verify(secret: secret, header: sign("s", T, BODY), body: BODY, now: T) }
    end
  end

  def test_missing_header_is_malformed
    [nil, "", "   ", 5].each do |h|
      assert_error("malformed_header") { Hansho::Webhook.verify(secret: "s", header: h, body: BODY, now: T) }
    end
  end

  def test_signature_check_precedes_body_parsing
    body = "not json"
    assert_error("malformed_body") { Hansho::Webhook.verify(secret: "s", header: sign("s", T, body), body: body, now: T) }
    assert_error("bad_signature") { Hansho::Webhook.verify(secret: "s", header: sign("other", T, body), body: body, now: T) }
  end

  def test_malformed_bodies_after_a_valid_signature
    ["[]", "5", "null", '{"id":"1"}', '{"event":""}', '{"event":5}', "{\"event\":\"a\xff\"}", ""].each do |body|
      assert_error("malformed_body") { Hansho::Webhook.verify(secret: "s", header: sign("s", T, body), body: body, now: T) }
    end
  end

  def test_unknown_event_with_odd_payloads_is_not_an_exception
    ["null", "5", '"x"', "[1]"].each do |data|
      body = %({"id":"9","event":"future.thing","created_at":1,"data":#{data}})
      ev = Hansho::Webhook.verify(secret: "s", header: sign("s", T, body), body: body, now: T)
      assert_equal "unknown", ev.type
      assert_equal "future.thing", ev.name
      expected = JSON.parse(data)
      expected.nil? ? assert_nil(ev.data) : assert_equal(expected, ev.data)
    end
  end

  def test_known_event_with_missing_payload_fields_defaults
    body = '{"id":"9","event":"message.delivered","created_at":1}'
    ev = Hansho::Webhook.verify(secret: "s", header: sign("s", T, body), body: body, now: T)
    assert_equal Hansho::Webhook::MessageDelivered.new(message_id: "", external_user_id: "", channel: "", level: "", devices: 0), ev.data
  end

  def test_action_text_is_only_set_for_text_actions
    body = '{"id":"9","event":"action","created_at":1,"data":{"message_id":"m","action_id":"reply","device_id":"d","external_user_id":"u","text":"on it"}}'
    ev = Hansho::Webhook.verify(secret: "s", header: sign("s", T, body), body: body, now: T)
    assert_equal "on it", ev.data.text
    plain = '{"id":"9","event":"action","created_at":1,"data":{"message_id":"m","action_id":"ack"}}'
    assert_nil Hansho::Webhook.verify(secret: "s", header: sign("s", T, plain), body: plain, now: T).data.text
  end

  def test_several_v1_signatures_any_match_in_any_position
    good = OpenSSL::HMAC.hexdigest("SHA256", "s", "#{T}.#{BODY}")
    bad = "0" * 64
    [bad, good].permutation.each do |sigs|
      header = "t=#{T}," + sigs.map { |s| "v1=#{s}" }.join(",")
      assert_equal "message.acked", Hansho::Webhook.verify(secret: "s", header: header, body: BODY, now: T).type
    end
    assert_error("bad_signature") { Hansho::Webhook.verify(secret: "s", header: "t=#{T},v1=#{bad},v1=#{bad}", body: BODY, now: T) }
  end

  def test_exposes_the_header_name
    assert_equal "X-Hansho-Webhook-Signature", Hansho::Webhook::SIGNATURE_HEADER
  end

  def test_verifies_the_bytes_received_over_http
    v = VECTORS["vectors"].find { |x| x["name"] == "valid pairing.created" }
    seen = []
    server = MiniServer.new do |req|
      seen << Hansho::Webhook.verify(secret: v["secret"], header: req.headers[Hansho::Webhook::SIGNATURE_HEADER.downcase],
                                     body: req.body, now: v["now"])
      [200, {}, ""]
    end
    http = Net::HTTP.new("127.0.0.1", server.port)
    res = http.post("/hook", v["body"], Hansho::Webhook::SIGNATURE_HEADER => v["header"], "Content-Type" => "application/json")
    assert_equal "200", res.code
    assert_equal ["pairing.created"], seen.map(&:type)
    assert_empty server.errors
  ensure
    server&.stop
  end
end

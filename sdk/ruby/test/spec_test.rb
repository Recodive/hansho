# frozen_string_literal: true

require_relative "test_helper"

# Every operation and error of the shared wire contract, sdk/spec/api.json.
class SpecTest < Minitest::Test
  include HubHelper

  SPEC = load_spec_file("api.json")
  WAV = "UklGRgAAAABXQVZF".unpack1("m")

  SPEC_CHANNEL = {
    name: "Operations", default_level: "active", max_level: "critical", sound: "ding", template_id: "status", thread: "ops-thread",
    actions: [{ id: "ack", title: "Acknowledge", ack: true }, { id: "reply", title: "Reply", kind: "text" }]
  }.freeze

  # Every operation of the spec: the SDK call and the value it must produce.
  CALLS = {
    "createPairingTicket" => [
      ->(c) { c.create_pairing_ticket("user-42") },
      Hansho::PairingTicket.new(ticket: "tk_abc", qr: "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=tk_abc&fp=ff00",
                                expires_at: Time.utc(2026, 11, 8, 12, 10, 0))
    ],
    "unpair" => [->(c) { c.unpair("user-42") }, nil],
    "putChannel" => [->(c) { c.put_channel("ops", **SPEC_CHANNEL) }, nil],
    "getChannel" => [
      ->(c) { c.get_channel("ops") },
      Hansho::Channel.new(name: "Operations", default_level: "active", max_level: "critical", sound: "", template_id: "", thread: "ops", actions: [])
    ],
    "deleteChannel" => [->(c) { c.delete_channel("ops") }, nil],
    "putTemplate" => [
      ->(c) { c.put_template("status", 2, { type: "vstack", children: [{ type: "text", style: "headline", text: "{{title}}" }] }) },
      nil
    ],
    "putSound" => [
      ->(c) { c.put_sound("ding", "audio/wav", WAV) },
      Hansho::SoundResult.new(id: "ding", filename: "ding.wav", sha256: "00", size: 12)
    ],
    "sendMessage" => [
      lambda { |c|
        c.send_message(
          external_user_id: "user-42", channel: "ops", level: "time-sensitive", title: "Disk full", subtitle: "db-1",
          body: "db-1 is at 97%", image_url: "https://example.com/i.png", template_id: "status",
          data: { disk: 97, nested: { snake_case_key: [1, 2] } }, sound: "ding",
          actions: [{ id: "ack", title: "Ack", ack: true }], collapse_id: "disk-db-1"
        )
      },
      Hansho::MessageResult.new(
        message_id: "msg_1", level: "time-sensitive", devices: 2, status: "queued",
        deliveries: [Hansho::Delivery.new(device_id: "dev_1", status: "queued"), Hansho::Delivery.new(device_id: "dev_2", status: "queued")],
        recipients: []
      )
    ],
    "sendPage" => [
      lambda { |c|
        c.send_message(external_user_id: "user-42", channel: "ops", level: :page, title: "Primary DB down",
                       body: "Failover did not complete", page: { interval_seconds: 60, max_retries: 3 })
      },
      Hansho::MessageResult.new(message_id: "msg_2", level: "page", devices: 1, status: "queued",
                                deliveries: [Hansho::Delivery.new(device_id: "dev_1", status: "queued")], recipients: [])
    ],
    "getMessage" => [
      ->(c) { c.get_message("msg_2") },
      Hansho::MessageStatus.new(
        message_id: "msg_2", status: "acked", level: "page", attempts: 2,
        deliveries: [Hansho::Delivery.new(device_id: "dev_1", status: "sent"),
                     Hansho::Delivery.new(device_id: "dev_2", status: "failed", error: "unregistered")],
        acked_at: Time.utc(2026, 11, 8, 12, 5, 0)
      )
    ],
    "startActivity" => [
      lambda { |c|
        c.start_activity(external_user_id: "user-42", channel: "ops", template_id: "deploy", data: { progress: 0.1 },
                         alert: { title: "Deploy started", body: "v1.2.3" })
      },
      Hansho::ActivityStarted.new(activity_id: "act_1", devices: 1)
    ],
    "updateActivity" => [
      ->(c) { c.update_activity("act_1", data: { progress: 0.6 }, priority: 10, alert: { title: "Halfway", body: "60%" }) },
      Hansho::ActivityUpdated.new(activity_id: "act_1", update: 3)
    ],
    "endActivity" => [->(c) { c.end_activity("act_1") }, nil],
    "putFeed" => [
      ->(c) { c.put_feed("uptime", external_user_id: "user-42", template_id: "status", title: "Uptime", data: { up: true, ms: 41 }) },
      Hansho::FeedResult.new(feed_id: "uptime", devices: 2)
    ]
  }.freeze

  SPEC["operations"].each do |op|
    define_method("test_operation_#{op["name"]}") do
      call, want = CALLS.fetch(op["name"]) { flunk "no SDK call for spec operation #{op["name"]}" }
      answer = ->(_r) { reply(op["response"]["status"], op["response"]["body"].nil? ? "" : op["response"]["body"]) }
      with_hub(answer) do |hub|
        got = call.call(hub.client)
        if want.nil?
          assert_nil got
        else
          assert_equal want, got
        end
        assert_equal 1, hub.requests.length
        r = hub.requests.first
        assert_equal op["method"], r.method
        assert_equal op["path"], r.target
        assert_equal "Bearer #{TEST_KEY}", r.headers["authorization"]
        req = op["request"]
        if req.nil?
          assert_equal "", r.body
        elsif req["raw_base64"]
          assert_equal req["content_type"], r.headers["content-type"]
          assert_equal req["raw_base64"].unpack1("m"), r.body
        else
          assert_equal req["content_type"], r.headers["content-type"]
          assert_equal req["body"], JSON.parse(r.body)
        end
      end
    end
  end

  def test_covers_every_sdk_call
    names = SPEC["operations"].map { |o| o["name"] }
    assert_empty CALLS.keys - names, "SDK calls without an operation in sdk/spec/api.json"
    assert_empty names - CALLS.keys, "spec operations without an SDK call"
  end

  SPEC["errors"].each_with_index do |e, i|
    define_method("test_error_#{i}_status_#{e["status"]}") do
      answer = ->(_r) { reply(e["status"], e["raw"] || JSON.generate(e["body"]), e["headers"]) }
      with_hub(answer) do |hub|
        err = assert_raises(Hansho::APIError) do
          hub.client.send_message(external_user_id: "u", channel: "c", title: "t", body: "b")
        end
        assert_equal e["status"], err.status
        assert_equal e["code"], err.code
        assert_equal e["field"], err.field.to_s
        assert_equal e["retry_after_ms"], err.retry_after_ms.to_i
        assert_equal 1, hub.requests.length, "POST /messages must not be retried"
      end
    end
  end
end

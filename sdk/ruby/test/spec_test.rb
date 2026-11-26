# frozen_string_literal: true

require_relative "test_helper"

# Every operation and error of the shared wire contract, "x-conformance" in sdk/spec/openapi.json.
class SpecTest < Minitest::Test
  include HubHelper

  SPEC = load_spec_file("openapi.json").fetch("x-conformance")
  WAV = "UklGRgAAAABXQVZF".unpack1("m")

  SPEC_CHANNEL = {
    name: "Operations", default_level: "active", max_level: "critical", sound: "ding", template_id: "status", thread: "ops-thread",
    actions: [{ id: "ack", title: "Acknowledge", ack: true }, { id: "reply", title: "Reply", kind: "text" },
              { id: "runbook", title: "Runbook", kind: "url", url: "https://example.com/runbook" }]
  }.freeze

  SPEC_AUDIENCE = { mode: :only, users: ["alice"], groups: ["oncall"] }.freeze
  # What an audience reads back as.
  SPEC_AUDIENCE_READ = Hansho::Audience.new(mode: "only", users: ["alice"], groups: ["oncall"]).freeze
  SPEC_CHANNEL_READ = Hansho::Channel.new(id: "ops", name: "Operations", default_level: "active", max_level: "critical", sound: "",
                                          template_id: "", thread: "ops", actions: [],
                                          audience: Hansho::Audience.new(mode: "everyone", users: [], groups: []))
  SPEC_TEMPLATE = Hansho::Template.new(id: "status", version: 2, body: { "type" => "text", "text" => "{{title}}" })
  SPEC_SOUND = Hansho::Sound.new(id: "ding", content_type: "audio/wav", bytes: 12, sha256: "00")
  SPEC_GROUP = Hansho::Group.new(id: "oncall", members: %w[alice bob])

  # Every operation of the spec: the SDK call and the value it must produce.
  CALLS = {
    "createPairingTicket" => [
      ->(c) { c.create_pairing_ticket("user-42") },
      Hansho::PairingTicket.new(ticket: "tk_abc", qr: "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=tk_abc&fp=ff00",
                                expires_at: Time.utc(2026, 11, 8, 12, 10, 0))
    ],
    "createPairingTicketWithName" => [
      ->(c) { c.create_pairing_ticket("user-42", display_name: "Zoë 日本語") },
      Hansho::PairingTicket.new(ticket: "tk_def", qr: "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=tk_def&fp=ff00",
                                expires_at: Time.utc(2026, 11, 8, 12, 10, 0))
    ],
    "setUserDisplayName" => [->(c) { c.set_user_display_name("user-42", "Zoë 日本語") }, nil],
    "clearUserDisplayName" => [->(c) { c.set_user_display_name("user 42", "") }, nil],
    "unpair" => [->(c) { c.unpair("user-42") }, nil],
    "putChannel" => [->(c) { c.put_channel("ops", **SPEC_CHANNEL) }, nil],
    "getChannel" => [
      ->(c) { c.get_channel("ops") },
      Hansho::Channel.new(id: "ops", name: "Operations", default_level: "active", max_level: "critical", sound: "", template_id: "",
                          thread: "ops", actions: [])
    ],
    "listChannels" => [->(c) { c.list_channels }, [SPEC_CHANNEL_READ]],
    "deleteChannel" => [->(c) { c.delete_channel("ops") }, nil],
    "putTemplate" => [
      ->(c) { c.put_template("status", 2, { type: "vstack", children: [{ type: "text", style: "headline", text: "{{title}}" }] }) },
      nil
    ],
    "listTemplates" => [->(c) { c.list_templates }, [SPEC_TEMPLATE]],
    "getTemplate" => [->(c) { c.get_template("status") }, SPEC_TEMPLATE],
    "deleteTemplate" => [->(c) { c.delete_template("status") }, nil],
    "listSounds" => [->(c) { c.list_sounds }, [SPEC_SOUND]],
    "getSound" => [->(c) { c.get_sound("ding") }, SPEC_SOUND],
    "deleteSound" => [->(c) { c.delete_sound("ding") }, nil],
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
    "sendPageEach" => [
      lambda { |c|
        c.send_message(external_user_ids: %w[alice bob], channel: "ops", level: :page, title: "Primary DB down",
                       body: "Failover did not complete", page: { interval_seconds: 60, max_retries: 3, ack: "each" })
      },
      Hansho::MessageResult.new(
        message_id: nil, level: "page", devices: nil, status: nil, deliveries: [],
        recipients: [
          Hansho::Recipient.new(external_user_id: "alice", message_id: "msg_3", status: "queued", devices: 1,
                                deliveries: [Hansho::Delivery.new(device_id: "dev_1", status: "queued")]),
          Hansho::Recipient.new(external_user_id: "bob", message_id: "msg_4", status: "queued", devices: 1,
                                deliveries: [Hansho::Delivery.new(device_id: "dev_2", status: "queued")])
        ]
      )
    ],
    "getMessageAckedNoName" => [
      ->(c) { c.get_message("msg_3") },
      Hansho::MessageStatus.new(message_id: "msg_3", status: "acked", level: "page", attempts: 1,
                                deliveries: [Hansho::Delivery.new(device_id: "dev_1", status: "sent")],
                                acked_at: Time.utc(2026, 11, 8, 12, 5, 0), acked_by: "alice")
    ],
    "getMessage" => [
      ->(c) { c.get_message("msg_2") },
      Hansho::MessageStatus.new(
        message_id: "msg_2", status: "acked", level: "page", attempts: 2,
        deliveries: [Hansho::Delivery.new(device_id: "dev_1", status: "sent"),
                     Hansho::Delivery.new(device_id: "dev_2", status: "failed", error: "unregistered")],
        acked_at: Time.utc(2026, 11, 8, 12, 5, 0), acked_by: "user-42", acked_by_name: "Zoë 日本語"
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
    ],
    "putGroup" => [->(c) { c.put_group("oncall", %w[alice bob]) }, Hansho::Group.new(id: "oncall", members: %w[alice bob])],
    "getGroup" => [->(c) { c.get_group("oncall") }, SPEC_GROUP],
    "listGroups" => [->(c) { c.list_groups }, [SPEC_GROUP]],
    "getFeed" => [
      ->(c) { c.get_feed("uptime", external_user_id: "user-42") },
      Hansho::FeedSettings.new(feed_id: "uptime", external_user_id: "user-42", template_id: "status", title: "Uptime", channel: "ops",
                               audience: Hansho::Audience.new(mode: "only", users: ["alice"], groups: []), updated_at: 1_762_603_200)
    ],
    "deleteGroup" => [->(c) { c.delete_group("oncall") }, nil],
    "kick" => [->(c) { c.kick("user-42") }, nil],
    "putChannelWithAudience" => [
      ->(c) { c.put_channel("ops", name: "Operations", default_level: "active", max_level: "critical", audience: SPEC_AUDIENCE) },
      nil
    ],
    "putFeedWithChannel" => [
      ->(c) { c.put_feed("uptime", template_id: "status", channel: "ops", audience: SPEC_AUDIENCE, data: { up: true }) },
      Hansho::FeedResult.new(feed_id: "uptime", devices: 1)
    ],
    "sendToAudience" => [
      ->(c) { c.send_message(to_audience: true, channel: "ops", title: "Deploy done", body: "v1.2.3 is live") },
      Hansho::MessageResult.new(
        message_id: nil, level: "active", devices: nil, status: nil, deliveries: [],
        recipients: [
          Hansho::Recipient.new(external_user_id: "alice", message_id: "msg_7", status: "queued", devices: 1,
                                deliveries: [Hansho::Delivery.new(device_id: "dev_1", status: "queued")]),
          Hansho::Recipient.new(external_user_id: "carol", message_id: nil, status: Hansho::RECIPIENT_NO_ACCESS, devices: 0, deliveries: [])
        ]
      )
    ]
  }.freeze

  # The error codes the SDK exports, by the code the hub sends.
  ERROR_CODES = { "unknown_group" => Hansho::APIError::UNKNOWN_GROUP, "no_access" => Hansho::APIError::NO_ACCESS }.freeze

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
    assert_empty CALLS.keys - names, "SDK calls without an x-conformance case in sdk/spec/openapi.json"
    assert_empty names - CALLS.keys, "spec operations without an SDK call"
  end

  # Every operationId of the spec has a method on the client, named in snake_case.
  def test_client_has_a_method_for_every_spec_operation
    missing = Hansho::SPEC_OPERATIONS.reject { |id| Hansho::Client.method_defined?(id.gsub(/([A-Z])/) { "_#{$1.downcase}" }) }
    assert_empty missing, "spec operations without a client method"
  end

  def test_covers_every_operation_of_the_spec
    assert_empty Hansho::SPEC_OPERATIONS - SPEC["operations"].map { |o| o["operation"] }, "operations without a conformance case"
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
        assert_equal ERROR_CODES.fetch(e["code"]), err.code if ERROR_CODES.key?(e["code"])
        assert_equal e["field"], err.field.to_s
        assert_equal e["retry_after_ms"], err.retry_after_ms.to_i
        assert_equal 1, hub.requests.length, "POST /messages must not be retried"
      end
    end
  end
end

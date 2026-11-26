# frozen_string_literal: true

require_relative "test_helper"

class AudienceTest < Minitest::Test
  include HubHelper

  CHANNEL = { name: "Pager", default_level: "page", max_level: "page" }.freeze

  def body_of(request)
    JSON.parse(request.body)
  end

  def answer(status, body)
    ->(_r) { reply(status, body) }
  end

  # --- groups ---------------------------------------------------------------

  def test_put_get_and_delete_a_group
    with_hub(answer(200, '{"id":"oncall","members":["alice","bob"]}')) do |h|
      g = h.client.put_group("oncall", %w[alice bob])
      assert_equal Hansho::Group.new(id: "oncall", members: %w[alice bob]), g
      assert_equal ["PUT", "/api/v1/groups/oncall"], [h.requests[0].method, h.requests[0].target]
      assert_equal({ "members" => %w[alice bob] }, body_of(h.requests[0]))
      assert_equal %w[alice bob], h.client.get_group("oncall").members
      assert_equal ["GET", "/api/v1/groups/oncall"], [h.requests[1].method, h.requests[1].target]
    end
    with_hub(answer(204, "")) do |h|
      assert_nil h.client.delete_group("oncall")
      assert_equal ["DELETE", "/api/v1/groups/oncall"], [h.requests[0].method, h.requests[0].target]
    end
  end

  def test_an_empty_member_list_is_sent
    with_hub(answer(200, '{"id":"g","members":[]}')) do |h|
      assert_equal [], h.client.put_group("g", []).members
      assert_equal({ "members" => [] }, body_of(h.requests[0]))
    end
  end

  def test_group_arguments_are_checked_before_sending
    with_hub do |h|
      assert_raises(ArgumentError) { h.client.put_group("", []) }
      assert_raises(ArgumentError) { h.client.put_group("g", "alice") }
      assert_raises(ArgumentError) { h.client.get_group("..") }
      h.client.get_group("a/b")
      assert_equal "/api/v1/groups/a%2Fb", h.requests[0].target
      assert_equal 1, h.requests.length
    end
  end

  def test_unknown_group
    [->(c) { c.get_group("x") }, ->(c) { c.delete_group("x") }].each do |call|
      with_hub(answer(404, '{"error":"unknown_group"}')) do |h|
        e = assert_raises(Hansho::APIError) { call.call(h.client) }
        assert_equal [404, Hansho::APIError::UNKNOWN_GROUP], [e.status, e.code]
        assert_equal 1, h.requests.length
      end
    end
  end

  # --- audiences ------------------------------------------------------------

  def test_channel_audience_round_trip
    with_hub do |h|
      h.client.put_channel("pager", **CHANNEL, audience: { mode: :only, users: ["alice"], groups: ["oncall"] })
      assert_equal({ "mode" => "only", "users" => ["alice"], "groups" => ["oncall"] }, body_of(h.requests[0])["audience"])
    end
    answer_body = '{"name":"Pager","default_level":"page","max_level":"page","audience":{"mode":"only","groups":["oncall"]}}'
    with_hub(answer(200, answer_body)) do |h|
      assert_equal Hansho::Audience.new(mode: "only", users: [], groups: ["oncall"]), h.client.get_channel("pager").audience
    end
  end

  def test_an_absent_audience_is_left_out_so_the_hub_keeps_the_stored_one
    with_hub do |h|
      h.client.put_channel("pager", **CHANNEL)
      refute body_of(h.requests[0]).key?("audience")
      h.client.put_feed("f", template_id: "t", data: 1)
      refute body_of(h.requests[1]).key?("audience")
      refute body_of(h.requests[1]).key?("channel")
    end
  end

  def test_everyone_is_sent_explicitly_and_string_keys_work
    with_hub do |h|
      h.client.put_channel("pager", **CHANNEL, audience: { mode: :everyone })
      assert_equal({ "mode" => "everyone" }, body_of(h.requests[0])["audience"])
      h.client.put_channel("pager", **CHANNEL, audience: { "mode" => "except", "users" => ["mallory"], "groups" => [] })
      assert_equal({ "mode" => "except", "users" => ["mallory"] }, body_of(h.requests[1])["audience"])
    end
  end

  def test_malformed_audiences_are_refused_before_sending
    with_hub do |h|
      assert_raises(ArgumentError) { h.client.put_channel("pager", **CHANNEL, audience: "everyone") }
      assert_raises(ArgumentError) { h.client.put_channel("pager", **CHANNEL, audience: { mode: :only, users: "alice" }) }
      assert_empty h.requests
    end
  end

  def test_feed_channel_and_audience
    with_hub do |h|
      h.client.put_feed("queue", template_id: "status", channel: "pager", data: { value: 3 })
      assert_equal({ "template_id" => "status", "channel" => "pager", "data" => { "value" => 3 } }, body_of(h.requests[0]))
      h.client.put_feed("queue", template_id: "status", data: 1, audience: { mode: :except, users: ["mallory"] })
      assert_equal({ "mode" => "except", "users" => ["mallory"] }, body_of(h.requests[1])["audience"])
    end
  end

  def test_get_feed
    body = '{"feed_id":"queue","external_user_id":"alice","template_id":"status","title":"Queue","channel":"pager",' \
           '"audience":{"mode":"only","users":["alice"]},"updated_at":1760000000}'
    with_hub(answer(200, body)) do |h|
      f = h.client.get_feed("queue", external_user_id: "alice")
      assert_equal Hansho::FeedSettings.new(feed_id: "queue", external_user_id: "alice", template_id: "status", title: "Queue",
                                            channel: "pager", audience: Hansho::Audience.new(mode: "only", users: ["alice"], groups: []),
                                            updated_at: 1_760_000_000), f
      assert_equal ["GET", "/api/v1/feeds/queue?external_user_id=alice"], [h.requests[0].method, h.requests[0].target]
    end
    with_hub(answer(200, '{"feed_id":"all","template_id":"t","updated_at":5}')) do |h|
      f = h.client.get_feed("all")
      assert_nil f.audience
      assert_nil f.channel
      assert_equal "/api/v1/feeds/all", h.requests[0].target
    end
  end

  def test_get_feed_escapes_the_user
    with_hub do |h|
      h.client.get_feed("f", external_user_id: "a&b=c d")
      assert_equal "/api/v1/feeds/f?external_user_id=a%26b%3Dc+d", h.requests[0].target
    end
  end

  def test_an_unknown_group_in_an_audience_is_a_400
    body = '{"error":"invalid_request","field":"audience.groups","message":"unknown group"}'
    with_hub(answer(400, body)) do |h|
      e = assert_raises(Hansho::APIError) { h.client.put_channel("pager", **CHANNEL, audience: { mode: :only, groups: ["typo"] }) }
      assert_equal [400, "invalid_request", "audience.groups", "unknown group"], [e.status, e.code, e.field, e.detail]
      assert_equal 1, h.requests.length
    end
  end

  def test_no_access_error_code
    with_hub(answer(403, '{"error":"no_access"}')) do |h|
      e = assert_raises(Hansho::APIError) do
        h.client.start_activity(external_user_id: "u", channel: "c", template_id: "t", data: {})
      end
      assert_equal [403, Hansho::APIError::NO_ACCESS], [e.status, e.code]
    end
  end

  # --- sending --------------------------------------------------------------

  def test_targets_on_the_wire
    with_hub do |h|
      h.client.send_message(external_user_ids: %w[alice bob], channel: "c", title: "t", body: "b")
      assert_equal({ "external_user_ids" => %w[alice bob], "channel" => "c", "title" => "t", "body" => "b" }, body_of(h.requests[0]))
      h.client.send_message(to_audience: true, channel: "c", title: "t", body: "b")
      assert_equal({ "to_audience" => true, "channel" => "c", "title" => "t", "body" => "b" }, body_of(h.requests[1]))
      assert_raises(ArgumentError) { h.client.send_message(external_user_ids: "alice", channel: "c", title: "t", body: "b") }
    end
  end

  def test_a_multi_target_result_has_recipients_and_no_top_level_message
    body = '{"level":"active","recipients":[' \
           '{"external_user_id":"alice","message_id":"msg_1","status":"queued","devices":1,"deliveries":[{"device_id":"d1","status":"queued"}]},' \
           '{"external_user_id":"carol","status":"no_access"}]}'
    with_hub(answer(202, body)) do |h|
      res = h.client.send_message(external_user_ids: %w[alice carol], channel: "c", title: "t", body: "b")
      assert_nil res.message_id
      assert_equal [], res.deliveries
      alice, carol = res.recipients
      assert_equal ["alice", "msg_1", "queued", 1], [alice.external_user_id, alice.message_id, alice.status, alice.devices]
      assert_equal "d1", alice.deliveries[0].device_id
      assert_equal ["carol", nil, Hansho::RECIPIENT_NO_ACCESS, 0, []], [carol.external_user_id, carol.message_id, carol.status, carol.devices, carol.deliveries]
    end
  end

  def test_a_single_user_without_access_keeps_the_top_level_fields
    body = '{"message_id":"","level":"active","devices":0,"status":"no_access","deliveries":[],' \
           '"recipients":[{"external_user_id":"carol","status":"no_access"}]}'
    with_hub(answer(202, body)) do |h|
      res = h.client.send_message(external_user_id: "carol", channel: "c", title: "t", body: "b")
      assert_equal ["", 0, "no_access"], [res.message_id, res.devices, res.status]
      assert_equal "no_access", res.recipients[0].status
    end
  end

  def test_an_answer_without_recipients_still_parses
    with_hub(answer(202, '{"message_id":"m1","level":"active","devices":1,"status":"queued","deliveries":[]}')) do |h|
      res = h.client.send_message(external_user_id: "u", channel: "c", title: "t", body: "b")
      assert_equal ["m1", []], [res.message_id, res.recipients]
    end
  end

  # --- kick -----------------------------------------------------------------

  def test_kick_is_unpair
    with_hub do |h|
      assert_nil h.client.kick("user 1")
      assert_equal ["DELETE", "/api/v1/users/user%201/pairings"], [h.requests[0].method, h.requests[0].target]
      assert_raises(ArgumentError) { h.client.kick("") }
    end
  end
end

# frozen_string_literal: true

require_relative "test_helper"
require "open3"
require "tmpdir"

# End to end against the real hub binary. Run it through sdk/scripts/run-integration.sh, which starts
# the hub and a stub relay and exports these variables; skipped when HANSHO_HUB_URL is not set:
#
#   HANSHO_HUB_URL, HANSHO_API_KEY            the hub and a sender API key
#   HANSHO_WEBHOOK_SECRET, HANSHO_WEBHOOK_ADDR  where the hub posts its signed webhooks (host:port), and their secret
#   HANSHO_FAKEDEVICE_BIN                      stand-in for the app: pairs, acknowledges pages
#   HANSHO_WEBHOOK_BIND                        optional host:port to listen on when it differs from the address
#                                              the hub dials (e.g. in a container)
class IntegrationTest < Minitest::Test
  WAIT_SECONDS = 25

  # Receives the hub's webhooks and verifies each one with this SDK's own verifier.
  class Receiver
    attr_reader :events, :rejected

    def initialize(addr, secret)
      @events = []
      @rejected = 0
      @mutex = Mutex.new
      host, port = addr.split(":", 2)
      verify = lambda do |req|
        event = Hansho::Webhook.verify(secret: secret, header: req.headers[Hansho::Webhook::SIGNATURE_HEADER.downcase], body: req.body)
        @mutex.synchronize { @events << event }
        [200, {}, ""]
      rescue Hansho::WebhookError
        @mutex.synchronize { @rejected += 1 }
        [400, {}, ""]
      end
      @server = bind(host, port.to_i, verify)
    end

    def stop
      @server.stop
    end

    def wait_for(type)
      deadline = Process.clock_gettime(Process::CLOCK_MONOTONIC) + WAIT_SECONDS
      while Process.clock_gettime(Process::CLOCK_MONOTONIC) < deadline
        hit = @mutex.synchronize { @events.find { |e| e.type == type && yield(e.data) } }
        return hit if hit

        sleep 0.1
      end
      raise "no #{type} webhook arrived"
    end

    private

    # The previous suite's receiver may still be closing.
    def bind(host, port, handler)
      tries = 0
      begin
        MiniServer.new(host: host, port: port, &handler)
      rescue Errno::EADDRINUSE
        tries += 1
        raise if tries > 50

        sleep 0.1
        retry
      end
    end
  end

  def setup
    skip "HANSHO_HUB_URL is not set; run sdk/scripts/run-integration.sh" unless ENV["HANSHO_HUB_URL"]
  end

  def fake_device(*args)
    out, err, status = Open3.capture3(ENV.fetch("HANSHO_FAKEDEVICE_BIN"), *args)
    raise "fakedevice #{args.first} failed: #{err}#{out}" unless status.success?

    out
  end

  def tiny_wav
    data = "\x00".b * 800 # 0.05 s of 8 kHz 8-bit mono silence
    header = "RIFF".b + [36 + data.bytesize].pack("V") + "WAVEfmt ".b + [16, 1, 1, 8000, 8000, 1, 8].pack("VvvVVvv") +
             "data".b + [data.bytesize].pack("V")
    header + data
  end

  def wait_status(client, id, *want)
    deadline = Process.clock_gettime(Process::CLOCK_MONOTONIC) + WAIT_SECONDS
    last = ""
    while Process.clock_gettime(Process::CLOCK_MONOTONIC) < deadline
      ms = client.get_message(id)
      last = ms.status
      return ms if want.include?(ms.status)

      sleep 0.2
    end
    raise "message #{id}: status #{last}, wanted one of #{want.join(", ")}"
  end

  def assert_api_error(status, code, field = nil)
    e = assert_raises(Hansho::APIError) { yield }
    assert_equal status, e.status
    assert_equal code, e.code
    assert_equal field, e.field unless field.nil?
    e
  end

  def test_runs_the_whole_flow
    hub_url = ENV.fetch("HANSHO_HUB_URL")
    rx = Receiver.new(ENV["HANSHO_WEBHOOK_BIND"] || ENV.fetch("HANSHO_WEBHOOK_ADDR"), ENV.fetch("HANSHO_WEBHOOK_SECRET"))
    begin
      c = Hansho::Client.new(base_url: hub_url, api_key: ENV.fetch("HANSHO_API_KEY"))
      p = "ruby"
      user = "u-#{p} ü/42?x"
      ch = "#{p}-ops"
      tpl = "#{p}-status"
      snd = "#{p}-ding"
      state = File.join(Dir.mktmpdir("hansho-ruby-it-"), "device.json")

      # errors
      assert_api_error(401, "unauthorized") { Hansho::Client.new(base_url: hub_url, api_key: "ck_not_a_key").create_pairing_ticket("x") }
      assert_api_error(404, "unknown_channel") { c.get_channel("no-such-channel") }
      assert_api_error(400, "invalid_request", "title") { c.send_message(external_user_id: user, channel: ch, title: "", body: "b") }

      # catalog
      sound = c.put_sound(snd, "audio/wav", tiny_wav)
      assert_equal snd, sound.id
      template = { type: "vstack", tint: { light: "#0A84FF", dark: "#64D2FF" },
                   children: [{ type: "text", style: "headline", text: "{{title}}" }, { type: "spacer" }] }
      c.put_template(tpl, 1, template)
      assert_api_error(409, "version_conflict") { c.put_template(tpl, 1, template) }
      c.put_template(tpl, 2, template)
      c.put_channel(ch, name: "Operations #{p}", default_level: :active, max_level: :page, sound: snd, template_id: tpl, thread: "ops",
                        actions: [{ id: "ack", title: "Acknowledge", ack: true }])
      got = c.get_channel(ch)
      assert_equal "Operations #{p}", got.name
      assert_equal "page", got.max_level
      assert_equal snd, got.sound
      assert_equal tpl, got.template_id
      assert_equal [{ id: "ack", title: "Acknowledge", ack: true }], got.actions
      assert_api_error(400, "invalid_request", "id") { c.put_channel("bad id", name: "n", default_level: :active, max_level: :page) }

      # pairing
      tk = c.create_pairing_ticket(user)
      assert tk.qr.start_with?("hansho://pair?") && tk.qr.include?(tk.ticket)
      assert_operator tk.expires_at, :>, Time.now
      fake_device("pair", "-hub", hub_url, "-ticket", tk.ticket, "-state", state, "-name", "SDK phone #{p}")
      rx.wait_for("pairing.created") { |d| d.external_user_id == user && d.device_name == "SDK phone #{p}" }

      # messages at every level
      %w[passive active time-sensitive critical].each do |level|
        res = c.send_message(external_user_id: user, channel: ch, level: level, title: "Disk full", body: "db-1 is at 97%",
                             data: { disk: 97 }, sound: snd, collapse_id: "disk-#{level}")
        assert_equal level, res.level
        assert_equal 1, res.devices
        assert res.message_id.start_with?("msg_")
        ms = wait_status(c, res.message_id, "delivered")
        assert_equal level, ms.level
        assert_equal 1, ms.deliveries.length
        assert_equal "sent", ms.deliveries[0].status
        rx.wait_for("message.delivered") { |d| d.message_id == res.message_id && d.level == level && d.devices == 1 }
      end
      default = c.send_message(external_user_id: user, channel: ch, title: "Default", body: "b")
      assert_equal "active", default.level

      # page + acknowledgement from the (fake) device
      page = c.send_message(external_user_id: user, channel: ch, level: :page, title: "Primary DB down", body: "Failover failed",
                            page: { interval_seconds: 60, max_retries: 3 })
      assert_equal "page", page.level
      wait_status(c, page.message_id, "paging")
      fake_device("ack", "-state", state, "-message", page.message_id)
      acked = wait_status(c, page.message_id, "acked")
      assert_kind_of Time, acked.acked_at
      ev = rx.wait_for("message.acked") { |d| d.message_id == page.message_id }
      assert_equal user, ev.data.external_user_id
      rx.wait_for("action") { |d| d.message_id == page.message_id && d.action_id == "ack" }

      # live activity
      started = c.start_activity(external_user_id: user, channel: ch, template_id: tpl, data: { title: "Deploy", progress: 0.1 },
                                 alert: { title: "Deploy started" })
      assert_equal 1, started.devices
      assert started.activity_id.start_with?("act_")
      fake_device("activity-token", "-state", state, "-activity", started.activity_id)
      up = c.update_activity(started.activity_id, data: { title: "Deploy", progress: 0.6 }, priority: 10)
      assert_equal started.activity_id, up.activity_id
      assert_operator up.update, :>=, 1
      c.end_activity(started.activity_id)
      assert_api_error(409, "activity_ended") { c.update_activity(started.activity_id, data: { title: "x" }) }

      # feeds
      own = c.put_feed("#{p}-uptime", external_user_id: user, template_id: tpl, data: { title: "up", ms: 41 })
      assert_equal "#{p}-uptime", own.feed_id
      assert_operator own.devices, :>=, 1
      shared = c.put_feed("#{p}-shared", template_id: tpl, data: { title: "all" })
      assert_operator shared.devices, :>=, 1
      assert_api_error(400, "invalid_request", "template_id") { c.put_feed("#{p}-bad", template_id: "missing", data: 1) }

      # unpair, then nobody is left to deliver to
      c.unpair(user)
      rx.wait_for("pairing.revoked") { |d| d.external_user_id == user && d.by == "sender" }
      none = c.send_message(external_user_id: user, channel: ch, title: "Anyone?", body: "b")
      assert_equal 0, none.devices
      wait_status(c, none.message_id, "undeliverable")
      rx.wait_for("message.undeliverable") { |d| d.message_id == none.message_id }
      c.delete_channel(ch)
      assert_api_error(404, "unknown_channel") { c.get_channel(ch) }

      assert_equal 0, rx.rejected, "the SDK rejected a webhook the hub signed"
      refute_empty rx.events
    ensure
      rx.stop
    end
  end
end

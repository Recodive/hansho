# frozen_string_literal: true

require "json"
require "minitest/autorun"
require "timeout"
require_relative "../lib/hansho"
require_relative "support/mini_server"

TEST_KEY = "ck_test_SECRET_do_not_leak"
SPEC_DIR = File.expand_path("../../spec", __dir__)

def load_spec_file(name)
  JSON.parse(File.read(File.join(SPEC_DIR, name), encoding: "UTF-8"))
end

# A fake hub plus a client that records its retry waits instead of sleeping.
class HubFixture
  attr_reader :server, :waits

  def initialize(&handler)
    @waits = []
    @server = MiniServer.new(&handler)
  end

  def requests
    @server.requests
  end

  def url
    @server.url
  end

  def client(**opts)
    waits = @waits
    Hansho::Client.new(base_url: url, api_key: TEST_KEY, sleeper: ->(s) { waits << s }, **opts)
  end

  def stop
    @server.stop
  end
end

module HubHelper
  # Runs the block with a fake hub; the optional `handler` lambda answers like MiniServer's block. Fails the
  # test if the handler raised.
  def with_hub(handler = nil)
    hub = HubFixture.new(&handler)
    yield hub
    assert_empty hub.server.errors, "the fake hub's handler raised"
  ensure
    hub&.stop
  end

  def reply(status, body = "{}", headers = {})
    [status, headers, body.is_a?(String) ? body : JSON.generate(body)]
  end
end

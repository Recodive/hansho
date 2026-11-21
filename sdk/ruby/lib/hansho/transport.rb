# frozen_string_literal: true

require "net/http"
require "uri"

module Hansho
  # What a transport returns. #headers has lower-case names.
  Response = Struct.new(:status, :headers, :body, keyword_init: true)

  # The default transport, on Net::HTTP. A transport is any object that answers
  # `call(method:, url:, target:, headers:, body:, timeout:)` with a Response and
  # raises on connection failure; pass your own to Client.new(transport: ...) for
  # proxies or tests. +url+ is the base URI (scheme, host, port), +target+ the request path.
  class NetHTTPTransport
    def call(method:, url:, target:, headers:, body:, timeout:)
      http = Net::HTTP.new(url.hostname, url.port)
      http.use_ssl = url.scheme == "https"
      # Net::HTTP would silently repeat idempotent requests after a reset or timeout; retries are the client's call.
      http.max_retries = 0
      limit = timeout&.positive? ? timeout : nil
      http.open_timeout = limit
      http.read_timeout = limit
      http.write_timeout = limit
      req = Net::HTTPGenericRequest.new(method, !body.nil?, true, target, headers)
      req.body = body unless body.nil?
      res = http.start { |h| h.request(req) }
      out = {}
      res.each_header { |k, v| out[k.downcase] = v }
      Response.new(status: res.code.to_i, headers: out, body: res.body.to_s.dup.force_encoding(Encoding::UTF_8))
    end
  end
end

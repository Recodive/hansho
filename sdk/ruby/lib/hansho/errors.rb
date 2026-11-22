# frozen_string_literal: true

module Hansho
  # Base class of everything this gem raises on purpose (argument mistakes raise ArgumentError).
  class Error < StandardError; end

  # A non-2xx answer from the hub. The API key never appears in it.
  class APIError < Error
    # 403: the user is outside the channel's audience (+start_activity+ for such a user).
    NO_ACCESS = "no_access"
    # 404: the group does not exist (+get_group+, +delete_group+).
    UNKNOWN_GROUP = "unknown_group"

    # HTTP status code.
    attr_reader :status
    # The hub's error code, e.g. "invalid_request"; "" when the body was not JSON.
    attr_reader :code
    # The offending request field for validation failures, else nil.
    attr_reader :field
    # Parsed Retry-After in milliseconds, else nil.
    attr_reader :retry_after_ms
    # The hub's validation message, when present.
    attr_reader :detail

    def initialize(method:, path:, status:, code: nil, field: nil, retry_after_ms: nil, detail: nil)
      text = "hansho: #{method} #{path}: #{status}"
      text += " #{code}" if code && !code.empty?
      text += " (field #{field})" if field && !field.empty?
      text += " (retry after #{retry_after_ms} ms)" if retry_after_ms
      super(text)
      @status = status
      @code = code || ""
      @field = field && !field.empty? ? field : nil
      @retry_after_ms = retry_after_ms
      @detail = detail
    end

    # Retry-After in seconds, else nil.
    def retry_after
      retry_after_ms && retry_after_ms / 1000.0
    end
  end

  # No HTTP answer arrived: connection failure, timeout or an unreadable response.
  # The original exception is in #cause.
  class NetworkError < Error
    def initialize(method, path, cause)
      message = cause.respond_to?(:message) ? cause.message : cause.to_s
      super("hansho: #{method} #{path}: #{message}")
    end
  end

  # Raised by Webhook.verify; check #code.
  class WebhookError < Error
    CODES = %w[malformed_header bad_signature stale malformed_body invalid_body invalid_secret].freeze

    # One of CODES.
    attr_reader :code

    def initialize(code, message)
      super(message)
      @code = code
    end
  end
end

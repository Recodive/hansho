# frozen_string_literal: true

require "time"

module Hansho
  # Retry policy shared by the client: only 429 and 503, at most 3 times.
  module Retry
    DEFAULT_MAX_RETRIES = 3
    BASE_BACKOFF_MS = 250
    MAX_BACKOFF_MS = 8000
    # A longer Retry-After is raised to the caller instead of waited out.
    MAX_WAIT_MS = 60_000

    module_function

    # Only GET, PUT and DELETE may be repeated without side effects.
    def idempotent?(method)
      %w[GET PUT DELETE].include?(method)
    end

    def retryable_status?(status)
      status == 429 || status == 503
    end

    # Exponential backoff with jitter in [d/2, d], in milliseconds.
    def backoff_ms(attempt, random: Kernel)
      d = [BASE_BACKOFF_MS * (2**[attempt, 10].min), MAX_BACKOFF_MS].min
      (d / 2.0) + (random.rand * (d / 2.0))
    end

    # Reads delay-seconds or an HTTP date; returns nil when absent or unusable.
    # The result is in milliseconds.
    def parse_retry_after(value, now: Time.now)
      return nil if value.nil?

      v = value.strip
      return nil if v.empty?
      return (Float(v) * 1000).round if v.match?(/\A\d+(\.\d+)?\z/)

      at = parse_date(v)
      return nil unless at

      [((at - now) * 1000).round, 0].max
    end

    def parse_date(text)
      Time.httpdate(text)
    rescue ArgumentError
      begin
        Time.rfc2822(text)
      rescue ArgumentError
        nil
      end
    end
  end
end

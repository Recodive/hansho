# frozen_string_literal: true

require_relative "hansho/version"
require_relative "hansho/errors"
require_relative "hansho/retry"
require_relative "hansho/types"
require_relative "hansho/transport"
require_relative "hansho/client"
require_relative "hansho/webhook"

# Official Ruby client for a Hansho hub's sender API: pair users, send notifications,
# pages, Live Activities and widget feeds, and verify the hub's webhooks.
module Hansho
  # Shorthand for Webhook.verify.
  def self.verify_webhook(**opts)
    Webhook.verify(**opts)
  end
end

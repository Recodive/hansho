# frozen_string_literal: true

require_relative "lib/hansho/version"

Gem::Specification.new do |spec|
  spec.name = "hansho"
  spec.version = Hansho::VERSION
  spec.authors = ["Recodive"]

  spec.summary = "Official Ruby client for a Hansho hub: pair users, send notifications, pages, Live Activities and widget feeds, verify webhooks."
  spec.description = "A small, dependency-free client (net/http, json, openssl) for the Hansho hub's sender API: " \
                     "pairing tickets, channels, templates, sounds, notifications, pages, Live Activities, widget feeds, " \
                     "typed errors, safe retries and signed-webhook verification."
  spec.homepage = "https://github.com/Recodive/hansho/tree/main/sdk/ruby#readme"
  spec.license = "MIT"
  spec.required_ruby_version = ">= 3.1"

  spec.metadata = {
    "homepage_uri" => spec.homepage,
    "source_code_uri" => "https://github.com/Recodive/hansho/tree/main/sdk/ruby",
    "bug_tracker_uri" => "https://github.com/Recodive/hansho/issues",
    "rubygems_mfa_required" => "true"
  }

  spec.files = Dir["lib/**/*.rb"] + %w[README.md LICENSE]
  spec.require_paths = ["lib"]
end

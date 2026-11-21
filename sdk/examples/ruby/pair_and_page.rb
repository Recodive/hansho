# frozen_string_literal: true

# Pair a user with the Hansho app and send them a page.
#
#   HANSHO_HUB_URL=https://hub.example.com HANSHO_API_KEY=ck_... bundle exec ruby pair_and_page.rb user-42
require "hansho"

hansho = Hansho::Client.new(base_url: ENV.fetch("HANSHO_HUB_URL"), api_key: ENV.fetch("HANSHO_API_KEY"))
user = ARGV.fetch(0) { abort "usage: pair_and_page.rb USER_ID" }

hansho.put_channel("oncall", name: "On call", default_level: :active, max_level: :page)
ticket = hansho.create_pairing_ticket(user)
puts "Render this as a QR code and scan it in the Hansho app:\n#{ticket.qr}"
print "Press Enter once the app has paired... "
$stdin.gets

res = hansho.send_message(
  external_user_id: user,
  channel: "oncall",
  level: :page,
  title: "Primary DB down",
  body: "Failover did not complete"
)
puts "sent #{res.message_id} to #{res.devices} device(s); it repeats until acknowledged"

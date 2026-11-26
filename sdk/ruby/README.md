# hansho (Ruby)

Ruby client for a Hansho hub. Ruby 3.1 or newer, no runtime dependencies (`net/http`,
`json`, `openssl` from the standard library).

```sh
gem install hansho        # or: gem "hansho" in your Gemfile
```

See [../README.md](https://github.com/Recodive/hansho/blob/main/sdk/README.md) for the model shared with the Go and Node SDKs.

## Quick start

```ruby
require "hansho"

hansho = Hansho::Client.new(base_url: "https://hub.example.com", api_key: ENV.fetch("HANSHO_API_KEY"))

hansho.put_channel("ops", name: "Operations", default_level: :active, max_level: :critical)

res = hansho.send_message(
  external_user_id: "user-42",
  channel: "ops",
  title: "Disk full",
  body: "db-1 is at 97%"
)
puts "#{res.message_id} #{res.status}" # msg_... queued
```

Options: `max_retries:` (default 3, 0 disables), `timeout:` (seconds per attempt, default 30,
0 or nil disables), `user_agent:`, `transport:` (any object answering
`call(method:, url:, target:, headers:, body:, timeout:)` with a `Hansho::Response`, for proxies
and tests) and `sleeper:` (replaces the retry wait; meant for tests).

Arguments are keywords in `snake_case`; results are `Struct`s with the same names
(`res.message_id`, `ticket.expires_at` as a `Time`). `level` is a String or Symbol
(`:passive`, `:active`, `:time_sensitive` or `"time-sensitive"`, `:critical`, `:page`; the values are in
`Hansho::Level`); a level or audience mode that is not one of them raises `ArgumentError` before anything is sent, while
an answer with a value this SDK does not know keeps it as a String. Nested hashes (`actions`, `page`, `alert`) take Symbol or String keys.

## Pairing

```ruby
ticket = hansho.create_pairing_ticket("user-42")
# Render ticket.qr as a QR code; the user scans it in the Hansho app.
# ticket.expires_at is 10 minutes out; the ticket works once.
# The hub then sends a pairing.created webhook.

hansho.unpair("user-42") # revokes every pairing of the user
hansho.kick("user-42")   # the same call; the app tells the person that you ended it

# Optionally give the user a display name with the ticket:
hansho.create_pairing_ticket("user-42", display_name: "Ada Lovelace")
```

## Users

```ruby
# What other people see for a user instead of their external id (acknowledged-by, ...): any language,
# trimmed, at most 40 characters, no control characters. The user need not be paired yet.
hansho.set_user_display_name("user-42", "Ada Lovelace")
hansho.set_user_display_name("user-42", "") # clears it
```

## Audiences and groups

A channel or feed is open to everyone unless it has an `audience:`: `mode: :only` the listed users and groups, or
`mode: :except` them. Users are your external user ids (they may not have paired yet); groups are named lists you keep
on the hub, and every group an audience names must exist (a channel or feed naming a missing one is refused with a
400, `e.field == "audience.groups"`). The hub decides who has access and checks it on every request, so a change
applies at once.

```ruby
hansho.put_group("oncall", %w[alice bob]) # replaces the whole member list; get_group, delete_group
hansho.put_channel("pager", name: "Pager", default_level: :page, max_level: :page,
                            audience: { mode: :only, groups: ["oncall"] })
# Leaving audience: out keeps the one stored on the hub, so a republish never widens access.
# Send audience: { mode: :everyone } to open a channel up again.

# A feed uses its channel's audience unless it sets its own. Leaving both out keeps the stored ones.
hansho.put_feed("queue", template_id: "status", channel: "pager", data: { value: 3 })
settings = hansho.get_feed("queue") # the settings without the data; pass external_user_id: for a per-user feed

# Send to several users, or to everyone in the channel's audience. People outside it are not delivered to.
res = hansho.send_message(to_audience: true, channel: "pager", title: "DB down", body: "...")
res.recipients.each do |r|
  next unless r.status == Hansho::RECIPIENT_NO_ACCESS # only with external_user_id(s): not in the audience, no message
end
```

`send_message` takes exactly one of `external_user_id:`, `external_user_ids:` (at most 1000) and `to_audience: true`.
The answer has one `Recipient` per user in `res.recipients` (`message_id` is `nil` for `"no_access"`). For
`external_user_id:` the top-level fields describe that one recipient (`message_id` is `""`, `status` is
`"no_access"` and `devices` is 0 without access); for several users or an audience the top-level `message_id`,
`devices` and `status` are `nil`, `deliveries` is empty and each message is under `recipients`. `audience` comes
back from `get_channel` and `get_feed` as a `Hansho::Audience` (`mode`, `users`, `groups`; `audience[:mode]` works too),
and a channel's `actions` as `Hansho::Action`s. What you pass in stays a Hash (Symbol or String keys) or the type itself.

Error codes: `Hansho::APIError::NO_ACCESS` (403, `start_activity` for a user outside the channel's audience) and
`Hansho::APIError::UNKNOWN_GROUP` (404), compared against `e.code`.

## Sending

```ruby
# A notification. The level defaults to the channel's default and is capped at its max.
hansho.send_message(
  external_user_id: "user-42", channel: "ops", level: :time_sensitive,
  title: "Deploy failed", body: "web-3 rolled back",
  actions: [{ id: "retry", title: "Retry" }],
  collapse_id: "deploy-web" # a later message with the same id replaces this one
)

# A page repeats every interval_seconds until someone acknowledges it on any
# device, then the hub sends message.acked. Defaults: 120 s, 5 resends.
page = hansho.send_message(
  external_user_id: "user-42", channel: "ops", level: :page,
  title: "Primary DB down", body: "Failover did not complete",
  page: { interval_seconds: 60, max_retries: 10 }
)
status = hansho.get_message(page.message_id) # status.status: queued, delivered, undeliverable, paging, acked, expired
status.acked_by      # who acknowledged: their external user id (nil until acked)
status.acked_by_name # their display name, only when they have one (the message.acked webhook carries both too)

# A page to several people stops for everyone at the first acknowledgement ("any", the default).
# With ack: "each" every person keeps being paged until they acknowledge for themselves.
hansho.send_message(external_user_ids: %w[alice bob], channel: "ops", level: :page, title: "DB down", body: "...",
                    page: { interval_seconds: 60, ack: "each" })

# A templated message; data is sent exactly as given (its keys are never renamed).
hansho.put_template("status", 1, { type: "vstack", children: [{ type: "text", style: "headline", text: "{{title}}" }] })
hansho.send_message(external_user_id: "user-42", channel: "ops", template_id: "status", title: "Uptime", body: "99.98%", data: { title: "Uptime" })

# A Live Activity.
act = hansho.start_activity(external_user_id: "user-42", channel: "ops", template_id: "deploy", data: { progress: 0.1 })
hansho.update_activity(act.activity_id, data: { progress: 0.6 }, priority: 10)
hansho.end_activity(act.activity_id)

# A widget feed; omit external_user_id for one shared by all paired users. The title is the name the app's feed
# picker shows (it lists the feeds published for that person plus the shared ones).
hansho.put_feed("uptime", external_user_id: "user-42", template_id: "status", title: "Uptime", data: { title: "up" })

# A sound (audio/x-caf, audio/wav or audio/aiff, at most 1 MB).
hansho.put_sound("ding", "audio/wav", File.binread("ding.wav"))

# What the hub holds: list_channels, list_templates, list_sounds and list_groups return Arrays of Channel, Template,
# Sound and Group; get_template and get_sound return one; delete_template and delete_sound remove one.
hansho.list_channels.map(&:id)
hansho.get_template("status").version
hansho.get_sound("ding").bytes
hansho.list_groups.map(&:members)
```

Ids may contain any characters except `.` and `..`: they are path-escaped. Empty, non-String and
dot ids raise `ArgumentError` before anything is sent.

## Webhooks

Set `HUB_WEBHOOK_URL` and `HUB_WEBHOOK_SECRET` on the hub. Verify the **raw** request body: a body a
framework parsed and you re-serialised will not match its signature, and `Hansho::Webhook.verify`
refuses anything that is not a String (`invalid_body`).

Rack / Sinatra (`request.body.read` is the raw body; the header is `HTTP_X_HANSHO_WEBHOOK_SIGNATURE`):

```ruby
post "/hansho-hook" do
  event = Hansho::Webhook.verify(
    secret: ENV.fetch("HANSHO_WEBHOOK_SECRET"),
    header: request.env["HTTP_X_HANSHO_WEBHOOK_SIGNATURE"],
    body: request.body.read
  )
  case event.type
  when "message.acked" then puts "acknowledged: #{event.data.message_id}"
  when "action"        then puts "button #{event.data.action_id} on #{event.data.message_id}"
  when "unknown"       then puts "newer hub event: #{event.name}"
  end
  status 200
rescue Hansho::WebhookError
  status 400 # non-2xx: the hub retries
end
```

Rails: `request.raw_post` is the raw body, `request.headers["X-Hansho-Webhook-Signature"]` the header; skip
CSRF for the route.

`verify` accepts several `v1=` signatures (secret rotation), compares in constant time and rejects
timestamps more than 300 s from now (`tolerance:` in seconds changes it; `now:`, a `Time` or epoch
seconds, is injectable). `WebhookError#code` is one of `malformed_header`, `bad_signature`, `stale` (a valid
signature, replayed too late), `malformed_body`, `invalid_body`, `invalid_secret`. The returned
`Hansho::Webhook::Event` has `type`, `id`, `created_at` (a `Time`), `data` and the verified `raw` body.
Events: `pairing.created`, `pairing.revoked`, `message.delivered`, `message.undeliverable`,
`message.acked`, `message.dismissed`, `message.expired`, `action`, `activity.ended`, each with a `Struct` as
`data` (`snake_case` fields; a payload field the hub left out is `""` or `0` when the event always carries it, else `nil`); anything else is `type: "unknown"` with the event name in `name` and the
parsed payload in `data`.

## Errors

```ruby
begin
  hansho.send_message(**msg)
rescue Hansho::APIError => e
  # e.status, e.code ("unknown_channel", "invalid_request", ...), e.field ("title"), e.retry_after (seconds) / e.retry_after_ms
rescue Hansho::NetworkError => e
  # no answer at all: connection, timeout, unreadable response; see e.cause
end
```

Both inherit from `Hansho::Error`. API keys, ticket values and message bodies are never put into error
messages, and the client's `inspect` does not show the key.

## Retries

`GET`, `PUT` and `DELETE` are retried on 429 and 503, up to `max_retries` times, waiting for
`Retry-After` (a wait over 60 s is raised to you as the error) or an exponential backoff with jitter.
`send_message` is never retried unless you pass an `idempotency_key:`; the hub does not deduplicate on it
yet, so a retried send is at-least-once. Activity updates (`PATCH`) are not retried. Connection errors and
timeouts are not retried.

## Development

```sh
bundle install
bundle exec rake test
```

The types in `lib/hansho/types_gen.rb` are generated from `../spec/openapi.json` (`cd ../scripts/gen && go run . -lang ruby`);
the unit tests load its `x-conformance` cases and `../spec/webhook-vectors.json`. `test/integration_test.rb` runs
against a real hub and is skipped unless `HANSHO_HUB_URL` is set; `../scripts/run-integration.sh` sets that up.

## Licence

MIT, see [LICENSE](LICENSE). The hub itself is AGPL-3.0.

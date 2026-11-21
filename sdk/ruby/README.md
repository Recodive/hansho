# hansho (Ruby)

Ruby client for a Hansho hub. Ruby 3.1 or newer, no runtime dependencies (`net/http`,
`json`, `openssl` from the standard library).

```sh
gem install hansho        # or: gem "hansho" in your Gemfile
```

See [../README.md](../README.md) for the model shared with the Go and Node SDKs.

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
(`:passive`, `:active`, `:time_sensitive` or `"time-sensitive"`, `:critical`, `:page`); a level the hub
does not know comes back as its 400. Nested hashes (`actions`, `page`, `alert`) take Symbol or String keys.

## Pairing

```ruby
ticket = hansho.create_pairing_ticket("user-42")
# Render ticket.qr as a QR code; the user scans it in the Hansho app.
# ticket.expires_at is 10 minutes out; the ticket works once.
# The hub then sends a pairing.created webhook.

hansho.unpair("user-42") # revokes every pairing of the user
```

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
`data` (`snake_case` fields); anything else is `type: "unknown"` with the event name in `name` and the
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

The unit tests load `../spec/api.json` and `../spec/webhook-vectors.json`. `test/integration_test.rb` runs
against a real hub and is skipped unless `HANSHO_HUB_URL` is set; `../scripts/run-integration.sh` sets that up.

## Licence

MIT, see [LICENSE](LICENSE). The hub itself is AGPL-3.0.

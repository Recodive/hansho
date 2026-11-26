# hansho

Python client for a Hansho hub. Python 3.9 or newer, no runtime dependencies
(standard library only: `urllib.request`, `json`, `hmac`, `hashlib`). Synchronous,
fully type-hinted (ships `py.typed`).

```sh
pip install hansho
```

See [../README.md](https://github.com/Recodive/hansho/blob/main/sdk/README.md) for the model shared with the Go and Node SDKs.

## Quick start

```python
import os
from hansho import Channel, Hansho, Message

hansho = Hansho("https://hub.example.com", os.environ["HANSHO_API_KEY"])

hansho.put_channel("ops", Channel(name="Operations", default_level="active", max_level="critical"))

res = hansho.send_message(
    Message(external_user_id="user-42", channel="ops", title="Disk full", body="db-1 is at 97%")
)
print(res.message_id, res.status)  # msg_... queued
```

Keyword options: `max_retries` (default 3, 0 disables), `timeout` (seconds per
attempt, default 30, `None` or 0 disables), `user_agent`, `transport` (replace
the HTTP round trip, for proxies or tests) and `sleep` (replace the retry wait).
The client keeps no connection, so there is nothing to close and it is safe to share
between threads. The API key never appears in `repr(client)`, errors or exceptions.

`level` is one of `"passive"`, `"active"`, `"time-sensitive"`, `"critical"` and
`"page"` (the `Level` literal type, so a type checker flags `"pager"`). Requests and
results are dataclasses whose field names match the hub's JSON, except
`PageOptions.interval_seconds` (`interval_s` on the wire). Fields left as `None` are
not sent, and fields the SDK does not know about in a response are ignored.
Timestamps are timezone-aware `datetime` objects (UTC).

## Pairing

```python
ticket = hansho.create_pairing_ticket("user-42")
# Render ticket.qr as a QR code; the user scans it in the Hansho app.
# ticket.expires_at is 10 minutes out; the ticket works once.
# The hub then sends a pairing.created webhook.

hansho.unpair("user-42")  # revokes every pairing of the user
hansho.kick("user-42")    # the same call; the app tells the person that you ended it
```

## Users

```python
# What other people see for the user instead of their id (any language, max 40 characters).
hansho.set_user_display_name("user-42", "Ada Lovelace")
hansho.set_user_display_name("user-42", "")  # clears it
# Or give it with the pairing ticket:
hansho.create_pairing_ticket("user-42", display_name="Ada Lovelace")
```

## Audiences and groups

A channel or feed is open to everyone unless it has an `Audience`: `"only"` the listed users and groups, or
`"except"` them. Users are your external user ids (they may not have paired yet); groups are named lists you keep
on the hub, and every group an audience names must exist (a channel or feed naming a missing one is refused with a
400, `e.field == "audience.groups"`). The hub decides who has access and checks it on every request, so a change
applies at once.

```python
from hansho import RECIPIENT_NO_ACCESS, Audience, Channel, Feed, Message

hansho.put_group("oncall", ["alice", "bob"])  # replaces the whole member list; get_group, delete_group
hansho.put_channel("pager", Channel(
    name="Pager", default_level="page", max_level="page",
    audience=Audience("only", groups=["oncall"]),
))
# An absent audience (None) keeps the one stored on the hub, so a republish never widens access.
# Send Audience("everyone") to open a channel up again.

# A feed uses its channel's audience unless it sets its own. Leaving both out keeps the stored ones.
hansho.put_feed("queue", Feed(template_id="status", channel="pager", data={"value": 3}))
settings = hansho.get_feed("queue")  # the settings without the data; pass external_user_id for a per-user feed

# Send to several users, or to everyone in the channel's audience. People outside it are not delivered to.
res = hansho.send_message(Message(to_audience=True, channel="pager", title="DB down", body="..."))
for r in res.recipients:
    if r.status == RECIPIENT_NO_ACCESS:
        pass  # only with external_user_id(s): not in the audience, no message was created
```

`Message` takes exactly one of `external_user_id`, `external_user_ids` (at most 1000) and `to_audience`. The
answer has one `Recipient` per user in `res.recipients` (`message_id` is `None` for `"no_access"`). For
`external_user_id` the top-level fields describe that one recipient (`message_id == ""`, `status == "no_access"`
and `devices == 0` without access); for several users or an audience `message_id`, `devices` and `deliveries` are
empty and each message is under `recipients`.

Error codes: `CODE_NO_ACCESS` (403, `start_activity` for a user outside the channel's audience) and
`CODE_UNKNOWN_GROUP` (404), compared against `HanshoError.code`.

## Sending

```python
from hansho import Action, Activity, ActivityUpdate, Feed, Message, PageOptions

# A notification. The level defaults to the channel's default and is capped at its max.
hansho.send_message(Message(
    external_user_id="user-42", channel="ops", level="time-sensitive",
    title="Deploy failed", body="web-3 rolled back",
    actions=[Action(id="retry", title="Retry")],
    collapse_id="deploy-web",  # a later message with the same id replaces this one
))

# A page repeats every interval_seconds until someone acknowledges it on any
# device, then the hub sends message.acked. Defaults: 120 s, 5 resends.
page = hansho.send_message(Message(
    external_user_id="user-42", channel="ops", level="page",
    title="Primary DB down", body="Failover did not complete",
    page=PageOptions(interval_seconds=60, max_retries=10),
))
status = hansho.get_message(page.message_id)  # queued, delivered, undeliverable, paging, acked, expired
# status.acked_by is who acknowledged (external user id), status.acked_by_name their display name if they have one.

# A page to several people stops for everyone at the first acknowledgement (ack="any", the default).
# ack="each" keeps paging each person until they acknowledge for themselves.
Message(external_user_ids=["alice", "bob"], channel="ops", level="page", title="DB down", body="...",
        page=PageOptions(ack="each"))

# A templated message; data is sent exactly as given (its keys are never renamed).
hansho.put_template("status", 1, {"type": "vstack", "children": [{"type": "text", "style": "headline", "text": "{{title}}"}]})
hansho.send_message(Message(
    external_user_id="user-42", channel="ops", template_id="status",
    title="Uptime", body="99.98%", data={"title": "Uptime"},
))

# A Live Activity.
act = hansho.start_activity(Activity(external_user_id="user-42", channel="ops", template_id="deploy", data={"progress": 0.1}))
hansho.update_activity(act.activity_id, ActivityUpdate(data={"progress": 0.6}, priority=10))
hansho.end_activity(act.activity_id)

# A widget feed; omit external_user_id for one shared by all paired users. The title is the name the app's
# feed picker shows (it lists the feeds published for that person plus the shared ones).
hansho.put_feed("uptime", Feed(template_id="status", data={"title": "up"}, external_user_id="user-42", title="Uptime"))

# A sound (audio/x-caf, audio/wav or audio/aiff, at most 1 MB).
hansho.put_sound("ding", "audio/wav", wav_bytes)
```

`data` on a `Message` is left out when it is `None`. Ids may contain any characters
except the empty string, `.` and `..` (a `ValueError`): they are path-escaped.

## Webhooks

Set `HUB_WEBHOOK_URL` and `HUB_WEBHOOK_SECRET` on the hub. Verify the **raw**
request body: a body a framework parsed and you re-serialised will not match its
signature, and `verify_webhook` refuses a parsed object (`invalid_body`).

Plain `http.server`:

```python
import os
from http.server import BaseHTTPRequestHandler, HTTPServer
from hansho import WEBHOOK_SIGNATURE_HEADER, WebhookError, verify_webhook

class Hook(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        try:
            event = verify_webhook(
                secret=os.environ["HANSHO_WEBHOOK_SECRET"],
                header=self.headers.get(WEBHOOK_SIGNATURE_HEADER),
                body=body,  # bytes, exactly as received
            )
        except WebhookError:
            self.send_response(400)  # non-2xx: the hub retries
        else:
            if event.type == "message.acked":
                print("acknowledged:", event.data.message_id, event.data.acked_by, event.data.acked_by_name)
            elif event.type == "action":
                print("button", event.data.action_id, "on", event.data.message_id)
            elif event.type == "unknown":
                print("newer hub event:", event.name)
            self.send_response(200)
        self.send_header("Content-Length", "0")
        self.end_headers()

HTTPServer(("", 8080), Hook).serve_forever()
```

Flask: `verify_webhook(secret=..., header=request.headers.get(WEBHOOK_SIGNATURE_HEADER), body=request.get_data())`.
Django: `request.body`. FastAPI: `await request.body()`. All of them give the raw bytes.

`verify_webhook` accepts several `v1=` signatures (secret rotation), compares in
constant time and rejects timestamps more than 300 s from now (`tolerance`, in
seconds, changes it; `now` is injectable as a datetime or epoch seconds).
`WebhookError.code` is one of `malformed_header`, `bad_signature`, `stale` (a valid
signature, replayed too late), `malformed_body`, `invalid_body`, `invalid_secret`.

`Event` has `type`, `id`, `created_at`, `raw` (the verified body) and `data`. Events:
`pairing.created`, `pairing.revoked`, `message.delivered`, `message.undeliverable`,
`message.acked`, `message.dismissed`, `message.expired`, `action`, `activity.ended`; `data`
is the matching dataclass (`MessageAcked`, `ActionTaken`, ...) with snake_case fields.
Anything else is `type == "unknown"` with the event name in `name` and the parsed
payload in `data`; it never raises.

## Errors

```python
from hansho import HanshoError, HanshoNetworkError

try:
    hansho.send_message(msg)
except HanshoError as e:
    # e.status, e.code ("unknown_channel", "invalid_request", ...), e.field ("title"),
    # e.retry_after (seconds, from Retry-After), e.detail (the hub's message)
    ...
except HanshoNetworkError as e:
    # no answer at all: connection, timeout or an unreadable answer; see e.__cause__
    ...
```

The two are unrelated classes: an API answer is never a network error. Messages
carry the method, path, status and code only, never the key, a ticket or a
message body. Redirects are not followed, so the key cannot be handed to another
host.

## Retries

`GET`, `PUT` and `DELETE` are retried on 429 and 503, up to `max_retries` times,
waiting for `Retry-After` (a wait over 60 s is raised to you as the error) or an
exponential backoff with jitter. `send_message` is never retried unless the
message has an `idempotency_key`; it is sent as the `Idempotency-Key` header, and the
hub does not deduplicate on it yet, so a retried send is at-least-once. Activity
updates (`PATCH`) are not retried. There is no cancellation: bound a call with
`timeout`.

## Tests

```sh
uv run --python 3.9 -m unittest     # unit tests: the shared spec and webhook vectors, retries, errors
../scripts/run-integration.sh                        # end to end against the real hub
```

The unit tests load `../spec/api.json` and `../spec/webhook-vectors.json` and need
no packages. `tests/test_integration.py` is skipped unless `HANSHO_HUB_URL` is set;
`sdk/scripts/run-integration.sh` starts the hub and sets it.

## Licence

MIT, see [LICENSE](LICENSE) (the same licence as the other SDKs; the hub itself is AGPL-3.0).

Not published on PyPI yet.

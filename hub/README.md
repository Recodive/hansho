# Hansho hub

The self-hostable service your backend talks to. It pairs your users with the
Hansho app, stores your channels, templates and sounds, and delivers
notifications, pages, Live Activities and widget feeds end-to-end encrypted
through the relay. License: AGPL-3.0.

## Quickstart (10 minutes)

Releases are published as `ghcr.io/recodive/hansho-hub:<version>` (also `<major>.<minor>` and `latest`); to build
it yourself, `docker build -t hansho-hub hub/`.

You need a public HTTPS domain pointing at the hub: Hansho's relay checks that
you control it before it forwards anything.

```sh
export HUB_MASTER_KEY=$(openssl rand -base64 32)   # keep this: it wraps stored pairing keys
export HUB_PUBLIC_URL=https://hub.example.com

docker volume create hub-data
alias hub='docker run --rm -v hub-data:/data -e HUB_MASTER_KEY -e HUB_PUBLIC_URL -e HUB_DISPLAY_NAME="Acme Ops" ghcr.io/recodive/hansho-hub:0.2'
```

**1. Register with the relay.** This creates the hub signing key in the volume
and prints the two values to set next.

```sh
hub relay register
# HUB_ID=hub_...
# HUB_CHALLENGE_TOKEN=...
```

**2. Run the hub** with those two values, then verify the domain.

```sh
docker run -d --name hub -p 8090:8090 -v hub-data:/data \
  -e HUB_MASTER_KEY -e HUB_PUBLIC_URL -e HUB_DISPLAY_NAME="Acme Ops" \
  -e HUB_ID=hub_... -e HUB_CHALLENGE_TOKEN=... \
  -e HUB_WEBHOOK_URL=https://backend.example.com/hansho-hook -e HUB_WEBHOOK_SECRET=change-me \
  ghcr.io/recodive/hansho-hub:0.2
# put TLS in front (reverse proxy) so https://hub.example.com reaches port 8090
curl https://hub.example.com/.well-known/hansho-challenge   # prints the token
docker exec hub /hub relay verify                             # verified
```

**3. Create an API key** (printed once; only its hash is stored).

```sh
docker exec hub /hub apikey create backend
export KEY=ck_...
```

**4. Create a channel.**

```sh
curl -X PUT https://hub.example.com/api/v1/channels/ops \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"name":"Operations","default_level":"active","max_level":"critical"}'
```

**5. Pair a user.** Render the returned `qr` string as a QR code and scan it in
the app. The ticket is single use and valid for 10 minutes.

```sh
curl -X POST https://hub.example.com/api/v1/pairing-tickets \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"external_user_id":"user-42"}'
# {"ticket":"...","qr":"hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=...&fp=...","expires_at":"..."}
```

**6. Send a message.**

```sh
curl -X POST https://hub.example.com/api/v1/messages \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"external_user_id":"user-42","channel":"ops","title":"Disk full","body":"db-1 is at 97%"}'
# 202 {"message_id":"msg_...","level":"active","devices":1,"status":"queued","deliveries":[...]}

curl -H "Authorization: Bearer $KEY" https://hub.example.com/api/v1/messages/msg_...
```

## Configuration

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `HUB_PUBLIC_URL` | yes | | Public `https://` base URL; its host is what the relay verifies and what the app shows |
| `HUB_DISPLAY_NAME` | yes | | Sender name shown by the app |
| `HUB_RELAY_URL` | | `https://relay.hansho.app` | The relay Hansho runs. Override it only to test against another relay |
| `HUB_MASTER_KEY` | yes | | 32 random bytes, base64 (`openssl rand -base64 32`); wraps pairing keys at rest. Losing it makes stored pairings unusable |
| `HUB_ADDR` | | `:8090` | Listen address |
| `HUB_DB_PATH` | | `./hub.db` (`/data/hub.db` in Docker) | SQLite database |
| `HUB_KEY_PATH` | | `./hub.key` (`/data/hub.key` in Docker) | Ed25519 hub key (PEM PKCS#8, created with mode 0600) |
| `HUB_ID` | yes (`hub serve` refuses to start without it) | | `hub_id` from `hub relay register`; run that command before the first start |
| `HUB_CHALLENGE_TOKEN` | until verified | | Served at `/.well-known/hansho-challenge` |
| `HUB_WEBHOOK_URL` | | | Where events are POSTed; empty disables webhooks |
| `HUB_WEBHOOK_SECRET` | | | HMAC key for webhook signatures |
| `HUB_LA_ENCRYPT` | | `true` | Encrypt Live Activity content-state as `{"blob":...}` with a per-pairing key derived from the pairing key; `false` sends plaintext `{"t","d"}`. Verified on a device with the app force-quit; push-to-start attributes always carry `pairing_id` so the app can pick the key |
| `HUB_HOME_URL` | | `https://hansho.app` | The project site the status page at `GET /` links to (https only). `GET /` shows what the hub is (its name, domain and public key fingerprint) so that someone who opens the address in a browser is not left with a bare 404 |

## Commands

| Command | |
|---|---|
| `hub serve` | HTTP API, delivery worker (1 s tick), webhook loop (1 s tick); graceful shutdown on SIGTERM |
| `hub apikey create <name>` | Print a new sender API key once |
| `hub relay register` | Register with the relay for the host of `HUB_PUBLIC_URL` |
| `hub relay verify` | Ask the relay to check the domain challenge |

## Sender API (`Authorization: Bearer <api key>`)

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/pairing-tickets` | `{external_user_id}` -> `{ticket, qr, expires_at}` |
| `DELETE /api/v1/users/{external_user_id}/pairings` | Unpair a user everywhere. Done by the sender it is a *kick*: the user's devices are told so (see [Audiences](#audiences-groups-and-kick)) |
| `PUT/GET/DELETE /api/v1/channels/{id}` | `{name, default_level, max_level, sound?, template_id?, thread?, actions?, audience?}`; a PUT replaces the whole channel, so leaving `audience` out makes it `everyone` again |
| `PUT/GET/DELETE /api/v1/groups/{id}` | `{members:[external_user_id,...]}`; a PUT replaces the whole member list |
| `PUT /api/v1/templates/{id}` | `{version, body}`; version must increase |
| `PUT /api/v1/sounds/{id}` | Raw `audio/x-caf`, `audio/wav` or `audio/aiff`, at most 1 MB; WAV/AIFF shorter than 30 s |
| `POST /api/v1/messages` | `{external_user_id \| external_user_ids \| to_audience, channel, level?, title, subtitle?, body, image_url?, template_id?, data?, sound?, actions?, collapse_id?, page?:{interval_s?, max_retries?}}` -> 202 with a `recipients` list (see [Audiences](#audiences-groups-and-kick)) |
| `GET /api/v1/messages/{id}` | `{status, level, attempts, deliveries, acked_at?}` |
| `POST /api/v1/activities`, `PATCH`/`DELETE /api/v1/activities/{id}` | Live Activities (content-state at most 4 KB; update priority 5 or 10) |
| `PUT/GET /api/v1/feeds/{id}` | `{external_user_id?, template_id?, title?, channel?, audience?, data}` (GET takes `?external_user_id=` and returns the settings without the data); `channel` and `audience` are explained under [Audiences](#audiences-groups-and-kick) and, like `title`, an upsert without them clears them; omit the user for a feed shared by all paired users; `title` (at most 64 bytes) is the name the app's feed picker shows instead of the id, and an upsert without one clears it. Without `template_id` the app draws the data with an automatic layout chosen from its shape (a number with `min`/`max` -> gauge, a `value` -> big number, `items[]` with `state` -> status grid, other `items[]` -> list); a named template must exist |

### Audiences, groups and kick

Every channel and feed has an **audience**: who may use it.

    {"mode": "everyone" | "only" | "except", "users": ["external_user_id", ...], "groups": ["group_id", ...]}

`everyone` (the default when `audience` is left out) lets every paired user in; `only` lets in just the listed users and
the members of the listed groups; `except` lets in everyone but them. `users` and `groups` must be empty for `everyone`.
Users may not have paired yet and are not looked up when you save. Every group must exist: a `PUT` of a channel or
feed naming a missing group is refused with `400` (`audience.groups`, `unknown group`), so create the group first.
Deleting a group that audiences still name is allowed; it then behaves as an empty group. Groups are managed with `PUT /api/v1/groups/{id}` (`{"members": [...]}`, replacing the whole list),
`GET` and `DELETE`; the group id follows the same pattern as other ids, `GET` answers `404 unknown_group` for a missing one.

A feed uses the audience of the channel named in its `channel` field unless it sets its own `audience`; with neither it is
for everyone. Both are stored per feed, so republish them with every data update.

Access is checked by the hub on every request and read fresh each time, so a change to an audience or a group applies at
once, and it is the only place it is decided (the app and the relay never see audiences):

- `GET /device/v1/sync` leaves out channels the user cannot access, and `GET /device/v1/feeds` and `GET /device/v1/acks`
  leave out the feeds and acknowledged messages of channels they cannot access. Nothing marks them as locked.
- Every other device call about such a channel or feed (a feed fetch, an action or acknowledgement on one of its messages,
  a Live Activity token) answers `403 {"error":"no_access"}`. That is not `401 pairing_revoked`: the pairing is fine.
- A queued message, a page being resent and a Live Activity update are not delivered to someone who lost access in the
  meantime. Messages already in the inbox stay there. A Live Activity they lost access to still gets its end event, without
  newer data.
- `POST /api/v1/activities` for a user without access answers `403 no_access`.

**Send.** Give exactly one target: `external_user_id`, `external_user_ids` (at most 1000) or `to_audience: true` (every
paired user in the channel's audience, resolved when you send). The response lists one entry per recipient:

    {"level":"active","recipients":[
      {"external_user_id":"alice","message_id":"msg_...","status":"queued","devices":1,"deliveries":[...]},
      {"external_user_id":"carol","status":"no_access"}]}

A user outside the audience gets `status: "no_access"`, no message and no push; the others are delivered. A recipient
with a message has the usual message status (`queued`, or `undeliverable` when no device can be reached). The
`external_user_id` form also keeps the original top-level `message_id`, `devices`, `status` and `deliveries` of that single
recipient; with no access they are `status: "no_access"`, `devices: 0` and `message_id: ""`. `to_audience` only lists users
in the audience. A send is never refused for `no_access` as a whole.

**Kick.** `DELETE /api/v1/users/{external_user_id}/pairings` ends every pairing of the user, relay included. The user's
devices then get `401 {"error":"pairing_revoked","reason":"kicked"}`; when the person unpaired from the app themselves
(or the relay revoked it) `reason` is absent. There is no block list: the user can pair again with a new ticket. A kick
differs from leaving an audience, which only hides one channel or feed.

Levels, lowest to highest: `passive`, `active`, `time-sensitive`, `critical`,
`page`. The delivered level is `min(requested ?? channel default, channel max)`.

Message `status`: `queued`, `delivered`, `undeliverable` (no reachable device),
`paging`, `acked` (a page, or a message with an ack action, was acknowledged), `expired`. A `page` is re-sent every `interval_s` (default
120) until it is acknowledged on any device; `attempts` counts those resends.
After `max_retries` (default 5) resends it gets one more interval, then
expires. Encrypted alert payloads above 4096 bytes are rejected with
`413 payload_too_large`.

### Template reference

A template `body` is a tree of `{"type": ...}` nodes (depth 8, 64 nodes). Root-only
properties: `schema` (optional; 1 is the only version, anything else is rejected with `must be 1`), `tint` and `background`
(`{"light":"#RRGGBB","dark":"#RRGGBB"}`). Unknown properties are rejected with
`{"field":"body.children[1].colour","message":"unknown property"}`.

| Component | Properties |
|---|---|
| `vstack`, `hstack` | `children` (required), `spacing` 0-64, `align` leading/center/trailing, `padding` 0-32 (inner padding in points), `radius` 0-32 (corner radius of the fill), `background` (`{"light","dark"}`; on the root it is the card background) |
| `spacer` | `min` 0-256 |
| `text` | `text` (at most 512 characters), `style`, `color`, `lines` 1-10, `align`, `weight` regular/medium/semibold/bold |
| `symbol` | `name` (SF Symbol), `color`, `size` 8-96 |
| `image` | `url` (https), `width`/`height` at most 512, `fit` fill/fit, `radius` 0-64 |
| `progress` | `value` 0-1, `color`, `label` |
| `gauge` | `value`, `min`, `max`, `label`, `color` |
| `timer` | `to` (unix seconds), `mode` countdown/countup, `style`, `color` |
| `badge` | `text`, `color`, `symbol` |
| `divider` | `color` (optional); a 1 pt hairline, horizontal in a vstack and vertical in an hstack |
| `foreach` | `each` (data path to an array, required), `body` (one node, required), `max` 1-12 (default 6), `axis` vertical/horizontal (default vertical), `spacing` 0-64 |

`style` is one of `largeTitle, title, headline, body, caption, footnote`.
Numeric properties take a literal or `{"bind":"path"}`; string properties may
contain `{{path}}` placeholders (at most 16 per string). A `path` is dot-separated
keys of `[A-Za-z0-9_.-]` resolved against the message `data` (`items.0.name`
indexes arrays). Malformed placeholders (`{{`, `{{ a }}`, `{{{{x}}}}`) and an
`http://` `image.url` are rejected; an `image.url` starting with a placeholder
is accepted and checked again when the image is fetched. Out-of-range literals
are rejected with the path of the property; an invalid `bind` path is reported
at `<property>.bind`. A missing path renders as empty. There are no expressions;
`if` (below) leaves a node out.

`foreach` repeats its `body` once per element of the array at `each` (at most
`max` copies), laid out along `axis`. Inside the body, `{{item.<path>}}` and
`{"bind":"item.<path>"}` read the current element (`{{item}}` itself for a
string or number element) and `{{index}}` is its 0-based position; every other
path still reads the message `data`. A missing or non-array `each` renders
nothing. The body may contain containers but not another `foreach`
(`body.children[0]: foreach cannot be nested inside another foreach`). The body
counts once towards the depth and 64-node limits at save time; the app also
caps the expanded result at 64 nodes and drops extra copies, and values read
from `item` are plain text (a `{{...}}` inside a value is never expanded).

    {"type":"vstack","spacing":6,"children":[
      {"type":"text","text":"{{title}}","style":"headline"},
      {"type":"foreach","each":"items","max":5,"spacing":4,"body":
        {"type":"hstack","children":[
          {"type":"text","text":"{{item.title}}"},{"type":"spacer"},
          {"type":"text","text":"{{item.value}}","style":"caption"}]}}]}

#### Padding, cards and `if`

On the root stack, `padding` and `radius` style the card itself. Any node may carry `if`: a data path, optionally
prefixed with `!` (`^!?[A-Za-z0-9_.-]+$`); the node is left out unless the value is truthy (`!path`: unless falsy).
Falsy values are missing, `null`, `false`, `0`, `""`, `[]` and `{}`. Inside a `foreach` body, `if` paths can read
`item.<path>` and `index`.

Errors are `{"error": "<code>"}`, with `field` and `message` for validation
failures (template errors name the node, e.g. `body.children[2].type`).

## Webhooks

Events: `pairing.created`, `pairing.revoked`, `message.delivered`,
`message.undeliverable`, `message.acked`, `message.dismissed`, `message.expired`,
`action`, `activity.ended`. Body: `{"id","event","created_at","data":{...}}`. Header:

    X-Hansho-Webhook-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>

A non-2xx answer is retried after 10 s x 2^attempt, up to 8 attempts.

Acknowledging is opt-in per message. A page always asks for it (acknowledging is what stops the ringing). Any
other message asks for it only when it has the built-in `ack` action or an action flagged `"ack": true`. The
first press of that button sends `action` and `message.acked` and sets the message to `acked`; for a page it
also stops the ringing on every device. Later presses are ignored.

When the person dismisses such a message without acknowledging it, the hub sends `message.dismissed`
(`message_id`, `external_user_id`, `device_id`). Dismissing other messages is never reported. iOS counts only
an explicit dismissal (Clear in Notification Center, Dismiss on the Watch); a banner flicked away or a notification
left alone is not reported, and "Clear All" may not be either. So a missing `message.dismissed` means "unknown",
not "seen".

## Device API (used by the app)

Authenticated by an ECDSA P-256 signature (raw `r||s`, base64url) over
`timestamp \n method \n path \n hex(sha256(body))` in `X-Hansho-Device`,
`X-Hansho-Timestamp`, `X-Hansho-Signature` (skew 300 s, replay cache 600 s).
Cryptographic formats (pairing-key wrap, message envelope, Live Activity
state) are pinned by `internal/crypto/testdata/vectors.json`.

`GET /device/v1/acks?ids=<id>,<id>,...` (device-signed; at most 100 ids, more is
`400 invalid_request`) tells a device which of its pages were acknowledged on
another device, for when the cancel push was missed or the app was closed. It
returns `{"acked": {"<message_id>": <unix seconds>}}` with only the
acknowledged messages of the calling device's user; unknown, unacknowledged and
other users' ids are omitted, so it cannot be used to probe for messages. The
signature covers the path only, not the query string. A retired device or a
revoked pairing is refused with `401 device_revoked` / `pairing_revoked` like
every other device call.

### Widget feeds: who sees which feed

`GET /device/v1/feeds` (device-signed) lists the feeds the app's picker offers:
`{"feeds":[{"feed_id","title?","updated_at"}]}`, sorted by `feed_id`. It is
exactly what `GET /device/v1/feeds/{id}` can fetch for that device's pairing: the
feeds published for its `external_user_id` plus the global ones (published
without a user), one entry per id, a person's own feed winning over a global one
with the same id, minus the feeds whose [audience](#audiences-groups-and-kick) does
not include the person. It is a listing only; the signed data still comes from
`GET /device/v1/feeds/{id}`, which answers `403 no_access` for a feed outside the
person's audience. To limit a feed to certain people, give it an `audience` (or a
`channel` whose audience it inherits) or publish it per user with `external_user_id`.
Older hubs answer the listing with 404 or 405, and the app then falls back to
asking for the feed id.

`GET /device/v1/hub` is unauthenticated and returns
`{hub_id, name, domain, public_key, fingerprint}` (`public_key` is base64url of the
raw 32-byte Ed25519 key, `fingerprint` its hex sha256). The same object is the
`hub` field of the pairing redeem and sync responses.

## Develop

    go test ./... -count=1 && go vet ./...
    docker build -t hansho-hub .

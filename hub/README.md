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
| `HUB_WEBHOOK_URL` | | | Where events are POSTed; empty disables webhooks. Must be `https://`, except for a loopback address or an in-cluster name (a single-label host such as `demo`, `*.svc`, `*.cluster.local`), which may use `http://`. Redirects are not followed (a 3xx counts as a failed attempt). Finished rows (delivered or dropped) are deleted from the outbox after 7 days |
| `HUB_WEBHOOK_SECRET` | with `HUB_WEBHOOK_URL` | | HMAC key for webhook signatures; the hub refuses to start with a webhook URL and no secret. Each attempt is signed with the time of that attempt |
| `HUB_ALLOW_DISPLAY_NAME_EDIT` | | `false` | Let a person change their own display name in the app. The sender can always set it through the API or the admin UI |
| `HUB_LA_ENCRYPT` | | `true` | Encrypt Live Activity content-state as `{"blob":...}` with a per-pairing key derived from the pairing key; `false` sends plaintext `{"t","d"}`. Verified on a device with the app force-quit; push-to-start attributes always carry `pairing_id` so the app can pick the key |
| `HUB_LOGO_FILE` | | | Path to a PNG logo (64 to 1024 px each way, at most 256 KB). The hub serves it at `/_hub/logo.png` and names it in its info, and the app shows it next to the hub's name once a person has paired; without it the app draws the name's initial |
| `HUB_LOGO_URL` | | | The same logo from an https URL instead of a file (not both). The hub downloads it once at start and serves it itself, so phones never contact that host; if the download fails the hub starts without a logo and logs a warning |
| `HUB_HOME_URL` | | `https://hansho.app` | The project site the status page at `GET /` links to (https only). `GET /` shows what the hub is (its name, domain and public key fingerprint) so that someone who opens the address in a browser is not left with a bare 404 |
| `HUB_ADMIN` | | `false` | `true` turns on the [admin UI](#admin-ui) at `/admin`. Off by default: without it `/admin` does not exist |
| `HUB_ADMIN_PASSWORD` | with `HUB_ADMIN` | | The one password everyone uses to sign in to the admin UI, at least 12 characters. The hub refuses to start with the admin UI on and no password or a shorter one. Changing it signs everyone out |

## Admin UI

The admin UI lets someone without code do part of what the sender API does, in a browser: pair a person (a pair code with a QR code and a link), see who is paired and remove them, make or revoke API keys, make and change channels, groups and templates, upload and delete sounds, see the feeds your software has published, send a test message, and see the settings in effect. It is built into the hub: server-rendered pages under `/admin`, no external requests, and it works without JavaScript (JavaScript only adds the copy buttons and updates the explanation under a level when you change it).

Turn it on with two settings and open `https://<your hub>/admin`:

```sh
export HUB_ADMIN=true
export HUB_ADMIN_PASSWORD='a long password only your team knows'
```

- **Sign-in** is one shared password, no accounts. A session lasts 12 hours and is a signed cookie (`HttpOnly`, `Secure`, `SameSite=Strict`) whose key is derived from `HUB_MASTER_KEY` and the password. Every form carries a per-session token and its `Origin` or `Referer` must be the hub's own host. Pages are sent with `Cache-Control: no-store` and cannot be framed.
- **Wrong passwords** are limited per client address: after 5, sign-in is refused for 30 seconds, doubling with every further wrong password up to 15 minutes. The count lives in memory. The hub reads the address of the connection, not `X-Forwarded-For`, so behind a reverse proxy every admin shares one limit.
- **Actions are logged**, one line each and never with a secret (`admin: api key created id=2`, `admin: person removed`, `admin: pair code created`, `admin: channel saved`, `admin: group deleted`, `admin: test message sent`, sign-ins and sign-outs; a test message is logged with its id, channel, person and level, never its text). There is no history page.
- **A pair code and a new API key are shown once**, in the response to the form. The hub keeps only a hash of a key. Removing a person does what `DELETE /api/v1/users/{external_user_id}/pairings` does (a kick), and a person is the `external_user_id` you type, so people paired through the API show up too.
- **Channels and groups** are stored exactly like the ones the API makes, by the same code (the same validation as `PUT /api/v1/channels/{id}` and `PUT /api/v1/groups/{id}`), so a channel made here can be managed with scripts or Terraform and the other way round. Every channel and group page says that an apply overwrites what was changed in the browser, and the edit pages have a "Copy as API call" panel with the `curl` command (your hub's `HUB_PUBLIC_URL`, `$KEY` for the API key) and the JSON body of what is stored. A channel's id and a group's id cannot be changed once made, and a new one is refused when the id exists already (the API's `PUT` would replace it). A channel lists its buttons (id, label, plain, link or reply, and whether pressing acknowledges), and its audience as everyone, only the ticked people and groups, or everyone except them; a person who has not paired yet is typed in by name. Deleting a group asks first and lists the channels whose audience names it (the API allows it too: the group then counts as empty). Feeds can name a group as well, and the confirmation page cannot list them.
- **Templates** are checked and stored by the code of `PUT /api/v1/templates/{id}`: the hub's own validator, and the rule that a version must increase. You edit the template's JSON in a text box; a problem is shown the way the web template builder shows it, the first one with its path (`body.children[0].type: unknown component web`), and "Check without saving" only validates. Each real change is stored as the next version by itself (saving the same template again, laid out differently, changes nothing), and an id cannot be changed once made. "Use this example" fills the box with one of the builder's starter templates. The page also has a ready-to-copy prompt for an AI assistant (the rules, a component reference built from the hub's validator so it cannot fall behind it, an example, the template in the box and the last error), a link to the template builder at `HUB_HOME_URL` plus `/templates` to paste the template into (it is only a link: nothing is sent from the hub) and a "Copy as API call" panel whose call carries the next version, because the API only accepts a higher one. Deleting asks first and lists the channels that use the template; the store refuses while one does, as the API's `409 in_use` does.
- **Sounds** are stored by the code of `PUT /api/v1/sounds/{id}`: a CAF, WAV or AIFF file of at most 1 MB, WAV and AIFF shorter than 30 seconds. The type is read from the file's first bytes (its name is only the fallback), and uploading an id that exists replaces that sound. The list shows id, type, size and the first 12 characters of the SHA-256. Deleting asks first, and is refused while a channel uses the sound.
- **Feeds** are a read-only list of what your software published with `PUT /api/v1/feeds/{id}`: id, name in the app, template, who sees it and when it was last updated, never the data. Feeds and Live Activities are published by the sender's software through the API; there is nothing to change in the admin UI.
- **Settings** is a read-only page of what is in effect: display name, public address, hub id, relay, project site, whether a logo is set (with a preview), the webhook's host and whether its secret is set, whether Live Activities are encrypted, whether the admin UI is on, the pairing fingerprint and the build, each with the environment variable that changes it. It never shows a secret, the password or a key. These values stay in environment variables or Helm values.
- **A test message** goes to one person through the same code as `POST /api/v1/messages` (not through HTTP): choose or type a person, then one of the channels they may use. It is never sent above the channel's highest level, and the page says which level it went out at. The result links to a status page with what `GET /api/v1/messages/{id}` returns (status, level, repeats, deliveries per device, when it was acknowledged) and a Refresh link; nothing refreshes by itself. A page can set its own repeat interval and number of repeats, otherwise it uses 120 seconds and 5.
- **Put it behind TLS.** The session cookie is `Secure`, so browsers only keep it over `https://` (`HUB_PUBLIC_URL` is `https` anyway). The admin UI is on the same address as the API: if you do not want it reachable from the internet, block `/admin` at your reverse proxy or only allow your VPN's addresses, and use a password from a password manager.
- `HUB_ADMIN_ADDR` (a second listen address for the admin UI alone) is not implemented yet. The hub refuses to start when it is set, so the admin UI is never exposed somewhere you did not expect.

## Commands

| Command | |
|---|---|
| `hub serve` | HTTP API, delivery worker (1 s tick), webhook loop (1 s tick); graceful shutdown on SIGTERM |
| `hub apikey create <name>` | Print a new sender API key once |
| `hub apikey list` | List sender API keys: id, name, created, revoked (never the key itself) |
| `hub apikey revoke <id>` | Stop the key with that id from working; safe to repeat. Get the id from `hub apikey list` |
| `hub relay register` | Register with the relay for the host of `HUB_PUBLIC_URL` |
| `hub relay verify` | Ask the relay to check the domain challenge |

## Sender API (`Authorization: Bearer <api key>`)

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/pairing-tickets` | `{external_user_id}` -> `{ticket, qr, expires_at}` |
| `PUT /api/v1/users/{external_user_id}` | `{display_name}` -> 204. What other people see instead of the user's id; `""` clears it |
| `DELETE /api/v1/users/{external_user_id}/pairings` | Unpair a user everywhere. Done by the sender it is a *kick*: the user's devices are told so (see [Audiences](#audiences-groups-and-kick)) |
| `PUT/GET/DELETE /api/v1/channels/{id}` | `{name, default_level, max_level, sound?, template_id?, thread?, actions?, audience?}`; a PUT replaces the whole channel except its audience: leaving `audience` out keeps the stored one (a new channel gets `everyone`), so a republish from a client that does not know audiences never widens access; send `{"mode":"everyone"}` to open a channel up again |
| `PUT/GET/DELETE /api/v1/groups/{id}` | `{members:[external_user_id,...]}`; a PUT replaces the whole member list |
| `GET /api/v1/channels`, `/groups`, `/templates`, `/sounds` | Lists: an array of what the matching `GET .../{id}` returns |
| `PUT/GET/DELETE /api/v1/templates/{id}` | `{version, body}`; version must increase. A DELETE answers `409 in_use` while a channel still names the template |
| `PUT/GET/DELETE /api/v1/sounds/{id}` | PUT takes raw `audio/x-caf`, `audio/wav` or `audio/aiff`, at most 1 MB; WAV/AIFF shorter than 30 s. GET returns `{id, content_type, bytes, sha256}` without the audio; a DELETE answers `409 in_use` while a channel still names the sound |
| `POST /api/v1/messages` | `{external_user_id \| external_user_ids \| to_audience, channel, level?, title, subtitle?, body, image_url?, template_id?, data?, sound?, actions?, collapse_id?, page?:{interval_s?, max_retries?, ack?, show_acker?}}` -> 202 with a `recipients` list (see [Audiences](#audiences-groups-and-kick)) |
| `GET /api/v1/messages/{id}` | `{status, level, attempts, deliveries, acked_at?}` |
| `POST /api/v1/activities`, `PATCH`/`DELETE /api/v1/activities/{id}` | Live Activities (content-state at most 4 KB; update priority 5 or 10) |
| `PUT/GET /api/v1/feeds/{id}` | `{external_user_id?, template_id?, title?, channel?, audience?, data}` (GET takes `?external_user_id=` and returns the settings without the data); `channel` and `audience` are explained under [Audiences](#audiences-groups-and-kick) and an upsert without them keeps what is stored; omit the user for a feed shared by all paired users; `title` (at most 64 bytes) is the name the app's feed picker shows instead of the id, and an upsert without one clears it. Without `template_id` the app draws the data with an automatic layout chosen from its shape (a number with `min`/`max` -> gauge, a `value` -> big number, `items[]` with `state` -> status grid, other `items[]` -> list); a named template must exist |

### Audiences, groups and kick

Every channel and feed has an **audience**: who may use it.

    {"mode": "everyone" | "only" | "except", "users": ["external_user_id", ...], "groups": ["group_id", ...]}

`everyone` (what a new channel or feed gets when `audience` is left out; a later `PUT` of a channel without one keeps the stored audience) lets every paired user in; `only` lets in just the listed users and
the members of the listed groups; `except` lets in everyone but them. `users` and `groups` must be empty for `everyone`.
Users may not have paired yet and are not looked up when you save. Every group must exist: a `PUT` of a channel or
feed naming a missing group is refused with `400` (`audience.groups`, `unknown group`), so create the group first.
Deleting a group that audiences still name is allowed; it then behaves as an empty group. Groups are managed with `PUT /api/v1/groups/{id}` (`{"members": [...]}`, replacing the whole list),
`GET` and `DELETE`; the group id follows the same pattern as other ids, `GET` answers `404 unknown_group` for a missing one.

A feed uses the audience of the channel named in its `channel` field unless it sets its own `audience`; with neither it is
for everyone. Both are stored per feed; an upsert that leaves both out keeps them.

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

`image_url` and the `url` of a `"kind": "url"` action (in a message or a channel's `actions`) must be `https://` with a host, and `collapse_id` at most 64 bytes of printable ASCII without spaces; otherwise `400`. A Live Activity's `alert` is dropped when its channel's `max_level` is `passive`.

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

A page sent to several people (`external_user_ids` or `to_audience`) is one message per person, and they share a send. With `page.ack: "any"` (the default) the first acknowledgement by any of them acknowledges the page for all of them: the others stop ringing, their banner says who acknowledged it by display name (see below), or "acknowledged by another recipient" when that person has none (an `external_user_id` is never shown to other people; the encrypted cancel carries `"acked_by_other":{"name":"Alice"}`, or `{}` without a name, and no such key when the person acknowledged it themselves), each gets its own `message.acked` webhook, and `GET /api/v1/messages/{id}` shows `acked_by` and `acked_by_name`. With `page.ack: "each"` every person must acknowledge for themselves and is paged until they do. A page to one person behaves the same either way, and a page already paging when the hub is updated finishes as `each`. `message.acked` carries `acked_by`, the user who acknowledged, and `acked_by_name` when they have a display name.

**Display names.** A user can have a display name: what other people see instead of their `external_user_id`. It is one per user, shared by all their devices, any language (accents, emoji), trimmed, at most 40 characters, no control characters, and not unique. Set it with `PUT /api/v1/users/{external_user_id}` (`{"display_name":"Álice M."}`, `""` clears it, the user need not have paired yet), with an optional `display_name` in `POST /api/v1/pairing-tickets`, or on the admin People page. `GET /device/v1/sync` returns `profile: {display_name, editable}`. With `HUB_ALLOW_DISPLAY_NAME_EDIT=true` (default false) a person can change their own name in the app (`PUT /device/v1/profile`, otherwise `403 display_name_locked`). The last write wins, whether it came from the sender or the person.

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
returns `{"acked": {"<message_id>": <unix seconds>}, "acked_by_other": {"<message_id>": {"name"?: "Alice"}}}` (the latter only for pages another recipient acknowledged, by display name, never by user id; absent on older hubs) with only the
acknowledged messages of the calling device's user; unknown, unacknowledged and
other users' ids are omitted, so it cannot be used to probe for messages. The
signature covers the path only, not the query string. A retired device or a
revoked pairing is refused with `401 device_revoked` / `pairing_revoked` like
every other device call.

`POST /device/v1/actions` takes `{message_id, action_id, text?}`. The action id must be `ack`, `_dismissed`, or an
action of the message (or of its channel), else `400`. Each device may press at most 10 times in a burst, then one per
second (`429` with `Retry-After`). A message or activity of another user answers `404` exactly like an unknown id.

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

`GET /device/v1/pairing-tickets/{ticket}` is unauthenticated, read-only and does not use the ticket up. It answers
`{"state":"valid","expires_at":"<RFC3339>"}`, `{"state":"expired","expires_at":...}` (also for a ticket whose user was
removed with `DELETE /api/v1/users/{external_user_id}/pairings`) or `{"state":"used"}`, and `404 unknown_ticket` for a
ticket the hub never issued. The app asks it before it shows a pairing link's confirmation, so an expired link is
reported up front instead of after Approve (the redeem still answers `410 ticket_expired` / `ticket_used`). Older hubs
answer 404 or 405 and the app then skips the check.

`GET /device/v1/hub` is unauthenticated and returns
`{hub_id, name, domain, public_key, fingerprint}` (`public_key` is base64url of the
raw 32-byte Ed25519 key, `fingerprint` its hex sha256). The same object is the
`hub` field of the pairing redeem and sync responses.

## Develop

    go test ./... -count=1 && go vet ./...
    docker build -t hansho-hub .

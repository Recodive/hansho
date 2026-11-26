# app.hansho:hansho

Java client for a Hansho hub. Java 17 or newer, built on `java.net.http.HttpClient`; the only
runtime dependency is `jackson-databind`. It works from Kotlin and other JVM languages too.

```xml
<dependency>
  <groupId>app.hansho</groupId>
  <artifactId>hansho</artifactId>
  <version>0.2.0</version>
</dependency>
```

```kotlin
implementation("app.hansho:hansho:0.2.0")
```

See [../README.md](https://github.com/Recodive/hansho/blob/main/sdk/README.md) for the model shared with the other SDKs.

## Quick start

```java
import app.hansho.sdk.*;
import app.hansho.sdk.Models.*;

Hansho hansho = Hansho.create("https://hub.example.com", System.getenv("HANSHO_API_KEY"));

hansho.putChannel("ops", Channel.builder("Operations", Level.ACTIVE, Level.CRITICAL).build());

MessageResult res = hansho.sendMessage(
    Message.builder("user-42", "ops", "Disk full", "db-1 is at 97%").build());
System.out.println(res.messageId() + " " + res.status()); // msg_... queued
```

`Hansho` is thread-safe; share one instance. Options on `Hansho.builder(baseUrl, apiKey)`:
`maxRetries` (default 3, 0 disables), `timeout` (per attempt, default 30 s, `null` disables),
`userAgent`, `httpClient` (proxy, TLS, executor). Calls are synchronous; wrap them in your own
executor or `CompletableFuture.supplyAsync` if you need async. Levels are the enum `Level`
(`PASSIVE, ACTIVE, TIME_SENSITIVE, CRITICAL, PAGE`); an invalid level does not compile.

## Types

Every type that goes to or comes from the hub (`Message`, `Channel`, `Level`, `Audience`, `MessageResult`, the webhook
payloads, ...) is generated from the spec all the SDKs share, and lives in `app.hansho.sdk.Models`: Java wants one public
type per file, so they are nested in one class. `import app.hansho.sdk.Models.*;` brings them in. Each sent type has a
`builder(...)` with the required fields, setters for the rest, and `toBuilder()`.

A field the spec does not require is `null` when the hub leaves it out (a `String`, `Integer`, `Boolean`, ...; a list is
empty), and an enum value or field this SDK does not know (a newer hub) is `null` or ignored: decoding an answer never
throws for them.

## Pairing

```java
PairingTicket ticket = hansho.createPairingTicket("user-42");
// Render ticket.qr() as a QR code; the user scans it in the Hansho app.
// ticket.expiresAt() is 10 minutes out; the ticket works once.
// The hub then sends a pairing.created webhook.

hansho.unpair("user-42"); // revokes every pairing of the user
hansho.kick("user-42");   // the same call; the app tells the person that you ended it
```

## Users

A user can have a display name, which other people see instead of their external user id (any language, trimmed, at
most 40 characters, no control characters). Set it with the ticket, or any time later; `""` clears it.

```java
PairingTicket ticket = hansho.createPairingTicket("user-42", "Ada Lovelace");
hansho.setUserDisplayName("user-42", "Ada L."); // PUT /api/v1/users/user-42
hansho.setUserDisplayName("user-42", "");       // back to the id
```

## Audiences and groups

A channel or feed is open to everyone unless it has an `Audience`: `Audience.only(users, groups)` the listed users and
groups, or `Audience.except(users, groups)` them. Users are your external user ids (they may not have paired yet); groups
are named lists you keep on the hub. The hub decides who has access and checks it on every request, so a change applies at
once.

```java
hansho.putGroup("oncall", List.of("alice", "bob")); // replaces the whole member list
hansho.listGroups();                                // every group
hansho.putChannel("pager", Channel.builder("Pager", Level.PAGE, Level.PAGE)
    .audience(Audience.only(List.of(), List.of("oncall"))).build());
// An absent audience keeps the one stored on the hub (a new channel starts open to everyone);
// send Audience.everyone() to open an existing channel up.
// The groups an audience names must exist: otherwise the hub answers 400 (e.field() "audience.groups", e.detail() "unknown group").

// A feed uses its channel's audience unless it sets its own; like the title, send them with every update
// (an absent audience keeps the stored one here too).
hansho.putFeed("queue", Feed.of("status", Map.of("value", 3)).withChannel("pager"));
FeedSettings settings = hansho.getFeed("queue"); // settings without the data

// Send to several users, or to everyone in the channel's audience. People outside it are not delivered to.
MessageResult res = hansho.sendMessage(Message.forAudience("pager", "DB down", "...").build());
// Message.forUsers(List.of("alice", "carol"), "pager", "DB down", "...") is the several-users form.
for (Recipient r : res.recipients()) {
    if (Recipient.NO_ACCESS.equals(r.status())) { /* only with forUsers: not in the audience, no message was created */ }
}
```

A message has exactly one target: `Message.builder(userId, ...)`, `Message.forUsers(ids, ...)` (at most 1000) or
`Message.forAudience(...)`. For a send to one user the top-level `messageId()`, `status()`, `devices()` and
`deliveries()` describe that user (`messageId()` is empty and `status()` is `Recipient.NO_ACCESS` if they are outside the
audience). For several users or an audience the top-level `messageId()`, `status()` and `devices()` are `null`, `deliveries()` is
empty, and every message is under `recipients()` (an empty list for a hub that predates audiences).

Error codes: `HanshoException.NO_ACCESS` (403, `startActivity` for a user outside the channel's audience) and
`HanshoException.UNKNOWN_GROUP` (404).

## Sending

```java
// A notification. The level defaults to the channel's default and is capped at its max.
hansho.sendMessage(Message.builder("user-42", "ops", "Deploy failed", "web-3 rolled back")
    .level(Level.TIME_SENSITIVE)
    .actions(List.of(Action.of("retry", "Retry"),
        Action.url("runbook", "Runbook", "https://example.com/runbook"))) // a button that opens an https URL
    .collapseId("deploy-web") // a later message with the same id replaces this one
    .build());

// A page repeats every intervalSeconds until someone acknowledges it on any
// device, then the hub sends message.acked. Defaults: 120 s, 5 resends.
MessageResult page = hansho.sendMessage(Message.builder("user-42", "ops", "Primary DB down", "Failover did not complete")
    .level(Level.PAGE).page(PageOptions.of(60, 10)).build());
MessageStatus status = hansho.getMessage(page.messageId()); // queued, delivered, undeliverable, paging, acked, expired
// Once acked: status.ackedAt(), status.ackedBy() (external user id) and status.ackedByName() (null unless they have one).

// A page to several people: by default the first acknowledgement stops it for everyone (PageAck.ANY).
// With PageAck.EACH every recipient has to acknowledge for themselves.
hansho.sendMessage(Message.forUsers(List.of("alice", "bob"), "ops", "Primary DB down", "Failover did not complete")
    .level(Level.PAGE).page(PageOptions.of(60, 10).withAck(PageAck.EACH)).build());

// A templated message; data is sent exactly as given (its keys are never renamed).
hansho.putTemplate("status", 1, Map.of("type", "vstack", "children",
    List.of(Map.of("type", "text", "style", "headline", "text", "{{title}}"))));
hansho.sendMessage(Message.builder("user-42", "ops", "Uptime", "99.98%")
    .templateId("status").data(Map.of("title", "Uptime")).build());

// A Live Activity.
ActivityStarted act = hansho.startActivity(Activity.of("user-42", "ops", "deploy", Map.of("progress", 0.1)));
hansho.updateActivity(act.activityId(), ActivityUpdate.of(Map.of("progress", 0.6)).withPriority(10));
hansho.endActivity(act.activityId());

// A widget feed; leave out forUser(...) for one shared by all paired users. The title is the name the
// app's feed picker shows (it lists the feeds published for that person plus the shared ones).
hansho.putFeed("uptime", Feed.of("status", Map.of("title", "up")).forUser("user-42").withTitle("Uptime"));

// A sound (audio/x-caf, audio/wav or audio/aiff, at most 1 MB).
hansho.putSound("ding", "audio/wav", wavBytes);
```

## Channels, templates, sounds and groups

```java
List<Channel> channels = hansho.listChannels();   // channel.id() is set in answers (and ignored when sent)
Channel ops = hansho.getChannel("ops");

List<Template> templates = hansho.listTemplates(); // each at its latest version
Template status = hansho.getTemplate("status");    // status.id(), status.version(), status.body() (Map/List/...)
hansho.deleteTemplate("status");                   // refused with code "in_use" while a live channel names it

List<Sound> sounds = hansho.listSounds();          // metadata: id(), contentType(), bytes(), sha256()
Sound ding = hansho.getSound("ding");
hansho.deleteSound("ding");                        // refused with code "in_use" while a live channel names it

List<Group> groups = hansho.listGroups();
FeedSettings feed = hansho.getFeed("uptime", "user-42"); // or getFeed("uptime") for the shared feed
```

`data` and template bodies take a `Map`, a `List`, a Jackson `JsonNode` or any Jackson-serializable
object. Ids may contain any characters except `.` and `..`: they are path-escaped.

## Webhooks

Set `HUB_WEBHOOK_URL` and `HUB_WEBHOOK_SECRET` on the hub. Verify the **raw** request body as
bytes: a body a framework parsed and you re-serialised will not match its signature.

```java
HttpServer server = HttpServer.create(new InetSocketAddress(8081), 0);
server.createContext("/hansho-hook", ex -> {
    byte[] body = ex.getRequestBody().readAllBytes();
    int status = 200;
    try {
        WebhookEvent event = Webhook.verify(
            System.getenv("HANSHO_WEBHOOK_SECRET"),
            ex.getRequestHeaders().getFirst(Webhook.SIGNATURE_HEADER),
            body);
        if (event instanceof WebhookEvent.MessageAcked e) {
            System.out.println("acknowledged: " + e.data().messageId());
        } else if (event instanceof WebhookEvent.ActionTaken e) {
            System.out.println("button " + e.data().actionId() + " on " + e.data().messageId());
        } else if (event instanceof WebhookEvent.Unknown e) {
            System.out.println("newer hub event: " + e.name());
        }
    } catch (WebhookException e) {
        status = 400; // non-2xx: the hub retries
    }
    ex.sendResponseHeaders(status, -1);
    ex.close();
});
server.start();
```

In Spring, take the body as `byte[]` (`@RequestBody byte[] body`) and the header with
`@RequestHeader(value = "X-Hansho-Webhook-Signature", required = false)`; never a bound object. In Servlet
code read `request.getInputStream().readAllBytes()` before anything parses it.

`Webhook.verify` accepts several `v1=` signatures (secret rotation), compares in constant time and
rejects timestamps more than 300 s from now (pass `now` and `tolerance` to change or inject them).
`WebhookException.code()` is one of `MALFORMED_HEADER, BAD_SIGNATURE, STALE` (a valid signature, replayed too
late), `MALFORMED_BODY, INVALID_BODY, INVALID_SECRET`; `code().wire()` spells it like the other SDKs
(`"bad_signature"`). Events (records nested in the sealed interface `WebhookEvent`): `PairingCreated`,
`PairingRevoked`, `MessageDelivered`, `MessageUndeliverable`, `MessageAcked`, `MessageDismissed`,
`MessageExpired`, `ActionTaken`, `ActivityEnded`; each has `data()`, the payload as the `Models` type of the same name
(`e.data().messageId()`); an optional field the hub left out is `null`. Anything else is `Unknown` with the event name and the
parsed payload (Map, List, ...) in `data()`. Every event has `type()`, `id()`, `createdAt()` and `raw()`.

## Errors

```java
try {
    hansho.sendMessage(msg);
} catch (HanshoException e) {
    // e.status(), e.code() ("unknown_channel", "invalid_request", ...), e.field() ("title"), e.retryAfterMs()
} catch (HanshoNetworkException e) {
    // no answer at all: connection, timeout or interruption; see e.getCause()
}
```

Both are unchecked. A bad argument (an empty id, `.` or `..` as an id, a sound over 1 MB) throws
`IllegalArgumentException` before anything is sent. API keys, ticket values and message bodies never
appear in an error message.

## Retries

`GET`, `PUT` and `DELETE` are retried on 429 and 503, up to `maxRetries` times, waiting for
`Retry-After` (a wait over 60 s is thrown to you as the error) or an exponential backoff with jitter.
`sendMessage` is never retried unless the message has an `idempotencyKey` (sent as the
`Idempotency-Key` header); the hub does not deduplicate on it yet, so a retried send is at-least-once.
Activity updates (`PATCH`) are not retried.

## Building and testing

```sh
mvn verify                  # unit tests; they load ../spec/openapi.json (x-conformance) and ../spec/webhook-vectors.json
../scripts/run-integration.sh   # builds the hub, then runs IntegrationTest against it
```

`IntegrationTest` is skipped unless `HANSHO_HUB_URL` is set (the script also sets `HANSHO_API_KEY`,
`HANSHO_WEBHOOK_SECRET`, `HANSHO_WEBHOOK_ADDR` and `HANSHO_FAKEDEVICE_BIN`).

Published on Maven Central as `app.hansho:hansho`. A release is a `sdk/java/v<version>` tag; see [RELEASING.md](https://github.com/Recodive/hansho/blob/main/sdk/RELEASING.md).

## Licence

MIT, see [LICENSE](LICENSE). The hub itself is AGPL-3.0.

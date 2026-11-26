# Hansho for .NET

.NET client for a Hansho hub. Targets `net8.0` and newer, no package dependencies
(`System.Net.Http` and `System.Text.Json` from the base class library), nullable
reference types, async with `CancellationToken` everywhere.

```sh
dotnet add package Hansho
```

See the [SDK overview](https://github.com/Recodive/hansho/blob/main/sdk/README.md) for the model shared with the Go and Node SDKs.
Licence: MIT, see [LICENSE](https://github.com/Recodive/hansho/blob/main/sdk/LICENSE).

## Quick start

```csharp
using Hansho;

using var hansho = new HanshoClient("https://hub.example.com", Environment.GetEnvironmentVariable("HANSHO_API_KEY")!);

await hansho.PutChannelAsync("ops", new Channel { Name = "Operations", DefaultLevel = Level.Active, MaxLevel = Level.Critical });

var res = await hansho.SendMessageAsync(new Message
{
    ExternalUserId = "user-42",
    Channel = "ops",
    Title = "Disk full",
    Body = "db-1 is at 97%",
});
Console.WriteLine($"{res.MessageId} {res.Status}"); // msg_... queued
```

`HanshoClient` is thread-safe: create one and reuse it (register it as a singleton). Options go in
`HanshoClientOptions`: `HttpClient` (your own, from `IHttpClientFactory` or with a proxy; it is
never disposed or reconfigured), `MaxRetries` (default 3, 0 disables), `Timeout` (per attempt,
default 30 s), `UserAgent`. Every method takes a `CancellationToken` as its last argument to abort the
request and any retry wait.

`Level` is `Level.Passive`, `Level.Active`, `Level.TimeSensitive`, `Level.Critical` or `Level.Page`.
It is a struct, not an enum, so a level a newer hub sends is kept as it is.

## Pairing

```csharp
var ticket = await hansho.CreatePairingTicketAsync("user-42");
// Render ticket.Qr as a QR code; the user scans it in the Hansho app.
// ticket.ExpiresAt is 10 minutes out; the ticket works once.
// The hub then sends a pairing.created webhook.

await hansho.UnpairAsync("user-42"); // revokes every pairing of the user
await hansho.KickAsync("user-42");   // the same call; the app tells the person that you ended it
```

## Users

A user can have a display name, which other people see instead of their external user id (any language, trimmed, at
most 40 characters, no control characters). Set it with the ticket, or any time later; `""` clears it.

```csharp
var ticket = await hansho.CreatePairingTicketAsync("user-42", "Ada Lovelace");
await hansho.SetUserDisplayNameAsync("user-42", "Ada L."); // PUT /api/v1/users/user-42
await hansho.SetUserDisplayNameAsync("user-42", "");       // back to the id
```

## Audiences and groups

A channel or feed is open to everyone unless it has an `Audience`: `AudienceMode.Only` the listed users and groups, or
`AudienceMode.Except` them. Users are your external user ids (they may not have paired yet); groups are named lists you
keep on the hub. The hub decides who has access and checks it on every request, so a change applies at once.

```csharp
await hansho.PutGroupAsync("oncall", new[] { "alice", "bob" }); // replaces the whole member list
await hansho.PutChannelAsync("pager", new Channel
{
    Name = "Pager", DefaultLevel = Level.Page, MaxLevel = Level.Page,
    Audience = new Audience { Mode = AudienceMode.Only, Groups = new[] { "oncall" } },
});
// An absent Audience keeps the one stored on the hub (a new channel starts open to everyone);
// send new Audience { Mode = AudienceMode.Everyone } to open an existing channel up.
// The groups an audience names must exist: otherwise the hub answers 400 (Field "audience.groups", Detail "unknown group").

// A feed uses its Channel's audience unless it sets its own; like Title, send them with every update
// (an absent Audience keeps the stored one here too).
await hansho.PutFeedAsync("queue", new Feed { TemplateId = "status", Channel = "pager", Data = new { value = 3 } });
var settings = await hansho.GetFeedAsync("queue"); // settings without the data

// Send to several users, or to everyone in the channel's audience. People outside it are not delivered to.
var res = await hansho.SendMessageAsync(new Message { ToAudience = true, Channel = "pager", Title = "DB down", Body = "..." });
foreach (var r in res.Recipients ?? Array.Empty<Recipient>())
{
    if (r.Status == Recipient.NoAccess) { /* only with ExternalUserId(s): not in the audience, no message was created */ }
}
```

Give a message exactly one of `ExternalUserId`, `ExternalUserIds` (at most 1000) and `ToAudience`. For a send to
`ExternalUserId` the top-level `MessageId`, `Status`, `Devices` and `Deliveries` describe that user (`MessageId` is
empty and `Status` is `Recipient.NoAccess` if they are outside the audience). For several users or an audience the
top-level `MessageId`, `Devices` and `Deliveries` are empty and every message is under `Recipients`.

Error codes in `ErrorCodes`: `NoAccess` (403, `StartActivityAsync` for a user outside the channel's audience) and
`UnknownGroup` (404).

## Sending

```csharp
// A notification. The level defaults to the channel's default and is capped at its max.
await hansho.SendMessageAsync(new Message
{
    ExternalUserId = "user-42", Channel = "ops", Level = Level.TimeSensitive,
    Title = "Deploy failed", Body = "web-3 rolled back",
    Actions = new[] { new MessageAction { Id = "retry", Title = "Retry" } },
    CollapseId = "deploy-web", // a later message with the same id replaces this one
});

// A page repeats every IntervalSeconds until someone acknowledges it on any
// device, then the hub sends message.acked. Defaults: 120 s, 5 resends.
var page = await hansho.SendMessageAsync(new Message
{
    ExternalUserId = "user-42", Channel = "ops", Level = Level.Page,
    Title = "Primary DB down", Body = "Failover did not complete",
    Page = new PageOptions { IntervalSeconds = 60, MaxRetries = 10 },
});
var status = await hansho.GetMessageAsync(page.MessageId); // queued, delivered, undeliverable, paging, acked, expired
// Once acked: status.AckedAt, status.AckedBy (external user id) and status.AckedByName (null unless they have one).
// The message.acked webhook (MessageAcked) carries AckedBy and AckedByName too.

// A page to several people: by default the first acknowledgement stops it for everyone (PageAck.Any).
// With PageAck.Each every recipient has to acknowledge for themselves.
await hansho.SendMessageAsync(new Message
{
    ExternalUserIds = new[] { "alice", "bob" }, Channel = "ops", Level = Level.Page,
    Title = "Primary DB down", Body = "Failover did not complete",
    Page = new PageOptions { Ack = PageAck.Each },
});

// A templated message. Data is sent exactly as given: a dictionary, an anonymous or plain object,
// a JsonElement or a JsonNode. Keys are never renamed (no camelCase or snake_case policy).
await hansho.PutTemplateAsync("status", 1, new Dictionary<string, object>
{
    ["type"] = "vstack",
    ["children"] = new[] { new { type = "text", style = "headline", text = "{{title}}" } },
});
await hansho.SendMessageAsync(new Message
{
    ExternalUserId = "user-42", Channel = "ops", TemplateId = "status",
    Title = "Uptime", Body = "99.98%", Data = new { title = "Uptime" },
});

// A Live Activity.
var act = await hansho.StartActivityAsync(new Activity { ExternalUserId = "user-42", Channel = "ops", TemplateId = "deploy", Data = new { progress = 0.1 } });
await hansho.UpdateActivityAsync(act.ActivityId, new ActivityUpdate { Data = new { progress = 0.6 }, Priority = 10 });
await hansho.EndActivityAsync(act.ActivityId);

// A widget feed; leave ExternalUserId null for one shared by all paired users. The title is the name the
// app's feed picker shows (it lists the feeds published for that person plus the shared ones).
await hansho.PutFeedAsync("uptime", new Feed { ExternalUserId = "user-42", TemplateId = "status", Title = "Uptime", Data = new { title = "up" } });

// A sound (audio/x-caf, audio/wav or audio/aiff, at most 1 MB).
await hansho.PutSoundAsync("ding", "audio/wav", wavBytes);
```

Ids may contain any characters except `.` and `..`: they are path-escaped. An empty id, `.` or `..`
throws `ArgumentException` before anything is sent.

## Webhooks

Set `HUB_WEBHOOK_URL` and `HUB_WEBHOOK_SECRET` on the hub. Verify the **raw** request body: a body a
framework parsed and you re-serialised will not match its signature. `HanshoWebhook.Verify` only takes
bytes or a string, so a parsed object does not compile.

ASP.NET Core minimal API (read the body yourself, do not bind it):

```csharp
using Hansho;

app.MapPost("/hansho-hook", async (HttpRequest req) =>
{
    using var ms = new MemoryStream();
    await req.Body.CopyToAsync(ms);
    try
    {
        var ev = HanshoWebhook.Verify(
            Environment.GetEnvironmentVariable("HANSHO_WEBHOOK_SECRET")!,
            req.Headers[HanshoWebhook.SignatureHeader].ToString(),
            ms.ToArray());
        switch (ev)
        {
            case MessageAcked e: Console.WriteLine($"acknowledged: {e.MessageId}"); break;
            case ActionTaken e: Console.WriteLine($"button {e.ActionId} on {e.MessageId}"); break;
            case UnknownWebhookEvent e: Console.WriteLine($"newer hub event: {e.Name}"); break;
        }
        return Results.Ok();
    }
    catch (WebhookException)
    {
        return Results.StatusCode(400); // non-2xx: the hub retries
    }
});
```

Plain `HttpListener`: read `ctx.Request.InputStream` into a byte array and pass
`ctx.Request.Headers["X-Hansho-Webhook-Signature"]` as the header.

`Verify` accepts several `v1=` signatures (secret rotation), compares them in constant time and
rejects timestamps more than 300 s from now (the `tolerance` argument changes it; `now` is
injectable). `WebhookException.Code` is one of `MalformedHeader`, `BadSignature`, `Stale` (a valid
signature, replayed too late), `MalformedBody`, `InvalidSecret`. The events are `PairingCreated`,
`PairingRevoked`, `MessageDelivered`, `MessageUndeliverable`, `MessageAcked`, `MessageDismissed`,
`MessageExpired`, `ActionTaken` and `ActivityEnded`, all deriving from `WebhookEvent` (`Id`, `CreatedAt`,
`Raw`, `Type`); anything else is an `UnknownWebhookEvent` with its `Name` and the payload in `Data`
(a `JsonElement`).

## Errors

```csharp
try
{
    await hansho.SendMessageAsync(msg);
}
catch (HanshoApiException e)
{
    // e.Status, e.Code ("unknown_channel", "invalid_request", ...), e.Field ("title"), e.RetryAfter, e.Detail
}
catch (HanshoNetworkException e)
{
    // no answer at all: connection, timeout or cancellation; see e.InnerException,
    // e.IsTimeout and e.IsCancellation
}
```

Both derive from `HanshoException`. A cancelled call is a `HanshoNetworkException` with
`IsCancellation` true (its inner exception is the `OperationCanceledException`), the same as in the Go and
Node SDKs; catch it with `catch (HanshoNetworkException e) when (e.IsCancellation)`. Messages carry the
method, the path and the status, never the API key, a ticket or a message body.

## Retries

`GET`, `PUT` and `DELETE` are retried on 429 and 503, up to `MaxRetries` times, waiting for
`Retry-After` (a wait over 60 s is thrown to you as the error) or an exponential backoff with jitter.
`SendMessageAsync` is never retried unless the message has an `IdempotencyKey`; the hub does not
deduplicate on it yet, so a retried send is at-least-once. Activity updates (`PATCH`) are not retried.

The client created for you does not follow redirects (an API key should not be replayed to another
host); point `BaseUrl` at the hub's final URL.

## Tests

```sh
dotnet test tests/Hansho.Tests -f net8.0     # or net10.0
```

The unit tests load `../spec/api.json` and `../spec/webhook-vectors.json` and run against a local fake
hub. `../scripts/run-integration.sh` runs the integration test against the real hub binary.

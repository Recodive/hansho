# Hansho Go SDK

```sh
go get github.com/recodive/hansho/sdk/go
```

```go
import hansho "github.com/recodive/hansho/sdk/go"
```

Go 1.26 or newer, standard library only. See [../README.md](../README.md) for
the model shared with the Node SDK.

## Quick start

```go
c := hansho.New("https://hub.example.com", os.Getenv("HANSHO_API_KEY"))

err := c.PutChannel(ctx, "ops", hansho.Channel{
    Name: "Operations", DefaultLevel: hansho.LevelActive, MaxLevel: hansho.LevelCritical,
})

res, err := c.SendMessage(ctx, hansho.Message{
    ExternalUserID: "user-42", Channel: "ops",
    Title: "Disk full", Body: "db-1 is at 97%",
})
fmt.Println(res.MessageID, res.Status) // msg_... queued
```

Options: `WithHTTPClient(*http.Client)` (default 30 s timeout), `WithUserAgent(string)`,
`WithMaxRetries(int)` (default 3, 0 disables).

## Pairing

```go
ticket, err := c.CreatePairingTicket(ctx, "user-42")
// Render ticket.QR as a QR code; the user scans it in the Hansho app.
// ticket.ExpiresAt is 10 minutes out; the ticket works once.
// The hub then sends a pairing.created webhook.

err = c.Unpair(ctx, "user-42") // revokes every pairing of the user
err = c.Kick(ctx, "user-42")   // the same call; the app tells the person that you ended it
```

## Audiences and groups

A channel or feed is open to everyone unless it has an `Audience`: `AudienceOnly` the listed users and groups, or
`AudienceExcept` them. Users are your external user ids (they may not have paired yet); groups are named lists you keep
on the hub. The hub decides who has access and checks it on every request, so a change applies at once.

```go
c.PutGroup(ctx, "oncall", []string{"alice", "bob"}) // replaces the whole member list
c.PutChannel(ctx, "pager", hansho.Channel{
    Name: "Pager", DefaultLevel: hansho.LevelPage, MaxLevel: hansho.LevelPage,
    Audience: &hansho.Audience{Mode: hansho.AudienceOnly, Groups: []string{"oncall"}},
})
// PutChannel replaces the channel, except that a nil Audience keeps the stored one (send Mode "everyone" to open it up).

// A feed uses its Channel's audience unless it sets its own. Send both with every update.
c.PutFeed(ctx, "queue", hansho.Feed{TemplateID: "status", Channel: "pager", Data: map[string]any{"value": 3}})
fs, err := c.GetFeed(ctx, "queue", "") // settings without the data

// Send to several users, or to everyone in the channel's audience. People outside it are not delivered to.
res, err := c.SendMessage(ctx, hansho.Message{ToAudience: true, Channel: "pager", Title: "DB down", Body: "..."})
for _, r := range res.Recipients {
    if r.Status == hansho.RecipientNoAccess { /* only with ExternalUserID(s): not in the audience */ }
}
```

Error codes: `hansho.CodeNoAccess` (403, `StartActivity` for a user outside the channel's audience) and
`hansho.CodeUnknownGroup` (404).

## Sending

```go
// A notification. Level defaults to the channel's default and is capped at its max.
c.SendMessage(ctx, hansho.Message{
    ExternalUserID: "user-42", Channel: "ops", Level: hansho.LevelTimeSensitive,
    Title: "Deploy failed", Body: "web-3 rolled back",
    Actions: []hansho.Action{{ID: "retry", Title: "Retry"}},
    CollapseID: "deploy-web", // a later message with the same id replaces this one
})

// A page repeats every IntervalSeconds until someone acknowledges it on any
// device, then the hub sends message.acked. Zero PageOptions fields mean the hub
// defaults (120 s, 5 resends).
c.SendMessage(ctx, hansho.Message{
    ExternalUserID: "user-42", Channel: "ops", Level: hansho.LevelPage,
    Title: "Primary DB down", Body: "Failover did not complete",
    Page: &hansho.PageOptions{IntervalSeconds: 60, MaxRetries: 10},
})

st, err := c.GetMessage(ctx, res.MessageID) // queued, delivered, undeliverable, paging, acked, expired

// A templated message: data is rendered by the app with the template.
c.PutTemplate(ctx, "status", 1, json.RawMessage(`{"type":"vstack","children":[{"type":"text","style":"headline","text":"{{title}}"}]}`))
c.SendMessage(ctx, hansho.Message{ExternalUserID: "user-42", Channel: "ops", TemplateID: "status",
    Title: "Uptime", Body: "99.98%", Data: map[string]any{"title": "Uptime"}})

// A Live Activity.
act, err := c.StartActivity(ctx, hansho.Activity{ExternalUserID: "user-42", Channel: "ops", TemplateID: "deploy",
    Data: map[string]any{"progress": 0.1}})
c.UpdateActivity(ctx, act.ActivityID, hansho.ActivityUpdate{Data: map[string]any{"progress": 0.6}, Priority: 10})
c.EndActivity(ctx, act.ActivityID)

// A widget feed; leave ExternalUserID empty for one shared by all paired users. Title is the name the app's feed
// picker shows (it lists the feeds published for that person plus the shared ones).
c.PutFeed(ctx, "uptime", hansho.Feed{ExternalUserID: "user-42", TemplateID: "status", Title: "Uptime", Data: map[string]any{"title": "up"}})

// A sound (audio/x-caf, audio/wav or audio/aiff, at most 1 MB).
c.PutSound(ctx, "ding", "audio/wav", f)
```

Ids may contain any characters: they are path-escaped.

## Webhooks

Set `HUB_WEBHOOK_URL` and `HUB_WEBHOOK_SECRET` on the hub. Verify the **raw**
request body: a body you parsed and re-serialised will not match its signature.

```go
mux.Handle("POST /hansho-hook", hansho.NewWebhookHandler([]byte(os.Getenv("HANSHO_WEBHOOK_SECRET")),
    func(ctx context.Context, e hansho.Event) error {
        data, err := e.Data()
        if err != nil {
            return err
        }
        switch d := data.(type) {
        case hansho.MessageAcked:
            log.Println("acknowledged:", d.MessageID)
        case hansho.ActionTaken:
            log.Println("button", d.ActionID, "on", d.MessageID)
        case json.RawMessage: // e.Type == hansho.EventUnknown: a newer hub, e.Name has the name
        }
        return nil // an error makes the handler answer 500 and the hub retries
    }))
```

Or do it yourself with plain `net/http`:

```go
func hook(w http.ResponseWriter, r *http.Request) {
    body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20)) // the raw bytes
    if err != nil {
        http.Error(w, "bad request", http.StatusBadRequest)
        return
    }
    ev, err := hansho.VerifyWebhook(secret, r.Header.Get(hansho.WebhookSignatureHeader), body, time.Now(), 0)
    switch {
    case errors.Is(err, hansho.ErrStale): // a valid signature, replayed too late
    case err != nil: // hansho.ErrBadSignature, ErrMalformedHeader, ErrMalformedBody
        http.Error(w, "invalid", http.StatusBadRequest)
        return
    }
    _ = ev
}
```

`VerifyWebhook` accepts several `v1=` signatures (secret rotation), compares in
constant time and rejects timestamps more than 300 s (pass a tolerance to change
it) from `now`. Events: `pairing.created`, `pairing.revoked`, `message.delivered`,
`message.undeliverable`, `message.acked`, `message.dismissed`, `message.expired`,
`action`, `activity.ended`; `Event.Data()` returns the matching typed payload.

## Errors

```go
_, err := c.SendMessage(ctx, msg)
var apiErr *hansho.APIError
if errors.As(err, &apiErr) {
    // apiErr.Status, .Code ("unknown_channel", "invalid_request", ...), .Field ("title"), .RetryAfter
}
var netErr *hansho.NetworkError // no answer at all: connection, timeout, ctx cancelled
if errors.As(err, &netErr) && errors.Is(err, context.DeadlineExceeded) { ... }
```

## Retries

`GET`, `PUT` and `DELETE` are retried on 429 and 503, up to `WithMaxRetries`
times, waiting for `Retry-After` (a wait over 60 s is returned to you as the
error) or an exponential backoff with jitter. `SendMessage` is never retried
unless `Message.IdempotencyKey` is set; the hub does not deduplicate on it yet,
so a retried send is at-least-once. Activity updates (`PATCH`) are not retried.
Cancelling the context aborts the request and any wait.

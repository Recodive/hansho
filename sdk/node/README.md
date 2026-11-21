# @recodive/hansho

Node.js / TypeScript client for a Hansho hub. Node 22 or newer, no runtime
dependencies (global `fetch`, `node:crypto`). ESM and CommonJS builds with types.

```sh
npm install @recodive/hansho
```

See [../README.md](../README.md) for the model shared with the Go SDK.

## Quick start

```ts
import { Hansho } from "@recodive/hansho";

const hansho = new Hansho({ baseUrl: "https://hub.example.com", apiKey: process.env.HANSHO_API_KEY! });

await hansho.putChannel("ops", { name: "Operations", defaultLevel: "active", maxLevel: "critical" });

const res = await hansho.sendMessage({
  externalUserId: "user-42",
  channel: "ops",
  title: "Disk full",
  body: "db-1 is at 97%",
});
console.log(res.messageId, res.status); // msg_... queued
```

Options: `fetch` (custom fetch), `maxRetries` (default 3, 0 disables), `timeoutMs`
(default 30000), `userAgent`. Every method also takes `{ signal }` as its last
argument to abort the request and any retry wait.

`level` is a union (`"passive" | "active" | "time-sensitive" | "critical" | "page"`);
`level: "pager"` does not compile.

## Pairing

```ts
const ticket = await hansho.createPairingTicket("user-42");
// Render ticket.qr as a QR code; the user scans it in the Hansho app.
// ticket.expiresAt is 10 minutes out; the ticket works once.
// The hub then sends a pairing.created webhook.

await hansho.unpair("user-42"); // revokes every pairing of the user
await hansho.kick("user-42"); // the same call; the app tells the person that you ended it
```

## Audiences and groups

A channel or feed is open to everyone unless it has an `audience`: `"only"` the listed users and groups, or `"except"`
them. Users are your external user ids (they may not have paired yet); groups are named lists you keep on the hub. The hub
decides who has access and checks it on every request, so a change applies at once.

```ts
await hansho.putGroup("oncall", ["alice", "bob"]); // replaces the whole member list
await hansho.putChannel("pager", {
  name: "Pager",
  defaultLevel: "page",
  maxLevel: "page",
  audience: { mode: "only", groups: ["oncall"] },
});
// putChannel replaces the channel, except that leaving audience out keeps the stored one ({ mode: 'everyone' } opens it up).

// A feed uses its channel's audience unless it sets its own. Send both with every update.
await hansho.putFeed("queue", { templateId: "status", channel: "pager", data: { value: 3 } });
const settings = await hansho.getFeed("queue"); // settings without the data

// Send to several users, or to everyone in the channel's audience. People outside it are not delivered to.
const res = await hansho.sendMessage({ toAudience: true, channel: "pager", title: "DB down", body: "..." });
for (const r of res.recipients ?? []) {
  if (r.status === RECIPIENT_NO_ACCESS) {
    /* only with externalUserId(s): not in the audience, no message was created */
  }
}
```

Error codes in `ERROR_CODES`: `noAccess` (403, `startActivity` for a user outside the channel's audience) and
`unknownGroup` (404).

## Sending

```ts
// A notification. The level defaults to the channel's default and is capped at its max.
await hansho.sendMessage({
  externalUserId: "user-42", channel: "ops", level: "time-sensitive",
  title: "Deploy failed", body: "web-3 rolled back",
  actions: [{ id: "retry", title: "Retry" }],
  collapseId: "deploy-web", // a later message with the same id replaces this one
});

// A page repeats every intervalSeconds until someone acknowledges it on any
// device, then the hub sends message.acked. Defaults: 120 s, 5 resends.
const page = await hansho.sendMessage({
  externalUserId: "user-42", channel: "ops", level: "page",
  title: "Primary DB down", body: "Failover did not complete",
  page: { intervalSeconds: 60, maxRetries: 10 },
});
const status = await hansho.getMessage(page.messageId); // queued, delivered, undeliverable, paging, acked, expired

// A templated message; data is sent exactly as given (its keys are never renamed).
await hansho.putTemplate("status", 1, { type: "vstack", children: [{ type: "text", style: "headline", text: "{{title}}" }] });
await hansho.sendMessage({ externalUserId: "user-42", channel: "ops", templateId: "status", title: "Uptime", body: "99.98%", data: { title: "Uptime" } });

// A Live Activity.
const act = await hansho.startActivity({ externalUserId: "user-42", channel: "ops", templateId: "deploy", data: { progress: 0.1 } });
await hansho.updateActivity(act.activityId, { data: { progress: 0.6 }, priority: 10 });
await hansho.endActivity(act.activityId);

// A widget feed; omit externalUserId for one shared by all paired users. The title is the name the app's feed
// picker shows (it lists the feeds published for that person plus the shared ones).
await hansho.putFeed("uptime", { externalUserId: "user-42", templateId: "status", title: "Uptime", data: { title: "up" } });

// A sound (audio/x-caf, audio/wav or audio/aiff, at most 1 MB).
await hansho.putSound("ding", "audio/wav", wavBytes);
```

Ids may contain any characters except `.` and `..`: they are path-escaped.

## Webhooks

Set `HUB_WEBHOOK_URL` and `HUB_WEBHOOK_SECRET` on the hub. Verify the **raw**
request body: a body a framework parsed and you re-serialised will not match its
signature, and `verifyWebhook` refuses a parsed object (it is also a compile error).

Express: use `express.raw` on the webhook route, before any `express.json()`.

```ts
import express from "express";
import { verifyWebhook, WebhookError } from "@recodive/hansho";

app.post("/hansho-hook", express.raw({ type: "*/*" }), (req, res) => {
  try {
    const event = verifyWebhook({
      secret: process.env.HANSHO_WEBHOOK_SECRET!,
      header: req.get("X-Hansho-Webhook-Signature")!,
      body: req.body, // a Buffer
    });
    switch (event.type) {
      case "message.acked": console.log("acknowledged:", event.data.messageId); break;
      case "action": console.log("button", event.data.actionId, "on", event.data.messageId); break;
      case "unknown": console.log("newer hub event:", event.name); break;
    }
    res.sendStatus(200);
  } catch (e) {
    res.sendStatus(e instanceof WebhookError ? 400 : 500); // non-2xx: the hub retries
  }
});
```

Plain `node:http`:

```ts
http.createServer((req, res) => {
  const chunks: Buffer[] = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => {
    try {
      const event = verifyWebhook({ secret, header: req.headers["x-hansho-webhook-signature"] as string, body: Buffer.concat(chunks) });
      res.writeHead(200).end();
    } catch { res.writeHead(400).end(); }
  });
});
```

`verifyWebhook` accepts several `v1=` signatures (secret rotation), compares in
constant time and rejects timestamps more than 300 s from now (`toleranceMs`
changes it; `now` is injectable). `WebhookError.code` is one of `malformed_header`,
`bad_signature`, `stale` (a valid signature, replayed too late), `malformed_body`,
`invalid_body`, `invalid_secret`. Events: `pairing.created`, `pairing.revoked`,
`message.delivered`, `message.undeliverable`, `message.acked`, `message.dismissed`, `message.expired`,
`action`, `activity.ended`, with camelCase `data`; anything else is `type: "unknown"`
with the parsed payload in `data`.

## Errors

```ts
import { HanshoError, HanshoNetworkError } from "@recodive/hansho";

try {
  await hansho.sendMessage(msg);
} catch (e) {
  if (e instanceof HanshoError) {
    // e.status, e.code ("unknown_channel", "invalid_request", ...), e.field ("title"), e.retryAfterMs
  } else if (e instanceof HanshoNetworkError) {
    // no answer at all: connection, timeout or abort; see e.cause
  }
}
```

## Retries

`GET`, `PUT` and `DELETE` are retried on 429 and 503, up to `maxRetries` times,
waiting for `Retry-After` (a wait over 60 s is thrown to you as the error) or an
exponential backoff with jitter. `sendMessage` is never retried unless the
message has an `idempotencyKey`; the hub does not deduplicate on it yet, so a
retried send is at-least-once. Activity updates (`PATCH`) are not retried.

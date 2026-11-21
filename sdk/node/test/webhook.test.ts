import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import http from "node:http";
import type { AddressInfo } from "node:net";
import { describe, it } from "node:test";
import { type HanshoEvent, verifyWebhook, WEBHOOK_SIGNATURE_HEADER, WebhookError } from "../src/index.ts";
import { loadJson } from "./helpers.ts";

interface Vector {
  name: string;
  secret: string;
  header: string;
  body: string;
  now: number;
  expect: { ok: boolean; error?: string; type?: string; name?: string; id?: string; created_at?: number; data?: unknown };
}
const vectors = loadJson<{ tolerance_seconds: number; vectors: Vector[] }>("../../spec/webhook-vectors.json");

function sign(secret: string, atSeconds: number, body: string): string {
  const t = String(atSeconds);
  return `t=${t},v1=${createHmac("sha256", secret).update(`${t}.${body}`).digest("hex")}`;
}

describe("shared vectors (signed by the hub's own dispatcher)", () => {
  assert.ok(vectors.vectors.length >= 20);
  const classes = new Set<string>();
  for (const v of vectors.vectors) {
    it(v.name, () => {
      const run = () =>
        verifyWebhook({ secret: v.secret, header: v.header, body: v.body, now: v.now * 1000, toleranceMs: vectors.tolerance_seconds * 1000 });
      if (!v.expect.ok) {
        classes.add(v.expect.error!);
        assert.throws(run, (e: unknown) => e instanceof WebhookError && e.code === v.expect.error);
        return;
      }
      const ev = run();
      assert.equal(ev.type, v.expect.type);
      assert.equal(ev.id, v.expect.id);
      assert.equal(ev.createdAt.getTime(), v.expect.created_at! * 1000);
      if (ev.type === "unknown") {
        classes.add("unknown");
        assert.equal(ev.name, v.expect.name);
        assert.deepEqual(ev.data, v.expect.data);
      }
      assert.equal(Buffer.from(ev.raw).toString("utf8"), v.body);
      assert.deepEqual(JSON.parse(Buffer.from(ev.raw).toString("utf8")).data, v.expect.data);
    });
  }
  it("covers stale, bad signature, malformed header and unknown events", () => {
    for (const c of ["stale", "bad_signature", "malformed_header", "unknown"]) assert.ok(classes.has(c), c);
  });
});

// Review focus 2
describe("replay protection", () => {
  const body = '{"id":"1","event":"message.acked","created_at":1,"data":{}}';
  const t = 1_700_000_000;
  const header = sign("s", t, body);
  const check = (offsetS: number, toleranceMs?: number) => () =>
    verifyWebhook({ secret: "s", header, body, now: (t + offsetS) * 1000, ...(toleranceMs === undefined ? {} : { toleranceMs }) });

  it("accepts up to 300 s either side and rejects beyond", () => {
    for (const o of [0, 300, -300]) assert.doesNotThrow(check(o));
    for (const o of [301, -301, 86400]) assert.throws(check(o), (e: unknown) => e instanceof WebhookError && e.code === "stale");
  });
  it("tolerance is configurable", () => {
    assert.doesNotThrow(check(301, 600_000));
    assert.throws(check(11, 10_000), WebhookError);
    assert.doesNotThrow(check(10, 10_000));
  });
  it("accepts a Date for now and defaults to the clock", () => {
    assert.doesNotThrow(() => verifyWebhook({ secret: "s", header, body, now: new Date(t * 1000) }));
    const nowBody = body;
    const h = sign("s", Math.floor(Date.now() / 1000), nowBody);
    assert.doesNotThrow(() => verifyWebhook({ secret: "s", header: h, body: nowBody }));
    assert.throws(() => verifyWebhook({ secret: "s", header, body: nowBody }), (e: unknown) => e instanceof WebhookError && e.code === "stale");
  });
});

// Review focus 1
describe("raw body handling", () => {
  const sec = "whsec_sdk_vectors";
  const valid = vectors.vectors.find((v) => v.name === "valid message.acked")!;
  const now = valid.now * 1000;

  it("accepts a Buffer, a Uint8Array and a string", () => {
    for (const body of [Buffer.from(valid.body), new Uint8Array(Buffer.from(valid.body)), valid.body]) {
      assert.equal(verifyWebhook({ secret: sec, header: valid.header, body, now }).type, "message.acked");
    }
  });

  it("rejects a body that was parsed and re-serialised", () => {
    const parsed = JSON.parse(valid.body);
    for (const reserialised of [JSON.stringify(parsed, null, 2), JSON.stringify(parsed, null, 1), `${valid.body}\n`]) {
      assert.throws(
        () => verifyWebhook({ secret: sec, header: valid.header, body: reserialised, now }),
        (e: unknown) => e instanceof WebhookError && e.code === "bad_signature",
      );
    }
  });

  it("refuses a parsed object at run time (and at compile time, see types.test.ts)", () => {
    const parsed: unknown = JSON.parse(valid.body);
    for (const body of [parsed, null, undefined, 42, {}]) {
      assert.throws(
        // @ts-expect-error a parsed body is not a Buffer or string
        () => verifyWebhook({ secret: sec, header: valid.header, body, now }),
        (e: unknown) => e instanceof WebhookError && e.code === "invalid_body",
      );
    }
  });

  it("verifies the bytes it is given, including non-ASCII, from a node:http server", async () => {
    const seen: HanshoEvent[] = [];
    const server = http.createServer((req, res) => {
      const chunks: Buffer[] = [];
      req.on("data", (c: Buffer) => chunks.push(c));
      req.on("end", () => {
        try {
          seen.push(verifyWebhook({ secret: sec, header: req.headers[WEBHOOK_SIGNATURE_HEADER.toLowerCase()] as string, body: Buffer.concat(chunks), now }));
          res.writeHead(200).end();
        } catch (e) {
          res.writeHead(e instanceof WebhookError ? 400 : 500).end();
        }
      });
    });
    await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
    const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}/`;
    try {
      const post = (body: string) => fetch(url, { method: "POST", headers: { [WEBHOOK_SIGNATURE_HEADER]: valid.header }, body });
      assert.equal((await post(valid.body)).status, 200);
      assert.equal((await post(JSON.stringify(JSON.parse(valid.body), null, 2))).status, 400);
      const pairing = vectors.vectors.find((v) => v.name === "valid pairing.created")!;
      const r = await fetch(url, { method: "POST", headers: { [WEBHOOK_SIGNATURE_HEADER]: pairing.header }, body: pairing.body });
      assert.equal(r.status, 200); // non-ASCII body, verified byte for byte
    } finally {
      server.closeAllConnections();
      server.close();
    }
    assert.deepEqual(seen.map((e) => e.type), ["message.acked", "pairing.created"]);
  });

  it("exposes the header name", () => assert.equal(WEBHOOK_SIGNATURE_HEADER, "X-Hansho-Webhook-Signature"));
});

describe("secret and header validation", () => {
  const body = '{"id":"1","event":"action","created_at":1,"data":{}}';
  const t = Math.floor(Date.now() / 1000);

  it("accepts a Buffer secret and refuses an empty one", () => {
    assert.doesNotThrow(() => verifyWebhook({ secret: Buffer.from("s"), header: sign("s", t, body), body }));
    for (const secret of ["", new Uint8Array(0)]) {
      assert.throws(() => verifyWebhook({ secret, header: sign("", t, body), body }), (e: unknown) => e instanceof WebhookError && e.code === "invalid_secret");
    }
  });

  it("treats a missing header as malformed", () => {
    for (const header of [undefined, null, "", "   "]) {
      assert.throws(
        // @ts-expect-error header may be undefined at run time (absent request header)
        () => verifyWebhook({ secret: "s", header, body }),
        (e: unknown) => e instanceof WebhookError && e.code === "malformed_header",
      );
    }
  });

  it("rejects a valid signature over a body that is not a webhook envelope", () => {
    for (const bad of ["not json", "[]", '{"id":"1"}', ""]) {
      assert.throws(
        () => verifyWebhook({ secret: "s", header: sign("s", t, bad), body: bad }),
        (e: unknown) => e instanceof WebhookError && e.code === "malformed_body",
      );
    }
  });

  it("accepts upper-case hex", () => {
    const h = sign("s", t, body).replace(/v1=(.*)$/, (_m, hex: string) => `v1=${hex.toUpperCase()}`);
    assert.doesNotThrow(() => verifyWebhook({ secret: "s", header: h, body }));
  });
});

// Review focus 3
describe("forward compatibility", () => {
  const t = Math.floor(Date.now() / 1000);

  it("delivers an unknown event as 'unknown' with the raw payload", () => {
    const body = '{"id":7,"event":"message.holographic","created_at":5,"extra":1,"data":{"message_id":"m","new_field":[1,2]}}';
    const ev = verifyWebhook({ secret: "s", header: sign("s", t, body), body });
    assert.equal(ev.type, "unknown");
    if (ev.type === "unknown") {
      assert.equal(ev.name, "message.holographic");
      assert.deepEqual(ev.data, { message_id: "m", new_field: [1, 2] });
    }
    assert.equal(ev.id, "7");
  });

  it("ignores unknown fields on known events", () => {
    const body = '{"id":"8","event":"message.acked","created_at":5,"data":{"message_id":"m","device_id":"d","external_user_id":"u","from_the_future":true}}';
    const ev = verifyWebhook({ secret: "s", header: sign("s", t, body), body });
    assert.equal(ev.type, "message.acked");
    if (ev.type === "message.acked") assert.deepEqual(ev.data, { messageId: "m", externalUserId: "u", deviceId: "d" });
  });

  it("maps every known event to camelCase payloads", () => {
    const want: Record<string, unknown> = {
      "pairing.created": { pairingId: "pair_1", externalUserId: "user-42", deviceName: "Bas’s iPhone <Ünï>" },
      "pairing.revoked": { pairingId: "pair_1", externalUserId: "user-42", by: "sender" },
      "message.delivered": { messageId: "msg_1", externalUserId: "user-42", channel: "ops", level: "page", devices: 2 },
      "message.undeliverable": { messageId: "msg_2", externalUserId: "user-42", channel: "ops", reason: "no_devices" },
      "message.acked": { messageId: "msg_1", externalUserId: "user-42", deviceId: "dev_1" },
      "message.dismissed": { messageId: "msg_1", externalUserId: "user-42", deviceId: "dev_1" },
      "message.expired": { messageId: "msg_3", externalUserId: "user-42", channel: "ops", attempts: 6 },
      action: { messageId: "msg_1", actionId: "approve", deviceId: "dev_1", externalUserId: "user-42", text: "ship it" },
      "activity.ended": { activityId: "act_1", externalUserId: "user-42", channel: "ops", reason: "timeout" },
    };
    const seen = new Set<string>();
    for (const v of vectors.vectors) {
      if (!v.name.startsWith("valid ") || !v.expect.ok || v.expect.type === "unknown" || seen.has(v.expect.type!)) continue;
      seen.add(v.expect.type!);
      const ev = verifyWebhook({ secret: v.secret, header: v.header, body: v.body, now: v.now * 1000 });
      assert.deepEqual(ev.data, want[ev.type], ev.type);
    }
    assert.equal(seen.size, 9);
  });
});

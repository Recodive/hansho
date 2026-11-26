import assert from "node:assert/strict";
import http from "node:http";
import type { AddressInfo } from "node:net";
import { describe, it } from "node:test";
import { Hansho, HanshoError, HanshoNetworkError, NAME } from "../src/index.ts";
import { startHub, TEST_KEY } from "./helpers.ts";

const channel = { name: "Ops", defaultLevel: "active", maxLevel: "critical" } as const;
const msg = { externalUserId: "u", channel: "c", title: "t", body: "b" };

type Hub = Awaited<ReturnType<typeof startHub>>;

async function withHub<T>(fn: Parameters<typeof startHub>[0], run: (h: Hub) => Promise<T>): Promise<T> {
  const hub = await startHub(fn);
  try {
    return await run(hub);
  } finally {
    await hub.close();
  }
}

/** A server that accepts connections and never answers. */
async function silentServer() {
  const server = http.createServer(() => {});
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  return {
    url: `http://127.0.0.1:${(server.address() as AddressInfo).port}`,
    close: () => {
      server.closeAllConnections();
      server.close();
    },
  };
}

describe("basics", () => {
  it("sends headers and uses the default user agent", () =>
    withHub(undefined, async (h) => {
      await h.client.putChannel("ops", channel);
      const r = h.requests[0]!;
      assert.equal(r.headers.authorization, `Bearer ${TEST_KEY}`);
      assert.equal(r.headers.accept, "application/json");
      assert.equal(r.headers["content-type"], "application/json");
      assert.match(String(r.headers["user-agent"]), /^hansho-node\//);
      await h.newClient({ userAgent: "acme/2" }).endActivity("a");
      assert.equal(h.requests[1]!.headers["user-agent"], "acme/2");
      assert.equal(h.requests[1]!.headers["content-type"], undefined);
    }));

  it("handles a trailing slash and a path prefix in baseUrl", () =>
    withHub(undefined, async (h) => {
      await new Hansho({ baseUrl: `${h.url}/hansho/`, apiKey: TEST_KEY }).getMessage("m1");
      assert.equal(h.requests[0]!.url, "/hansho/api/v1/messages/m1");
    }));

  it("sets and clears a display name, escaping the id", () =>
    withHub(undefined, async (h) => {
      await h.client.setUserDisplayName("a/b c", "Ada");
      await h.client.setUserDisplayName("a/b c", "");
      assert.equal(h.requests[0]!.method, "PUT");
      assert.equal(h.requests[0]!.url, "/api/v1/users/a%2Fb%20c");
      assert.deepEqual(JSON.parse(h.requests[0]!.body.toString()), { display_name: "Ada" });
      assert.deepEqual(JSON.parse(h.requests[1]!.body.toString()), { display_name: "" });
      await assert.rejects(h.client.setUserDisplayName("", "x"), RangeError);
    }));

  it("omits display_name from a pairing ticket unless given", () =>
    withHub(
      (_r, reply) => {
        reply.status = 201;
        reply.body = '{"ticket":"t","qr":"q","expires_at":"2026-11-08T12:10:00Z"}';
      },
      async (h) => {
        await h.client.createPairingTicket("u");
        await h.client.createPairingTicket("u", { displayName: "Ada" });
        assert.deepEqual(JSON.parse(h.requests[0]!.body.toString()), { external_user_id: "u" });
        assert.deepEqual(JSON.parse(h.requests[1]!.body.toString()), { external_user_id: "u", display_name: "Ada" });
      },
    ));

  it("exports the codename constant", () => assert.equal(NAME, "hansho"));

  it("requires baseUrl and apiKey", () => {
    assert.throws(() => new Hansho({ baseUrl: "", apiKey: "k" }), TypeError);
    assert.throws(() => new Hansho({ baseUrl: "http://x", apiKey: "" }), TypeError);
  });

  it("does not expose the key when the client is serialised or inspected", async () => {
    const { inspect } = await import("node:util");
    const c = new Hansho({ baseUrl: "http://x", apiKey: TEST_KEY });
    assert.ok(!JSON.stringify(c).includes(TEST_KEY));
    assert.ok(!inspect(c, { showHidden: true, depth: 5 }).includes(TEST_KEY));
    assert.ok(!String(c).includes(TEST_KEY));
  });

  it("omits unset optional fields from the wire", () =>
    withHub(undefined, async (h) => {
      await h.client.sendMessage(msg);
      assert.deepEqual(JSON.parse(h.requests[0]!.body.toString()), { external_user_id: "u", channel: "c", title: "t", body: "b" });
      await h.client.putChannel("ops", channel);
      assert.deepEqual(JSON.parse(h.requests[1]!.body.toString()), { name: "Ops", default_level: "active", max_level: "critical" });
    }));

  it("sends maxRetries 0 as given", () =>
    withHub(undefined, async (h) => {
      await h.client.sendMessage({ ...msg, level: "page", page: { maxRetries: 0 } });
      assert.deepEqual(JSON.parse(h.requests[0]!.body.toString()).page, { max_retries: 0 });
    }));
});

// Review focus 3
describe("forward compatibility", () => {
  it("ignores response fields it does not know", () =>
    withHub(
      (_r, reply) => {
        reply.body =
          '{"message_id":"m","status":"delivered","level":"page","attempts":1,"deliveries":[{"device_id":"d","status":"sent","brand_new":{"x":[1]}}],"brand_new":"v","acked_at":"2026-11-08T12:05:00Z"}';
      },
      async (h) => {
        const ms = await h.client.getMessage("m");
        assert.equal(ms.messageId, "m");
        assert.equal(ms.deliveries.length, 1);
        assert.ok(!("brandNew" in ms) && !("brand_new" in ms));
      },
    ));
});

// Review focus 4
describe("retries", () => {
  it("retries PUT on 429 and honours Retry-After", () => {
    let n = 0;
    return withHub(
      (_r, reply) => {
        if (++n <= 2) Object.assign(reply, { status: 429, headers: { "retry-after": "2" }, body: '{"error":"rate_limited"}' });
      },
      async (h) => {
        await h.client.putChannel("ops", channel);
        assert.equal(h.requests.length, 3);
        assert.deepEqual(h.waits, [2000, 2000]);
      },
    );
  });

  it("understands an HTTP-date Retry-After", () => {
    let n = 0;
    return withHub(
      (_r, reply) => {
        if (++n === 1) Object.assign(reply, { status: 429, headers: { "retry-after": new Date(Date.now() + 30_000).toUTCString() } });
      },
      async (h) => {
        await h.client.deleteChannel("ops");
        assert.equal(h.waits.length, 1);
        assert.ok(h.waits[0]! >= 20_000 && h.waits[0]! <= 31_000, String(h.waits[0]));
      },
    );
  });

  it("stops after maxRetries and surfaces the error", () =>
    withHub(
      (_r, reply) => Object.assign(reply, { status: 429, headers: { "retry-after": "1" }, body: '{"error":"rate_limited"}' }),
      async (h) => {
        await assert.rejects(h.client.putChannel("ops", channel), (e: unknown) => e instanceof HanshoError && e.status === 429 && e.retryAfterMs === 1000);
        assert.equal(h.requests.length, 4);
        await assert.rejects(h.newClient({ maxRetries: 0 }).putChannel("ops", channel));
        assert.equal(h.requests.length, 5);
        await assert.rejects(h.newClient({ maxRetries: 1 }).putChannel("ops", channel));
        assert.equal(h.requests.length, 7);
      },
    ));

  it("retries GET, DELETE on 503 with backoff", () => {
    const seen = new Set<string>();
    return withHub(
      (r, reply) => {
        const key = `${r.method} ${r.url}`;
        if (!seen.has(key)) {
          seen.add(key);
          Object.assign(reply, { status: 503, body: '{"error":"unavailable"}' });
        }
      },
      async (h) => {
        await h.client.getChannel("x");
        await h.client.deleteChannel("x");
        await h.client.endActivity("x");
        assert.equal(h.waits.length, 3);
        for (const w of h.waits) assert.ok(w >= 125 && w <= 250, String(w));
      },
    );
  });

  it("does not follow redirects", async () => {
    await withHub(undefined, async (target) => {
      await withHub(
        (_r, reply) => Object.assign(reply, { status: 307, headers: { location: target.url + "/elsewhere" } }),
        async (h) => {
          await assert.rejects(h.client.putChannel("ops", channel), (e: unknown) => e instanceof HanshoError && e.status === 307);
          assert.equal(target.requests.length, 0);
        },
      );
    });
  });

  it("does not retry statuses other than 429/503", async () => {
    for (const status of [400, 401, 404, 409, 500, 502]) {
      await withHub(
        (_r, reply) => {
          reply.status = status;
        },
        async (h) => {
          await assert.rejects(h.client.putChannel("ops", channel));
          assert.equal(h.requests.length, 1, `status ${status}`);
        },
      );
    }
  });

  // Review focus 4
  it("surfaces 429/503 on POST /messages untouched", async () => {
    for (const status of [429, 503]) {
      await withHub(
        (_r, reply) => Object.assign(reply, { status, headers: { "retry-after": "3" }, body: '{"error":"rate_limited"}' }),
        async (h) => {
          await assert.rejects(h.client.sendMessage(msg), (e: unknown) => {
            assert.ok(e instanceof HanshoError);
            assert.equal(e.status, status);
            assert.equal(e.code, "rate_limited");
            assert.equal(e.retryAfterMs, 3000);
            return true;
          });
          assert.equal(h.requests.length, 1);
          assert.equal(h.waits.length, 0);
        },
      );
    }
  });

  it("retries a message that carries an idempotencyKey, without leaking it into the body", () => {
    let n = 0;
    return withHub(
      (_r, reply) => {
        if (++n === 1) Object.assign(reply, { status: 429, headers: { "retry-after": "1" } });
        else Object.assign(reply, { status: 202, body: '{"message_id":"m1","status":"queued"}' });
      },
      async (h) => {
        const res = await h.client.sendMessage({ ...msg, idempotencyKey: "order-7" });
        assert.equal(res.messageId, "m1");
        assert.equal(h.requests.length, 2);
        for (const r of h.requests) {
          assert.equal(r.headers["idempotency-key"], "order-7");
          assert.ok(!r.body.toString().includes("order-7"));
        }
        assert.deepEqual(h.requests[0]!.body, h.requests[1]!.body);
      },
    );
  });

  it("sends no Idempotency-Key header by default", () =>
    withHub(undefined, async (h) => {
      await h.client.sendMessage(msg);
      assert.equal(h.requests[0]!.headers["idempotency-key"], undefined);
    }));

  it("does not retry PATCH", () =>
    withHub(
      (_r, reply) => {
        reply.status = 429;
      },
      async (h) => {
        await assert.rejects(h.client.updateActivity("a", { data: 1 }), HanshoError);
        assert.equal(h.requests.length, 1);
      },
    ));

  it("surfaces a Retry-After beyond the cap", () =>
    withHub(
      (_r, reply) => Object.assign(reply, { status: 429, headers: { "retry-after": "3600" } }),
      async (h) => {
        await assert.rejects(h.client.putChannel("ops", channel), (e: unknown) => e instanceof HanshoError && e.retryAfterMs === 3_600_000);
        assert.equal(h.requests.length, 1);
      },
    ));
});

// Review focus 5
describe("path escaping", () => {
  const ids = ["a/b", "a?b=c", "a b", "ü", "日本", "a#b", "100%", "a/../b", "a%2Fb"];
  for (const id of ids) {
    it(`escapes ${JSON.stringify(id)}`, () =>
      withHub(undefined, async (h) => {
        const c = h.client;
        const cases: Array<[string, () => Promise<unknown>]> = [
          ["/api/v1/channels/", () => c.putChannel(id, channel)],
          ["/api/v1/channels/", () => c.getChannel(id)],
          ["/api/v1/channels/", () => c.deleteChannel(id)],
          ["/api/v1/templates/", () => c.putTemplate(id, 1, {})],
          ["/api/v1/sounds/", () => c.putSound(id, "audio/wav", new Uint8Array([1]))],
          ["/api/v1/feeds/", () => c.putFeed(id, { templateId: "t", data: 1 })],
          ["/api/v1/messages/", () => c.getMessage(id)],
          ["/api/v1/activities/", () => c.updateActivity(id, { data: 1 })],
          ["/api/v1/activities/", () => c.endActivity(id)],
        ];
        for (const [prefix, call] of cases) {
          const before = h.requests.length;
          await call();
          const target = h.requests[before]!.url;
          assert.ok(target.startsWith(prefix), target);
          const seg = target.slice(prefix.length);
          assert.ok(!/[/?# ]/.test(seg), `unescaped segment in ${target}`);
          assert.equal(decodeURIComponent(seg), id);
        }
        const before = h.requests.length;
        await c.unpair(id);
        const target = h.requests[before]!.url;
        const m = /^\/api\/v1\/users\/([^/?#]+)\/pairings$/.exec(target);
        assert.ok(m, target);
        assert.equal(decodeURIComponent(m[1]!), id);
      }));
  }

  it("produces the exact expected encoding", () =>
    withHub(undefined, async (h) => {
      await h.client.putChannel("ü x/y?z", channel);
      assert.equal(h.requests[0]!.url, "/api/v1/channels/%C3%BC%20x%2Fy%3Fz");
    }));

  it("rejects dot segments and empty ids before sending", () =>
    withHub(undefined, async (h) => {
      const c = h.client;
      for (const bad of ["", ".", ".."]) {
        await assert.rejects(c.getChannel(bad), RangeError, JSON.stringify(bad));
        await assert.rejects(c.unpair(bad), RangeError);
        await assert.rejects(c.endActivity(bad), RangeError);
      }
      await assert.rejects(c.createPairingTicket(""), RangeError);
      assert.equal(h.requests.length, 0);
    }));
});

describe("cancellation and errors", () => {
  it("aborts a request through the signal", async () => {
    const s = await silentServer();
    try {
      const c = new Hansho({ baseUrl: s.url, apiKey: TEST_KEY });
      const ac = new AbortController();
      setTimeout(() => ac.abort(), 50);
      const start = Date.now();
      await assert.rejects(c.sendMessage(msg, { signal: ac.signal }), (e: unknown) => e instanceof HanshoNetworkError);
      assert.ok(Date.now() - start < 3000);
      await assert.rejects(c.getChannel("x", { signal: AbortSignal.abort() }), HanshoNetworkError);
    } finally {
      s.close();
    }
  });

  it("times out", async () => {
    const s = await silentServer();
    try {
      const c = new Hansho({ baseUrl: s.url, apiKey: TEST_KEY, timeoutMs: 80 });
      await assert.rejects(c.sendMessage(msg), HanshoNetworkError);
    } finally {
      s.close();
    }
  });

  it("aborts during the retry wait", () =>
    withHub(
      (_r, reply) => Object.assign(reply, { status: 429, headers: { "retry-after": "30" } }),
      async (h) => {
        const c = new Hansho({ baseUrl: h.url, apiKey: TEST_KEY }); // real sleep
        const start = Date.now();
        await assert.rejects(c.putChannel("ops", channel, { signal: AbortSignal.timeout(100) }), HanshoNetworkError);
        assert.ok(Date.now() - start < 3000);
      },
    ));

  it("reports network errors apart from API errors, without the key", async () => {
    const hub = await startHub();
    const url = hub.url;
    await hub.close();
    const c = new Hansho({ baseUrl: url, apiKey: TEST_KEY });
    await assert.rejects(c.putChannel("ops", channel), (e: unknown) => {
      assert.ok(e instanceof HanshoNetworkError);
      assert.ok(!(e instanceof HanshoError));
      assert.ok(!e.message.includes(TEST_KEY));
      assert.ok(e.cause !== undefined);
      return true;
    });
  });

  it("never lets the key into errors", () =>
    withHub(
      (r, reply) => {
        Object.assign(reply, {
          status: 401,
          body: JSON.stringify({ error: "unauthorized", message: `bad key ${r.headers.authorization}` }),
        });
      },
      async (h) => {
        await assert.rejects(h.client.putChannel("ops", channel), (e: unknown) => {
          assert.ok(e instanceof HanshoError);
          assert.ok(!e.message.includes(TEST_KEY));
          assert.ok(!String(e.detail).includes(TEST_KEY));
          assert.ok(!JSON.stringify(e).includes(TEST_KEY));
          return true;
        });
      },
    ));

  it("formats HanshoError messages", () => {
    const e = new HanshoError({ method: "POST", path: "/api/v1/messages", status: 400, code: "invalid_request", field: "title" });
    assert.equal(e.message, "hansho: POST /api/v1/messages: 400 invalid_request (field title)");
    assert.equal(e.name, "HanshoError");
    assert.ok(e instanceof Error);
  });
});

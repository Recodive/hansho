import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { Audience } from "../src/index.ts";
import { ERROR_CODES, HanshoError, RECIPIENT_NO_ACCESS } from "../src/index.ts";
import { startHub } from "./helpers.ts";

type Hub = Awaited<ReturnType<typeof startHub>>;

async function withHub<T>(fn: Parameters<typeof startHub>[0], run: (h: Hub) => Promise<T>): Promise<T> {
  const hub = await startHub(fn);
  try {
    return await run(hub);
  } finally {
    await hub.close();
  }
}

const body = (h: Hub, i: number) => JSON.parse(h.requests[i]!.body.toString("utf8")) as unknown;

describe("groups", () => {
  it("replaces the whole member list", () =>
    withHub(
      (_r, reply) => void (reply.body = '{"id":"oncall","members":["alice","bob"]}'),
      async (h) => {
        const g = await h.client.putGroup("oncall", ["bob", "alice"]);
        const r = h.requests[0]!;
        assert.equal(r.method, "PUT");
        assert.equal(r.url, "/api/v1/groups/oncall");
        assert.deepEqual(body(h, 0), { members: ["bob", "alice"] });
        assert.deepEqual(g, { id: "oncall", members: ["alice", "bob"] });
        await h.client.putGroup("oncall", []);
        assert.deepEqual(body(h, 1), { members: [] });
      },
    ));

  it("gets and deletes, escaping the id", () =>
    withHub(
      (r, reply) => {
        if (r.method === "GET") reply.body = '{"id":"a b","members":[]}';
        else reply.status = 204;
      },
      async (h) => {
        assert.deepEqual(await h.client.getGroup("a b"), { id: "a b", members: [] });
        await h.client.deleteGroup("a b");
        assert.deepEqual(
          h.requests.map((r) => [r.method, r.url]),
          [
            ["GET", "/api/v1/groups/a%20b"],
            ["DELETE", "/api/v1/groups/a%20b"],
          ],
        );
        await assert.rejects(() => h.client.putGroup("", []), RangeError);
        await assert.rejects(() => h.client.getGroup(".."), RangeError);
        assert.equal(h.requests.length, 2);
      },
    ));

  it("reports a missing group as unknown_group", () =>
    withHub(
      (_r, reply) => {
        reply.status = 404;
        reply.body = '{"error":"unknown_group"}';
      },
      async (h) => {
        await assert.rejects(
          () => h.client.getGroup("gone"),
          (e: unknown) => e instanceof HanshoError && e.status === 404 && e.code === ERROR_CODES.unknownGroup,
        );
      },
    ));
});

describe("audiences", () => {
  const audience: Audience = { mode: "only", users: ["alice"], groups: ["oncall"] };

  it("puts and reads a channel audience", () =>
    withHub(
      (r, reply) => {
        if (r.method === "GET") {
          reply.body = '{"name":"Ops","default_level":"active","max_level":"page","audience":{"mode":"except","users":["bob"],"groups":[]}}';
        }
      },
      async (h) => {
        await h.client.putChannel("ops", { name: "Ops", defaultLevel: "active", maxLevel: "page", audience });
        assert.deepEqual(body(h, 0), {
          name: "Ops",
          default_level: "active",
          max_level: "page",
          audience: { mode: "only", users: ["alice"], groups: ["oncall"] },
        });
        const ch = await h.client.getChannel("ops");
        assert.deepEqual(ch.audience, { mode: "except", users: ["bob"], groups: [] });
        // Without one nothing is sent: the hub reads that as everyone.
        await h.client.putChannel("ops", { name: "Ops", defaultLevel: "active", maxLevel: "page" });
        assert.equal("audience" in (body(h, 2) as object), false);
      },
    ));

  it("sends a feed's channel and audience, and reads its settings", () =>
    withHub(
      (r, reply) => {
        if (r.method === "GET") {
          reply.body = '{"feed_id":"own","external_user_id":"a&b","template_id":"status","channel":"ops","updated_at":7}';
        } else reply.status = 202;
      },
      async (h) => {
        await h.client.putFeed("uptime", { templateId: "status", channel: "ops", audience: { ...audience }, data: 1 });
        assert.deepEqual(body(h, 0), {
          template_id: "status",
          channel: "ops",
          audience: { mode: "only", users: ["alice"], groups: ["oncall"] },
          data: 1,
        });
        const f = await h.client.getFeed("own", "a&b");
        assert.equal(h.requests[1]!.url, "/api/v1/feeds/own?external_user_id=a%26b");
        // Inheriting its channel's audience: none of its own.
        assert.deepEqual(f, { feedId: "own", externalUserId: "a&b", templateId: "status", channel: "ops", updatedAt: 7 });
        await h.client.getFeed("own");
        assert.equal(h.requests[2]!.url, "/api/v1/feeds/own");
      },
    ));
});

describe("send targets and results", () => {
  it("sends to several users and lists each recipient", () =>
    withHub(
      (_r, reply) => {
        reply.status = 202;
        reply.body = JSON.stringify({
          level: "active",
          recipients: [
            { external_user_id: "alice", message_id: "msg_1", status: "queued", devices: 1, deliveries: [{ device_id: "dev_1", status: "queued" }] },
            { external_user_id: "carol", status: "no_access" },
          ],
        });
      },
      async (h) => {
        const res = await h.client.sendMessage({ externalUserIds: ["alice", "carol"], channel: "ops", title: "t", body: "b" });
        assert.deepEqual(body(h, 0), { external_user_ids: ["alice", "carol"], channel: "ops", title: "t", body: "b" });
        assert.deepEqual(res, {
          level: "active",
          recipients: [
            { externalUserId: "alice", messageId: "msg_1", status: "queued", devices: 1, deliveries: [{ deviceId: "dev_1", status: "queued" }] },
            { externalUserId: "carol", status: RECIPIENT_NO_ACCESS },
          ],
        });
      },
    ));

  it("sends to an audience", () =>
    withHub(undefined, async (h) => {
      await h.client.sendMessage({ toAudience: true, channel: "ops", title: "t", body: "b" });
      assert.deepEqual(body(h, 0), { to_audience: true, channel: "ops", title: "t", body: "b" });
    }));

  it("keeps the single-user answer, with no message for a user without access", () =>
    withHub(
      (_r, reply) => {
        reply.status = 202;
        reply.body = '{"level":"active","status":"no_access","devices":0,"deliveries":[],"recipients":[{"external_user_id":"carol","status":"no_access"}]}';
      },
      async (h) => {
        const res = await h.client.sendMessage({ externalUserId: "carol", channel: "ops", title: "t", body: "b" });
        assert.equal(res.messageId, undefined);
        assert.equal(res.status, RECIPIENT_NO_ACCESS);
        assert.equal(res.devices, 0);
        assert.deepEqual(res.recipients, [{ externalUserId: "carol", status: "no_access" }]);
      },
    ));
});

describe("kick and no_access", () => {
  it("kick is an unpair", () =>
    withHub(undefined, async (h) => {
      await h.client.kick("user 42");
      assert.equal(h.requests[0]!.method, "DELETE");
      assert.equal(h.requests[0]!.url, "/api/v1/users/user%2042/pairings");
      await assert.rejects(() => h.client.kick(""), RangeError);
    }));

  it("reports no_access as a HanshoError", () =>
    withHub(
      (_r, reply) => {
        reply.status = 403;
        reply.body = '{"error":"no_access"}';
      },
      async (h) => {
        await assert.rejects(
          () => h.client.startActivity({ externalUserId: "u", channel: "c", templateId: "t", data: 1 }),
          (e: unknown) => e instanceof HanshoError && e.status === 403 && e.code === ERROR_CODES.noAccess,
        );
      },
    ));
});

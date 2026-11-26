import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { Hansho } from "../src/index.ts";
import { HanshoError } from "../src/index.ts";
import { loadSpec, startHub, TEST_KEY } from "./helpers.ts";

const wav = Buffer.from("UklGRgAAAABXQVZF", "base64");

const specChannel = {
  name: "Operations",
  defaultLevel: "active",
  maxLevel: "critical",
  sound: "ding",
  templateId: "status",
  thread: "ops-thread",
  actions: [
    { id: "ack", title: "Acknowledge", ack: true },
    { id: "reply", title: "Reply", kind: "text" },
  ],
} as const;

/** Every operation of the shared spec: the SDK call and the value it must produce. */
const calls: Record<string, { call: (c: Hansho) => Promise<unknown>; want: unknown }> = {
  createPairingTicket: {
    call: (c) => c.createPairingTicket("user-42"),
    want: {
      ticket: "tk_abc",
      qr: "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=tk_abc&fp=ff00",
      expiresAt: new Date("2026-11-08T12:10:00Z"),
    },
  },
  createPairingTicketWithName: {
    call: (c) => c.createPairingTicket("user-42", { displayName: "Zoë 日本語" }),
    want: {
      ticket: "tk_def",
      qr: "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=tk_def&fp=ff00",
      expiresAt: new Date("2026-11-08T12:10:00Z"),
    },
  },
  setUserDisplayName: { call: (c) => c.setUserDisplayName("user-42", "Zoë 日本語"), want: undefined },
  clearUserDisplayName: { call: (c) => c.setUserDisplayName("user 42", ""), want: undefined },
  unpair: { call: (c) => c.unpair("user-42"), want: undefined },
  putChannel: { call: (c) => c.putChannel("ops", { ...specChannel, actions: [...specChannel.actions] }), want: undefined },
  getChannel: {
    call: (c) => c.getChannel("ops"),
    want: { name: "Operations", defaultLevel: "active", maxLevel: "critical", sound: "", templateId: "", thread: "ops", actions: [] },
  },
  deleteChannel: { call: (c) => c.deleteChannel("ops"), want: undefined },
  putTemplate: {
    call: (c) => c.putTemplate("status", 2, { type: "vstack", children: [{ type: "text", style: "headline", text: "{{title}}" }] }),
    want: undefined,
  },
  putSound: {
    call: (c) => c.putSound("ding", "audio/wav", wav),
    want: { id: "ding", filename: "ding.wav", sha256: "00", size: 12 },
  },
  sendMessage: {
    call: (c) =>
      c.sendMessage({
        externalUserId: "user-42",
        channel: "ops",
        level: "time-sensitive",
        title: "Disk full",
        subtitle: "db-1",
        body: "db-1 is at 97%",
        imageUrl: "https://example.com/i.png",
        templateId: "status",
        data: { disk: 97, nested: { snake_case_key: [1, 2] } },
        sound: "ding",
        actions: [{ id: "ack", title: "Ack", ack: true }],
        collapseId: "disk-db-1",
      }),
    want: {
      messageId: "msg_1",
      level: "time-sensitive",
      devices: 2,
      status: "queued",
      deliveries: [
        { deviceId: "dev_1", status: "queued" },
        { deviceId: "dev_2", status: "queued" },
      ],
    },
  },
  sendPage: {
    call: (c) =>
      c.sendMessage({
        externalUserId: "user-42",
        channel: "ops",
        level: "page",
        title: "Primary DB down",
        body: "Failover did not complete",
        page: { intervalSeconds: 60, maxRetries: 3 },
      }),
    want: { messageId: "msg_2", level: "page", devices: 1, status: "queued", deliveries: [{ deviceId: "dev_1", status: "queued" }] },
  },
  sendPageEach: {
    call: (c) =>
      c.sendMessage({
        externalUserIds: ["alice", "bob"],
        channel: "ops",
        level: "page",
        title: "Primary DB down",
        body: "Failover did not complete",
        page: { intervalSeconds: 60, maxRetries: 3, ack: "each" },
      }),
    want: {
      messageId: "",
      level: "page",
      devices: 0,
      status: "",
      deliveries: [],
      recipients: [
        { externalUserId: "alice", messageId: "msg_3", status: "queued", devices: 1, deliveries: [{ deviceId: "dev_1", status: "queued" }] },
        { externalUserId: "bob", messageId: "msg_4", status: "queued", devices: 1, deliveries: [{ deviceId: "dev_2", status: "queued" }] },
      ],
    },
  },
  getMessageAckedNoName: {
    call: (c) => c.getMessage("msg_3"),
    want: {
      messageId: "msg_3",
      status: "acked",
      level: "page",
      attempts: 1,
      deliveries: [{ deviceId: "dev_1", status: "sent" }],
      ackedAt: new Date("2026-11-08T12:05:00Z"),
      ackedBy: "alice",
    },
  },
  getMessage: {
    call: (c) => c.getMessage("msg_2"),
    want: {
      messageId: "msg_2",
      status: "acked",
      level: "page",
      attempts: 2,
      deliveries: [
        { deviceId: "dev_1", status: "sent" },
        { deviceId: "dev_2", status: "failed", error: "unregistered" },
      ],
      ackedAt: new Date("2026-11-08T12:05:00Z"),
      ackedBy: "user-42",
      ackedByName: "Zoë 日本語",
    },
  },
  startActivity: {
    call: (c) =>
      c.startActivity({
        externalUserId: "user-42",
        channel: "ops",
        templateId: "deploy",
        data: { progress: 0.1 },
        alert: { title: "Deploy started", body: "v1.2.3" },
      }),
    want: { activityId: "act_1", devices: 1 },
  },
  updateActivity: {
    call: (c) => c.updateActivity("act_1", { data: { progress: 0.6 }, priority: 10, alert: { title: "Halfway", body: "60%" } }),
    want: { activityId: "act_1", update: 3 },
  },
  endActivity: { call: (c) => c.endActivity("act_1"), want: undefined },
  putFeed: {
    call: (c) => c.putFeed("uptime", { externalUserId: "user-42", templateId: "status", title: "Uptime", data: { up: true, ms: 41 } }),
    want: { feedId: "uptime", devices: 2 },
  },
  putGroup: { call: (c) => c.putGroup("oncall", ["alice", "bob"]), want: { id: "oncall", members: ["alice", "bob"] } },
  getGroup: { call: (c) => c.getGroup("oncall"), want: { id: "oncall", members: ["alice", "bob"] } },
  deleteGroup: { call: (c) => c.deleteGroup("oncall"), want: undefined },
  kick: { call: (c) => c.kick("user-42"), want: undefined },
  putChannelWithAudience: {
    call: (c) =>
      c.putChannel("ops", {
        name: "Operations",
        defaultLevel: "active",
        maxLevel: "critical",
        audience: { mode: "only", users: ["alice"], groups: ["oncall"] },
      }),
    want: undefined,
  },
  putFeedWithChannel: {
    call: (c) =>
      c.putFeed("uptime", {
        templateId: "status",
        channel: "ops",
        audience: { mode: "only", users: ["alice"], groups: ["oncall"] },
        data: { up: true },
      }),
    want: { feedId: "uptime", devices: 1 },
  },
  sendToAudience: {
    call: (c) => c.sendMessage({ channel: "ops", toAudience: true, title: "Deploy done", body: "v1.2.3 is live" }),
    // For an audience the top-level message fields are empty; each message is under recipients.
    want: {
      messageId: "",
      level: "active",
      devices: 0,
      status: "",
      deliveries: [],
      recipients: [
        { externalUserId: "alice", messageId: "msg_7", status: "queued", devices: 1, deliveries: [{ deviceId: "dev_1", status: "queued" }] },
        { externalUserId: "carol", status: "no_access" },
      ],
    },
  },
};

describe("shared spec operations", () => {
  const spec = loadSpec();
  for (const op of spec.operations) {
    it(op.name, async () => {
      const tc = calls[op.name];
      assert.ok(tc, `no SDK call for spec operation ${op.name}`);
      const hub = await startHub((r, reply) => {
        reply.status = op.response.status;
        reply.body = JSON.stringify(op.response.body);
        assert.equal(r.method, op.method);
        assert.equal(r.url, op.path);
        assert.equal(r.headers.authorization, `Bearer ${TEST_KEY}`);
        if (op.request === null) {
          assert.equal(r.body.length, 0);
        } else if (op.request.raw_base64 !== undefined) {
          assert.equal(r.headers["content-type"], op.request.content_type);
          assert.deepEqual(r.body, Buffer.from(op.request.raw_base64, "base64"));
        } else {
          assert.equal(r.headers["content-type"], op.request.content_type);
          assert.deepEqual(JSON.parse(r.body.toString("utf8")), op.request.body);
        }
      });
      try {
        const got = await tc.call(hub.client);
        assert.deepEqual(got, tc.want);
        assert.equal(hub.requests.length, 1);
      } finally {
        await hub.close();
      }
    });
  }
  it("covers every SDK call", () => {
    const names = new Set(spec.operations.map((o) => o.name));
    for (const k of Object.keys(calls)) assert.ok(names.has(k), `SDK call ${k} has no operation in sdk/spec/api.json`);
  });
});

describe("shared spec errors", () => {
  for (const [i, e] of loadSpec().errors.entries()) {
    it(`error ${i} (${e.status})`, async () => {
      const hub = await startHub((_r, reply) => {
        reply.status = e.status;
        reply.headers = e.headers;
        reply.body = e.raw ?? JSON.stringify(e.body);
      });
      try {
        await assert.rejects(
          hub.client.sendMessage({ externalUserId: "u", channel: "c", title: "t", body: "b" }),
          (err: unknown) => {
            assert.ok(err instanceof HanshoError);
            assert.equal(err.status, e.status);
            assert.equal(err.code, e.code);
            assert.equal(err.field ?? "", e.field);
            assert.equal(err.retryAfterMs ?? 0, e.retry_after_ms);
            return true;
          },
        );
        assert.equal(hub.requests.length, 1, "POST /messages must not be retried");
      } finally {
        await hub.close();
      }
    });
  }
});

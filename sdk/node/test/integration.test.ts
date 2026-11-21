import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync } from "node:fs";
import http from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, it } from "node:test";
import { Hansho, HanshoError, type HanshoEvent, type Level, verifyWebhook, WEBHOOK_SIGNATURE_HEADER, WebhookError } from "../src/index.ts";

// Run through sdk/scripts/run-integration.sh, which starts the real hub binary and
// a stub relay and exports the HANSHO_* variables used here.
const skip = !process.env.HANSHO_HUB_BIN && "HANSHO_HUB_BIN is not set; run sdk/scripts/run-integration.sh";

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function fakeDevice(...args: string[]): string {
  return execFileSync(process.env.HANSHO_FAKEDEVICE_BIN!, args, { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
}

function tinyWav(): Uint8Array {
  const data = Buffer.alloc(800); // 0.05 s of 8 kHz 8-bit mono silence
  const h = Buffer.alloc(44);
  h.write("RIFF", 0);
  h.writeUInt32LE(36 + data.length, 4);
  h.write("WAVEfmt ", 8);
  h.writeUInt32LE(16, 16);
  h.writeUInt16LE(1, 20);
  h.writeUInt16LE(1, 22);
  h.writeUInt32LE(8000, 24);
  h.writeUInt32LE(8000, 28);
  h.writeUInt16LE(1, 32);
  h.writeUInt16LE(8, 34);
  h.write("data", 36);
  h.writeUInt32LE(data.length, 40);
  return Buffer.concat([h, data]);
}

async function listen(addr: string, secret: string) {
  const events: HanshoEvent[] = [];
  let rejected = 0;
  const server = http.createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c));
    req.on("end", () => {
      try {
        events.push(verifyWebhook({ secret, header: req.headers[WEBHOOK_SIGNATURE_HEADER.toLowerCase()] as string, body: Buffer.concat(chunks) }));
        res.writeHead(200).end();
      } catch (e) {
        rejected++;
        res.writeHead(e instanceof WebhookError ? 400 : 500).end();
      }
    });
  });
  const [host, port] = addr.split(":") as [string, string];
  for (let i = 0; ; i++) {
    try {
      await new Promise<void>((resolve, reject) => {
        server.once("error", reject);
        server.listen(Number(port), host, () => resolve());
      });
      break;
    } catch (e) {
      if (i >= 50) throw e;
      await sleep(100); // the previous suite's receiver may still be closing
    }
  }
  return {
    events,
    get rejected() {
      return rejected;
    },
    close: () => {
      server.closeAllConnections();
      server.close();
    },
    async wait<T extends HanshoEvent["type"]>(type: T, match: (e: Extract<HanshoEvent, { type: T }>) => boolean): Promise<Extract<HanshoEvent, { type: T }>> {
      const deadline = Date.now() + 25_000;
      while (Date.now() < deadline) {
        const hit = events.find((e) => e.type === type && match(e as Extract<HanshoEvent, { type: T }>));
        if (hit) return hit as Extract<HanshoEvent, { type: T }>;
        await sleep(100);
      }
      throw new Error(`no ${type} webhook arrived`);
    },
  };
}

async function waitStatus(c: Hansho, id: string, ...want: string[]) {
  const deadline = Date.now() + 25_000;
  let last = "";
  while (Date.now() < deadline) {
    const ms = await c.getMessage(id);
    last = ms.status;
    if (want.includes(ms.status)) return ms;
    await sleep(200);
  }
  throw new Error(`message ${id}: status ${last}, wanted one of ${want.join(", ")}`);
}

function isApiError(status: number, code: string, field?: string) {
  return (e: unknown) => e instanceof HanshoError && e.status === status && e.code === code && (field === undefined || e.field === field);
}

describe("integration against the real hub", { skip }, () => {
  it("runs the whole flow", async () => {
    const hubUrl = process.env.HANSHO_HUB_URL!;
    const rx = await listen(process.env.HANSHO_WEBHOOK_ADDR!, process.env.HANSHO_WEBHOOK_SECRET!);
    try {
      const c = new Hansho({ baseUrl: hubUrl, apiKey: process.env.HANSHO_API_KEY! });
      const p = "node";
      const user = `u-${p} ü/42?x`;
      const [ch, tpl, snd] = [`${p}-ops`, `${p}-status`, `${p}-ding`];
      const statePath = join(mkdtempSync(join(tmpdir(), "hansho-node-it-")), "device.json");

      // errors
      await assert.rejects(new Hansho({ baseUrl: hubUrl, apiKey: "ck_not_a_key" }).createPairingTicket("x"), isApiError(401, "unauthorized"));
      await assert.rejects(c.getChannel("no-such-channel"), isApiError(404, "unknown_channel"));
      await assert.rejects(c.sendMessage({ externalUserId: user, channel: ch, title: "", body: "b" }), isApiError(400, "invalid_request", "title"));

      // catalog
      const sound = await c.putSound(snd, "audio/wav", tinyWav());
      assert.equal(sound.id, snd);
      const template = { type: "vstack", tint: { light: "#0A84FF", dark: "#64D2FF" }, children: [{ type: "text", style: "headline", text: "{{title}}" }, { type: "spacer" }] };
      await c.putTemplate(tpl, 1, template);
      await assert.rejects(c.putTemplate(tpl, 1, template), isApiError(409, "version_conflict"));
      await c.putTemplate(tpl, 2, template);
      await c.putChannel(ch, {
        name: `Operations ${p}`,
        defaultLevel: "active",
        maxLevel: "page",
        sound: snd,
        templateId: tpl,
        thread: "ops",
        actions: [{ id: "ack", title: "Acknowledge", ack: true }],
      });
      const got = await c.getChannel(ch);
      assert.equal(got.name, `Operations ${p}`);
      assert.equal(got.maxLevel, "page");
      assert.equal(got.sound, snd);
      assert.equal(got.templateId, tpl);
      assert.deepEqual(got.actions, [{ id: "ack", title: "Acknowledge", ack: true }]);
      await assert.rejects(c.putChannel("bad id", { name: "n", defaultLevel: "active", maxLevel: "page" }), isApiError(400, "invalid_request", "id"));

      // pairing
      const tk = await c.createPairingTicket(user);
      assert.ok(tk.qr.startsWith("hansho://pair?") && tk.qr.includes(tk.ticket));
      assert.ok(tk.expiresAt.getTime() > Date.now());
      fakeDevice("pair", "-hub", hubUrl, "-ticket", tk.ticket, "-state", statePath, "-name", `SDK phone ${p}`);
      await rx.wait("pairing.created", (e) => e.data.externalUserId === user && e.data.deviceName === `SDK phone ${p}`);

      // messages at every level
      for (const level of ["passive", "active", "time-sensitive", "critical"] as Level[]) {
        const res = await c.sendMessage({
          externalUserId: user,
          channel: ch,
          level,
          title: "Disk full",
          body: "db-1 is at 97%",
          data: { disk: 97 },
          sound: snd,
          collapseId: `disk-${level}`,
        });
        assert.equal(res.level, level);
        assert.equal(res.devices, 1);
        assert.ok(res.messageId.startsWith("msg_"));
        const ms = await waitStatus(c, res.messageId, "delivered");
        assert.equal(ms.level, level);
        assert.equal(ms.deliveries.length, 1);
        assert.equal(ms.deliveries[0]!.status, "sent");
        await rx.wait("message.delivered", (e) => e.data.messageId === res.messageId && e.data.level === level && e.data.devices === 1);
      }
      const def = await c.sendMessage({ externalUserId: user, channel: ch, title: "Default", body: "b" });
      assert.equal(def.level, "active");

      // page + acknowledgement from the (fake) device
      const page = await c.sendMessage({
        externalUserId: user,
        channel: ch,
        level: "page",
        title: "Primary DB down",
        body: "Failover failed",
        page: { intervalSeconds: 60, maxRetries: 3 },
      });
      assert.equal(page.level, "page");
      await waitStatus(c, page.messageId, "paging");
      fakeDevice("ack", "-state", statePath, "-message", page.messageId);
      const acked = await waitStatus(c, page.messageId, "acked");
      assert.ok(acked.ackedAt instanceof Date && !Number.isNaN(acked.ackedAt.getTime()));
      const ev = await rx.wait("message.acked", (e) => e.data.messageId === page.messageId);
      assert.equal(ev.data.externalUserId, user);
      await rx.wait("action", (e) => e.data.messageId === page.messageId && e.data.actionId === "ack");

      // live activity
      const started = await c.startActivity({
        externalUserId: user,
        channel: ch,
        templateId: tpl,
        data: { title: "Deploy", progress: 0.1 },
        alert: { title: "Deploy started" },
      });
      assert.equal(started.devices, 1);
      assert.ok(started.activityId.startsWith("act_"));
      fakeDevice("activity-token", "-state", statePath, "-activity", started.activityId);
      const up = await c.updateActivity(started.activityId, { data: { title: "Deploy", progress: 0.6 }, priority: 10 });
      assert.equal(up.activityId, started.activityId);
      assert.ok(up.update >= 1);
      await c.endActivity(started.activityId);
      await assert.rejects(c.updateActivity(started.activityId, { data: { title: "x" } }), isApiError(409, "activity_ended"));

      // feeds
      const own = await c.putFeed(`${p}-uptime`, { externalUserId: user, templateId: tpl, data: { title: "up", ms: 41 } });
      assert.equal(own.feedId, `${p}-uptime`);
      assert.ok(own.devices >= 1);
      const shared = await c.putFeed(`${p}-shared`, { templateId: tpl, data: { title: "all" } });
      assert.ok(shared.devices >= 1);
      await assert.rejects(c.putFeed(`${p}-bad`, { templateId: "missing", data: 1 }), isApiError(400, "invalid_request", "template_id"));

      // unpair, then nobody is left to deliver to
      await c.unpair(user);
      await rx.wait("pairing.revoked", (e) => e.data.externalUserId === user && e.data.by === "sender");
      const none = await c.sendMessage({ externalUserId: user, channel: ch, title: "Anyone?", body: "b" });
      assert.equal(none.devices, 0);
      await waitStatus(c, none.messageId, "undeliverable");
      await rx.wait("message.undeliverable", (e) => e.data.messageId === none.messageId);
      await c.deleteChannel(ch);
      await assert.rejects(c.getChannel(ch), isApiError(404, "unknown_channel"));

      assert.equal(rx.rejected, 0, "the SDK rejected a webhook the hub signed");
      assert.ok(rx.events.length > 0);
    } finally {
      rx.close();
    }
  });
});

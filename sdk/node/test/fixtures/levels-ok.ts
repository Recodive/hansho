import { Hansho, type Level, type Message } from "../../src/index.ts";

const levels: Level[] = ["passive", "active", "time-sensitive", "critical", "page"];
const ok: Message = { externalUserId: "u", channel: "c", level: "time-sensitive", title: "t", body: "b" };
// @ts-expect-error "pager" is not a level
const bad: Message = { externalUserId: "u", channel: "c", level: "pager", title: "t", body: "b" };
// @ts-expect-error a channel's levels are checked too
const badChannel = new Hansho({ baseUrl: "x", apiKey: "k" }).putChannel("c", { name: "n", defaultLevel: "pager", maxLevel: "page" });
export { levels, ok, bad, badChannel };

import { type HanshoEvent, verifyWebhook } from "../../src/index.ts";

declare const parsed: { id: string };
declare const raw: Buffer;
// @ts-expect-error a parsed (re-serialisable) body is a type error: pass the raw bytes
verifyWebhook({ secret: "s", header: "h", body: parsed });
const ev: HanshoEvent = verifyWebhook({ secret: "s", header: "h", body: raw });
if (ev.type === "message.acked") {
  const id: string = ev.data.messageId;
  void id;
}
if (ev.type === "unknown") {
  const name: string = ev.name;
  void name;
}

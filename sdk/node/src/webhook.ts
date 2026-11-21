import { createHmac, timingSafeEqual } from "node:crypto";
import type { Level } from "./types.ts";

/** `X-Hansho-Webhook-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>` */
export const WEBHOOK_SIGNATURE_HEADER = "X-Hansho-Webhook-Signature";

/** Default allowed distance between the signed timestamp and now: 300 s. */
export const DEFAULT_TOLERANCE_MS = 300_000;

export type WebhookErrorCode =
  | "malformed_header"
  | "bad_signature"
  | "stale"
  | "malformed_body"
  | "invalid_body"
  | "invalid_secret";

/** Thrown by {@link verifyWebhook}; check `code`. */
export class WebhookError extends Error {
  readonly code: WebhookErrorCode;
  constructor(code: WebhookErrorCode, message: string) {
    super(message);
    this.name = "WebhookError";
    this.code = code;
  }
}

export interface PairingCreated {
  pairingId: string;
  externalUserId: string;
  deviceName: string;
}
export interface PairingRevoked {
  pairingId: string;
  externalUserId: string;
  /** Who revoked: "sender", "device" or "relay". */
  by: string;
}
export interface MessageDelivered {
  messageId: string;
  externalUserId: string;
  channel: string;
  level: Level;
  devices: number;
}
export interface MessageUndeliverable {
  messageId: string;
  externalUserId: string;
  channel: string;
  /** "no_devices" or "all_deliveries_failed". */
  reason: string;
}
export interface MessageAcked {
  messageId: string;
  externalUserId: string;
  deviceId: string;
}
/** The person swiped away a message that asked to be acknowledged, without acknowledging it. */
export interface MessageDismissed {
  messageId: string;
  externalUserId: string;
  deviceId: string;
}
export interface MessageExpired {
  messageId: string;
  externalUserId: string;
  channel: string;
  attempts: number;
}
/** A button press. */
export interface ActionTaken {
  messageId: string;
  actionId: string;
  deviceId: string;
  externalUserId: string;
  /** Reply text of a text action. */
  text?: string;
}
export interface ActivityEnded {
  activityId: string;
  externalUserId: string;
  channel: string;
  reason: string;
}

interface EventBase {
  /** The hub's event id. */
  id: string;
  createdAt: Date;
  /** The verified request body. */
  raw: Uint8Array;
}

/** A verified webhook. Narrow on `type`; an event this SDK does not know is `"unknown"`. */
export type HanshoEvent =
  | (EventBase & { type: "pairing.created"; data: PairingCreated })
  | (EventBase & { type: "pairing.revoked"; data: PairingRevoked })
  | (EventBase & { type: "message.delivered"; data: MessageDelivered })
  | (EventBase & { type: "message.undeliverable"; data: MessageUndeliverable })
  | (EventBase & { type: "message.acked"; data: MessageAcked })
  | (EventBase & { type: "message.dismissed"; data: MessageDismissed })
  | (EventBase & { type: "message.expired"; data: MessageExpired })
  | (EventBase & { type: "action"; data: ActionTaken })
  | (EventBase & { type: "activity.ended"; data: ActivityEnded })
  | (EventBase & {
      type: "unknown";
      /** The event name as sent. */
      name: string;
      /** The payload as parsed JSON, untouched. */
      data: unknown;
    });

export interface VerifyWebhookOptions {
  /** The shared secret (`HUB_WEBHOOK_SECRET`). */
  secret: string | Uint8Array;
  /** The `X-Hansho-Webhook-Signature` request header. */
  header: string;
  /**
   * The request body exactly as received. Use `express.raw({ type: "*\/*" })`
   * (or read the stream yourself): a body that a framework parsed and you
   * re-serialised will not verify, and an object is rejected.
   */
  body: Uint8Array | string;
  /** The current time (Date or epoch milliseconds). Default: now. */
  now?: Date | number;
  /** Allowed distance between the signed timestamp and now. Default 300000; a value <= 0 means the default. */
  toleranceMs?: number;
}

type Wire = Record<string, unknown>;

const s = (v: unknown): string => (typeof v === "string" ? v : v === undefined || v === null ? "" : String(v));
const n = (v: unknown): number => (typeof v === "number" ? v : Number(v ?? 0));

function undef<T extends Wire>(o: T): T {
  for (const k of Object.keys(o)) if (o[k] === undefined) delete o[k];
  return o;
}

function mapPayload(type: string, d: Wire): HanshoEvent["data"] | undefined {
  switch (type) {
    case "pairing.created":
      return { pairingId: s(d.pairing_id), externalUserId: s(d.external_user_id), deviceName: s(d.device_name) };
    case "pairing.revoked":
      return { pairingId: s(d.pairing_id), externalUserId: s(d.external_user_id), by: s(d.by) };
    case "message.delivered":
      return { messageId: s(d.message_id), externalUserId: s(d.external_user_id), channel: s(d.channel), level: s(d.level) as Level, devices: n(d.devices) };
    case "message.undeliverable":
      return { messageId: s(d.message_id), externalUserId: s(d.external_user_id), channel: s(d.channel), reason: s(d.reason) };
    case "message.acked":
      return { messageId: s(d.message_id), externalUserId: s(d.external_user_id), deviceId: s(d.device_id) };
    case "message.dismissed":
      return { messageId: s(d.message_id), externalUserId: s(d.external_user_id), deviceId: s(d.device_id) };
    case "message.expired":
      return { messageId: s(d.message_id), externalUserId: s(d.external_user_id), channel: s(d.channel), attempts: n(d.attempts) };
    case "action":
      return undef({
        messageId: s(d.message_id),
        actionId: s(d.action_id),
        deviceId: s(d.device_id),
        externalUserId: s(d.external_user_id),
        text: typeof d.text === "string" ? d.text : undefined,
      });
    case "activity.ended":
      return { activityId: s(d.activity_id), externalUserId: s(d.external_user_id), channel: s(d.channel), reason: s(d.reason) };
    default:
      return undefined;
  }
}

function parseHeader(header: unknown): { t: string; sigs: string[] } {
  if (typeof header !== "string" || header.trim() === "") throw new WebhookError("malformed_header", "missing signature header");
  let t: string | undefined;
  const sigs: string[] = [];
  for (const part of header.split(",")) {
    const trimmed = part.trim();
    const eq = trimmed.indexOf("=");
    if (eq < 0) throw new WebhookError("malformed_header", "signature header part is not key=value");
    const k = trimmed.slice(0, eq);
    const v = trimmed.slice(eq + 1);
    if (k === "t") {
      if (t !== undefined) throw new WebhookError("malformed_header", "duplicate timestamp");
      t = v;
    } else if (k === "v1" && v !== "") {
      sigs.push(v);
    }
  }
  if (t === undefined || !/^-?\d+$/.test(t) || sigs.length === 0) {
    throw new WebhookError("malformed_header", "signature header needs t=<unix> and v1=<hex>");
  }
  return { t, sigs };
}

/**
 * Verifies a webhook against its raw body and returns the event. Throws
 * {@link WebhookError} (`code`: `malformed_header`, `bad_signature`, `stale`,
 * `malformed_body`, `invalid_body`, `invalid_secret`). With several `v1` values
 * in the header (secret rotation) any match is accepted.
 */
export function verifyWebhook(opts: VerifyWebhookOptions): HanshoEvent {
  const { secret, header, body } = opts;
  if (!(typeof secret === "string" || secret instanceof Uint8Array) || secret.length === 0) {
    throw new WebhookError("invalid_secret", "webhook secret is empty");
  }
  if (!(typeof body === "string" || body instanceof Uint8Array)) {
    throw new WebhookError("invalid_body", "body must be the raw request body (Buffer or string), not a parsed object");
  }
  const { t, sigs } = parseHeader(header);
  const raw = typeof body === "string" ? new TextEncoder().encode(body) : body;
  const want = createHmac("sha256", secret).update(`${t}.`).update(raw).digest();
  let match = false;
  for (const sig of sigs) {
    if (!/^[0-9a-fA-F]{64}$/.test(sig)) continue;
    if (timingSafeEqual(Buffer.from(sig, "hex"), want)) match = true;
  }
  if (!match) throw new WebhookError("bad_signature", "signature does not match");

  const nowMs = opts.now === undefined ? Date.now() : opts.now instanceof Date ? opts.now.getTime() : opts.now;
  const tol = opts.toleranceMs !== undefined && opts.toleranceMs > 0 ? opts.toleranceMs : DEFAULT_TOLERANCE_MS;
  if (Math.abs(nowMs - Number(t) * 1000) > tol) throw new WebhookError("stale", "timestamp outside tolerance");

  let env: Wire;
  try {
    const parsed: unknown = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(raw));
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("not an object");
    env = parsed as Wire;
  } catch {
    throw new WebhookError("malformed_body", "body is not a webhook envelope");
  }
  const name = env.event;
  if (typeof name !== "string" || name === "") throw new WebhookError("malformed_body", "body has no event");
  const base: EventBase = { id: s(env.id), createdAt: new Date(n(env.created_at) * 1000), raw };
  const data = (env.data && typeof env.data === "object" ? env.data : {}) as Wire;
  const mapped = mapPayload(name, data);
  if (mapped === undefined) return { ...base, type: "unknown", name, data: env.data };
  return { ...base, type: name, data: mapped } as HanshoEvent;
}

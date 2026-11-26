import { HanshoError, HanshoNetworkError } from "./errors.ts";
import { backoffMs, DEFAULT_MAX_RETRIES, defaultSleep, isIdempotent, isRetryableStatus, MAX_RETRY_WAIT_MS, parseRetryAfter } from "./retry.ts";
import type {
  Activity,
  ActivityStarted,
  ActivityUpdate,
  ActivityUpdated,
  Channel,
  Feed,
  FeedResult,
  FeedSettings,
  Group,
  Message,
  MessageResult,
  MessageStatus,
  PairingTicket,
  Sound,
  SoundResult,
  Template,
} from "./types_gen.ts";
import {
  fromWireActivityStarted,
  fromWireActivityUpdated,
  fromWireChannel,
  fromWireFeedResult,
  fromWireFeedSettings,
  fromWireGroup,
  fromWireMessageResult,
  fromWireMessageStatus,
  fromWirePairingTicket,
  fromWireSound,
  fromWireSoundResult,
  fromWireTemplate,
  toWireActivity,
  toWireActivityUpdate,
  toWireChannel,
  toWireFeed,
  toWireMessage,
  VERSION,
} from "./types_gen.ts";
import type { HanshoOptions, PairingTicketOptions, RequestOptions } from "./types.ts";

/** Product codename. */
export const NAME = "hansho";

const MAX_SOUND_BYTES = 1 << 20;

type Wire = Record<string, unknown>;

interface Call {
  method: string;
  /** Escaped path relative to the base URL. */
  path: string;
  body?: string | Uint8Array;
  contentType?: string;
  retry: boolean;
  headers?: Record<string, string>;
}

/** Escapes one path segment; rejects ids that would not stay in their collection. */
function seg(id: string): string {
  if (typeof id !== "string" || id === "") throw new RangeError("hansho: id must not be empty");
  // WHATWG URL parsing collapses "%2e%2e", so these cannot be sent safely.
  if (id === "." || id === "..") throw new RangeError(`hansho: id must not be "${id}"`);
  return encodeURIComponent(id);
}

function defined<T extends Wire>(o: T): T {
  for (const k of Object.keys(o)) if (o[k] === undefined) delete o[k];
  return o;
}

/** The elements of a list answer; anything else is an empty list. */
function items(raw: unknown): unknown[] {
  return Array.isArray(raw) ? raw : [];
}

/** Client for a Hansho hub's sender API. */
export class Hansho {
  readonly #baseUrl: string;
  readonly #apiKey: string;
  readonly #fetch: typeof fetch;
  readonly #maxRetries: number;
  readonly #timeoutMs: number;
  readonly #userAgent: string;
  readonly #sleep: (ms: number, signal?: AbortSignal) => Promise<void>;

  constructor(opts: HanshoOptions) {
    if (!opts || typeof opts.baseUrl !== "string" || opts.baseUrl === "") throw new TypeError("hansho: baseUrl is required");
    if (typeof opts.apiKey !== "string" || opts.apiKey === "") throw new TypeError("hansho: apiKey is required");
    this.#baseUrl = opts.baseUrl.replace(/\/+$/, "");
    this.#apiKey = opts.apiKey;
    this.#fetch = opts.fetch ?? ((input, init) => fetch(input, init));
    this.#maxRetries = Math.max(0, opts.maxRetries ?? DEFAULT_MAX_RETRIES);
    this.#timeoutMs = opts.timeoutMs ?? 30_000;
    this.#userAgent = opts.userAgent ?? `${NAME}-node/${VERSION}`;
    this.#sleep = opts.sleep ?? defaultSleep;
  }

  /** Keeps the API key out of logs. */
  toJSON(): { baseUrl: string } {
    return { baseUrl: this.#baseUrl };
  }

  // --- pairing -------------------------------------------------------------

  /**
   * Creates a single-use pairing ticket (valid 10 minutes). Render `qr` as a QR
   * code for the user to scan in the Hansho app.
   */
  async createPairingTicket(externalUserId: string, opts?: PairingTicketOptions): Promise<PairingTicket> {
    if (externalUserId === "") throw new RangeError("hansho: id must not be empty");
    const body = defined({ external_user_id: externalUserId, display_name: opts?.displayName });
    return fromWirePairingTicket(await this.#json("POST", "/api/v1/pairing-tickets", body, opts));
  }

  /**
   * Sets what other people see for the user instead of their `externalUserId`: any language, trimmed, at most 40
   * characters, no control characters. `""` clears it. The user does not need to be paired yet.
   */
  async setUserDisplayName(externalUserId: string, displayName: string, opts?: RequestOptions): Promise<void> {
    if (externalUserId === "") throw new RangeError("hansho: id must not be empty");
    await this.#json("PUT", `/api/v1/users/${seg(externalUserId)}`, { display_name: displayName }, opts);
  }

  /** Revokes every pairing of the user. */
  async unpair(externalUserId: string, opts?: RequestOptions): Promise<void> {
    await this.#json("DELETE", `/api/v1/users/${seg(externalUserId)}/pairings`, undefined, opts);
  }

  /**
   * Ends the user's whole pairing, on every device: `unpair` under its sender-side name. The app tells the person
   * that the sender ended it (a 401 `pairing_revoked` with reason `"kicked"`), where an unpair they did themselves
   * carries no reason. There is no block list; a new ticket pairs them again.
   */
  kick(externalUserId: string, opts?: RequestOptions): Promise<void> {
    return this.unpair(externalUserId, opts);
  }

  // --- catalog -------------------------------------------------------------

  async putChannel(id: string, channel: Channel, opts?: RequestOptions): Promise<void> {
    await this.#json("PUT", `/api/v1/channels/${seg(id)}`, toWireChannel(channel), opts);
  }

  async getChannel(id: string, opts?: RequestOptions): Promise<Channel> {
    return fromWireChannel(await this.#json("GET", `/api/v1/channels/${seg(id)}`, undefined, opts));
  }

  /** Every channel of the hub. */
  async listChannels(opts?: RequestOptions): Promise<Channel[]> {
    return items(await this.#jsonAny("GET", "/api/v1/channels", opts)).map(fromWireChannel);
  }

  async deleteChannel(id: string, opts?: RequestOptions): Promise<void> {
    await this.#json("DELETE", `/api/v1/channels/${seg(id)}`, undefined, opts);
  }

  // --- groups --------------------------------------------------------------

  /**
   * Creates a group or replaces its whole member list (external user ids, who may not have paired yet). Audiences
   * that name the group follow at once. Ids match `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`.
   */
  async putGroup(id: string, members: string[], opts?: RequestOptions): Promise<Group> {
    return fromWireGroup(await this.#json("PUT", `/api/v1/groups/${seg(id)}`, { members }, opts));
  }

  /** A missing group is a `HanshoError` with code `"unknown_group"`. */
  async getGroup(id: string, opts?: RequestOptions): Promise<Group> {
    return fromWireGroup(await this.#json("GET", `/api/v1/groups/${seg(id)}`, undefined, opts));
  }

  /** Every group of the hub. */
  async listGroups(opts?: RequestOptions): Promise<Group[]> {
    return items(await this.#jsonAny("GET", "/api/v1/groups", opts)).map(fromWireGroup);
  }

  /** An audience that still names the group matches nobody through it. */
  async deleteGroup(id: string, opts?: RequestOptions): Promise<void> {
    await this.#json("DELETE", `/api/v1/groups/${seg(id)}`, undefined, opts);
  }

  /** Stores a template version; the version must increase. */
  async putTemplate(id: string, version: number, body: unknown, opts?: RequestOptions): Promise<void> {
    await this.#json("PUT", `/api/v1/templates/${seg(id)}`, { version, body }, opts);
  }

  /** Every stored template, with its current version. */
  async listTemplates(opts?: RequestOptions): Promise<Template[]> {
    return items(await this.#jsonAny("GET", "/api/v1/templates", opts)).map(fromWireTemplate);
  }

  /** A missing template is a `HanshoError` with status 404. */
  async getTemplate(id: string, opts?: RequestOptions): Promise<Template> {
    return fromWireTemplate(await this.#json("GET", `/api/v1/templates/${seg(id)}`, undefined, opts));
  }

  async deleteTemplate(id: string, opts?: RequestOptions): Promise<void> {
    await this.#json("DELETE", `/api/v1/templates/${seg(id)}`, undefined, opts);
  }

  /** Uploads a sound (`audio/x-caf`, `audio/wav` or `audio/aiff`, at most 1 MB). */
  async putSound(id: string, contentType: string, bytes: Uint8Array | ArrayBuffer, opts?: RequestOptions): Promise<SoundResult> {
    const path = `/api/v1/sounds/${seg(id)}`;
    const data = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
    if (data.byteLength > MAX_SOUND_BYTES) throw new RangeError("hansho: sound exceeds 1 MB");
    const w = await this.#send({ method: "PUT", path, body: data, contentType, retry: true }, opts);
    return fromWireSoundResult(w);
  }

  /** The metadata of every stored sound. */
  async listSounds(opts?: RequestOptions): Promise<Sound[]> {
    return items(await this.#jsonAny("GET", "/api/v1/sounds", opts)).map(fromWireSound);
  }

  /** The metadata of a sound, not its audio. */
  async getSound(id: string, opts?: RequestOptions): Promise<Sound> {
    return fromWireSound(await this.#json("GET", `/api/v1/sounds/${seg(id)}`, undefined, opts));
  }

  async deleteSound(id: string, opts?: RequestOptions): Promise<void> {
    await this.#json("DELETE", `/api/v1/sounds/${seg(id)}`, undefined, opts);
  }

  // --- messages ------------------------------------------------------------

  /**
   * Sends a notification, or a page when `level` is `"page"`. It is never
   * retried automatically (a repeat would notify twice) unless the message has
   * an `idempotencyKey`.
   */
  async sendMessage(message: Message, opts?: RequestOptions): Promise<MessageResult> {
    const call: Call = {
      method: "POST",
      path: "/api/v1/messages",
      body: JSON.stringify(toWireMessage(message)),
      contentType: "application/json",
      retry: false,
    };
    if (message.idempotencyKey) {
      call.retry = true;
      call.headers = { "Idempotency-Key": message.idempotencyKey };
    }
    return fromWireMessageResult(await this.#send(call, opts));
  }

  async getMessage(id: string, opts?: RequestOptions): Promise<MessageStatus> {
    return fromWireMessageStatus(await this.#json("GET", `/api/v1/messages/${seg(id)}`, undefined, opts));
  }

  // --- live activities and feeds -------------------------------------------

  async startActivity(activity: Activity, opts?: RequestOptions): Promise<ActivityStarted> {
    return fromWireActivityStarted(await this.#json("POST", "/api/v1/activities", toWireActivity(activity), opts));
  }

  /** Not retried automatically. */
  async updateActivity(id: string, update: ActivityUpdate, opts?: RequestOptions): Promise<ActivityUpdated> {
    return fromWireActivityUpdated(await this.#json("PATCH", `/api/v1/activities/${seg(id)}`, toWireActivityUpdate(update), opts));
  }

  async endActivity(id: string, opts?: RequestOptions): Promise<void> {
    await this.#json("DELETE", `/api/v1/activities/${seg(id)}`, undefined, opts);
  }

  async putFeed(id: string, feed: Feed, opts?: RequestOptions): Promise<FeedResult> {
    return fromWireFeedResult(await this.#json("PUT", `/api/v1/feeds/${seg(id)}`, toWireFeed(feed), opts));
  }

  /** A feed's settings without its data. Omit `externalUserId` for the feed shared by all users. */
  async getFeed(id: string, externalUserId?: string, opts?: RequestOptions): Promise<FeedSettings> {
    const query = externalUserId ? `?external_user_id=${encodeURIComponent(externalUserId)}` : "";
    return fromWireFeedSettings(await this.#json("GET", `/api/v1/feeds/${seg(id)}${query}`, undefined, opts));
  }

  // --- plumbing ------------------------------------------------------------

  /** A request whose answer may be a JSON array (the list operations). */
  #jsonAny(method: string, path: string, opts?: RequestOptions): Promise<unknown> {
    return this.#json(method, path, undefined, opts);
  }

  #json(method: string, path: string, body: unknown, opts?: RequestOptions): Promise<Wire> {
    const call: Call = { method, path, retry: isIdempotent(method) };
    if (body !== undefined) {
      call.body = JSON.stringify(body);
      call.contentType = "application/json";
    }
    return this.#send(call, opts);
  }

  async #send(call: Call, opts?: RequestOptions): Promise<Wire> {
    for (let attempt = 0; ; attempt++) {
      const res = await this.#roundTrip(call, opts);
      if (res.status >= 200 && res.status < 300) {
        if (res.text.trim() === "") return {};
        try {
          const parsed: unknown = JSON.parse(res.text);
          return parsed && typeof parsed === "object" ? (parsed as Wire) : {};
        } catch (cause) {
          throw new HanshoNetworkError(call.method, call.path, new Error("invalid JSON in response", { cause }));
        }
      }
      const err = this.#apiError(call, res);
      if (!call.retry || !isRetryableStatus(res.status) || attempt >= this.#maxRetries) throw err;
      const wait = err.retryAfterMs ?? backoffMs(attempt);
      if (wait > MAX_RETRY_WAIT_MS) throw err;
      try {
        await this.#sleep(wait, opts?.signal);
      } catch (cause) {
        throw new HanshoNetworkError(call.method, call.path, cause);
      }
    }
  }

  async #roundTrip(call: Call, opts?: RequestOptions): Promise<{ status: number; headers: Headers; text: string }> {
    const headers: Record<string, string> = {
      authorization: `Bearer ${this.#apiKey}`,
      accept: "application/json",
      "user-agent": this.#userAgent,
      ...call.headers,
    };
    if (call.contentType) headers["content-type"] = call.contentType;
    const signals: AbortSignal[] = [];
    if (opts?.signal) signals.push(opts.signal);
    if (this.#timeoutMs > 0) signals.push(AbortSignal.timeout(this.#timeoutMs));
    // The hub never redirects; following one could hand the API key to another host.
    const init: RequestInit = { method: call.method, headers, redirect: "manual" };
    if (call.body !== undefined) init.body = call.body;
    if (signals.length > 0) init.signal = signals.length === 1 ? signals[0]! : AbortSignal.any(signals);
    try {
      const res = await this.#fetch(this.#baseUrl + call.path, init);
      return { status: res.status, headers: res.headers, text: await res.text() };
    } catch (cause) {
      throw new HanshoNetworkError(call.method, call.path, this.#redact(cause));
    }
  }

  #redact(cause: unknown): unknown {
    if (cause instanceof Error && cause.message.includes(this.#apiKey)) {
      return new Error(cause.message.replaceAll(this.#apiKey, "[redacted]"));
    }
    return cause;
  }

  #apiError(call: Call, res: { status: number; headers: Headers; text: string }): HanshoError {
    let code: string | undefined;
    let field: string | undefined;
    let detail: string | undefined;
    try {
      const p: unknown = JSON.parse(res.text);
      if (p && typeof p === "object") {
        const o = p as Wire;
        if (typeof o.error === "string") code = o.error;
        if (typeof o.field === "string" && o.field) field = o.field;
        if (typeof o.message === "string") detail = o.message.replaceAll(this.#apiKey, "[redacted]");
      }
    } catch {
      /* not JSON (proxy error page): status only */
    }
    const retryAfterMs = parseRetryAfter(res.headers.get("retry-after"));
    return new HanshoError({ method: call.method, path: call.path, status: res.status, code, field, detail, retryAfterMs: retryAfterMs || undefined });
  }
}

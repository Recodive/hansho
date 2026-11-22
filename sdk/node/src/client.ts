import { HanshoError, HanshoNetworkError } from "./errors.ts";
import { backoffMs, DEFAULT_MAX_RETRIES, defaultSleep, isIdempotent, isRetryableStatus, MAX_RETRY_WAIT_MS, parseRetryAfter } from "./retry.ts";
import type {
  Action,
  Activity,
  ActivityAlert,
  ActivityStarted,
  ActivityUpdate,
  ActivityUpdated,
  Audience,
  Channel,
  HanshoOptions,
  Delivery,
  Feed,
  FeedResult,
  FeedSettings,
  Group,
  Level,
  Message,
  MessageResult,
  MessageStatus,
  PairingTicket,
  Recipient,
  RequestOptions,
  SoundResult,
} from "./types.ts";

/** Product codename. */
export const NAME = "hansho";
/** SDK version, sent in the default User-Agent. */
export const VERSION = "0.1.0";

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

function actionsToWire(actions: Action[] | undefined): unknown {
  return actions?.map((a) => defined({ id: a.id, title: a.title, ack: a.ack, kind: a.kind }));
}

function actionsFromWire(raw: unknown): Action[] | undefined {
  if (!Array.isArray(raw)) return undefined;
  return raw.map((a: Wire) => defined({ id: a.id as string, title: a.title as string, ack: a.ack as boolean | undefined, kind: a.kind as string | undefined }));
}

function alertToWire(a: ActivityAlert | undefined): unknown {
  return a && defined({ title: a.title, body: a.body });
}

function deliveryFromWire(raw: unknown): Delivery[] {
  if (!Array.isArray(raw)) return [];
  return raw.map((d: Wire) => defined({ deviceId: d.device_id as string, status: d.status as string, error: (d.error as string | undefined) || undefined }));
}

function audienceToWire(a: Audience | undefined): unknown {
  return a && defined({ mode: a.mode, users: a.users, groups: a.groups });
}

function audienceFromWire(raw: unknown): Audience | undefined {
  if (!raw || typeof raw !== "object") return undefined;
  const a = raw as Wire;
  return defined({ mode: a.mode as Audience["mode"], users: a.users as string[] | undefined, groups: a.groups as string[] | undefined });
}

function channelToWire(c: Channel): Wire {
  return defined({
    name: c.name,
    default_level: c.defaultLevel,
    max_level: c.maxLevel,
    sound: c.sound,
    template_id: c.templateId,
    thread: c.thread,
    actions: actionsToWire(c.actions),
    audience: audienceToWire(c.audience),
  });
}

function channelFromWire(w: Wire): Channel {
  return defined({
    name: w.name as string,
    defaultLevel: w.default_level as Level,
    maxLevel: w.max_level as Level,
    sound: w.sound as string | undefined,
    templateId: w.template_id as string | undefined,
    thread: w.thread as string | undefined,
    actions: actionsFromWire(w.actions),
    audience: audienceFromWire(w.audience),
  });
}

function recipientsFromWire(raw: unknown): Recipient[] | undefined {
  if (!Array.isArray(raw)) return undefined;
  return raw.map((r: Wire) =>
    defined({
      externalUserId: r.external_user_id as string,
      messageId: r.message_id as string | undefined,
      status: r.status as string,
      devices: r.devices as number | undefined,
      deliveries: r.deliveries === undefined ? undefined : deliveryFromWire(r.deliveries),
    }),
  );
}

function groupFromWire(w: Wire): Group {
  return { id: w.id as string, members: (w.members as string[] | undefined) ?? [] };
}

function messageToWire(m: Message): Wire {
  return defined({
    external_user_id: m.externalUserId,
    external_user_ids: m.externalUserIds,
    to_audience: m.toAudience,
    channel: m.channel,
    level: m.level,
    title: m.title,
    subtitle: m.subtitle,
    body: m.body,
    image_url: m.imageUrl,
    template_id: m.templateId,
    data: m.data,
    sound: m.sound,
    actions: actionsToWire(m.actions),
    collapse_id: m.collapseId,
    page: m.page && defined({ interval_s: m.page.intervalSeconds, max_retries: m.page.maxRetries }),
  });
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
  async createPairingTicket(externalUserId: string, opts?: RequestOptions): Promise<PairingTicket> {
    if (externalUserId === "") throw new RangeError("hansho: id must not be empty");
    const w = await this.#json("POST", "/api/v1/pairing-tickets", { external_user_id: externalUserId }, opts);
    return { ticket: w.ticket as string, qr: w.qr as string, expiresAt: new Date(w.expires_at as string) };
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
    await this.#json("PUT", `/api/v1/channels/${seg(id)}`, channelToWire(channel), opts);
  }

  async getChannel(id: string, opts?: RequestOptions): Promise<Channel> {
    return channelFromWire(await this.#json("GET", `/api/v1/channels/${seg(id)}`, undefined, opts));
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
    return groupFromWire(await this.#json("PUT", `/api/v1/groups/${seg(id)}`, { members }, opts));
  }

  /** A missing group is a `HanshoError` with code `"unknown_group"`. */
  async getGroup(id: string, opts?: RequestOptions): Promise<Group> {
    return groupFromWire(await this.#json("GET", `/api/v1/groups/${seg(id)}`, undefined, opts));
  }

  /** An audience that still names the group matches nobody through it. */
  async deleteGroup(id: string, opts?: RequestOptions): Promise<void> {
    await this.#json("DELETE", `/api/v1/groups/${seg(id)}`, undefined, opts);
  }

  /** Stores a template version; the version must increase. */
  async putTemplate(id: string, version: number, body: unknown, opts?: RequestOptions): Promise<void> {
    await this.#json("PUT", `/api/v1/templates/${seg(id)}`, { version, body }, opts);
  }

  /** Uploads a sound (`audio/x-caf`, `audio/wav` or `audio/aiff`, at most 1 MB). */
  async putSound(id: string, contentType: string, bytes: Uint8Array | ArrayBuffer, opts?: RequestOptions): Promise<SoundResult> {
    const path = `/api/v1/sounds/${seg(id)}`;
    const data = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
    if (data.byteLength > MAX_SOUND_BYTES) throw new RangeError("hansho: sound exceeds 1 MB");
    const w = await this.#send({ method: "PUT", path, body: data, contentType, retry: true }, opts);
    return { id: w.id as string, filename: w.filename as string, sha256: w.sha256 as string, size: w.size as number };
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
      body: JSON.stringify(messageToWire(message)),
      contentType: "application/json",
      retry: false,
    };
    if (message.idempotencyKey) {
      call.retry = true;
      call.headers = { "Idempotency-Key": message.idempotencyKey };
    }
    const w = await this.#send(call, opts);
    return defined({
      messageId: (w.message_id as string | undefined) ?? "",
      level: w.level as Level,
      devices: (w.devices as number | undefined) ?? 0,
      status: (w.status as string | undefined) ?? "",
      deliveries: deliveryFromWire(w.deliveries),
      recipients: recipientsFromWire(w.recipients),
    });
  }

  async getMessage(id: string, opts?: RequestOptions): Promise<MessageStatus> {
    const w = await this.#json("GET", `/api/v1/messages/${seg(id)}`, undefined, opts);
    return defined({
      messageId: w.message_id as string,
      status: w.status as string,
      level: w.level as Level,
      attempts: w.attempts as number,
      deliveries: deliveryFromWire(w.deliveries),
      ackedAt: typeof w.acked_at === "string" ? new Date(w.acked_at) : undefined,
    });
  }

  // --- live activities and feeds -------------------------------------------

  async startActivity(activity: Activity, opts?: RequestOptions): Promise<ActivityStarted> {
    const w = await this.#json(
      "POST",
      "/api/v1/activities",
      defined({
        external_user_id: activity.externalUserId,
        channel: activity.channel,
        template_id: activity.templateId,
        data: activity.data,
        alert: alertToWire(activity.alert),
      }),
      opts,
    );
    return { activityId: w.activity_id as string, devices: w.devices as number };
  }

  /** Not retried automatically. */
  async updateActivity(id: string, update: ActivityUpdate, opts?: RequestOptions): Promise<ActivityUpdated> {
    const w = await this.#json(
      "PATCH",
      `/api/v1/activities/${seg(id)}`,
      defined({ data: update.data, priority: update.priority, alert: alertToWire(update.alert) }),
      opts,
    );
    return { activityId: w.activity_id as string, update: w.update as number };
  }

  async endActivity(id: string, opts?: RequestOptions): Promise<void> {
    await this.#json("DELETE", `/api/v1/activities/${seg(id)}`, undefined, opts);
  }

  async putFeed(id: string, feed: Feed, opts?: RequestOptions): Promise<FeedResult> {
    const w = await this.#json(
      "PUT",
      `/api/v1/feeds/${seg(id)}`,
      defined({
        external_user_id: feed.externalUserId,
        template_id: feed.templateId,
        title: feed.title,
        channel: feed.channel,
        audience: audienceToWire(feed.audience),
        data: feed.data,
      }),
      opts,
    );
    return { feedId: w.feed_id as string, devices: w.devices as number };
  }

  /** A feed's settings without its data. Omit `externalUserId` for the feed shared by all users. */
  async getFeed(id: string, externalUserId?: string, opts?: RequestOptions): Promise<FeedSettings> {
    const query = externalUserId ? `?external_user_id=${encodeURIComponent(externalUserId)}` : "";
    const w = await this.#json("GET", `/api/v1/feeds/${seg(id)}${query}`, undefined, opts);
    return defined({
      feedId: w.feed_id as string,
      externalUserId: w.external_user_id as string | undefined,
      templateId: w.template_id as string,
      title: w.title as string | undefined,
      channel: w.channel as string | undefined,
      audience: audienceFromWire(w.audience),
      updatedAt: w.updated_at as number,
    });
  }

  // --- plumbing ------------------------------------------------------------

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

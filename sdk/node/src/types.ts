/** How loudly a message is presented, lowest to highest. */
export type Level = "passive" | "active" | "time-sensitive" | "critical" | "page";

/** A button shown with a message or channel. An `ack` action acknowledges a page. */
export interface Action {
  id: string;
  title: string;
  ack?: boolean;
  kind?: string;
}

export interface PairingTicket {
  ticket: string;
  /** Render this string as a QR code for the user to scan in the app. */
  qr: string;
  expiresAt: Date;
}

/** How an {@link Audience} reads its lists: everyone, only the listed, or everyone except the listed. */
export type AudienceMode = "everyone" | "only" | "except";

/**
 * Who may use a channel or feed. `users` are your external user ids (they may not have paired yet) and `groups` are
 * group ids (see `putGroup`; a group that does not exist has no members). Both must be empty for `"everyone"`.
 */
export interface Audience {
  mode: AudienceMode;
  users?: string[];
  groups?: string[];
}

/** A named list of users, used in audiences. */
export interface Group {
  id: string;
  members: string[];
}

export interface Channel {
  name: string;
  defaultLevel: Level;
  maxLevel: Level;
  sound?: string;
  templateId?: string;
  thread?: string;
  actions?: Action[];
  /**
   * Who may use the channel. Leaving this out keeps the stored audience; send `{ mode: "everyone" }` to open the channel
   * up. `getChannel` always returns it.
   */
  audience?: Audience;
}

export interface PageOptions {
  /** Seconds between resends (hub default 120). */
  intervalSeconds?: number;
  /** Resends before the page expires (hub default 5). */
  maxRetries?: number;
}

export interface Message {
  /** Exactly one of `externalUserId`, `externalUserIds` and `toAudience` is required. */
  externalUserId?: string;
  /** Send to several users at once (at most 1000). */
  externalUserIds?: string[];
  /** Send to every paired user in the channel's audience, resolved when you send. */
  toAudience?: boolean;
  channel: string;
  level?: Level;
  title: string;
  subtitle?: string;
  body: string;
  imageUrl?: string;
  templateId?: string;
  /** Template data; sent as given (keys are never renamed). */
  data?: unknown;
  sound?: string;
  actions?: Action[];
  collapseId?: string;
  page?: PageOptions;
  /**
   * Opt in to retries of 429 and 503 for this send. Sent as the `Idempotency-Key`
   * header, never in the body. The hub ignores it today, so a retried send is
   * at-least-once.
   */
  idempotencyKey?: string;
}

export interface Delivery {
  deviceId: string;
  status: string;
  error?: string;
}

/** The `status` of a {@link Recipient} outside the channel's audience: it got no message. */
export const RECIPIENT_NO_ACCESS = "no_access";

/** The outcome of a send for one user. `status` is the message status (queued, undeliverable) or `"no_access"`, with no `messageId`. */
export interface Recipient {
  externalUserId: string;
  messageId?: string;
  status: string;
  devices?: number;
  deliveries?: Delivery[];
}

/**
 * For a send to `externalUserId` the fields describe that one recipient (`messageId` is `""`, `status` is
 * `"no_access"` and `devices` is 0 when the user is outside the audience). For several users or an audience
 * `messageId`, `devices` and `deliveries` are empty and each message is under `recipients`.
 */
export interface MessageResult {
  messageId: string;
  level: Level;
  devices: number;
  status: string;
  deliveries: Delivery[];
  /** One entry per user. Absent when talking to a hub that predates audiences. */
  recipients?: Recipient[];
}

/** `status` is one of queued, delivered, undeliverable, paging, acked, expired. */
export interface MessageStatus {
  messageId: string;
  status: string;
  level: Level;
  attempts: number;
  deliveries: Delivery[];
  ackedAt?: Date;
}

export interface ActivityAlert {
  title?: string;
  body?: string;
}

export interface Activity {
  externalUserId: string;
  channel: string;
  templateId: string;
  data: unknown;
  alert?: ActivityAlert;
}

export interface ActivityUpdate {
  data: unknown;
  /** 5 (default) or 10. */
  priority?: 5 | 10;
  alert?: ActivityAlert;
}

export interface ActivityStarted {
  activityId: string;
  devices: number;
}

export interface ActivityUpdated {
  activityId: string;
  update: number;
}

export interface Feed {
  /** Omit for a feed shared by all paired users. */
  externalUserId?: string;
  templateId: string;
  /** The name the app's feed picker shows instead of the id (at most 64 bytes). */
  title?: string;
  /** The feed uses this channel's audience unless it sets its own. Like `title`, replaced by every `putFeed`. */
  channel?: string;
  /** Who may see the feed. With neither this nor `channel` it is for everyone. */
  audience?: Audience;
  data: unknown;
}

/** A feed without its data, from `getFeed`. `audience` is absent while the feed inherits its channel's. */
export interface FeedSettings {
  feedId: string;
  externalUserId?: string;
  templateId: string;
  title?: string;
  channel?: string;
  audience?: Audience;
  updatedAt: number;
}

export interface FeedResult {
  feedId: string;
  devices: number;
}

export interface SoundResult {
  id: string;
  filename: string;
  sha256: string;
  size: number;
}

/** Per-call options. */
export interface RequestOptions {
  signal?: AbortSignal;
}

export interface HanshoOptions {
  /** The hub's public URL. */
  baseUrl: string;
  /** A sender API key from `hub apikey create`. */
  apiKey: string;
  /** Custom fetch (proxies, tests). Defaults to the global `fetch`. */
  fetch?: typeof fetch;
  /** Retries for GET/PUT/DELETE on 429 and 503. Default 3; 0 disables. */
  maxRetries?: number;
  /** Per-attempt timeout in milliseconds. Default 30000; 0 disables. */
  timeoutMs?: number;
  userAgent?: string;
  /** Replaces the retry wait; meant for tests. */
  sleep?: (ms: number, signal?: AbortSignal) => Promise<void>;
}

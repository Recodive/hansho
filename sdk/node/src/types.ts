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

export interface Channel {
  name: string;
  defaultLevel: Level;
  maxLevel: Level;
  sound?: string;
  templateId?: string;
  thread?: string;
  actions?: Action[];
}

export interface PageOptions {
  /** Seconds between resends (hub default 120). */
  intervalSeconds?: number;
  /** Resends before the page expires (hub default 5). */
  maxRetries?: number;
}

export interface Message {
  externalUserId: string;
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

export interface MessageResult {
  messageId: string;
  level: Level;
  devices: number;
  status: string;
  deliveries: Delivery[];
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
  data: unknown;
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

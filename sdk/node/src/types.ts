/** Options of `createPairingTicket`. */
export interface PairingTicketOptions extends RequestOptions {
  /** What other people see for this user instead of their id (max 40 characters, any language). */
  displayName?: string;
}

/** The `status` of a {@link Recipient} outside the channel's audience: it got no message. */
export const RECIPIENT_NO_ACCESS = "no_access";

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

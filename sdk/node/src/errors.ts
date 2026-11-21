/** A non-2xx answer from the hub. The API key never appears in it. */
export class HanshoError extends Error {
  readonly status: number;
  /** The hub's error code, e.g. `invalid_request`; empty when the body was not JSON. */
  readonly code: string;
  /** The offending request field for validation failures. */
  readonly field?: string;
  /** Parsed `Retry-After`, in milliseconds. */
  readonly retryAfterMs?: number;
  /** The hub's validation message, when present. */
  readonly detail?: string;

  constructor(init: {
    method: string;
    path: string;
    status: number;
    code?: string;
    field?: string;
    retryAfterMs?: number;
    detail?: string;
  }) {
    let text = `hansho: ${init.method} ${init.path}: ${init.status}`;
    if (init.code) text += ` ${init.code}`;
    if (init.field) text += ` (field ${init.field})`;
    if (init.retryAfterMs) text += ` (retry after ${init.retryAfterMs} ms)`;
    super(text);
    this.name = "HanshoError";
    this.status = init.status;
    this.code = init.code ?? "";
    if (init.field) this.field = init.field;
    if (init.retryAfterMs) this.retryAfterMs = init.retryAfterMs;
    if (init.detail) this.detail = init.detail;
  }
}

/** No HTTP answer arrived: connection failure, timeout or abort. See `cause`. */
export class HanshoNetworkError extends Error {
  constructor(method: string, path: string, cause: unknown) {
    super(`hansho: ${method} ${path}: ${cause instanceof Error ? cause.message : String(cause)}`, { cause });
    this.name = "HanshoNetworkError";
  }
}

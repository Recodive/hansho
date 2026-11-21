export const DEFAULT_MAX_RETRIES = 3;
const BASE_BACKOFF_MS = 250;
const MAX_BACKOFF_MS = 8000;
/** A longer Retry-After is surfaced to the caller instead of waited out. */
export const MAX_RETRY_WAIT_MS = 60_000;

export function isIdempotent(method: string): boolean {
  return method === "GET" || method === "PUT" || method === "DELETE";
}

export function isRetryableStatus(status: number): boolean {
  return status === 429 || status === 503;
}

/** Exponential backoff with jitter in [d/2, d]. */
export function backoffMs(attempt: number): number {
  const d = Math.min(BASE_BACKOFF_MS * 2 ** Math.min(attempt, 10), MAX_BACKOFF_MS);
  return d / 2 + Math.random() * (d / 2);
}

/** Reads delay-seconds or an HTTP date; returns 0 when absent or unusable. */
export function parseRetryAfter(value: string | null, now = Date.now()): number {
  if (!value) return 0;
  const v = value.trim();
  if (/^\d+(\.\d+)?$/.test(v)) return Math.round(Number(v) * 1000);
  const at = Date.parse(v);
  return Number.isNaN(at) ? 0 : Math.max(0, at - now);
}

export function defaultSleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) return reject(signal.reason);
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(signal?.reason);
    };
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

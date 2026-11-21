from __future__ import annotations

import random
import re
from datetime import datetime, timezone
from email.utils import parsedate_to_datetime
from typing import Optional

DEFAULT_MAX_RETRIES = 3
BASE_BACKOFF = 0.25
MAX_BACKOFF = 8.0
#: A longer Retry-After is surfaced to the caller instead of waited out (seconds).
MAX_RETRY_WAIT = 60.0
#: parse_retry_after never returns more than this (about 31 years), in seconds.
MAX_RETRY_AFTER = 1e9

_DELAY_SECONDS = re.compile(r"[0-9]+(\.[0-9]+)?")


def is_idempotent(method: str) -> bool:
    return method in ("GET", "PUT", "DELETE")


def is_retryable_status(status: int) -> bool:
    return status in (429, 503)


def backoff(attempt: int) -> float:
    """Exponential backoff with jitter in [d/2, d], in seconds."""
    d = float(min(BASE_BACKOFF * 2 ** min(attempt, 10), MAX_BACKOFF))
    return d / 2 + random.random() * (d / 2)


def parse_retry_after(value: Optional[str], now: Optional[float] = None) -> float:
    """Reads delay-seconds or an HTTP date; returns 0 when absent or unusable."""
    if not value:
        return 0.0
    v = value.strip()
    if _DELAY_SECONDS.fullmatch(v):
        return min(float(v), MAX_RETRY_AFTER)  # a silly value must not become inf
    try:
        at = parsedate_to_datetime(v)
    except (TypeError, ValueError, IndexError):
        return 0.0
    if at.tzinfo is None:
        at = at.replace(tzinfo=timezone.utc)
    ref = datetime.now(timezone.utc).timestamp() if now is None else now
    return max(0.0, at.timestamp() - ref)

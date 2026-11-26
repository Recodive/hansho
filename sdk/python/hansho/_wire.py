"""Readers and writers the generated models (_types_gen.py) are built from.

Answers are read leniently: an unknown field is ignored, an unknown enum value stays the raw string, and a
missing or mistyped value becomes the field's empty value (or None for an optional field). Only a timestamp
that is present but not RFC 3339 is an error.
"""

from __future__ import annotations

import math
import re
from datetime import datetime, timedelta, timezone
from typing import Any, Callable, Dict, List, Optional, TypeVar

T = TypeVar("T")

_TIME = re.compile(
    r"([0-9]{4})-([0-9]{2})-([0-9]{2})[Tt ]([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]+))?([Zz]|[+-][0-9]{2}:[0-9]{2})"
)


def _s(v: Any) -> str:
    if isinstance(v, str):
        return v
    if v is None:
        return ""
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, float) and v.is_integer():
        return str(int(v))
    return str(v)


def _opt_s(v: Any) -> Optional[str]:
    return v if isinstance(v, str) else None


def _i(v: Any) -> int:
    if isinstance(v, bool):
        return int(v)
    if isinstance(v, (int, float)):
        out: Any = v
    else:
        try:
            out = float(v) if v is not None else 0
        except (TypeError, ValueError):
            return 0
    if isinstance(out, float):
        return int(out) if math.isfinite(out) else 0
    return out  # type: ignore[no-any-return]


def _opt_i(v: Any) -> Optional[int]:
    return None if v is None or isinstance(v, (str, bool, list, dict)) else _i(v)


def _b(v: Any) -> bool:
    return v if isinstance(v, bool) else False


def _opt_b(v: Any) -> Optional[bool]:
    return v if isinstance(v, bool) else None


def _enum(v: Any) -> Any:
    """An enum value as sent, known or not."""
    return _s(v)


def _opt_enum(v: Any) -> Any:
    return _opt_s(v)


def _ie(v: Any) -> Any:
    return _i(v)


def _opt_ie(v: Any) -> Any:
    return _opt_i(v)


def _any(v: Any) -> Any:
    return v


def _time(v: Any) -> datetime:
    """Parses an RFC 3339 timestamp (datetime.fromisoformat cannot before Python 3.11)."""
    m = _TIME.fullmatch(v) if isinstance(v, str) else None
    if m is None:
        raise ValueError("invalid timestamp")
    y, mo, d, h, mi, s, frac, tz = m.groups()
    micro = int((frac or "0")[:6].ljust(6, "0"))
    if tz in ("Z", "z"):
        zone = timezone.utc
    else:
        off = timedelta(hours=int(tz[1:3]), minutes=int(tz[4:6]))
        zone = timezone(off if tz[0] == "+" else -off)
    return datetime(int(y), int(mo), int(d), int(h), int(mi), int(s), micro, tzinfo=zone)


def _opt_time(v: Any) -> Optional[datetime]:
    return _time(v) if isinstance(v, str) else None


def _obj(v: Any, read: Callable[[Any], T]) -> Optional[T]:
    return read(v) if isinstance(v, dict) else None


def _list(v: Any, read: Callable[[Any], T]) -> List[T]:
    return [read(x) for x in v] if isinstance(v, list) else []


def _drop_none(o: Dict[str, Any]) -> Dict[str, Any]:
    return {k: v for k, v in o.items() if v is not None}


def _iso(v: Optional[datetime]) -> Optional[str]:
    return None if v is None else v.isoformat()


def _enc(v: Any, write: Callable[[Any], T]) -> Optional[T]:
    return None if v is None else write(v)


def _arr(v: Any, write: Optional[Callable[[Any], Any]] = None, keep_empty: bool = False) -> Optional[List[Any]]:
    """A list for the wire; an empty optional one is left out."""
    if v is None:
        return None
    out = [x if write is None else write(x) for x in v]
    return out if out or keep_empty else None

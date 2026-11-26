from __future__ import annotations

import hmac
import json
import math
import re
from dataclasses import dataclass
from datetime import datetime, timezone
from hashlib import sha256
from typing import Any, Dict, List, Optional, Tuple, Union

from ._types import Level

#: ``X-Hansho-Webhook-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>``
WEBHOOK_SIGNATURE_HEADER = "X-Hansho-Webhook-Signature"

#: Default allowed distance between the signed timestamp and now, in seconds.
DEFAULT_TOLERANCE = 300.0

_INT = re.compile(r"-?[0-9]{1,18}")
_HEX_SHA256 = re.compile(r"[0-9a-fA-F]{64}")


class WebhookError(Exception):
    """Raised by :func:`verify_webhook`; check ``code``.

    ``code`` is one of ``malformed_header``, ``bad_signature``, ``stale``,
    ``malformed_body``, ``invalid_body``, ``invalid_secret``.
    """

    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        self.code = code


@dataclass(frozen=True)
class PairingCreated:
    pairing_id: str
    external_user_id: str
    device_name: str


@dataclass(frozen=True)
class PairingRevoked:
    pairing_id: str
    external_user_id: str
    #: Who revoked: "sender", "device" or "relay".
    by: str


@dataclass(frozen=True)
class MessageDelivered:
    message_id: str
    external_user_id: str
    channel: str
    level: Level
    devices: int


@dataclass(frozen=True)
class MessageUndeliverable:
    message_id: str
    external_user_id: str
    channel: str
    #: "no_devices" or "all_deliveries_failed".
    reason: str


@dataclass(frozen=True)
class MessageAcked:
    message_id: str
    external_user_id: str
    device_id: str
    #: Who acknowledged: their external user id (differs from external_user_id when a page went to several people).
    acked_by: Optional[str] = None
    #: The acknowledger's display name; only when they have one.
    acked_by_name: Optional[str] = None


@dataclass(frozen=True)
class MessageDismissed:
    """The person swiped away a message that asked to be acknowledged, without acknowledging it."""

    message_id: str
    external_user_id: str
    device_id: str


@dataclass(frozen=True)
class MessageExpired:
    message_id: str
    external_user_id: str
    channel: str
    attempts: int


@dataclass(frozen=True)
class ActionTaken:
    """A button press."""

    message_id: str
    action_id: str
    device_id: str
    external_user_id: str
    #: Reply text of a text action.
    text: Optional[str] = None


@dataclass(frozen=True)
class ActivityEnded:
    activity_id: str
    external_user_id: str
    channel: str
    reason: str


Payload = Union[
    PairingCreated,
    PairingRevoked,
    MessageDelivered,
    MessageUndeliverable,
    MessageAcked,
    MessageDismissed,
    MessageExpired,
    ActionTaken,
    ActivityEnded,
]


@dataclass(frozen=True)
class Event:
    """A verified webhook.

    ``type`` is the event name (``pairing.created``, ``message.acked``, ``action``,
    ...) and ``data`` the matching payload dataclass. An event this SDK does not
    know has ``type == "unknown"``, the name as sent in ``name``, and the payload
    as parsed JSON, untouched, in ``data``.
    """

    type: str
    #: The hub's event id.
    id: str
    created_at: datetime
    #: The verified request body.
    raw: bytes
    data: Any
    #: The event name as sent; only set when ``type == "unknown"``.
    name: Optional[str] = None


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


def _n(v: Any) -> Union[int, float]:
    if isinstance(v, bool):
        return int(v)
    if isinstance(v, (int, float)):
        out: Union[int, float] = v
    else:
        try:
            out = float(v) if v is not None else 0
        except (TypeError, ValueError):
            out = 0
    if isinstance(out, float):
        if not math.isfinite(out):
            return 0
        if out.is_integer():
            return int(out)
    return out


def _payload(name: str, d: Dict[str, Any]) -> Optional[Payload]:
    if name == "pairing.created":
        return PairingCreated(_s(d.get("pairing_id")), _s(d.get("external_user_id")), _s(d.get("device_name")))
    if name == "pairing.revoked":
        return PairingRevoked(_s(d.get("pairing_id")), _s(d.get("external_user_id")), _s(d.get("by")))
    if name == "message.delivered":
        return MessageDelivered(
            _s(d.get("message_id")),
            _s(d.get("external_user_id")),
            _s(d.get("channel")),
            _s(d.get("level")),  # type: ignore[arg-type]
            int(_n(d.get("devices"))),
        )
    if name == "message.undeliverable":
        return MessageUndeliverable(_s(d.get("message_id")), _s(d.get("external_user_id")), _s(d.get("channel")), _s(d.get("reason")))
    if name == "message.acked":
        return MessageAcked(
            _s(d.get("message_id")),
            _s(d.get("external_user_id")),
            _s(d.get("device_id")),
            d["acked_by"] if isinstance(d.get("acked_by"), str) else None,
            d["acked_by_name"] if isinstance(d.get("acked_by_name"), str) else None,
        )
    if name == "message.dismissed":
        return MessageDismissed(_s(d.get("message_id")), _s(d.get("external_user_id")), _s(d.get("device_id")))
    if name == "message.expired":
        return MessageExpired(_s(d.get("message_id")), _s(d.get("external_user_id")), _s(d.get("channel")), int(_n(d.get("attempts"))))
    if name == "action":
        text = d.get("text")
        return ActionTaken(
            _s(d.get("message_id")),
            _s(d.get("action_id")),
            _s(d.get("device_id")),
            _s(d.get("external_user_id")),
            text if isinstance(text, str) else None,
        )
    if name == "activity.ended":
        return ActivityEnded(_s(d.get("activity_id")), _s(d.get("external_user_id")), _s(d.get("channel")), _s(d.get("reason")))
    return None


def _parse_header(header: Any) -> Tuple[str, List[str]]:
    if not isinstance(header, str) or header.strip() == "":
        raise WebhookError("malformed_header", "missing signature header")
    t: Optional[str] = None
    sigs: List[str] = []
    for part in header.split(","):
        trimmed = part.strip()
        k, eq, v = trimmed.partition("=")
        if not eq:
            raise WebhookError("malformed_header", "signature header part is not key=value")
        if k == "t":
            if t is not None:
                raise WebhookError("malformed_header", "duplicate timestamp")
            t = v
        elif k == "v1" and v != "":
            sigs.append(v)
    if t is None or not _INT.fullmatch(t) or not sigs:
        raise WebhookError("malformed_header", "signature header needs t=<unix> and v1=<hex>")
    return t, sigs


def verify_webhook(
    *,
    secret: Union[str, bytes],
    header: Optional[str],
    body: Union[bytes, bytearray, memoryview, str],
    now: Union[datetime, float, int, None] = None,
    tolerance: Optional[float] = None,
) -> Event:
    """Verifies a webhook against its raw body and returns the event.

    :param secret: the shared secret (``HUB_WEBHOOK_SECRET``).
    :param header: the ``X-Hansho-Webhook-Signature`` request header (``None`` if absent).
    :param body: the request body exactly as received (bytes, or a str that is
        encoded as UTF-8). A body that a framework parsed and you re-serialised
        will not verify, and a parsed object is rejected.
    :param now: the current time as a datetime or epoch seconds. Default: now.
    :param tolerance: allowed distance in seconds between the signed timestamp
        and now. Default 300; a value <= 0 means the default.
    :raises WebhookError: see :class:`WebhookError` for the ``code`` values. With
        several ``v1`` values in the header (secret rotation) any match is accepted.
    """
    if not isinstance(secret, (str, bytes)) or len(secret) == 0:
        raise WebhookError("invalid_secret", "webhook secret is empty")
    if not isinstance(body, (bytes, bytearray, memoryview, str)):
        raise WebhookError("invalid_body", "body must be the raw request body (bytes or str), not a parsed object")
    t, sigs = _parse_header(header)
    try:
        raw = body.encode("utf-8") if isinstance(body, str) else bytes(body)
    except UnicodeEncodeError:  # a str with lone surrogates cannot be a request body
        raise WebhookError("invalid_body", "body is not valid text") from None
    key = secret.encode("utf-8") if isinstance(secret, str) else secret
    mac = hmac.new(key, t.encode("ascii") + b"." + raw, sha256).digest()
    match = False
    for sig in sigs:  # no early exit: every candidate is compared
        if _HEX_SHA256.fullmatch(sig) and hmac.compare_digest(bytes.fromhex(sig), mac):
            match = True
    if not match:
        raise WebhookError("bad_signature", "signature does not match")

    if now is None:
        now_s = datetime.now(timezone.utc).timestamp()
    elif isinstance(now, datetime):
        now_s = (now if now.tzinfo is not None else now.replace(tzinfo=timezone.utc)).timestamp()
    else:
        now_s = float(now)
    tol = tolerance if tolerance is not None and tolerance > 0 else DEFAULT_TOLERANCE
    if abs(now_s - int(t)) > tol:
        raise WebhookError("stale", "timestamp outside tolerance")

    try:
        env = json.loads(raw.decode("utf-8"))
        if not isinstance(env, dict):
            raise ValueError("not an object")
    except ValueError:  # includes UnicodeDecodeError and JSONDecodeError
        raise WebhookError("malformed_body", "body is not a webhook envelope") from None
    name = env.get("event")
    if not isinstance(name, str) or name == "":
        raise WebhookError("malformed_body", "body has no event")
    try:
        created = datetime.fromtimestamp(_n(env.get("created_at")), tz=timezone.utc)
    except (OverflowError, OSError, ValueError):
        raise WebhookError("malformed_body", "created_at is out of range") from None
    raw_data = env.get("data")
    data = raw_data if isinstance(raw_data, dict) else {}
    mapped = _payload(name, data)
    if mapped is None:
        return Event(type="unknown", id=_s(env.get("id")), created_at=created, raw=raw, data=raw_data, name=name)
    return Event(type=name, id=_s(env.get("id")), created_at=created, raw=raw, data=mapped)

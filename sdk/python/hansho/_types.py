from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from typing import Any, Callable, List, Literal, Mapping, Optional, Tuple


#: How loudly a message is presented, lowest to highest.
Level = Literal["passive", "active", "time-sensitive", "critical", "page"]
LEVELS: Tuple[str, ...] = ("passive", "active", "time-sensitive", "critical", "page")


@dataclass
class Action:
    """A button shown with a message or channel. An ``ack`` action acknowledges a page."""

    id: str
    title: str
    ack: Optional[bool] = None
    kind: Optional[str] = None


@dataclass(frozen=True)
class PairingTicket:
    ticket: str
    #: Render this string as a QR code for the user to scan in the app.
    qr: str
    expires_at: datetime


@dataclass
class Channel:
    name: str
    default_level: Level
    max_level: Level
    sound: Optional[str] = None
    template_id: Optional[str] = None
    thread: Optional[str] = None
    actions: Optional[List[Action]] = None


@dataclass
class PageOptions:
    #: Seconds between resends (hub default 120).
    interval_seconds: Optional[int] = None
    #: Resends before the page expires (hub default 5).
    max_retries: Optional[int] = None


@dataclass
class Message:
    external_user_id: str
    channel: str
    title: str
    body: str
    level: Optional[Level] = None
    subtitle: Optional[str] = None
    image_url: Optional[str] = None
    template_id: Optional[str] = None
    #: Template data; sent as given (keys are never renamed). ``None`` leaves it out.
    data: Any = None
    sound: Optional[str] = None
    actions: Optional[List[Action]] = None
    collapse_id: Optional[str] = None
    page: Optional[PageOptions] = None
    #: Opt in to retries of 429 and 503 for this send. Sent as the
    #: ``Idempotency-Key`` header, never in the body. The hub ignores it today,
    #: so a retried send is at-least-once.
    idempotency_key: Optional[str] = None


@dataclass(frozen=True)
class Delivery:
    device_id: str
    status: str
    error: Optional[str] = None


@dataclass(frozen=True)
class MessageResult:
    message_id: str
    level: str
    devices: int
    status: str
    deliveries: List[Delivery]


@dataclass(frozen=True)
class MessageStatus:
    """``status`` is one of queued, delivered, undeliverable, paging, acked, expired."""

    message_id: str
    status: str
    level: str
    attempts: int
    deliveries: List[Delivery]
    acked_at: Optional[datetime] = None


@dataclass
class ActivityAlert:
    title: Optional[str] = None
    body: Optional[str] = None


@dataclass
class Activity:
    external_user_id: str
    channel: str
    template_id: str
    data: Any
    alert: Optional[ActivityAlert] = None


@dataclass
class ActivityUpdate:
    data: Any
    #: 5 (default) or 10.
    priority: Optional[int] = None
    alert: Optional[ActivityAlert] = None


@dataclass(frozen=True)
class ActivityStarted:
    activity_id: str
    devices: int


@dataclass(frozen=True)
class ActivityUpdated:
    activity_id: str
    update: int


@dataclass
class Feed:
    template_id: str
    data: Any
    #: Omit for a feed shared by all paired users.
    external_user_id: Optional[str] = None
    #: The name the app's feed picker shows instead of the id (at most 64 bytes).
    title: Optional[str] = None


@dataclass(frozen=True)
class FeedResult:
    feed_id: str
    devices: int


@dataclass(frozen=True)
class SoundResult:
    id: str
    filename: str
    sha256: str
    size: int


@dataclass(frozen=True)
class TransportRequest:
    """What the client hands to its transport."""

    method: str
    #: Absolute URL, path already escaped.
    url: str
    headers: Mapping[str, str]
    body: Optional[bytes]
    #: Per-attempt timeout in seconds; ``None`` means no timeout.
    timeout: Optional[float]


@dataclass(frozen=True)
class TransportResponse:
    status: int
    #: Header names in lower case.
    headers: Mapping[str, str]
    body: bytes


#: A transport performs one HTTP round trip. It returns the response for *any*
#: status (4xx and 5xx included) and raises for failures without an answer.
Transport = Callable[[TransportRequest], TransportResponse]

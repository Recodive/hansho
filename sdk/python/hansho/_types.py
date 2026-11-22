from __future__ import annotations

from dataclasses import dataclass, field
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


#: How an :class:`Audience` reads its lists: everyone, only the listed, or everyone except the listed.
AudienceMode = Literal["everyone", "only", "except"]


@dataclass
class Audience:
    """Who may use a channel or feed.

    ``users`` are your external user ids (they may not have paired yet) and ``groups`` are group ids
    (see :meth:`Hansho.put_group`; every group must exist when you save). Both must be empty for ``"everyone"``.
    """

    mode: AudienceMode
    users: List[str] = field(default_factory=list)
    groups: List[str] = field(default_factory=list)


@dataclass(frozen=True)
class Group:
    """A named list of users, used in audiences."""

    id: str
    members: List[str]


@dataclass
class Channel:
    name: str
    default_level: Level
    max_level: Level
    sound: Optional[str] = None
    template_id: Optional[str] = None
    thread: Optional[str] = None
    actions: Optional[List[Action]] = None
    #: Who may use the channel. ``None`` keeps the audience stored on the hub (a new channel is open to
    #: everyone); send ``Audience("everyone")`` to open a channel up again. ``get_channel`` always returns one.
    audience: Optional[Audience] = None


@dataclass
class PageOptions:
    #: Seconds between resends (hub default 120).
    interval_seconds: Optional[int] = None
    #: Resends before the page expires (hub default 5).
    max_retries: Optional[int] = None


@dataclass
class Message:
    """A notification for one user, for several, or for a channel's audience.

    Give exactly one of ``external_user_id``, ``external_user_ids`` and ``to_audience``; ``channel``, ``title`` and
    ``body`` are required too (they default to ``""`` only so that ``external_user_id`` can stay the first
    positional argument; the hub refuses a send without them). Users outside the channel's audience are not
    delivered to: their entry in :attr:`MessageResult.recipients` has status ``"no_access"``.
    """

    external_user_id: Optional[str] = None
    channel: str = ""
    title: str = ""
    body: str = ""
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
    #: Send to several users at once (at most 1000).
    external_user_ids: Optional[List[str]] = None
    #: Send to every paired user in the channel's audience, resolved when you send.
    to_audience: Optional[bool] = None


@dataclass(frozen=True)
class Delivery:
    device_id: str
    status: str
    error: Optional[str] = None


#: The ``status`` of a :class:`Recipient` outside the channel's audience: it got no message.
RECIPIENT_NO_ACCESS = "no_access"


@dataclass(frozen=True)
class Recipient:
    """The outcome of a send for one user. ``status`` is the message status (queued, undeliverable) or
    ``"no_access"``, in which case there is no ``message_id``."""

    external_user_id: str
    status: str
    message_id: Optional[str] = None
    devices: int = 0
    deliveries: List[Delivery] = field(default_factory=list)


@dataclass(frozen=True)
class MessageResult:
    """The answer to ``send_message``.

    For a send to ``external_user_id`` the other fields describe that one recipient (``message_id`` is ``""``,
    ``status`` is ``"no_access"`` and ``devices`` is 0 when the user is outside the audience). For several users
    or an audience ``message_id``, ``devices`` and ``deliveries`` are empty and each message is under
    ``recipients``.
    """

    message_id: str
    level: str
    devices: int
    status: str
    deliveries: List[Delivery]
    #: One entry per user; empty when talking to a hub that predates audiences.
    recipients: List[Recipient] = field(default_factory=list)


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
    #: The feed uses this channel's audience unless it sets its own.
    channel: Optional[str] = None
    #: Who may see the feed. With neither this nor ``channel`` it is for everyone; leaving both out keeps the
    #: values stored on the hub.
    audience: Optional[Audience] = None


@dataclass(frozen=True)
class FeedSettings:
    """A feed without its data, from ``get_feed``. ``audience`` is ``None`` while the feed inherits its channel's."""

    feed_id: str
    template_id: str
    updated_at: int
    external_user_id: Optional[str] = None
    title: Optional[str] = None
    channel: Optional[str] = None
    audience: Optional[Audience] = None


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

"""Official Python client for a Hansho hub's sender API.

    from hansho import Hansho, Message

    hansho = Hansho("https://hub.example.com", api_key)
    hansho.send_message(Message(external_user_id="user-42", channel="ops", title="Disk full", body="db-1 is at 97%"))

Webhooks are verified with :func:`verify_webhook`. Standard library only.
"""

from ._client import NAME, VERSION, Hansho, urllib_transport
from ._errors import HanshoError, HanshoNetworkError
from ._types import (
    LEVELS,
    Action,
    Activity,
    ActivityAlert,
    ActivityStarted,
    ActivityUpdate,
    ActivityUpdated,
    Channel,
    Delivery,
    Feed,
    FeedResult,
    Level,
    Message,
    MessageResult,
    MessageStatus,
    PageOptions,
    PairingTicket,
    SoundResult,
    Transport,
    TransportRequest,
    TransportResponse,
)
from ._version import __version__
from ._webhook import (
    DEFAULT_TOLERANCE,
    WEBHOOK_SIGNATURE_HEADER,
    ActionTaken,
    ActivityEnded,
    Event,
    MessageAcked,
    MessageDelivered,
    MessageDismissed,
    MessageExpired,
    MessageUndeliverable,
    PairingCreated,
    PairingRevoked,
    Payload,
    WebhookError,
    verify_webhook,
)

__all__ = [
    "LEVELS",
    "DEFAULT_TOLERANCE",
    "NAME",
    "VERSION",
    "WEBHOOK_SIGNATURE_HEADER",
    "Action",
    "ActionTaken",
    "Activity",
    "ActivityAlert",
    "ActivityEnded",
    "ActivityStarted",
    "ActivityUpdate",
    "ActivityUpdated",
    "Channel",
    "Delivery",
    "Event",
    "Feed",
    "FeedResult",
    "Hansho",
    "HanshoError",
    "HanshoNetworkError",
    "Level",
    "Message",
    "MessageAcked",
    "MessageDelivered",
    "MessageDismissed",
    "MessageExpired",
    "MessageResult",
    "MessageStatus",
    "MessageUndeliverable",
    "PageOptions",
    "PairingCreated",
    "PairingRevoked",
    "PairingTicket",
    "Payload",
    "SoundResult",
    "Transport",
    "TransportRequest",
    "TransportResponse",
    "WebhookError",
    "__version__",
    "urllib_transport",
    "verify_webhook",
]

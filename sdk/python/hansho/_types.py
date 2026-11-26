from __future__ import annotations

from dataclasses import dataclass
from typing import Callable, Mapping, Optional, Tuple

from ._types_gen import LEVEL_VALUES

#: The levels, lowest to highest. Same as ``LEVEL_VALUES``.
LEVELS: Tuple[str, ...] = LEVEL_VALUES

#: The ``status`` of a :class:`Recipient` outside the channel's audience: it got no message.
RECIPIENT_NO_ACCESS = "no_access"


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

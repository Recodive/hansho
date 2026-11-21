from __future__ import annotations

from typing import Optional


#: Error codes of the hub that callers commonly branch on (``HanshoError.code``).
#: 403: the user is outside the channel's audience (``start_activity`` for such a user).
CODE_NO_ACCESS = "no_access"
#: 404: the group does not exist (``get_group``, ``delete_group``).
CODE_UNKNOWN_GROUP = "unknown_group"


class HanshoError(Exception):
    """A non-2xx answer from the hub. The API key never appears in it."""

    status: int
    """The HTTP status."""
    code: str
    """The hub's error code, e.g. ``invalid_request``; empty when the body was not JSON."""
    field: Optional[str]
    """The offending request field for validation failures."""
    retry_after: Optional[float]
    """Parsed ``Retry-After``, in seconds."""
    detail: Optional[str]
    """The hub's validation message, when present."""

    def __init__(
        self,
        method: str,
        path: str,
        status: int,
        code: Optional[str] = None,
        field: Optional[str] = None,
        retry_after: Optional[float] = None,
        detail: Optional[str] = None,
    ) -> None:
        text = f"hansho: {method} {path}: {status}"
        if code:
            text += f" {code}"
        if field:
            text += f" (field {field})"
        if retry_after:
            text += f" (retry after {round(retry_after * 1000)} ms)"
        super().__init__(text)
        self.status = status
        self.code = code or ""
        self.field = field or None
        self.retry_after = retry_after or None
        self.detail = detail or None


class HanshoNetworkError(Exception):
    """No HTTP answer arrived: connection failure, timeout or an unreadable answer.

    It is deliberately not a :class:`HanshoError`. The underlying exception is
    ``__cause__``.
    """

    def __init__(self, method: str, path: str, cause: BaseException) -> None:
        super().__init__(f"hansho: {method} {path}: {cause}")
        self.__cause__ = cause

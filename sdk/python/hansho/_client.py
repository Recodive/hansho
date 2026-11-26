from __future__ import annotations

import json
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Callable, Dict, List, Optional, Sequence, TypeVar, Union

from ._errors import HanshoError, HanshoNetworkError
from ._retry import (
    DEFAULT_MAX_RETRIES,
    MAX_RETRY_WAIT,
    backoff,
    is_idempotent,
    is_retryable_status,
    parse_retry_after,
)
from ._types import Transport, TransportRequest, TransportResponse
from ._types_gen import (
    Activity,
    ActivityStarted,
    ActivityUpdate,
    ActivityUpdated,
    Channel,
    Feed,
    FeedResult,
    FeedSettings,
    Group,
    Message,
    MessageResult,
    MessageStatus,
    PairingTicket,
    Sound,
    SoundResult,
    Template,
    from_wire_activity_started,
    from_wire_activity_updated,
    from_wire_channel,
    from_wire_feed_result,
    from_wire_feed_settings,
    from_wire_group,
    from_wire_message_result,
    from_wire_message_status,
    from_wire_pairing_ticket,
    from_wire_sound,
    from_wire_sound_result,
    from_wire_template,
    to_wire_activity,
    to_wire_activity_update,
    to_wire_channel,
    to_wire_feed,
    to_wire_message,
)
from ._version import __version__
from ._wire import _drop_none, _list

#: Product codename.
NAME = "hansho"
#: SDK version, sent in the default User-Agent.
VERSION = __version__

MAX_SOUND_BYTES = 1 << 20

Wire = Dict[str, Any]
T = TypeVar("T")


# --- transport ---------------------------------------------------------------


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """The hub never redirects; following one could hand the API key to another host."""

    def redirect_request(self, *args: Any, **kwargs: Any) -> None:
        return None


_OPENER = urllib.request.build_opener(_NoRedirect)


def _lower(headers: Any) -> Dict[str, str]:
    return {k.lower(): v for k, v in headers.items()}


def urllib_transport(req: TransportRequest) -> TransportResponse:
    """The default transport: :mod:`urllib.request`, without redirect following."""
    r = urllib.request.Request(req.url, data=req.body, headers=dict(req.headers), method=req.method)
    try:
        with _OPENER.open(r, timeout=req.timeout) as resp:
            return TransportResponse(resp.status, _lower(resp.headers), resp.read())
    except urllib.error.HTTPError as e:
        try:
            body = e.read()
        finally:
            e.close()
        return TransportResponse(e.code, _lower(e.headers), body)


# --- paths ---------------------------------------------------------------------


def _seg(id: str) -> str:
    """Escapes one path segment; rejects ids that would not stay in their collection."""
    if not isinstance(id, str):
        raise TypeError("hansho: id must be a str")
    if id == "":
        raise ValueError("hansho: id must not be empty")
    if id in (".", ".."):
        raise ValueError(f'hansho: id must not be "{id}"')
    return urllib.parse.quote(id, safe="")


def _group(w: Any, id: str) -> Group:
    g = from_wire_group(w)
    if g.id == "":
        g.id = id
    return g


class _Call:
    __slots__ = ("method", "path", "body", "content_type", "retry", "headers")

    def __init__(
        self,
        method: str,
        path: str,
        body: Optional[bytes] = None,
        content_type: Optional[str] = None,
        retry: bool = False,
        headers: Optional[Dict[str, str]] = None,
    ) -> None:
        self.method = method
        self.path = path
        self.body = body
        self.content_type = content_type
        self.retry = retry
        self.headers = headers or {}


class Hansho:
    """Client for a Hansho hub's sender API.

    :param base_url: the hub's public URL.
    :param api_key: a sender API key from ``hub apikey create``.
    :param max_retries: retries for GET/PUT/DELETE on 429 and 503. Default 3; 0 disables.
    :param timeout: per-attempt socket timeout in seconds. Default 30; ``None`` or 0 disables.
    :param user_agent: replaces the default ``hansho-python/<version>``.
    :param transport: replaces the HTTP round trip (proxies, tests); see :data:`hansho.Transport`.
    :param sleep: replaces the retry wait (takes seconds); meant for tests.

    Errors: a non-2xx answer raises :class:`HanshoError`; a failure without an
    answer raises :class:`HanshoNetworkError`; bad arguments raise ``ValueError``
    or ``TypeError`` before anything is sent.
    """

    def __init__(
        self,
        base_url: str,
        api_key: str,
        *,
        max_retries: int = DEFAULT_MAX_RETRIES,
        timeout: Optional[float] = 30.0,
        user_agent: Optional[str] = None,
        transport: Optional[Transport] = None,
        sleep: Optional[Callable[[float], None]] = None,
    ) -> None:
        if not isinstance(base_url, str) or base_url == "":
            raise ValueError("hansho: base_url is required")
        if not isinstance(api_key, str) or api_key == "":
            raise ValueError("hansho: api_key is required")
        self._base_url = base_url.rstrip("/")
        self._api_key = api_key
        self._max_retries = max(0, max_retries)
        self._timeout = timeout if timeout else None
        self._user_agent = user_agent or f"{NAME}-python/{VERSION}"
        self._transport = transport or urllib_transport
        self._sleep = sleep or time.sleep

    def __repr__(self) -> str:
        """Keeps the API key out of logs."""
        return f"Hansho(base_url={self._base_url!r})"

    # --- pairing -------------------------------------------------------------

    def create_pairing_ticket(self, external_user_id: str, display_name: Optional[str] = None) -> PairingTicket:
        """Creates a single-use pairing ticket (valid 10 minutes).

        Render ``qr`` as a QR code for the user to scan in the Hansho app. ``display_name`` (when not
        ``None``) is what other people see for the user instead of their id; ``""`` is sent as given.
        """
        if external_user_id == "":
            raise ValueError("hansho: id must not be empty")
        return self._json(
            "POST",
            "/api/v1/pairing-tickets",
            _drop_none({"external_user_id": external_user_id, "display_name": display_name}),
            from_wire_pairing_ticket,
        )

    def set_user_display_name(self, external_user_id: str, display_name: str) -> None:
        """Sets what other people see for the user instead of their external user id.

        Any language, trimmed, at most 40 characters, no control characters. ``""`` clears it. The user does
        not need to be paired yet.
        """
        self._json_void("PUT", f"/api/v1/users/{_seg(external_user_id)}", {"display_name": display_name})

    def unpair(self, external_user_id: str) -> None:
        """Revokes every pairing of the user."""
        self._json_void("DELETE", f"/api/v1/users/{_seg(external_user_id)}/pairings")

    def kick(self, external_user_id: str) -> None:
        """Ends the user's whole pairing, on every device: :meth:`unpair` under its sender-side name.

        The app tells the person that the sender ended it (a 401 ``pairing_revoked`` with reason ``"kicked"``),
        where an unpair they did themselves carries no reason. There is no block list; a new ticket pairs them again.
        """
        self.unpair(external_user_id)

    # --- catalog -------------------------------------------------------------

    def put_channel(self, id: str, channel: Channel) -> None:
        self._json_void("PUT", f"/api/v1/channels/{_seg(id)}", to_wire_channel(channel))

    def get_channel(self, id: str) -> Channel:
        return self._json("GET", f"/api/v1/channels/{_seg(id)}", None, from_wire_channel)

    def list_channels(self) -> List[Channel]:
        """Every channel."""
        return self._json("GET", "/api/v1/channels", None, lambda w: _list(w, from_wire_channel))

    def delete_channel(self, id: str) -> None:
        self._json_void("DELETE", f"/api/v1/channels/{_seg(id)}")

    # --- templates and sounds ------------------------------------------------

    def put_template(self, id: str, version: int, body: Any) -> None:
        """Stores a template version; the version must increase."""
        self._json_void("PUT", f"/api/v1/templates/{_seg(id)}", {"version": version, "body": body})

    def list_templates(self) -> List[Template]:
        """Every template at its latest version."""
        return self._json("GET", "/api/v1/templates", None, lambda w: _list(w, from_wire_template))

    def get_template(self, id: str) -> Template:
        """A missing template is a :class:`HanshoError` with code ``"unknown_template"``."""
        return self._json("GET", f"/api/v1/templates/{_seg(id)}", None, from_wire_template)

    def delete_template(self, id: str) -> None:
        """A template a live channel still names is refused with a :class:`HanshoError`, code ``"in_use"``."""
        self._json_void("DELETE", f"/api/v1/templates/{_seg(id)}")

    def put_sound(self, id: str, content_type: str, data: Union[bytes, bytearray, memoryview]) -> SoundResult:
        """Uploads a sound (``audio/x-caf``, ``audio/wav`` or ``audio/aiff``, at most 1 MB)."""
        path = f"/api/v1/sounds/{_seg(id)}"
        raw = bytes(data)
        if len(raw) > MAX_SOUND_BYTES:
            raise ValueError("hansho: sound exceeds 1 MB")
        call = _Call("PUT", path, raw, content_type, retry=True)
        return self._do(call, from_wire_sound_result)

    def list_sounds(self) -> List[Sound]:
        """Every sound's metadata."""
        return self._json("GET", "/api/v1/sounds", None, lambda w: _list(w, from_wire_sound))

    def get_sound(self, id: str) -> Sound:
        """A missing sound is a :class:`HanshoError` with code ``"unknown_sound"``."""
        return self._json("GET", f"/api/v1/sounds/{_seg(id)}", None, from_wire_sound)

    def delete_sound(self, id: str) -> None:
        """A sound a live channel still names is refused with a :class:`HanshoError`, code ``"in_use"``."""
        self._json_void("DELETE", f"/api/v1/sounds/{_seg(id)}")

    # --- groups --------------------------------------------------------------

    def list_groups(self) -> List[Group]:
        """Every group."""
        return self._json("GET", "/api/v1/groups", None, lambda w: _list(w, from_wire_group))

    def put_group(self, id: str, members: Sequence[str]) -> Group:
        """Creates a group or replaces its whole member list (external user ids, who may not have paired yet).

        Audiences that name the group follow at once. Ids match ``[A-Za-z0-9][A-Za-z0-9_-]{0,63}``.
        """
        return self._json("PUT", f"/api/v1/groups/{_seg(id)}", {"members": list(members)}, lambda w: _group(w, id))

    def get_group(self, id: str) -> Group:
        """A missing group is a :class:`HanshoError` with code ``"unknown_group"``."""
        return self._json("GET", f"/api/v1/groups/{_seg(id)}", None, lambda w: _group(w, id))

    def delete_group(self, id: str) -> None:
        """An audience that still names the group matches nobody through it."""
        self._json_void("DELETE", f"/api/v1/groups/{_seg(id)}")

    # --- messages ------------------------------------------------------------

    def send_message(self, message: Message) -> MessageResult:
        """Sends a notification, or a page when ``level`` is ``"page"``.

        It is never retried automatically (a repeat would notify twice) unless
        the message has an ``idempotency_key``.
        """
        call = _Call("POST", "/api/v1/messages", _dumps(to_wire_message(message)), "application/json")
        if message.idempotency_key:
            call.retry = True
            call.headers = {"Idempotency-Key": message.idempotency_key}
        return self._do(call, from_wire_message_result)

    def get_message(self, id: str) -> MessageStatus:
        return self._json("GET", f"/api/v1/messages/{_seg(id)}", None, from_wire_message_status)

    # --- live activities and feeds -------------------------------------------

    def start_activity(self, activity: Activity) -> ActivityStarted:
        return self._json("POST", "/api/v1/activities", to_wire_activity(activity), from_wire_activity_started)

    def update_activity(self, id: str, update: ActivityUpdate) -> ActivityUpdated:
        """Not retried automatically."""
        return self._json("PATCH", f"/api/v1/activities/{_seg(id)}", to_wire_activity_update(update), from_wire_activity_updated)

    def end_activity(self, id: str) -> None:
        self._json_void("DELETE", f"/api/v1/activities/{_seg(id)}")

    def put_feed(self, id: str, feed: Feed) -> FeedResult:
        return self._json("PUT", f"/api/v1/feeds/{_seg(id)}", to_wire_feed(feed), from_wire_feed_result)

    def get_feed(self, id: str, external_user_id: Optional[str] = None) -> FeedSettings:
        """A feed's settings without its data. Omit `external_user_id` for the feed shared by all users."""
        query = f"?external_user_id={urllib.parse.quote(external_user_id, safe='')}" if external_user_id else ""
        return self._json("GET", f"/api/v1/feeds/{_seg(id)}{query}", None, from_wire_feed_settings)

    # --- plumbing ------------------------------------------------------------

    @staticmethod
    def _build(method: str, path: str, body: Any = None) -> _Call:
        call = _Call(method, path, retry=is_idempotent(method))
        if body is not None:
            call.body = _dumps(body)
            call.content_type = "application/json"
        return call

    def _json(self, method: str, path: str, body: Any, parse: Callable[[Any], T]) -> T:
        return self._do(self._build(method, path, body), parse)

    def _json_void(self, method: str, path: str, body: Any = None) -> None:
        self._send(self._build(method, path, body))

    def _do(self, call: _Call, parse: Callable[[Any], T]) -> T:
        wire = self._send(call)
        try:
            return parse(wire)
        except (ValueError, TypeError, AttributeError, KeyError, OverflowError):
            raise HanshoNetworkError(call.method, call.path, ValueError("unexpected response shape")) from None

    def _send(self, call: _Call) -> Any:
        attempt = 0
        while True:
            res = self._round_trip(call)
            if 200 <= res.status < 300:
                return self._parse_body(call, res)
            err = self._api_error(call, res)
            if not call.retry or not is_retryable_status(res.status) or attempt >= self._max_retries:
                raise err
            wait = err.retry_after if err.retry_after is not None else backoff(attempt)
            if wait > MAX_RETRY_WAIT:
                raise err
            self._sleep(wait)
            attempt += 1

    def _parse_body(self, call: _Call, res: TransportResponse) -> Any:
        try:
            text = res.body.decode("utf-8")
            if text.strip() == "":
                return {}
            parsed = json.loads(text)
        except ValueError:  # UnicodeDecodeError and JSONDecodeError; the body is not quoted
            raise HanshoNetworkError(call.method, call.path, ValueError("invalid JSON in response")) from None
        return parsed if isinstance(parsed, (dict, list)) else {}

    def _round_trip(self, call: _Call) -> TransportResponse:
        headers = {
            "Authorization": f"Bearer {self._api_key}",
            "Accept": "application/json",
            "User-Agent": self._user_agent,
        }
        headers.update(call.headers)
        if call.content_type:
            headers["Content-Type"] = call.content_type
        req = TransportRequest(call.method, self._base_url + call.path, headers, call.body, self._timeout)
        failure: Optional[Exception] = None
        try:
            res = self._transport(req)
        except Exception as e:
            failure = e
        if failure is not None:  # raised outside the except block: no chained context holding the key
            cause = self._redact(failure)
            raise HanshoNetworkError(call.method, call.path, cause) from cause
        if not isinstance(res.status, int) or not 100 <= res.status <= 599:
            raise HanshoNetworkError(call.method, call.path, ValueError("invalid status in response"))
        return TransportResponse(res.status, {k.lower(): v for k, v in res.headers.items()}, res.body)

    def _redact(self, cause: Exception) -> Exception:
        if self._api_key in str(cause):
            return Exception(str(cause).replace(self._api_key, "[redacted]"))
        return cause

    def _api_error(self, call: _Call, res: TransportResponse) -> HanshoError:
        code: Optional[str] = None
        field: Optional[str] = None
        detail: Optional[str] = None
        try:
            p = json.loads(res.body.decode("utf-8"))
        except ValueError:
            p = None  # not JSON (proxy error page): status only
        if isinstance(p, dict):
            if isinstance(p.get("error"), str):
                code = p["error"]
            if isinstance(p.get("field"), str) and p["field"]:
                field = p["field"]
            if isinstance(p.get("message"), str):
                detail = p["message"].replace(self._api_key, "[redacted]")
        retry_after = parse_retry_after(res.headers.get("retry-after"))
        return HanshoError(call.method, call.path, res.status, code, field, retry_after or None, detail)


def _dumps(o: Any) -> bytes:
    return json.dumps(o, ensure_ascii=False, separators=(",", ":"), allow_nan=False).encode("utf-8")

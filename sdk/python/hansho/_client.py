from __future__ import annotations

import json
import time
import urllib.error
import urllib.parse
import urllib.request
import re
from datetime import datetime, timedelta, timezone
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
from ._types import (
    Action,
    Activity,
    ActivityAlert,
    ActivityStarted,
    ActivityUpdate,
    ActivityUpdated,
    Audience,
    Channel,
    Delivery,
    Feed,
    FeedResult,
    FeedSettings,
    Group,
    Message,
    MessageResult,
    MessageStatus,
    PairingTicket,
    Recipient,
    SoundResult,
    Transport,
    TransportRequest,
    TransportResponse,
)
from ._version import __version__

#: Product codename.
NAME = "hansho"
#: SDK version, sent in the default User-Agent.
VERSION = __version__

MAX_SOUND_BYTES = 1 << 20

Wire = Dict[str, Any]
T = TypeVar("T")

_TIME = re.compile(
    r"([0-9]{4})-([0-9]{2})-([0-9]{2})[Tt ]([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]+))?([Zz]|[+-][0-9]{2}:[0-9]{2})"
)


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


# --- wire conversion -----------------------------------------------------------


def _seg(id: str) -> str:
    """Escapes one path segment; rejects ids that would not stay in their collection."""
    if not isinstance(id, str):
        raise TypeError("hansho: id must be a str")
    if id == "":
        raise ValueError("hansho: id must not be empty")
    if id in (".", ".."):
        raise ValueError(f'hansho: id must not be "{id}"')
    return urllib.parse.quote(id, safe="")


def _drop_none(o: Wire) -> Wire:
    return {k: v for k, v in o.items() if v is not None}


def _actions_to_wire(actions: Optional[List[Action]]) -> Optional[List[Wire]]:
    if actions is None:
        return None
    return [_drop_none({"id": a.id, "title": a.title, "ack": a.ack, "kind": a.kind}) for a in actions]


def _actions_from_wire(raw: Any) -> Optional[List[Action]]:
    if not isinstance(raw, list):
        return None
    out = []
    for a in raw:
        ack = a.get("ack")
        kind = a.get("kind")
        out.append(Action(_s(a.get("id")), _s(a.get("title")), ack if isinstance(ack, bool) else None, kind if isinstance(kind, str) else None))
    return out


def _alert_to_wire(a: Optional[ActivityAlert]) -> Optional[Wire]:
    return None if a is None else _drop_none({"title": a.title, "body": a.body})


def _s(v: Any) -> str:
    return v if isinstance(v, str) else ("" if v is None else str(v))


def _opt_s(v: Any) -> Optional[str]:
    return v if isinstance(v, str) else None


def _i(v: Any) -> int:
    if isinstance(v, bool):
        return int(v)
    if isinstance(v, (int, float)):
        return int(v)
    return 0


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


def _deliveries(raw: Any) -> List[Delivery]:
    if not isinstance(raw, list):
        return []
    return [Delivery(_s(d.get("device_id")), _s(d.get("status")), _s(d.get("error")) or None) for d in raw]


def _audience_to_wire(a: Optional[Audience]) -> Optional[Wire]:
    if a is None:
        return None
    return _drop_none({"mode": a.mode, "users": list(a.users) or None, "groups": list(a.groups) or None})


def _audience_from_wire(raw: Any) -> Optional[Audience]:
    if not isinstance(raw, dict):
        return None
    return Audience(_s(raw.get("mode")), _strs(raw.get("users")), _strs(raw.get("groups")))  # type: ignore[arg-type]


def _strs(raw: Any) -> List[str]:
    return [x for x in raw if isinstance(x, str)] if isinstance(raw, list) else []


def _recipients(raw: Any) -> List[Recipient]:
    if not isinstance(raw, list):
        return []
    return [
        Recipient(_s(r.get("external_user_id")), _s(r.get("status")), _opt_s(r.get("message_id")), _i(r.get("devices")), _deliveries(r.get("deliveries")))
        for r in raw
        if isinstance(r, dict)
    ]


def _channel_to_wire(c: Channel) -> Wire:
    return _drop_none(
        {
            "name": c.name,
            "default_level": c.default_level,
            "max_level": c.max_level,
            "sound": c.sound,
            "template_id": c.template_id,
            "thread": c.thread,
            "actions": _actions_to_wire(c.actions),
            "audience": _audience_to_wire(c.audience),
        }
    )


def _channel_from_wire(w: Wire) -> Channel:
    return Channel(
        name=_s(w.get("name")),
        default_level=_s(w.get("default_level")),  # type: ignore[arg-type]
        max_level=_s(w.get("max_level")),  # type: ignore[arg-type]
        sound=_opt_s(w.get("sound")),
        template_id=_opt_s(w.get("template_id")),
        thread=_opt_s(w.get("thread")),
        actions=_actions_from_wire(w.get("actions")),
        audience=_audience_from_wire(w.get("audience")),
    )


def _group(w: Wire, id: str) -> Group:
    return Group(_s(w.get("id")) or id, _strs(w.get("members")))


def _message_to_wire(m: Message) -> Wire:
    page = None
    if m.page is not None:
        page = _drop_none({"interval_s": m.page.interval_seconds, "max_retries": m.page.max_retries})
    return _drop_none(
        {
            "external_user_id": m.external_user_id,
            "external_user_ids": None if m.external_user_ids is None else list(m.external_user_ids),
            "to_audience": True if m.to_audience else None,
            "channel": m.channel,
            "level": m.level,
            "title": m.title,
            "subtitle": m.subtitle,
            "body": m.body,
            "image_url": m.image_url,
            "template_id": m.template_id,
            "data": m.data,
            "sound": m.sound,
            "actions": _actions_to_wire(m.actions),
            "collapse_id": m.collapse_id,
            "page": page,
        }
    )


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

    def create_pairing_ticket(self, external_user_id: str) -> PairingTicket:
        """Creates a single-use pairing ticket (valid 10 minutes).

        Render ``qr`` as a QR code for the user to scan in the Hansho app.
        """
        if external_user_id == "":
            raise ValueError("hansho: id must not be empty")
        return self._json(
            "POST",
            "/api/v1/pairing-tickets",
            {"external_user_id": external_user_id},
            lambda w: PairingTicket(_s(w.get("ticket")), _s(w.get("qr")), _time(w.get("expires_at"))),
        )

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
        self._json_void("PUT", f"/api/v1/channels/{_seg(id)}", _channel_to_wire(channel))

    def get_channel(self, id: str) -> Channel:
        return self._json("GET", f"/api/v1/channels/{_seg(id)}", None, _channel_from_wire)

    def delete_channel(self, id: str) -> None:
        self._json_void("DELETE", f"/api/v1/channels/{_seg(id)}")

    # --- groups --------------------------------------------------------------

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

    def put_template(self, id: str, version: int, body: Any) -> None:
        """Stores a template version; the version must increase."""
        self._json_void("PUT", f"/api/v1/templates/{_seg(id)}", {"version": version, "body": body})

    def put_sound(self, id: str, content_type: str, data: Union[bytes, bytearray, memoryview]) -> SoundResult:
        """Uploads a sound (``audio/x-caf``, ``audio/wav`` or ``audio/aiff``, at most 1 MB)."""
        path = f"/api/v1/sounds/{_seg(id)}"
        raw = bytes(data)
        if len(raw) > MAX_SOUND_BYTES:
            raise ValueError("hansho: sound exceeds 1 MB")
        call = _Call("PUT", path, raw, content_type, retry=True)
        return self._do(call, lambda w: SoundResult(_s(w.get("id")), _s(w.get("filename")), _s(w.get("sha256")), _i(w.get("size"))))

    # --- messages ------------------------------------------------------------

    def send_message(self, message: Message) -> MessageResult:
        """Sends a notification, or a page when ``level`` is ``"page"``.

        It is never retried automatically (a repeat would notify twice) unless
        the message has an ``idempotency_key``.
        """
        call = _Call("POST", "/api/v1/messages", _dumps(_message_to_wire(message)), "application/json")
        if message.idempotency_key:
            call.retry = True
            call.headers = {"Idempotency-Key": message.idempotency_key}
        return self._do(
            call,
            lambda w: MessageResult(
                _s(w.get("message_id")),
                _s(w.get("level")),
                _i(w.get("devices")),
                _s(w.get("status")),
                _deliveries(w.get("deliveries")),
                _recipients(w.get("recipients")),
            ),
        )

    def get_message(self, id: str) -> MessageStatus:
        return self._json(
            "GET",
            f"/api/v1/messages/{_seg(id)}",
            None,
            lambda w: MessageStatus(
                _s(w.get("message_id")),
                _s(w.get("status")),
                _s(w.get("level")),
                _i(w.get("attempts")),
                _deliveries(w.get("deliveries")),
                _time(w["acked_at"]) if isinstance(w.get("acked_at"), str) else None,
            ),
        )

    # --- live activities and feeds -------------------------------------------

    def start_activity(self, activity: Activity) -> ActivityStarted:
        return self._json(
            "POST",
            "/api/v1/activities",
            _drop_none(
                {
                    "external_user_id": activity.external_user_id,
                    "channel": activity.channel,
                    "template_id": activity.template_id,
                    "data": activity.data,
                    "alert": _alert_to_wire(activity.alert),
                }
            ),
            lambda w: ActivityStarted(_s(w.get("activity_id")), _i(w.get("devices"))),
        )

    def update_activity(self, id: str, update: ActivityUpdate) -> ActivityUpdated:
        """Not retried automatically."""
        return self._json(
            "PATCH",
            f"/api/v1/activities/{_seg(id)}",
            _drop_none({"data": update.data, "priority": update.priority, "alert": _alert_to_wire(update.alert)}),
            lambda w: ActivityUpdated(_s(w.get("activity_id")), _i(w.get("update"))),
        )

    def end_activity(self, id: str) -> None:
        self._json_void("DELETE", f"/api/v1/activities/{_seg(id)}")

    def put_feed(self, id: str, feed: Feed) -> FeedResult:
        return self._json(
            "PUT",
            f"/api/v1/feeds/{_seg(id)}",
            _drop_none(
                {
                    "external_user_id": feed.external_user_id,
                    "template_id": feed.template_id,
                    "title": feed.title,
                    "channel": feed.channel,
                    "audience": _audience_to_wire(feed.audience),
                    "data": feed.data,
                }
            ),
            lambda w: FeedResult(_s(w.get("feed_id")), _i(w.get("devices"))),
        )

    def get_feed(self, id: str, external_user_id: Optional[str] = None) -> FeedSettings:
        """A feed's settings without its data. Omit ``external_user_id`` for the feed shared by all users."""
        query = f"?external_user_id={urllib.parse.quote(external_user_id, safe='')}" if external_user_id else ""
        return self._json(
            "GET",
            f"/api/v1/feeds/{_seg(id)}{query}",
            None,
            lambda w: FeedSettings(
                _s(w.get("feed_id")),
                _s(w.get("template_id")),
                _i(w.get("updated_at")),
                _opt_s(w.get("external_user_id")) or None,
                _opt_s(w.get("title")) or None,
                _opt_s(w.get("channel")) or None,
                _audience_from_wire(w.get("audience")),
            ),
        )

    # --- plumbing ------------------------------------------------------------

    @staticmethod
    def _build(method: str, path: str, body: Any = None) -> _Call:
        call = _Call(method, path, retry=is_idempotent(method))
        if body is not None:
            call.body = _dumps(body)
            call.content_type = "application/json"
        return call

    def _json(self, method: str, path: str, body: Any, parse: Callable[[Wire], T]) -> T:
        return self._do(self._build(method, path, body), parse)

    def _json_void(self, method: str, path: str, body: Any = None) -> None:
        self._send(self._build(method, path, body))

    def _do(self, call: _Call, parse: Callable[[Wire], T]) -> T:
        wire = self._send(call)
        try:
            return parse(wire)
        except (ValueError, TypeError, AttributeError, KeyError, OverflowError):
            raise HanshoNetworkError(call.method, call.path, ValueError("unexpected response shape")) from None

    def _send(self, call: _Call) -> Wire:
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

    def _parse_body(self, call: _Call, res: TransportResponse) -> Wire:
        try:
            text = res.body.decode("utf-8")
            if text.strip() == "":
                return {}
            parsed = json.loads(text)
        except ValueError:  # UnicodeDecodeError and JSONDecodeError; the body is not quoted
            raise HanshoNetworkError(call.method, call.path, ValueError("invalid JSON in response")) from None
        return parsed if isinstance(parsed, dict) else {}

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

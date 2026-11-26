import json
import socket
import threading
import time
import unittest
from datetime import datetime, timedelta, timezone
from email.utils import format_datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, List
from urllib.parse import unquote

from hansho import (
    Action,
    NAME,
    VERSION,
    ActivityUpdate,
    Channel,
    Feed,
    Hansho,
    HanshoError,
    HanshoNetworkError,
    Message,
    PageOptions,
    TransportRequest,
    TransportResponse,
)
from hansho._retry import parse_retry_after

from .helpers import TEST_KEY, Hub, Recorded, Reply

CHANNEL = Channel("Ops", "active", "critical")
MSG = Message(external_user_id="u", channel="c", title="t", body="b")


class Basics(unittest.TestCase):
    def test_headers_and_default_user_agent(self) -> None:
        with Hub() as h:
            h.client.put_channel("ops", CHANNEL)
            r = h.requests[0]
            self.assertEqual(r.headers["authorization"], f"Bearer {TEST_KEY}")
            self.assertEqual(r.headers["accept"], "application/json")
            self.assertEqual(r.headers["content-type"], "application/json")
            self.assertTrue(r.headers["user-agent"].startswith("hansho-python/"), r.headers["user-agent"])
            h.new_client(user_agent="acme/2").end_activity("a")
            self.assertEqual(h.requests[1].headers["user-agent"], "acme/2")
            self.assertNotIn("content-type", h.requests[1].headers)

    def test_trailing_slash_and_path_prefix_in_base_url(self) -> None:
        with Hub() as h:
            Hansho(f"{h.url}/hansho/", TEST_KEY).get_message("m1")
            self.assertEqual(h.requests[0].url, "/hansho/api/v1/messages/m1")

    def test_constants(self) -> None:
        self.assertEqual(NAME, "hansho")
        self.assertRegex(VERSION, r"^[0-9]+\.[0-9]+\.[0-9]+")

    def test_requires_base_url_and_api_key(self) -> None:
        with self.assertRaises(ValueError):
            Hansho("", "k")
        with self.assertRaises(ValueError):
            Hansho("http://x", "")

    def test_does_not_expose_the_key_in_repr(self) -> None:
        c = Hansho("http://x", TEST_KEY)
        self.assertNotIn(TEST_KEY, repr(c))
        self.assertNotIn(TEST_KEY, str(c))

    def test_unset_optional_fields_are_left_out_of_the_wire(self) -> None:
        with Hub() as h:
            h.client.send_message(MSG)
            self.assertEqual(json.loads(h.requests[0].body), {"external_user_id": "u", "channel": "c", "title": "t", "body": "b"})
            h.client.put_channel("ops", CHANNEL)
            self.assertEqual(json.loads(h.requests[1].body), {"name": "Ops", "default_level": "active", "max_level": "critical"})
            h.client.put_feed("f", Feed("t", 1))
            self.assertEqual(json.loads(h.requests[2].body), {"template_id": "t", "data": 1})

    def test_zero_and_empty_are_sent_as_given(self) -> None:
        with Hub() as h:
            h.client.send_message(Message(external_user_id="u", channel="c", title="t", body="", level="page", page=PageOptions(max_retries=0), data=0))
            body = json.loads(h.requests[0].body)
            self.assertEqual(body["page"], {"max_retries": 0})
            self.assertEqual(body["body"], "")
            self.assertEqual(body["data"], 0)

    def test_non_ascii_body_is_utf8(self) -> None:
        with Hub() as h:
            h.client.send_message(Message(external_user_id="u", channel="c", title="Täst 日本", body="b"))
            self.assertIn("Täst 日本".encode("utf-8"), h.requests[0].body)

    def test_unserialisable_data_fails_before_sending(self) -> None:
        with Hub() as h:
            for bad in (object(), float("nan")):
                with self.assertRaises((TypeError, ValueError)):
                    h.client.send_message(Message(external_user_id="u", channel="c", title="t", body="b", data=bad))
            self.assertEqual(h.requests, [])

    def test_sound_over_1mb_is_refused_before_sending(self) -> None:
        with Hub() as h:
            with self.assertRaises(ValueError):
                h.client.put_sound("ding", "audio/wav", bytes((1 << 20) + 1))
            h.client.put_sound("ding", "audio/wav", bytes(1 << 20))
            self.assertEqual(len(h.requests), 1)


class ForwardCompatibility(unittest.TestCase):
    def test_ignores_response_fields_it_does_not_know(self) -> None:
        def serve(_r: Recorded, reply: Reply) -> None:
            reply.body = (
                '{"message_id":"m","status":"delivered","level":"page","attempts":1,'
                '"deliveries":[{"device_id":"d","status":"sent","brand_new":{"x":[1]}}],"brand_new":"v","acked_at":"2026-11-08T12:05:00Z"}'
            )

        with Hub(serve) as h:
            ms = h.client.get_message("m")
            self.assertEqual(ms.message_id, "m")
            self.assertEqual(len(ms.deliveries), 1)
            self.assertFalse(hasattr(ms, "brand_new"))

    def test_timestamps_with_fractions_and_offsets(self) -> None:
        for raw, want in (
            ("2026-11-08T12:05:00.123456789Z", datetime(2026, 11, 8, 12, 5, 0, 123456, tzinfo=timezone.utc)),
            ("2026-11-08T14:05:00+02:00", datetime(2026, 11, 8, 12, 5, tzinfo=timezone.utc)),
            ("2026-11-08T12:05:00.5z", datetime(2026, 11, 8, 12, 5, 0, 500000, tzinfo=timezone.utc)),
        ):
            body = json.dumps({"message_id": "m", "acked_at": raw})
            with Hub(lambda _r, reply, body=body: setattr(reply, "body", body)) as h:
                self.assertEqual(h.client.get_message("m").acked_at, want)

    def test_an_unusable_answer_is_a_network_error(self) -> None:
        for body in ("not json", '{"ticket":"t","qr":"q","expires_at":"yesterday"}'):
            with Hub(lambda _r, reply, body=body: setattr(reply, "body", body)) as h:
                with self.assertRaises(HanshoNetworkError) as cm:
                    h.client.create_pairing_ticket("u")
                self.assertNotIn("not json", str(cm.exception))

    def test_empty_and_non_object_success_bodies(self) -> None:
        for body in ("", "  ", "[]", "null", "5"):
            with Hub(lambda _r, reply, body=body: setattr(reply, "body", body)) as h:
                h.client.put_channel("ops", CHANNEL)
                self.assertIsNone(h.client.send_message(MSG).message_id)


class GeneratedModels(unittest.TestCase):
    def test_an_unknown_enum_value_and_an_unknown_field_are_read_as_they_come(self) -> None:
        body = '{"id":"ops","name":"Ops","default_level":"loud","max_level":"page","audience":{"mode":"sometimes","new":1},"later":true}'
        with Hub(lambda _r, reply: setattr(reply, "body", body)) as h:
            c = h.client.get_channel("ops")
        self.assertEqual((c.id, c.default_level, c.max_level), ("ops", "loud", "page"))
        assert c.audience is not None
        self.assertEqual(c.audience.mode, "sometimes")

    def test_a_list_answer_that_is_not_a_list_is_empty(self) -> None:
        with Hub(lambda _r, reply: setattr(reply, "body", "{}")) as h:
            self.assertEqual(h.client.list_templates(), [])
            self.assertEqual(h.client.list_groups(), [])

    def test_an_empty_optional_list_is_left_out_and_a_url_action_is_sent(self) -> None:
        with Hub() as h:
            h.client.put_channel(
                "ops", Channel("Ops", "active", "page", actions=[Action("a", "Docs", kind="url", url="https://example.com")])
            )
            h.client.put_channel("ops", Channel("Ops", "active", "page", actions=[]))
        self.assertEqual(json.loads(h.requests[0].body)["actions"], [{"id": "a", "title": "Docs", "kind": "url", "url": "https://example.com"}])
        self.assertNotIn("actions", json.loads(h.requests[1].body))


class Retries(unittest.TestCase):
    def test_put_is_retried_on_429_and_honours_retry_after(self) -> None:
        n = [0]

        def serve(_r: Recorded, reply: Reply) -> None:
            n[0] += 1
            if n[0] <= 2:
                reply.status, reply.headers, reply.body = 429, {"Retry-After": "2"}, '{"error":"rate_limited"}'

        with Hub(serve) as h:
            h.client.put_channel("ops", CHANNEL)
            self.assertEqual(len(h.requests), 3)
            self.assertEqual(h.waits, [2, 2])

    def test_http_date_retry_after(self) -> None:
        n = [0]

        def serve(_r: Recorded, reply: Reply) -> None:
            n[0] += 1
            if n[0] == 1:
                reply.status = 429
                reply.headers = {"Retry-After": format_datetime(datetime.now(timezone.utc) + timedelta(seconds=30), usegmt=True)}

        with Hub(serve) as h:
            h.client.delete_channel("ops")
            self.assertEqual(len(h.waits), 1)
            self.assertTrue(20 <= h.waits[0] <= 31, h.waits)

    def test_parse_retry_after(self) -> None:
        self.assertEqual(parse_retry_after(None), 0)
        self.assertEqual(parse_retry_after("7"), 7)
        self.assertEqual(parse_retry_after(" 1.5 "), 1.5)
        self.assertEqual(parse_retry_after("garbage"), 0)
        self.assertEqual(parse_retry_after("-3"), 0)
        self.assertEqual(parse_retry_after("9" * 400), 1e9)  # no inf: HanshoError must stay constructible
        self.assertEqual(parse_retry_after("٣"), 0)  # an Arabic-Indic digit is not delay-seconds
        self.assertEqual(parse_retry_after("Wed, 21 Oct 2015 07:28:00 GMT", now=1445412480 - 10), 10)
        self.assertEqual(parse_retry_after("Wed, 21 Oct 2015 07:28:00 GMT", now=1445412480 + 10), 0)

    def test_stops_after_max_retries_and_surfaces_the_error(self) -> None:
        def serve(_r: Recorded, reply: Reply) -> None:
            reply.status, reply.headers, reply.body = 429, {"Retry-After": "1"}, '{"error":"rate_limited"}'

        with Hub(serve) as h:
            with self.assertRaises(HanshoError) as cm:
                h.client.put_channel("ops", CHANNEL)
            self.assertEqual((cm.exception.status, cm.exception.retry_after), (429, 1.0))
            self.assertEqual(len(h.requests), 4)
            with self.assertRaises(HanshoError):
                h.new_client(max_retries=0).put_channel("ops", CHANNEL)
            self.assertEqual(len(h.requests), 5)
            with self.assertRaises(HanshoError):
                h.new_client(max_retries=1).put_channel("ops", CHANNEL)
            self.assertEqual(len(h.requests), 7)

    def test_get_and_delete_on_503_use_jittered_backoff(self) -> None:
        seen = set()

        def serve(r: Recorded, reply: Reply) -> None:
            key = (r.method, r.url)
            if key not in seen:
                seen.add(key)
                reply.status, reply.body = 503, '{"error":"unavailable"}'

        with Hub(serve) as h:
            h.client.get_channel("x")
            h.client.delete_channel("x")
            h.client.end_activity("x")
            self.assertEqual(len(h.waits), 3)
            for w in h.waits:
                self.assertTrue(0.125 <= w <= 0.25, w)

    def test_backoff_grows_and_is_capped(self) -> None:
        with Hub(lambda _r, reply: setattr(reply, "status", 503)) as h:
            with self.assertRaises(HanshoError):
                h.new_client(max_retries=8).get_channel("x")
            self.assertEqual(len(h.waits), 8)
            for attempt, w in enumerate(h.waits):
                d = min(0.25 * 2**attempt, 8.0)
                self.assertTrue(d / 2 <= w <= d, (attempt, w))

    def test_other_statuses_are_not_retried(self) -> None:
        for status in (400, 401, 404, 409, 500, 502):
            with Hub(lambda _r, reply, status=status: setattr(reply, "status", status)) as h:
                with self.assertRaises(HanshoError):
                    h.client.put_channel("ops", CHANNEL)
                self.assertEqual(len(h.requests), 1, f"status {status}")

    def test_post_messages_is_never_retried_by_default(self) -> None:
        for status in (429, 503):

            def serve(_r: Recorded, reply: Reply, status: int = status) -> None:
                reply.status, reply.headers, reply.body = status, {"Retry-After": "3"}, '{"error":"rate_limited"}'

            with Hub(serve) as h:
                with self.assertRaises(HanshoError) as cm:
                    h.client.send_message(MSG)
                e = cm.exception
                self.assertEqual((e.status, e.code, e.retry_after), (status, "rate_limited", 3.0))
                self.assertEqual(len(h.requests), 1)
                self.assertEqual(h.waits, [])

    def test_idempotency_key_opts_in_and_stays_out_of_the_body(self) -> None:
        n = [0]

        def serve(_r: Recorded, reply: Reply) -> None:
            n[0] += 1
            if n[0] == 1:
                reply.status, reply.headers = 429, {"Retry-After": "1"}
            else:
                reply.status, reply.body = 202, '{"message_id":"m1","status":"queued"}'

        with Hub(serve) as h:
            res = h.client.send_message(Message(external_user_id="u", channel="c", title="t", body="b", idempotency_key="order-7"))
            self.assertEqual(res.message_id, "m1")
            self.assertEqual(len(h.requests), 2)
            for r in h.requests:
                self.assertEqual(r.headers["idempotency-key"], "order-7")
                self.assertNotIn(b"order-7", r.body)
            self.assertEqual(h.requests[0].body, h.requests[1].body)

    def test_no_idempotency_key_header_by_default(self) -> None:
        with Hub() as h:
            h.client.send_message(MSG)
            self.assertNotIn("idempotency-key", h.requests[0].headers)

    def test_patch_is_not_retried(self) -> None:
        with Hub(lambda _r, reply: setattr(reply, "status", 429)) as h:
            with self.assertRaises(HanshoError):
                h.client.update_activity("a", ActivityUpdate(1))
            self.assertEqual(len(h.requests), 1)

    def test_retry_after_beyond_the_cap_is_surfaced(self) -> None:
        def serve(_r: Recorded, reply: Reply) -> None:
            reply.status, reply.headers = 429, {"Retry-After": "3600"}

        with Hub(serve) as h:
            with self.assertRaises(HanshoError) as cm:
                h.client.put_channel("ops", CHANNEL)
            self.assertEqual(cm.exception.retry_after, 3600.0)
            self.assertEqual(len(h.requests), 1)
            self.assertEqual(h.waits, [])

    def test_absurd_retry_after_is_a_typed_error(self) -> None:
        def serve(_r: Recorded, reply: Reply) -> None:
            reply.status, reply.headers = 429, {"Retry-After": "9" * 400}

        with Hub(serve) as h:
            with self.assertRaises(HanshoError) as cm:
                h.client.put_channel("ops", CHANNEL)
            self.assertEqual(cm.exception.retry_after, 1e9)
            self.assertEqual(len(h.requests), 1)

    def test_retry_after_exactly_at_the_cap_is_waited(self) -> None:
        n = [0]

        def serve(_r: Recorded, reply: Reply) -> None:
            n[0] += 1
            if n[0] == 1:
                reply.status, reply.headers = 429, {"Retry-After": "60"}

        with Hub(serve) as h:
            h.client.put_channel("ops", CHANNEL)
            self.assertEqual(h.waits, [60.0])

    def test_default_sleep_really_waits(self) -> None:
        n = [0]

        def serve(_r: Recorded, reply: Reply) -> None:
            n[0] += 1
            if n[0] == 1:
                reply.status, reply.headers = 429, {"Retry-After": "0.3"}

        with Hub(serve) as h:
            c = Hansho(h.url, TEST_KEY)  # real time.sleep
            start = time.monotonic()
            c.put_channel("ops", CHANNEL)
            self.assertGreaterEqual(time.monotonic() - start, 0.25)


class PathEscaping(unittest.TestCase):
    ids = ["a/b", "a?b=c", "a b", "ü", "日本", "a#b", "100%", "a/../b", "a%2Fb", "a\\b", "a;b", "!*'()"]

    def test_ids_stay_in_their_segment(self) -> None:
        for id in self.ids:
            with Hub() as h:
                c = h.client
                cases = [
                    ("/api/v1/channels/", lambda: c.put_channel(id, CHANNEL)),
                    ("/api/v1/channels/", lambda: c.get_channel(id)),
                    ("/api/v1/channels/", lambda: c.delete_channel(id)),
                    ("/api/v1/templates/", lambda: c.put_template(id, 1, {})),
                    ("/api/v1/sounds/", lambda: c.put_sound(id, "audio/wav", b"\x01")),
                    ("/api/v1/feeds/", lambda: c.put_feed(id, Feed("t", 1))),
                    ("/api/v1/messages/", lambda: c.get_message(id)),
                    ("/api/v1/activities/", lambda: c.update_activity(id, ActivityUpdate(1))),
                    ("/api/v1/activities/", lambda: c.end_activity(id)),
                ]
                for prefix, call in cases:
                    before = len(h.requests)
                    call()
                    target = h.requests[before].url
                    self.assertTrue(target.startswith(prefix), target)
                    seg = target[len(prefix) :]
                    self.assertFalse(any(ch in seg for ch in "/?# "), f"unescaped segment in {target}")
                    self.assertEqual(unquote(seg), id)
                before = len(h.requests)
                c.unpair(id)
                target = h.requests[before].url
                self.assertTrue(target.startswith("/api/v1/users/") and target.endswith("/pairings"), target)
                seg = target[len("/api/v1/users/") : -len("/pairings")]
                self.assertFalse(any(ch in seg for ch in "/?# "), target)
                self.assertEqual(unquote(seg), id)

    def test_display_name_put_and_ticket_body(self) -> None:
        def serve(r: Recorded, reply: Reply) -> None:
            reply.body = '{"ticket":"t","qr":"q","expires_at":"2026-11-08T12:10:00Z"}'

        with Hub(serve) as h:
            h.client.set_user_display_name("a/b c", "Ada")
            h.client.set_user_display_name("u", "")
            h.client.create_pairing_ticket("u")
            h.client.create_pairing_ticket("u", display_name="")
            h.client.create_pairing_ticket("u", "Zoë")
            r = h.requests
            self.assertEqual((r[0].method, r[0].url), ("PUT", "/api/v1/users/a%2Fb%20c"))
            self.assertEqual(json.loads(r[0].body), {"display_name": "Ada"})
            self.assertEqual(json.loads(r[1].body), {"display_name": ""})
            self.assertEqual(json.loads(r[2].body), {"external_user_id": "u"})
            self.assertEqual(json.loads(r[3].body), {"external_user_id": "u", "display_name": ""})
            self.assertEqual(json.loads(r[4].body), {"external_user_id": "u", "display_name": "Zoë"})
            for bad in ("", ".", ".."):
                with self.assertRaises(ValueError):
                    h.client.set_user_display_name(bad, "x")
            self.assertEqual(len(h.requests), 5)

    def test_page_ack_option_is_sent_only_when_set(self) -> None:
        with Hub() as h:
            h.client.send_message(Message(external_user_id="u", channel="c", title="t", body="b", level="page", page=PageOptions(max_retries=1)))
            h.client.send_message(Message(external_user_id="u", channel="c", title="t", body="b", level="page", page=PageOptions(ack="each")))
            self.assertEqual(json.loads(h.requests[0].body)["page"], {"max_retries": 1})
            self.assertEqual(json.loads(h.requests[1].body)["page"], {"ack": "each"})

    def test_exact_encoding(self) -> None:
        with Hub() as h:
            h.client.put_channel("ü x/y?z", CHANNEL)
            self.assertEqual(h.requests[0].url, "/api/v1/channels/%C3%BC%20x%2Fy%3Fz")

    def test_dot_segments_and_empty_ids_are_refused_before_sending(self) -> None:
        with Hub() as h:
            c = h.client
            for bad in ("", ".", ".."):
                for call in (c.get_channel, c.unpair, c.end_activity, c.get_message, c.delete_channel):
                    with self.assertRaises(ValueError):
                        call(bad)
            with self.assertRaises(ValueError):
                c.create_pairing_ticket("")
            with self.assertRaises(TypeError):
                c.get_channel(None)  # type: ignore[arg-type]
            self.assertEqual(h.requests, [])


class SilentServer:
    """Accepts connections and never answers."""

    def __init__(self) -> None:
        release = self.release = threading.Event()

        class H(BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                release.wait(10)

            do_POST = do_PUT = do_DELETE = do_GET

            def log_message(self, *a: Any) -> None:
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), H)
        self.server.daemon_threads = True
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, args=(0.02,), daemon=True).start()

    def close(self) -> None:
        self.release.set()
        self.server.shutdown()
        self.server.server_close()


class NetworkErrors(unittest.TestCase):
    def test_times_out(self) -> None:
        s = SilentServer()
        try:
            c = Hansho(s.url, TEST_KEY, timeout=0.2)
            start = time.monotonic()
            with self.assertRaises(HanshoNetworkError) as cm:
                c.send_message(MSG)
            self.assertLess(time.monotonic() - start, 3)
            self.assertIsInstance(cm.exception.__cause__, (socket.timeout, OSError))
        finally:
            s.close()

    def test_connection_refused_is_a_network_error_not_an_api_error_and_has_no_key(self) -> None:
        h = Hub()
        url = h.url
        h.close()
        with self.assertRaises(HanshoNetworkError) as cm:
            Hansho(url, TEST_KEY).put_channel("ops", CHANNEL)
        e = cm.exception
        self.assertNotIsInstance(e, HanshoError)
        self.assertNotIn(TEST_KEY, str(e))
        self.assertIsNotNone(e.__cause__)

    def test_transport_errors_are_redacted(self) -> None:
        def transport(req: TransportRequest) -> TransportResponse:
            raise ConnectionError(f"proxy said no to {req.headers['Authorization']}")

        c = Hansho("http://x", TEST_KEY, transport=transport)
        with self.assertRaises(HanshoNetworkError) as cm:
            c.get_channel("a")
        self.assertNotIn(TEST_KEY, str(cm.exception))
        self.assertNotIn(TEST_KEY, str(cm.exception.__cause__))
        self.assertIsNone(cm.exception.__context__)
        self.assertIn("[redacted]", str(cm.exception))

    def test_api_errors_never_carry_the_key(self) -> None:
        def serve(r: Recorded, reply: Reply) -> None:
            reply.status = 401
            reply.body = json.dumps({"error": "unauthorized", "message": f"bad key {r.headers['authorization']}"})

        with Hub(serve) as h:
            with self.assertRaises(HanshoError) as cm:
                h.client.put_channel("ops", CHANNEL)
            e = cm.exception
            self.assertNotIn(TEST_KEY, str(e))
            self.assertNotIn(TEST_KEY, str(e.detail))
            self.assertNotIn(TEST_KEY, repr(e.__dict__))

    def test_error_messages_do_not_contain_request_or_response_bodies(self) -> None:
        def serve(_r: Recorded, reply: Reply) -> None:
            reply.status = 400
            reply.body = '{"error":"invalid_request","field":"title","message":"required"}'

        with Hub(serve) as h:
            with self.assertRaises(HanshoError) as cm:
                h.client.send_message(Message(external_user_id="u", channel="c", title="SECRET-TITLE", body="SECRET-BODY"))
            self.assertEqual(str(cm.exception), "hansho: POST /api/v1/messages: 400 invalid_request (field title)")
            self.assertEqual(cm.exception.detail, "required")

    def test_a_redirect_is_not_followed(self) -> None:
        with Hub() as other:

            def serve(_r: Recorded, reply: Reply) -> None:
                reply.status = 307
                reply.headers["Location"] = other.url + "/stolen"

            with Hub(serve) as h:
                with self.assertRaises(HanshoError) as cm:
                    h.client.put_channel("ops", CHANNEL)
                self.assertEqual(cm.exception.status, 307)
            self.assertEqual(other.requests, [], "the API key must not follow a redirect")

    def test_error_formats(self) -> None:
        e = HanshoError("POST", "/api/v1/messages", 429, "rate_limited", None, 7.0)
        self.assertEqual(str(e), "hansho: POST /api/v1/messages: 429 rate_limited (retry after 7000 ms)")
        self.assertIsInstance(e, Exception)
        self.assertEqual((e.field, e.detail), (None, None))


class InjectedTransport(unittest.TestCase):
    def test_transport_sees_the_exact_request(self) -> None:
        seen: List[TransportRequest] = []

        def transport(req: TransportRequest) -> TransportResponse:
            seen.append(req)
            return TransportResponse(202, {"Content-Type": "application/json"}, b'{"message_id":"m","level":"page","devices":1,"status":"queued"}')

        c = Hansho("http://hub.test/", TEST_KEY, transport=transport, timeout=None)
        res = c.send_message(Message(external_user_id="u", channel="c", title="t", body="b", idempotency_key="k"))
        self.assertEqual((res.message_id, res.devices), ("m", 1))
        r = seen[0]
        self.assertEqual((r.method, r.url, r.timeout), ("POST", "http://hub.test/api/v1/messages", None))
        self.assertEqual(r.headers["Idempotency-Key"], "k")
        self.assertEqual(json.loads(r.body or b""), {"external_user_id": "u", "channel": "c", "title": "t", "body": "b"})

    def test_retries_through_a_transport_with_mixed_case_headers(self) -> None:
        answers = [TransportResponse(503, {"Retry-After": "2"}, b""), TransportResponse(200, {}, b"{}")]
        waits: List[float] = []
        c = Hansho("http://hub.test", TEST_KEY, transport=lambda _r: answers.pop(0), sleep=waits.append)
        c.delete_channel("x")
        self.assertEqual(waits, [2.0])

    def test_bad_status_from_a_transport_is_a_network_error(self) -> None:
        c = Hansho("http://hub.test", TEST_KEY, transport=lambda _r: TransportResponse(0, {}, b""))
        with self.assertRaises(HanshoNetworkError):
            c.delete_channel("x")


if __name__ == "__main__":
    unittest.main()

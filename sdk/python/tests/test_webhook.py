import dataclasses
import hashlib
import hmac
import json
import threading
import unittest
import urllib.error
import urllib.request
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Callable, Dict, List, Optional

from hansho import (
    DEFAULT_TOLERANCE,
    WEBHOOK_SIGNATURE_HEADER,
    ActionTaken,
    Event,
    WebhookError,
    verify_webhook,
)

from .helpers import load_json

VECTORS = load_json("webhook-vectors.json")


def sign(secret: str, at: int, body: str) -> str:
    t = str(at)
    return f"t={t},v1=" + hmac.new(secret.encode(), f"{t}.{body}".encode(), hashlib.sha256).hexdigest()


def drop_none(d: Dict[str, Any]) -> Dict[str, Any]:
    return {k: v for k, v in d.items() if v is not None}


def vector(name: str) -> Dict[str, Any]:
    return next(v for v in VECTORS["vectors"] if v["name"] == name)


def code_of(fn: Callable[[], Any]) -> Optional[str]:
    try:
        fn()
    except WebhookError as e:
        return e.code
    return None


class SharedVectors(unittest.TestCase):
    """Vectors signed by the hub's own dispatcher; one test per vector is added below."""

    def test_enough_vectors(self) -> None:
        self.assertGreaterEqual(len(VECTORS["vectors"]), 20)

    def test_covers_each_class(self) -> None:
        classes = set()
        for v in VECTORS["vectors"]:
            e = v["expect"]
            classes.add(e["type"] if e["ok"] else e["error"])
        for c in ("stale", "bad_signature", "malformed_header", "unknown"):
            self.assertIn(c, classes)
        for t in (
            "pairing.created",
            "pairing.revoked",
            "message.delivered",
            "message.undeliverable",
            "message.acked",
            "message.dismissed",
            "message.expired",
            "action",
            "activity.ended",
        ):
            self.assertIn(t, classes)


def _make_vector_test(v: Dict[str, Any]) -> Callable[[SharedVectors], None]:
    def test(self: SharedVectors) -> None:
        def run(body: Any) -> Event:
            return verify_webhook(
                secret=v["secret"], header=v["header"], body=body, now=v["now"], tolerance=VECTORS["tolerance_seconds"]
            )

        exp = v["expect"]
        if not exp["ok"]:
            with self.assertRaises(WebhookError) as cm:
                run(v["body"])
            self.assertEqual(cm.exception.code, exp["error"])
            return
        ev = run(v["body"])
        self.assertEqual(ev.type, exp["type"])
        self.assertEqual(ev.id, exp["id"])
        self.assertEqual(ev.created_at, datetime.fromtimestamp(exp["created_at"], tz=timezone.utc))
        if ev.type == "unknown":
            self.assertEqual(ev.name, exp["name"])
            self.assertEqual(ev.data, exp["data"])
        else:
            self.assertIsNone(ev.name)
            # Payload field names are the wire names, so the dataclass maps back onto the hub's data.
            self.assertEqual(drop_none(dataclasses.asdict(ev.data)), exp["data"])
        self.assertEqual(ev.raw, v["body"].encode("utf-8"))
        self.assertEqual(run(v["body"].encode("utf-8")), ev)  # bytes verify like the str

    return test


for _n, _v in enumerate(VECTORS["vectors"]):
    _slug = "".join(c if c.isascii() and c.isalnum() else "_" for c in _v["name"])
    setattr(SharedVectors, f"test_{_n:02d}_{_slug}", _make_vector_test(_v))


class Basics(unittest.TestCase):
    def test_header_name(self) -> None:
        self.assertEqual(WEBHOOK_SIGNATURE_HEADER, "X-Hansho-Webhook-Signature")

    def test_default_tolerance(self) -> None:
        self.assertEqual(DEFAULT_TOLERANCE, 300)


class ReplayProtection(unittest.TestCase):
    body = '{"id":"1","event":"message.acked","created_at":1,"data":{}}'
    t = 1_700_000_000
    header = sign("s", t, body)

    def check(self, offset: float, tolerance: Optional[float] = None) -> Callable[[], Event]:
        return lambda: verify_webhook(secret="s", header=self.header, body=self.body, now=self.t + offset, tolerance=tolerance)

    def test_300_seconds_either_side(self) -> None:
        for o in (0, 300, -300):
            self.check(o)()
        for o in (301, -301, 86400):
            self.assertEqual(code_of(self.check(o)), "stale")

    def test_tolerance_is_configurable(self) -> None:
        self.check(301, 600)()
        self.assertEqual(code_of(self.check(11, 10)), "stale")
        self.check(10, 10)()
        self.assertEqual(code_of(self.check(301, 0)), "stale")  # <= 0 means the default
        self.assertEqual(code_of(self.check(301, -5)), "stale")

    def test_now_as_datetime_and_clock(self) -> None:
        verify_webhook(secret="s", header=self.header, body=self.body, now=datetime.fromtimestamp(self.t, tz=timezone.utc))
        verify_webhook(secret="s", header=self.header, body=self.body, now=datetime(2023, 11, 14, 22, 13, 20))  # naive = UTC
        now = int(datetime.now(timezone.utc).timestamp())
        verify_webhook(secret="s", header=sign("s", now, self.body), body=self.body)
        self.assertEqual(code_of(lambda: verify_webhook(secret="s", header=self.header, body=self.body)), "stale")


class RawBody(unittest.TestCase):
    valid = vector("valid message.acked")
    sec = "whsec_sdk_vectors"

    def verify(self, body: Any) -> Event:
        return verify_webhook(secret=self.sec, header=self.valid["header"], body=body, now=self.valid["now"])

    def test_bytes_bytearray_memoryview_str(self) -> None:
        raw = self.valid["body"].encode()
        for body in (raw, bytearray(raw), memoryview(raw), self.valid["body"]):
            self.assertEqual(self.verify(body).type, "message.acked")

    def test_reserialised_body_does_not_verify(self) -> None:
        parsed = json.loads(self.valid["body"])
        for body in (json.dumps(parsed, indent=2), json.dumps(parsed, indent=1), self.valid["body"] + "\n"):
            self.assertEqual(code_of(lambda: self.verify(body)), "bad_signature")

    def test_parsed_object_is_refused(self) -> None:
        for body in (json.loads(self.valid["body"]), None, 42, {}, []):
            self.assertEqual(code_of(lambda: self.verify(body)), "invalid_body")

    def test_lone_surrogate_str_is_refused(self) -> None:
        self.assertEqual(code_of(lambda: self.verify(chr(0xD800))), "invalid_body")

    def test_non_ascii_body_verifies_byte_for_byte(self) -> None:
        v = vector("valid pairing.created")
        ev = verify_webhook(secret=v["secret"], header=v["header"], body=v["body"].encode("utf-8"), now=v["now"])
        self.assertEqual(ev.data.device_name, "Bas’s iPhone <Ünï>")

    def test_from_a_stdlib_http_server(self) -> None:
        seen: List[Event] = []
        sec, now = self.sec, self.valid["now"]

        class H(BaseHTTPRequestHandler):
            def do_POST(self) -> None:
                body = self.rfile.read(int(self.headers["Content-Length"]))
                try:
                    seen.append(verify_webhook(secret=sec, header=self.headers.get(WEBHOOK_SIGNATURE_HEADER), body=body, now=now))
                    self.send_response(200)
                except WebhookError:
                    self.send_response(400)
                self.send_header("Content-Length", "0")
                self.end_headers()

            def log_message(self, *a: Any) -> None:
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), H)
        threading.Thread(target=server.serve_forever, args=(0.02,), daemon=True).start()
        url = f"http://127.0.0.1:{server.server_address[1]}/"

        def post(body: str, header: str) -> int:
            req = urllib.request.Request(url, data=body.encode("utf-8"), headers={WEBHOOK_SIGNATURE_HEADER: header}, method="POST")
            try:
                with urllib.request.urlopen(req) as r:
                    return r.status
            except urllib.error.HTTPError as e:
                return e.code

        try:
            self.assertEqual(post(self.valid["body"], self.valid["header"]), 200)
            self.assertEqual(post(json.dumps(json.loads(self.valid["body"]), indent=2), self.valid["header"]), 400)
            p = vector("valid pairing.created")
            self.assertEqual(post(p["body"], p["header"]), 200)  # non-ASCII body, verified byte for byte
        finally:
            server.shutdown()
            server.server_close()
        self.assertEqual([e.type for e in seen], ["message.acked", "pairing.created"])


class SecretAndHeader(unittest.TestCase):
    body = '{"id":"1","event":"action","created_at":1,"data":{}}'
    t = int(datetime.now(timezone.utc).timestamp())

    def test_bytes_secret_and_empty_secret(self) -> None:
        verify_webhook(secret=b"s", header=sign("s", self.t, self.body), body=self.body)
        for secret in ("", b"", None, 5):
            self.assertEqual(
                code_of(lambda: verify_webhook(secret=secret, header=sign("", self.t, self.body), body=self.body)),  # type: ignore[arg-type]
                "invalid_secret",
            )

    def test_missing_header_is_malformed(self) -> None:
        for header in (None, "", "   ", 7):
            self.assertEqual(code_of(lambda: verify_webhook(secret="s", header=header, body=self.body)), "malformed_header")  # type: ignore[arg-type]

    def test_valid_signature_over_a_non_envelope_is_malformed_body(self) -> None:
        for bad in ("not json", "[]", '{"id":"1"}', "", '{"event":""}', '{"event":5}'):
            self.assertEqual(
                code_of(lambda: verify_webhook(secret="s", header=sign("s", self.t, bad), body=bad)), "malformed_body", repr(bad)
            )
        raw = b'{"event":"a\xff"}'
        h = f"t={self.t},v1=" + hmac.new(b"s", f"{self.t}.".encode() + raw, hashlib.sha256).hexdigest()
        self.assertEqual(code_of(lambda: verify_webhook(secret="s", header=h, body=raw)), "malformed_body")

    def test_upper_case_hex(self) -> None:
        head, sig = sign("s", self.t, self.body).split("v1=")
        verify_webhook(secret="s", header=head + "v1=" + sig.upper(), body=self.body)

    def test_unicode_digits_in_timestamp_are_malformed(self) -> None:
        h = sign("s", self.t, self.body).replace(f"t={self.t}", "t=١٢٣")
        self.assertEqual(code_of(lambda: verify_webhook(secret="s", header=h, body=self.body)), "malformed_header")

    def test_huge_timestamp_is_malformed_not_a_crash(self) -> None:
        h = "t=" + "9" * 5000 + ",v1=" + "0" * 64
        self.assertEqual(code_of(lambda: verify_webhook(secret="s", header=h, body=self.body)), "malformed_header")

    def test_whitespace_around_parts_is_tolerated_inside_a_signature_it_is_not(self) -> None:
        head, sig = sign("s", self.t, self.body).split(",v1=")
        verify_webhook(secret="s", header=f" {head} , v1={sig}\n", body=self.body)
        bad = f"{head},v1={sig[:32]}\n{sig[32:]}"
        self.assertEqual(code_of(lambda: verify_webhook(secret="s", header=bad, body=self.body)), "bad_signature")

    def test_wrong_secret(self) -> None:
        self.assertEqual(code_of(lambda: verify_webhook(secret="other", header=sign("s", self.t, self.body), body=self.body)), "bad_signature")


class ForwardCompatibility(unittest.TestCase):
    t = int(datetime.now(timezone.utc).timestamp())

    def test_unknown_event_is_unknown_with_raw_payload(self) -> None:
        body = '{"id":7,"event":"message.holographic","created_at":5,"extra":1,"data":{"message_id":"m","new_field":[1,2]}}'
        ev = verify_webhook(secret="s", header=sign("s", self.t, body), body=body)
        self.assertEqual(ev.type, "unknown")
        self.assertEqual(ev.name, "message.holographic")
        self.assertEqual(ev.data, {"message_id": "m", "new_field": [1, 2]})
        self.assertEqual(ev.id, "7")
        self.assertEqual(ev.created_at, datetime.fromtimestamp(5, tz=timezone.utc))

    def test_unknown_fields_on_known_events_are_ignored(self) -> None:
        body = '{"id":"8","event":"message.acked","created_at":5,"data":{"message_id":"m","device_id":"d","external_user_id":"u","from_the_future":true}}'
        ev = verify_webhook(secret="s", header=sign("s", self.t, body), body=body)
        self.assertEqual(ev.type, "message.acked")
        self.assertEqual(dataclasses.asdict(ev.data), {"message_id": "m", "external_user_id": "u", "device_id": "d"})

    def test_action_text_is_optional(self) -> None:
        body = '{"id":"9","event":"action","created_at":5,"data":{"message_id":"m","action_id":"a","device_id":"d","external_user_id":"u"}}'
        ev = verify_webhook(secret="s", header=sign("s", self.t, body), body=body)
        self.assertIsInstance(ev.data, ActionTaken)
        self.assertIsNone(ev.data.text)

    def test_missing_data_is_tolerated(self) -> None:
        body = '{"id":"1","event":"message.acked","created_at":5}'
        ev = verify_webhook(secret="s", header=sign("s", self.t, body), body=body)
        self.assertEqual(ev.data.message_id, "")
        body = '{"id":"1","event":"future.thing","created_at":5}'
        self.assertIsNone(verify_webhook(secret="s", header=sign("s", self.t, body), body=body).data)

    def test_odd_numbers_do_not_crash(self) -> None:
        for created in ("1e400", "Infinity", "NaN", "-1e30", '"x"', "null", "[]"):
            body = '{"id":"1","event":"message.delivered","created_at":%s,"data":{"devices":%s}}' % (created, created)
            try:
                verify_webhook(secret="s", header=sign("s", self.t, body), body=body)
            except WebhookError as e:
                self.assertEqual(e.code, "malformed_body", created)

    def test_known_events_map_to_payloads(self) -> None:
        want = {
            "pairing.created": ("pairing_id", "pair_1"),
            "message.delivered": ("devices", 2),
            "message.expired": ("attempts", 6),
            "action": ("action_id", "approve"),
            "activity.ended": ("reason", "timeout"),
        }
        seen = set()
        for v in VECTORS["vectors"]:
            e = v["expect"]
            if not v["name"].startswith("valid ") or not e["ok"] or e["type"] == "unknown" or e["type"] in seen:
                continue
            seen.add(e["type"])
            ev = verify_webhook(secret=v["secret"], header=v["header"], body=v["body"], now=v["now"])
            if e["type"] in want:
                attr, val = want[e["type"]]
                self.assertEqual(getattr(ev.data, attr), val)
        self.assertEqual(len(seen), 9)


if __name__ == "__main__":
    unittest.main()

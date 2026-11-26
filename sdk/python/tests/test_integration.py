"""End-to-end flow against the real hub.

Skipped unless HANSHO_HUB_URL is set. sdk/scripts/run-integration.sh starts the
real hub binary and a stub relay and exports HANSHO_HUB_URL, HANSHO_API_KEY,
HANSHO_WEBHOOK_SECRET, HANSHO_WEBHOOK_ADDR and HANSHO_FAKEDEVICE_BIN.
"""

import os
import struct
import subprocess
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Callable, List, Optional

from hansho import (
    WEBHOOK_SIGNATURE_HEADER,
    Action,
    Activity,
    ActivityAlert,
    ActivityUpdate,
    Channel,
    Event,
    Feed,
    Hansho,
    HanshoError,
    Message,
    MessageStatus,
    PageOptions,
    WebhookError,
    verify_webhook,
)

SKIP = None if os.environ.get("HANSHO_HUB_URL") else "HANSHO_HUB_URL is not set; run sdk/scripts/run-integration.sh"


def fake_device(*args: str) -> str:
    done = subprocess.run(
        [os.environ["HANSHO_FAKEDEVICE_BIN"], *args], check=True, capture_output=True, text=True, stdin=subprocess.DEVNULL
    )
    return done.stdout


def tiny_wav() -> bytes:
    data = bytes(800)  # 0.05 s of 8 kHz 8-bit mono silence
    header = b"RIFF" + struct.pack("<I", 36 + len(data)) + b"WAVEfmt " + struct.pack("<IHHIIHH", 16, 1, 1, 8000, 8000, 1, 8)
    return header + b"data" + struct.pack("<I", len(data)) + data


class Receiver:
    """Receives the hub's webhooks on HANSHO_WEBHOOK_ADDR and verifies each with the SDK."""

    def __init__(self, addr: str, secret: str) -> None:
        self.events: List[Event] = []
        self.rejected = 0
        lock = self._lock = threading.Lock()
        rx = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:
                body = self.rfile.read(int(self.headers.get("Content-Length") or 0))
                try:
                    ev = verify_webhook(secret=secret, header=self.headers.get(WEBHOOK_SIGNATURE_HEADER), body=body)
                    with lock:
                        rx.events.append(ev)
                    status = 200
                except WebhookError:
                    with lock:
                        rx.rejected += 1
                    status = 400
                except Exception:
                    with lock:
                        rx.rejected += 1
                    status = 500
                self.send_response(status)
                self.send_header("Content-Length", "0")
                self.end_headers()

            def log_message(self, *a: Any) -> None:
                pass

        host, _, port = addr.rpartition(":")
        for i in range(51):
            try:
                self._server = ThreadingHTTPServer((host, int(port)), Handler)
                break
            except OSError:
                if i >= 50:
                    raise
                time.sleep(0.1)  # a previous suite's receiver may still be closing
        self._server.daemon_threads = True
        self._thread = threading.Thread(target=self._server.serve_forever, args=(0.05,), daemon=True)
        self._thread.start()

    def close(self) -> None:
        self._server.shutdown()
        self._server.server_close()
        self._thread.join(5)

    def wait(self, type: str, match: Callable[[Event], bool], timeout: float = 25.0) -> Event:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            with self._lock:
                for e in self.events:
                    if e.type == type and match(e):
                        return e
            time.sleep(0.1)
        raise AssertionError(f"no {type} webhook arrived")


def wait_status(c: Hansho, id: str, *want: str) -> MessageStatus:
    deadline = time.monotonic() + 25
    last = ""
    while time.monotonic() < deadline:
        ms = c.get_message(id)
        last = ms.status
        if ms.status in want:
            return ms
        time.sleep(0.2)
    raise AssertionError(f"message {id}: status {last}, wanted one of {', '.join(want)}")


@unittest.skipIf(SKIP, SKIP or "")
class IntegrationAgainstTheRealHub(unittest.TestCase):
    def api_error(self, fn: Callable[[], Any], status: int, code: str, field: Optional[str] = None) -> None:
        with self.assertRaises(HanshoError) as cm:
            fn()
        e = cm.exception
        self.assertEqual((e.status, e.code), (status, code))
        if field is not None:
            self.assertEqual(e.field, field)

    def test_the_whole_flow(self) -> None:
        hub_url = os.environ["HANSHO_HUB_URL"]
        rx = Receiver(os.environ["HANSHO_WEBHOOK_ADDR"], os.environ["HANSHO_WEBHOOK_SECRET"])
        try:
            c = Hansho(hub_url, os.environ["HANSHO_API_KEY"])
            p = "python"
            user = f"u-{p} ü/42?x"
            ch, tpl, snd = f"{p}-ops", f"{p}-status", f"{p}-ding"
            state = os.path.join(tempfile.mkdtemp(prefix="hansho-python-it-"), "device.json")

            # errors
            self.api_error(lambda: Hansho(hub_url, "ck_not_a_key").create_pairing_ticket("x"), 401, "unauthorized")
            self.api_error(lambda: c.get_channel("no-such-channel"), 404, "unknown_channel")
            self.api_error(lambda: c.send_message(Message(external_user_id=user, channel=ch, title="", body="b")), 400, "invalid_request", "title")

            # catalog
            sound = c.put_sound(snd, "audio/wav", tiny_wav())
            self.assertEqual(sound.id, snd)
            template = {
                "type": "vstack",
                "tint": {"light": "#0A84FF", "dark": "#64D2FF"},
                "children": [{"type": "text", "style": "headline", "text": "{{title}}"}, {"type": "spacer"}],
            }
            c.put_template(tpl, 1, template)
            self.api_error(lambda: c.put_template(tpl, 1, template), 409, "version_conflict")
            c.put_template(tpl, 2, template)
            c.put_channel(
                ch,
                Channel(
                    name=f"Operations {p}",
                    default_level="active",
                    max_level="page",
                    sound=snd,
                    template_id=tpl,
                    thread="ops",
                    actions=[Action("ack", "Acknowledge", ack=True)],
                ),
            )
            got = c.get_channel(ch)
            self.assertEqual(got.name, f"Operations {p}")
            self.assertEqual(got.max_level, "page")
            self.assertEqual(got.sound, snd)
            self.assertEqual(got.template_id, tpl)
            self.assertEqual(got.actions, [Action("ack", "Acknowledge", ack=True)])
            self.api_error(lambda: c.put_channel("bad id", Channel("n", "active", "page")), 400, "invalid_request", "id")

            # pairing
            tk = c.create_pairing_ticket(user)
            self.assertTrue(tk.qr.startswith("hansho://pair?") and tk.ticket in tk.qr)
            self.assertGreater(tk.expires_at.timestamp(), time.time())
            fake_device("pair", "-hub", hub_url, "-ticket", tk.ticket, "-state", state, "-name", f"SDK phone {p}")
            rx.wait("pairing.created", lambda e: e.data.external_user_id == user and e.data.device_name == f"SDK phone {p}")

            # messages at every level
            for level in ("passive", "active", "time-sensitive", "critical"):
                res = c.send_message(
                    Message(
                        external_user_id=user,
                        channel=ch,
                        level=level,  # type: ignore[arg-type]
                        title="Disk full",
                        body="db-1 is at 97%",
                        data={"disk": 97},
                        sound=snd,
                        collapse_id=f"disk-{level}",
                    )
                )
                self.assertEqual(res.level, level)
                self.assertEqual(res.devices, 1)
                self.assertTrue(res.message_id.startswith("msg_"))
                ms = wait_status(c, res.message_id, "delivered")
                self.assertEqual(ms.level, level)
                self.assertEqual(len(ms.deliveries), 1)
                self.assertEqual(ms.deliveries[0].status, "sent")
                rx.wait(
                    "message.delivered",
                    lambda e, res=res, level=level: e.data.message_id == res.message_id and e.data.level == level and e.data.devices == 1,
                )
            default = c.send_message(Message(external_user_id=user, channel=ch, title="Default", body="b"))
            self.assertEqual(default.level, "active")

            # page + acknowledgement from the (fake) device
            page = c.send_message(
                Message(
                    external_user_id=user,
                    channel=ch,
                    level="page",
                    title="Primary DB down",
                    body="Failover failed",
                    page=PageOptions(interval_seconds=60, max_retries=3),
                )
            )
            self.assertEqual(page.level, "page")
            wait_status(c, page.message_id, "paging")
            fake_device("ack", "-state", state, "-message", page.message_id)
            acked = wait_status(c, page.message_id, "acked")
            self.assertIsNotNone(acked.acked_at)
            ev = rx.wait("message.acked", lambda e: e.data.message_id == page.message_id)
            self.assertEqual(ev.data.external_user_id, user)
            rx.wait("action", lambda e: e.data.message_id == page.message_id and e.data.action_id == "ack")

            # live activity
            started = c.start_activity(
                Activity(user, ch, tpl, {"title": "Deploy", "progress": 0.1}, alert=ActivityAlert(title="Deploy started"))
            )
            self.assertEqual(started.devices, 1)
            self.assertTrue(started.activity_id.startswith("act_"))
            fake_device("activity-token", "-state", state, "-activity", started.activity_id)
            up = c.update_activity(started.activity_id, ActivityUpdate({"title": "Deploy", "progress": 0.6}, priority=10))
            self.assertEqual(up.activity_id, started.activity_id)
            self.assertGreaterEqual(up.update, 1)
            c.end_activity(started.activity_id)
            self.api_error(lambda: c.update_activity(started.activity_id, ActivityUpdate({"title": "x"})), 409, "activity_ended")

            # feeds
            own = c.put_feed(f"{p}-uptime", Feed(tpl, {"title": "up", "ms": 41}, external_user_id=user))
            self.assertEqual(own.feed_id, f"{p}-uptime")
            self.assertGreaterEqual(own.devices, 1)
            shared = c.put_feed(f"{p}-shared", Feed(tpl, {"title": "all"}))
            self.assertGreaterEqual(shared.devices, 1)
            self.api_error(lambda: c.put_feed(f"{p}-bad", Feed("missing", 1)), 400, "invalid_request", "template_id")

            # unpair, then nobody is left to deliver to
            c.unpair(user)
            rx.wait("pairing.revoked", lambda e: e.data.external_user_id == user and e.data.by == "sender")
            none = c.send_message(Message(external_user_id=user, channel=ch, title="Anyone?", body="b"))
            self.assertEqual(none.devices, 0)
            wait_status(c, none.message_id, "undeliverable")
            rx.wait("message.undeliverable", lambda e: e.data.message_id == none.message_id)
            c.delete_channel(ch)
            self.api_error(lambda: c.get_channel(ch), 404, "unknown_channel")

            self.assertEqual(rx.rejected, 0, "the SDK rejected a webhook the hub signed")
            self.assertGreater(len(rx.events), 0)
        finally:
            rx.close()


if __name__ == "__main__":
    unittest.main()

import base64
import json
import re
import unittest
from datetime import datetime, timezone
from typing import Any, Callable, Dict, Tuple

from hansho import (
    Action,
    CODE_NO_ACCESS,
    CODE_UNKNOWN_GROUP,
    Activity,
    ActivityAlert,
    Audience,
    ActivityStarted,
    ActivityUpdate,
    ActivityUpdated,
    Channel,
    Delivery,
    Feed,
    FeedResult,
    FeedSettings,
    Group,
    Hansho,
    HanshoError,
    Message,
    MessageResult,
    MessageStatus,
    PageOptions,
    PairingTicket,
    Recipient,
    Sound,
    SoundResult,
    Template,
)
from hansho._types_gen import SPEC_OPERATIONS

from .helpers import TEST_KEY, Hub, load_json

WAV = base64.b64decode("UklGRgAAAABXQVZF")


def utc(*a: int) -> datetime:
    return datetime(*a, tzinfo=timezone.utc)


def spec_channel() -> Channel:
    return Channel(
        name="Operations",
        default_level="active",
        max_level="critical",
        sound="ding",
        template_id="status",
        thread="ops-thread",
        actions=[
            Action("ack", "Acknowledge", ack=True),
            Action("reply", "Reply", kind="text"),
            Action("runbook", "Runbook", kind="url", url="https://example.com/runbook"),
        ],
    )


def spec_audience() -> Audience:
    return Audience("only", users=["alice"], groups=["oncall"])


# Every operation of the shared spec: the SDK call and the value it must produce.
CALLS: Dict[str, Tuple[Callable[[Hansho], Any], Any]] = {
    "createPairingTicket": (
        lambda c: c.create_pairing_ticket("user-42"),
        PairingTicket("tk_abc", "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=tk_abc&fp=ff00", utc(2026, 11, 8, 12, 10)),
    ),
    "createPairingTicketWithName": (
        lambda c: c.create_pairing_ticket("user-42", display_name="Zoë 日本語"),
        PairingTicket("tk_def", "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=tk_def&fp=ff00", utc(2026, 11, 8, 12, 10)),
    ),
    "setUserDisplayName": (lambda c: c.set_user_display_name("user-42", "Zoë 日本語"), None),
    "clearUserDisplayName": (lambda c: c.set_user_display_name("user 42", ""), None),
    "unpair": (lambda c: c.unpair("user-42"), None),
    "putChannel": (lambda c: c.put_channel("ops", spec_channel()), None),
    "getChannel": (
        lambda c: c.get_channel("ops"),
        Channel("Operations", "active", "critical", sound="", template_id="", thread="ops", actions=[], id="ops"),
    ),
    "deleteChannel": (lambda c: c.delete_channel("ops"), None),
    "putTemplate": (
        lambda c: c.put_template(
            "status", 2, {"type": "vstack", "children": [{"type": "text", "style": "headline", "text": "{{title}}"}]}
        ),
        None,
    ),
    "putSound": (lambda c: c.put_sound("ding", "audio/wav", WAV), SoundResult("ding", "ding.wav", "00", 12)),
    "sendMessage": (
        lambda c: c.send_message(
            Message(
                external_user_id="user-42",
                channel="ops",
                level="time-sensitive",
                title="Disk full",
                subtitle="db-1",
                body="db-1 is at 97%",
                image_url="https://example.com/i.png",
                template_id="status",
                data={"disk": 97, "nested": {"snake_case_key": [1, 2]}},
                sound="ding",
                actions=[Action("ack", "Ack", ack=True)],
                collapse_id="disk-db-1",
            )
        ),
        MessageResult(
            level="time-sensitive",
            message_id="msg_1",
            devices=2,
            status="queued",
            deliveries=[Delivery("dev_1", "queued"), Delivery("dev_2", "queued")],
        ),
    ),
    "sendPage": (
        lambda c: c.send_message(
            Message(
                external_user_id="user-42",
                channel="ops",
                level="page",
                title="Primary DB down",
                body="Failover did not complete",
                page=PageOptions(interval_seconds=60, max_retries=3),
            )
        ),
        MessageResult(level="page", message_id="msg_2", devices=1, status="queued", deliveries=[Delivery("dev_1", "queued")]),
    ),
    "sendPageEach": (
        lambda c: c.send_message(
            Message(
                external_user_ids=["alice", "bob"],
                channel="ops",
                level="page",
                title="Primary DB down",
                body="Failover did not complete",
                page=PageOptions(interval_seconds=60, max_retries=3, ack="each"),
            )
        ),
        MessageResult(
            level="page",
            recipients=[
                Recipient("alice", "queued", "msg_3", 1, [Delivery("dev_1", "queued")]),
                Recipient("bob", "queued", "msg_4", 1, [Delivery("dev_2", "queued")]),
            ],
        ),
    ),
    "getMessageAckedNoName": (
        lambda c: c.get_message("msg_3"),
        MessageStatus("msg_3", "acked", "page", 1, [Delivery("dev_1", "sent")], utc(2026, 11, 8, 12, 5), "alice"),
    ),
    "getMessage": (
        lambda c: c.get_message("msg_2"),
        MessageStatus(
            "msg_2",
            "acked",
            "page",
            2,
            [Delivery("dev_1", "sent"), Delivery("dev_2", "failed", "unregistered")],
            utc(2026, 11, 8, 12, 5),
            "user-42",
            "Zoë 日本語",
        ),
    ),
    "startActivity": (
        lambda c: c.start_activity(
            Activity(
                external_user_id="user-42",
                channel="ops",
                template_id="deploy",
                data={"progress": 0.1},
                alert=ActivityAlert("Deploy started", "v1.2.3"),
            )
        ),
        ActivityStarted("act_1", 1),
    ),
    "updateActivity": (
        lambda c: c.update_activity("act_1", ActivityUpdate({"progress": 0.6}, priority=10, alert=ActivityAlert("Halfway", "60%"))),
        ActivityUpdated("act_1", 3),
    ),
    "endActivity": (lambda c: c.end_activity("act_1"), None),
    "putFeed": (
        lambda c: c.put_feed("uptime", Feed("status", {"up": True, "ms": 41}, external_user_id="user-42", title="Uptime")),
        FeedResult("uptime", 2),
    ),
    "putGroup": (lambda c: c.put_group("oncall", ["alice", "bob"]), Group("oncall", ["alice", "bob"])),
    "getGroup": (lambda c: c.get_group("oncall"), Group("oncall", ["alice", "bob"])),
    "deleteGroup": (lambda c: c.delete_group("oncall"), None),
    "kick": (lambda c: c.kick("user-42"), None),
    "putChannelWithAudience": (
        lambda c: c.put_channel("ops", Channel("Operations", "active", "critical", audience=spec_audience())),
        None,
    ),
    "putFeedWithChannel": (
        lambda c: c.put_feed("uptime", Feed("status", {"up": True}, channel="ops", audience=spec_audience())),
        FeedResult("uptime", 1),
    ),
    "sendToAudience": (
        lambda c: c.send_message(Message(to_audience=True, channel="ops", title="Deploy done", body="v1.2.3 is live")),
        MessageResult(
            level="active",
            recipients=[
                Recipient("alice", "queued", "msg_7", 1, [Delivery("dev_1", "queued")]),
                Recipient("carol", "no_access"),
            ],
        ),
    ),
    "listChannels": (
        lambda c: c.list_channels(),
        [
            Channel(
                "Operations",
                "active",
                "critical",
                sound="",
                template_id="",
                thread="ops",
                actions=[],
                audience=Audience("everyone"),
                id="ops",
            )
        ],
    ),
    "listTemplates": (lambda c: c.list_templates(), [Template("status", 2, {"type": "text", "text": "{{title}}"})]),
    "getTemplate": (lambda c: c.get_template("status"), Template("status", 2, {"type": "text", "text": "{{title}}"})),
    "deleteTemplate": (lambda c: c.delete_template("status"), None),
    "listSounds": (lambda c: c.list_sounds(), [Sound("ding", "audio/wav", 12, "00")]),
    "getSound": (lambda c: c.get_sound("ding"), Sound("ding", "audio/wav", 12, "00")),
    "deleteSound": (lambda c: c.delete_sound("ding"), None),
    "listGroups": (lambda c: c.list_groups(), [Group("oncall", ["alice", "bob"])]),
    "getFeed": (
        lambda c: c.get_feed("uptime", "user-42"),
        FeedSettings(
            "uptime",
            "status",
            1762603200,
            external_user_id="user-42",
            title="Uptime",
            channel="ops",
            audience=Audience("only", users=["alice"]),
        ),
    ),
}

# The error codes the SDK exports, by the code the hub sends.
ERROR_CODES = {"unknown_group": CODE_UNKNOWN_GROUP, "no_access": CODE_NO_ACCESS}


class SpecOperations(unittest.TestCase):
    spec = load_json("openapi.json")["x-conformance"]

    def test_every_operation_has_a_call_and_vice_versa(self) -> None:
        names = {o["name"] for o in self.spec["operations"]}
        self.assertEqual(names, set(CALLS))

    def test_every_spec_operation_has_a_client_method(self) -> None:
        missing = []
        for op in SPEC_OPERATIONS:
            method = re.sub(r"(?<!^)(?=[A-Z])", "_", op).lower()
            if not callable(getattr(Hansho, method, None)):
                missing.append(f"{op} -> {method}")
        self.assertEqual(missing, [], "the client lacks a method for these spec operations")

    def test_every_case_names_a_spec_operation(self) -> None:
        for o in self.spec["operations"]:
            self.assertIn(o["operation"], SPEC_OPERATIONS, o["name"])


def _make_op_test(op: Dict[str, Any]) -> Callable[[SpecOperations], None]:
    def test(self: SpecOperations) -> None:
        call, want = CALLS[op["name"]]

        def serve(r: Any, reply: Any) -> None:
            reply.status = op["response"]["status"]
            reply.body = json.dumps(op["response"]["body"])
            assert r.method == op["method"], r.method
            assert r.url == op["path"], r.url
            assert r.headers["authorization"] == f"Bearer {TEST_KEY}"
            req = op["request"]
            if req is None:
                assert r.body == b"", r.body
            elif "raw_base64" in req:
                assert r.headers["content-type"] == req["content_type"]
                assert r.body == base64.b64decode(req["raw_base64"])
            else:
                assert r.headers["content-type"] == req["content_type"], r.headers.get("content-type")
                assert json.loads(r.body.decode("utf-8")) == req["body"], r.body

        with Hub(serve) as hub:
            got = call(hub.client)
            self.assertEqual(got, want)
            self.assertEqual(len(hub.requests), 1)

    return test


for _op in SpecOperations.spec["operations"]:
    setattr(SpecOperations, f"test_{_op['name']}", _make_op_test(_op))


class SpecErrors(unittest.TestCase):
    pass


def _make_err_test(e: Dict[str, Any]) -> Callable[[SpecErrors], None]:
    def test(self: SpecErrors) -> None:
        def serve(_r: Any, reply: Any) -> None:
            reply.status = e["status"]
            reply.headers = e["headers"]
            reply.body = e["raw"] if "raw" in e else json.dumps(e["body"])

        with Hub(serve) as hub:
            with self.assertRaises(HanshoError) as cm:
                hub.client.send_message(Message("u", "c", "t", "b"))
            err = cm.exception
            self.assertEqual(err.status, e["status"])
            self.assertEqual(err.code, e["code"])
            if e["code"] in ERROR_CODES:
                self.assertEqual(err.code, ERROR_CODES[e["code"]])
            self.assertEqual(err.field or "", e["field"])
            self.assertEqual(round((err.retry_after or 0) * 1000), e["retry_after_ms"])
            self.assertEqual(len(hub.requests), 1, "POST /messages must not be retried")

    return test


for _i, _e in enumerate(SpecErrors.__dict__.get("x", []) or load_json("openapi.json")["x-conformance"]["errors"]):
    setattr(SpecErrors, f"test_error_{_i}_{_e['status']}", _make_err_test(_e))


if __name__ == "__main__":
    unittest.main()

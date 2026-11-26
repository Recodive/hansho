import json
import unittest
from urllib.parse import parse_qs, urlsplit

from hansho import (
    CODE_NO_ACCESS,
    CODE_UNKNOWN_GROUP,
    RECIPIENT_NO_ACCESS,
    Activity,
    Audience,
    Channel,
    Delivery,
    Feed,
    FeedSettings,
    Group,
    HanshoError,
    Message,
    Recipient,
)

from .helpers import Hub, Recorded, Reply


def answer(status: int, body: str):
    def serve(_r: Recorded, reply: Reply) -> None:
        reply.status, reply.body = status, body

    return serve


class Groups(unittest.TestCase):
    def test_put_get_delete(self) -> None:
        with Hub(answer(200, '{"id":"oncall","members":["alice","bob"]}')) as h:
            self.assertEqual(h.client.put_group("oncall", ["alice", "bob"]), Group("oncall", ["alice", "bob"]))
            self.assertEqual((h.requests[0].method, h.requests[0].url), ("PUT", "/api/v1/groups/oncall"))
            self.assertEqual(json.loads(h.requests[0].body), {"members": ["alice", "bob"]})
            self.assertEqual(h.client.get_group("oncall"), Group("oncall", ["alice", "bob"]))
            self.assertEqual((h.requests[1].method, h.requests[1].url), ("GET", "/api/v1/groups/oncall"))
        with Hub(answer(204, "")) as h:
            self.assertIsNone(h.client.delete_group("oncall"))
            self.assertEqual((h.requests[0].method, h.requests[0].url), ("DELETE", "/api/v1/groups/oncall"))

    def test_an_empty_member_list_is_sent(self) -> None:
        with Hub(answer(200, '{"id":"g","members":[]}')) as h:
            self.assertEqual(h.client.put_group("g", []), Group("g", []))
            self.assertEqual(json.loads(h.requests[0].body), {"members": []})

    def test_ids_are_validated_and_escaped(self) -> None:
        with Hub() as h:
            with self.assertRaises(ValueError):
                h.client.put_group("", [])
            with self.assertRaises(ValueError):
                h.client.get_group("..")
            h.client.get_group("a/b")
            self.assertEqual(h.requests[0].url, "/api/v1/groups/a%2Fb")

    def test_unknown_group(self) -> None:
        for call in (lambda c: c.get_group("x"), lambda c: c.delete_group("x")):
            with Hub(answer(404, '{"error":"unknown_group"}')) as h:
                with self.assertRaises(HanshoError) as cm:
                    call(h.client)
                self.assertEqual((cm.exception.status, cm.exception.code), (404, CODE_UNKNOWN_GROUP))
                self.assertEqual(len(h.requests), 1)


class Audiences(unittest.TestCase):
    def test_channel_audience_round_trip(self) -> None:
        ch = Channel("Pager", "page", "page", audience=Audience("only", users=["alice"], groups=["oncall"]))
        with Hub() as h:
            h.client.put_channel("pager", ch)
            self.assertEqual(
                json.loads(h.requests[0].body),
                {"name": "Pager", "default_level": "page", "max_level": "page", "audience": {"mode": "only", "users": ["alice"], "groups": ["oncall"]}},
            )
        body = '{"name":"Pager","default_level":"page","max_level":"page","audience":{"mode":"only","groups":["oncall"]}}'
        with Hub(answer(200, body)) as h:
            got = h.client.get_channel("pager")
            self.assertEqual(got.audience, Audience("only", groups=["oncall"]))

    def test_an_absent_audience_is_left_out_so_the_hub_keeps_the_stored_one(self) -> None:
        with Hub() as h:
            h.client.put_channel("pager", Channel("Pager", "page", "page"))
            self.assertNotIn("audience", json.loads(h.requests[0].body))
            h.client.put_feed("f", Feed("t", 1))
            self.assertNotIn("audience", json.loads(h.requests[1].body))
            self.assertNotIn("channel", json.loads(h.requests[1].body))

    def test_everyone_is_sent_explicitly(self) -> None:
        with Hub() as h:
            h.client.put_channel("pager", Channel("Pager", "page", "page", audience=Audience("everyone")))
            self.assertEqual(json.loads(h.requests[0].body)["audience"], {"mode": "everyone"})

    def test_feed_channel_and_audience(self) -> None:
        with Hub() as h:
            h.client.put_feed("queue", Feed("status", {"value": 3}, channel="pager"))
            self.assertEqual(json.loads(h.requests[0].body), {"template_id": "status", "channel": "pager", "data": {"value": 3}})
            h.client.put_feed("queue", Feed("status", 1, audience=Audience("except", users=["mallory"])))
            self.assertEqual(json.loads(h.requests[1].body)["audience"], {"mode": "except", "users": ["mallory"]})

    def test_get_feed(self) -> None:
        body = '{"feed_id":"queue","external_user_id":"alice","template_id":"status","title":"Queue","channel":"pager","audience":{"mode":"only","users":["alice"]},"updated_at":1760000000}'
        with Hub(answer(200, body)) as h:
            got = h.client.get_feed("queue", "alice")
            want = FeedSettings("queue", "status", 1760000000, "alice", "Queue", "pager", Audience("only", users=["alice"]))
            self.assertEqual(got, want)
            parts = urlsplit(h.requests[0].url)
            self.assertEqual((h.requests[0].method, parts.path), ("GET", "/api/v1/feeds/queue"))
            self.assertEqual(parse_qs(parts.query), {"external_user_id": ["alice"]})
        with Hub(answer(200, '{"feed_id":"all","template_id":"t","updated_at":5}')) as h:
            self.assertEqual(h.client.get_feed("all"), FeedSettings("all", "t", 5))
            self.assertEqual(h.requests[0].url, "/api/v1/feeds/all")

    def test_get_feed_escapes_the_user(self) -> None:
        with Hub() as h:
            h.client.get_feed("f", "a&b=c d")
            self.assertEqual(parse_qs(urlsplit(h.requests[0].url).query), {"external_user_id": ["a&b=c d"]})

    def test_an_unknown_group_in_an_audience_is_a_400(self) -> None:
        body = '{"error":"invalid_request","field":"audience.groups","message":"unknown group"}'
        with Hub(answer(400, body)) as h:
            with self.assertRaises(HanshoError) as cm:
                h.client.put_channel("pager", Channel("Pager", "page", "page", audience=Audience("only", groups=["typo"])))
            e = cm.exception
            self.assertEqual((e.status, e.code, e.field, e.detail), (400, "invalid_request", "audience.groups", "unknown group"))
            self.assertEqual(len(h.requests), 1)

    def test_no_access_error_code(self) -> None:
        with Hub(answer(403, '{"error":"no_access"}')) as h:
            with self.assertRaises(HanshoError) as cm:
                h.client.start_activity(Activity("u", "c", "t", {}))
            self.assertEqual((cm.exception.status, cm.exception.code), (403, CODE_NO_ACCESS))


class Sending(unittest.TestCase):
    def test_targets_on_the_wire(self) -> None:
        with Hub() as h:
            h.client.send_message(Message(external_user_ids=["alice", "bob"], channel="c", title="t", body="b"))
            self.assertEqual(
                json.loads(h.requests[0].body), {"external_user_ids": ["alice", "bob"], "channel": "c", "title": "t", "body": "b"}
            )
            h.client.send_message(Message(to_audience=True, channel="c", title="t", body="b"))
            self.assertEqual(json.loads(h.requests[1].body), {"to_audience": True, "channel": "c", "title": "t", "body": "b"})

    def test_a_multi_target_result_has_recipients_and_no_top_level_message(self) -> None:
        body = (
            '{"level":"active","recipients":['
            '{"external_user_id":"alice","message_id":"msg_1","status":"queued","devices":1,"deliveries":[{"device_id":"d1","status":"queued"}]},'
            '{"external_user_id":"carol","status":"no_access"}]}'
        )
        with Hub(answer(202, body)) as h:
            res = h.client.send_message(Message(external_user_ids=["alice", "carol"], channel="c", title="t", body="b"))
        self.assertEqual((res.message_id, res.devices, res.status, res.deliveries), (None, 0, None, []))
        self.assertEqual(
            res.recipients,
            [
                Recipient("alice", "queued", "msg_1", 1, [Delivery("d1", "queued")]),
                Recipient("carol", RECIPIENT_NO_ACCESS),
            ],
        )
        self.assertIsNone(res.recipients[1].message_id)

    def test_a_single_user_without_access_keeps_the_top_level_fields(self) -> None:
        body = (
            '{"message_id":"","level":"active","devices":0,"status":"no_access","deliveries":[],'
            '"recipients":[{"external_user_id":"carol","status":"no_access"}]}'
        )
        with Hub(answer(202, body)) as h:
            res = h.client.send_message(Message("carol", "c", "t", "b"))
        self.assertEqual((res.message_id, res.devices, res.status), (None, 0, RECIPIENT_NO_ACCESS))
        self.assertEqual(res.recipients, [Recipient("carol", "no_access")])

    def test_an_answer_without_recipients_still_parses(self) -> None:
        with Hub(answer(202, '{"message_id":"m1","level":"active","devices":1,"status":"queued","deliveries":[]}')) as h:
            res = h.client.send_message(Message("u", "c", "t", "b"))
        self.assertEqual((res.message_id, res.recipients), ("m1", []))


class Kick(unittest.TestCase):
    def test_kick_is_unpair(self) -> None:
        with Hub() as h:
            self.assertIsNone(h.client.kick("user 1"))
            self.assertEqual((h.requests[0].method, h.requests[0].url), ("DELETE", "/api/v1/users/user%201/pairings"))
            with self.assertRaises(ValueError):
                h.client.kick("")


if __name__ == "__main__":
    unittest.main()

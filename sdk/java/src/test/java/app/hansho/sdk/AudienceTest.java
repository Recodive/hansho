package app.hansho.sdk;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.IOException;
import java.util.List;
import org.junit.jupiter.api.Test;

class AudienceTest {
    static final ObjectMapper MAPPER = new ObjectMapper();
    static final Channel PAGER = Channel.builder("Pager", Level.PAGE, Level.PAGE).build();

    static JsonNode body(TestHub hub, int i) throws IOException {
        return MAPPER.readTree(hub.requests.get(i).body());
    }

    static JsonNode json(String s) throws IOException {
        return MAPPER.readTree(s);
    }

    static Message toOne() {
        return Message.builder("u", "c", "t", "b").build();
    }

    // --- groups ------------------------------------------------------------------

    @Test
    void putGroupReplacesTheWholeMemberList() throws Exception {
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(200, "{\"id\":\"oncall\",\"members\":[\"alice\",\"bob\"]}"))) {
            Group g = hub.client().putGroup("oncall", List.of("bob", "alice"));
            TestHub.Req r = hub.requests.get(0);
            assertEquals("PUT", r.method());
            assertEquals("/api/v1/groups/oncall", r.uri());
            assertEquals(json("{\"members\":[\"bob\",\"alice\"]}"), body(hub, 0));
            assertEquals(new Group("oncall", List.of("alice", "bob")), g);
            hub.client().putGroup("oncall", List.of());
            assertEquals(json("{\"members\":[]}"), body(hub, 1));
        }
    }

    @Test
    void getsAndDeletesAGroupEscapingTheId() throws Exception {
        try (TestHub hub = new TestHub().on(r -> r.method().equals("GET")
                ? TestHub.Reply.json(200, "{\"id\":\"a b\",\"members\":[]}")
                : TestHub.Reply.status(204))) {
            assertEquals(new Group("a b", List.of()), hub.client().getGroup("a b"));
            hub.client().deleteGroup("a b");
            assertEquals(List.of("/api/v1/groups/a%20b", "/api/v1/groups/a%20b"), hub.uris());
            assertEquals("DELETE", hub.requests.get(1).method());
            assertThrows(IllegalArgumentException.class, () -> hub.client().putGroup("", List.of()));
            assertThrows(IllegalArgumentException.class, () -> hub.client().getGroup(".."));
            assertEquals(2, hub.requests.size());
        }
    }

    @Test
    void aMissingGroupIsUnknownGroup() throws Exception {
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(404, "{\"error\":\"unknown_group\"}"))) {
            HanshoException e = assertThrows(HanshoException.class, () -> hub.client().getGroup("gone"));
            assertEquals(404, e.status());
            assertEquals(HanshoException.UNKNOWN_GROUP, e.code());
        }
    }

    // --- audiences ---------------------------------------------------------------

    @Test
    void putsAChannelAudienceAndOmitsItWhenAbsent() throws Exception {
        try (TestHub hub = new TestHub()) {
            hub.client().putChannel("pager", Channel.builder("Pager", Level.PAGE, Level.PAGE)
                    .audience(Audience.only(List.of("alice"), List.of("oncall"))).build());
            assertEquals(json("{\"mode\":\"only\",\"users\":[\"alice\"],\"groups\":[\"oncall\"]}"), body(hub, 0).get("audience"));

            hub.client().putChannel("pager", PAGER);
            assertFalse(body(hub, 1).has("audience"), "an absent audience keeps the stored one");

            hub.client().putChannel("pager", Channel.builder("Pager", Level.PAGE, Level.PAGE).audience(Audience.everyone()).build());
            assertEquals(json("{\"mode\":\"everyone\"}"), body(hub, 2).get("audience"));
        }
    }

    @Test
    void readsAChannelAudience() throws Exception {
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(200,
                "{\"name\":\"Ops\",\"default_level\":\"active\",\"max_level\":\"page\",\"audience\":{\"mode\":\"except\",\"users\":[\"bob\"],\"groups\":[]}}"))) {
            Channel c = hub.client().getChannel("ops");
            assertEquals(Audience.except(List.of("bob"), List.of()), c.audience());
        }
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(200, "{\"name\":\"Ops\",\"audience\":{\"mode\":\"newmode\"}}"))) {
            assertNull(hub.client().getChannel("ops").audience().mode(), "an unknown mode is null, not an exception");
        }
    }

    @Test
    void aFeedCarriesItsChannelAndAudience() throws Exception {
        try (TestHub hub = new TestHub()) {
            hub.client().putFeed("queue", Feed.of("status", 1).withChannel("pager"));
            JsonNode plain = body(hub, 0);
            assertEquals("pager", plain.get("channel").asText());
            assertFalse(plain.has("audience"));

            hub.client().putFeed("queue", Feed.of("status", 1).withAudience(Audience.except(List.of(), List.of("interns"))));
            JsonNode own = body(hub, 1);
            assertFalse(own.has("channel"));
            assertEquals(json("{\"mode\":\"except\",\"groups\":[\"interns\"]}"), own.get("audience"));
        }
    }

    @Test
    void getFeedReadsTheSettingsWithoutTheData() throws Exception {
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(200,
                "{\"feed_id\":\"queue\",\"external_user_id\":\"u 1\",\"template_id\":\"status\",\"title\":\"Queue\",\"channel\":\"pager\",\"updated_at\":1760000000}"))) {
            FeedSettings f = hub.client().getFeed("queue", "u 1");
            assertEquals("GET", hub.requests.get(0).method());
            assertEquals("/api/v1/feeds/queue?external_user_id=u%201", hub.requests.get(0).uri());
            assertEquals(new FeedSettings("queue", "u 1", "status", "Queue", "pager", null, 1760000000L), f);

            hub.client().getFeed("queue");
            assertEquals("/api/v1/feeds/queue", hub.requests.get(1).uri());
        }
    }

    @Test
    void anAudienceNamingAnUnknownGroupIsAValidationError() throws Exception {
        String err = "{\"error\":\"invalid_request\",\"field\":\"audience.groups\",\"message\":\"unknown group\"}";
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(400, err))) {
            Audience typo = Audience.only(List.of(), List.of("typo"));
            HanshoException e = assertThrows(HanshoException.class, () -> hub.client().putChannel("pager",
                    Channel.builder("Pager", Level.PAGE, Level.PAGE).audience(typo).build()));
            assertEquals(400, e.status());
            assertEquals("audience.groups", e.field());
            assertEquals("unknown group", e.detail());
            assertThrows(HanshoException.class, () -> hub.client().putFeed("f", Feed.of("t", 1).withAudience(typo)));
        }
    }

    // --- kick --------------------------------------------------------------------

    @Test
    void kickDeletesTheUsersPairings() throws Exception {
        try (TestHub hub = new TestHub().always(TestHub.Reply.status(204))) {
            hub.client().kick("user 42");
            assertEquals("DELETE", hub.requests.get(0).method());
            assertEquals("/api/v1/users/user%2042/pairings", hub.requests.get(0).uri());
            assertThrows(IllegalArgumentException.class, () -> hub.client().kick(""));
        }
    }

    // --- sending -----------------------------------------------------------------

    @Test
    void sendsToSeveralUsersOrToTheAudience() throws Exception {
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(202, "{}"))) {
            hub.client().sendMessage(Message.forUsers(List.of("alice", "bob"), "c", "t", "b").build());
            assertEquals(json("{\"external_user_ids\":[\"alice\",\"bob\"],\"channel\":\"c\",\"title\":\"t\",\"body\":\"b\"}"), body(hub, 0));
            hub.client().sendMessage(Message.forAudience("c", "t", "b").build());
            assertEquals(json("{\"to_audience\":true,\"channel\":\"c\",\"title\":\"t\",\"body\":\"b\"}"), body(hub, 1));
            hub.client().sendMessage(toOne());
            assertEquals(json("{\"external_user_id\":\"u\",\"channel\":\"c\",\"title\":\"t\",\"body\":\"b\"}"), body(hub, 2));
        }
    }

    @Test
    void listsEveryRecipientAndMarksThoseWithoutAccess() throws Exception {
        String reply = "{\"level\":\"active\",\"recipients\":["
                + "{\"external_user_id\":\"alice\",\"message_id\":\"msg_1\",\"status\":\"queued\",\"devices\":1,\"deliveries\":[{\"device_id\":\"d1\",\"status\":\"queued\"}]},"
                + "{\"external_user_id\":\"carol\",\"status\":\"no_access\"}]}";
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(202, reply))) {
            MessageResult res = hub.client().sendMessage(Message.forUsers(List.of("alice", "carol"), "c", "t", "b").build());
            assertEquals("", res.messageId());
            assertEquals("", res.status());
            assertEquals(0, res.devices());
            assertEquals(Level.ACTIVE, res.level());
            assertTrue(res.deliveries().isEmpty());
            assertEquals(2, res.recipients().size());
            Recipient alice = res.recipients().get(0);
            assertEquals("msg_1", alice.messageId());
            assertEquals("queued", alice.status());
            assertEquals(1, alice.devices());
            assertEquals(List.of(new Delivery("d1", "queued", null)), alice.deliveries());
            Recipient carol = res.recipients().get(1);
            assertEquals("carol", carol.externalUserId());
            assertEquals(Recipient.NO_ACCESS, carol.status());
            assertNull(carol.messageId());
            assertEquals(0, carol.devices());
        }
    }

    @Test
    void aSingleUserWithoutAccessKeepsTheTopLevelFields() throws Exception {
        String reply = "{\"level\":\"active\",\"external_user_id\":\"u\",\"message_id\":\"\",\"status\":\"no_access\",\"devices\":0,\"deliveries\":[],"
                + "\"recipients\":[{\"external_user_id\":\"u\",\"status\":\"no_access\"}]}";
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(202, reply))) {
            MessageResult res = hub.client().sendMessage(toOne());
            assertEquals("", res.messageId());
            assertEquals(Recipient.NO_ACCESS, res.status());
            assertEquals(0, res.devices());
            assertEquals(Recipient.NO_ACCESS, res.recipients().get(0).status());
        }
    }

    @Test
    void aHubWithoutRecipientsStillParses() throws Exception {
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(202,
                "{\"message_id\":\"m1\",\"level\":\"active\",\"devices\":2,\"status\":\"queued\",\"deliveries\":[]}"))) {
            MessageResult res = hub.client().sendMessage(toOne());
            assertEquals("m1", res.messageId());
            assertEquals(2, res.devices());
            assertTrue(res.recipients().isEmpty());
        }
    }

    @Test
    void noAccessOnAnActivityIsAForbiddenError() throws Exception {
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(403, "{\"error\":\"no_access\"}"))) {
            HanshoException e = assertThrows(HanshoException.class,
                    () -> hub.client().startActivity(Activity.of("u", "c", "t", 1)));
            assertEquals(403, e.status());
            assertEquals(HanshoException.NO_ACCESS, e.code());
        }
    }
}

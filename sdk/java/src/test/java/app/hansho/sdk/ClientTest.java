package app.hansho.sdk;

import app.hansho.sdk.Models.*;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.IOException;
import java.net.URLDecoder;
import java.net.ServerSocket;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneOffset;
import java.time.format.DateTimeFormatter;
import java.util.List;
import java.util.Map;
import java.util.function.Consumer;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;

class ClientTest {
    static final ObjectMapper MAPPER = new ObjectMapper();
    static final TestHub.Reply OK = TestHub.Reply.json(200, "{}");
    static final Channel CHANNEL = Channel.builder("Ops", Level.ACTIVE, Level.CRITICAL).build();

    static Message message() {
        return Message.builder("u", "c", "t", "b").build();
    }

    static TestHub.Reply limited(int status) {
        return TestHub.Reply.json(status, "{\"error\":\"x\"}");
    }

    @Test
    void sendsHeadersAndDefaultUserAgent() throws Exception {
        try (TestHub hub = new TestHub()) {
            hub.client().getChannel("ops");
            TestHub.Req r = hub.requests.get(0);
            assertEquals("Bearer " + TestHub.KEY, r.header("Authorization"));
            assertEquals("application/json", r.header("Accept"));
            assertEquals("hansho-java/" + Hansho.VERSION, r.header("User-Agent"));
            assertNull(r.header("Content-Type"));
            hub.client(b -> b.userAgent("mine/1")).getChannel("ops");
            assertEquals("mine/1", hub.requests.get(1).header("User-Agent"));
        }
    }

    @Test
    void handlesTrailingSlashAndPathPrefixInBaseUrl() throws Exception {
        try (TestHub hub = new TestHub()) {
            Hansho.builder(hub.url() + "/", TestHub.KEY).build().getChannel("a");
            Hansho.builder(hub.url() + "/prefix//", TestHub.KEY).build().getChannel("a");
            assertEquals(List.of("/api/v1/channels/a", "/prefix/api/v1/channels/a"), hub.uris());
        }
    }

    @Test
    void requiresBaseUrlAndApiKey() {
        assertThrows(IllegalArgumentException.class, () -> Hansho.builder("", "k"));
        assertThrows(IllegalArgumentException.class, () -> Hansho.builder("http://x", ""));
        assertThrows(IllegalArgumentException.class, () -> Hansho.builder(null, "k"));
        assertThrows(IllegalArgumentException.class, () -> Hansho.builder("http://x", null));
    }

    @Test
    void doesNotExposeTheKeyInToString() {
        Hansho c = Hansho.create("http://x", TestHub.KEY);
        assertFalse(c.toString().contains(TestHub.KEY));
        assertFalse(new PairingTicket("tk_secret", "hansho://pair?t=tk_secret", null).toString().contains("tk_secret"));
    }

    @Test
    void omitsUnsetOptionalFieldsFromTheWire() throws Exception {
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(202, "{}"))) {
            hub.client().sendMessage(message());
            assertEquals(MAPPER.readTree("{\"external_user_id\":\"u\",\"channel\":\"c\",\"title\":\"t\",\"body\":\"b\"}"),
                    MAPPER.readTree(hub.requests.get(0).body()));
            hub.client().putChannel("c", CHANNEL);
            assertEquals(MAPPER.readTree("{\"name\":\"Ops\",\"default_level\":\"active\",\"max_level\":\"critical\"}"),
                    MAPPER.readTree(hub.requests.get(1).body()));
            hub.client().sendMessage(Message.builder("u", "c", "", "b").build());
            assertEquals("", MAPPER.readTree(hub.requests.get(2).body()).get("title").asText(), "an empty title is sent, the hub rejects it");
        }
    }

    @Test
    void ignoresResponseFieldsItDoesNotKnow() throws Exception {
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(200,
                "{\"id\":\"ops\",\"name\":\"N\",\"default_level\":\"hologram\",\"max_level\":\"page\",\"future\":{\"a\":[1]},\"actions\":[{\"id\":\"a\",\"title\":\"T\",\"extra\":1}]}"))) {
            Channel c = hub.client().getChannel("ops");
            assertEquals("N", c.name());
            assertNull(c.defaultLevel(), "an unknown level is null, not an exception");
            assertEquals(Level.PAGE, c.maxLevel());
            assertEquals(List.of(Action.of("a", "T")), c.actions());
        }
    }

    @Test
    void emptyAndNonObjectSuccessBodiesAreTolerated() throws Exception {
        try (TestHub hub = new TestHub().always(TestHub.Reply.status(204))) {
            assertDoesNotThrow(() -> hub.client().deleteChannel("a"));
        }
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(200, "[1]"))) {
            assertDoesNotThrow(() -> hub.client().deleteChannel("a"));
        }
    }

    @Test
    void invalidJsonInASuccessAnswerIsANetworkError() throws Exception {
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(200, "<html>"))) {
            assertThrows(HanshoNetworkException.class, () -> hub.client().getChannel("a"));
        }
    }

    // --- retries ---------------------------------------------------------------

    @Test
    void retriesPutOn429AndHonoursRetryAfter() throws Exception {
        try (TestHub hub = new TestHub().script(limited(429).with("Retry-After", "2"), limited(429).with("Retry-After", "1.5"), OK)) {
            hub.client().putChannel("ops", CHANNEL);
            assertEquals(3, hub.requests.size());
            assertEquals(List.of(2000L, 1500L), hub.sleeps);
        }
    }

    @Test
    void retriesGetAndDeleteOn503WithJitteredBackoff() throws Exception {
        for (Consumer<Hansho> call : List.<Consumer<Hansho>>of(c -> c.getChannel("a"), c -> c.deleteChannel("a"), c -> c.getMessage("m"), c -> c.unpair("u"),
                c -> c.putTemplate("t", 1, Map.of()), c -> c.putSound("s", "audio/wav", new byte[] {1}), c -> c.putFeed("f", Feed.of("t", 1)),
                c -> c.endActivity("a"))) {
            try (TestHub hub = new TestHub().script(limited(503), limited(503), limited(503), OK)) {
                call.accept(hub.client());
                assertEquals(4, hub.requests.size());
                long[][] bounds = {{125, 250}, {250, 500}, {500, 1000}};
                for (int i = 0; i < 3; i++) {
                    long s = hub.sleeps.get(i);
                    assertTrue(s >= bounds[i][0] && s <= bounds[i][1], "sleep " + i + " was " + s);
                }
            }
        }
    }

    @Test
    void stopsAfterMaxRetriesAndSurfacesTheError() throws Exception {
        try (TestHub hub = new TestHub().always(limited(503))) {
            HanshoException e = assertThrows(HanshoException.class, () -> hub.client().getChannel("a"));
            assertEquals(503, e.status());
            assertEquals(4, hub.requests.size(), "1 attempt + 3 retries");
        }
        try (TestHub hub = new TestHub().always(limited(503))) {
            assertThrows(HanshoException.class, () -> hub.client(b -> b.maxRetries(0)).getChannel("a"));
            assertEquals(1, hub.requests.size());
        }
        try (TestHub hub = new TestHub().always(limited(503))) {
            assertThrows(HanshoException.class, () -> hub.client(b -> b.maxRetries(1)).getChannel("a"));
            assertEquals(2, hub.requests.size());
        }
    }

    @ParameterizedTest
    @ValueSource(ints = {400, 401, 404, 409, 500, 502})
    void doesNotRetryOtherStatuses(int status) throws Exception {
        try (TestHub hub = new TestHub().always(limited(status))) {
            assertThrows(HanshoException.class, () -> hub.client().getChannel("a"));
            assertEquals(1, hub.requests.size());
            assertTrue(hub.sleeps.isEmpty());
        }
    }

    @ParameterizedTest
    @ValueSource(ints = {429, 503})
    void neverRetriesPostMessagesByDefault(int status) throws Exception {
        try (TestHub hub = new TestHub().always(limited(status).with("Retry-After", "1"))) {
            HanshoException e = assertThrows(HanshoException.class, () -> hub.client().sendMessage(message()));
            assertEquals(status, e.status());
            assertEquals(1000, e.retryAfterMs());
            assertEquals(1, hub.requests.size());
            assertNull(hub.requests.get(0).header("Idempotency-Key"));
            // other POSTs are not idempotent either
            assertThrows(HanshoException.class, () -> hub.client().startActivity(Activity.of("u", "c", "t", 1)));
            assertThrows(HanshoException.class, () -> hub.client().createPairingTicket("u"));
            assertEquals(3, hub.requests.size());
        }
    }

    @Test
    void retriesAMessageThatCarriesAnIdempotencyKey() throws Exception {
        try (TestHub hub = new TestHub().script(limited(503), TestHub.Reply.json(202, "{\"message_id\":\"m1\",\"devices\":1}"))) {
            MessageResult r = hub.client().sendMessage(
                    Message.builder("u", "c", "t", "b").idempotencyKey("key-123").build());
            assertEquals("m1", r.messageId());
            assertEquals(2, hub.requests.size());
            for (TestHub.Req q : hub.requests) {
                assertEquals("key-123", q.header("Idempotency-Key"));
                assertFalse(q.bodyText().contains("key-123"), "the key must not leak into the body");
            }
        }
    }

    @Test
    void doesNotRetryPatch() throws Exception {
        try (TestHub hub = new TestHub().always(limited(503))) {
            assertThrows(HanshoException.class, () -> hub.client().updateActivity("a", ActivityUpdate.of(1)));
            assertEquals(1, hub.requests.size());
        }
    }

    @Test
    void surfacesARetryAfterBeyondTheCap() throws Exception {
        try (TestHub hub = new TestHub().always(limited(429).with("Retry-After", "3600"))) {
            HanshoException e = assertThrows(HanshoException.class, () -> hub.client().putChannel("ops", CHANNEL));
            assertEquals(3_600_000, e.retryAfterMs());
            assertEquals(Duration.ofHours(1), e.retryAfter());
            assertEquals(1, hub.requests.size());
        }
        try (TestHub hub = new TestHub().script(limited(429).with("Retry-After", "60"), OK)) {
            hub.client().putChannel("ops", CHANNEL);
            assertEquals(List.of(60_000L), hub.sleeps, "exactly 60 s is still waited out");
        }
    }

    @Test
    void parsesRetryAfter() {
        Instant now = Instant.parse("2026-11-08T12:00:00Z");
        String date = DateTimeFormatter.RFC_1123_DATE_TIME.format(now.plusSeconds(5).atOffset(ZoneOffset.UTC));
        assertEquals(5000, Retry.parseRetryAfter(date, now));
        assertEquals(0, Retry.parseRetryAfter(DateTimeFormatter.RFC_1123_DATE_TIME.format(now.minusSeconds(5).atOffset(ZoneOffset.UTC)), now));
        assertEquals(7000, Retry.parseRetryAfter(" 7 ", now));
        assertEquals(1500, Retry.parseRetryAfter("1.5", now));
        for (String bad : new String[] {null, "", "soon", "-3"}) {
            assertEquals(0, Retry.parseRetryAfter(bad, now), String.valueOf(bad));
        }
    }

    @Test
    void anInterruptedRetryWaitIsANetworkError() throws Exception {
        try (TestHub hub = new TestHub().always(limited(503))) {
            Hansho c = hub.client(b -> b.sleeper(ms -> {
                throw new InterruptedException();
            }));
            assertThrows(HanshoNetworkException.class, () -> c.getChannel("a"));
            assertTrue(Thread.interrupted(), "the interrupt flag is restored");
        }
    }

    // --- errors ------------------------------------------------------------------

    @Test
    void apiErrorsCarryStatusCodeFieldAndNeverTheKeyOrBody() throws Exception {
        String body = "{\"error\":\"invalid_request\",\"field\":\"title\",\"message\":\"required, not " + TestHub.KEY + "\"}";
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(400, body))) {
            Message secret = Message.builder("u", "c", "", "SECRET BODY TEXT").build();
            HanshoException e = assertThrows(HanshoException.class, () -> hub.client().sendMessage(secret));
            assertEquals(400, e.status());
            assertEquals("invalid_request", e.code());
            assertEquals("title", e.field());
            assertEquals(0, e.retryAfterMs());
            assertFalse(e.detail().contains(TestHub.KEY));
            assertFalse(e.getMessage().contains(TestHub.KEY));
            assertFalse(e.getMessage().contains("SECRET BODY TEXT"));
            assertEquals("hansho: POST /api/v1/messages: 400 invalid_request (field title)", e.getMessage());
        }
    }

    @Test
    void aNetworkFailureIsADifferentErrorType() throws Exception {
        int port;
        try (ServerSocket s = new ServerSocket(0)) {
            port = s.getLocalPort();
        }
        Hansho c = Hansho.builder("http://127.0.0.1:" + port + "/", TestHub.KEY).build();
        HanshoNetworkException e = assertThrows(HanshoNetworkException.class, () -> c.getChannel("a"));
        assertFalse(e.getMessage().contains(TestHub.KEY));
        assertTrue(e.getMessage().startsWith("hansho: GET /api/v1/channels/a: "));
        assertTrue(e.getCause() instanceof IOException);
    }

    @Test
    void timesOut() throws Exception {
        try (TestHub hub = new TestHub().on(r -> {
            try {
                Thread.sleep(1500);
            } catch (InterruptedException ignored) {
                Thread.currentThread().interrupt();
            }
            return OK;
        })) {
            Hansho c = hub.client(b -> b.timeout(Duration.ofMillis(200)));
            assertThrows(HanshoNetworkException.class, () -> c.getChannel("a"));
        }
    }

    @Test
    void networkErrorsDoNotLeakTheKey() {
        Hansho c = Hansho.builder("http://127.0.0.1:1", TestHub.KEY).build();
        HanshoNetworkException e = assertThrows(HanshoNetworkException.class, () -> c.getChannel("a"));
        assertFalse(e.toString().contains(TestHub.KEY));
    }

    // --- argument checks and path escaping ----------------------------------------

    static final String[] IDS = {"a/b", "a?b=c", "a b", "ü", "日本", "a#b", "100%", "a/../b", "a%2Fb", "a+b", "a\"b"};

    @Test
    void escapesIdsInEveryPath() throws Exception {
        for (String id : IDS) {
            try (TestHub hub = new TestHub()) {
                Hansho c = hub.client();
                String[][] cases = {{"/api/v1/channels/"}, {"/api/v1/channels/"}, {"/api/v1/channels/"}, {"/api/v1/templates/"},
                        {"/api/v1/sounds/"}, {"/api/v1/feeds/"}, {"/api/v1/messages/"}, {"/api/v1/activities/"}, {"/api/v1/activities/"}};
                c.putChannel(id, CHANNEL);
                c.getChannel(id);
                c.deleteChannel(id);
                c.putTemplate(id, 1, Map.of());
                c.putSound(id, "audio/wav", new byte[] {1});
                c.putFeed(id, Feed.of("t", 1));
                c.getMessage(id);
                c.updateActivity(id, ActivityUpdate.of(1));
                c.endActivity(id);
                for (int i = 0; i < cases.length; i++) {
                    String target = hub.requests.get(i).uri();
                    assertTrue(target.startsWith(cases[i][0]), target);
                    String seg = target.substring(cases[i][0].length());
                    assertFalse(seg.matches(".*[/?# ].*"), "unescaped segment in " + target);
                    assertEquals(id, URLDecoder.decode(seg.replace("+", "%2B"), StandardCharsets.UTF_8));
                }
                c.unpair(id);
                String t = hub.requests.get(cases.length).uri();
                assertTrue(t.matches("/api/v1/users/[^/?# ]+/pairings"), t);
            }
        }
    }

    @Test
    void producesTheExactExpectedEncoding() throws Exception {
        try (TestHub hub = new TestHub()) {
            hub.client().putChannel("ü x/y?z", CHANNEL);
            assertEquals("/api/v1/channels/%C3%BC%20x%2Fy%3Fz", hub.requests.get(0).uri());
            hub.client().getChannel("日本");
            assertEquals("/api/v1/channels/%E6%97%A5%E6%9C%AC", hub.requests.get(1).uri());
            hub.client().getChannel("a.b-c_d~e");
            assertEquals("/api/v1/channels/a.b-c_d~e", hub.requests.get(2).uri());
        }
    }

    @Test
    void rejectsDotSegmentsAndEmptyIdsBeforeSending() throws Exception {
        try (TestHub hub = new TestHub()) {
            Hansho c = hub.client();
            for (String bad : new String[] {"", ".", ".."}) {
                assertThrows(IllegalArgumentException.class, () -> c.getChannel(bad), bad);
                assertThrows(IllegalArgumentException.class, () -> c.unpair(bad), bad);
                assertThrows(IllegalArgumentException.class, () -> c.endActivity(bad), bad);
            }
            assertThrows(IllegalArgumentException.class, () -> c.createPairingTicket(""));
            assertThrows(IllegalArgumentException.class, () -> c.putSound("s", "audio/wav", new byte[(1 << 20) + 1]));
            assertThrows(IllegalArgumentException.class, () -> ActivityUpdate.of(1).withPriority(7));
            assertEquals(0, hub.requests.size());
        }
    }

    @Test
    void setsAndClearsADisplayName() throws Exception {
        try (TestHub hub = new TestHub().always(TestHub.Reply.status(204))) {
            Hansho c = hub.client();
            c.setUserDisplayName("a b/ü", "Ada \"L\" 日本");
            TestHub.Req r = hub.requests.get(0);
            assertEquals("PUT", r.method());
            assertEquals("/api/v1/users/a%20b%2F%C3%BC", r.uri());
            assertEquals(MAPPER.readTree("{\"display_name\":\"Ada \\\"L\\\" 日本\"}"), MAPPER.readTree(r.body()));
            c.setUserDisplayName("u", "");
            assertEquals(MAPPER.readTree("{\"display_name\":\"\"}"), MAPPER.readTree(hub.requests.get(1).body()));
            for (String bad : new String[] {"", ".", ".."}) {
                assertThrows(IllegalArgumentException.class, () -> c.setUserDisplayName(bad, "x"), bad);
            }
            assertThrows(IllegalArgumentException.class, () -> c.setUserDisplayName(null, "x"));
            assertEquals(2, hub.requests.size());
        }
    }

    @Test
    void pairingTicketBodyHasADisplayNameOnlyWhenGiven() throws Exception {
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(201, "{\"ticket\":\"t\",\"qr\":\"q\",\"expires_at\":\"2026-11-08T12:10:00Z\"}"))) {
            Hansho c = hub.client();
            c.createPairingTicket("u");
            assertEquals(MAPPER.readTree("{\"external_user_id\":\"u\"}"), MAPPER.readTree(hub.requests.get(0).body()));
            c.createPairingTicket("u", null);
            assertEquals(MAPPER.readTree("{\"external_user_id\":\"u\"}"), MAPPER.readTree(hub.requests.get(1).body()));
            c.createPairingTicket("u", "Ada Lovelace");
            assertEquals(MAPPER.readTree("{\"external_user_id\":\"u\",\"display_name\":\"Ada Lovelace\"}"), MAPPER.readTree(hub.requests.get(2).body()));
            c.createPairingTicket("u", "");
            assertEquals(MAPPER.readTree("{\"external_user_id\":\"u\",\"display_name\":\"\"}"), MAPPER.readTree(hub.requests.get(3).body()));
            assertThrows(IllegalArgumentException.class, () -> c.createPairingTicket("", "x"));
            assertEquals(4, hub.requests.size());
        }
    }

    @Test
    void pageAckIsSentOnlyWhenSet() throws Exception {
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(202, "{}"))) {
            Hansho c = hub.client();
            c.sendMessage(Message.builder("u", "c", "t", "b").level(Level.PAGE).page(PageOptions.of(60, 3)).build());
            assertEquals(MAPPER.readTree("{\"interval_s\":60,\"max_retries\":3}"), MAPPER.readTree(hub.requests.get(0).body()).get("page"));
            c.sendMessage(Message.builder("u", "c", "t", "b").level(Level.PAGE).page(PageOptions.of(60, 3).withAck(PageAck.EACH)).build());
            assertEquals(MAPPER.readTree("{\"interval_s\":60,\"max_retries\":3,\"ack\":\"each\"}"), MAPPER.readTree(hub.requests.get(1).body()).get("page"));
            c.sendMessage(Message.builder("u", "c", "t", "b").level(Level.PAGE).page(PageOptions.ack(PageAck.ANY)).build());
            assertEquals(MAPPER.readTree("{\"ack\":\"any\"}"), MAPPER.readTree(hub.requests.get(2).body()).get("page"));
        }
    }

    @Test
    void messageStatusCarriesWhoAcknowledged() throws Exception {
        String base = "\"message_id\":\"m\",\"status\":\"acked\",\"level\":\"page\",\"attempts\":1,\"deliveries\":[],\"acked_at\":\"2026-11-08T12:05:00Z\"";
        try (TestHub hub = new TestHub()) {
            hub.always(TestHub.Reply.json(200, "{" + base + ",\"acked_by\":\"alice\",\"acked_by_name\":\"Alice A.\"}"));
            MessageStatus s = hub.client().getMessage("m");
            assertEquals("alice", s.ackedBy());
            assertEquals("Alice A.", s.ackedByName());
            hub.always(TestHub.Reply.json(200, "{" + base + ",\"acked_by\":\"alice\"}"));
            s = hub.client().getMessage("m");
            assertEquals("alice", s.ackedBy());
            assertNull(s.ackedByName());
            hub.always(TestHub.Reply.json(200, "{\"message_id\":\"m\",\"status\":\"queued\",\"level\":\"page\",\"attempts\":0,\"deliveries\":[]}"));
            s = hub.client().getMessage("m");
            assertNull(s.ackedBy());
            assertNull(s.ackedByName());
            assertNull(s.ackedAt());
        }
    }

    @Test
    void sendsDataKeysUntouched() throws Exception {
        try (TestHub hub = new TestHub().always(TestHub.Reply.json(202, "{}"))) {
            hub.client().sendMessage(Message.builder("u", "c", "t", "b").data(Map.of("snake_case", Map.of("camelCase", 1))).build());
            JsonNode data = MAPPER.readTree(hub.requests.get(0).body()).get("data");
            assertEquals(1, data.get("snake_case").get("camelCase").asInt());
        }
    }
}

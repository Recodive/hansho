package app.hansho.sdk;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.HttpServer;
import java.io.IOException;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.HexFormat;
import java.util.List;
import java.util.Set;
import java.util.stream.Stream;
import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;
import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestFactory;

class WebhookTest {
    static final ObjectMapper MAPPER = new ObjectMapper();
    static final JsonNode VECTORS = load();
    static final long T = 1_700_000_000L;

    static JsonNode load() {
        try {
            return MAPPER.readTree(Files.readString(Path.of("..", "spec", "webhook-vectors.json"), StandardCharsets.UTF_8));
        } catch (IOException e) {
            throw new IllegalStateException(e);
        }
    }

    static String sign(String secret, long at, String body) throws Exception {
        Mac mac = Mac.getInstance("HmacSHA256");
        mac.init(new SecretKeySpec(secret.getBytes(StandardCharsets.UTF_8), "HmacSHA256"));
        mac.update((at + ".").getBytes(StandardCharsets.UTF_8));
        return "t=" + at + ",v1=" + HexFormat.of().formatHex(mac.doFinal(body.getBytes(StandardCharsets.UTF_8)));
    }

    static WebhookException.Code refusal(org.junit.jupiter.api.function.Executable r) {
        return assertThrows(WebhookException.class, r).code();
    }

    static JsonNode vector(String name) {
        for (JsonNode v : VECTORS.get("vectors")) {
            if (v.get("name").asText().equals(name)) {
                return v;
            }
        }
        throw new AssertionError("no vector " + name);
    }

    static WebhookEvent verifyVector(JsonNode v) {
        return Webhook.verify(v.get("secret").asText().getBytes(StandardCharsets.UTF_8), v.get("header").asText(),
                v.get("body").asText().getBytes(StandardCharsets.UTF_8), Instant.ofEpochSecond(v.get("now").asLong()),
                Duration.ofSeconds(VECTORS.get("tolerance_seconds").asLong()));
    }

    @TestFactory
    Stream<DynamicTest> sharedVectors() {
        assertTrue(VECTORS.get("vectors").size() >= 20);
        Set<String> classes = new HashSet<>();
        List<DynamicTest> tests = new ArrayList<>();
        for (JsonNode v : VECTORS.get("vectors")) {
            tests.add(DynamicTest.dynamicTest(v.get("name").asText(), () -> {
                JsonNode expect = v.get("expect");
                if (!expect.get("ok").asBoolean()) {
                    classes.add(expect.get("error").asText());
                    assertEquals(expect.get("error").asText(), refusal(() -> verifyVector(v)).wire());
                    return;
                }
                WebhookEvent ev = verifyVector(v);
                assertEquals(expect.get("type").asText(), ev.type());
                assertEquals(expect.get("id").asText(), ev.id());
                assertEquals(Instant.ofEpochSecond(expect.get("created_at").asLong()), ev.createdAt());
                if (ev instanceof WebhookEvent.Unknown u) {
                    classes.add("unknown");
                    assertEquals(expect.get("name").asText(), u.name());
                    assertEquals(expect.get("data"), MAPPER.valueToTree(u.data()));
                }
                assertEquals(v.get("body").asText(), new String(ev.raw(), StandardCharsets.UTF_8));
                assertEquals(expect.get("data"), MAPPER.readTree(ev.raw()).get("data"));
            }));
        }
        tests.add(DynamicTest.dynamicTest("covers stale, bad signature, malformed header and unknown events", () -> {
            // runs last, after the vector tests have filled classes
            for (String c : List.of("stale", "bad_signature", "malformed_header", "unknown")) {
                assertTrue(classes.contains(c), c);
            }
        }));
        return tests.stream();
    }

    @Test
    void replayProtection() throws Exception {
        String body = "{\"id\":\"1\",\"event\":\"message.acked\",\"created_at\":1,\"data\":{}}";
        String header = sign("s", T, body);
        for (long o : new long[] {0, 300, -300}) {
            assertDoesNotThrow(() -> Webhook.verify("s", header, body, Instant.ofEpochSecond(T + o), null), "offset " + o);
        }
        for (long o : new long[] {301, -301, 86400}) {
            assertEquals(WebhookException.Code.STALE, refusal(() -> Webhook.verify("s", header, body, Instant.ofEpochSecond(T + o), null)));
        }
        assertDoesNotThrow(() -> Webhook.verify("s", header, body, Instant.ofEpochSecond(T + 301), Duration.ofMinutes(10)));
        assertEquals(WebhookException.Code.STALE, refusal(() -> Webhook.verify("s", header, body, Instant.ofEpochSecond(T + 11), Duration.ofSeconds(10))));
        assertDoesNotThrow(() -> Webhook.verify("s", header, body, Instant.ofEpochSecond(T + 10), Duration.ofSeconds(10)));
        // a tolerance that is not positive means the default
        assertDoesNotThrow(() -> Webhook.verify("s", header, body, Instant.ofEpochSecond(T + 300), Duration.ZERO));
        // the clock is the default
        String fresh = sign("s", Instant.now().getEpochSecond(), body);
        assertDoesNotThrow(() -> Webhook.verify("s", fresh, body));
        assertEquals(WebhookException.Code.STALE, refusal(() -> Webhook.verify("s", header, body)));
    }

    @Test
    void rawBodyHandling() throws Exception {
        JsonNode valid = vector("valid message.acked");
        String body = valid.get("body").asText();
        String header = valid.get("header").asText();
        String secret = valid.get("secret").asText();
        Instant now = Instant.ofEpochSecond(valid.get("now").asLong());
        assertEquals("message.acked", Webhook.verify(secret, header, body.getBytes(StandardCharsets.UTF_8), now, null).type());
        assertEquals("message.acked", Webhook.verify(secret, header, body, now, null).type());

        JsonNode parsed = MAPPER.readTree(body);
        for (String reserialised : new String[] {MAPPER.writerWithDefaultPrettyPrinter().writeValueAsString(parsed), body + "\n", " " + body}) {
            assertEquals(WebhookException.Code.BAD_SIGNATURE, refusal(() -> Webhook.verify(secret, header, reserialised, now, null)));
        }
        assertEquals(WebhookException.Code.INVALID_BODY, refusal(() -> Webhook.verify(secret, header, (byte[]) null)));
        assertEquals(WebhookException.Code.INVALID_BODY, refusal(() -> Webhook.verify(secret, header, (String) null)));
        assertEquals("X-Hansho-Webhook-Signature", Webhook.SIGNATURE_HEADER);
    }

    @Test
    void verifiesTheBytesItIsGivenFromAnHttpServer() throws Exception {
        JsonNode valid = vector("valid message.acked");
        JsonNode pairing = vector("valid pairing.created");
        Instant now = Instant.ofEpochSecond(T);
        List<String> seen = new java.util.concurrent.CopyOnWriteArrayList<>();
        HttpServer server = HttpServer.create(new InetSocketAddress(InetAddress.getLoopbackAddress(), 0), 0);
        server.createContext("/", ex -> {
            byte[] body = ex.getRequestBody().readAllBytes();
            int status;
            try {
                seen.add(Webhook.verify(valid.get("secret").asText().getBytes(StandardCharsets.UTF_8),
                        ex.getRequestHeaders().getFirst(Webhook.SIGNATURE_HEADER), body, now, null).type());
                status = 200;
            } catch (WebhookException e) {
                status = 400;
            }
            ex.sendResponseHeaders(status, -1);
            ex.close();
        });
        server.start();
        try {
            HttpClient http = HttpClient.newHttpClient();
            URI url = URI.create("http://127.0.0.1:" + server.getAddress().getPort() + "/");
            java.util.function.BiFunction<String, String, Integer> post = (header, body) -> {
                try {
                    return http.send(HttpRequest.newBuilder(url).header(Webhook.SIGNATURE_HEADER, header)
                            .POST(HttpRequest.BodyPublishers.ofString(body)).build(), HttpResponse.BodyHandlers.discarding()).statusCode();
                } catch (Exception e) {
                    throw new IllegalStateException(e);
                }
            };
            assertEquals(200, post.apply(valid.get("header").asText(), valid.get("body").asText()));
            assertEquals(400, post.apply(valid.get("header").asText(), MAPPER.writerWithDefaultPrettyPrinter().writeValueAsString(MAPPER.readTree(valid.get("body").asText()))));
            assertEquals(200, post.apply(pairing.get("header").asText(), pairing.get("body").asText()), "non-ASCII body, verified byte for byte");
        } finally {
            server.stop(0);
        }
        assertEquals(List.of("message.acked", "pairing.created"), seen);
    }

    @Test
    void secretAndHeaderValidation() throws Exception {
        String body = "{\"id\":\"1\",\"event\":\"action\",\"created_at\":1,\"data\":{}}";
        long t = Instant.now().getEpochSecond();
        assertDoesNotThrow(() -> Webhook.verify("s".getBytes(StandardCharsets.UTF_8), sign("s", t, body), body.getBytes(StandardCharsets.UTF_8)));
        assertEquals(WebhookException.Code.INVALID_SECRET, refusal(() -> Webhook.verify("", sign("x", t, body), body)));
        assertEquals(WebhookException.Code.INVALID_SECRET, refusal(() -> Webhook.verify(new byte[0], sign("x", t, body), body.getBytes(StandardCharsets.UTF_8))));
        assertEquals(WebhookException.Code.INVALID_SECRET, refusal(() -> Webhook.verify((String) null, sign("s", t, body), body)));
        for (String header : new String[] {null, "", "   "}) {
            assertEquals(WebhookException.Code.MALFORMED_HEADER, refusal(() -> Webhook.verify("s", header, body)));
        }
        for (String bad : new String[] {"not json", "[]", "{\"id\":\"1\"}", "", "{\"event\":\"\"}", "{\"event\":5}", "{} trailing"}) {
            assertEquals(WebhookException.Code.MALFORMED_BODY, refusal(() -> Webhook.verify("s", sign("s", t, bad), bad)), bad);
        }
        // invalid UTF-8 under a valid signature
        byte[] invalid = {'{', (byte) 0xff, '}'};
        Mac mac = Mac.getInstance("HmacSHA256");
        mac.init(new SecretKeySpec("s".getBytes(StandardCharsets.UTF_8), "HmacSHA256"));
        mac.update((t + ".").getBytes(StandardCharsets.UTF_8));
        String h = "t=" + t + ",v1=" + HexFormat.of().formatHex(mac.doFinal(invalid));
        assertEquals(WebhookException.Code.MALFORMED_BODY, refusal(() -> Webhook.verify("s", h, invalid)));
        // upper-case hex is accepted
        String upper = sign("s", t, body);
        String upperHeader = upper.substring(0, upper.indexOf("v1=") + 3) + upper.substring(upper.indexOf("v1=") + 3).toUpperCase();
        assertDoesNotThrow(() -> Webhook.verify("s", upperHeader, body));
        // an absurd timestamp is stale, never an overflow
        String huge = "9999999999999999";
        Mac m2 = Mac.getInstance("HmacSHA256");
        m2.init(new SecretKeySpec("s".getBytes(StandardCharsets.UTF_8), "HmacSHA256"));
        m2.update((huge + ".").getBytes(StandardCharsets.UTF_8));
        String hh = "t=" + huge + ",v1=" + HexFormat.of().formatHex(m2.doFinal(body.getBytes(StandardCharsets.UTF_8)));
        assertEquals(WebhookException.Code.STALE, refusal(() -> Webhook.verify("s", hh, body)));
    }

    @Test
    void deliversUnknownEventsAsUnknownWithTheRawPayload() throws Exception {
        long t = Instant.now().getEpochSecond();
        String body = "{\"id\":7,\"event\":\"message.holographic\",\"created_at\":5,\"extra\":1,\"data\":{\"message_id\":\"m\",\"new_field\":[1,2]}}";
        WebhookEvent ev = Webhook.verify("s", sign("s", t, body), body);
        WebhookEvent.Unknown u = (WebhookEvent.Unknown) ev;
        assertEquals("unknown", u.type());
        assertEquals("message.holographic", u.name());
        assertEquals(MAPPER.readTree("{\"message_id\":\"m\",\"new_field\":[1,2]}"), MAPPER.valueToTree(u.data()));
        assertEquals("7", u.id());
        assertArrayEquals(body.getBytes(StandardCharsets.UTF_8), u.raw());
    }

    @Test
    void ignoresUnknownFieldsOnKnownEvents() throws Exception {
        long t = Instant.now().getEpochSecond();
        String body = "{\"id\":\"8\",\"event\":\"message.acked\",\"created_at\":5,\"data\":{\"message_id\":\"m\",\"device_id\":\"d\",\"external_user_id\":\"u\",\"from_the_future\":true}}";
        WebhookEvent.MessageAcked a = (WebhookEvent.MessageAcked) Webhook.verify("s", sign("s", t, body), body);
        assertEquals("m", a.messageId());
        assertEquals("u", a.externalUserId());
        assertEquals("d", a.deviceId());
    }

    @Test
    void mapsEveryKnownEvent() {
        java.util.function.Function<String, WebhookEvent> ev = name -> verifyVector(vector("valid " + name));
        WebhookEvent.PairingCreated pc = (WebhookEvent.PairingCreated) ev.apply("pairing.created");
        assertEquals("pair_1", pc.pairingId());
        assertEquals("user-42", pc.externalUserId());
        assertEquals("Bas’s iPhone <Ünï>", pc.deviceName());

        WebhookEvent.PairingRevoked pr = (WebhookEvent.PairingRevoked) ev.apply("pairing.revoked");
        assertEquals("pair_1", pr.pairingId());
        assertEquals("sender", pr.by());

        WebhookEvent.MessageDelivered md = (WebhookEvent.MessageDelivered) ev.apply("message.delivered");
        assertEquals("msg_1", md.messageId());
        assertEquals("ops", md.channel());
        assertEquals(Level.PAGE, md.level());
        assertEquals(2, md.devices());

        WebhookEvent.MessageUndeliverable mu = (WebhookEvent.MessageUndeliverable) ev.apply("message.undeliverable");
        assertEquals("msg_2", mu.messageId());
        assertEquals("no_devices", mu.reason());

        WebhookEvent.MessageAcked ma = (WebhookEvent.MessageAcked) ev.apply("message.acked");
        assertEquals("dev_1", ma.deviceId());

        WebhookEvent.MessageDismissed mdis = (WebhookEvent.MessageDismissed) ev.apply("message.dismissed");
        assertEquals("msg_1", mdis.messageId());
        assertEquals("dev_1", mdis.deviceId());

        WebhookEvent.MessageExpired me = (WebhookEvent.MessageExpired) ev.apply("message.expired");
        assertEquals("msg_3", me.messageId());
        assertEquals(6, me.attempts());

        WebhookEvent.ActionTaken at = (WebhookEvent.ActionTaken) ev.apply("action");
        assertEquals("msg_1", at.messageId());
        assertEquals("approve", at.actionId());
        assertEquals("dev_1", at.deviceId());
        assertEquals("user-42", at.externalUserId());
        assertEquals("ship it", at.text());

        WebhookEvent.ActivityEnded ae = (WebhookEvent.ActivityEnded) ev.apply("activity.ended");
        assertEquals("act_1", ae.activityId());
        assertEquals("timeout", ae.reason());
    }
}

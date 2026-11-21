package app.hansho.sdk;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Instant;
import java.util.Base64;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.function.Function;
import java.util.stream.Stream;
import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestFactory;

/** Every operation and error of ../spec/api.json, produced by the SDK against a local fake hub. */
class SpecTest {
    static final ObjectMapper MAPPER = new ObjectMapper();
    static final JsonNode SPEC = load();

    static JsonNode load() {
        try {
            return MAPPER.readTree(Files.readString(Path.of("..", "spec", "api.json"), StandardCharsets.UTF_8));
        } catch (IOException e) {
            throw new IllegalStateException(e);
        }
    }

    static final byte[] WAV = Base64.getDecoder().decode("UklGRgAAAABXQVZF");

    static final Map<String, Function<Hansho, Object>> CALLS = new HashMap<>();
    static final Map<String, Object> WANT = new HashMap<>();

    static void op(String name, Function<Hansho, Object> call, Object want) {
        CALLS.put(name, call);
        WANT.put(name, want);
    }

    static {
        op("createPairingTicket", c -> c.createPairingTicket("user-42"), new PairingTicket(
                "tk_abc", "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=tk_abc&fp=ff00", Instant.parse("2026-11-08T12:10:00Z")));
        op("unpair", c -> {
            c.unpair("user-42");
            return null;
        }, null);
        op("putChannel", c -> {
            c.putChannel("ops", Channel.builder("Operations", Level.ACTIVE, Level.CRITICAL)
                    .sound("ding").templateId("status").thread("ops-thread")
                    .actions(List.of(Action.acknowledge("ack", "Acknowledge"), Action.text("reply", "Reply")))
                    .build());
            return null;
        }, null);
        op("getChannel", c -> c.getChannel("ops"),
                new Channel("Operations", Level.ACTIVE, Level.CRITICAL, "", "", "ops", List.of()));
        op("deleteChannel", c -> {
            c.deleteChannel("ops");
            return null;
        }, null);
        op("putTemplate", c -> {
            c.putTemplate("status", 2, Map.of("type", "vstack", "children",
                    List.of(Map.of("type", "text", "style", "headline", "text", "{{title}}"))));
            return null;
        }, null);
        op("putSound", c -> c.putSound("ding", "audio/wav", WAV), new SoundResult("ding", "ding.wav", "00", 12));
        op("sendMessage", c -> c.sendMessage(Message.builder("user-42", "ops", "Disk full", "db-1 is at 97%")
                .level(Level.TIME_SENSITIVE).subtitle("db-1").imageUrl("https://example.com/i.png").templateId("status")
                .data(Map.of("disk", 97, "nested", Map.of("snake_case_key", List.of(1, 2))))
                .sound("ding").actions(List.of(Action.acknowledge("ack", "Ack"))).collapseId("disk-db-1").build()),
                new MessageResult("msg_1", Level.TIME_SENSITIVE, 2, "queued",
                        List.of(new Delivery("dev_1", "queued", null), new Delivery("dev_2", "queued", null))));
        op("sendPage", c -> c.sendMessage(Message.builder("user-42", "ops", "Primary DB down", "Failover did not complete")
                .level(Level.PAGE).page(PageOptions.of(60, 3)).build()),
                new MessageResult("msg_2", Level.PAGE, 1, "queued", List.of(new Delivery("dev_1", "queued", null))));
        op("getMessage", c -> c.getMessage("msg_2"), new MessageStatus("msg_2", "acked", Level.PAGE, 2,
                List.of(new Delivery("dev_1", "sent", null), new Delivery("dev_2", "failed", "unregistered")),
                Instant.parse("2026-11-08T12:05:00Z")));
        op("startActivity", c -> c.startActivity(Activity.of("user-42", "ops", "deploy", Map.of("progress", 0.1))
                .withAlert(new ActivityAlert("Deploy started", "v1.2.3"))), new ActivityStarted("act_1", 1));
        op("updateActivity", c -> c.updateActivity("act_1", ActivityUpdate.of(Map.of("progress", 0.6)).withPriority(10)
                .withAlert(new ActivityAlert("Halfway", "60%"))), new ActivityUpdated("act_1", 3));
        op("endActivity", c -> {
            c.endActivity("act_1");
            return null;
        }, null);
        op("putFeed", c -> c.putFeed("uptime", Feed.of("status", Map.of("up", true, "ms", 41)).forUser("user-42").withTitle("Uptime")),
                new FeedResult("uptime", 2));
    }

    @TestFactory
    Stream<DynamicTest> operations() {
        return stream(SPEC.get("operations")).map(op -> DynamicTest.dynamicTest(op.get("name").asText(), () -> {
            Function<Hansho, Object> call = CALLS.get(op.get("name").asText());
            assertNotNull(call, "no SDK call for spec operation " + op.get("name").asText());
            try (TestHub hub = new TestHub()) {
                JsonNode resp = op.get("response");
                hub.always(TestHub.Reply.json(resp.get("status").asInt(), resp.get("body").isNull() ? null : resp.get("body").toString()));
                Object got = call.apply(hub.client());
                assertEquals(WANT.get(op.get("name").asText()), got);
                assertEquals(1, hub.requests.size());

                TestHub.Req r = hub.requests.get(0);
                assertEquals(op.get("method").asText(), r.method());
                assertEquals(op.get("path").asText(), r.uri());
                assertEquals(SPEC.get("auth_scheme").asText() + " " + TestHub.KEY, r.header(SPEC.get("auth_header").asText()));
                JsonNode req = op.get("request");
                if (req.isNull()) {
                    assertEquals(0, r.body().length);
                } else if (req.has("raw_base64")) {
                    assertEquals(req.get("content_type").asText(), r.header("Content-Type"));
                    assertTrue(java.util.Arrays.equals(Base64.getDecoder().decode(req.get("raw_base64").asText()), r.body()));
                } else {
                    assertEquals(req.get("content_type").asText(), r.header("Content-Type"));
                    assertEquals(req.get("body"), MAPPER.readTree(r.body()));
                }
            }
        }));
    }

    @Test
    void coversEverySdkCall() {
        List<String> names = stream(SPEC.get("operations")).map(o -> o.get("name").asText()).toList();
        for (String k : CALLS.keySet()) {
            assertTrue(names.contains(k), "SDK call " + k + " has no operation in sdk/spec/api.json");
        }
        assertEquals(names.size(), CALLS.size(), "every spec operation needs an SDK call");
    }

    @TestFactory
    Stream<DynamicTest> errors() {
        List<JsonNode> errors = stream(SPEC.get("errors")).toList();
        return java.util.stream.IntStream.range(0, errors.size()).mapToObj(i -> {
            JsonNode e = errors.get(i);
            return DynamicTest.dynamicTest("error " + i + " (" + e.get("status").asInt() + ")", () -> {
                try (TestHub hub = new TestHub()) {
                    String body = e.has("raw") ? e.get("raw").asText() : e.get("body").toString();
                    TestHub.Reply reply = TestHub.Reply.json(e.get("status").asInt(), body);
                    var it = e.get("headers").fields();
                    while (it.hasNext()) {
                        var f = it.next();
                        reply = reply.with(f.getKey(), f.getValue().asText());
                    }
                    hub.always(reply);
                    HanshoException ex = assertThrows(HanshoException.class,
                            () -> hub.client().sendMessage(Message.builder("u", "c", "t", "b").build()));
                    assertEquals(e.get("status").asInt(), ex.status());
                    assertEquals(e.get("code").asText(), ex.code());
                    assertEquals(e.get("field").asText(), ex.field());
                    assertEquals(e.get("retry_after_ms").asLong(), ex.retryAfterMs());
                    assertEquals(1, hub.requests.size(), "POST /messages must not be retried");
                }
            });
        });
    }

    static Stream<JsonNode> stream(JsonNode array) {
        return java.util.stream.StreamSupport.stream(array.spliterator(), false);
    }
}

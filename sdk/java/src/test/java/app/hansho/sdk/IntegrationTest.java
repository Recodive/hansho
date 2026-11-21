package app.hansho.sdk;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assumptions.assumeTrue;

import com.sun.net.httpserver.HttpServer;
import java.io.IOException;
import java.net.BindException;
import java.net.InetSocketAddress;
import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.Predicate;
import org.junit.jupiter.api.Test;

/**
 * End to end against the real hub. Skipped unless HANSHO_HUB_URL is set; sdk/scripts/run-integration.sh starts the
 * hub and a stub relay and exports HANSHO_HUB_URL, HANSHO_API_KEY, HANSHO_WEBHOOK_SECRET, HANSHO_WEBHOOK_ADDR and
 * HANSHO_FAKEDEVICE_BIN.
 */
class IntegrationTest {
    static String env(String name) {
        String v = System.getenv(name);
        return v == null ? "" : v;
    }

    static void sleep(long ms) throws InterruptedException {
        Thread.sleep(ms);
    }

    static String fakeDevice(String... args) throws Exception {
        String[] cmd = new String[args.length + 1];
        cmd[0] = env("HANSHO_FAKEDEVICE_BIN");
        System.arraycopy(args, 0, cmd, 1, args.length);
        Process p = new ProcessBuilder(cmd).redirectErrorStream(true).start();
        String out = new String(p.getInputStream().readAllBytes(), StandardCharsets.UTF_8);
        if (!p.waitFor(30, TimeUnit.SECONDS)) {
            p.destroyForcibly();
            throw new AssertionError("fakedevice timed out: " + String.join(" ", args));
        }
        assertEquals(0, p.exitValue(), "fakedevice " + String.join(" ", args) + ": " + out);
        return out;
    }

    static byte[] tinyWav() {
        int n = 800; // 0.05 s of 8 kHz 8-bit mono silence
        ByteBuffer b = ByteBuffer.allocate(44 + n).order(ByteOrder.LITTLE_ENDIAN);
        b.put("RIFF".getBytes(StandardCharsets.US_ASCII)).putInt(36 + n).put("WAVEfmt ".getBytes(StandardCharsets.US_ASCII));
        b.putInt(16).putShort((short) 1).putShort((short) 1).putInt(8000).putInt(8000).putShort((short) 1).putShort((short) 8);
        b.put("data".getBytes(StandardCharsets.US_ASCII)).putInt(n);
        return b.array();
    }

    /** Receives the hub's webhooks and verifies each with this SDK's own verifier. */
    static final class Receiver implements AutoCloseable {
        final List<WebhookEvent> events = new CopyOnWriteArrayList<>();
        final AtomicInteger rejected = new AtomicInteger();
        final HttpServer server;

        Receiver(String addr, String secret) throws Exception {
            int colon = addr.lastIndexOf(':');
            String host = addr.substring(0, colon);
            int port = Integer.parseInt(addr.substring(colon + 1));
            HttpServer s = null;
            for (int i = 0; ; i++) {
                try {
                    s = HttpServer.create(new InetSocketAddress(host, port), 0);
                    break;
                } catch (BindException e) {
                    if (i >= 50) {
                        throw e;
                    }
                    sleep(100); // a previous receiver may still be closing
                }
            }
            server = s;
            server.createContext("/", ex -> {
                byte[] body = ex.getRequestBody().readAllBytes();
                int status = 200;
                try {
                    events.add(Webhook.verify(secret, ex.getRequestHeaders().getFirst(Webhook.SIGNATURE_HEADER), body));
                } catch (WebhookException e) {
                    rejected.incrementAndGet();
                    status = 400;
                } catch (RuntimeException e) {
                    rejected.incrementAndGet();
                    status = 500;
                }
                ex.sendResponseHeaders(status, -1);
                ex.close();
            });
            server.start();
        }

        <T extends WebhookEvent> T await(Class<T> type, Predicate<T> match) throws InterruptedException {
            long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(25);
            while (System.nanoTime() < deadline) {
                for (WebhookEvent e : events) {
                    if (type.isInstance(e) && match.test(type.cast(e))) {
                        return type.cast(e);
                    }
                }
                sleep(100);
            }
            throw new AssertionError("no " + type.getSimpleName() + " webhook arrived");
        }

        @Override
        public void close() {
            server.stop(0);
        }
    }

    static MessageStatus waitStatus(Hansho c, String id, String... want) throws InterruptedException {
        long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(25);
        String last = "";
        while (System.nanoTime() < deadline) {
            MessageStatus ms = c.getMessage(id);
            last = ms.status();
            if (List.of(want).contains(ms.status())) {
                return ms;
            }
            sleep(200);
        }
        throw new AssertionError("message " + id + ": status " + last + ", wanted one of " + String.join(", ", want));
    }

    static HanshoException apiError(int status, String code, String field, Runnable call) {
        HanshoException e = assertThrows(HanshoException.class, call::run);
        assertEquals(status, e.status(), e.getMessage());
        assertEquals(code, e.code(), e.getMessage());
        if (field != null) {
            assertEquals(field, e.field(), e.getMessage());
        }
        return e;
    }

    @Test
    void runsTheWholeFlow() throws Exception {
        assumeTrue(!env("HANSHO_HUB_URL").isEmpty(), "HANSHO_HUB_URL is not set; run sdk/scripts/run-integration.sh");
        String hubUrl = env("HANSHO_HUB_URL");
        try (Receiver rx = new Receiver(env("HANSHO_WEBHOOK_ADDR"), env("HANSHO_WEBHOOK_SECRET"))) {
            Hansho c = Hansho.create(hubUrl, env("HANSHO_API_KEY"));
            String p = "java";
            String user = "u-" + p + " ü/42?x";
            String ch = p + "-ops";
            String tpl = p + "-status";
            String snd = p + "-ding";
            String state = Files.createTempDirectory("hansho-java-it-").resolve("device.json").toString();

            // errors
            apiError(401, "unauthorized", null, () -> Hansho.create(hubUrl, "ck_not_a_key").createPairingTicket("x"));
            apiError(404, "unknown_channel", null, () -> c.getChannel("no-such-channel"));
            apiError(400, "invalid_request", "title", () -> c.sendMessage(Message.builder(user, ch, "", "b").build()));

            // catalog
            SoundResult sound = c.putSound(snd, "audio/wav", tinyWav());
            assertEquals(snd, sound.id());
            Map<String, Object> template = Map.of("type", "vstack", "tint", Map.of("light", "#0A84FF", "dark", "#64D2FF"),
                    "children", List.of(Map.of("type", "text", "style", "headline", "text", "{{title}}"), Map.of("type", "spacer")));
            c.putTemplate(tpl, 1, template);
            apiError(409, "version_conflict", null, () -> c.putTemplate(tpl, 1, template));
            c.putTemplate(tpl, 2, template);
            c.putChannel(ch, Channel.builder("Operations " + p, Level.ACTIVE, Level.PAGE).sound(snd).templateId(tpl).thread("ops")
                    .actions(List.of(Action.acknowledge("ack", "Acknowledge"))).build());
            Channel got = c.getChannel(ch);
            assertEquals("Operations " + p, got.name());
            assertEquals(Level.PAGE, got.maxLevel());
            assertEquals(snd, got.sound());
            assertEquals(tpl, got.templateId());
            assertEquals(List.of(Action.acknowledge("ack", "Acknowledge")), got.actions());
            apiError(400, "invalid_request", "id", () -> c.putChannel("bad id", Channel.builder("n", Level.ACTIVE, Level.PAGE).build()));

            // pairing
            PairingTicket tk = c.createPairingTicket(user);
            assertTrue(tk.qr().startsWith("hansho://pair?") && tk.qr().contains(tk.ticket()));
            assertTrue(tk.expiresAt().isAfter(Instant.now()));
            fakeDevice("pair", "-hub", hubUrl, "-ticket", tk.ticket(), "-state", state, "-name", "SDK phone " + p);
            rx.await(WebhookEvent.PairingCreated.class, e -> e.externalUserId().equals(user) && e.deviceName().equals("SDK phone " + p));

            // messages at every level
            for (Level level : new Level[] {Level.PASSIVE, Level.ACTIVE, Level.TIME_SENSITIVE, Level.CRITICAL}) {
                MessageResult res = c.sendMessage(Message.builder(user, ch, "Disk full", "db-1 is at 97%").level(level)
                        .data(Map.of("disk", 97)).sound(snd).collapseId("disk-" + level.wire()).build());
                assertEquals(level, res.level());
                assertEquals(1, res.devices());
                assertTrue(res.messageId().startsWith("msg_"));
                MessageStatus ms = waitStatus(c, res.messageId(), "delivered");
                assertEquals(level, ms.level());
                assertEquals(1, ms.deliveries().size());
                assertEquals("sent", ms.deliveries().get(0).status());
                rx.await(WebhookEvent.MessageDelivered.class,
                        e -> e.messageId().equals(res.messageId()) && e.level() == level && e.devices() == 1);
            }
            MessageResult def = c.sendMessage(Message.builder(user, ch, "Default", "b").build());
            assertEquals(Level.ACTIVE, def.level());

            // page + acknowledgement from the (fake) device
            MessageResult page = c.sendMessage(Message.builder(user, ch, "Primary DB down", "Failover failed")
                    .level(Level.PAGE).page(PageOptions.of(60, 3)).build());
            assertEquals(Level.PAGE, page.level());
            waitStatus(c, page.messageId(), "paging");
            fakeDevice("ack", "-state", state, "-message", page.messageId());
            MessageStatus acked = waitStatus(c, page.messageId(), "acked");
            assertNotNull(acked.ackedAt());
            WebhookEvent.MessageAcked ev = rx.await(WebhookEvent.MessageAcked.class, e -> e.messageId().equals(page.messageId()));
            assertEquals(user, ev.externalUserId());
            rx.await(WebhookEvent.ActionTaken.class, e -> e.messageId().equals(page.messageId()) && e.actionId().equals("ack"));

            // live activity
            ActivityStarted started = c.startActivity(Activity.of(user, ch, tpl, Map.of("title", "Deploy", "progress", 0.1))
                    .withAlert(new ActivityAlert("Deploy started", null)));
            assertEquals(1, started.devices());
            assertTrue(started.activityId().startsWith("act_"));
            fakeDevice("activity-token", "-state", state, "-activity", started.activityId());
            ActivityUpdated up = c.updateActivity(started.activityId(),
                    ActivityUpdate.of(Map.of("title", "Deploy", "progress", 0.6)).withPriority(10));
            assertEquals(started.activityId(), up.activityId());
            assertTrue(up.update() >= 1);
            c.endActivity(started.activityId());
            apiError(409, "activity_ended", null, () -> c.updateActivity(started.activityId(), ActivityUpdate.of(Map.of("title", "x"))));

            // feeds
            FeedResult own = c.putFeed(p + "-uptime", Feed.of(tpl, Map.of("title", "up", "ms", 41)).forUser(user));
            assertEquals(p + "-uptime", own.feedId());
            assertTrue(own.devices() >= 1);
            FeedResult shared = c.putFeed(p + "-shared", Feed.of(tpl, Map.of("title", "all")));
            assertTrue(shared.devices() >= 1);
            apiError(400, "invalid_request", "template_id", () -> c.putFeed(p + "-bad", Feed.of("missing", 1)));

            // unpair, then nobody is left to deliver to
            c.unpair(user);
            rx.await(WebhookEvent.PairingRevoked.class, e -> e.externalUserId().equals(user) && e.by().equals("sender"));
            MessageResult none = c.sendMessage(Message.builder(user, ch, "Anyone?", "b").build());
            assertEquals(0, none.devices());
            waitStatus(c, none.messageId(), "undeliverable");
            rx.await(WebhookEvent.MessageUndeliverable.class, e -> e.messageId().equals(none.messageId()));
            c.deleteChannel(ch);
            apiError(404, "unknown_channel", null, () -> c.getChannel(ch));

            assertEquals(0, rx.rejected.get(), "the SDK rejected a webhook the hub signed");
            assertTrue(!rx.events.isEmpty());
        }
    }
}

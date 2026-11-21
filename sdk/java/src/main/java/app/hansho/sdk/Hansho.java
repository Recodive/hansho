package app.hansho.sdk;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.time.format.DateTimeParseException;
import java.util.ArrayList;
import java.util.List;
import java.util.Objects;

/**
 * Client for a Hansho hub's sender API. Instances are thread-safe and meant to be shared.
 *
 * <pre>{@code
 * Hansho hansho = Hansho.builder("https://hub.example.com", System.getenv("HANSHO_API_KEY")).build();
 * hansho.sendMessage(Message.builder("user-42", "ops", "Disk full", "db-1 is at 97%").build());
 * }</pre>
 *
 * <p>Failures: a non-2xx answer throws {@link HanshoException}; no answer at all throws
 * {@link HanshoNetworkException}; an invalid argument throws {@link IllegalArgumentException} before anything is sent.
 */
public final class Hansho {
    /** Product codename. */
    public static final String NAME = "hansho";
    /** SDK version, sent in the default User-Agent. */
    public static final String VERSION = "0.1.0";

    private static final int MAX_SOUND_BYTES = 1 << 20;

    /** Replaces the retry wait; meant for tests. */
    @FunctionalInterface
    public interface Sleeper {
        void sleep(long millis) throws InterruptedException;
    }

    private record Call(
            String method, String path, byte[] body, String contentType, boolean retry, String idempotencyKey) {}

    private record Response(int status, HttpResponse<byte[]> raw) {}

    private final String baseUrl;
    private final String apiKey;
    private final HttpClient http;
    private final int maxRetries;
    private final Duration timeout;
    private final String userAgent;
    private final Sleeper sleeper;

    private Hansho(Builder b) {
        this.baseUrl = stripTrailingSlashes(b.baseUrl);
        this.apiKey = b.apiKey;
        this.maxRetries = Math.max(0, b.maxRetries);
        this.timeout = b.timeout;
        this.userAgent = b.userAgent;
        this.sleeper = b.sleeper;
        HttpClient.Builder hb = HttpClient.newBuilder().version(HttpClient.Version.HTTP_1_1);
        if (b.timeout != null && !b.timeout.isZero()) {
            hb.connectTimeout(b.timeout);
        }
        this.http = b.httpClient != null ? b.httpClient : hb.build();
    }

    /** @param baseUrl the hub's public URL; @param apiKey a sender API key from {@code hub apikey create} */
    public static Builder builder(String baseUrl, String apiKey) {
        return new Builder(baseUrl, apiKey);
    }

    /** Shorthand for {@code builder(baseUrl, apiKey).build()}. */
    public static Hansho create(String baseUrl, String apiKey) {
        return builder(baseUrl, apiKey).build();
    }

    /** Builds a {@link Hansho}. */
    public static final class Builder {
        private final String baseUrl;
        private final String apiKey;
        private HttpClient httpClient;
        private int maxRetries = Retry.DEFAULT_MAX_RETRIES;
        private Duration timeout = Duration.ofSeconds(30);
        private String userAgent = NAME + "-java/" + VERSION;
        private Sleeper sleeper = Thread::sleep;

        private Builder(String baseUrl, String apiKey) {
            if (baseUrl == null || baseUrl.isEmpty()) {
                throw new IllegalArgumentException("hansho: baseUrl is required");
            }
            if (apiKey == null || apiKey.isEmpty()) {
                throw new IllegalArgumentException("hansho: apiKey is required");
            }
            this.baseUrl = baseUrl;
            this.apiKey = apiKey;
        }

        /** Custom HTTP client (proxy, TLS, executor). The default is HTTP/1.1 with the connect timeout. */
        public Builder httpClient(HttpClient httpClient) {
            this.httpClient = httpClient;
            return this;
        }

        /** Retries for GET/PUT/DELETE on 429 and 503. Default 3; 0 disables. */
        public Builder maxRetries(int maxRetries) {
            this.maxRetries = maxRetries;
            return this;
        }

        /** Per-attempt timeout. Default 30 s; {@code null} or zero disables. */
        public Builder timeout(Duration timeout) {
            this.timeout = timeout;
            return this;
        }

        public Builder userAgent(String userAgent) {
            this.userAgent = Objects.requireNonNull(userAgent, "userAgent");
            return this;
        }

        /** Replaces the retry wait; meant for tests. */
        public Builder sleeper(Sleeper sleeper) {
            this.sleeper = Objects.requireNonNull(sleeper, "sleeper");
            return this;
        }

        public Hansho build() {
            return new Hansho(this);
        }
    }

    /** Never prints the API key. */
    @Override
    public String toString() {
        return "Hansho[baseUrl=" + baseUrl + "]";
    }

    // --- pairing -------------------------------------------------------------

    /**
     * Creates a single-use pairing ticket (valid 10 minutes). Render {@link PairingTicket#qr()} as a QR code
     * for the user to scan in the Hansho app. The hub then sends a {@code pairing.created} webhook.
     */
    public PairingTicket createPairingTicket(String externalUserId) {
        requireId(externalUserId);
        ObjectNode body = Json.MAPPER.createObjectNode().put("external_user_id", externalUserId);
        JsonNode w = json("POST", "/api/v1/pairing-tickets", body);
        return new PairingTicket(Json.str(w.get("ticket")), Json.str(w.get("qr")), instant(w.get("expires_at")));
    }

    /** Revokes every pairing of the user. */
    public void unpair(String externalUserId) {
        json("DELETE", "/api/v1/users/" + seg(externalUserId) + "/pairings", null);
    }

    // --- catalog -------------------------------------------------------------

    public void putChannel(String id, Channel channel) {
        Objects.requireNonNull(channel, "channel");
        ObjectNode w = Json.MAPPER.createObjectNode();
        Json.put(w, "name", channel.name());
        Json.put(w, "default_level", wire(channel.defaultLevel()));
        Json.put(w, "max_level", wire(channel.maxLevel()));
        Json.put(w, "sound", channel.sound());
        Json.put(w, "template_id", channel.templateId());
        Json.put(w, "thread", channel.thread());
        putActions(w, channel.actions());
        json("PUT", "/api/v1/channels/" + seg(id), w);
    }

    public Channel getChannel(String id) {
        JsonNode w = json("GET", "/api/v1/channels/" + seg(id), null);
        return new Channel(
                Json.optStr(w.get("name")),
                Level.fromWire(Json.optStr(w.get("default_level"))),
                Level.fromWire(Json.optStr(w.get("max_level"))),
                Json.optStr(w.get("sound")),
                Json.optStr(w.get("template_id")),
                Json.optStr(w.get("thread")),
                actionsFromWire(w.get("actions")));
    }

    public void deleteChannel(String id) {
        json("DELETE", "/api/v1/channels/" + seg(id), null);
    }

    /**
     * Stores a template version; the version must increase.
     *
     * @param body the template tree (a {@code Map}, a {@code JsonNode} or any Jackson-serializable object)
     */
    public void putTemplate(String id, int version, Object body) {
        ObjectNode w = Json.MAPPER.createObjectNode().put("version", version);
        w.set("body", tree(body));
        json("PUT", "/api/v1/templates/" + seg(id), w);
    }

    /** Uploads a sound ({@code audio/x-caf}, {@code audio/wav} or {@code audio/aiff}, at most 1 MB). */
    public SoundResult putSound(String id, String contentType, byte[] bytes) {
        String path = "/api/v1/sounds/" + seg(id);
        Objects.requireNonNull(contentType, "contentType");
        Objects.requireNonNull(bytes, "bytes");
        if (bytes.length > MAX_SOUND_BYTES) {
            throw new IllegalArgumentException("hansho: sound exceeds 1 MB");
        }
        JsonNode w = send(new Call("PUT", path, bytes, contentType, true, null));
        return new SoundResult(
                Json.str(w.get("id")), Json.str(w.get("filename")), Json.str(w.get("sha256")), Json.num(w.get("size")));
    }

    // --- messages ------------------------------------------------------------

    /**
     * Sends a notification, or a page when the level is {@link Level#PAGE}. It is never retried automatically
     * (a repeat would notify twice) unless the message has an {@link Message#idempotencyKey()}.
     */
    public MessageResult sendMessage(Message m) {
        Objects.requireNonNull(m, "message");
        ObjectNode w = Json.MAPPER.createObjectNode();
        Json.put(w, "external_user_id", m.externalUserId());
        Json.put(w, "channel", m.channel());
        Json.put(w, "level", wire(m.level()));
        Json.put(w, "title", m.title());
        Json.put(w, "subtitle", m.subtitle());
        Json.put(w, "body", m.body());
        Json.put(w, "image_url", m.imageUrl());
        Json.put(w, "template_id", m.templateId());
        if (m.data() != null) {
            w.set("data", tree(m.data()));
        }
        Json.put(w, "sound", m.sound());
        putActions(w, m.actions());
        Json.put(w, "collapse_id", m.collapseId());
        if (m.page() != null) {
            ObjectNode p = w.putObject("page");
            if (m.page().intervalSeconds() != null) {
                p.put("interval_s", m.page().intervalSeconds());
            }
            if (m.page().maxRetries() != null) {
                p.put("max_retries", m.page().maxRetries());
            }
        }
        String key = m.idempotencyKey();
        boolean keyed = key != null && !key.isEmpty();
        JsonNode r = send(new Call("POST", "/api/v1/messages", bytes(w), "application/json", keyed, keyed ? key : null));
        return new MessageResult(
                Json.str(r.get("message_id")),
                Level.fromWire(Json.optStr(r.get("level"))),
                (int) Json.num(r.get("devices")),
                Json.str(r.get("status")),
                deliveriesFromWire(r.get("deliveries")));
    }

    public MessageStatus getMessage(String id) {
        JsonNode w = json("GET", "/api/v1/messages/" + seg(id), null);
        return new MessageStatus(
                Json.str(w.get("message_id")),
                Json.str(w.get("status")),
                Level.fromWire(Json.optStr(w.get("level"))),
                (int) Json.num(w.get("attempts")),
                deliveriesFromWire(w.get("deliveries")),
                instant(w.get("acked_at")));
    }

    // --- live activities and feeds -------------------------------------------

    public ActivityStarted startActivity(Activity a) {
        Objects.requireNonNull(a, "activity");
        ObjectNode w = Json.MAPPER.createObjectNode();
        Json.put(w, "external_user_id", a.externalUserId());
        Json.put(w, "channel", a.channel());
        Json.put(w, "template_id", a.templateId());
        if (a.data() != null) {
            w.set("data", tree(a.data()));
        }
        putAlert(w, a.alert());
        JsonNode r = json("POST", "/api/v1/activities", w);
        return new ActivityStarted(Json.str(r.get("activity_id")), (int) Json.num(r.get("devices")));
    }

    /** Not retried automatically. */
    public ActivityUpdated updateActivity(String id, ActivityUpdate u) {
        Objects.requireNonNull(u, "update");
        ObjectNode w = Json.MAPPER.createObjectNode();
        if (u.data() != null) {
            w.set("data", tree(u.data()));
        }
        if (u.priority() != null) {
            w.put("priority", u.priority());
        }
        putAlert(w, u.alert());
        JsonNode r = json("PATCH", "/api/v1/activities/" + seg(id), w);
        return new ActivityUpdated(Json.str(r.get("activity_id")), (int) Json.num(r.get("update")));
    }

    public void endActivity(String id) {
        json("DELETE", "/api/v1/activities/" + seg(id), null);
    }

    public FeedResult putFeed(String id, Feed f) {
        Objects.requireNonNull(f, "feed");
        ObjectNode w = Json.MAPPER.createObjectNode();
        Json.put(w, "external_user_id", f.externalUserId());
        Json.put(w, "template_id", f.templateId());
        Json.put(w, "title", f.title());
        if (f.data() != null) {
            w.set("data", tree(f.data()));
        }
        JsonNode r = json("PUT", "/api/v1/feeds/" + seg(id), w);
        return new FeedResult(Json.str(r.get("feed_id")), (int) Json.num(r.get("devices")));
    }

    // --- wire mapping --------------------------------------------------------

    private static String wire(Level l) {
        return l == null ? null : l.wire();
    }

    private static JsonNode tree(Object value) {
        try {
            return Json.MAPPER.valueToTree(value);
        } catch (IllegalArgumentException e) {
            throw new IllegalArgumentException("hansho: data is not JSON-serializable", e);
        }
    }

    private static byte[] bytes(JsonNode node) {
        try {
            return Json.MAPPER.writeValueAsBytes(node);
        } catch (IOException e) {
            throw new IllegalArgumentException("hansho: request is not JSON-serializable", e);
        }
    }

    private static void putActions(ObjectNode w, List<Action> actions) {
        if (actions == null) {
            return;
        }
        ArrayNode arr = w.putArray("actions");
        for (Action a : actions) {
            ObjectNode o = arr.addObject();
            o.put("id", a.id());
            o.put("title", a.title());
            if (a.ack() != null) {
                o.put("ack", a.ack());
            }
            Json.put(o, "kind", a.kind());
        }
    }

    private static void putAlert(ObjectNode w, ActivityAlert alert) {
        if (alert == null) {
            return;
        }
        ObjectNode o = w.putObject("alert");
        Json.put(o, "title", alert.title());
        Json.put(o, "body", alert.body());
    }

    private static List<Action> actionsFromWire(JsonNode raw) {
        List<Action> out = new ArrayList<>();
        if (raw != null && raw.isArray()) {
            for (JsonNode a : raw) {
                out.add(new Action(
                        Json.str(a.get("id")),
                        Json.str(a.get("title")),
                        a.path("ack").isBoolean() ? a.get("ack").asBoolean() : null,
                        Json.optStr(a.get("kind"))));
            }
        }
        return out;
    }

    private static List<Delivery> deliveriesFromWire(JsonNode raw) {
        List<Delivery> out = new ArrayList<>();
        if (raw != null && raw.isArray()) {
            for (JsonNode d : raw) {
                String err = Json.str(d.get("error"));
                out.add(new Delivery(Json.str(d.get("device_id")), Json.str(d.get("status")), err.isEmpty() ? null : err));
            }
        }
        return out;
    }

    private static Instant instant(JsonNode n) {
        String s = Json.optStr(n);
        if (s == null) {
            return null;
        }
        try {
            return Instant.parse(s);
        } catch (DateTimeParseException e) {
            return null;
        }
    }

    // --- paths ---------------------------------------------------------------

    private static void requireId(String id) {
        if (id == null || id.isEmpty()) {
            throw new IllegalArgumentException("hansho: id must not be empty");
        }
    }

    /** Escapes one path segment; rejects ids that would not stay in their collection. */
    static String seg(String id) {
        requireId(id);
        if (id.equals(".") || id.equals("..")) {
            throw new IllegalArgumentException("hansho: id must not be \"" + id + "\"");
        }
        StringBuilder sb = new StringBuilder();
        for (byte b : id.getBytes(StandardCharsets.UTF_8)) {
            int c = b & 0xff;
            if ((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
                    || c == '-' || c == '.' || c == '_' || c == '~') {
                sb.append((char) c);
            } else {
                sb.append('%').append("0123456789ABCDEF".charAt(c >> 4)).append("0123456789ABCDEF".charAt(c & 15));
            }
        }
        return sb.toString();
    }

    private static String stripTrailingSlashes(String s) {
        int end = s.length();
        while (end > 0 && s.charAt(end - 1) == '/') {
            end--;
        }
        return s.substring(0, end);
    }

    // --- plumbing ------------------------------------------------------------

    private JsonNode json(String method, String path, JsonNode body) {
        Call call = new Call(method, path, body == null ? null : bytes(body), body == null ? null : "application/json",
                Retry.isIdempotent(method), null);
        return send(call);
    }

    private JsonNode send(Call call) {
        for (int attempt = 0; ; attempt++) {
            Response res = roundTrip(call);
            if (res.status >= 200 && res.status < 300) {
                byte[] raw = res.raw.body();
                if (raw == null || new String(raw, StandardCharsets.UTF_8).isBlank()) {
                    return Json.MAPPER.createObjectNode();
                }
                try {
                    JsonNode parsed = Json.MAPPER.readTree(raw);
                    return parsed != null && parsed.isObject() ? parsed : Json.MAPPER.createObjectNode();
                } catch (IOException e) {
                    throw new HanshoNetworkException(call.method, call.path, new IOException("invalid JSON in response"));
                }
            }
            HanshoException err = apiError(call, res);
            if (!call.retry || !Retry.isRetryableStatus(res.status) || attempt >= maxRetries) {
                throw err;
            }
            long wait = err.retryAfterMs() > 0 ? err.retryAfterMs() : Retry.backoffMs(attempt);
            if (wait > Retry.MAX_WAIT_MS) {
                throw err;
            }
            try {
                sleeper.sleep(wait);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                throw new HanshoNetworkException(call.method, call.path, e);
            }
        }
    }

    private Response roundTrip(Call call) {
        HttpRequest.Builder rb = HttpRequest.newBuilder(URI.create(baseUrl + call.path))
                .header("Authorization", "Bearer " + apiKey)
                .header("Accept", "application/json")
                .header("User-Agent", userAgent);
        if (call.contentType != null) {
            rb.header("Content-Type", call.contentType);
        }
        if (call.idempotencyKey != null) {
            rb.header("Idempotency-Key", call.idempotencyKey);
        }
        if (timeout != null && !timeout.isZero()) {
            rb.timeout(timeout);
        }
        rb.method(call.method, call.body == null
                ? HttpRequest.BodyPublishers.noBody()
                : HttpRequest.BodyPublishers.ofByteArray(call.body));
        try {
            HttpResponse<byte[]> res = http.send(rb.build(), HttpResponse.BodyHandlers.ofByteArray());
            return new Response(res.statusCode(), res);
        } catch (IOException e) {
            throw new HanshoNetworkException(call.method, call.path, redact(e));
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new HanshoNetworkException(call.method, call.path, e);
        }
    }

    private Throwable redact(Throwable cause) {
        String m = cause.getMessage();
        if (m != null && m.contains(apiKey)) {
            return new IOException(m.replace(apiKey, "[redacted]"));
        }
        return cause;
    }

    private HanshoException apiError(Call call, Response res) {
        String code = null;
        String field = null;
        String detail = null;
        try {
            JsonNode p = Json.MAPPER.readTree(res.raw.body());
            if (p != null && p.isObject()) {
                code = Json.optStr(p.get("error"));
                field = Json.optStr(p.get("field"));
                String msg = Json.optStr(p.get("message"));
                if (msg != null) {
                    detail = msg.replace(apiKey, "[redacted]");
                }
            }
        } catch (IOException e) {
            // not JSON (a proxy error page): status only
        }
        long retryAfter = Retry.parseRetryAfter(res.raw.headers().firstValue("Retry-After").orElse(null), Instant.now());
        return new HanshoException(call.method, call.path, res.status, code, field, retryAfter, detail);
    }
}

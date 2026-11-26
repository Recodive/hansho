package app.hansho.sdk;

import app.hansho.sdk.Models.Activity;
import app.hansho.sdk.Models.ActivityStarted;
import app.hansho.sdk.Models.ActivityUpdate;
import app.hansho.sdk.Models.ActivityUpdated;
import app.hansho.sdk.Models.Channel;
import app.hansho.sdk.Models.Feed;
import app.hansho.sdk.Models.FeedResult;
import app.hansho.sdk.Models.FeedSettings;
import app.hansho.sdk.Models.Group;
import app.hansho.sdk.Models.Message;
import app.hansho.sdk.Models.MessageResult;
import app.hansho.sdk.Models.MessageStatus;
import app.hansho.sdk.Models.PairingTicket;
import app.hansho.sdk.Models.Sound;
import app.hansho.sdk.Models.SoundResult;
import app.hansho.sdk.Models.Template;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.Objects;

/**
 * Client for a Hansho hub's sender API. Instances are thread-safe and meant to be shared.
 *
 * <pre>{@code
 * Hansho hansho = Hansho.builder("https://hub.example.com", System.getenv("HANSHO_API_KEY")).build();
 * hansho.sendMessage(Message.builder("ops", "Disk full", "db-1 is at 97%").externalUserId("user-42").build());
 * }</pre>
 *
 * <p>Failures: a non-2xx answer throws {@link HanshoException}; no answer at all throws
 * {@link HanshoNetworkException}; an invalid argument throws {@link IllegalArgumentException} before anything is sent.
 */
public final class Hansho {
    /** Product codename. */
    public static final String NAME = "hansho";
    /** SDK version, sent in the default User-Agent. */
    public static final String VERSION = Models.VERSION;
    /** The {@code status} of a {@link Models.Recipient} outside the channel's audience: it got no message. */
    public static final String RECIPIENT_NO_ACCESS = "no_access";

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
        return createPairingTicket(externalUserId, null);
    }

    /**
     * Like {@link #createPairingTicket(String)}, and gives the user a display name with the ticket: what other people
     * see instead of their id (any language, trimmed, at most 40 characters, no control characters). {@code null}
     * sends none; {@code ""} is sent as given.
     */
    public PairingTicket createPairingTicket(String externalUserId, String displayName) {
        requireId(externalUserId);
        ObjectNode body = Json.MAPPER.createObjectNode().put("external_user_id", externalUserId);
        Json.put(body, "display_name", displayName);
        return PairingTicket.fromWire(json("POST", "/api/v1/pairing-tickets", body));
    }

    // --- users ---------------------------------------------------------------

    /**
     * Sets what other people see for the user instead of their external user id: any language, trimmed, at most 40
     * characters, no control characters. {@code ""} (or {@code null}) clears it. The user need not be paired yet.
     */
    public void setUserDisplayName(String externalUserId, String displayName) {
        ObjectNode body = Json.MAPPER.createObjectNode().put("display_name", displayName == null ? "" : displayName);
        json("PUT", "/api/v1/users/" + seg(externalUserId), body);
    }

    /** Revokes every pairing of the user. */
    public void unpair(String externalUserId) {
        json("DELETE", "/api/v1/users/" + seg(externalUserId) + "/pairings", null);
    }

    /**
     * Ends the user's whole pairing, on every device: {@link #unpair} under its sender-side name. The app tells the
     * person that the sender ended it (a 401 {@code pairing_revoked} with reason {@code kicked}), where an unpair they
     * did themselves carries no reason. There is no block list; a new ticket pairs them again.
     */
    public void kick(String externalUserId) {
        unpair(externalUserId);
    }

    // --- catalog -------------------------------------------------------------

    /**
     * Creates or replaces a channel. A {@code null} {@link Channel#audience()} keeps the audience stored on the hub (a
     * new channel starts open to everyone); send {@link Models.Audience#everyone()} to open an existing channel up. The groups
     * an audience names must exist, or the hub answers 400 (field {@code audience.groups}). {@link Channel#id()} is the
     * hub's to set and is not sent.
     */
    public void putChannel(String id, Channel channel) {
        Objects.requireNonNull(channel, "channel");
        json("PUT", "/api/v1/channels/" + seg(id), channel.toWire());
    }

    public Channel getChannel(String id) {
        return Channel.fromWire(json("GET", "/api/v1/channels/" + seg(id), null));
    }

    /** Every channel. */
    public List<Channel> listChannels() {
        return Json.list(json("GET", "/api/v1/channels", null), Channel::fromWire);
    }

    public void deleteChannel(String id) {
        json("DELETE", "/api/v1/channels/" + seg(id), null);
    }

    // --- groups --------------------------------------------------------------

    /**
     * Creates a group or replaces its whole member list (external user ids, who may not have paired yet). Audiences
     * that name the group follow at once. Ids match {@code [A-Za-z0-9][A-Za-z0-9_-]{0,63}}.
     */
    public Group putGroup(String id, List<String> members) {
        Objects.requireNonNull(members, "members");
        ObjectNode w = Json.MAPPER.createObjectNode();
        putStrings(w, "members", members);
        return Group.fromWire(json("PUT", "/api/v1/groups/" + seg(id), w));
    }

    /** A missing group is a {@link HanshoException} with code {@link HanshoException#UNKNOWN_GROUP}. */
    public Group getGroup(String id) {
        return Group.fromWire(json("GET", "/api/v1/groups/" + seg(id), null));
    }

    /** Every group. */
    public List<Group> listGroups() {
        return Json.list(json("GET", "/api/v1/groups", null), Group::fromWire);
    }

    /** An audience that still names the group matches nobody through it. */
    public void deleteGroup(String id) {
        json("DELETE", "/api/v1/groups/" + seg(id), null);
    }

    // --- templates and sounds ------------------------------------------------

    /**
     * Stores a template version; the version must increase.
     *
     * @param body the template tree (a {@code Map}, a {@code JsonNode} or any Jackson-serializable object)
     */
    public void putTemplate(String id, int version, Object body) {
        ObjectNode w = Json.MAPPER.createObjectNode().put("version", version);
        w.set("body", Json.tree(body));
        json("PUT", "/api/v1/templates/" + seg(id), w);
    }

    /** Every template at its latest version. */
    public List<Template> listTemplates() {
        return Json.list(json("GET", "/api/v1/templates", null), Template::fromWire);
    }

    /** A missing template is a {@link HanshoException} with code {@code unknown_template}. */
    public Template getTemplate(String id) {
        return Template.fromWire(json("GET", "/api/v1/templates/" + seg(id), null));
    }

    /** A template a live channel still names is refused with a {@link HanshoException} (code {@code in_use}). */
    public void deleteTemplate(String id) {
        json("DELETE", "/api/v1/templates/" + seg(id), null);
    }

    /** Uploads a sound ({@code audio/x-caf}, {@code audio/wav} or {@code audio/aiff}, at most 1 MB). */
    public SoundResult putSound(String id, String contentType, byte[] bytes) {
        String path = "/api/v1/sounds/" + seg(id);
        Objects.requireNonNull(contentType, "contentType");
        Objects.requireNonNull(bytes, "bytes");
        if (bytes.length > MAX_SOUND_BYTES) {
            throw new IllegalArgumentException("hansho: sound exceeds 1 MB");
        }
        return SoundResult.fromWire(send(new Call("PUT", path, bytes, contentType, true, null)));
    }

    /** Every sound's metadata. */
    public List<Sound> listSounds() {
        return Json.list(json("GET", "/api/v1/sounds", null), Sound::fromWire);
    }

    /** A missing sound is a {@link HanshoException} with code {@code unknown_sound}. */
    public Sound getSound(String id) {
        return Sound.fromWire(json("GET", "/api/v1/sounds/" + seg(id), null));
    }

    /** A sound a live channel still names is refused with a {@link HanshoException} (code {@code in_use}). */
    public void deleteSound(String id) {
        json("DELETE", "/api/v1/sounds/" + seg(id), null);
    }

    // --- messages ------------------------------------------------------------

    /**
     * Sends a notification, or a page when the level is {@link Models.Level#PAGE}. It is never retried automatically
     * (a repeat would notify twice) unless the message has an {@link Message#idempotencyKey()}.
     */
    public MessageResult sendMessage(Message m) {
        Objects.requireNonNull(m, "message");
        String key = m.idempotencyKey();
        boolean keyed = key != null && !key.isEmpty();
        return MessageResult.fromWire(
                send(new Call("POST", "/api/v1/messages", bytes(m.toWire()), "application/json", keyed, keyed ? key : null)));
    }

    public MessageStatus getMessage(String id) {
        return MessageStatus.fromWire(json("GET", "/api/v1/messages/" + seg(id), null));
    }

    // --- live activities and feeds -------------------------------------------

    public ActivityStarted startActivity(Activity a) {
        Objects.requireNonNull(a, "activity");
        return ActivityStarted.fromWire(json("POST", "/api/v1/activities", a.toWire()));
    }

    /** Not retried automatically. */
    public ActivityUpdated updateActivity(String id, ActivityUpdate u) {
        Objects.requireNonNull(u, "update");
        return ActivityUpdated.fromWire(json("PATCH", "/api/v1/activities/" + seg(id), u.toWire()));
    }

    public void endActivity(String id) {
        json("DELETE", "/api/v1/activities/" + seg(id), null);
    }

    public FeedResult putFeed(String id, Feed f) {
        Objects.requireNonNull(f, "feed");
        return FeedResult.fromWire(json("PUT", "/api/v1/feeds/" + seg(id), f.toWire()));
    }

    /** A feed's settings without its data; for the feed shared by all users. */
    public FeedSettings getFeed(String id) {
        return getFeed(id, null);
    }

    /** A feed's settings without its data. Pass {@code null} as {@code externalUserId} for the feed shared by all users. */
    public FeedSettings getFeed(String id, String externalUserId) {
        String query = externalUserId == null || externalUserId.isEmpty() ? "" : "?external_user_id=" + escape(externalUserId);
        return FeedSettings.fromWire(json("GET", "/api/v1/feeds/" + seg(id) + query, null));
    }

    // --- wire mapping --------------------------------------------------------

    private static byte[] bytes(JsonNode node) {
        try {
            return Json.MAPPER.writeValueAsBytes(node);
        } catch (IOException e) {
            throw new IllegalArgumentException("hansho: request is not JSON-serializable", e);
        }
    }

    private static void putStrings(ObjectNode w, String key, List<String> values) {
        var arr = w.putArray(key);
        for (String v : values) {
            arr.add(v);
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
        return escape(id);
    }

    /** Percent-escapes every byte but the unreserved ones (a path segment or a query value). */
    private static String escape(String s) {
        StringBuilder sb = new StringBuilder();
        for (byte b : s.getBytes(StandardCharsets.UTF_8)) {
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
                    return parsed != null && (parsed.isObject() || parsed.isArray()) ? parsed : Json.MAPPER.createObjectNode();
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

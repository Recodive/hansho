package app.hansho.sdk;

import com.fasterxml.jackson.databind.JsonNode;
import java.io.IOException;
import java.nio.ByteBuffer;
import java.nio.charset.CharacterCodingException;
import java.nio.charset.CodingErrorAction;
import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.security.MessageDigest;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.function.Function;
import java.util.regex.Pattern;
import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;

/**
 * Verifies and parses the hub's signed webhooks.
 *
 * <p>The signature header is {@code X-Hansho-Webhook-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>}.
 * Verify the <b>raw</b> request body: a body a framework parsed and you re-serialised will not match its signature.
 * With several {@code v1} values in the header (secret rotation) any match is accepted; the comparison runs in
 * constant time and timestamps more than 300 s from now are rejected as {@link WebhookException.Code#STALE}.
 */
public final class Webhook {
    /** The request header that carries the signature. */
    public static final String SIGNATURE_HEADER = "X-Hansho-Webhook-Signature";

    /** Default allowed distance between the signed timestamp and now: 300 s. */
    public static final Duration DEFAULT_TOLERANCE = Duration.ofSeconds(300);

    private static final Pattern HEX64 = Pattern.compile("[0-9a-fA-F]{64}");
    private static final Pattern INTEGER = Pattern.compile("-?\\d+");

    private Webhook() {}

    public static WebhookEvent verify(String secret, String header, byte[] body) {
        return verify(secret, header, body, null, null);
    }

    public static WebhookEvent verify(String secret, String header, String body) {
        return verify(secret, header, body, null, null);
    }

    public static WebhookEvent verify(byte[] secret, String header, byte[] body) {
        return verify(secret, header, body, null, null);
    }

    public static WebhookEvent verify(String secret, String header, String body, Instant now, Duration tolerance) {
        return verify(secret, header, body == null ? null : body.getBytes(StandardCharsets.UTF_8), now, tolerance);
    }

    public static WebhookEvent verify(String secret, String header, byte[] body, Instant now, Duration tolerance) {
        return verify(secret == null ? null : secret.getBytes(StandardCharsets.UTF_8), header, body, now, tolerance);
    }

    /**
     * Verifies a webhook against its raw body and returns the event.
     *
     * @param secret    the shared secret ({@code HUB_WEBHOOK_SECRET})
     * @param header    the {@code X-Hansho-Webhook-Signature} request header (may be {@code null} when absent)
     * @param body      the request body exactly as received
     * @param now       the current time; {@code null} for the clock
     * @param tolerance allowed distance between the signed timestamp and now; {@code null} or not positive means 300 s
     * @throws WebhookException see {@link WebhookException.Code}
     */
    public static WebhookEvent verify(byte[] secret, String header, byte[] body, Instant now, Duration tolerance) {
        if (secret == null || secret.length == 0) {
            throw new WebhookException(WebhookException.Code.INVALID_SECRET, "webhook secret is empty");
        }
        if (body == null) {
            throw new WebhookException(
                    WebhookException.Code.INVALID_BODY, "body must be the raw request body (bytes or string), not a parsed object");
        }
        Parsed h = parseHeader(header);
        byte[] want = hmac(secret, h.t, body);
        boolean match = false;
        for (String sig : h.sigs) {
            if (!HEX64.matcher(sig).matches()) {
                continue;
            }
            if (MessageDigest.isEqual(hex(sig), want)) {
                match = true;
            }
        }
        if (!match) {
            throw new WebhookException(WebhookException.Code.BAD_SIGNATURE, "signature does not match");
        }

        long nowMs = (now == null ? Instant.now() : now).toEpochMilli();
        long tolMs = tolerance != null && !tolerance.isZero() && !tolerance.isNegative()
                ? tolerance.toMillis() : DEFAULT_TOLERANCE.toMillis();
        long signedMs;
        try {
            signedMs = Math.multiplyExact(Long.parseLong(h.t), 1000L);
        } catch (ArithmeticException | NumberFormatException e) {
            throw new WebhookException(WebhookException.Code.STALE, "timestamp outside tolerance");
        }
        if (Math.abs(nowMs - signedMs) > tolMs) {
            throw new WebhookException(WebhookException.Code.STALE, "timestamp outside tolerance");
        }

        JsonNode env;
        try {
            String text = StandardCharsets.UTF_8.newDecoder()
                    .onMalformedInput(CodingErrorAction.REPORT)
                    .onUnmappableCharacter(CodingErrorAction.REPORT)
                    .decode(ByteBuffer.wrap(body))
                    .toString();
            env = Json.MAPPER.readTree(text);
        } catch (CharacterCodingException e) {
            throw new WebhookException(WebhookException.Code.MALFORMED_BODY, "body is not a webhook envelope");
        } catch (IOException e) {
            throw new WebhookException(WebhookException.Code.MALFORMED_BODY, "body is not a webhook envelope");
        }
        if (env == null || !env.isObject()) {
            throw new WebhookException(WebhookException.Code.MALFORMED_BODY, "body is not a webhook envelope");
        }
        JsonNode nameNode = env.get("event");
        if (nameNode == null || !nameNode.isTextual() || nameNode.asText().isEmpty()) {
            throw new WebhookException(WebhookException.Code.MALFORMED_BODY, "body has no event");
        }
        String name = nameNode.asText();
        String id = Json.str(env.get("id"));
        Instant createdAt = Instant.ofEpochSecond(Json.num(env.get("created_at")));
        JsonNode dn = env.get("data");
        JsonNode d = dn != null && dn.isObject() ? dn : Json.MAPPER.createObjectNode();

        Function<JsonNode, Object> decode = Models.EVENTS.get(name);
        if (decode == null) {
            return new WebhookEvent.Unknown(
                    id, createdAt, body, name, dn == null ? null : Json.MAPPER.convertValue(dn, Object.class));
        }
        Object data = decode.apply(d);
        return switch (name) {
            case "pairing.created" -> new WebhookEvent.PairingCreated(id, createdAt, body, (Models.PairingCreated) data);
            case "pairing.revoked" -> new WebhookEvent.PairingRevoked(id, createdAt, body, (Models.PairingRevoked) data);
            case "message.delivered" -> new WebhookEvent.MessageDelivered(id, createdAt, body, (Models.MessageDelivered) data);
            case "message.undeliverable" -> new WebhookEvent.MessageUndeliverable(id, createdAt, body, (Models.MessageUndeliverable) data);
            case "message.acked" -> new WebhookEvent.MessageAcked(id, createdAt, body, (Models.MessageAcked) data);
            case "message.dismissed" -> new WebhookEvent.MessageDismissed(id, createdAt, body, (Models.MessageDismissed) data);
            case "message.expired" -> new WebhookEvent.MessageExpired(id, createdAt, body, (Models.MessageExpired) data);
            case "action" -> new WebhookEvent.ActionTaken(id, createdAt, body, (Models.ActionTaken) data);
            case "activity.ended" -> new WebhookEvent.ActivityEnded(id, createdAt, body, (Models.ActivityEnded) data);
            // an event the hub table has and this switch does not: WebhookTest wants none
            default -> new WebhookEvent.Unknown(id, createdAt, body, name, Json.MAPPER.convertValue(d, Object.class));
        };
    }

    private record Parsed(String t, List<String> sigs) {}

    private static Parsed parseHeader(String header) {
        if (header == null || header.isBlank()) {
            throw new WebhookException(WebhookException.Code.MALFORMED_HEADER, "missing signature header");
        }
        String t = null;
        List<String> sigs = new ArrayList<>();
        for (String part : header.split(",", -1)) {
            String trimmed = part.trim();
            int eq = trimmed.indexOf('=');
            if (eq < 0) {
                throw new WebhookException(WebhookException.Code.MALFORMED_HEADER, "signature header part is not key=value");
            }
            String k = trimmed.substring(0, eq);
            String v = trimmed.substring(eq + 1);
            if (k.equals("t")) {
                if (t != null) {
                    throw new WebhookException(WebhookException.Code.MALFORMED_HEADER, "duplicate timestamp");
                }
                t = v;
            } else if (k.equals("v1") && !v.isEmpty()) {
                sigs.add(v);
            }
        }
        if (t == null || !INTEGER.matcher(t).matches() || sigs.isEmpty()) {
            throw new WebhookException(
                    WebhookException.Code.MALFORMED_HEADER, "signature header needs t=<unix> and v1=<hex>");
        }
        return new Parsed(t, sigs);
    }

    private static byte[] hmac(byte[] secret, String t, byte[] body) {
        try {
            Mac mac = Mac.getInstance("HmacSHA256");
            mac.init(new SecretKeySpec(secret, "HmacSHA256"));
            mac.update((t + ".").getBytes(StandardCharsets.UTF_8));
            return mac.doFinal(body);
        } catch (GeneralSecurityException e) {
            throw new IllegalStateException("HmacSHA256 is not available", e);
        }
    }

    private static byte[] hex(String s) {
        byte[] out = new byte[s.length() / 2];
        for (int i = 0; i < out.length; i++) {
            out[i] = (byte) Integer.parseInt(s.substring(2 * i, 2 * i + 2), 16);
        }
        return out;
    }
}

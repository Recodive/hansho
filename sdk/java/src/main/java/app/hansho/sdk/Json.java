package app.hansho.sdk;

import com.fasterxml.jackson.databind.DeserializationFeature;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;

/** Small JSON helpers shared by the client and the webhook verifier. */
final class Json {
    static final ObjectMapper MAPPER = new ObjectMapper().enable(DeserializationFeature.FAIL_ON_TRAILING_TOKENS);

    private Json() {}

    /** A value as text; empty for absent, null and containers (lenient, like the other SDKs). */
    static String str(JsonNode n) {
        if (n == null || n.isNull() || n.isMissingNode() || !n.isValueNode()) {
            return "";
        }
        return n.asText();
    }

    /** The text of a string node, otherwise {@code null}. */
    static String optStr(JsonNode n) {
        return n != null && n.isTextual() ? n.asText() : null;
    }

    static long num(JsonNode n) {
        if (n == null) {
            return 0;
        }
        if (n.isNumber()) {
            return n.asLong();
        }
        if (n.isTextual()) {
            try {
                return (long) Double.parseDouble(n.asText().trim());
            } catch (NumberFormatException e) {
                return 0;
            }
        }
        return 0;
    }

    static void put(ObjectNode o, String key, String value) {
        if (value != null) {
            o.put(key, value);
        }
    }
}

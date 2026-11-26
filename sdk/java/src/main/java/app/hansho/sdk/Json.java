package app.hansho.sdk;

import com.fasterxml.jackson.databind.DeserializationFeature;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import java.time.Instant;
import java.time.format.DateTimeParseException;
import java.util.ArrayList;
import java.util.List;
import java.util.function.Function;

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

    /** A number as an {@code Integer}; {@code null} for absent, null, anything but a number, or a value not in {@code allowed} when given. */
    static Integer optInt(JsonNode n, int... allowed) {
        if (n == null || !n.isNumber()) {
            return null;
        }
        int v = n.asInt();
        if (allowed.length == 0) {
            return v;
        }
        for (int a : allowed) {
            if (a == v) {
                return v;
            }
        }
        return null;
    }

    static Long optLong(JsonNode n) {
        return n != null && n.isNumber() ? Long.valueOf(n.asLong()) : null;
    }

    static boolean bool(JsonNode n) {
        return n != null && n.isBoolean() && n.asBoolean();
    }

    static Boolean optBool(JsonNode n) {
        return n != null && n.isBoolean() ? Boolean.valueOf(n.asBoolean()) : null;
    }

    /** An ISO-8601 instant; {@code null} for absent or unparseable. */
    static Instant instant(JsonNode n) {
        String s = optStr(n);
        if (s == null) {
            return null;
        }
        try {
            return Instant.parse(s);
        } catch (DateTimeParseException e) {
            return null;
        }
    }

    /** Any JSON value as plain Java (Map, List, String, Number, Boolean); {@code null} for absent or JSON null. */
    static Object any(JsonNode n) {
        return n == null || n.isNull() || n.isMissingNode() ? null : MAPPER.convertValue(n, Object.class);
    }

    /** The value as an object through {@code decode}; {@code null} unless it is a JSON object. */
    static <T> T object(JsonNode n, Function<JsonNode, T> decode) {
        return n != null && n.isObject() ? decode.apply(n) : null;
    }

    /** The text items of an array; empty for anything else. */
    static List<String> strings(JsonNode n) {
        List<String> out = new ArrayList<>();
        if (n != null && n.isArray()) {
            for (JsonNode s : n) {
                if (s.isTextual()) {
                    out.add(s.asText());
                }
            }
        }
        return out;
    }

    /** The items of an array through {@code decode}; empty for anything else. */
    static <T> List<T> list(JsonNode n, Function<JsonNode, T> decode) {
        List<T> out = new ArrayList<>();
        if (n != null && n.isArray()) {
            for (JsonNode item : n) {
                out.add(decode.apply(item));
            }
        }
        return out;
    }

    /** A value as a JSON tree; the argument is sent as given, its keys are never renamed. */
    static JsonNode tree(Object value) {
        try {
            return MAPPER.valueToTree(value);
        } catch (IllegalArgumentException e) {
            throw new IllegalArgumentException("hansho: data is not JSON-serializable", e);
        }
    }
}

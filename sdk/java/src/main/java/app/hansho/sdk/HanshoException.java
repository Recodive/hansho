package app.hansho.sdk;

import java.time.Duration;

/**
 * A non-2xx answer from the hub. The API key never appears in it. A failure without an answer
 * (connection, timeout, interruption) is a {@link HanshoNetworkException} instead.
 */
public class HanshoException extends RuntimeException {
    private static final long serialVersionUID = 1L;

    private final int status;
    private final String code;
    private final String field;
    private final long retryAfterMs;
    private final String detail;

    HanshoException(String method, String path, int status, String code, String field, long retryAfterMs, String detail) {
        super(message(method, path, status, code, field, retryAfterMs));
        this.status = status;
        this.code = code == null ? "" : code;
        this.field = field == null ? "" : field;
        this.retryAfterMs = retryAfterMs;
        this.detail = detail;
    }

    private static String message(String method, String path, int status, String code, String field, long retryAfterMs) {
        StringBuilder sb = new StringBuilder("hansho: ").append(method).append(' ').append(path).append(": ").append(status);
        if (code != null && !code.isEmpty()) {
            sb.append(' ').append(code);
        }
        if (field != null && !field.isEmpty()) {
            sb.append(" (field ").append(field).append(')');
        }
        if (retryAfterMs > 0) {
            sb.append(" (retry after ").append(retryAfterMs).append(" ms)");
        }
        return sb.toString();
    }

    /** The HTTP status. */
    public int status() {
        return status;
    }

    /** The hub's error code, e.g. {@code invalid_request}; empty when the body was not JSON. */
    public String code() {
        return code;
    }

    /** The offending request field for validation failures; empty otherwise. */
    public String field() {
        return field;
    }

    /** The parsed {@code Retry-After} in milliseconds; 0 when absent. */
    public long retryAfterMs() {
        return retryAfterMs;
    }

    /** The parsed {@code Retry-After}; {@link Duration#ZERO} when absent. */
    public Duration retryAfter() {
        return Duration.ofMillis(retryAfterMs);
    }

    /** The hub's validation message, when present (never the request body); otherwise {@code null}. */
    public String detail() {
        return detail;
    }
}

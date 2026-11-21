package app.hansho.sdk;

import java.time.Instant;
import java.time.ZonedDateTime;
import java.time.format.DateTimeFormatter;
import java.time.format.DateTimeParseException;
import java.util.concurrent.ThreadLocalRandom;
import java.util.regex.Pattern;

/** Retry policy: idempotent calls only, 429 and 503 only, bounded waits. */
final class Retry {
    static final int DEFAULT_MAX_RETRIES = 3;
    /** A longer Retry-After is surfaced to the caller instead of waited out. */
    static final long MAX_WAIT_MS = 60_000;

    private static final long BASE_BACKOFF_MS = 250;
    private static final long MAX_BACKOFF_MS = 8000;
    private static final Pattern SECONDS = Pattern.compile("\\d+(\\.\\d+)?");

    private Retry() {}

    static boolean isIdempotent(String method) {
        return method.equals("GET") || method.equals("PUT") || method.equals("DELETE");
    }

    static boolean isRetryableStatus(int status) {
        return status == 429 || status == 503;
    }

    /** Exponential backoff with jitter in [d/2, d]. */
    static long backoffMs(int attempt) {
        long d = Math.min(BASE_BACKOFF_MS << Math.min(attempt, 10), MAX_BACKOFF_MS);
        return d / 2 + ThreadLocalRandom.current().nextLong(d / 2 + 1);
    }

    /** Reads delay-seconds or an HTTP date; 0 when absent or unusable. */
    static long parseRetryAfter(String value, Instant now) {
        if (value == null) {
            return 0;
        }
        String v = value.trim();
        if (v.isEmpty()) {
            return 0;
        }
        if (SECONDS.matcher(v).matches()) {
            return Math.round(Double.parseDouble(v) * 1000);
        }
        try {
            Instant at = ZonedDateTime.parse(v, DateTimeFormatter.RFC_1123_DATE_TIME).toInstant();
            return Math.max(0, at.toEpochMilli() - now.toEpochMilli());
        } catch (DateTimeParseException e) {
            return 0;
        }
    }
}

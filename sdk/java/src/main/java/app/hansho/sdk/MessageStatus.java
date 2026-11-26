package app.hansho.sdk;

import java.time.Instant;
import java.util.List;

/**
 * A message's progress. {@code status} is one of queued, delivered, undeliverable, paging, acked, expired.
 *
 * @param ackedAt     when a page was acknowledged; {@code null} otherwise
 * @param ackedBy     who acknowledged it (their external user id); {@code null} when not acknowledged
 * @param ackedByName the display name of whoever acknowledged, {@code null} unless they have one
 */
public record MessageStatus(
        String messageId,
        String status,
        Level level,
        int attempts,
        List<Delivery> deliveries,
        Instant ackedAt,
        String ackedBy,
        String ackedByName) {
    public MessageStatus(
            String messageId, String status, Level level, int attempts, List<Delivery> deliveries, Instant ackedAt) {
        this(messageId, status, level, attempts, deliveries, ackedAt, null, null);
    }
}

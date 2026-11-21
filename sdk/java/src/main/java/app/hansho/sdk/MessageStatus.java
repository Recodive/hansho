package app.hansho.sdk;

import java.time.Instant;
import java.util.List;

/**
 * A message's progress. {@code status} is one of queued, delivered, undeliverable, paging, acked, expired.
 *
 * @param ackedAt when a page was acknowledged; {@code null} otherwise
 */
public record MessageStatus(
        String messageId, String status, Level level, int attempts, List<Delivery> deliveries, Instant ackedAt) {}

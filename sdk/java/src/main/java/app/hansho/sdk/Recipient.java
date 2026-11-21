package app.hansho.sdk;

import java.util.List;

/**
 * The outcome of a send for one user. {@code status} is the message status ({@code queued}, {@code undeliverable})
 * or {@link #NO_ACCESS}, in which case there is no message and {@code messageId} is {@code null}.
 *
 * @param externalUserId the user
 * @param messageId      the message id; {@code null} for a user without access
 * @param status         e.g. {@code queued} or {@link #NO_ACCESS}
 * @param devices        how many devices it was queued for
 * @param deliveries     per-device state
 */
public record Recipient(String externalUserId, String messageId, String status, int devices, List<Delivery> deliveries) {
    /** The {@code status} of a user outside the channel's audience: it got no message. */
    public static final String NO_ACCESS = "no_access";

    public Recipient {
        deliveries = deliveries == null ? List.of() : List.copyOf(deliveries);
    }
}

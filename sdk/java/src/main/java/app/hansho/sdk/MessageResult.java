package app.hansho.sdk;

import java.util.List;

/**
 * The hub's answer to a send.
 *
 * <p>For a send to {@link Message#externalUserId()} the other fields describe that one recipient: {@code messageId} is
 * empty, {@code status} is {@link Recipient#NO_ACCESS} and {@code devices} is 0 when the user is outside the channel's
 * audience. For several users or an audience, {@code messageId}, {@code devices} and {@code status} are empty and each
 * message is under {@code recipients}.
 *
 * @param messageId  the message id
 * @param level      the level it was sent at; {@code null} if the hub sent one this SDK does not know
 * @param devices    how many devices it was queued for
 * @param status     e.g. {@code queued}
 * @param deliveries per-device state
 * @param recipients one entry per user; empty when talking to a hub that predates audiences
 */
public record MessageResult(
        String messageId, Level level, int devices, String status, List<Delivery> deliveries, List<Recipient> recipients) {

    public MessageResult {
        recipients = recipients == null ? List.of() : List.copyOf(recipients);
    }

    public MessageResult(String messageId, Level level, int devices, String status, List<Delivery> deliveries) {
        this(messageId, level, devices, status, deliveries, List.of());
    }
}

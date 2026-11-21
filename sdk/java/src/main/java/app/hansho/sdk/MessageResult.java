package app.hansho.sdk;

import java.util.List;

/**
 * The hub's answer to a send.
 *
 * @param messageId  the message id
 * @param level      the level it was sent at; {@code null} if the hub sent one this SDK does not know
 * @param devices    how many devices it was queued for
 * @param status     e.g. {@code queued}
 * @param deliveries per-device state
 */
public record MessageResult(String messageId, Level level, int devices, String status, List<Delivery> deliveries) {}

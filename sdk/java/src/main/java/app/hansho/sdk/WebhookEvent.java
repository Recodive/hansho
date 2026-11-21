package app.hansho.sdk;

import java.time.Instant;

/**
 * A verified webhook. Narrow with {@code instanceof}; an event this SDK does not know is {@link Unknown}.
 * {@link #raw()} is the verified request body.
 */
public sealed interface WebhookEvent {
    /** The event name, e.g. {@code "message.acked"}; {@code "unknown"} for {@link Unknown}. */
    String type();

    /** The hub's event id. */
    String id();

    Instant createdAt();

    /** The verified request body. */
    byte[] raw();

    /** {@code pairing.created}. */
    record PairingCreated(String id, Instant createdAt, byte[] raw, String pairingId, String externalUserId, String deviceName) implements WebhookEvent {
        @Override
        public String type() {
            return "pairing.created";
        }
    }

    /** {@code pairing.revoked}. */
    record PairingRevoked(String id, Instant createdAt, byte[] raw, String pairingId, String externalUserId, String by) implements WebhookEvent {
        @Override
        public String type() {
            return "pairing.revoked";
        }
    }

    /** {@code message.delivered}. */
    record MessageDelivered(String id, Instant createdAt, byte[] raw, String messageId, String externalUserId, String channel, Level level, int devices) implements WebhookEvent {
        @Override
        public String type() {
            return "message.delivered";
        }
    }

    /** {@code message.undeliverable}. */
    record MessageUndeliverable(String id, Instant createdAt, byte[] raw, String messageId, String externalUserId, String channel, String reason) implements WebhookEvent {
        @Override
        public String type() {
            return "message.undeliverable";
        }
    }

    /** {@code message.acked}. */
    record MessageAcked(String id, Instant createdAt, byte[] raw, String messageId, String externalUserId, String deviceId) implements WebhookEvent {
        @Override
        public String type() {
            return "message.acked";
        }
    }

    /** The person swiped away a message that asked to be acknowledged, without acknowledging it. */
    record MessageDismissed(String id, Instant createdAt, byte[] raw, String messageId, String externalUserId, String deviceId) implements WebhookEvent {
        @Override
        public String type() {
            return "message.dismissed";
        }
    }

    /** {@code message.expired}. */
    record MessageExpired(String id, Instant createdAt, byte[] raw, String messageId, String externalUserId, String channel, int attempts) implements WebhookEvent {
        @Override
        public String type() {
            return "message.expired";
        }
    }

    /** A button press; {@code text} is the reply of a text action, otherwise {@code null}. */
    record ActionTaken(String id, Instant createdAt, byte[] raw, String messageId, String actionId, String deviceId, String externalUserId, String text) implements WebhookEvent {
        @Override
        public String type() {
            return "action";
        }
    }

    /** {@code activity.ended}. */
    record ActivityEnded(String id, Instant createdAt, byte[] raw, String activityId, String externalUserId, String channel, String reason) implements WebhookEvent {
        @Override
        public String type() {
            return "activity.ended";
        }
    }

    /**
     * An event from a newer hub. {@code name} is the event name as sent and {@code data} the payload as
     * parsed JSON (Map, List, String, Number, Boolean or null).
     */
    record Unknown(String id, Instant createdAt, byte[] raw, String name, Object data) implements WebhookEvent {
        @Override
        public String type() {
            return "unknown";
        }
    }
}

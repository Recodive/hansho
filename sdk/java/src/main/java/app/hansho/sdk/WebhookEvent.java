package app.hansho.sdk;

import java.time.Instant;

/**
 * A verified webhook. Narrow with {@code instanceof}; an event this SDK does not know is {@link Unknown}.
 * {@link #raw()} is the verified request body, and {@code data()} of a known event is its payload, one of the
 * {@link Models} types of the same name (for example {@link Models.MessageAcked}).
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
    record PairingCreated(String id, Instant createdAt, byte[] raw, Models.PairingCreated data) implements WebhookEvent {
        @Override
        public String type() {
            return "pairing.created";
        }
    }

    /** {@code pairing.revoked}. */
    record PairingRevoked(String id, Instant createdAt, byte[] raw, Models.PairingRevoked data) implements WebhookEvent {
        @Override
        public String type() {
            return "pairing.revoked";
        }
    }

    /** {@code message.delivered}. */
    record MessageDelivered(String id, Instant createdAt, byte[] raw, Models.MessageDelivered data) implements WebhookEvent {
        @Override
        public String type() {
            return "message.delivered";
        }
    }

    /** {@code message.undeliverable}. */
    record MessageUndeliverable(String id, Instant createdAt, byte[] raw, Models.MessageUndeliverable data) implements WebhookEvent {
        @Override
        public String type() {
            return "message.undeliverable";
        }
    }

    /** {@code message.acked}. */
    record MessageAcked(String id, Instant createdAt, byte[] raw, Models.MessageAcked data) implements WebhookEvent {
        @Override
        public String type() {
            return "message.acked";
        }
    }

    /** {@code message.dismissed}. */
    record MessageDismissed(String id, Instant createdAt, byte[] raw, Models.MessageDismissed data) implements WebhookEvent {
        @Override
        public String type() {
            return "message.dismissed";
        }
    }

    /** {@code message.expired}. */
    record MessageExpired(String id, Instant createdAt, byte[] raw, Models.MessageExpired data) implements WebhookEvent {
        @Override
        public String type() {
            return "message.expired";
        }
    }

    /** {@code action}. */
    record ActionTaken(String id, Instant createdAt, byte[] raw, Models.ActionTaken data) implements WebhookEvent {
        @Override
        public String type() {
            return "action";
        }
    }

    /** {@code activity.ended}. */
    record ActivityEnded(String id, Instant createdAt, byte[] raw, Models.ActivityEnded data) implements WebhookEvent {
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

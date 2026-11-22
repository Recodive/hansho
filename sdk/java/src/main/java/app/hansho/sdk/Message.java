package app.hansho.sdk;

import java.util.List;
import java.util.Objects;

/**
 * A notification, or a page when {@code level} is {@link Level#PAGE}.
 *
 * <p>{@code data} is sent as given (keys are never renamed); it may be a {@code Map}, a {@code List},
 * a Jackson {@code JsonNode} or any Jackson-serializable object.
 *
 * <p>{@code idempotencyKey} opts in to retries of 429 and 503 for this send. It is sent as the
 * {@code Idempotency-Key} header, never in the body. The hub does not deduplicate on it yet, so a
 * retried send is at-least-once.
 *
 * <p>A message goes to exactly one target: one user ({@link #builder}), several users ({@link #forUsers}, at most
 * 1000) or every paired user in the channel's audience ({@link #forAudience}, resolved when you send). People outside
 * the audience are not delivered to: their {@link Recipient} has the status {@link Recipient#NO_ACCESS}.
 */
public record Message(
        String externalUserId,
        List<String> externalUserIds,
        boolean toAudience,
        String channel,
        Level level,
        String title,
        String subtitle,
        String body,
        String imageUrl,
        String templateId,
        Object data,
        String sound,
        List<Action> actions,
        String collapseId,
        PageOptions page,
        String idempotencyKey) {

    public Message {
        externalUserIds = externalUserIds == null ? null : List.copyOf(externalUserIds);
        actions = actions == null ? null : List.copyOf(actions);
    }

    /** A message for one user. */
    public static Builder builder(String externalUserId, String channel, String title, String body) {
        return new Builder(Objects.requireNonNull(externalUserId, "externalUserId"), null, false, channel, title, body);
    }

    /** A message for several users at once (at most 1000). */
    public static Builder forUsers(List<String> externalUserIds, String channel, String title, String body) {
        return new Builder(null, Objects.requireNonNull(externalUserIds, "externalUserIds"), false, channel, title, body);
    }

    /** A message for every paired user in the channel's audience, resolved when you send. */
    public static Builder forAudience(String channel, String title, String body) {
        return new Builder(null, null, true, channel, title, body);
    }

    /** Builds a {@link Message}. */
    public static final class Builder {
        private final String externalUserId;
        private final List<String> externalUserIds;
        private final boolean toAudience;
        private final String channel;
        private final String title;
        private final String body;
        private Level level;
        private String subtitle;
        private String imageUrl;
        private String templateId;
        private Object data;
        private String sound;
        private List<Action> actions;
        private String collapseId;
        private PageOptions page;
        private String idempotencyKey;

        private Builder(
                String externalUserId, List<String> externalUserIds, boolean toAudience, String channel, String title, String body) {
            this.externalUserId = externalUserId;
            this.externalUserIds = externalUserIds;
            this.toAudience = toAudience;
            this.channel = Objects.requireNonNull(channel, "channel");
            this.title = Objects.requireNonNull(title, "title");
            this.body = Objects.requireNonNull(body, "body");
        }

        /** Defaults to the channel's default level and is capped at its maximum. */
        public Builder level(Level level) {
            this.level = level;
            return this;
        }

        public Builder subtitle(String subtitle) {
            this.subtitle = subtitle;
            return this;
        }

        public Builder imageUrl(String imageUrl) {
            this.imageUrl = imageUrl;
            return this;
        }

        public Builder templateId(String templateId) {
            this.templateId = templateId;
            return this;
        }

        public Builder data(Object data) {
            this.data = data;
            return this;
        }

        public Builder sound(String sound) {
            this.sound = sound;
            return this;
        }

        public Builder actions(List<Action> actions) {
            this.actions = actions;
            return this;
        }

        /** A later message with the same id replaces this one. */
        public Builder collapseId(String collapseId) {
            this.collapseId = collapseId;
            return this;
        }

        public Builder page(PageOptions page) {
            this.page = page;
            return this;
        }

        public Builder idempotencyKey(String idempotencyKey) {
            this.idempotencyKey = idempotencyKey;
            return this;
        }

        public Message build() {
            return new Message(
                    externalUserId, externalUserIds, toAudience, channel, level, title, subtitle, body, imageUrl, templateId, data, sound,
                    actions, collapseId, page, idempotencyKey);
        }
    }
}

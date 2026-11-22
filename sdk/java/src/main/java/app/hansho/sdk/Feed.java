package app.hansho.sdk;

import java.util.Objects;

/**
 * A widget feed.
 *
 * <p>{@code channel} and {@code audience} say who may see the feed. Without an audience it uses the audience of its
 * channel, and with neither it is for everyone. Like {@code title}, both are stored per feed, so send them with every
 * update; a {@code null} audience keeps the one stored on the hub (send {@link Audience#everyone()} to open the feed up).
 *
 * @param externalUserId {@code null} for a feed shared by all paired users
 * @param templateId     the template that renders it
 * @param title          the name the app's feed picker shows instead of the id (at most 64 bytes); may be {@code null}
 * @param data           template data, sent as given
 * @param channel        the channel whose audience the feed uses; may be {@code null}
 * @param audience       the feed's own audience; may be {@code null}
 */
public record Feed(
        String externalUserId, String templateId, String title, Object data, String channel, Audience audience) {

    public Feed(String externalUserId, String templateId, String title, Object data) {
        this(externalUserId, templateId, title, data, null, null);
    }

    public static Feed of(String templateId, Object data) {
        return new Feed(null, Objects.requireNonNull(templateId, "templateId"), null, data, null, null);
    }

    public Feed forUser(String externalUserId) {
        return new Feed(externalUserId, templateId, title, data, channel, audience);
    }

    public Feed withTitle(String title) {
        return new Feed(externalUserId, templateId, title, data, channel, audience);
    }

    public Feed withChannel(String channel) {
        return new Feed(externalUserId, templateId, title, data, channel, audience);
    }

    public Feed withAudience(Audience audience) {
        return new Feed(externalUserId, templateId, title, data, channel, audience);
    }
}

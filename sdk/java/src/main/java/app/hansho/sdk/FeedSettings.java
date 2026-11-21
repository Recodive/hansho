package app.hansho.sdk;

/**
 * A feed without its data, from {@link Hansho#getFeed}.
 *
 * @param feedId         the feed id
 * @param externalUserId {@code null} for a feed shared by all paired users
 * @param templateId     the template that renders it
 * @param title          the picker name; {@code null} when there is none
 * @param channel        the channel whose audience it inherits; {@code null} when there is none
 * @param audience       its own audience; {@code null} while it inherits its channel's
 * @param updatedAt      when the data last changed, in seconds since the epoch
 */
public record FeedSettings(
        String feedId, String externalUserId, String templateId, String title, String channel, Audience audience, long updatedAt) {}

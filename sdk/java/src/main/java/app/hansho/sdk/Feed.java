package app.hansho.sdk;

import java.util.Objects;

/**
 * A widget feed.
 *
 * @param externalUserId {@code null} for a feed shared by all paired users
 * @param templateId     the template that renders it
 * @param title          the name the app's feed picker shows instead of the id (at most 64 bytes); may be {@code null}
 * @param data           template data, sent as given
 */
public record Feed(String externalUserId, String templateId, String title, Object data) {
    public static Feed of(String templateId, Object data) {
        return new Feed(null, Objects.requireNonNull(templateId, "templateId"), null, data);
    }

    public Feed forUser(String externalUserId) {
        return new Feed(externalUserId, templateId, title, data);
    }

    public Feed withTitle(String title) {
        return new Feed(externalUserId, templateId, title, data);
    }
}

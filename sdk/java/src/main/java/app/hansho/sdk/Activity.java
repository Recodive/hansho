package app.hansho.sdk;

import java.util.Objects;

/** Starts a Live Activity. {@code data} is sent as given (see {@link Message}). */
public record Activity(String externalUserId, String channel, String templateId, Object data, ActivityAlert alert) {
    public static Activity of(String externalUserId, String channel, String templateId, Object data) {
        return new Activity(
                Objects.requireNonNull(externalUserId, "externalUserId"),
                Objects.requireNonNull(channel, "channel"),
                Objects.requireNonNull(templateId, "templateId"),
                data,
                null);
    }

    public Activity withAlert(ActivityAlert alert) {
        return new Activity(externalUserId, channel, templateId, data, alert);
    }
}

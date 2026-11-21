package app.hansho.sdk;

/**
 * A Live Activity update.
 *
 * @param data     the new template data, sent as given
 * @param priority 5 (the hub's default) or 10; {@code null} leaves it out
 * @param alert    an optional alert
 */
public record ActivityUpdate(Object data, Integer priority, ActivityAlert alert) {
    public ActivityUpdate {
        if (priority != null && priority != 5 && priority != 10) {
            throw new IllegalArgumentException("hansho: priority must be 5 or 10");
        }
    }

    public static ActivityUpdate of(Object data) {
        return new ActivityUpdate(data, null, null);
    }

    public ActivityUpdate withPriority(int priority) {
        return new ActivityUpdate(data, priority, alert);
    }

    public ActivityUpdate withAlert(ActivityAlert alert) {
        return new ActivityUpdate(data, priority, alert);
    }
}

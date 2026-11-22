package app.hansho.sdk;

import java.util.List;
import java.util.Objects;

/**
 * A channel: where messages go and the defaults they inherit.
 *
 * <p>{@code audience} decides who may use the channel. A {@code null} audience keeps the one stored on the hub (a new
 * channel starts open to everyone); send {@link Audience#everyone()} to open an existing channel up.
 * {@link Hansho#getChannel} always returns one.
 */
public record Channel(
        String name,
        Level defaultLevel,
        Level maxLevel,
        String sound,
        String templateId,
        String thread,
        List<Action> actions,
        Audience audience) {

    public Channel {
        actions = actions == null ? null : List.copyOf(actions);
    }

    public Channel(
            String name, Level defaultLevel, Level maxLevel, String sound, String templateId, String thread, List<Action> actions) {
        this(name, defaultLevel, maxLevel, sound, templateId, thread, actions, null);
    }

    public static Builder builder(String name, Level defaultLevel, Level maxLevel) {
        return new Builder(name, defaultLevel, maxLevel);
    }

    /** Builds a {@link Channel}. */
    public static final class Builder {
        private final String name;
        private final Level defaultLevel;
        private final Level maxLevel;
        private String sound;
        private String templateId;
        private String thread;
        private List<Action> actions;
        private Audience audience;

        private Builder(String name, Level defaultLevel, Level maxLevel) {
            this.name = Objects.requireNonNull(name, "name");
            this.defaultLevel = Objects.requireNonNull(defaultLevel, "defaultLevel");
            this.maxLevel = Objects.requireNonNull(maxLevel, "maxLevel");
        }

        public Builder sound(String sound) {
            this.sound = sound;
            return this;
        }

        public Builder templateId(String templateId) {
            this.templateId = templateId;
            return this;
        }

        public Builder thread(String thread) {
            this.thread = thread;
            return this;
        }

        public Builder actions(List<Action> actions) {
            this.actions = actions;
            return this;
        }

        /** Who may use the channel; leave it out to keep the one stored on the hub. */
        public Builder audience(Audience audience) {
            this.audience = audience;
            return this;
        }

        public Channel build() {
            return new Channel(name, defaultLevel, maxLevel, sound, templateId, thread, actions, audience);
        }
    }
}

package app.hansho.sdk;

/** How loudly a message is presented, lowest to highest. */
public enum Level {
    PASSIVE("passive"),
    ACTIVE("active"),
    TIME_SENSITIVE("time-sensitive"),
    CRITICAL("critical"),
    PAGE("page");

    private final String wire;

    Level(String wire) {
        this.wire = wire;
    }

    /** The value used on the wire, e.g. {@code "time-sensitive"}. */
    public String wire() {
        return wire;
    }

    /** Parses a wire value; {@code null} for a level this SDK does not know (a newer hub). */
    public static Level fromWire(String value) {
        for (Level l : values()) {
            if (l.wire.equals(value)) {
                return l;
            }
        }
        return null;
    }
}

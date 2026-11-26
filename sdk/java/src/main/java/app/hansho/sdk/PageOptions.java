package app.hansho.sdk;

/**
 * How a page repeats until it is acknowledged.
 *
 * @param intervalSeconds seconds between resends (hub default 120); {@code null} for the default
 * @param maxRetries      resends before the page expires (hub default 5); {@code null} for the default
 * @param ack             who must acknowledge a page sent to several people; {@code null} for the hub default
 *                        ({@link Ack#ANY})
 */
public record PageOptions(Integer intervalSeconds, Integer maxRetries, Ack ack) {
    /** Who must acknowledge a page sent to several people. */
    public enum Ack {
        /** The first acknowledgement stops the page for everyone (hub default). */
        ANY("any"),
        /** Each person is paged until they acknowledge for themselves. */
        EACH("each");

        private final String wire;

        Ack(String wire) {
            this.wire = wire;
        }

        String wire() {
            return wire;
        }
    }

    public PageOptions(Integer intervalSeconds, Integer maxRetries) {
        this(intervalSeconds, maxRetries, null);
    }

    public static PageOptions of(int intervalSeconds, int maxRetries) {
        return new PageOptions(intervalSeconds, maxRetries, null);
    }

    /** Page options that only set who must acknowledge; interval and retries stay at the hub defaults. */
    public static PageOptions ack(Ack ack) {
        return new PageOptions(null, null, ack);
    }

    /** A copy that asks for the given acknowledgement mode. */
    public PageOptions withAck(Ack ack) {
        return new PageOptions(intervalSeconds, maxRetries, ack);
    }
}

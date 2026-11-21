package app.hansho.sdk;

/**
 * How a page repeats until it is acknowledged.
 *
 * @param intervalSeconds seconds between resends (hub default 120); {@code null} for the default
 * @param maxRetries      resends before the page expires (hub default 5); {@code null} for the default
 */
public record PageOptions(Integer intervalSeconds, Integer maxRetries) {
    public static PageOptions of(int intervalSeconds, int maxRetries) {
        return new PageOptions(intervalSeconds, maxRetries);
    }
}

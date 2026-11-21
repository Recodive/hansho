package app.hansho.sdk;

/** No HTTP answer arrived: connection failure, timeout, interruption or an unreadable answer. See {@link #getCause()}. */
public class HanshoNetworkException extends RuntimeException {
    private static final long serialVersionUID = 1L;

    HanshoNetworkException(String method, String path, Throwable cause) {
        super("hansho: " + method + " " + path + ": " + describe(cause), cause);
    }

    private static String describe(Throwable cause) {
        String m = cause.getMessage();
        return m == null || m.isEmpty() ? cause.getClass().getSimpleName() : m;
    }
}

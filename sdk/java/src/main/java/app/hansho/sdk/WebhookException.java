package app.hansho.sdk;

/** Thrown by {@link Webhook#verify}; check {@link #code()}. */
public class WebhookException extends RuntimeException {
    private static final long serialVersionUID = 1L;

    /** Why a webhook was refused. */
    public enum Code {
        MALFORMED_HEADER("malformed_header"),
        BAD_SIGNATURE("bad_signature"),
        /** A valid signature, replayed too late (or from too far in the future). */
        STALE("stale"),
        MALFORMED_BODY("malformed_body"),
        INVALID_BODY("invalid_body"),
        INVALID_SECRET("invalid_secret");

        private final String wire;

        Code(String wire) {
            this.wire = wire;
        }

        /** The code as the other SDKs spell it, e.g. {@code "bad_signature"}. */
        public String wire() {
            return wire;
        }
    }

    private final Code code;

    WebhookException(Code code, String message) {
        super(message);
        this.code = code;
    }

    public Code code() {
        return code;
    }
}

package app.hansho.sdk;

/** How an {@link Audience} reads its lists: everyone, only the listed, or everyone except the listed. */
public enum AudienceMode {
    /** Every paired user (the lists must be empty). */
    EVERYONE("everyone"),
    /** Just the listed users and the members of the listed groups. */
    ONLY("only"),
    /** Everyone but the listed users and the members of the listed groups. */
    EXCEPT("except");

    private final String wire;

    AudienceMode(String wire) {
        this.wire = wire;
    }

    /** The value used on the wire, e.g. {@code "only"}. */
    public String wire() {
        return wire;
    }

    /** Parses a wire value; {@code null} for a mode this SDK does not know (a newer hub). */
    public static AudienceMode fromWire(String value) {
        for (AudienceMode m : values()) {
            if (m.wire.equals(value)) {
                return m;
            }
        }
        return null;
    }
}

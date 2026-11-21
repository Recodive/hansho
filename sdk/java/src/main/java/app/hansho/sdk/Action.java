package app.hansho.sdk;

import java.util.Objects;

/**
 * A button shown with a message or channel. An {@code ack} action acknowledges a page.
 *
 * @param id    action id, reported back in the {@code action} webhook
 * @param title button label
 * @param ack   {@code true} to acknowledge a page; {@code null} leaves it out
 * @param kind  e.g. {@code "text"} for a reply field; {@code null} leaves it out
 */
public record Action(String id, String title, Boolean ack, String kind) {
    public Action {
        Objects.requireNonNull(id, "id");
        Objects.requireNonNull(title, "title");
    }

    /** A plain button. */
    public static Action of(String id, String title) {
        return new Action(id, title, null, null);
    }

    /** A button that acknowledges a page. */
    public static Action acknowledge(String id, String title) {
        return new Action(id, title, Boolean.TRUE, null);
    }

    /** A button that opens a reply field; the text arrives in the {@code action} webhook. */
    public static Action text(String id, String title) {
        return new Action(id, title, null, "text");
    }
}

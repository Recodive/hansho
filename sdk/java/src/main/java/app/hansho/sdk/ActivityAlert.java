package app.hansho.sdk;

/** The alert an activity start or update may show. Either part may be {@code null}. */
public record ActivityAlert(String title, String body) {}

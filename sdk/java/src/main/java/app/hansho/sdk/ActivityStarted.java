package app.hansho.sdk;

/** The hub's answer to {@link Hansho#startActivity}. */
public record ActivityStarted(String activityId, int devices) {}

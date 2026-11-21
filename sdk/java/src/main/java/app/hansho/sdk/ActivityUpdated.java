package app.hansho.sdk;

/** The hub's answer to {@link Hansho#updateActivity}. */
public record ActivityUpdated(String activityId, int update) {}

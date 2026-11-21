package app.hansho.sdk;

/** The hub's answer to {@link Hansho#putFeed}. */
public record FeedResult(String feedId, int devices) {}

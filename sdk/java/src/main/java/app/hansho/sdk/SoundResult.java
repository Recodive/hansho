package app.hansho.sdk;

/** The hub's answer to {@link Hansho#putSound}. */
public record SoundResult(String id, String filename, String sha256, long size) {}

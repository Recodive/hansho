package app.hansho.sdk;

/**
 * One device a message was handed to.
 *
 * @param deviceId the device
 * @param status   e.g. {@code queued}, {@code sent}, {@code failed}
 * @param error    the failure reason; {@code null} when there is none
 */
public record Delivery(String deviceId, String status, String error) {}

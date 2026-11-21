package app.hansho.sdk;

import java.time.Instant;

/**
 * A single-use pairing ticket, valid for 10 minutes.
 *
 * @param ticket    the ticket value; treat it as a secret
 * @param qr        render this string as a QR code for the user to scan in the Hansho app
 * @param expiresAt when the ticket expires
 */
public record PairingTicket(String ticket, String qr, Instant expiresAt) {
    @Override
    public String toString() {
        return "PairingTicket[expiresAt=" + expiresAt + "]";
    }
}

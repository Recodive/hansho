using System;
using System.Collections.Generic;
using System.Globalization;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;

namespace Hansho;

/// <summary>Why <see cref="HanshoWebhook.Verify(string, string?, ReadOnlySpan{byte}, DateTimeOffset?, TimeSpan?)"/> refused a webhook.</summary>
public enum WebhookErrorCode
{
    /// <summary>The signature header is missing or not <c>t=&lt;unix&gt;,v1=&lt;hex&gt;</c>.</summary>
    MalformedHeader,
    /// <summary>No <c>v1</c> signature matches the body and secret.</summary>
    BadSignature,
    /// <summary>A valid signature whose timestamp is outside the tolerance (a replay).</summary>
    Stale,
    /// <summary>A valid signature over a body that is not a webhook envelope.</summary>
    MalformedBody,
    /// <summary>The secret is empty.</summary>
    InvalidSecret,
}

/// <summary>Thrown by <see cref="HanshoWebhook"/>; check <see cref="Code"/>.</summary>
public sealed class WebhookException : Exception
{
    public WebhookErrorCode Code { get; }

    public WebhookException(WebhookErrorCode code, string message) : base(message) => Code = code;
}

/// <summary>
/// A verified webhook. Switch on the type (<c>event is MessageAcked acked</c>); an event this SDK does not know is
/// an <see cref="UnknownWebhookEvent"/>, never an exception.
/// </summary>
public abstract record WebhookEvent
{
    /// <summary>The event name as the hub sent it, e.g. <c>message.acked</c>; <c>unknown</c> for <see cref="UnknownWebhookEvent"/>.</summary>
    public abstract string Type { get; }

    /// <summary>The hub's event id.</summary>
    public string Id { get; init; } = "";

    public DateTimeOffset CreatedAt { get; init; }

    /// <summary>The verified request body.</summary>
    public byte[] Raw { get; init; } = Array.Empty<byte>();
}

public sealed record PairingCreated(string PairingId, string ExternalUserId, string DeviceName) : WebhookEvent
{
    public override string Type => "pairing.created";
}

/// <summary>By is who revoked: "sender", "device" or "relay".</summary>
public sealed record PairingRevoked(string PairingId, string ExternalUserId, string By) : WebhookEvent
{
    public override string Type => "pairing.revoked";
}

public sealed record MessageDelivered(string MessageId, string ExternalUserId, string Channel, Level Level, int Devices) : WebhookEvent
{
    public override string Type => "message.delivered";
}

/// <summary>Reason is "no_devices" or "all_deliveries_failed".</summary>
public sealed record MessageUndeliverable(string MessageId, string ExternalUserId, string Channel, string Reason) : WebhookEvent
{
    public override string Type => "message.undeliverable";
}

public sealed record MessageAcked(string MessageId, string ExternalUserId, string DeviceId) : WebhookEvent
{
    public override string Type => "message.acked";
}

/// <summary>The person swiped away a message that asked to be acknowledged, without acknowledging it.</summary>
public sealed record MessageDismissed(string MessageId, string ExternalUserId, string DeviceId) : WebhookEvent
{
    public override string Type => "message.dismissed";
}

public sealed record MessageExpired(string MessageId, string ExternalUserId, string Channel, int Attempts) : WebhookEvent
{
    public override string Type => "message.expired";
}

/// <summary>A button press. Text is the reply of a text action.</summary>
public sealed record ActionTaken(string MessageId, string ActionId, string DeviceId, string ExternalUserId, string? Text) : WebhookEvent
{
    public override string Type => "action";
}

public sealed record ActivityEnded(string ActivityId, string ExternalUserId, string Channel, string Reason) : WebhookEvent
{
    public override string Type => "activity.ended";
}

/// <summary>An event newer than this SDK: Name is the event name as sent, Data the payload as parsed JSON, untouched.</summary>
public sealed record UnknownWebhookEvent(string Name, JsonElement Data) : WebhookEvent
{
    public override string Type => "unknown";
}

/// <summary>
/// Verifies the hub's webhooks. The signature header is
/// <c>X-Hansho-Webhook-Signature: t=&lt;unix&gt;,v1=&lt;hex HMAC-SHA256(secret, t + "." + body)&gt;</c>.
/// Verify the <b>raw</b> request body: a body a framework parsed and you re-serialised will not match its
/// signature. Only byte and string bodies are accepted, so a parsed object does not compile.
/// </summary>
public static class HanshoWebhook
{
    /// <summary>The request header that carries the signature.</summary>
    public const string SignatureHeader = "X-Hansho-Webhook-Signature";

    /// <summary>Default allowed distance between the signed timestamp and now: 300 s.</summary>
    public static readonly TimeSpan DefaultTolerance = TimeSpan.FromSeconds(300);

    /// <summary>
    /// Verifies a webhook against its raw body and returns the event. Throws <see cref="WebhookException"/>. With
    /// several <c>v1</c> values in the header (secret rotation) any match is accepted.
    /// </summary>
    /// <param name="secret">The shared secret (<c>HUB_WEBHOOK_SECRET</c>).</param>
    /// <param name="signatureHeader">The <see cref="SignatureHeader"/> request header; null when absent.</param>
    /// <param name="body">The request body exactly as received.</param>
    /// <param name="now">The current time; default: the clock.</param>
    /// <param name="tolerance">Allowed distance between the signed timestamp and now. Null or not positive means 300 s.</param>
    public static WebhookEvent Verify(ReadOnlySpan<byte> secret, string? signatureHeader, ReadOnlySpan<byte> body, DateTimeOffset? now = null, TimeSpan? tolerance = null)
    {
        if (secret.IsEmpty) throw new WebhookException(WebhookErrorCode.InvalidSecret, "webhook secret is empty");
        var (t, sigs) = ParseHeader(signatureHeader);

        var tBytes = Encoding.ASCII.GetBytes(t);
        var signed = new byte[tBytes.Length + 1 + body.Length];
        tBytes.CopyTo(signed, 0);
        signed[tBytes.Length] = (byte)'.';
        body.CopyTo(signed.AsSpan(tBytes.Length + 1));
        var want = HMACSHA256.HashData(secret, signed);

        var match = false;
        foreach (var sig in sigs)
        {
            if (!IsHex64(sig)) continue;
            // Every candidate is compared, in constant time, so timing does not reveal which one matched.
            match |= CryptographicOperations.FixedTimeEquals(Convert.FromHexString(sig), want);
        }
        if (!match) throw new WebhookException(WebhookErrorCode.BadSignature, "signature does not match");

        var tol = tolerance is { } tv && tv > TimeSpan.Zero ? tv : DefaultTolerance;
        var nowMs = (now ?? DateTimeOffset.UtcNow).ToUnixTimeMilliseconds();
        var ageMs = !long.TryParse(t, NumberStyles.AllowLeadingSign, CultureInfo.InvariantCulture, out var ts)
            ? double.PositiveInfinity
            : Math.Abs(nowMs - (double)ts * 1000);
        if (ageMs > tol.TotalMilliseconds) throw new WebhookException(WebhookErrorCode.Stale, "timestamp outside tolerance");

        return Parse(body.ToArray());
    }

    /// <inheritdoc cref="Verify(ReadOnlySpan{byte}, string?, ReadOnlySpan{byte}, DateTimeOffset?, TimeSpan?)"/>
    public static WebhookEvent Verify(string secret, string? signatureHeader, ReadOnlySpan<byte> body, DateTimeOffset? now = null, TimeSpan? tolerance = null) =>
        Verify(Encoding.UTF8.GetBytes(secret ?? ""), signatureHeader, body, now, tolerance);

    /// <inheritdoc cref="Verify(ReadOnlySpan{byte}, string?, ReadOnlySpan{byte}, DateTimeOffset?, TimeSpan?)"/>
    /// <remarks>The string is encoded as UTF-8, byte for byte what <c>new StreamReader(body).ReadToEnd()</c> decoded.</remarks>
    public static WebhookEvent Verify(string secret, string? signatureHeader, string body, DateTimeOffset? now = null, TimeSpan? tolerance = null) =>
        Verify(Encoding.UTF8.GetBytes(secret ?? ""), signatureHeader, Encoding.UTF8.GetBytes(body ?? ""), now, tolerance);

    private static bool IsHex64(string s)
    {
        if (s.Length != 64) return false;
        foreach (var c in s)
            if (!Uri.IsHexDigit(c)) return false;
        return true;
    }

    private static (string T, List<string> Sigs) ParseHeader(string? header)
    {
        if (string.IsNullOrWhiteSpace(header)) throw new WebhookException(WebhookErrorCode.MalformedHeader, "missing signature header");
        string? t = null;
        var sigs = new List<string>();
        foreach (var part in header.Split(','))
        {
            var trimmed = part.Trim();
            var eq = trimmed.IndexOf('=');
            if (eq < 0) throw new WebhookException(WebhookErrorCode.MalformedHeader, "signature header part is not key=value");
            var k = trimmed[..eq];
            var v = trimmed[(eq + 1)..];
            if (k == "t")
            {
                if (t is not null) throw new WebhookException(WebhookErrorCode.MalformedHeader, "duplicate timestamp");
                t = v;
            }
            else if (k == "v1" && v != "")
            {
                sigs.Add(v);
            }
        }
        if (t is null || !IsSignedDigits(t) || sigs.Count == 0)
            throw new WebhookException(WebhookErrorCode.MalformedHeader, "signature header needs t=<unix> and v1=<hex>");
        return (t, sigs);
    }

    private static bool IsSignedDigits(string s)
    {
        var i = s.StartsWith('-') ? 1 : 0;
        if (i >= s.Length) return false;
        for (; i < s.Length; i++)
            if (s[i] is < '0' or > '9') return false;
        return true;
    }

    private static WebhookEvent Parse(byte[] body)
    {
        JsonDocument doc;
        try
        {
            // Strict UTF-8: a body with invalid bytes is not an envelope.
            doc = JsonDocument.Parse(new UTF8Encoding(false, true).GetString(body));
        }
        catch (Exception e) when (e is JsonException or DecoderFallbackException)
        {
            throw new WebhookException(WebhookErrorCode.MalformedBody, "body is not a webhook envelope");
        }

        using (doc)
        {
            var env = doc.RootElement;
            if (env.ValueKind != JsonValueKind.Object) throw new WebhookException(WebhookErrorCode.MalformedBody, "body is not a webhook envelope");
            if (!env.TryGetProperty("event", out var ev) || ev.ValueKind != JsonValueKind.String || ev.GetString() is not { Length: > 0 } name)
                throw new WebhookException(WebhookErrorCode.MalformedBody, "body has no event");

            var id = S(env, "id");
            var createdAt = Unix(env, "created_at");
            var data = env.TryGetProperty("data", out var d) && d.ValueKind == JsonValueKind.Object ? d : default;

            WebhookEvent? mapped = name switch
            {
                "pairing.created" => new PairingCreated(S(data, "pairing_id"), S(data, "external_user_id"), S(data, "device_name")),
                "pairing.revoked" => new PairingRevoked(S(data, "pairing_id"), S(data, "external_user_id"), S(data, "by")),
                "message.delivered" => new MessageDelivered(S(data, "message_id"), S(data, "external_user_id"), S(data, "channel"), new Level(S(data, "level")), N(data, "devices")),
                "message.undeliverable" => new MessageUndeliverable(S(data, "message_id"), S(data, "external_user_id"), S(data, "channel"), S(data, "reason")),
                "message.acked" => new MessageAcked(S(data, "message_id"), S(data, "external_user_id"), S(data, "device_id")),
                "message.dismissed" => new MessageDismissed(S(data, "message_id"), S(data, "external_user_id"), S(data, "device_id")),
                "message.expired" => new MessageExpired(S(data, "message_id"), S(data, "external_user_id"), S(data, "channel"), N(data, "attempts")),
                "action" => new ActionTaken(S(data, "message_id"), S(data, "action_id"), S(data, "device_id"), S(data, "external_user_id"), data.ValueKind == JsonValueKind.Object && data.TryGetProperty("text", out var tx) && tx.ValueKind == JsonValueKind.String ? tx.GetString() : null),
                "activity.ended" => new ActivityEnded(S(data, "activity_id"), S(data, "external_user_id"), S(data, "channel"), S(data, "reason")),
                _ => null,
            };
            if (mapped is null)
            {
                // The payload as sent (not an empty object when it was something else), detached from the document.
                var payload = env.TryGetProperty("data", out var rawData) ? rawData.Clone() : default;
                return new UnknownWebhookEvent(name, payload) { Id = id, CreatedAt = createdAt, Raw = body };
            }
            return mapped with { Id = id, CreatedAt = createdAt, Raw = body };
        }
    }

    // A forgiving read, like the other SDKs: a missing or oddly typed field reads as empty, never as an exception.
    private static string S(JsonElement o, string key)
    {
        if (o.ValueKind != JsonValueKind.Object || !o.TryGetProperty(key, out var v)) return "";
        return v.ValueKind switch
        {
            JsonValueKind.String => v.GetString() ?? "",
            JsonValueKind.Null or JsonValueKind.Undefined => "",
            _ => v.GetRawText(),
        };
    }

    private static int N(JsonElement o, string key)
    {
        if (o.ValueKind != JsonValueKind.Object || !o.TryGetProperty(key, out var v)) return 0;
        if (v.ValueKind == JsonValueKind.Number) return v.TryGetInt32(out var i) ? i : (int)Math.Clamp(v.GetDouble(), int.MinValue, int.MaxValue);
        return v.ValueKind == JsonValueKind.String && double.TryParse(v.GetString(), NumberStyles.Float, CultureInfo.InvariantCulture, out var d)
            ? (int)Math.Clamp(d, int.MinValue, int.MaxValue)
            : 0;
    }

    private static DateTimeOffset Unix(JsonElement o, string key)
    {
        if (!o.TryGetProperty(key, out var v)) return DateTimeOffset.UnixEpoch;
        double s;
        if (v.ValueKind == JsonValueKind.Number) s = v.GetDouble();
        else if (v.ValueKind != JsonValueKind.String || !double.TryParse(v.GetString(), NumberStyles.Float, CultureInfo.InvariantCulture, out s)) return DateTimeOffset.UnixEpoch;
        // DateTimeOffset spans years 1 to 9999; anything outside is not a usable timestamp.
        return s is > -62135596800 and < 253402300799 ? DateTimeOffset.FromUnixTimeMilliseconds((long)(s * 1000)) : DateTimeOffset.UnixEpoch;
    }
}

using System;
using System.Collections.Generic;
using System.Linq;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using Xunit;

namespace Hansho.Tests;

public class WebhookTests
{
    private const string Secret = "whsec_sdk_vectors";

    private sealed record Vector(string Name, string Secret, string Header, string Body, long Now, JsonElement Expect);

    private static readonly double ToleranceSeconds;
    private static readonly List<Vector> Vectors;

    static WebhookTests()
    {
        var root = Spec.Load("webhook-vectors.json").RootElement.Clone();
        ToleranceSeconds = root.GetProperty("tolerance_seconds").GetDouble();
        Vectors = root.GetProperty("vectors").EnumerateArray().Select(v => new Vector(
            v.GetProperty("name").GetString()!,
            v.GetProperty("secret").GetString()!,
            v.GetProperty("header").GetString()!,
            v.GetProperty("body").GetString()!,
            v.GetProperty("now").GetInt64(),
            v.GetProperty("expect"))).ToList();
    }

    public static IEnumerable<object[]> VectorNames => Vectors.Select(v => new object[] { v.Name });

    private static WebhookErrorCode CodeOf(string name) => name switch
    {
        "malformed_header" => WebhookErrorCode.MalformedHeader,
        "bad_signature" => WebhookErrorCode.BadSignature,
        "stale" => WebhookErrorCode.Stale,
        "malformed_body" => WebhookErrorCode.MalformedBody,
        "invalid_secret" => WebhookErrorCode.InvalidSecret,
        _ => throw new InvalidOperationException("unmapped error class " + name),
    };

    private static string Sign(string secret, long atSeconds, string body) => Sign(secret, atSeconds.ToString(), Encoding.UTF8.GetBytes(body));

    private static string Sign(string secret, string t, string body) => Sign(secret, t, Encoding.UTF8.GetBytes(body));

    private static string Sign(string secret, string t, byte[] body)
    {
        var mac = HMACSHA256.HashData(Encoding.UTF8.GetBytes(secret), Encoding.ASCII.GetBytes(t + ".").Concat(body).ToArray());
        return $"t={t},v1={Convert.ToHexString(mac).ToLowerInvariant()}";
    }

    [Theory]
    [MemberData(nameof(VectorNames))]
    public void SharedVector(string name)
    {
        var v = Vectors.Single(x => x.Name == name);
        var now = DateTimeOffset.FromUnixTimeSeconds(v.Now);
        var tol = TimeSpan.FromSeconds(ToleranceSeconds);
        var bodyBytes = Encoding.UTF8.GetBytes(v.Body);

        // The string, byte and span-secret overloads must agree.
        WebhookEvent Run(int overload) => overload switch
        {
            0 => HanshoWebhook.Verify(v.Secret, v.Header, v.Body, now, tol),
            1 => HanshoWebhook.Verify(v.Secret, v.Header, bodyBytes, now, tol),
            _ => HanshoWebhook.Verify(Encoding.UTF8.GetBytes(v.Secret), v.Header, bodyBytes.AsSpan(), now, tol),
        };

        for (var overload = 0; overload < 3; overload++)
        {
            if (!v.Expect.GetProperty("ok").GetBoolean())
            {
                var want = CodeOf(v.Expect.GetProperty("error").GetString()!);
                var ex = Assert.Throws<WebhookException>(() => Run(overload));
                Assert.Equal(want, ex.Code);
                continue;
            }

            var ev = Run(overload);
            Assert.Equal(v.Expect.GetProperty("type").GetString(), ev.Type);
            Assert.Equal(v.Expect.GetProperty("id").GetString(), ev.Id);
            Assert.Equal(DateTimeOffset.FromUnixTimeSeconds(v.Expect.GetProperty("created_at").GetInt64()), ev.CreatedAt);
            Assert.Equal(bodyBytes, ev.Raw);
            var data = v.Expect.GetProperty("data");
            string S(string k) => data.GetProperty(k).GetString()!;
            switch (ev)
            {
                case PairingCreated e: Assert.Equal((S("pairing_id"), S("external_user_id"), S("device_name")), (e.PairingId, e.ExternalUserId, e.DeviceName)); break;
                case PairingRevoked e: Assert.Equal((S("pairing_id"), S("external_user_id"), S("by")), (e.PairingId, e.ExternalUserId, e.By)); break;
                case MessageDelivered e:
                    Assert.Equal((S("message_id"), S("external_user_id"), S("channel"), S("level")), (e.MessageId, e.ExternalUserId, e.Channel, e.Level.ToString()));
                    Assert.Equal(data.GetProperty("devices").GetInt32(), e.Devices);
                    break;
                case MessageUndeliverable e: Assert.Equal((S("message_id"), S("external_user_id"), S("channel"), S("reason")), (e.MessageId, e.ExternalUserId, e.Channel, e.Reason)); break;
                case MessageAcked e: Assert.Equal((S("message_id"), S("external_user_id"), S("device_id")), (e.MessageId, e.ExternalUserId, e.DeviceId)); break;
                case MessageDismissed e: Assert.Equal((S("message_id"), S("external_user_id"), S("device_id")), (e.MessageId, e.ExternalUserId, e.DeviceId)); break;
                case MessageExpired e:
                    Assert.Equal((S("message_id"), S("external_user_id"), S("channel")), (e.MessageId, e.ExternalUserId, e.Channel));
                    Assert.Equal(data.GetProperty("attempts").GetInt32(), e.Attempts);
                    break;
                case ActionTaken e:
                    Assert.Equal((S("message_id"), S("action_id"), S("device_id"), S("external_user_id"), S("text")), (e.MessageId, e.ActionId, e.DeviceId, e.ExternalUserId, e.Text));
                    break;
                case ActivityEnded e: Assert.Equal((S("activity_id"), S("external_user_id"), S("channel"), S("reason")), (e.ActivityId, e.ExternalUserId, e.Channel, e.Reason)); break;
                case UnknownWebhookEvent e:
                    Assert.Equal(v.Expect.GetProperty("name").GetString(), e.Name);
                    Assert.True(Json.Equal(data, e.Data));
                    break;
                default: Assert.Fail("unhandled event type " + ev.GetType().Name); break;
            }
            Assert.Equal(v.Expect.GetProperty("type").GetString() == "unknown", ev is UnknownWebhookEvent);
        }
    }

    [Fact]
    public void VectorsCoverEveryClassOfOutcome()
    {
        Assert.True(Vectors.Count >= 20);
        var classes = Vectors.Select(v => v.Expect.GetProperty("ok").GetBoolean() ? v.Expect.GetProperty("type").GetString()! : v.Expect.GetProperty("error").GetString()!).ToHashSet();
        foreach (var c in new[] { "stale", "bad_signature", "malformed_header", "unknown", "message.acked", "action" }) Assert.Contains(c, classes);
        var types = Vectors.Where(v => v.Expect.GetProperty("ok").GetBoolean()).Select(v => v.Expect.GetProperty("type").GetString()!).ToHashSet();
        foreach (var t in new[] { "pairing.created", "pairing.revoked", "message.delivered", "message.undeliverable", "message.acked", "message.dismissed", "message.expired", "action", "activity.ended" })
            Assert.Contains(t, types);
    }

    // --- replay protection ---------------------------------------------------

    private const string AckedBody = "{\"id\":\"1\",\"event\":\"message.acked\",\"created_at\":1,\"data\":{}}";
    private const long T0 = 1_700_000_000;

    private static Action Check(long offsetSeconds, TimeSpan? tolerance = null) =>
        () => HanshoWebhook.Verify("s", Sign("s", T0, AckedBody), AckedBody, DateTimeOffset.FromUnixTimeSeconds(T0 + offsetSeconds), tolerance);

    [Fact]
    public void AcceptsUpTo300SecondsEitherSideAndRejectsBeyond()
    {
        foreach (var o in new long[] { 0, 300, -300 }) Check(o)();
        foreach (var o in new long[] { 301, -301, 86400 })
            Assert.Equal(WebhookErrorCode.Stale, Assert.Throws<WebhookException>(Check(o)).Code);
        Assert.Equal(TimeSpan.FromSeconds(300), HanshoWebhook.DefaultTolerance);
    }

    [Fact]
    public void ToleranceIsConfigurableAndANonPositiveOneMeansTheDefault()
    {
        Check(301, TimeSpan.FromMinutes(10))();
        Assert.Throws<WebhookException>(Check(11, TimeSpan.FromSeconds(10)));
        Check(10, TimeSpan.FromSeconds(10))();
        Check(299, TimeSpan.Zero)();
        Assert.Throws<WebhookException>(Check(301, TimeSpan.Zero));
        Assert.Throws<WebhookException>(Check(301, TimeSpan.FromSeconds(-5)));
    }

    [Fact]
    public void DefaultsToTheClock()
    {
        var h = Sign("s", DateTimeOffset.UtcNow.ToUnixTimeSeconds(), AckedBody);
        Assert.Equal("message.acked", HanshoWebhook.Verify("s", h, AckedBody).Type);
        var old = Assert.Throws<WebhookException>(() => HanshoWebhook.Verify("s", Sign("s", T0, AckedBody), AckedBody));
        Assert.Equal(WebhookErrorCode.Stale, old.Code);
    }

    [Fact]
    public void AValidSignatureOverAnOverflowingTimestampIsStale()
    {
        var t = "99999999999999999999";
        var body = Encoding.UTF8.GetBytes(AckedBody);
        var ex = Assert.Throws<WebhookException>(() => HanshoWebhook.Verify(Encoding.UTF8.GetBytes("s"), Sign("s", t, body), body));
        Assert.Equal(WebhookErrorCode.Stale, ex.Code);
        // ... and a wrong signature over it is still a bad signature: the signature is checked first.
        Assert.Equal(WebhookErrorCode.BadSignature, Assert.Throws<WebhookException>(() => HanshoWebhook.Verify("other", Sign("s", t, body), body)).Code);
    }

    // --- raw body ----------------------------------------------------------

    [Fact]
    public void VerifiesTheRawBytesNotAReserialisedBody()
    {
        var valid = Vectors.Single(v => v.Name == "valid message.acked");
        var now = DateTimeOffset.FromUnixTimeSeconds(valid.Now);
        Assert.Equal("message.acked", HanshoWebhook.Verify(Secret, valid.Header, valid.Body, now).Type);
        Assert.Equal("message.acked", HanshoWebhook.Verify(Secret, valid.Header, Encoding.UTF8.GetBytes(valid.Body), now).Type);

        using var doc = JsonDocument.Parse(valid.Body);
        var pretty = JsonSerializer.Serialize(doc.RootElement, new JsonSerializerOptions { WriteIndented = true });
        foreach (var reserialised in new[] { pretty, valid.Body + "\n", valid.Body.Replace(":", ": ") })
            Assert.Equal(WebhookErrorCode.BadSignature, Assert.Throws<WebhookException>(() => HanshoWebhook.Verify(Secret, valid.Header, reserialised, now)).Code);
    }

    [Fact]
    public void VerifiesNonAsciiBodiesByteForByte()
    {
        var v = Vectors.Single(x => x.Name == "valid pairing.created");
        var ev = Assert.IsType<PairingCreated>(HanshoWebhook.Verify(Secret, v.Header, v.Body, DateTimeOffset.FromUnixTimeSeconds(v.Now)));
        Assert.Equal("Bas’s iPhone <Ünï>", ev.DeviceName);
    }

    [Fact]
    public void ExposesTheHeaderName() => Assert.Equal("X-Hansho-Webhook-Signature", HanshoWebhook.SignatureHeader);

    // --- secret, header, body ----------------------------------------------

    private static readonly string Now = DateTimeOffset.UtcNow.ToUnixTimeSeconds().ToString();
    private const string ActionBody = "{\"id\":\"1\",\"event\":\"action\",\"created_at\":1,\"data\":{}}";

    [Fact]
    public void AcceptsAByteSecretAndRefusesAnEmptyOne()
    {
        var body = Encoding.UTF8.GetBytes(ActionBody);
        Assert.Equal("action", HanshoWebhook.Verify(Encoding.UTF8.GetBytes("s"), Sign("s", Now, body), body).Type);
        Assert.Equal(WebhookErrorCode.InvalidSecret, Assert.Throws<WebhookException>(() => HanshoWebhook.Verify("", Sign("", Now, body), body)).Code);
        Assert.Equal(WebhookErrorCode.InvalidSecret, Assert.Throws<WebhookException>(() => HanshoWebhook.Verify(Array.Empty<byte>(), Sign("", Now, body), body)).Code);
    }

    [Fact]
    public void TreatsAMissingHeaderAsMalformed()
    {
        foreach (var header in new string?[] { null, "", "   " })
            Assert.Equal(WebhookErrorCode.MalformedHeader, Assert.Throws<WebhookException>(() => HanshoWebhook.Verify("s", header, ActionBody)).Code);
    }

    [Fact]
    public void RejectsAValidSignatureOverABodyThatIsNotAnEnvelope()
    {
        foreach (var bad in new[] { "not json", "[]", "{\"id\":\"1\"}", "", "{\"event\":\"\"}", "{\"event\":5}", "null", "{" })
            Assert.Equal(WebhookErrorCode.MalformedBody, Assert.Throws<WebhookException>(() => HanshoWebhook.Verify("s", Sign("s", Now, bad), bad)).Code);
        var invalidUtf8 = new byte[] { (byte)'{', 0xff, 0xfe, (byte)'}' };
        Assert.Equal(WebhookErrorCode.MalformedBody, Assert.Throws<WebhookException>(() => HanshoWebhook.Verify(Encoding.UTF8.GetBytes("s"), Sign("s", Now, invalidUtf8), invalidUtf8)).Code);
    }

    [Fact]
    public void AcceptsUpperCaseHexAndSeveralSignatures()
    {
        var h = Sign("s", Now, ActionBody);
        var hex = h[(h.IndexOf("v1=", StringComparison.Ordinal) + 3)..];
        Assert.Equal("action", HanshoWebhook.Verify("s", h.Replace(hex, hex.ToUpperInvariant()), ActionBody).Type);
        var zeros = new string('0', 64);
        Assert.Equal("action", HanshoWebhook.Verify("s", $"t={Now},v1={zeros},v1={hex}", ActionBody).Type);
        Assert.Equal("action", HanshoWebhook.Verify("s", $" v1={hex} , t={Now} ", ActionBody).Type);
        foreach (var bad in new[] { $"t={Now},v1={hex[..63]}", $"t={Now},v1={hex}00", $"t={Now},v1={new string('z', 64)}" })
            Assert.Equal(WebhookErrorCode.BadSignature, Assert.Throws<WebhookException>(() => HanshoWebhook.Verify("s", bad, ActionBody)).Code);
    }

    // --- forward compatibility ---------------------------------------------

    [Fact]
    public void DeliversAnUnknownEventAsUnknownWithTheRawPayload()
    {
        var body = "{\"id\":7,\"event\":\"message.holographic\",\"created_at\":5,\"extra\":1,\"data\":{\"message_id\":\"m\",\"new_field\":[1,2]}}";
        var ev = Assert.IsType<UnknownWebhookEvent>(HanshoWebhook.Verify("s", Sign("s", Now, body), body));
        Assert.Equal("unknown", ev.Type);
        Assert.Equal("message.holographic", ev.Name);
        Assert.Equal("7", ev.Id);
        Assert.Equal(DateTimeOffset.FromUnixTimeSeconds(5), ev.CreatedAt);
        Assert.Equal("{\"message_id\":\"m\",\"new_field\":[1,2]}", ev.Data.GetRawText()); // still valid after verification returned
        Assert.Equal(Encoding.UTF8.GetBytes(body), ev.Raw);
    }

    [Fact]
    public void IgnoresFieldsItDoesNotKnowAndReadsMissingOnesAsEmpty()
    {
        var body = "{\"id\":\"1\",\"event\":\"message.expired\",\"created_at\":1,\"data\":{\"message_id\":\"m\",\"attempts\":\"3\",\"brand_new\":{}}}";
        var ev = Assert.IsType<MessageExpired>(HanshoWebhook.Verify("s", Sign("s", Now, body), body));
        Assert.Equal(("m", "", "", 3), (ev.MessageId, ev.ExternalUserId, ev.Channel, ev.Attempts));
        var action = "{\"id\":\"1\",\"event\":\"action\",\"created_at\":1,\"data\":{\"action_id\":\"a\",\"text\":5}}";
        Assert.Null(Assert.IsType<ActionTaken>(HanshoWebhook.Verify("s", Sign("s", Now, action), action)).Text);
        var nodata = "{\"id\":\"1\",\"event\":\"message.acked\",\"created_at\":1}";
        Assert.Equal("", Assert.IsType<MessageAcked>(HanshoWebhook.Verify("s", Sign("s", Now, nodata), nodata)).MessageId);
    }

    [Fact]
    public void KeepsALevelItDoesNotKnow()
    {
        var body = "{\"id\":\"1\",\"event\":\"message.delivered\",\"created_at\":1,\"data\":{\"level\":\"emergency\",\"devices\":2}}";
        var ev = Assert.IsType<MessageDelivered>(HanshoWebhook.Verify("s", Sign("s", Now, body), body));
        Assert.Equal("emergency", ev.Level.ToString());
        Assert.Equal(2, ev.Devices);
    }
}

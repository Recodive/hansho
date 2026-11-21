using System;
using System.Collections.Generic;
using System.Linq;
using System.Net.Http;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using Xunit;

namespace Hansho.Tests;

public class ClientTests
{
    private static readonly Channel ChannelDef = new() { Name = "Ops", DefaultLevel = Level.Active, MaxLevel = Level.Critical };
    private static readonly Message Msg = new() { ExternalUserId = "u", Channel = "c", Title = "t", Body = "b" };

    [Fact]
    public async Task SendsHeadersAndUsesTheDefaultUserAgent()
    {
        using var hub = FakeHub.Start();
        await hub.Client.PutChannelAsync("ops", ChannelDef);
        var r = hub.Requests[0];
        Assert.Equal("Bearer " + FakeHub.Key, r.Headers["Authorization"]);
        Assert.Equal("application/json", r.Headers["Accept"]);
        Assert.Equal("application/json", r.ContentType);
        Assert.StartsWith("hansho-dotnet/", r.Headers["User-Agent"]);

        using var custom = hub.NewClient(o => o.UserAgent = "acme/2");
        await custom.EndActivityAsync("a");
        Assert.Equal("acme/2", hub.Requests[1].Headers["User-Agent"]);
        Assert.Null(hub.Requests[1].ContentType);
    }

    [Fact]
    public async Task KeepsAPathPrefixAndIgnoresATrailingSlash()
    {
        using var hub = FakeHub.Start();
        using var c = new HanshoClient(hub.Url + "/hansho/", FakeHub.Key);
        await c.GetMessageAsync("m1");
        Assert.Equal("/hansho/api/v1/messages/m1", hub.Requests[0].RawUrl);
    }

    [Fact]
    public void RequiresABaseUrlAndAnApiKey()
    {
        Assert.Throws<ArgumentException>(() => new HanshoClient("", "k"));
        Assert.Throws<ArgumentException>(() => new HanshoClient("not a url", "k"));
        Assert.Throws<ArgumentException>(() => new HanshoClient("ftp://hub.example.com", "k"));
        Assert.Throws<ArgumentException>(() => new HanshoClient("http://x", ""));
    }

    [Fact]
    public void ReportsTheSdkVersionOfTheAssembly()
    {
        Assert.Equal(HanshoClient.Version, typeof(HanshoClient).Assembly.GetName().Version!.ToString(3));
        Assert.Equal("hansho", HanshoClient.Name);
    }

    [Fact]
    public void NeverPrintsTheKey()
    {
        using var c = new HanshoClient("http://x", FakeHub.Key);
        Assert.DoesNotContain(FakeHub.Key, c.ToString());
        Assert.DoesNotContain(FakeHub.Key, new HanshoClientOptions { BaseUrl = "http://x", ApiKey = FakeHub.Key }.ToString());
        Assert.DoesNotContain(FakeHub.Key, JsonSerializer.Serialize(c));
    }

    [Fact]
    public async Task OmitsUnsetOptionalFieldsFromTheWire()
    {
        using var hub = FakeHub.Start();
        await hub.Client.SendMessageAsync(Msg);
        Assert.Equal(new[] { "external_user_id", "channel", "title", "body" }.OrderBy(x => x), PropertyNames(hub.Requests[0]).OrderBy(x => x));
        await hub.Client.PutChannelAsync("ops", ChannelDef);
        Assert.Equal(new[] { "name", "default_level", "max_level" }.OrderBy(x => x), PropertyNames(hub.Requests[1]).OrderBy(x => x));
        await hub.Client.PutFeedAsync("f", new Feed { TemplateId = "t", Data = 1 });
        Assert.Equal(new[] { "template_id", "data" }.OrderBy(x => x), PropertyNames(hub.Requests[2]).OrderBy(x => x));
    }

    [Fact]
    public async Task SendsMaxRetriesZeroAsGiven()
    {
        using var hub = FakeHub.Start();
        await hub.Client.SendMessageAsync(Msg with { Level = Level.Page, Page = new PageOptions { MaxRetries = 0 } });
        Assert.Equal("{\"max_retries\":0}", Json.Parse(hub.Requests[0].BodyText).GetProperty("page").GetRawText());
    }

    [Fact]
    public async Task SendsDataExactlyAsGivenAndWithoutEscapingUnicode()
    {
        using var hub = FakeHub.Start();
        await hub.Client.SendMessageAsync(Msg with
        {
            Title = "Ünï <ok>",
            Data = new Dictionary<string, object?> { ["CamelKey"] = 1, ["snake_key"] = new { PascalProp = "x" }, ["nothing"] = null },
        });
        var body = Json.Parse(hub.Requests[0].BodyText);
        Assert.Equal("Ünï <ok>", body.GetProperty("title").GetString());
        Assert.Equal("{\"CamelKey\":1,\"snake_key\":{\"PascalProp\":\"x\"},\"nothing\":null}", body.GetProperty("data").GetRawText());
    }

    [Fact]
    public async Task AcceptsJsonElementAndJsonNodeAsData()
    {
        using var hub = FakeHub.Start();
        var el = Json.Parse("{\"a\":[1,2,{\"b\":null}]}");
        var node = System.Text.Json.Nodes.JsonNode.Parse("{\"c\":true}")!;
        await hub.Client.PutTemplateAsync("t", 1, el);
        await hub.Client.PutTemplateAsync("t", 2, node);
        await hub.Client.PutTemplateAsync("t", 3, node); // the same node twice: it is copied, not re-parented
        Assert.Equal("{\"a\":[1,2,{\"b\":null}]}", Json.Parse(hub.Requests[0].BodyText).GetProperty("body").GetRawText());
        Assert.Equal("{\"c\":true}", Json.Parse(hub.Requests[2].BodyText).GetProperty("body").GetRawText());
    }

    [Fact]
    public async Task UsesAnInjectedHttpClientWithoutChangingOrDisposingIt()
    {
        using var hub = FakeHub.Start();
        using var http = new HttpClient { Timeout = TimeSpan.FromSeconds(77) };
        var c = new HanshoClient(new HanshoClientOptions { BaseUrl = hub.Url, ApiKey = FakeHub.Key, HttpClient = http });
        await c.GetMessageAsync("m");
        c.Dispose();
        Assert.Equal(TimeSpan.FromSeconds(77), http.Timeout);
        Assert.Empty(http.DefaultRequestHeaders);
        Assert.Equal(HttpStatusCodeOk, (int)(await http.GetAsync(hub.Url + "/health")).StatusCode); // still usable
    }

    private const int HttpStatusCodeOk = 200;

    [Fact]
    public async Task IgnoresResponseFieldsItDoesNotKnow()
    {
        using var hub = FakeHub.Start((_, reply) =>
            reply.Body = "{\"message_id\":\"m\",\"status\":\"delivered\",\"level\":\"page\",\"attempts\":1,\"deliveries\":[{\"device_id\":\"d\",\"status\":\"sent\",\"brand_new\":{\"x\":[1]}}],\"brand_new\":\"v\",\"acked_at\":\"2026-11-08T12:05:00Z\"}");
        var ms = await hub.Client.GetMessageAsync("m");
        Assert.Equal("m", ms.MessageId);
        Assert.Single(ms.Deliveries);
        Assert.Null(ms.Deliveries[0].Error);
    }

    [Fact]
    public async Task KeepsALevelThisSdkDoesNotKnow()
    {
        using var hub = FakeHub.Start((_, reply) => reply.Body = "{\"message_id\":\"m\",\"level\":\"emergency\",\"devices\":1,\"status\":\"queued\"}");
        var res = await hub.Client.SendMessageAsync(Msg);
        Assert.Equal("emergency", res.Level.ToString());
    }

    [Fact]
    public async Task ToleratesAnEmptyOrNonObjectSuccessBody()
    {
        foreach (var body in new[] { "", "null", "[]", "42" })
        {
            using var hub = FakeHub.Start((_, reply) => reply.Body = body);
            await hub.Client.PutChannelAsync("ops", ChannelDef);
            await hub.Client.EndActivityAsync("a");
        }
    }

    [Fact]
    public async Task AnInvalidSuccessBodyIsANetworkError()
    {
        using var hub = FakeHub.Start((_, reply) => reply.Body = "{not json");
        await Assert.ThrowsAsync<HanshoNetworkException>(() => hub.Client.GetMessageAsync("m"));
    }

    // --- retries -----------------------------------------------------------

    [Fact]
    public async Task RetriesPutOn429AndHonoursRetryAfter()
    {
        var n = 0;
        using var hub = FakeHub.Start((_, reply) =>
        {
            if (Interlocked.Increment(ref n) <= 2) { reply.Status = 429; reply.Headers["Retry-After"] = "2"; reply.Body = "{\"error\":\"rate_limited\"}"; }
        });
        await hub.Client.PutChannelAsync("ops", ChannelDef);
        Assert.Equal(3, hub.Requests.Count);
        Assert.Equal(new[] { TimeSpan.FromSeconds(2), TimeSpan.FromSeconds(2) }, hub.Waits);
    }

    [Fact]
    public async Task UnderstandsFractionalSecondsAndAnHttpDateRetryAfter()
    {
        var n = 0;
        using var hub = FakeHub.Start((_, reply) =>
        {
            var i = Interlocked.Increment(ref n);
            if (i == 1) { reply.Status = 429; reply.Headers["Retry-After"] = "1.5"; }
            if (i == 2) { reply.Status = 429; reply.Headers["Retry-After"] = DateTimeOffset.UtcNow.AddSeconds(30).ToString("R"); }
        });
        await hub.Client.DeleteChannelAsync("ops");
        Assert.Equal(2, hub.Waits.Count);
        Assert.Equal(TimeSpan.FromMilliseconds(1500), hub.Waits[0]);
        Assert.InRange(hub.Waits[1].TotalSeconds, 20, 31);
    }

    [Fact]
    public async Task StopsAfterMaxRetriesAndSurfacesTheError()
    {
        using var hub = FakeHub.Start((_, reply) => { reply.Status = 429; reply.Headers["Retry-After"] = "1"; reply.Body = "{\"error\":\"rate_limited\"}"; });
        var err = await Assert.ThrowsAsync<HanshoApiException>(() => hub.Client.PutChannelAsync("ops", ChannelDef));
        Assert.Equal(429, err.Status);
        Assert.Equal(TimeSpan.FromSeconds(1), err.RetryAfter);
        Assert.Equal(4, hub.Requests.Count);

        using var none = hub.NewClient(o => o.MaxRetries = 0);
        await Assert.ThrowsAsync<HanshoApiException>(() => none.PutChannelAsync("ops", ChannelDef));
        Assert.Equal(5, hub.Requests.Count);

        using var one = hub.NewClient(o => o.MaxRetries = 1);
        await Assert.ThrowsAsync<HanshoApiException>(() => one.PutChannelAsync("ops", ChannelDef));
        Assert.Equal(7, hub.Requests.Count);
    }

    [Fact]
    public async Task RetriesGetAndDeleteOn503WithJitteredBackoff()
    {
        var seen = new HashSet<string>();
        using var hub = FakeHub.Start((r, reply) =>
        {
            lock (seen) if (seen.Add(r.Method + " " + r.RawUrl)) { reply.Status = 503; reply.Body = "{\"error\":\"unavailable\"}"; }
        });
        await hub.Client.GetChannelAsync("x");
        await hub.Client.DeleteChannelAsync("x");
        await hub.Client.EndActivityAsync("x");
        Assert.Equal(3, hub.Waits.Count);
        foreach (var w in hub.Waits) Assert.InRange(w.TotalMilliseconds, 125, 250);
    }

    [Fact]
    public async Task BackoffGrowsAndIsCapped()
    {
        using var hub = FakeHub.Start((_, reply) => reply.Status = 503);
        using var c = hub.NewClient(o => o.MaxRetries = 6);
        await Assert.ThrowsAsync<HanshoApiException>(() => c.GetChannelAsync("x"));
        var w = hub.Waits.Select(t => t.TotalMilliseconds).ToArray();
        Assert.Equal(6, w.Length);
        double[] max = { 250, 500, 1000, 2000, 4000, 8000 };
        for (var i = 0; i < w.Length; i++) Assert.InRange(w[i], max[i] / 2, max[i]);
    }

    [Fact]
    public async Task ARetryAfterOfZeroFallsBackToBackoff()
    {
        var n = 0;
        using var hub = FakeHub.Start((_, reply) =>
        {
            if (Interlocked.Increment(ref n) == 1) { reply.Status = 429; reply.Headers["Retry-After"] = "0"; }
        });
        await hub.Client.GetChannelAsync("x");
        Assert.InRange(Assert.Single(hub.Waits).TotalMilliseconds, 125, 250);
    }

    [Fact]
    public async Task DoesNotRetryStatusesOtherThan429And503()
    {
        foreach (var status in new[] { 400, 401, 404, 409, 500, 502 })
        {
            using var hub = FakeHub.Start((_, reply) => reply.Status = status);
            await Assert.ThrowsAsync<HanshoApiException>(() => hub.Client.PutChannelAsync("ops", ChannelDef));
            Assert.Single(hub.Requests);
        }
    }

    [Fact]
    public async Task SurfacesRateLimitsOnPostMessagesUntouched()
    {
        foreach (var status in new[] { 429, 503 })
        {
            using var hub = FakeHub.Start((_, reply) => { reply.Status = status; reply.Headers["Retry-After"] = "3"; reply.Body = "{\"error\":\"rate_limited\"}"; });
            var err = await Assert.ThrowsAsync<HanshoApiException>(() => hub.Client.SendMessageAsync(Msg));
            Assert.Equal(status, err.Status);
            Assert.Equal("rate_limited", err.Code);
            Assert.Equal(TimeSpan.FromSeconds(3), err.RetryAfter);
            Assert.Single(hub.Requests);
            Assert.Empty(hub.Waits);
        }
    }

    [Fact]
    public async Task RetriesAMessageWithAnIdempotencyKeyWithoutLeakingItIntoTheBody()
    {
        var n = 0;
        using var hub = FakeHub.Start((_, reply) =>
        {
            if (Interlocked.Increment(ref n) == 1) { reply.Status = 429; reply.Headers["Retry-After"] = "1"; }
            else { reply.Status = 202; reply.Body = "{\"message_id\":\"m1\",\"status\":\"queued\"}"; }
        });
        var res = await hub.Client.SendMessageAsync(Msg with { IdempotencyKey = "order-7" });
        Assert.Equal("m1", res.MessageId);
        Assert.Equal(2, hub.Requests.Count);
        foreach (var r in hub.Requests)
        {
            Assert.Equal("order-7", r.Headers["Idempotency-Key"]);
            Assert.DoesNotContain("order-7", r.BodyText);
        }
        Assert.Equal(hub.Requests[0].Body, hub.Requests[1].Body);
    }

    [Fact]
    public async Task SendsNoIdempotencyKeyHeaderByDefault()
    {
        using var hub = FakeHub.Start();
        await hub.Client.SendMessageAsync(Msg);
        Assert.Null(hub.Requests[0].Headers["Idempotency-Key"]);
    }

    [Fact]
    public async Task DoesNotRetryPatch()
    {
        using var hub = FakeHub.Start((_, reply) => reply.Status = 429);
        await Assert.ThrowsAsync<HanshoApiException>(() => hub.Client.UpdateActivityAsync("a", new ActivityUpdate { Data = 1 }));
        Assert.Single(hub.Requests);
    }

    [Fact]
    public async Task SurfacesARetryAfterBeyondTheCap()
    {
        using var hub = FakeHub.Start((_, reply) => { reply.Status = 429; reply.Headers["Retry-After"] = "3600"; });
        var err = await Assert.ThrowsAsync<HanshoApiException>(() => hub.Client.PutChannelAsync("ops", ChannelDef));
        Assert.Equal(TimeSpan.FromHours(1), err.RetryAfter);
        Assert.Single(hub.Requests);
        Assert.Empty(hub.Waits);
    }

    [Fact]
    public async Task ResendsTheSameSoundBytesWhenRetrying()
    {
        var n = 0;
        using var hub = FakeHub.Start((_, reply) => { if (Interlocked.Increment(ref n) == 1) reply.Status = 503; });
        var bytes = Wav.Tiny();
        await hub.Client.PutSoundAsync("ding", "audio/wav", bytes);
        Assert.Equal(2, hub.Requests.Count);
        Assert.Equal(bytes, hub.Requests[0].Body);
        Assert.Equal(bytes, hub.Requests[1].Body);
        Assert.Equal("audio/wav", hub.Requests[1].ContentType);
    }

    // --- path escaping -----------------------------------------------------

    public static IEnumerable<object[]> TrickyIds() =>
        new[] { "a/b", "a?b=c", "a b", "ü", "日本", "a#b", "100%", "a/../b", "a%2Fb", "%2e%2e", "a;b", "a'(b)*!" }.Select(s => new object[] { s });

    [Theory]
    [MemberData(nameof(TrickyIds))]
    public async Task EscapesIdsOnTheWire(string id)
    {
        using var hub = FakeHub.Start();
        var c = hub.Client;
        var cases = new (string Prefix, Func<Task> Call)[]
        {
            ("/api/v1/channels/", () => c.PutChannelAsync(id, ChannelDef)),
            ("/api/v1/channels/", () => c.GetChannelAsync(id)),
            ("/api/v1/channels/", () => c.DeleteChannelAsync(id)),
            ("/api/v1/templates/", () => c.PutTemplateAsync(id, 1, new { })),
            ("/api/v1/sounds/", () => c.PutSoundAsync(id, "audio/wav", new byte[] { 1 })),
            ("/api/v1/feeds/", () => c.PutFeedAsync(id, new Feed { TemplateId = "t", Data = 1 })),
            ("/api/v1/messages/", () => c.GetMessageAsync(id)),
            ("/api/v1/activities/", () => c.UpdateActivityAsync(id, new ActivityUpdate { Data = 1 })),
            ("/api/v1/activities/", () => c.EndActivityAsync(id)),
        };
        foreach (var (prefix, call) in cases)
        {
            var before = hub.Requests.Count;
            await call();
            var target = hub.Requests[before].RawUrl;
            Assert.StartsWith(prefix, target);
            var seg = target[prefix.Length..];
            Assert.DoesNotContain('/', seg);
            Assert.DoesNotContain('?', seg);
            Assert.DoesNotContain('#', seg);
            Assert.DoesNotContain(' ', seg);
            Assert.Equal(id, Uri.UnescapeDataString(seg));
        }
        var b = hub.Requests.Count;
        await c.UnpairAsync(id);
        var m = System.Text.RegularExpressions.Regex.Match(hub.Requests[b].RawUrl, @"^/api/v1/users/([^/?#]+)/pairings$");
        Assert.True(m.Success, hub.Requests[b].RawUrl);
        Assert.Equal(id, Uri.UnescapeDataString(m.Groups[1].Value));
    }

    [Fact]
    public async Task ProducesTheExactExpectedEncoding()
    {
        using var hub = FakeHub.Start();
        await hub.Client.PutChannelAsync("ü x/y?z", ChannelDef);
        Assert.Equal("/api/v1/channels/%C3%BC%20x%2Fy%3Fz", hub.Requests[0].RawUrl);
    }

    [Fact]
    public async Task RejectsDotSegmentsAndEmptyIdsBeforeSending()
    {
        using var hub = FakeHub.Start();
        var c = hub.Client;
        foreach (var bad in new[] { "", ".", ".." })
        {
            await Assert.ThrowsAsync<ArgumentException>(() => c.GetChannelAsync(bad));
            await Assert.ThrowsAsync<ArgumentException>(() => c.UnpairAsync(bad));
            await Assert.ThrowsAsync<ArgumentException>(() => c.EndActivityAsync(bad));
            await Assert.ThrowsAsync<ArgumentException>(() => c.PutFeedAsync(bad, new Feed { TemplateId = "t", Data = 1 }));
        }
        await Assert.ThrowsAsync<ArgumentException>(() => c.CreatePairingTicketAsync(""));
        Assert.Empty(hub.Requests);
    }

    [Fact]
    public async Task RejectsASoundOverOneMegabyte()
    {
        using var hub = FakeHub.Start();
        await hub.Client.PutSoundAsync("ok", "audio/wav", new byte[1 << 20]);
        await Assert.ThrowsAsync<ArgumentException>(() => hub.Client.PutSoundAsync("big", "audio/wav", new byte[(1 << 20) + 1]));
        Assert.Single(hub.Requests);
    }

    // --- cancellation and errors -------------------------------------------

    [Fact]
    public async Task CancellationIsANetworkErrorNotAnApiError()
    {
        using var server = new SilentServer();
        using var c = new HanshoClient(server.Url, FakeHub.Key);
        using var cts = new CancellationTokenSource(TimeSpan.FromMilliseconds(50));
        var started = DateTime.UtcNow;
        var err = await Assert.ThrowsAsync<HanshoNetworkException>(() => c.SendMessageAsync(Msg, cts.Token));
        Assert.True(err.IsCancellation);
        Assert.False(err.IsTimeout);
        Assert.IsAssignableFrom<OperationCanceledException>(err.InnerException);
        Assert.True(DateTime.UtcNow - started < TimeSpan.FromSeconds(5));

        var pre = await Assert.ThrowsAsync<HanshoNetworkException>(() => c.GetChannelAsync("x", new CancellationToken(true)));
        Assert.True(pre.IsCancellation);
    }

    [Fact]
    public async Task TimesOutPerAttempt()
    {
        using var server = new SilentServer();
        using var c = new HanshoClient(new HanshoClientOptions { BaseUrl = server.Url, ApiKey = FakeHub.Key, Timeout = TimeSpan.FromMilliseconds(80) });
        var err = await Assert.ThrowsAsync<HanshoNetworkException>(() => c.SendMessageAsync(Msg));
        Assert.True(err.IsTimeout);
        Assert.False(err.IsCancellation);
    }

    [Fact]
    public async Task CancelsDuringTheRetryWait()
    {
        using var hub = FakeHub.Start((_, reply) => { reply.Status = 429; reply.Headers["Retry-After"] = "30"; });
        using var c = new HanshoClient(new HanshoClientOptions { BaseUrl = hub.Url, ApiKey = FakeHub.Key }); // the real delay
        using var cts = new CancellationTokenSource(TimeSpan.FromMilliseconds(150));
        var started = DateTime.UtcNow;
        var err = await Assert.ThrowsAsync<HanshoNetworkException>(() => c.PutChannelAsync("ops", ChannelDef, cts.Token));
        Assert.True(err.IsCancellation);
        Assert.True(DateTime.UtcNow - started < TimeSpan.FromSeconds(5));
        Assert.Single(hub.Requests);
    }

    [Fact]
    public async Task ReportsNetworkErrorsApartFromApiErrorsAndWithoutTheKey()
    {
        var hub = FakeHub.Start();
        var url = hub.Url;
        hub.Dispose();
        using var c = new HanshoClient(url, FakeHub.Key);
        var err = await Assert.ThrowsAsync<HanshoNetworkException>(() => c.PutChannelAsync("ops", ChannelDef));
        Assert.IsNotType<HanshoApiException>(err);
        Assert.DoesNotContain(FakeHub.Key, err.Message);
        Assert.DoesNotContain(FakeHub.Key, err.ToString());
        Assert.NotNull(err.InnerException);
        Assert.StartsWith("hansho: PUT /api/v1/channels/ops: ", err.Message);
        Assert.IsAssignableFrom<HanshoException>(err);
    }

    [Fact]
    public async Task NeverLetsTheKeyIntoErrors()
    {
        using var hub = FakeHub.Start((r, reply) =>
        {
            reply.Status = 401;
            reply.Body = JsonSerializer.Serialize(new { error = "unauthorized", message = "bad key " + r.Headers["Authorization"] });
        });
        var err = await Assert.ThrowsAsync<HanshoApiException>(() => hub.Client.PutChannelAsync("ops", ChannelDef));
        Assert.DoesNotContain(FakeHub.Key, err.Message);
        Assert.DoesNotContain(FakeHub.Key, err.Detail);
        Assert.DoesNotContain(FakeHub.Key, err.ToString());
        Assert.Contains("[redacted]", err.Detail);
    }

    [Fact]
    public async Task NeverPutsRequestOrResponseBodiesIntoTheMessage()
    {
        using var hub = FakeHub.Start((_, reply) => { reply.Status = 400; reply.Body = "{\"error\":\"invalid_request\",\"field\":\"title\",\"message\":\"secret detail\"}"; });
        var err = await Assert.ThrowsAsync<HanshoApiException>(() => hub.Client.SendMessageAsync(Msg with { Body = "private body text" }));
        Assert.DoesNotContain("private body text", err.Message);
        Assert.DoesNotContain("secret detail", err.Message);
        Assert.Equal("secret detail", err.Detail);
    }

    [Fact]
    public void FormatsApiErrorMessages()
    {
        var e = new HanshoApiException("POST", "/api/v1/messages", 400, "invalid_request", "title");
        Assert.Equal("hansho: POST /api/v1/messages: 400 invalid_request (field title)", e.Message);
        Assert.Equal("hansho: GET /x: 429 rate_limited (retry after 7000 ms)", new HanshoApiException("GET", "/x", 429, "rate_limited", retryAfter: TimeSpan.FromSeconds(7)).Message);
        Assert.Equal("hansho: GET /x: 502", new HanshoApiException("GET", "/x", 502).Message);
        Assert.IsAssignableFrom<Exception>(e);
    }

    private static IEnumerable<string> PropertyNames(Recorded r) =>
        Json.Parse(r.BodyText).EnumerateObject().Select(p => p.Name).ToList();
}

using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using Xunit;

namespace Hansho.Tests;

/// <summary>Skipped unless HANSHO_HUB_URL is set; sdk/scripts/run-integration.sh starts the real hub and sets it.</summary>
public sealed class IntegrationFactAttribute : FactAttribute
{
    public IntegrationFactAttribute()
    {
        if (string.IsNullOrEmpty(Environment.GetEnvironmentVariable("HANSHO_HUB_URL")))
            Skip = "HANSHO_HUB_URL is not set; run sdk/scripts/run-integration.sh";
    }
}

/// <summary>The same end-to-end flow as the Node and Go suites, against the real hub binary.</summary>
public class IntegrationTests
{
    private static string Env(string name) =>
        Environment.GetEnvironmentVariable(name) is { Length: > 0 } v ? v : throw new InvalidOperationException(name + " is not set");

    private static string FakeDevice(params string[] args)
    {
        var psi = new ProcessStartInfo(Env("HANSHO_FAKEDEVICE_BIN")) { RedirectStandardOutput = true, RedirectStandardError = true, UseShellExecute = false };
        foreach (var a in args) psi.ArgumentList.Add(a);
        using var p = Process.Start(psi)!;
        var stdout = p.StandardOutput.ReadToEndAsync();
        var stderr = p.StandardError.ReadToEndAsync();
        if (!p.WaitForExit(30_000)) { p.Kill(true); throw new TimeoutException("fakedevice " + args[0] + " timed out"); }
        if (p.ExitCode != 0) throw new InvalidOperationException($"fakedevice {args[0]} failed ({p.ExitCode}): {stderr.Result}");
        return stdout.Result;
    }

    /// <summary>Receives the hub's webhooks on HANSHO_WEBHOOK_ADDR and verifies each with the SDK's own verifier.</summary>
    internal sealed class Receiver : IDisposable
    {
        private HttpListener _listener = null!;
        private readonly List<WebhookEvent> _events = new();
        private int _rejected;

        public int Rejected => Volatile.Read(ref _rejected);

        public Receiver(string addr, string secret)
        {
            var i = addr.LastIndexOf(':');
            var host = addr[..i];
            if (host is "" or "0.0.0.0" or "[::]" or "::") host = "+";
            var prefix = $"http://{host}:{addr[(i + 1)..]}/";
            // The previous suite's receiver may still be closing. A listener whose Start() failed is
            // unusable, so every attempt gets a new one.
            for (var n = 0; ; n++)
            {
                var l = new HttpListener();
                l.Prefixes.Add(prefix);
                try { l.Start(); _listener = l; break; }
                catch (HttpListenerException) when (n < 50) { l.Close(); Thread.Sleep(100); }
            }
            _ = Task.Run(() => Loop(secret));
        }

        private async Task Loop(string secret)
        {
            while (_listener.IsListening)
            {
                HttpListenerContext ctx;
                try { ctx = await _listener.GetContextAsync(); }
                catch (Exception) { return; }
                try
                {
                    using var ms = new MemoryStream();
                    await ctx.Request.InputStream.CopyToAsync(ms);
                    try
                    {
                        var ev = HanshoWebhook.Verify(secret, ctx.Request.Headers[HanshoWebhook.SignatureHeader], ms.ToArray());
                        lock (_events) _events.Add(ev);
                        ctx.Response.StatusCode = 200;
                    }
                    catch (WebhookException)
                    {
                        Interlocked.Increment(ref _rejected);
                        ctx.Response.StatusCode = 400;
                    }
                    ctx.Response.Close();
                }
                catch (Exception) { /* the hub went away */ }
            }
        }

        public async Task<T> Wait<T>(Func<T, bool> match) where T : WebhookEvent
        {
            var deadline = DateTime.UtcNow.AddSeconds(25);
            while (DateTime.UtcNow < deadline)
            {
                lock (_events)
                    if (_events.OfType<T>().FirstOrDefault(match) is { } hit) return hit;
                await Task.Delay(100);
            }
            throw new TimeoutException($"no {typeof(T).Name} webhook arrived");
        }

        public int Count { get { lock (_events) return _events.Count; } }

        public void Dispose() { try { _listener.Close(); } catch (ObjectDisposedException) { } }
    }

    private static async Task<MessageStatus> WaitStatus(HanshoClient c, string id, params string[] want)
    {
        var deadline = DateTime.UtcNow.AddSeconds(25);
        var last = "";
        while (DateTime.UtcNow < deadline)
        {
            var ms = await c.GetMessageAsync(id);
            last = ms.Status;
            if (want.Contains(ms.Status)) return ms;
            await Task.Delay(200);
        }
        throw new TimeoutException($"message {id}: status {last}, wanted one of {string.Join(", ", want)}");
    }

    private static async Task ApiError(Task call, int status, string code, string? field = null)
    {
        var e = await Assert.ThrowsAsync<HanshoApiException>(() => call);
        Assert.Equal(status, e.Status);
        Assert.Equal(code, e.Code);
        if (field is not null) Assert.Equal(field, e.Field);
    }

    [IntegrationFact]
    [Trait("Category", "Integration")]
    public async Task RunsTheWholeFlow()
    {
        var hubUrl = Env("HANSHO_HUB_URL");
        using var rx = new Receiver(Env("HANSHO_WEBHOOK_ADDR"), Env("HANSHO_WEBHOOK_SECRET"));
        using var c = new HanshoClient(hubUrl, Env("HANSHO_API_KEY"));

        const string p = "dotnet";
        var user = $"u-{p} ü/42?x";
        string ch = $"{p}-ops", tpl = $"{p}-status", snd = $"{p}-ding";
        var stateDir = Directory.CreateTempSubdirectory("hansho-dotnet-it-");
        var statePath = Path.Combine(stateDir.FullName, "device.json");
        try
        {
            // errors
            using (var bad = new HanshoClient(hubUrl, "ck_not_a_key"))
                await ApiError(bad.CreatePairingTicketAsync("x"), 401, "unauthorized");
            await ApiError(c.GetChannelAsync("no-such-channel"), 404, "unknown_channel");
            await ApiError(c.SendMessageAsync(new Message { ExternalUserId = user, Channel = ch, Title = "", Body = "b" }), 400, "invalid_request", "title");

            // catalog
            var sound = await c.PutSoundAsync(snd, "audio/wav", Wav.Tiny());
            Assert.Equal(snd, sound.Id);
            var template = new Dictionary<string, object>
            {
                ["type"] = "vstack",
                ["tint"] = new { light = "#0A84FF", dark = "#64D2FF" },
                ["children"] = new object[]
                {
                    new { type = "text", style = "headline", text = "{{title}}" },
                    new { type = "spacer" },
                },
            };
            await c.PutTemplateAsync(tpl, 1, template);
            await ApiError(c.PutTemplateAsync(tpl, 1, template), 409, "version_conflict");
            await c.PutTemplateAsync(tpl, 2, template);
            await c.PutChannelAsync(ch, new Channel
            {
                Name = $"Operations {p}",
                DefaultLevel = Level.Active,
                MaxLevel = Level.Page,
                Sound = snd,
                TemplateId = tpl,
                Thread = "ops",
                Actions = new[] { new MessageAction { Id = "ack", Title = "Acknowledge", Ack = true } },
            });
            var got = await c.GetChannelAsync(ch);
            Assert.Equal($"Operations {p}", got.Name);
            Assert.Equal(Level.Page, got.MaxLevel);
            Assert.Equal(snd, got.Sound);
            Assert.Equal(tpl, got.TemplateId);
            var action = Assert.Single(got.Actions!);
            Assert.Equal(("ack", "Acknowledge", (bool?)true), (action.Id, action.Title, action.Ack));
            await ApiError(c.PutChannelAsync("bad id", new Channel { Name = "n", DefaultLevel = Level.Active, MaxLevel = Level.Page }), 400, "invalid_request", "id");

            // pairing
            var tk = await c.CreatePairingTicketAsync(user);
            Assert.StartsWith("hansho://pair?", tk.Qr);
            Assert.Contains(tk.Ticket, tk.Qr);
            Assert.True(tk.ExpiresAt > DateTimeOffset.UtcNow);
            FakeDevice("pair", "-hub", hubUrl, "-ticket", tk.Ticket, "-state", statePath, "-name", $"SDK phone {p}");
            await rx.Wait<PairingCreated>(e => e.ExternalUserId == user && e.DeviceName == $"SDK phone {p}");

            // messages at every level
            foreach (var level in new[] { Level.Passive, Level.Active, Level.TimeSensitive, Level.Critical })
            {
                var res = await c.SendMessageAsync(new Message
                {
                    ExternalUserId = user,
                    Channel = ch,
                    Level = level,
                    Title = "Disk full",
                    Body = "db-1 is at 97%",
                    Data = new { disk = 97 },
                    Sound = snd,
                    CollapseId = $"disk-{level}",
                });
                Assert.Equal(level, res.Level);
                Assert.Equal(1, res.Devices);
                Assert.StartsWith("msg_", res.MessageId);
                var ms = await WaitStatus(c, res.MessageId, "delivered");
                Assert.Equal(level, ms.Level);
                var d = Assert.Single(ms.Deliveries);
                Assert.Equal("sent", d.Status);
                await rx.Wait<MessageDelivered>(e => e.MessageId == res.MessageId && e.Level == level && e.Devices == 1);
            }
            var def = await c.SendMessageAsync(new Message { ExternalUserId = user, Channel = ch, Title = "Default", Body = "b" });
            Assert.Equal(Level.Active, def.Level);

            // a message with an idempotency key goes through the same path
            var keyed = await c.SendMessageAsync(new Message { ExternalUserId = user, Channel = ch, Title = "Keyed", Body = "b", IdempotencyKey = "it-" + Guid.NewGuid().ToString("N") });
            Assert.StartsWith("msg_", keyed.MessageId);

            // page + acknowledgement from the (fake) device
            var page = await c.SendMessageAsync(new Message
            {
                ExternalUserId = user,
                Channel = ch,
                Level = Level.Page,
                Title = "Primary DB down",
                Body = "Failover failed",
                Page = new PageOptions { IntervalSeconds = 60, MaxRetries = 3 },
            });
            Assert.Equal(Level.Page, page.Level);
            await WaitStatus(c, page.MessageId, "paging");
            FakeDevice("ack", "-state", statePath, "-message", page.MessageId);
            var acked = await WaitStatus(c, page.MessageId, "acked");
            Assert.NotNull(acked.AckedAt);
            Assert.True(acked.AckedAt > DateTimeOffset.UtcNow.AddMinutes(-5));
            var ev = await rx.Wait<MessageAcked>(e => e.MessageId == page.MessageId);
            Assert.Equal(user, ev.ExternalUserId);
            await rx.Wait<ActionTaken>(e => e.MessageId == page.MessageId && e.ActionId == "ack");

            // live activity
            var started = await c.StartActivityAsync(new Activity
            {
                ExternalUserId = user,
                Channel = ch,
                TemplateId = tpl,
                Data = new { title = "Deploy", progress = 0.1 },
                Alert = new ActivityAlert { Title = "Deploy started" },
            });
            Assert.Equal(1, started.Devices);
            Assert.StartsWith("act_", started.ActivityId);
            FakeDevice("activity-token", "-state", statePath, "-activity", started.ActivityId);
            var up = await c.UpdateActivityAsync(started.ActivityId, new ActivityUpdate { Data = new { title = "Deploy", progress = 0.6 }, Priority = 10 });
            Assert.Equal(started.ActivityId, up.ActivityId);
            Assert.True(up.Update >= 1);
            await c.EndActivityAsync(started.ActivityId);
            await ApiError(c.UpdateActivityAsync(started.ActivityId, new ActivityUpdate { Data = new { title = "x" } }), 409, "activity_ended");

            // feeds
            var own = await c.PutFeedAsync($"{p}-uptime", new Feed { ExternalUserId = user, TemplateId = tpl, Data = new { title = "up", ms = 41 } });
            Assert.Equal($"{p}-uptime", own.FeedId);
            Assert.True(own.Devices >= 1);
            var shared = await c.PutFeedAsync($"{p}-shared", new Feed { TemplateId = tpl, Data = new { title = "all" } });
            Assert.True(shared.Devices >= 1);
            await ApiError(c.PutFeedAsync($"{p}-bad", new Feed { TemplateId = "missing", Data = 1 }), 400, "invalid_request", "template_id");

            // unpair, then nobody is left to deliver to
            await c.UnpairAsync(user);
            await rx.Wait<PairingRevoked>(e => e.ExternalUserId == user && e.By == "sender");
            var none = await c.SendMessageAsync(new Message { ExternalUserId = user, Channel = ch, Title = "Anyone?", Body = "b" });
            Assert.Equal(0, none.Devices);
            await WaitStatus(c, none.MessageId, "undeliverable");
            await rx.Wait<MessageUndeliverable>(e => e.MessageId == none.MessageId);
            await c.DeleteChannelAsync(ch);
            await ApiError(c.GetChannelAsync(ch), 404, "unknown_channel");

            Assert.Equal(0, rx.Rejected); // the SDK rejected a webhook the hub signed
            Assert.True(rx.Count > 0);
        }
        finally
        {
            stateDir.Delete(true);
        }
    }
}

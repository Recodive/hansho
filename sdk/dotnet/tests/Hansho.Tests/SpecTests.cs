using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using System.Threading.Tasks;
using Xunit;

namespace Hansho.Tests;

public class SpecTests
{
    private static readonly Channel SpecChannel = new()
    {
        Name = "Operations",
        DefaultLevel = Level.Active,
        MaxLevel = Level.Critical,
        Sound = "ding",
        TemplateId = "status",
        Thread = "ops-thread",
        Actions = new[]
        {
            new MessageAction { Id = "ack", Title = "Acknowledge", Ack = true },
            new MessageAction { Id = "reply", Title = "Reply", Kind = "text" },
            new MessageAction { Id = "runbook", Title = "Runbook", Kind = "url", Url = "https://example.com/runbook" },
        },
    };

    private static readonly byte[] WavBytes = Convert.FromBase64String("UklGRgAAAABXQVZF");

    private static readonly DateTimeOffset Expires = new(2026, 11, 8, 12, 10, 0, TimeSpan.Zero);
    private static readonly DateTimeOffset Acked = new(2026, 11, 8, 12, 5, 0, TimeSpan.Zero);
    private static readonly Channel ChannelOps = new() { Id = "ops", Name = "Operations", DefaultLevel = Level.Active, MaxLevel = Level.Critical, Sound = "", TemplateId = "", Thread = "ops", Actions = Array.Empty<MessageAction>() };
    private static readonly Template TemplateStatus = new() { Id = "status", Version = 2, Body = Json.Parse("{\"type\":\"text\",\"text\":\"{{title}}\"}") };
    private static readonly Sound SoundDing = new() { Id = "ding", ContentType = "audio/wav", Bytes = 12, Sha256 = "00" };
    private static readonly Group GroupOncall = new() { Id = "oncall", Members = new[] { "alice", "bob" } };
    private static readonly Audience AudienceOnly = new() { Mode = AudienceMode.Only, Users = new[] { "alice" }, Groups = new[] { "oncall" } };

    private static Delivery Queued(string device) => new() { DeviceId = device, Status = "queued" };

    /// <summary>The SDK call for each case of x-conformance in openapi.json, and the value it must return.</summary>
    private static readonly Dictionary<string, (Func<HanshoClient, Task<object?>> Call, object? Want)> Calls = new()
    {
        ["createPairingTicket"] = (async c => await c.CreatePairingTicketAsync("user-42"),
            new PairingTicket { Ticket = "tk_abc", Qr = "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=tk_abc&fp=ff00", ExpiresAt = Expires }),
        ["createPairingTicketWithName"] = (async c => await c.CreatePairingTicketAsync("user-42", "Zoë 日本語"),
            new PairingTicket { Ticket = "tk_def", Qr = "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=tk_def&fp=ff00", ExpiresAt = Expires }),
        ["setUserDisplayName"] = (async c => { await c.SetUserDisplayNameAsync("user-42", "Zoë 日本語"); return null; }, null),
        ["clearUserDisplayName"] = (async c => { await c.SetUserDisplayNameAsync("user 42", ""); return null; }, null),
        ["unpair"] = (async c => { await c.UnpairAsync("user-42"); return null; }, null),
        ["putChannel"] = (async c => { await c.PutChannelAsync("ops", SpecChannel); return null; }, null),
        ["getChannel"] = (async c => await c.GetChannelAsync("ops"), ChannelOps),
        ["listChannels"] = (async c => await c.ListChannelsAsync(), new[] { ChannelOps with { Audience = new Audience { Mode = AudienceMode.Everyone } } }),
        ["deleteChannel"] = (async c => { await c.DeleteChannelAsync("ops"); return null; }, null),
        ["putTemplate"] = (async c =>
        {
            await c.PutTemplateAsync("status", 2, new Dictionary<string, object>
            {
                ["type"] = "vstack",
                ["children"] = new[] { new Dictionary<string, object> { ["type"] = "text", ["style"] = "headline", ["text"] = "{{title}}" } },
            });
            return null;
        }, null),
        ["listTemplates"] = (async c => await c.ListTemplatesAsync(), new[] { TemplateStatus }),
        ["getTemplate"] = (async c => await c.GetTemplateAsync("status"), TemplateStatus),
        ["deleteTemplate"] = (async c => { await c.DeleteTemplateAsync("status"); return null; }, null),
        ["putSound"] = (async c => await c.PutSoundAsync("ding", "audio/wav", WavBytes), new SoundResult { Id = "ding", Filename = "ding.wav", Sha256 = "00", Size = 12 }),
        ["listSounds"] = (async c => await c.ListSoundsAsync(), new[] { SoundDing }),
        ["getSound"] = (async c => await c.GetSoundAsync("ding"), SoundDing),
        ["deleteSound"] = (async c => { await c.DeleteSoundAsync("ding"); return null; }, null),
        ["sendMessage"] = (async c => await c.SendMessageAsync(new Message
        {
            ExternalUserId = "user-42",
            Channel = "ops",
            Level = Level.TimeSensitive,
            Title = "Disk full",
            Subtitle = "db-1",
            Body = "db-1 is at 97%",
            ImageUrl = "https://example.com/i.png",
            TemplateId = "status",
            Data = new Dictionary<string, object> { ["disk"] = 97, ["nested"] = new Dictionary<string, object> { ["snake_case_key"] = new[] { 1, 2 } } },
            Sound = "ding",
            Actions = new[] { new MessageAction { Id = "ack", Title = "Ack", Ack = true } },
            CollapseId = "disk-db-1",
        }), new MessageResult { MessageId = "msg_1", Level = Level.TimeSensitive, Devices = 2, Status = "queued", Deliveries = new[] { Queued("dev_1"), Queued("dev_2") } }),
        ["sendPage"] = (async c => await c.SendMessageAsync(new Message
        {
            ExternalUserId = "user-42",
            Channel = "ops",
            Level = Level.Page,
            Title = "Primary DB down",
            Body = "Failover did not complete",
            Page = new PageOptions { IntervalSeconds = 60, MaxRetries = 3 },
        }), new MessageResult { MessageId = "msg_2", Level = Level.Page, Devices = 1, Status = "queued", Deliveries = new[] { Queued("dev_1") } }),
        ["sendPageEach"] = (async c => await c.SendMessageAsync(new Message
        {
            ExternalUserIds = new[] { "alice", "bob" },
            Channel = "ops",
            Level = Level.Page,
            Title = "Primary DB down",
            Body = "Failover did not complete",
            Page = new PageOptions { IntervalSeconds = 60, MaxRetries = 3, Ack = PageAck.Each },
        }), new MessageResult
        {
            Level = Level.Page,
            Recipients = new[]
            {
                new Recipient { ExternalUserId = "alice", MessageId = "msg_3", Status = "queued", Devices = 1, Deliveries = new[] { Queued("dev_1") } },
                new Recipient { ExternalUserId = "bob", MessageId = "msg_4", Status = "queued", Devices = 1, Deliveries = new[] { Queued("dev_2") } },
            },
        }),
        ["getMessageAckedNoName"] = (async c => await c.GetMessageAsync("msg_3"),
            new MessageStatus
            {
                MessageId = "msg_3", Status = "acked", Level = Level.Page, Attempts = 1, AckedAt = Acked, AckedBy = "alice",
                Deliveries = new[] { new Delivery { DeviceId = "dev_1", Status = "sent", Error = "" } },
            }),
        ["getMessage"] = (async c => await c.GetMessageAsync("msg_2"),
            new MessageStatus
            {
                MessageId = "msg_2", Status = "acked", Level = Level.Page, Attempts = 2, AckedAt = Acked, AckedBy = "user-42", AckedByName = "Zoë 日本語",
                Deliveries = new[]
                {
                    new Delivery { DeviceId = "dev_1", Status = "sent", Error = "" },
                    new Delivery { DeviceId = "dev_2", Status = "failed", Error = "unregistered" },
                },
            }),
        ["startActivity"] = (async c => await c.StartActivityAsync(new Activity
        {
            ExternalUserId = "user-42",
            Channel = "ops",
            TemplateId = "deploy",
            Data = new { progress = 0.1 },
            Alert = new ActivityAlert { Title = "Deploy started", Body = "v1.2.3" },
        }), new ActivityStarted { ActivityId = "act_1", Devices = 1 }),
        ["updateActivity"] = (async c => await c.UpdateActivityAsync("act_1", new ActivityUpdate
        {
            Data = new { progress = 0.6 },
            Priority = 10,
            Alert = new ActivityAlert { Title = "Halfway", Body = "60%" },
        }), new ActivityUpdated { ActivityId = "act_1", Update = 3 }),
        ["endActivity"] = (async c => { await c.EndActivityAsync("act_1"); return null; }, null),
        ["putFeed"] = (async c => await c.PutFeedAsync("uptime", new Feed
        {
            ExternalUserId = "user-42",
            TemplateId = "status",
            Title = "Uptime",
            Data = new Dictionary<string, object> { ["up"] = true, ["ms"] = 41 },
        }), new FeedResult { FeedId = "uptime", Devices = 2 }),
        ["getFeed"] = (async c => await c.GetFeedAsync("uptime", "user-42"),
            new FeedSettings
            {
                FeedId = "uptime", ExternalUserId = "user-42", TemplateId = "status", Title = "Uptime", Channel = "ops",
                Audience = new Audience { Mode = AudienceMode.Only, Users = new[] { "alice" } }, UpdatedAt = 1762603200,
            }),
        ["putGroup"] = (async c => await c.PutGroupAsync("oncall", new[] { "alice", "bob" }), GroupOncall),
        ["getGroup"] = (async c => await c.GetGroupAsync("oncall"), GroupOncall),
        ["listGroups"] = (async c => await c.ListGroupsAsync(), new[] { GroupOncall }),
        ["deleteGroup"] = (async c => { await c.DeleteGroupAsync("oncall"); return null; }, null),
        ["kick"] = (async c => { await c.KickAsync("user-42"); return null; }, null),
        ["putChannelWithAudience"] = (async c =>
        {
            await c.PutChannelAsync("ops", new Channel { Name = "Operations", DefaultLevel = Level.Active, MaxLevel = Level.Critical, Audience = AudienceOnly });
            return null;
        }, null),
        ["putFeedWithChannel"] = (async c => await c.PutFeedAsync("uptime", new Feed
        {
            TemplateId = "status",
            Channel = "ops",
            Audience = AudienceOnly,
            Data = new Dictionary<string, object> { ["up"] = true },
        }), new FeedResult { FeedId = "uptime", Devices = 1 }),
        ["sendToAudience"] = (async c => await c.SendMessageAsync(new Message
        {
            ToAudience = true,
            Channel = "ops",
            Title = "Deploy done",
            Body = "v1.2.3 is live",
        }), new MessageResult
        {
            Level = Level.Active,
            Recipients = new[]
            {
                new Recipient { ExternalUserId = "alice", MessageId = "msg_7", Status = "queued", Devices = 1, Deliveries = new[] { Queued("dev_1") } },
                new Recipient { ExternalUserId = "carol", Status = Recipient.NoAccess },
            },
        }),
    };

    [Theory]
    [MemberData(nameof(Spec.OperationNames), MemberType = typeof(Spec))]
    public async Task Operation(string name)
    {
        var op = Spec.Operations.Single(o => o.Name == name);
        Assert.Contains(op.Operation, HanshoSpec.Operations);
        Assert.True(Calls.TryGetValue(name, out var tc), $"no SDK call for spec operation {name}");

        using var hub = FakeHub.Start((_, reply) =>
        {
            reply.Status = op.ResponseStatus;
            reply.Body = op.ResponseBody.ValueKind == JsonValueKind.Null ? "" : op.ResponseBody.GetRawText();
        });
        var got = await tc.Call(hub.Client);

        var r = Assert.Single(hub.Requests);
        Assert.Equal(op.Method, r.Method);
        Assert.Equal(op.Path, r.RawUrl); // path and query (getFeed)
        Assert.Equal("Bearer " + FakeHub.Key, r.Headers["Authorization"]);
        if (op.Request.ValueKind == JsonValueKind.Null)
        {
            Assert.Empty(r.Body);
            Assert.Null(r.ContentType);
        }
        else
        {
            Assert.Equal(op.Request.GetProperty("content_type").GetString(), r.ContentType);
            if (op.Request.TryGetProperty("raw_base64", out var b64))
                Assert.Equal(Convert.FromBase64String(b64.GetString()!), r.Body);
            else
                Assert.True(Json.Equal(op.Request.GetProperty("body"), Json.Parse(r.BodyText)), $"body mismatch:\nwant {op.Request.GetProperty("body").GetRawText()}\ngot  {r.BodyText}");
        }
        Assert.Equal(Json.Describe(tc.Want), Json.Describe(got));
    }

    [Fact]
    public void EverySpecOperationAndEverySdkCallIsCovered()
    {
        var names = Spec.Operations.Select(o => o.Name).ToHashSet();
        Assert.True(names.Count >= 35);
        foreach (var k in Calls.Keys) Assert.Contains(k, names);
        foreach (var n in names) Assert.True(Calls.ContainsKey(n), $"spec operation {n} has no SDK call");
    }

    [Fact]
    public void HasAClientMethodForEveryOperationIdOfTheSpec()
    {
        var methods = typeof(HanshoClient).GetMethods().Select(m => m.Name).ToHashSet();
        var missing = HanshoSpec.Operations.Where(id => !methods.Contains(char.ToUpperInvariant(id[0]) + id[1..] + "Async")).ToList();
        Assert.True(missing.Count == 0, "the client lacks methods for: " + string.Join(", ", missing));
        Assert.Equal(HanshoSpec.Operations.Count, HanshoSpec.Operations.Distinct().Count());
    }

    [Theory]
    [MemberData(nameof(Spec.ErrorIndexes), MemberType = typeof(Spec))]
    public async Task Error(int index)
    {
        var e = Spec.Errors[index];
        using var hub = FakeHub.Start((_, reply) =>
        {
            reply.Status = e.Status;
            foreach (var (k, v) in e.Headers) reply.Headers[k] = v;
            reply.Body = e.Raw ?? e.Body.GetRawText();
        });
        var err = await Assert.ThrowsAsync<HanshoApiException>(() =>
            hub.Client.SendMessageAsync(new Message { ExternalUserId = "u", Channel = "c", Title = "t", Body = "b" }));
        Assert.Equal(e.Status, err.Status);
        Assert.Equal(e.Code, err.Code);
        Assert.Equal(e.Field, err.Field ?? "");
        Assert.Equal(e.RetryAfterMs, (long)(err.RetryAfter?.TotalMilliseconds ?? 0));
        Assert.Single(hub.Requests); // POST /messages must not be retried
        Assert.Empty(hub.Waits);
    }
}

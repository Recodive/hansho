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
        },
    };

    private static readonly byte[] WavBytes = Convert.FromBase64String("UklGRgAAAABXQVZF");

    /// <summary>The SDK call for each operation in api.json, and the value it must return.</summary>
    private static readonly Dictionary<string, (Func<HanshoClient, Task<object?>> Call, object? Want)> Calls = new()
    {
        ["createPairingTicket"] = (async c => await c.CreatePairingTicketAsync("user-42"),
            new PairingTicket("tk_abc", "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=tk_abc&fp=ff00", new DateTimeOffset(2026, 11, 8, 12, 10, 0, TimeSpan.Zero))),
        ["createPairingTicketWithName"] = (async c => await c.CreatePairingTicketAsync("user-42", "Zoë 日本語"),
            new PairingTicket("tk_def", "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=tk_def&fp=ff00", new DateTimeOffset(2026, 11, 8, 12, 10, 0, TimeSpan.Zero))),
        ["setUserDisplayName"] = (async c => { await c.SetUserDisplayNameAsync("user-42", "Zoë 日本語"); return null; }, null),
        ["clearUserDisplayName"] = (async c => { await c.SetUserDisplayNameAsync("user 42", ""); return null; }, null),
        ["unpair"] = (async c => { await c.UnpairAsync("user-42"); return null; }, null),
        ["putChannel"] = (async c => { await c.PutChannelAsync("ops", SpecChannel); return null; }, null),
        ["getChannel"] = (async c => await c.GetChannelAsync("ops"),
            new Channel { Name = "Operations", DefaultLevel = Level.Active, MaxLevel = Level.Critical, Sound = "", TemplateId = "", Thread = "ops", Actions = Array.Empty<MessageAction>() }),
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
        ["putSound"] = (async c => await c.PutSoundAsync("ding", "audio/wav", WavBytes), new SoundResult("ding", "ding.wav", "00", 12)),
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
        }), new MessageResult("msg_1", Level.TimeSensitive, 2, "queued", new[] { new Delivery("dev_1", "queued", null), new Delivery("dev_2", "queued", null) })),
        ["sendPage"] = (async c => await c.SendMessageAsync(new Message
        {
            ExternalUserId = "user-42",
            Channel = "ops",
            Level = Level.Page,
            Title = "Primary DB down",
            Body = "Failover did not complete",
            Page = new PageOptions { IntervalSeconds = 60, MaxRetries = 3 },
        }), new MessageResult("msg_2", Level.Page, 1, "queued", new[] { new Delivery("dev_1", "queued", null) })),
        ["sendPageEach"] = (async c => await c.SendMessageAsync(new Message
        {
            ExternalUserIds = new[] { "alice", "bob" },
            Channel = "ops",
            Level = Level.Page,
            Title = "Primary DB down",
            Body = "Failover did not complete",
            Page = new PageOptions { IntervalSeconds = 60, MaxRetries = 3, Ack = PageAck.Each },
        }), new MessageResult("", Level.Page, 0, "", Array.Empty<Delivery>(), new[]
        {
            new Recipient("alice", "msg_3", "queued", 1, new[] { new Delivery("dev_1", "queued", null) }),
            new Recipient("bob", "msg_4", "queued", 1, new[] { new Delivery("dev_2", "queued", null) }),
        })),
        ["getMessageAckedNoName"] = (async c => await c.GetMessageAsync("msg_3"),
            new MessageStatus("msg_3", "acked", Level.Page, 1, new[] { new Delivery("dev_1", "sent", null) }, new DateTimeOffset(2026, 11, 8, 12, 5, 0, TimeSpan.Zero)) { AckedBy = "alice" }),
        ["getMessage"] = (async c => await c.GetMessageAsync("msg_2"),
            new MessageStatus("msg_2", "acked", Level.Page, 2, new[] { new Delivery("dev_1", "sent", null), new Delivery("dev_2", "failed", "unregistered") }, new DateTimeOffset(2026, 11, 8, 12, 5, 0, TimeSpan.Zero))
            { AckedBy = "user-42", AckedByName = "Zoë 日本語" }),
        ["startActivity"] = (async c => await c.StartActivityAsync(new Activity
        {
            ExternalUserId = "user-42",
            Channel = "ops",
            TemplateId = "deploy",
            Data = new { progress = 0.1 },
            Alert = new ActivityAlert { Title = "Deploy started", Body = "v1.2.3" },
        }), new ActivityStarted("act_1", 1)),
        ["updateActivity"] = (async c => await c.UpdateActivityAsync("act_1", new ActivityUpdate
        {
            Data = new { progress = 0.6 },
            Priority = 10,
            Alert = new ActivityAlert { Title = "Halfway", Body = "60%" },
        }), new ActivityUpdated("act_1", 3)),
        ["endActivity"] = (async c => { await c.EndActivityAsync("act_1"); return null; }, null),
        ["putFeed"] = (async c => await c.PutFeedAsync("uptime", new Feed
        {
            ExternalUserId = "user-42",
            TemplateId = "status",
            Title = "Uptime",
            Data = new Dictionary<string, object> { ["up"] = true, ["ms"] = 41 },
        }), new FeedResult("uptime", 2)),
        ["putGroup"] = (async c => await c.PutGroupAsync("oncall", new[] { "alice", "bob" }), new Group("oncall", new[] { "alice", "bob" })),
        ["getGroup"] = (async c => await c.GetGroupAsync("oncall"), new Group("oncall", new[] { "alice", "bob" })),
        ["deleteGroup"] = (async c => { await c.DeleteGroupAsync("oncall"); return null; }, null),
        ["kick"] = (async c => { await c.KickAsync("user-42"); return null; }, null),
        ["putChannelWithAudience"] = (async c =>
        {
            await c.PutChannelAsync("ops", new Channel
            {
                Name = "Operations",
                DefaultLevel = Level.Active,
                MaxLevel = Level.Critical,
                Audience = new Audience { Mode = AudienceMode.Only, Users = new[] { "alice" }, Groups = new[] { "oncall" } },
            });
            return null;
        }, null),
        ["putFeedWithChannel"] = (async c => await c.PutFeedAsync("uptime", new Feed
        {
            TemplateId = "status",
            Channel = "ops",
            Audience = new Audience { Mode = AudienceMode.Only, Users = new[] { "alice" }, Groups = new[] { "oncall" } },
            Data = new Dictionary<string, object> { ["up"] = true },
        }), new FeedResult("uptime", 1)),
        ["sendToAudience"] = (async c => await c.SendMessageAsync(new Message
        {
            ToAudience = true,
            Channel = "ops",
            Title = "Deploy done",
            Body = "v1.2.3 is live",
        }), new MessageResult("", Level.Active, 0, "", Array.Empty<Delivery>(), new[]
        {
            new Recipient("alice", "msg_7", "queued", 1, new[] { new Delivery("dev_1", "queued", null) }),
            new Recipient("carol", null, Recipient.NoAccess, 0, Array.Empty<Delivery>()),
        })),
    };

    [Theory]
    [MemberData(nameof(Spec.OperationNames), MemberType = typeof(Spec))]
    public async Task Operation(string name)
    {
        var op = Spec.Operations.Single(o => o.Name == name);
        Assert.True(Calls.TryGetValue(name, out var tc), $"no SDK call for spec operation {name}");

        using var hub = FakeHub.Start((_, reply) =>
        {
            reply.Status = op.ResponseStatus;
            reply.Body = op.ResponseBody.ValueKind == JsonValueKind.Null ? "" : op.ResponseBody.GetRawText();
        });
        var got = await tc.Call(hub.Client);

        var r = Assert.Single(hub.Requests);
        Assert.Equal(op.Method, r.Method);
        Assert.Equal(op.Path, r.RawUrl);
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
        Assert.True(names.Count >= 21);
        foreach (var k in Calls.Keys) Assert.Contains(k, names);
        foreach (var n in names) Assert.True(Calls.ContainsKey(n), $"spec operation {n} has no SDK call");
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

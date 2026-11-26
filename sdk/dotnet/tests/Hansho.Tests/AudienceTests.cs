using System;
using System.Collections.Generic;
using System.Linq;
using System.Threading.Tasks;
using Xunit;

namespace Hansho.Tests;

public class AudienceTests
{
    private static readonly Channel Pager = new() { Name = "Pager", DefaultLevel = Level.Page, MaxLevel = Level.Page };
    private static readonly Message ToOne = new() { ExternalUserId = "u", Channel = "c", Title = "t", Body = "b" };

    private static string Body(Recorded r) => r.BodyText;

    // --- groups ------------------------------------------------------------------

    [Fact]
    public async Task PutGroupReplacesTheWholeMemberList()
    {
        using var hub = FakeHub.Start((_, reply) => reply.Body = "{\"id\":\"oncall\",\"members\":[\"alice\",\"bob\"]}");
        var g = await hub.Client.PutGroupAsync("oncall", new[] { "bob", "alice" });
        var r = hub.Requests[0];
        Assert.Equal("PUT", r.Method);
        Assert.Equal("/api/v1/groups/oncall", r.RawUrl);
        Assert.Equal("{\"members\":[\"bob\",\"alice\"]}", Body(r));
        Assert.Equal("oncall", g.Id);
        Assert.Equal(new[] { "alice", "bob" }, g.Members);
        await hub.Client.PutGroupAsync("oncall", Array.Empty<string>());
        Assert.Equal("{\"members\":[]}", Body(hub.Requests[1]));
    }

    [Fact]
    public async Task GetsAndDeletesAGroupEscapingTheId()
    {
        using var hub = FakeHub.Start((r, reply) =>
        {
            if (r.Method == "DELETE") reply.Status = 204;
            else reply.Body = "{\"id\":\"a b\",\"members\":[]}";
        });
        var g = await hub.Client.GetGroupAsync("a b");
        Assert.Equal("a b", g.Id);
        Assert.Empty(g.Members);
        await hub.Client.DeleteGroupAsync("a b");
        Assert.Equal(new[] { "GET /api/v1/groups/a%20b", "DELETE /api/v1/groups/a%20b" }, hub.Requests.Select(r => r.Method + " " + r.RawUrl));
        await Assert.ThrowsAsync<ArgumentException>(() => hub.Client.PutGroupAsync("", Array.Empty<string>()));
        await Assert.ThrowsAsync<ArgumentException>(() => hub.Client.GetGroupAsync(".."));
        Assert.Equal(2, hub.Requests.Count);
    }

    [Fact]
    public async Task AMissingGroupIsUnknownGroup()
    {
        using var hub = FakeHub.Start((_, reply) => { reply.Status = 404; reply.Body = "{\"error\":\"unknown_group\"}"; });
        var e = await Assert.ThrowsAsync<HanshoApiException>(() => hub.Client.GetGroupAsync("gone"));
        Assert.Equal(404, e.Status);
        Assert.Equal(ErrorCodes.UnknownGroup, e.Code);
    }

    // --- audiences ---------------------------------------------------------------

    [Fact]
    public async Task PutsAChannelAudienceAndOmitsItWhenAbsent()
    {
        using var hub = FakeHub.Start();
        await hub.Client.PutChannelAsync("pager", Pager with
        {
            Audience = new Audience { Mode = AudienceMode.Only, Users = new[] { "alice" }, Groups = new[] { "oncall" } },
        });
        var body = Json.Parse(Body(hub.Requests[0]));
        Assert.Equal("{\"mode\":\"only\",\"users\":[\"alice\"],\"groups\":[\"oncall\"]}", body.GetProperty("audience").GetRawText());

        await hub.Client.PutChannelAsync("pager", Pager);
        Assert.False(Json.Parse(Body(hub.Requests[1])).TryGetProperty("audience", out _), "an absent audience keeps the stored one");

        await hub.Client.PutChannelAsync("pager", Pager with { Audience = new Audience { Mode = AudienceMode.Everyone } });
        Assert.Equal("{\"mode\":\"everyone\"}", Json.Parse(Body(hub.Requests[2])).GetProperty("audience").GetRawText());
    }

    [Fact]
    public async Task ReadsAChannelAudience()
    {
        using var hub = FakeHub.Start((_, reply) => reply.Body =
            "{\"name\":\"Ops\",\"default_level\":\"active\",\"max_level\":\"page\",\"audience\":{\"mode\":\"except\",\"users\":[\"bob\"],\"groups\":[]}}");
        var c = await hub.Client.GetChannelAsync("ops");
        Assert.Equal(AudienceMode.Except, c.Audience!.Mode);
        Assert.Equal(new[] { "bob" }, c.Audience.Users);
        Assert.Empty(c.Audience.Groups!);
    }

    [Fact]
    public async Task AFeedCarriesItsChannelAndAudience()
    {
        using var hub = FakeHub.Start();
        await hub.Client.PutFeedAsync("queue", new Feed { TemplateId = "status", Channel = "pager", Data = 1 });
        var plain = Json.Parse(Body(hub.Requests[0]));
        Assert.Equal("pager", plain.GetProperty("channel").GetString());
        Assert.False(plain.TryGetProperty("audience", out _));

        await hub.Client.PutFeedAsync("queue", new Feed
        {
            TemplateId = "status",
            Audience = new Audience { Mode = AudienceMode.Except, Groups = new[] { "interns" } },
            Data = 1,
        });
        var own = Json.Parse(Body(hub.Requests[1]));
        Assert.False(own.TryGetProperty("channel", out _));
        Assert.Equal("{\"mode\":\"except\",\"groups\":[\"interns\"]}", own.GetProperty("audience").GetRawText());
    }

    [Fact]
    public async Task GetFeedReadsTheSettingsWithoutTheData()
    {
        using var hub = FakeHub.Start((_, reply) => reply.Body =
            "{\"feed_id\":\"queue\",\"external_user_id\":\"u 1\",\"template_id\":\"status\",\"title\":\"Queue\",\"channel\":\"pager\",\"updated_at\":1760000000}");
        var f = await hub.Client.GetFeedAsync("queue", "u 1");
        Assert.Equal("GET", hub.Requests[0].Method);
        Assert.Equal("/api/v1/feeds/queue?external_user_id=u%201", hub.Requests[0].RawUrl);
        Assert.Equal(new FeedSettings { FeedId = "queue", ExternalUserId = "u 1", TemplateId = "status", Title = "Queue", Channel = "pager", UpdatedAt = 1760000000 }, f);

        await hub.Client.GetFeedAsync("queue");
        Assert.Equal("/api/v1/feeds/queue", hub.Requests[1].RawUrl);
    }

    [Fact]
    public async Task AnAudienceNamingAnUnknownGroupIsAValidationError()
    {
        using var hub = FakeHub.Start((_, reply) =>
        {
            reply.Status = 400;
            reply.Body = "{\"error\":\"invalid_request\",\"field\":\"audience.groups\",\"message\":\"unknown group\"}";
        });
        var audience = new Audience { Mode = AudienceMode.Only, Groups = new[] { "typo" } };
        var e = await Assert.ThrowsAsync<HanshoApiException>(() => hub.Client.PutChannelAsync("pager", Pager with { Audience = audience }));
        Assert.Equal(400, e.Status);
        Assert.Equal("audience.groups", e.Field);
        Assert.Equal("unknown group", e.Detail);
        await Assert.ThrowsAsync<HanshoApiException>(() => hub.Client.PutFeedAsync("f", new Feed { TemplateId = "t", Audience = audience, Data = 1 }));
    }

    // --- kick --------------------------------------------------------------------

    [Fact]
    public async Task KickDeletesTheUsersPairings()
    {
        using var hub = FakeHub.Start((_, reply) => reply.Status = 204);
        await hub.Client.KickAsync("user 42");
        var r = hub.Requests[0];
        Assert.Equal("DELETE", r.Method);
        Assert.Equal("/api/v1/users/user%2042/pairings", r.RawUrl);
        await Assert.ThrowsAsync<ArgumentException>(() => hub.Client.KickAsync(""));
    }

    // --- sending -----------------------------------------------------------------

    [Fact]
    public async Task SendsToSeveralUsersOrToTheAudience()
    {
        using var hub = FakeHub.Start();
        await hub.Client.SendMessageAsync(ToOne with { ExternalUserId = null, ExternalUserIds = new[] { "alice", "bob" } });
        Assert.Equal("{\"external_user_ids\":[\"alice\",\"bob\"],\"channel\":\"c\",\"title\":\"t\",\"body\":\"b\"}", Body(hub.Requests[0]));
        await hub.Client.SendMessageAsync(ToOne with { ExternalUserId = null, ToAudience = true });
        Assert.Equal("{\"to_audience\":true,\"channel\":\"c\",\"title\":\"t\",\"body\":\"b\"}", Body(hub.Requests[1]));
        await hub.Client.SendMessageAsync(ToOne);
        Assert.Equal("{\"external_user_id\":\"u\",\"channel\":\"c\",\"title\":\"t\",\"body\":\"b\"}", Body(hub.Requests[2]));
    }

    [Fact]
    public async Task ListsEveryRecipientAndMarksThoseWithoutAccess()
    {
        using var hub = FakeHub.Start((_, reply) =>
        {
            reply.Status = 202;
            reply.Body = "{\"level\":\"active\",\"recipients\":["
                + "{\"external_user_id\":\"alice\",\"message_id\":\"msg_1\",\"status\":\"queued\",\"devices\":1,\"deliveries\":[{\"device_id\":\"d1\",\"status\":\"queued\"}]},"
                + "{\"external_user_id\":\"carol\",\"status\":\"no_access\"}]}";
        });
        var res = await hub.Client.SendMessageAsync(ToOne with { ExternalUserId = null, ExternalUserIds = new[] { "alice", "carol" } });
        Assert.Null(res.MessageId); // several users: each message is under Recipients
        Assert.Null(res.Status);
        Assert.Null(res.Devices);
        Assert.Equal(Level.Active, res.Level);
        Assert.Null(res.Deliveries);
        var rs = res.Recipients!;
        Assert.Equal(2, rs.Count);
        Assert.Equal("msg_1", rs[0].MessageId);
        Assert.Equal("queued", rs[0].Status);
        Assert.Equal(1, rs[0].Devices);
        Assert.Equal(new Delivery { DeviceId = "d1", Status = "queued" }, Assert.Single(rs[0].Deliveries!));
        Assert.Equal("carol", rs[1].ExternalUserId);
        Assert.Equal(Recipient.NoAccess, rs[1].Status);
        Assert.Null(rs[1].MessageId);
        Assert.Null(rs[1].Devices);
    }

    [Fact]
    public async Task ASingleUserWithoutAccessKeepsTheTopLevelFields()
    {
        using var hub = FakeHub.Start((_, reply) =>
        {
            reply.Status = 202;
            reply.Body = "{\"level\":\"active\",\"external_user_id\":\"u\",\"message_id\":\"\",\"status\":\"no_access\",\"devices\":0,\"deliveries\":[],"
                + "\"recipients\":[{\"external_user_id\":\"u\",\"status\":\"no_access\"}]}";
        });
        var res = await hub.Client.SendMessageAsync(ToOne);
        Assert.Equal("", res.MessageId);
        Assert.Equal(Recipient.NoAccess, res.Status);
        Assert.Equal(0, res.Devices);
        Assert.Equal(Recipient.NoAccess, Assert.Single(res.Recipients!).Status);
    }

    [Fact]
    public async Task AHubWithoutRecipientsStillParses()
    {
        using var hub = FakeHub.Start((_, reply) => reply.Body = "{\"message_id\":\"m1\",\"level\":\"active\",\"devices\":2,\"status\":\"queued\",\"deliveries\":[]}");
        var res = await hub.Client.SendMessageAsync(ToOne);
        Assert.Equal("m1", res.MessageId);
        Assert.Equal(2, res.Devices);
        Assert.Null(res.Recipients);
    }

    [Fact]
    public async Task NoAccessOnAnActivityIsAForbiddenError()
    {
        using var hub = FakeHub.Start((_, reply) => { reply.Status = 403; reply.Body = "{\"error\":\"no_access\"}"; });
        var e = await Assert.ThrowsAsync<HanshoApiException>(() => hub.Client.StartActivityAsync(
            new Activity { ExternalUserId = "u", Channel = "c", TemplateId = "t", Data = 1 }));
        Assert.Equal(403, e.Status);
        Assert.Equal(ErrorCodes.NoAccess, e.Code);
    }
}

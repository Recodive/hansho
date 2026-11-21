using System;
using System.Collections.Generic;

namespace Hansho;

/// <summary>
/// How loudly a message is presented, lowest to highest. A struct rather than an enum so a level a newer hub
/// sends (in a response or a webhook) is kept as it is instead of failing.
/// </summary>
public readonly record struct Level(string Value)
{
    public static readonly Level Passive = new("passive");
    public static readonly Level Active = new("active");
    public static readonly Level TimeSensitive = new("time-sensitive");
    public static readonly Level Critical = new("critical");
    /// <summary>Repeats until someone acknowledges it on any device.</summary>
    public static readonly Level Page = new("page");

    public override string ToString() => Value ?? "";
}

/// <summary>A button shown with a message or channel. An <c>Ack</c> action acknowledges a page.</summary>
public sealed record MessageAction
{
    public required string Id { get; init; }
    public required string Title { get; init; }
    public bool? Ack { get; init; }
    public string? Kind { get; init; }
}

/// <summary>A single-use pairing ticket, valid for 10 minutes.</summary>
public sealed record PairingTicket(string Ticket, string Qr, DateTimeOffset ExpiresAt);

/// <summary>
/// How an <see cref="Audience"/> reads its lists: everyone, only the listed, or everyone except the listed. A struct
/// like <see cref="Level"/>, so a mode a newer hub sends is kept as it is.
/// </summary>
public readonly record struct AudienceMode(string Value)
{
    /// <summary>Every paired user (the lists must be empty).</summary>
    public static readonly AudienceMode Everyone = new("everyone");
    /// <summary>Just the listed users and the members of the listed groups.</summary>
    public static readonly AudienceMode Only = new("only");
    /// <summary>Everyone but the listed users and the members of the listed groups.</summary>
    public static readonly AudienceMode Except = new("except");

    public override string ToString() => Value ?? "";
}

/// <summary>
/// Who may use a channel or feed. <c>Users</c> are your external user ids (they may not have paired yet) and
/// <c>Groups</c> are group ids (see <see cref="HanshoClient.PutGroupAsync"/>; a group that does not exist has no
/// members). Both must be empty for <see cref="AudienceMode.Everyone"/>.
/// </summary>
public sealed record Audience
{
    public required AudienceMode Mode { get; init; }
    public IReadOnlyList<string> Users { get; init; } = Array.Empty<string>();
    public IReadOnlyList<string> Groups { get; init; } = Array.Empty<string>();
}

/// <summary>A named list of users, used in audiences.</summary>
public sealed record Group(string Id, IReadOnlyList<string> Members);

public sealed record Channel
{
    public required string Name { get; init; }
    public required Level DefaultLevel { get; init; }
    public required Level MaxLevel { get; init; }
    public string? Sound { get; init; }
    public string? TemplateId { get; init; }
    public string? Thread { get; init; }
    public IReadOnlyList<MessageAction>? Actions { get; init; }
    /// <summary>
    /// Who may use the channel. Null keeps the audience stored on the hub (a new channel starts open to everyone);
    /// send <see cref="AudienceMode.Everyone"/> to open an existing channel up. <see cref="HanshoClient.GetChannelAsync"/>
    /// always returns one.
    /// </summary>
    public Audience? Audience { get; init; }
}

public sealed record PageOptions
{
    /// <summary>Seconds between resends (hub default 120).</summary>
    public int? IntervalSeconds { get; init; }
    /// <summary>Resends before the page expires (hub default 5). 0 is sent as 0.</summary>
    public int? MaxRetries { get; init; }
}

public sealed record Message
{
    /// <summary>
    /// Give exactly one of <c>ExternalUserId</c>, <c>ExternalUserIds</c> (at most 1000) and <c>ToAudience</c>. People
    /// outside the channel's audience are not delivered to: their entry in <see cref="MessageResult.Recipients"/> has
    /// the status <see cref="Recipient.NoAccess"/>.
    /// </summary>
    public string? ExternalUserId { get; init; }
    /// <summary>Send to several users at once.</summary>
    public IReadOnlyList<string>? ExternalUserIds { get; init; }
    /// <summary>Send to every paired user in the channel's audience, resolved when you send.</summary>
    public bool ToAudience { get; init; }
    public required string Channel { get; init; }
    /// <summary>Defaults to the channel's default level and is capped at its max.</summary>
    public Level? Level { get; init; }
    public required string Title { get; init; }
    public string? Subtitle { get; init; }
    public required string Body { get; init; }
    public string? ImageUrl { get; init; }
    public string? TemplateId { get; init; }
    /// <summary>
    /// Template data, sent as given: a dictionary, an anonymous or plain object, a <c>JsonElement</c> or a
    /// <c>JsonNode</c>. Keys are never renamed.
    /// </summary>
    public object? Data { get; init; }
    public string? Sound { get; init; }
    public IReadOnlyList<MessageAction>? Actions { get; init; }
    public string? CollapseId { get; init; }
    public PageOptions? Page { get; init; }
    /// <summary>
    /// Opts this send in to retries of 429 and 503. Sent as the <c>Idempotency-Key</c> header, never in the body.
    /// The hub ignores it today, so a retried send is at-least-once.
    /// </summary>
    public string? IdempotencyKey { get; init; }
}

public sealed record Delivery(string DeviceId, string Status, string? Error);

/// <summary>
/// The outcome of a send for one user. <c>Status</c> is the message status (queued, undeliverable) or
/// <see cref="NoAccess"/>, in which case there is no <c>MessageId</c>.
/// </summary>
public sealed record Recipient(string ExternalUserId, string? MessageId, string Status, int Devices, IReadOnlyList<Delivery> Deliveries)
{
    /// <summary>The <c>Status</c> of a user outside the channel's audience: it got no message.</summary>
    public const string NoAccess = "no_access";
}

/// <summary>
/// The answer to a send. For a send to <c>ExternalUserId</c> the other fields describe that one recipient (<c>MessageId</c>
/// is empty, <c>Status</c> is <see cref="Recipient.NoAccess"/> and <c>Devices</c> is 0 when the user is outside the
/// audience); for several users or an audience, <c>MessageId</c>, <c>Devices</c> and <c>Deliveries</c> are empty and each
/// message is under <c>Recipients</c>, which is null when the hub predates audiences.
/// </summary>
public sealed record MessageResult(string MessageId, Level Level, int Devices, string Status, IReadOnlyList<Delivery> Deliveries, IReadOnlyList<Recipient>? Recipients = null);

/// <summary><c>Status</c> is one of queued, delivered, undeliverable, paging, acked, expired.</summary>
public sealed record MessageStatus(string MessageId, string Status, Level Level, int Attempts, IReadOnlyList<Delivery> Deliveries, DateTimeOffset? AckedAt);

public sealed record ActivityAlert
{
    public string? Title { get; init; }
    public string? Body { get; init; }
}

public sealed record Activity
{
    public required string ExternalUserId { get; init; }
    public required string Channel { get; init; }
    public required string TemplateId { get; init; }
    /// <summary>Template data, sent as given (see <see cref="Message.Data"/>).</summary>
    public required object Data { get; init; }
    public ActivityAlert? Alert { get; init; }
}

public sealed record ActivityUpdate
{
    /// <summary>Template data, sent as given (see <see cref="Message.Data"/>).</summary>
    public required object Data { get; init; }
    /// <summary>5 (default) or 10.</summary>
    public int? Priority { get; init; }
    public ActivityAlert? Alert { get; init; }
}

public sealed record ActivityStarted(string ActivityId, int Devices);

public sealed record ActivityUpdated(string ActivityId, int Update);

public sealed record Feed
{
    /// <summary>Leave null for a feed shared by all paired users.</summary>
    public string? ExternalUserId { get; init; }
    public required string TemplateId { get; init; }
    /// <summary>The name the app's feed picker shows instead of the id (at most 64 bytes).</summary>
    public string? Title { get; init; }
    /// <summary>The feed uses this channel's audience unless it sets its own. Like <c>Title</c>, replaced by every put.</summary>
    public string? Channel { get; init; }
    /// <summary>Who may see the feed. Null keeps the one stored on the hub; with neither this nor a channel it is for everyone.</summary>
    public Audience? Audience { get; init; }
    /// <summary>Template data, sent as given (see <see cref="Message.Data"/>).</summary>
    public required object Data { get; init; }
}

public sealed record FeedResult(string FeedId, int Devices);

/// <summary>A feed without its data, from <see cref="HanshoClient.GetFeedAsync"/>. <c>Audience</c> is null while the feed inherits its channel's.</summary>
public sealed record FeedSettings(string FeedId, string? ExternalUserId, string TemplateId, string? Title, string? Channel, Audience? Audience, long UpdatedAt);

public sealed record SoundResult(string Id, string Filename, string Sha256, long Size);

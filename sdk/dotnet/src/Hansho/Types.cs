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

public sealed record Channel
{
    public required string Name { get; init; }
    public required Level DefaultLevel { get; init; }
    public required Level MaxLevel { get; init; }
    public string? Sound { get; init; }
    public string? TemplateId { get; init; }
    public string? Thread { get; init; }
    public IReadOnlyList<MessageAction>? Actions { get; init; }
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
    public required string ExternalUserId { get; init; }
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

public sealed record MessageResult(string MessageId, Level Level, int Devices, string Status, IReadOnlyList<Delivery> Deliveries);

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
    /// <summary>Template data, sent as given (see <see cref="Message.Data"/>).</summary>
    public required object Data { get; init; }
}

public sealed record FeedResult(string FeedId, int Devices);

public sealed record SoundResult(string Id, string Filename, string Sha256, long Size);

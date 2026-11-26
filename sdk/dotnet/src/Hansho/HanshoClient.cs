using System;
using System.Collections.Generic;
using System.Globalization;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Encodings.Web;
using System.Text.Json;
using System.Text.Json.Nodes;
using System.Threading;
using System.Threading.Tasks;

namespace Hansho;

/// <summary>
/// Client for a Hansho hub's sender API. Thread-safe; create one and reuse it. Every call can be cancelled with a
/// <see cref="CancellationToken"/>. A non-2xx answer throws <see cref="HanshoApiException"/>; no answer at all
/// (connection, timeout, cancellation) throws <see cref="HanshoNetworkException"/>.
/// </summary>
/// <remarks>
/// Only GET, PUT and DELETE are retried (on 429 and 503, at most <see cref="HanshoClientOptions.MaxRetries"/> times,
/// honouring Retry-After up to 60 s, else exponential backoff with jitter). <see cref="SendMessageAsync"/> is never
/// retried unless the message has an <see cref="Message.IdempotencyKey"/>; activity updates (PATCH) are never retried.
/// </remarks>
public sealed class HanshoClient : IDisposable
{
    /// <summary>Product codename.</summary>
    public const string Name = "hansho";

    /// <summary>SDK version, sent in the default User-Agent.</summary>
    public const string Version = "0.1.0";

    private const int MaxSoundBytes = 1 << 20;

    private static readonly JsonSerializerOptions DataOptions = new()
    {
        Encoder = JavaScriptEncoder.UnsafeRelaxedJsonEscaping,
    };

    private readonly string _baseUrl;
    private readonly string _apiKey;
    private readonly HttpClient _http;
    private readonly bool _ownsHttp;
    private readonly int _maxRetries;
    private readonly TimeSpan _timeout;
    private readonly string _userAgent;
    private readonly Func<TimeSpan, CancellationToken, Task> _delay;

    /// <summary>Creates a client with the default options.</summary>
    /// <param name="baseUrl">The hub's public URL.</param>
    /// <param name="apiKey">A sender API key.</param>
    public HanshoClient(string baseUrl, string apiKey) : this(new HanshoClientOptions { BaseUrl = baseUrl, ApiKey = apiKey }) { }

    public HanshoClient(HanshoClientOptions options)
    {
        ArgumentNullException.ThrowIfNull(options);
        if (string.IsNullOrEmpty(options.BaseUrl)) throw new ArgumentException("hansho: BaseUrl is required", nameof(options));
        if (!Uri.TryCreate(options.BaseUrl, UriKind.Absolute, out var uri) || (uri.Scheme != Uri.UriSchemeHttp && uri.Scheme != Uri.UriSchemeHttps))
            throw new ArgumentException("hansho: BaseUrl must be an absolute http(s) URL", nameof(options));
        if (string.IsNullOrEmpty(options.ApiKey)) throw new ArgumentException("hansho: ApiKey is required", nameof(options));

        _baseUrl = options.BaseUrl.TrimEnd('/');
        _apiKey = options.ApiKey;
        _maxRetries = Math.Max(0, options.MaxRetries);
        _timeout = options.Timeout;
        _userAgent = string.IsNullOrEmpty(options.UserAgent) ? $"{Name}-dotnet/{Version}" : options.UserAgent;
        _delay = options.Delay ?? ((d, ct) => Task.Delay(d, ct));
        if (options.HttpClient is { } hc)
        {
            _http = hc;
        }
        else
        {
            _http = new HttpClient(new SocketsHttpHandler { AllowAutoRedirect = false, PooledConnectionLifetime = TimeSpan.FromMinutes(2) })
            {
                Timeout = System.Threading.Timeout.InfiniteTimeSpan, // per-attempt timeouts are applied below
            };
            _ownsHttp = true;
        }
    }

    /// <summary>Disposes the <see cref="HttpClient"/> only when this client created it.</summary>
    public void Dispose()
    {
        if (_ownsHttp) _http.Dispose();
    }

    /// <summary>Never prints the API key.</summary>
    public override string ToString() => $"HanshoClient {{ BaseUrl = {_baseUrl} }}";

    // --- pairing -------------------------------------------------------------

    /// <summary>
    /// Creates a single-use pairing ticket (valid 10 minutes). Render <see cref="PairingTicket.Qr"/> as a QR code
    /// for the user to scan in the Hansho app.
    /// </summary>
    public Task<PairingTicket> CreatePairingTicketAsync(string externalUserId, CancellationToken cancellationToken = default) =>
        CreatePairingTicketAsync(externalUserId, null, cancellationToken);

    /// <summary>
    /// Like <see cref="CreatePairingTicketAsync(string, CancellationToken)"/>, and gives the user a display name with
    /// the ticket: what other people see instead of their id (any language, trimmed, at most 40 characters, no control
    /// characters). Null sends none; "" is sent as given.
    /// </summary>
    public async Task<PairingTicket> CreatePairingTicketAsync(string externalUserId, string? displayName, CancellationToken cancellationToken = default)
    {
        if (string.IsNullOrEmpty(externalUserId)) throw new ArgumentException("hansho: id must not be empty", nameof(externalUserId));
        var body = new JsonObject { ["external_user_id"] = externalUserId };
        if (displayName is not null) body["display_name"] = displayName;
        var w = await JsonCallAsync("POST", "/api/v1/pairing-tickets", body, cancellationToken).ConfigureAwait(false);
        return new PairingTicket(Str(w, "ticket"), Str(w, "qr"), Time(w, "expires_at") ?? default);
    }

    // --- users ---------------------------------------------------------------

    /// <summary>
    /// Sets what other people see for the user instead of their external user id: any language, trimmed, at most 40
    /// characters, no control characters. "" (or null) clears it. The user need not be paired yet.
    /// </summary>
    public async Task SetUserDisplayNameAsync(string externalUserId, string? displayName, CancellationToken cancellationToken = default)
    {
        await JsonCallAsync("PUT", $"/api/v1/users/{Seg(externalUserId)}", new JsonObject { ["display_name"] = displayName ?? "" }, cancellationToken).ConfigureAwait(false);
    }

    /// <summary>Revokes every pairing of the user.</summary>
    public async Task UnpairAsync(string externalUserId, CancellationToken cancellationToken = default)
    {
        await JsonCallAsync("DELETE", $"/api/v1/users/{Seg(externalUserId)}/pairings", null, cancellationToken).ConfigureAwait(false);
    }

    /// <summary>
    /// Ends the user's whole pairing, on every device: <see cref="UnpairAsync"/> under its sender-side name. The app
    /// tells the person that the sender ended it (a 401 <c>pairing_revoked</c> with reason <c>kicked</c>), where an
    /// unpair they did themselves carries no reason. There is no block list; a new ticket pairs them again.
    /// </summary>
    public Task KickAsync(string externalUserId, CancellationToken cancellationToken = default) => UnpairAsync(externalUserId, cancellationToken);

    // --- catalog -------------------------------------------------------------

    public async Task PutChannelAsync(string id, Channel channel, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(channel);
        var body = new JsonObject();
        Put(body, "name", channel.Name);
        Put(body, "default_level", channel.DefaultLevel.ToString());
        Put(body, "max_level", channel.MaxLevel.ToString());
        Put(body, "sound", channel.Sound);
        Put(body, "template_id", channel.TemplateId);
        Put(body, "thread", channel.Thread);
        if (channel.Actions is not null) body["actions"] = ActionsToWire(channel.Actions);
        if (channel.Audience is { } audience) body["audience"] = AudienceToWire(audience);
        await JsonCallAsync("PUT", $"/api/v1/channels/{Seg(id)}", body, cancellationToken).ConfigureAwait(false);
    }

    public async Task<Channel> GetChannelAsync(string id, CancellationToken cancellationToken = default)
    {
        var w = await JsonCallAsync("GET", $"/api/v1/channels/{Seg(id)}", null, cancellationToken).ConfigureAwait(false);
        return new Channel
        {
            Name = Str(w, "name"),
            DefaultLevel = new Level(Str(w, "default_level")),
            MaxLevel = new Level(Str(w, "max_level")),
            Sound = OptStr(w, "sound"),
            TemplateId = OptStr(w, "template_id"),
            Thread = OptStr(w, "thread"),
            Actions = ActionsFromWire(w),
            Audience = AudienceFromWire(w),
        };
    }

    public async Task DeleteChannelAsync(string id, CancellationToken cancellationToken = default)
    {
        await JsonCallAsync("DELETE", $"/api/v1/channels/{Seg(id)}", null, cancellationToken).ConfigureAwait(false);
    }

    // --- groups --------------------------------------------------------------

    /// <summary>
    /// Creates a group or replaces its whole member list (external user ids, who may not have paired yet). Audiences
    /// that name the group follow at once. Ids match <c>[A-Za-z0-9][A-Za-z0-9_-]{0,63}</c>.
    /// </summary>
    public async Task<Group> PutGroupAsync(string id, IEnumerable<string> members, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(members);
        var list = new JsonArray();
        foreach (var m in members) list.Add(JsonValue.Create(m));
        var w = await JsonCallAsync("PUT", $"/api/v1/groups/{Seg(id)}", new JsonObject { ["members"] = list }, cancellationToken).ConfigureAwait(false);
        return GroupFromWire(w);
    }

    /// <summary>A missing group is a <see cref="HanshoApiException"/> with code <see cref="ErrorCodes.UnknownGroup"/>.</summary>
    public async Task<Group> GetGroupAsync(string id, CancellationToken cancellationToken = default)
    {
        var w = await JsonCallAsync("GET", $"/api/v1/groups/{Seg(id)}", null, cancellationToken).ConfigureAwait(false);
        return GroupFromWire(w);
    }

    /// <summary>An audience that still names the group matches nobody through it.</summary>
    public async Task DeleteGroupAsync(string id, CancellationToken cancellationToken = default)
    {
        await JsonCallAsync("DELETE", $"/api/v1/groups/{Seg(id)}", null, cancellationToken).ConfigureAwait(false);
    }

    /// <summary>
    /// Stores a template version; the version must increase. <paramref name="body"/> is the template tree: a
    /// dictionary, an anonymous or plain object, a <c>JsonElement</c> or a <c>JsonNode</c>.
    /// </summary>
    public async Task PutTemplateAsync(string id, int version, object body, CancellationToken cancellationToken = default)
    {
        var wire = new JsonObject { ["version"] = version, ["body"] = ToNode(body) };
        await JsonCallAsync("PUT", $"/api/v1/templates/{Seg(id)}", wire, cancellationToken).ConfigureAwait(false);
    }

    /// <summary>Uploads a sound (<c>audio/x-caf</c>, <c>audio/wav</c> or <c>audio/aiff</c>, at most 1 MB).</summary>
    public async Task<SoundResult> PutSoundAsync(string id, string contentType, ReadOnlyMemory<byte> bytes, CancellationToken cancellationToken = default)
    {
        var path = $"/api/v1/sounds/{Seg(id)}";
        if (string.IsNullOrEmpty(contentType)) throw new ArgumentException("hansho: contentType is required", nameof(contentType));
        if (bytes.Length > MaxSoundBytes) throw new ArgumentException("hansho: sound exceeds 1 MB", nameof(bytes));
        var call = new Call("PUT", path, bytes.ToArray(), contentType, Retry: true);
        var w = await SendAsync(call, cancellationToken).ConfigureAwait(false);
        return new SoundResult(Str(w, "id"), Str(w, "filename"), Str(w, "sha256"), Long(w, "size"));
    }

    // --- messages ------------------------------------------------------------

    /// <summary>
    /// Sends a notification, or a page when the level is <see cref="Level.Page"/>. It is never retried
    /// automatically (a repeat would notify twice) unless the message has an <see cref="Message.IdempotencyKey"/>.
    /// </summary>
    public async Task<MessageResult> SendMessageAsync(Message message, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(message);
        var body = new JsonObject();
        Put(body, "external_user_id", message.ExternalUserId);
        if (message.ExternalUserIds is { } ids) body["external_user_ids"] = StringsToWire(ids);
        if (message.ToAudience) body["to_audience"] = true;
        Put(body, "channel", message.Channel);
        Put(body, "level", message.Level?.ToString());
        Put(body, "title", message.Title);
        Put(body, "subtitle", message.Subtitle);
        Put(body, "body", message.Body);
        Put(body, "image_url", message.ImageUrl);
        Put(body, "template_id", message.TemplateId);
        if (message.Data is not null) body["data"] = ToNode(message.Data);
        Put(body, "sound", message.Sound);
        if (message.Actions is not null) body["actions"] = ActionsToWire(message.Actions);
        Put(body, "collapse_id", message.CollapseId);
        if (message.Page is { } page)
        {
            var p = new JsonObject();
            if (page.IntervalSeconds is { } i) p["interval_s"] = i;
            if (page.MaxRetries is { } m) p["max_retries"] = m;
            if (page.Ack is { } ack) p["ack"] = ack.Value;
            body["page"] = p;
        }

        var call = new Call("POST", "/api/v1/messages", Encode(body), "application/json", Retry: false);
        if (!string.IsNullOrEmpty(message.IdempotencyKey))
            call = call with { Retry = true, Headers = new[] { ("Idempotency-Key", message.IdempotencyKey) } };
        var w = await SendAsync(call, cancellationToken).ConfigureAwait(false);
        return new MessageResult(Str(w, "message_id"), new Level(Str(w, "level")), Int(w, "devices"), Str(w, "status"), DeliveriesFromWire(w), RecipientsFromWire(w));
    }

    public async Task<MessageStatus> GetMessageAsync(string id, CancellationToken cancellationToken = default)
    {
        var w = await JsonCallAsync("GET", $"/api/v1/messages/{Seg(id)}", null, cancellationToken).ConfigureAwait(false);
        return new MessageStatus(Str(w, "message_id"), Str(w, "status"), new Level(Str(w, "level")), Int(w, "attempts"), DeliveriesFromWire(w), Time(w, "acked_at"))
        {
            AckedBy = OptStr(w, "acked_by"),
            AckedByName = OptStr(w, "acked_by_name"),
        };
    }

    // --- live activities and feeds -------------------------------------------

    public async Task<ActivityStarted> StartActivityAsync(Activity activity, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(activity);
        var body = new JsonObject();
        Put(body, "external_user_id", activity.ExternalUserId);
        Put(body, "channel", activity.Channel);
        Put(body, "template_id", activity.TemplateId);
        body["data"] = ToNode(activity.Data);
        if (activity.Alert is { } alert) body["alert"] = AlertToWire(alert);
        var w = await JsonCallAsync("POST", "/api/v1/activities", body, cancellationToken).ConfigureAwait(false);
        return new ActivityStarted(Str(w, "activity_id"), Int(w, "devices"));
    }

    /// <summary>Not retried automatically.</summary>
    public async Task<ActivityUpdated> UpdateActivityAsync(string id, ActivityUpdate update, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(update);
        var body = new JsonObject { ["data"] = ToNode(update.Data) };
        if (update.Priority is { } priority) body["priority"] = priority;
        if (update.Alert is { } alert) body["alert"] = AlertToWire(alert);
        var w = await JsonCallAsync("PATCH", $"/api/v1/activities/{Seg(id)}", body, cancellationToken).ConfigureAwait(false);
        return new ActivityUpdated(Str(w, "activity_id"), Int(w, "update"));
    }

    public async Task EndActivityAsync(string id, CancellationToken cancellationToken = default)
    {
        await JsonCallAsync("DELETE", $"/api/v1/activities/{Seg(id)}", null, cancellationToken).ConfigureAwait(false);
    }

    public async Task<FeedResult> PutFeedAsync(string id, Feed feed, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(feed);
        var path = $"/api/v1/feeds/{Seg(id)}";
        var body = new JsonObject();
        Put(body, "external_user_id", feed.ExternalUserId);
        Put(body, "template_id", feed.TemplateId);
        Put(body, "title", feed.Title);
        Put(body, "channel", feed.Channel);
        if (feed.Audience is { } audience) body["audience"] = AudienceToWire(audience);
        body["data"] = ToNode(feed.Data);
        var w = await JsonCallAsync("PUT", path, body, cancellationToken).ConfigureAwait(false);
        return new FeedResult(Str(w, "feed_id"), Int(w, "devices"));
    }

    /// <summary>A feed's settings without its data. Leave <paramref name="externalUserId"/> null for the feed shared by all users.</summary>
    public async Task<FeedSettings> GetFeedAsync(string id, string? externalUserId = null, CancellationToken cancellationToken = default)
    {
        var path = $"/api/v1/feeds/{Seg(id)}";
        if (!string.IsNullOrEmpty(externalUserId)) path += "?external_user_id=" + Uri.EscapeDataString(externalUserId);
        var w = await JsonCallAsync("GET", path, null, cancellationToken).ConfigureAwait(false);
        return new FeedSettings(Str(w, "feed_id"), OptStr(w, "external_user_id"), Str(w, "template_id"), OptStr(w, "title"), OptStr(w, "channel"), AudienceFromWire(w), Long(w, "updated_at"));
    }

    // --- wire helpers --------------------------------------------------------

    /// <summary>Escapes one path segment; rejects ids that would not stay in their collection.</summary>
    private static string Seg(string id)
    {
        if (string.IsNullOrEmpty(id)) throw new ArgumentException("hansho: id must not be empty", nameof(id));
        // A dot segment would be collapsed by URL normalisation, so it cannot be sent safely.
        if (id is "." or "..") throw new ArgumentException($"hansho: id must not be \"{id}\"", nameof(id));
        return Uri.EscapeDataString(id);
    }

    private static void Put(JsonObject o, string key, string? value)
    {
        if (value is not null) o[key] = value;
    }

    private static JsonNode? ToNode(object? value) => value switch
    {
        null => null,
        JsonNode n => n.DeepClone(),
        JsonElement e => JsonSerializer.SerializeToNode(e, DataOptions),
        _ => JsonSerializer.SerializeToNode(value, value.GetType(), DataOptions),
    };

    private static JsonArray ActionsToWire(IReadOnlyList<MessageAction> actions)
    {
        var arr = new JsonArray();
        foreach (var a in actions)
        {
            var o = new JsonObject { ["id"] = a.Id, ["title"] = a.Title };
            if (a.Ack is { } ack) o["ack"] = ack;
            Put(o, "kind", a.Kind);
            arr.Add(o);
        }
        return arr;
    }

    private static JsonArray StringsToWire(IEnumerable<string> values)
    {
        var arr = new JsonArray();
        foreach (var v in values) arr.Add(JsonValue.Create(v)); // not Add<T>: that needs reflection-based serialization
        return arr;
    }

    private static JsonObject AudienceToWire(Audience a)
    {
        var o = new JsonObject { ["mode"] = a.Mode.ToString() };
        if (a.Users.Count > 0) o["users"] = StringsToWire(a.Users);
        if (a.Groups.Count > 0) o["groups"] = StringsToWire(a.Groups);
        return o;
    }

    private static JsonObject AlertToWire(ActivityAlert alert)
    {
        var o = new JsonObject();
        Put(o, "title", alert.Title);
        Put(o, "body", alert.Body);
        return o;
    }

    private static byte[] Encode(JsonNode body) =>
        Encoding.UTF8.GetBytes(body.ToJsonString(DataOptions));

    private static IReadOnlyList<MessageAction>? ActionsFromWire(JsonElement w)
    {
        if (!w.TryGetProperty("actions", out var raw) || raw.ValueKind != JsonValueKind.Array) return null;
        var list = new List<MessageAction>();
        foreach (var a in raw.EnumerateArray())
        {
            if (a.ValueKind != JsonValueKind.Object) continue;
            list.Add(new MessageAction
            {
                Id = Str(a, "id"),
                Title = Str(a, "title"),
                Ack = a.TryGetProperty("ack", out var ack) && ack.ValueKind is JsonValueKind.True or JsonValueKind.False ? ack.GetBoolean() : null,
                Kind = OptStr(a, "kind"),
            });
        }
        return list;
    }

    private static List<string> StringsFromWire(JsonElement w, string key)
    {
        var list = new List<string>();
        if (w.ValueKind == JsonValueKind.Object && w.TryGetProperty(key, out var raw) && raw.ValueKind == JsonValueKind.Array)
            foreach (var s in raw.EnumerateArray())
                if (s.ValueKind == JsonValueKind.String) list.Add(s.GetString()!);
        return list;
    }

    private static Audience? AudienceFromWire(JsonElement w)
    {
        if (w.ValueKind != JsonValueKind.Object || !w.TryGetProperty("audience", out var a) || a.ValueKind != JsonValueKind.Object) return null;
        return new Audience { Mode = new AudienceMode(Str(a, "mode")), Users = StringsFromWire(a, "users"), Groups = StringsFromWire(a, "groups") };
    }

    private static Group GroupFromWire(JsonElement w) => new(Str(w, "id"), StringsFromWire(w, "members"));

    private static IReadOnlyList<Recipient>? RecipientsFromWire(JsonElement w)
    {
        if (w.ValueKind != JsonValueKind.Object || !w.TryGetProperty("recipients", out var raw) || raw.ValueKind != JsonValueKind.Array) return null;
        var list = new List<Recipient>();
        foreach (var r in raw.EnumerateArray())
        {
            if (r.ValueKind != JsonValueKind.Object) continue;
            list.Add(new Recipient(Str(r, "external_user_id"), OptStr(r, "message_id"), Str(r, "status"), Int(r, "devices"), DeliveriesFromWire(r)));
        }
        return list;
    }

    private static IReadOnlyList<Delivery> DeliveriesFromWire(JsonElement w)
    {
        var list = new List<Delivery>();
        if (w.TryGetProperty("deliveries", out var raw) && raw.ValueKind == JsonValueKind.Array)
        {
            foreach (var d in raw.EnumerateArray())
            {
                if (d.ValueKind != JsonValueKind.Object) continue;
                var err = OptStr(d, "error");
                list.Add(new Delivery(Str(d, "device_id"), Str(d, "status"), string.IsNullOrEmpty(err) ? null : err));
            }
        }
        return list;
    }

    // Lenient readers: a missing or differently typed field reads as empty, never as an exception.
    private static string Str(JsonElement o, string key) => OptStr(o, key) ?? "";

    private static string? OptStr(JsonElement o, string key) =>
        o.ValueKind == JsonValueKind.Object && o.TryGetProperty(key, out var v) && v.ValueKind == JsonValueKind.String ? v.GetString() : null;

    private static int Int(JsonElement o, string key) => (int)Math.Clamp(Long(o, key), int.MinValue, int.MaxValue);

    private static long Long(JsonElement o, string key) =>
        o.ValueKind == JsonValueKind.Object && o.TryGetProperty(key, out var v) && v.ValueKind == JsonValueKind.Number
            ? (v.TryGetInt64(out var l) ? l : (long)v.GetDouble())
            : 0;

    private static DateTimeOffset? Time(JsonElement o, string key) =>
        OptStr(o, key) is { } s && DateTimeOffset.TryParse(s, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var t) ? t : null;

    // --- plumbing ------------------------------------------------------------

    private sealed record Call(string Method, string Path, byte[]? Body, string? ContentType, bool Retry, (string Name, string Value)[]? Headers = null);

    private sealed record Raw(int Status, string Text, string? RetryAfter);

    private Task<JsonElement> JsonCallAsync(string method, string path, JsonNode? body, CancellationToken ct) =>
        SendAsync(new Call(method, path, body is null ? null : Encode(body), body is null ? null : "application/json", Retry.IsIdempotent(method)), ct);

    private async Task<JsonElement> SendAsync(Call call, CancellationToken ct)
    {
        for (var attempt = 0; ; attempt++)
        {
            var res = await RoundTripAsync(call, ct).ConfigureAwait(false);
            if (res.Status is >= 200 and < 300)
            {
                if (string.IsNullOrWhiteSpace(res.Text)) return default;
                try
                {
                    using var doc = JsonDocument.Parse(res.Text);
                    return doc.RootElement.ValueKind == JsonValueKind.Object ? doc.RootElement.Clone() : default;
                }
                catch (JsonException e)
                {
                    throw new HanshoNetworkException(call.Method, call.Path, new FormatException("invalid JSON in response", e));
                }
            }

            var err = ApiError(call, res);
            if (!call.Retry || !Retry.IsRetryableStatus(res.Status) || attempt >= _maxRetries) throw err;
            var wait = err.RetryAfter ?? Retry.Backoff(attempt);
            if (wait > Retry.MaxWait) throw err;
            try
            {
                await _delay(wait, ct).ConfigureAwait(false);
            }
            catch (Exception e) when (e is not HanshoException)
            {
                throw new HanshoNetworkException(call.Method, call.Path, e);
            }
        }
    }

    private async Task<Raw> RoundTripAsync(Call call, CancellationToken ct)
    {
        // A request can be sent once, so every attempt builds a new one from the stored bytes.
        using var request = new HttpRequestMessage(new HttpMethod(call.Method), _baseUrl + call.Path);
        request.Headers.TryAddWithoutValidation("Authorization", "Bearer " + _apiKey);
        request.Headers.TryAddWithoutValidation("Accept", "application/json");
        request.Headers.TryAddWithoutValidation("User-Agent", _userAgent);
        if (call.Headers is { } extra)
            foreach (var (name, value) in extra) request.Headers.TryAddWithoutValidation(name, value);
        if (call.Body is not null)
        {
            request.Content = new ByteArrayContent(call.Body);
            request.Content.Headers.ContentType = new MediaTypeHeaderValue(call.ContentType!);
        }

        using var cts = CancellationTokenSource.CreateLinkedTokenSource(ct);
        var timed = _timeout > TimeSpan.Zero && _timeout != System.Threading.Timeout.InfiniteTimeSpan;
        if (timed) cts.CancelAfter(_timeout);
        try
        {
            using var response = await _http.SendAsync(request, HttpCompletionOption.ResponseContentRead, cts.Token).ConfigureAwait(false);
            var text = await response.Content.ReadAsStringAsync(cts.Token).ConfigureAwait(false);
            string? retryAfter = null;
            if (response.Headers.TryGetValues("Retry-After", out var values))
                foreach (var v in values) { retryAfter = v; break; }
            return new Raw((int)response.StatusCode, text, retryAfter);
        }
        catch (OperationCanceledException e) when (ct.IsCancellationRequested)
        {
            throw new HanshoNetworkException(call.Method, call.Path, e);
        }
        catch (OperationCanceledException) when (timed && cts.IsCancellationRequested)
        {
            throw new HanshoNetworkException(call.Method, call.Path, new TimeoutException($"timed out after {_timeout.TotalMilliseconds:0} ms"));
        }
        catch (Exception e) when (e is not HanshoException)
        {
            throw new HanshoNetworkException(call.Method, call.Path, Redact(e));
        }
    }

    /// <summary>Keeps the API key out of exception messages (it would only ever appear if a proxy echoed it).</summary>
    private Exception Redact(Exception e) =>
        e.Message.Contains(_apiKey, StringComparison.Ordinal) ? new Exception(e.Message.Replace(_apiKey, "[redacted]", StringComparison.Ordinal)) : e;

    private HanshoApiException ApiError(Call call, Raw res)
    {
        string? code = null, field = null, detail = null;
        try
        {
            using var doc = JsonDocument.Parse(res.Text);
            var o = doc.RootElement;
            if (o.ValueKind == JsonValueKind.Object)
            {
                code = OptStr(o, "error");
                field = OptStr(o, "field");
                detail = OptStr(o, "message")?.Replace(_apiKey, "[redacted]", StringComparison.Ordinal);
            }
        }
        catch (JsonException)
        {
            // Not JSON (a proxy's error page): the status alone.
        }
        return new HanshoApiException(call.Method, call.Path, res.Status, code, field, Retry.ParseRetryAfter(res.RetryAfter, DateTimeOffset.UtcNow), detail);
    }
}

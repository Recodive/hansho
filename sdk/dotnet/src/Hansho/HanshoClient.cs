using System;
using System.Collections.Generic;
using System.Net.Http;
using System.Net.Http.Headers;
using System.Text;
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
    public const string Version = HanshoSpec.Version;

    private const int MaxSoundBytes = 1 << 20;

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
        return Wire.ReadPairingTicket(w);
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
        await JsonCallAsync("PUT", $"/api/v1/channels/{Seg(id)}", Wire.WriteChannel(channel), cancellationToken).ConfigureAwait(false);
    }

    public async Task<Channel> GetChannelAsync(string id, CancellationToken cancellationToken = default)
    {
        var w = await JsonCallAsync("GET", $"/api/v1/channels/{Seg(id)}", null, cancellationToken).ConfigureAwait(false);
        return Wire.ReadChannel(w);
    }

    public async Task<IReadOnlyList<Channel>> ListChannelsAsync(CancellationToken cancellationToken = default)
    {
        var w = await JsonCallAsync("GET", "/api/v1/channels", null, cancellationToken).ConfigureAwait(false);
        return Wire.ListOf(w, JsonValueKind.Object, Wire.ReadChannel);
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
        var list = Wire.Arr(members, static m => JsonValue.Create(m));
        var w = await JsonCallAsync("PUT", $"/api/v1/groups/{Seg(id)}", new JsonObject { ["members"] = list }, cancellationToken).ConfigureAwait(false);
        return Wire.ReadGroup(w);
    }

    /// <summary>A missing group is a <see cref="HanshoApiException"/> with code <see cref="ErrorCodes.UnknownGroup"/>.</summary>
    public async Task<Group> GetGroupAsync(string id, CancellationToken cancellationToken = default)
    {
        var w = await JsonCallAsync("GET", $"/api/v1/groups/{Seg(id)}", null, cancellationToken).ConfigureAwait(false);
        return Wire.ReadGroup(w);
    }

    public async Task<IReadOnlyList<Group>> ListGroupsAsync(CancellationToken cancellationToken = default)
    {
        var w = await JsonCallAsync("GET", "/api/v1/groups", null, cancellationToken).ConfigureAwait(false);
        return Wire.ListOf(w, JsonValueKind.Object, Wire.ReadGroup);
    }

    /// <summary>An audience that still names the group matches nobody through it.</summary>
    public async Task DeleteGroupAsync(string id, CancellationToken cancellationToken = default)
    {
        await JsonCallAsync("DELETE", $"/api/v1/groups/{Seg(id)}", null, cancellationToken).ConfigureAwait(false);
    }

    // --- templates -----------------------------------------------------------

    /// <summary>
    /// Stores a template version; the version must increase. <paramref name="body"/> is the template tree: a
    /// dictionary, an anonymous or plain object, a <c>JsonElement</c> or a <c>JsonNode</c>.
    /// </summary>
    public async Task PutTemplateAsync(string id, int version, object body, CancellationToken cancellationToken = default)
    {
        var wire = new JsonObject { ["version"] = version, ["body"] = Wire.ToNode(body) };
        await JsonCallAsync("PUT", $"/api/v1/templates/{Seg(id)}", wire, cancellationToken).ConfigureAwait(false);
    }

    /// <summary>The stored template; its <c>Body</c> is the template tree as a <c>JsonElement</c>.</summary>
    public async Task<Template> GetTemplateAsync(string id, CancellationToken cancellationToken = default)
    {
        var w = await JsonCallAsync("GET", $"/api/v1/templates/{Seg(id)}", null, cancellationToken).ConfigureAwait(false);
        return Wire.ReadTemplate(w);
    }

    public async Task<IReadOnlyList<Template>> ListTemplatesAsync(CancellationToken cancellationToken = default)
    {
        var w = await JsonCallAsync("GET", "/api/v1/templates", null, cancellationToken).ConfigureAwait(false);
        return Wire.ListOf(w, JsonValueKind.Object, Wire.ReadTemplate);
    }

    public async Task DeleteTemplateAsync(string id, CancellationToken cancellationToken = default)
    {
        await JsonCallAsync("DELETE", $"/api/v1/templates/{Seg(id)}", null, cancellationToken).ConfigureAwait(false);
    }

    // --- sounds --------------------------------------------------------------

    /// <summary>Uploads a sound (<c>audio/x-caf</c>, <c>audio/wav</c> or <c>audio/aiff</c>, at most 1 MB).</summary>
    public async Task<SoundResult> PutSoundAsync(string id, string contentType, ReadOnlyMemory<byte> bytes, CancellationToken cancellationToken = default)
    {
        var path = $"/api/v1/sounds/{Seg(id)}";
        if (string.IsNullOrEmpty(contentType)) throw new ArgumentException("hansho: contentType is required", nameof(contentType));
        if (bytes.Length > MaxSoundBytes) throw new ArgumentException("hansho: sound exceeds 1 MB", nameof(bytes));
        var call = new Call("PUT", path, bytes.ToArray(), contentType, Retry: true);
        var w = await SendAsync(call, cancellationToken).ConfigureAwait(false);
        return Wire.ReadSoundResult(w);
    }

    /// <summary>A stored sound's metadata, not its bytes.</summary>
    public async Task<Sound> GetSoundAsync(string id, CancellationToken cancellationToken = default)
    {
        var w = await JsonCallAsync("GET", $"/api/v1/sounds/{Seg(id)}", null, cancellationToken).ConfigureAwait(false);
        return Wire.ReadSound(w);
    }

    public async Task<IReadOnlyList<Sound>> ListSoundsAsync(CancellationToken cancellationToken = default)
    {
        var w = await JsonCallAsync("GET", "/api/v1/sounds", null, cancellationToken).ConfigureAwait(false);
        return Wire.ListOf(w, JsonValueKind.Object, Wire.ReadSound);
    }

    public async Task DeleteSoundAsync(string id, CancellationToken cancellationToken = default)
    {
        await JsonCallAsync("DELETE", $"/api/v1/sounds/{Seg(id)}", null, cancellationToken).ConfigureAwait(false);
    }

    // --- messages ------------------------------------------------------------

    /// <summary>
    /// Sends a notification, or a page when the level is <see cref="Level.Page"/>. It is never retried
    /// automatically (a repeat would notify twice) unless the message has an <see cref="Message.IdempotencyKey"/>.
    /// </summary>
    public async Task<MessageResult> SendMessageAsync(Message message, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(message);
        var call = new Call("POST", "/api/v1/messages", Encode(Wire.WriteMessage(message)), "application/json", Retry: false);
        var headers = Wire.HeadersOfMessage(message);
        if (headers.Length > 0) call = call with { Retry = true, Headers = headers };
        var w = await SendAsync(call, cancellationToken).ConfigureAwait(false);
        return Wire.ReadMessageResult(w);
    }

    public async Task<MessageStatus> GetMessageAsync(string id, CancellationToken cancellationToken = default)
    {
        var w = await JsonCallAsync("GET", $"/api/v1/messages/{Seg(id)}", null, cancellationToken).ConfigureAwait(false);
        return Wire.ReadMessageStatus(w);
    }

    // --- live activities and feeds -------------------------------------------

    public async Task<ActivityStarted> StartActivityAsync(Activity activity, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(activity);
        var w = await JsonCallAsync("POST", "/api/v1/activities", Wire.WriteActivity(activity), cancellationToken).ConfigureAwait(false);
        return Wire.ReadActivityStarted(w);
    }

    /// <summary>Not retried automatically.</summary>
    public async Task<ActivityUpdated> UpdateActivityAsync(string id, ActivityUpdate update, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(update);
        var w = await JsonCallAsync("PATCH", $"/api/v1/activities/{Seg(id)}", Wire.WriteActivityUpdate(update), cancellationToken).ConfigureAwait(false);
        return Wire.ReadActivityUpdated(w);
    }

    public async Task EndActivityAsync(string id, CancellationToken cancellationToken = default)
    {
        await JsonCallAsync("DELETE", $"/api/v1/activities/{Seg(id)}", null, cancellationToken).ConfigureAwait(false);
    }

    public async Task<FeedResult> PutFeedAsync(string id, Feed feed, CancellationToken cancellationToken = default)
    {
        ArgumentNullException.ThrowIfNull(feed);
        var w = await JsonCallAsync("PUT", $"/api/v1/feeds/{Seg(id)}", Wire.WriteFeed(feed), cancellationToken).ConfigureAwait(false);
        return Wire.ReadFeedResult(w);
    }

    /// <summary>A feed's settings without its data. Leave <paramref name="externalUserId"/> null for the feed shared by all users.</summary>
    public async Task<FeedSettings> GetFeedAsync(string id, string? externalUserId = null, CancellationToken cancellationToken = default)
    {
        var path = $"/api/v1/feeds/{Seg(id)}";
        if (!string.IsNullOrEmpty(externalUserId)) path += "?external_user_id=" + Uri.EscapeDataString(externalUserId);
        var w = await JsonCallAsync("GET", path, null, cancellationToken).ConfigureAwait(false);
        return Wire.ReadFeedSettings(w);
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

    private static byte[] Encode(JsonNode body) =>
        Encoding.UTF8.GetBytes(body.ToJsonString(Wire.DataOptions));

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
                    return doc.RootElement.ValueKind is JsonValueKind.Object or JsonValueKind.Array ? doc.RootElement.Clone() : default;
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
                code = Wire.OptStr(o, "error");
                field = Wire.OptStr(o, "field");
                detail = Wire.OptStr(o, "message")?.Replace(_apiKey, "[redacted]", StringComparison.Ordinal);
            }
        }
        catch (JsonException)
        {
            // Not JSON (a proxy's error page): the status alone.
        }
        return new HanshoApiException(call.Method, call.Path, res.Status, code, field, Retry.ParseRetryAfter(res.RetryAfter, DateTimeOffset.UtcNow), detail);
    }
}

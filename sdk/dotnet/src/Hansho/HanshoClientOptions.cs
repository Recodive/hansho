using System;
using System.Net.Http;
using System.Threading;
using System.Threading.Tasks;

namespace Hansho;

/// <summary>Options for <see cref="HanshoClient"/>. A plain class, so it never prints the API key.</summary>
public sealed class HanshoClientOptions
{
    /// <summary>The hub's public URL. A path prefix is kept.</summary>
    public string BaseUrl { get; set; } = "";

    /// <summary>A sender API key from <c>hub apikey create</c>. Keep it on your server.</summary>
    public string ApiKey { get; set; } = "";

    /// <summary>
    /// Your own <see cref="HttpClient"/> (proxy, handler, tests). It is not disposed and its settings are not
    /// changed. Default: one the client creates and owns, without automatic redirects.
    /// </summary>
    public HttpClient? HttpClient { get; set; }

    /// <summary>Retries of GET/PUT/DELETE on 429 and 503. Default 3; 0 disables.</summary>
    public int MaxRetries { get; set; } = Retry.DefaultMaxRetries;

    /// <summary>Per-attempt timeout. Default 30 s; <see cref="TimeSpan.Zero"/> or <see cref="System.Threading.Timeout.InfiniteTimeSpan"/> disables.</summary>
    public TimeSpan Timeout { get; set; } = TimeSpan.FromSeconds(30);

    /// <summary>Default <c>hansho-dotnet/&lt;version&gt;</c>.</summary>
    public string? UserAgent { get; set; }

    /// <summary>Replaces the wait between retries; meant for tests.</summary>
    public Func<TimeSpan, CancellationToken, Task>? Delay { get; set; }

    public override string ToString() => $"HanshoClientOptions {{ BaseUrl = {BaseUrl} }}";
}

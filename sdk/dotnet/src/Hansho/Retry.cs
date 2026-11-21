using System;
using System.Globalization;
using System.Text.RegularExpressions;

namespace Hansho;

internal static class Retry
{
    internal const int DefaultMaxRetries = 3;
    private const double BaseBackoffMs = 250;
    private const double MaxBackoffMs = 8000;

    /// <summary>A longer Retry-After is surfaced to the caller instead of waited out.</summary>
    internal static readonly TimeSpan MaxWait = TimeSpan.FromSeconds(60);

    internal static bool IsIdempotent(string method) => method is "GET" or "PUT" or "DELETE";

    internal static bool IsRetryableStatus(int status) => status is 429 or 503;

    /// <summary>Exponential backoff with jitter in [d/2, d].</summary>
    internal static TimeSpan Backoff(int attempt)
    {
        var d = Math.Min(BaseBackoffMs * Math.Pow(2, Math.Min(attempt, 10)), MaxBackoffMs);
        return TimeSpan.FromMilliseconds(d / 2 + Random.Shared.NextDouble() * (d / 2));
    }

    private static readonly Regex Seconds = new(@"^\d+(\.\d+)?$", RegexOptions.CultureInvariant);

    /// <summary>Reads delay-seconds or an HTTP date; returns null when absent or unusable.</summary>
    internal static TimeSpan? ParseRetryAfter(string? value, DateTimeOffset now)
    {
        if (string.IsNullOrWhiteSpace(value)) return null;
        var v = value.Trim();
        if (Seconds.IsMatch(v))
        {
            return double.TryParse(v, NumberStyles.Float, CultureInfo.InvariantCulture, out var s) && s < TimeSpan.MaxValue.TotalSeconds
                ? TimeSpan.FromMilliseconds(Math.Round(s * 1000))
                : null;
        }
        if (DateTimeOffset.TryParse(v, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var at))
        {
            var wait = at - now;
            return wait > TimeSpan.Zero ? wait : TimeSpan.Zero;
        }
        return null;
    }
}

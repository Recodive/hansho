using System;

namespace Hansho;

/// <summary>Base of the exceptions the client throws after it has sent (or tried to send) a request.</summary>
public abstract class HanshoException : Exception
{
    protected HanshoException(string message, Exception? inner = null) : base(message, inner) { }
}

/// <summary>A non-2xx answer from the hub. The API key never appears in it.</summary>
public sealed class HanshoApiException : HanshoException
{
    public int Status { get; }

    /// <summary>The hub's error code, e.g. <c>invalid_request</c>; empty when the body was not JSON.</summary>
    public string Code { get; }

    /// <summary>The offending request field for validation failures.</summary>
    public string? Field { get; }

    /// <summary>The parsed <c>Retry-After</c>, when the hub sent one.</summary>
    public TimeSpan? RetryAfter { get; }

    /// <summary>The hub's validation message, when present.</summary>
    public string? Detail { get; }

    public HanshoApiException(string method, string path, int status, string? code = null, string? field = null, TimeSpan? retryAfter = null, string? detail = null)
        : base(Format(method, path, status, code, field, retryAfter))
    {
        Status = status;
        Code = code ?? "";
        Field = string.IsNullOrEmpty(field) ? null : field;
        RetryAfter = retryAfter is { } ra && ra > TimeSpan.Zero ? ra : null;
        Detail = string.IsNullOrEmpty(detail) ? null : detail;
    }

    private static string Format(string method, string path, int status, string? code, string? field, TimeSpan? retryAfter)
    {
        var text = $"hansho: {method} {path}: {status}";
        if (!string.IsNullOrEmpty(code)) text += $" {code}";
        if (!string.IsNullOrEmpty(field)) text += $" (field {field})";
        if (retryAfter is { } ra && ra > TimeSpan.Zero) text += $" (retry after {(long)ra.TotalMilliseconds} ms)";
        return text;
    }
}

/// <summary>
/// No HTTP answer arrived: the connection failed, the request timed out or the caller's token was cancelled.
/// See <see cref="Exception.InnerException"/>.
/// </summary>
public sealed class HanshoNetworkException : HanshoException
{
    public HanshoNetworkException(string method, string path, Exception cause)
        : base($"hansho: {method} {path}: {cause.Message}", cause) { }

    /// <summary>The caller cancelled the call (or its retry wait).</summary>
    public bool IsCancellation => InnerException is OperationCanceledException;

    /// <summary>The per-attempt timeout elapsed.</summary>
    public bool IsTimeout => InnerException is TimeoutException;
}

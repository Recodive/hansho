using System;
using System.Collections.Generic;
using System.Collections.Specialized;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Sockets;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using Xunit;

[assembly: CollectionBehavior(MaxParallelThreads = 2)]

namespace Hansho.Tests;

internal sealed record Recorded(string Method, string RawUrl, NameValueCollection Headers, string? ContentType, byte[] Body)
{
    public string BodyText => Encoding.UTF8.GetString(Body);
}

internal sealed class Reply
{
    public int Status { get; set; } = 200;
    public Dictionary<string, string> Headers { get; } = new();
    public string Body { get; set; } = "{}";
}

/// <summary>A local stand-in for the hub: a real HTTP server on loopback, so the path and headers are the wire form.</summary>
internal sealed class FakeHub : IDisposable
{
    public const string Key = "ck_test_SECRET_do_not_leak";

    private readonly HttpListener _listener = new();
    private readonly Action<Recorded, Reply>? _fill;
    private readonly List<Recorded> _requests = new();
    private readonly object _gate = new();
    private readonly Task _loop;

    public string Url { get; }
    public List<TimeSpan> Waits { get; } = new();
    public HanshoClient Client { get; }

    public IReadOnlyList<Recorded> Requests
    {
        get { lock (_gate) return _requests.ToList(); }
    }

    private FakeHub(string url, Action<Recorded, Reply>? fill)
    {
        Url = url;
        _fill = fill;
        _listener.Prefixes.Add(url + "/");
        _listener.Start();
        _loop = Task.Run(Loop);
        Client = NewClient();
    }

    public static FakeHub Start(Action<Recorded, Reply>? fill = null)
    {
        for (var i = 0; ; i++)
        {
            var url = $"http://127.0.0.1:{FreePort()}";
            try { return new FakeHub(url, fill); }
            catch (HttpListenerException) when (i < 20) { /* the port was taken in between: try another */ }
        }
    }

    public HanshoClient NewClient(Action<HanshoClientOptions>? configure = null, string? baseUrl = null)
    {
        var o = new HanshoClientOptions
        {
            BaseUrl = baseUrl ?? Url,
            ApiKey = Key,
            Delay = (d, ct) =>
            {
                lock (_gate) Waits.Add(d);
                ct.ThrowIfCancellationRequested();
                return Task.CompletedTask;
            },
        };
        configure?.Invoke(o);
        return new HanshoClient(o);
    }

    public static int FreePort()
    {
        var l = new TcpListener(IPAddress.Loopback, 0);
        l.Start();
        var port = ((IPEndPoint)l.LocalEndpoint).Port;
        l.Stop();
        return port;
    }

    private async Task Loop()
    {
        while (_listener.IsListening)
        {
            HttpListenerContext ctx;
            try { ctx = await _listener.GetContextAsync(); }
            catch (Exception) { return; }
            try
            {
                using var ms = new MemoryStream();
                await ctx.Request.InputStream.CopyToAsync(ms);
                var rec = new Recorded(ctx.Request.HttpMethod, ctx.Request.RawUrl ?? "", ctx.Request.Headers, ctx.Request.ContentType, ms.ToArray());
                lock (_gate) _requests.Add(rec);
                var reply = new Reply();
                _fill?.Invoke(rec, reply);
                ctx.Response.StatusCode = reply.Status;
                foreach (var (k, v) in reply.Headers) ctx.Response.Headers[k] = v;
                if (reply.Status != 204)
                {
                    ctx.Response.ContentType = "application/json";
                    var bytes = Encoding.UTF8.GetBytes(reply.Body);
                    ctx.Response.ContentLength64 = bytes.Length;
                    await ctx.Response.OutputStream.WriteAsync(bytes);
                }
                ctx.Response.Close();
            }
            catch (Exception) { /* a client that went away */ }
        }
    }

    public void Dispose()
    {
        try { _listener.Close(); } catch (ObjectDisposedException) { }
        Client.Dispose();
    }
}

/// <summary>A server that accepts connections and never answers.</summary>
internal sealed class SilentServer : IDisposable
{
    private readonly TcpListener _listener = new(IPAddress.Loopback, 0);
    private readonly List<TcpClient> _clients = new();

    public string Url { get; }

    public SilentServer()
    {
        _listener.Start();
        Url = $"http://127.0.0.1:{((IPEndPoint)_listener.LocalEndpoint).Port}";
        _ = Task.Run(async () =>
        {
            try
            {
                while (true)
                {
                    var c = await _listener.AcceptTcpClientAsync();
                    lock (_clients) _clients.Add(c);
                }
            }
            catch (Exception) { }
        });
    }

    public void Dispose()
    {
        _listener.Stop();
        lock (_clients) foreach (var c in _clients) c.Dispose();
    }
}

internal sealed record SpecOp(string Name, string Operation, string Method, string Path, JsonElement Request, int ResponseStatus, JsonElement ResponseBody);

internal sealed record SpecError(int Status, Dictionary<string, string> Headers, JsonElement Body, string? Raw, string Code, string Field, long RetryAfterMs);

internal static class Spec
{
    /// <summary>Finds sdk/spec by walking up from the test output, so it works wherever the build output goes.</summary>
    public static string SpecPath(string file)
    {
        var linked = Path.Combine(AppContext.BaseDirectory, "spec", file);
        if (File.Exists(linked)) return linked;
        for (var dir = new DirectoryInfo(AppContext.BaseDirectory); dir is not null; dir = dir.Parent)
        {
            var p = Path.Combine(dir.FullName, "spec", file);
            if (File.Exists(p)) return p;
        }
        throw new FileNotFoundException("spec/" + file);
    }

    public static JsonDocument Load(string file) => JsonDocument.Parse(File.ReadAllText(SpecPath(file)));

    public static IReadOnlyList<SpecOp> Operations { get; } = LoadOperations();
    public static IReadOnlyList<SpecError> Errors { get; } = LoadErrors();

    /// <summary>The requests every SDK must produce and the answers the hub gives: "x-conformance" of the OpenAPI document.</summary>
    private static JsonElement Conformance() => Load("openapi.json").RootElement.GetProperty("x-conformance").Clone();

    private static List<SpecOp> LoadOperations()
    {
        var root = Conformance();
        return root.GetProperty("operations").EnumerateArray().Select(o => new SpecOp(
            o.GetProperty("name").GetString()!,
            o.GetProperty("operation").GetString()!,
            o.GetProperty("method").GetString()!,
            o.GetProperty("path").GetString()!,
            o.GetProperty("request"),
            o.GetProperty("response").GetProperty("status").GetInt32(),
            o.GetProperty("response").GetProperty("body"))).ToList();
    }

    private static List<SpecError> LoadErrors()
    {
        var root = Conformance();
        return root.GetProperty("errors").EnumerateArray().Select(e => new SpecError(
            e.GetProperty("status").GetInt32(),
            e.GetProperty("headers").EnumerateObject().ToDictionary(p => p.Name, p => p.Value.GetString()!),
            e.GetProperty("body"),
            e.TryGetProperty("raw", out var raw) ? raw.GetString() : null,
            e.GetProperty("code").GetString()!,
            e.GetProperty("field").GetString()!,
            e.GetProperty("retry_after_ms").GetInt64())).ToList();
    }

    public static IEnumerable<object[]> OperationNames => Operations.Select(o => new object[] { o.Name });
    public static IEnumerable<object[]> ErrorIndexes => Errors.Select((_, i) => new object[] { i });
}

internal static class Json
{
    /// <summary>Structural equality: object members in any order, numbers by value.</summary>
    public static bool Equal(JsonElement a, JsonElement b)
    {
        if (a.ValueKind != b.ValueKind) return false;
        switch (a.ValueKind)
        {
            case JsonValueKind.Object:
                var ap = a.EnumerateObject().ToList();
                var bp = b.EnumerateObject().ToList();
                return ap.Count == bp.Count && ap.All(p => b.TryGetProperty(p.Name, out var other) && Equal(p.Value, other));
            case JsonValueKind.Array:
                var aa = a.EnumerateArray().ToList();
                var ba = b.EnumerateArray().ToList();
                return aa.Count == ba.Count && aa.Zip(ba).All(t => Equal(t.First, t.Second));
            case JsonValueKind.String:
                return a.GetString() == b.GetString();
            case JsonValueKind.Number:
                return a.GetDouble() == b.GetDouble();
            default:
                return true;
        }
    }

    public static JsonElement Parse(string json) => JsonDocument.Parse(json).RootElement.Clone();

    public static string Describe(object? value) => JsonSerializer.Serialize(value);
}

internal static class Wav
{
    /// <summary>0.05 s of 8 kHz 8-bit mono silence.</summary>
    public static byte[] Tiny()
    {
        var data = new byte[800];
        using var ms = new MemoryStream();
        using var w = new BinaryWriter(ms);
        w.Write(Encoding.ASCII.GetBytes("RIFF"));
        w.Write(36 + data.Length);
        w.Write(Encoding.ASCII.GetBytes("WAVEfmt "));
        w.Write(16);
        w.Write((short)1);
        w.Write((short)1);
        w.Write(8000);
        w.Write(8000);
        w.Write((short)1);
        w.Write((short)8);
        w.Write(Encoding.ASCII.GetBytes("data"));
        w.Write(data.Length);
        w.Write(data);
        w.Flush();
        return ms.ToArray();
    }
}

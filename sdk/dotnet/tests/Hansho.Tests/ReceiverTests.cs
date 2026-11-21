using System;
using System.Net;
using System.Net.Http;
using System.Net.Sockets;
using System.Text;
using System.Threading.Tasks;
using Xunit;

namespace Hansho.Tests;

/// <summary>The integration test's webhook receiver, checked without a hub.</summary>
public class ReceiverTests
{
    [Fact]
    public async Task StartsOnceAPortThatWasBusyIsFree()
    {
        var port = FakeHub.FreePort();
        var holder = new TcpListener(IPAddress.Loopback, port);
        holder.Start();
        var release = Task.Run(async () => { await Task.Delay(300); holder.Stop(); });

        using var rx = new IntegrationTests.Receiver($"127.0.0.1:{port}", "s");
        await release;

        var body = "{\"id\":\"1\",\"event\":\"message.acked\",\"created_at\":1,\"data\":{\"message_id\":\"m\"}}";
        var t = DateTimeOffset.UtcNow.ToUnixTimeSeconds();
        var mac = System.Security.Cryptography.HMACSHA256.HashData(Encoding.UTF8.GetBytes("s"), Encoding.UTF8.GetBytes($"{t}.{body}"));
        using var http = new HttpClient();
        using var req = new HttpRequestMessage(HttpMethod.Post, $"http://127.0.0.1:{port}/hook") { Content = new StringContent(body) };
        req.Headers.TryAddWithoutValidation(HanshoWebhook.SignatureHeader, $"t={t},v1={Convert.ToHexString(mac).ToLowerInvariant()}");
        using var res = await http.SendAsync(req);
        Assert.Equal(HttpStatusCode.OK, res.StatusCode);
        var ev = await rx.Wait<MessageAcked>(e => e.MessageId == "m");
        Assert.Equal(0, rx.Rejected);

        using var bad = new HttpRequestMessage(HttpMethod.Post, $"http://127.0.0.1:{port}/hook") { Content = new StringContent(body) };
        bad.Headers.TryAddWithoutValidation(HanshoWebhook.SignatureHeader, $"t={t},v1={new string('0', 64)}");
        using var rejected = await http.SendAsync(bad);
        Assert.Equal(HttpStatusCode.BadRequest, rejected.StatusCode);
        Assert.Equal(1, rx.Rejected);
    }
}

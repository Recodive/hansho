// Pair a user with the Hansho app and send them a page.
//
//   HANSHO_HUB_URL=https://hub.example.com HANSHO_API_KEY=ck_... dotnet run -- user-42
using Hansho;

var user = args.Length > 0 ? args[0] : throw new ArgumentException("usage: dotnet run -- <external user id>");
using var hansho = new HanshoClient(
    Environment.GetEnvironmentVariable("HANSHO_HUB_URL") ?? throw new InvalidOperationException("HANSHO_HUB_URL is not set"),
    Environment.GetEnvironmentVariable("HANSHO_API_KEY") ?? throw new InvalidOperationException("HANSHO_API_KEY is not set"));

await hansho.PutChannelAsync("oncall", new Channel { Name = "On call", DefaultLevel = Level.Active, MaxLevel = Level.Page });
var ticket = await hansho.CreatePairingTicketAsync(user);
Console.WriteLine($"Render this as a QR code and scan it in the Hansho app:\n {ticket.Qr}");
Console.Write("Press Enter once the app has paired... ");
Console.ReadLine();

var res = await hansho.SendMessageAsync(new Message
{
    ExternalUserId = user,
    Channel = "oncall",
    Level = Level.Page,
    Title = "Primary DB down",
    Body = "Failover did not complete",
});
Console.WriteLine($"sent {res.MessageId} to {res.Devices} device(s); it repeats until acknowledged");

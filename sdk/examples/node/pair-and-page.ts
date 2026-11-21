// Pair a user with the Hansho app and send them a page.
//
//   HANSHO_HUB_URL=https://hub.example.com HANSHO_API_KEY=ck_... node pair-and-page.ts user-42
import { createInterface } from "node:readline/promises";
import { Hansho } from "@recodive/hansho";

const hansho = new Hansho({ baseUrl: process.env.HANSHO_HUB_URL!, apiKey: process.env.HANSHO_API_KEY! });
const user = process.argv[2]!;

await hansho.putChannel("oncall", { name: "On call", defaultLevel: "active", maxLevel: "page" });
const ticket = await hansho.createPairingTicket(user);
console.log("Render this as a QR code and scan it in the Hansho app:\n", ticket.qr);
await createInterface({ input: process.stdin, output: process.stdout }).question("Press Enter once the app has paired... ");

const res = await hansho.sendMessage({
  externalUserId: user,
  channel: "oncall",
  level: "page",
  title: "Primary DB down",
  body: "Failover did not complete",
});
console.log(`sent ${res.messageId} to ${res.devices} device(s); it repeats until acknowledged`);

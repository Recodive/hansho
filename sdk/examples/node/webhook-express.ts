// Receive Hansho webhooks with Express. The route needs the RAW body, so it uses
// express.raw() and is registered before any express.json().
import express from "express";
import { verifyWebhook, WebhookError } from "@recodive/hansho";

const app = express();

app.post("/hansho-hook", express.raw({ type: "*/*" }), (req, res) => {
  try {
    const event = verifyWebhook({
      secret: process.env.HANSHO_WEBHOOK_SECRET!,
      header: req.get("X-Hansho-Webhook-Signature")!,
      body: req.body, // a Buffer
    });
    if (event.type === "message.acked") console.log("acknowledged:", event.data.messageId);
    res.sendStatus(200);
  } catch (e) {
    res.sendStatus(e instanceof WebhookError ? 400 : 500);
  }
});

app.use(express.json()); // after the webhook route
app.listen(3000);

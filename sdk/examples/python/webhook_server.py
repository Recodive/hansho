"""Receive and verify the hub's webhooks with nothing but the standard library.

    HANSHO_WEBHOOK_SECRET=whsec_... python webhook_server.py     # then set HUB_WEBHOOK_URL=http://host:8080/
"""

import os
from http.server import BaseHTTPRequestHandler, HTTPServer

from hansho import WEBHOOK_SIGNATURE_HEADER, WebhookError, verify_webhook

SECRET = os.environ["HANSHO_WEBHOOK_SECRET"]


class Hook(BaseHTTPRequestHandler):
    def do_POST(self) -> None:
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))  # the raw bytes
        try:
            event = verify_webhook(secret=SECRET, header=self.headers.get(WEBHOOK_SIGNATURE_HEADER), body=body)
        except WebhookError:
            self.send_response(400)  # non-2xx: the hub retries
        else:
            if event.type == "message.acked":
                print("acknowledged:", event.data.message_id)
            elif event.type == "action":
                print("button", event.data.action_id, "on", event.data.message_id)
            elif event.type == "unknown":
                print("newer hub event:", event.name)
            self.send_response(200)
        self.send_header("Content-Length", "0")
        self.end_headers()


if __name__ == "__main__":
    HTTPServer(("", 8080), Hook).serve_forever()

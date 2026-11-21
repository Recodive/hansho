"""Pair a user with the Hansho app and send them a page.

    pip install hansho
    HANSHO_HUB_URL=https://hub.example.com HANSHO_API_KEY=ck_... python pair_and_page.py user-42
"""

import os
import sys

from hansho import Channel, Hansho, Message

hansho = Hansho(os.environ["HANSHO_HUB_URL"], os.environ["HANSHO_API_KEY"])
user = sys.argv[1]

hansho.put_channel("oncall", Channel(name="On call", default_level="active", max_level="page"))
ticket = hansho.create_pairing_ticket(user)
print("Render this as a QR code and scan it in the Hansho app:\n", ticket.qr)
input("Press Enter once the app has paired... ")

res = hansho.send_message(
    Message(
        external_user_id=user,
        channel="oncall",
        level="page",
        title="Primary DB down",
        body="Failover did not complete",
    )
)
print(f"sent {res.message_id} to {res.devices} device(s); it repeats until acknowledged")

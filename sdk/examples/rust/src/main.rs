// Pair a user with the Hansho app and send them a page.
//
//   HANSHO_HUB_URL=https://hub.example.com HANSHO_API_KEY=ck_... cargo run -- user-42
use std::io::{self, BufRead, Write};

use hansho::{Channel, Client, Level, Message};

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let hub_url = std::env::var("HANSHO_HUB_URL")?;
    let api_key = std::env::var("HANSHO_API_KEY")?;
    let user = std::env::args().nth(1).ok_or("usage: pair-and-page <user id>")?;
    let hansho = Client::new(hub_url, api_key)?;

    hansho.put_channel(
        "oncall",
        &Channel { name: "On call".into(), default_level: Level::Active, max_level: Level::Page, ..Default::default() },
    )?;
    let ticket = hansho.create_pairing_ticket(&user)?;
    println!("Render this as a QR code and scan it in the Hansho app:\n {}", ticket.qr);
    print!("Press Enter once the app has paired... ");
    io::stdout().flush()?;
    io::stdin().lock().read_line(&mut String::new())?;

    let res = hansho.send_message(&Message {
        level: Some(Level::Page),
        ..Message::new(&user, "oncall", "Primary DB down", "Failover did not complete")
    })?;
    println!("sent {} to {} device(s); it repeats until acknowledged", res.message_id, res.devices);
    Ok(())
}

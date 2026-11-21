//! End to end against the real hub; skipped unless HANSHO_HUB_URL is set. Run it through
//! sdk/scripts/run-integration.sh, which starts the hub binary and a stub relay and exports:
//!
//!   HANSHO_HUB_URL, HANSHO_API_KEY, HANSHO_WEBHOOK_SECRET, HANSHO_WEBHOOK_ADDR, HANSHO_FAKEDEVICE_BIN
//!
//! The webhook receiver runs inside this test on HANSHO_WEBHOOK_ADDR and verifies every
//! delivery with this SDK's own verifier.

mod common;

use std::env;
use std::io::Write;
use std::net::TcpListener;
use std::path::PathBuf;
use std::process::Command;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant, SystemTime};

use common::read_request;
use hansho::*;

fn step(name: &str) {
    println!("integration: {name}");
}

fn fake_device(args: &[&str]) -> String {
    let bin = env::var("HANSHO_FAKEDEVICE_BIN").expect("HANSHO_FAKEDEVICE_BIN");
    let out = Command::new(bin).args(args).output().expect("run fakedevice");
    assert!(out.status.success(), "fakedevice {args:?}: {}", String::from_utf8_lossy(&out.stderr));
    String::from_utf8_lossy(&out.stdout).into_owned()
}

/// 0.05 s of 8 kHz 8-bit mono silence.
fn tiny_wav() -> Vec<u8> {
    let data = vec![0u8; 800];
    let mut h = Vec::new();
    h.extend_from_slice(b"RIFF");
    h.extend_from_slice(&(36 + data.len() as u32).to_le_bytes());
    h.extend_from_slice(b"WAVEfmt ");
    h.extend_from_slice(&16u32.to_le_bytes());
    h.extend_from_slice(&1u16.to_le_bytes()); // PCM
    h.extend_from_slice(&1u16.to_le_bytes()); // mono
    h.extend_from_slice(&8000u32.to_le_bytes());
    h.extend_from_slice(&8000u32.to_le_bytes());
    h.extend_from_slice(&1u16.to_le_bytes());
    h.extend_from_slice(&8u16.to_le_bytes());
    h.extend_from_slice(b"data");
    h.extend_from_slice(&(data.len() as u32).to_le_bytes());
    h.extend_from_slice(&data);
    h
}

/// Receives the hub's webhooks and verifies them with the SDK.
struct Receiver {
    events: Arc<Mutex<Vec<Event>>>,
    rejected: Arc<AtomicUsize>,
    stop: Arc<AtomicBool>,
    thread: Option<thread::JoinHandle<()>>,
}

impl Receiver {
    fn listen(addr: &str, secret: &str) -> Receiver {
        let mut listener = None;
        for i in 0.. {
            match TcpListener::bind(addr) {
                Ok(l) => {
                    listener = Some(l);
                    break;
                }
                Err(e) if i >= 50 => panic!("listen on {addr}: {e}"),
                Err(_) => thread::sleep(Duration::from_millis(100)),
            }
        }
        let listener = listener.unwrap();
        listener.set_nonblocking(true).unwrap();
        let (events, rejected, stop) = (Arc::new(Mutex::new(Vec::new())), Arc::new(AtomicUsize::new(0)), Arc::new(AtomicBool::new(false)));
        let (ev, rej, stp) = (events.clone(), rejected.clone(), stop.clone());
        let verifier = Verifier::new(secret);
        let thread = thread::spawn(move || {
            while !stp.load(Ordering::Relaxed) {
                let Ok((mut conn, _)) = listener.accept() else {
                    thread::sleep(Duration::from_millis(10));
                    continue;
                };
                conn.set_nonblocking(false).ok();
                let Some(req) = read_request(&mut conn) else { continue };
                let header = req.headers.get(&WEBHOOK_SIGNATURE_HEADER.to_ascii_lowercase()).map(String::as_str).unwrap_or("");
                let status = match verifier.verify(header, &req.body) {
                    Ok(e) => {
                        ev.lock().unwrap().push(e);
                        200
                    }
                    Err(_) => {
                        rej.fetch_add(1, Ordering::SeqCst);
                        400
                    }
                };
                let _ = conn.write_all(format!("HTTP/1.1 {status} X\r\nContent-Length: 0\r\nConnection: close\r\n\r\n").as_bytes());
            }
        });
        Receiver { events, rejected, stop, thread: Some(thread) }
    }

    /// Waits for a webhook the predicate accepts.
    fn wait(&self, what: &str, pred: impl Fn(&Event) -> bool) -> Event {
        common::eventually(Duration::from_secs(25), || self.events.lock().unwrap().iter().find(|e| pred(e)).cloned())
            .unwrap_or_else(|| panic!("no {what} webhook arrived"))
    }
}

impl Drop for Receiver {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
        if let Some(t) = self.thread.take() {
            let _ = t.join();
        }
    }
}

fn wait_status(c: &Client, id: &str, want: &[&str]) -> MessageStatus {
    let deadline = Instant::now() + Duration::from_secs(25);
    let mut last = String::new();
    while Instant::now() < deadline {
        let ms = c.get_message(id).unwrap();
        if want.contains(&ms.status.as_str()) {
            return ms;
        }
        last = ms.status;
        thread::sleep(Duration::from_millis(200));
    }
    panic!("message {id}: status {last}, wanted one of {want:?}");
}

#[track_caller]
fn expect_api<T: std::fmt::Debug>(r: Result<T>, status: u16, code: &str, field: Option<&str>) {
    match r {
        Err(Error::Api(e)) => {
            assert_eq!((e.status, e.code.as_str()), (status, code), "{e}");
            if let Some(f) = field {
                assert_eq!(e.field.as_deref(), Some(f), "{e}");
            }
        }
        other => panic!("wanted API error {status} {code}, got {other:?}"),
    }
}

#[test]
fn runs_the_whole_flow_against_the_real_hub() {
    let Ok(hub_url) = env::var("HANSHO_HUB_URL") else {
        println!("integration: SKIPPED (HANSHO_HUB_URL is not set; run sdk/scripts/run-integration.sh)");
        return;
    };
    let var = |k: &str| env::var(k).unwrap_or_else(|_| panic!("{k} is not set"));
    let rx = Receiver::listen(&var("HANSHO_WEBHOOK_ADDR"), &var("HANSHO_WEBHOOK_SECRET"));
    let c = Client::new(&hub_url, var("HANSHO_API_KEY")).unwrap();
    let p = "rust";
    let user = format!("u-{p} ü/42?x");
    let (ch, tpl, snd) = (format!("{p}-ops"), format!("{p}-status"), format!("{p}-ding"));
    let state_dir: PathBuf = env::temp_dir().join(format!("hansho-rust-it-{}", std::process::id()));
    std::fs::create_dir_all(&state_dir).unwrap();
    let state = state_dir.join("device.json");
    let state = state.to_str().unwrap();

    step("errors");
    expect_api(Client::new(&hub_url, "ck_not_a_key").unwrap().create_pairing_ticket("x"), 401, "unauthorized", None);
    expect_api(c.get_channel("no-such-channel"), 404, "unknown_channel", None);
    expect_api(c.send_message(&Message::new(&user, &ch, "", "b")), 400, "invalid_request", Some("title"));

    step("catalog: sound, template, channel");
    let sound = c.put_sound(&snd, "audio/wav", &tiny_wav()).unwrap();
    assert_eq!(sound.id, snd);
    let template = json!({"type": "vstack", "tint": {"light": "#0A84FF", "dark": "#64D2FF"}, "children": [{"type": "text", "style": "headline", "text": "{{title}}"}, {"type": "spacer"}]});
    c.put_template(&tpl, 1, &template).unwrap();
    expect_api(c.put_template(&tpl, 1, &template), 409, "version_conflict", None);
    c.put_template(&tpl, 2, &template).unwrap();
    let ack = Action { id: "ack".into(), title: "Acknowledge".into(), ack: Some(true), kind: None };
    c.put_channel(
        &ch,
        &Channel {
            name: format!("Operations {p}"),
            default_level: Level::Active,
            max_level: Level::Page,
            sound: Some(snd.clone()),
            template_id: Some(tpl.clone()),
            thread: Some("ops".into()),
            actions: vec![ack.clone()],
        },
    )
    .unwrap();
    let got = c.get_channel(&ch).unwrap();
    assert_eq!(got.name, format!("Operations {p}"));
    assert_eq!(got.max_level, Level::Page);
    assert_eq!(got.sound.as_deref(), Some(snd.as_str()));
    assert_eq!(got.template_id.as_deref(), Some(tpl.as_str()));
    assert_eq!(got.actions, vec![ack]);
    let bad = Channel { name: "n".into(), default_level: Level::Active, max_level: Level::Page, ..Default::default() };
    expect_api(c.put_channel("bad id", &bad), 400, "invalid_request", Some("id"));

    step("pairing through the fake device");
    let tk = c.create_pairing_ticket(&user).unwrap();
    assert!(tk.qr.starts_with("hansho://pair?") && tk.qr.contains(&tk.ticket));
    assert!(tk.expires_at > SystemTime::now());
    let device_name = format!("SDK phone {p}");
    fake_device(&["pair", "-hub", &hub_url, "-ticket", &tk.ticket, "-state", state, "-name", &device_name]);
    rx.wait(
        "pairing.created",
        |e| matches!(&e.data, EventData::PairingCreated(d) if d.external_user_id == user && d.device_name == device_name),
    );

    step("messages at every level");
    for level in [Level::Passive, Level::Active, Level::TimeSensitive, Level::Critical] {
        let res = c
            .send_message(&Message {
                level: Some(level.clone()),
                data: Some(json!({"disk": 97})),
                sound: Some(snd.clone()),
                collapse_id: Some(format!("disk-{level}")),
                ..Message::new(&user, &ch, "Disk full", "db-1 is at 97%")
            })
            .unwrap();
        assert_eq!(res.level, level);
        assert_eq!(res.devices, 1);
        assert!(res.message_id.starts_with("msg_"));
        let ms = wait_status(&c, &res.message_id, &["delivered"]);
        assert_eq!(ms.level, level);
        assert_eq!(ms.deliveries.len(), 1);
        assert_eq!(ms.deliveries[0].status, "sent");
        rx.wait(
            "message.delivered",
            |e| matches!(&e.data, EventData::MessageDelivered(d) if d.message_id == res.message_id && d.level == level && d.devices == 1),
        );
    }
    let def = c.send_message(&Message::new(&user, &ch, "Default", "b")).unwrap();
    assert_eq!(def.level, Level::Active);

    step("page and its acknowledgement");
    let page = c
        .send_message(&Message {
            level: Some(Level::Page),
            page: Some(PageOptions { interval_seconds: Some(60), max_retries: Some(3) }),
            ..Message::new(&user, &ch, "Primary DB down", "Failover failed")
        })
        .unwrap();
    assert_eq!(page.level, Level::Page);
    wait_status(&c, &page.message_id, &["paging"]);
    fake_device(&["ack", "-state", state, "-message", &page.message_id]);
    let acked = wait_status(&c, &page.message_id, &["acked"]);
    assert!(acked.acked_at.is_some_and(|t| t > SystemTime::UNIX_EPOCH));
    let ev = rx.wait("message.acked", |e| matches!(&e.data, EventData::MessageAcked(d) if d.message_id == page.message_id));
    assert!(matches!(&ev.data, EventData::MessageAcked(d) if d.external_user_id == user));
    rx.wait("action", |e| matches!(&e.data, EventData::Action(d) if d.message_id == page.message_id && d.action_id == "ack"));

    step("live activity");
    let started = c
        .start_activity(&Activity {
            external_user_id: user.clone(),
            channel: ch.clone(),
            template_id: tpl.clone(),
            data: json!({"title": "Deploy", "progress": 0.1}),
            alert: Some(ActivityAlert { title: Some("Deploy started".into()), body: None }),
        })
        .unwrap();
    assert_eq!(started.devices, 1);
    assert!(started.activity_id.starts_with("act_"));
    fake_device(&["activity-token", "-state", state, "-activity", &started.activity_id]);
    let up = c
        .update_activity(
            &started.activity_id,
            &ActivityUpdate { data: json!({"title": "Deploy", "progress": 0.6}), priority: Some(10), alert: None },
        )
        .unwrap();
    assert_eq!(up.activity_id, started.activity_id);
    assert!(up.update >= 1);
    c.end_activity(&started.activity_id).unwrap();
    expect_api(
        c.update_activity(&started.activity_id, &ActivityUpdate { data: json!({"title": "x"}), ..Default::default() }),
        409,
        "activity_ended",
        None,
    );

    step("feeds");
    let own = c
        .put_feed(
            &format!("{p}-uptime"),
            &Feed {
                external_user_id: Some(user.clone()),
                template_id: tpl.clone(),
                data: json!({"title": "up", "ms": 41}),
                ..Default::default()
            },
        )
        .unwrap();
    assert_eq!(own.feed_id, format!("{p}-uptime"));
    assert!(own.devices >= 1);
    let shared = c
        .put_feed(&format!("{p}-shared"), &Feed { template_id: tpl.clone(), data: json!({"title": "all"}), ..Default::default() })
        .unwrap();
    assert!(shared.devices >= 1);
    expect_api(
        c.put_feed(&format!("{p}-bad"), &Feed { template_id: "missing".into(), data: json!(1), ..Default::default() }),
        400,
        "invalid_request",
        Some("template_id"),
    );

    step("unpair, then nobody is left to deliver to");
    c.unpair(&user).unwrap();
    rx.wait("pairing.revoked", |e| matches!(&e.data, EventData::PairingRevoked(d) if d.external_user_id == user && d.by == "sender"));
    let none = c.send_message(&Message::new(&user, &ch, "Anyone?", "b")).unwrap();
    assert_eq!(none.devices, 0);
    wait_status(&c, &none.message_id, &["undeliverable"]);
    rx.wait("message.undeliverable", |e| matches!(&e.data, EventData::MessageUndeliverable(d) if d.message_id == none.message_id));
    c.delete_channel(&ch).unwrap();
    expect_api(c.get_channel(&ch), 404, "unknown_channel", None);

    assert_eq!(rx.rejected.load(Ordering::SeqCst), 0, "the SDK rejected a webhook the hub signed");
    let seen = rx.events.lock().unwrap().len();
    assert!(seen > 0);
    let _ = std::fs::remove_dir_all(&state_dir);
    println!("integration: OK ({seen} signed webhooks verified)");
}

//! Every case of `x-conformance` in sdk/spec/openapi.json: the exact request the SDK produces and the typed result, plus a
//! check that the client has a method for every operationId of the spec.

mod common;

use std::collections::BTreeMap;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use common::{base64_decode, load_spec, start, Reply, TEST_KEY};
use hansho::*;
use serde_json::{json, Value};

const WAV: &str = "UklGRgAAAABXQVZF";

fn at(secs: u64) -> SystemTime {
    UNIX_EPOCH + Duration::from_secs(secs)
}

fn delivery(device: &str, status: &str, error: &str) -> Delivery {
    Delivery { device_id: device.into(), status: status.into(), error: error.into() }
}

type Call = Box<dyn Fn(&Client)>;

/// The `x-conformance` object of the OpenAPI document: the cases (`operations`) and the `errors`.
fn conformance() -> Value {
    load_spec("openapi.json")["x-conformance"].clone()
}

/// `listTemplates` -> `list_templates`.
fn snake(id: &str) -> String {
    id.chars().fold(String::new(), |mut out, c| {
        if c.is_ascii_uppercase() {
            out.push('_');
        }
        out.push(c.to_ascii_lowercase());
        out
    })
}

/// Every operation of the spec: the SDK call, asserting the value it must produce.
fn calls() -> BTreeMap<&'static str, Call> {
    let mut m: BTreeMap<&'static str, Call> = BTreeMap::new();
    m.insert(
        "createPairingTicket",
        Box::new(|c| {
            let got = c.create_pairing_ticket("user-42").unwrap();
            let want = PairingTicket {
                ticket: "tk_abc".into(),
                qr: "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=tk_abc&fp=ff00".into(),
                expires_at: at(1_794_139_800), // 2026-11-08T12:10:00Z
            };
            assert_eq!(got, want);
        }),
    );
    m.insert(
        "createPairingTicketWithName",
        Box::new(|c| {
            let got = c.create_pairing_ticket_with_name("user-42", "Zoë 日本語").unwrap();
            let want = PairingTicket {
                ticket: "tk_def".into(),
                qr: "hansho://pair?hub=https%3A%2F%2Fhub.example.com&t=tk_def&fp=ff00".into(),
                expires_at: at(1_794_139_800),
            };
            assert_eq!(got, want);
        }),
    );
    m.insert("setUserDisplayName", Box::new(|c| c.set_user_display_name("user-42", "Zoë 日本語").unwrap()));
    m.insert("clearUserDisplayName", Box::new(|c| c.set_user_display_name("user 42", "").unwrap()));
    m.insert("unpair", Box::new(|c| c.unpair("user-42").unwrap()));
    m.insert(
        "putChannel",
        Box::new(|c| {
            let ch = Channel {
                id: None,
                name: "Operations".into(),
                default_level: Level::Active,
                max_level: Level::Critical,
                sound: Some("ding".into()),
                template_id: Some("status".into()),
                thread: Some("ops-thread".into()),
                actions: vec![
                    Action { id: "ack".into(), title: "Acknowledge".into(), ack: Some(true), ..Default::default() },
                    Action { id: "reply".into(), title: "Reply".into(), kind: Some("text".into()), ..Default::default() },
                    Action {
                        id: "runbook".into(),
                        title: "Runbook".into(),
                        kind: Some("url".into()),
                        url: Some("https://example.com/runbook".into()),
                        ..Default::default()
                    },
                ],
                audience: None,
            };
            c.put_channel("ops", &ch).unwrap();
        }),
    );
    m.insert(
        "getChannel",
        Box::new(|c| {
            let want = Channel {
                id: Some("ops".into()),
                name: "Operations".into(),
                default_level: Level::Active,
                max_level: Level::Critical,
                sound: None,
                template_id: None,
                thread: Some("ops".into()),
                actions: vec![],
                audience: None,
            };
            assert_eq!(c.get_channel("ops").unwrap(), want);
        }),
    );
    m.insert("deleteChannel", Box::new(|c| c.delete_channel("ops").unwrap()));
    m.insert(
        "putTemplate",
        Box::new(|c| {
            let body = json!({"type": "vstack", "children": [{"type": "text", "style": "headline", "text": "{{title}}"}]});
            c.put_template("status", 2, &body).unwrap();
        }),
    );
    m.insert(
        "putSound",
        Box::new(|c| {
            let got = c.put_sound("ding", "audio/wav", &base64_decode(WAV)).unwrap();
            assert_eq!(got, SoundResult { id: "ding".into(), filename: "ding.wav".into(), sha256: "00".into(), size: 12 });
        }),
    );
    m.insert(
        "sendMessage",
        Box::new(|c| {
            let msg = Message {
                external_user_id: Some("user-42".into()),
                channel: "ops".into(),
                level: Some(Level::TimeSensitive),
                title: "Disk full".into(),
                subtitle: Some("db-1".into()),
                body: "db-1 is at 97%".into(),
                image_url: Some("https://example.com/i.png".into()),
                template_id: Some("status".into()),
                data: Some(json!({"disk": 97, "nested": {"snake_case_key": [1, 2]}})),
                sound: Some("ding".into()),
                actions: vec![Action { id: "ack".into(), title: "Ack".into(), ack: Some(true), ..Default::default() }],
                collapse_id: Some("disk-db-1".into()),
                ..Default::default()
            };
            let want = MessageResult {
                message_id: "msg_1".into(),
                level: Level::TimeSensitive,
                devices: 2,
                status: "queued".into(),
                deliveries: vec![delivery("dev_1", "queued", ""), delivery("dev_2", "queued", "")],
                recipients: vec![],
            };
            assert_eq!(c.send_message(&msg).unwrap(), want);
        }),
    );
    m.insert(
        "sendPage",
        Box::new(|c| {
            let msg = Message {
                level: Some(Level::Page),
                page: Some(PageOptions { interval_seconds: Some(60), max_retries: Some(3), ..Default::default() }),
                ..Message::new("user-42", "ops", "Primary DB down", "Failover did not complete")
            };
            let want = MessageResult {
                message_id: "msg_2".into(),
                level: Level::Page,
                devices: 1,
                status: "queued".into(),
                deliveries: vec![delivery("dev_1", "queued", "")],
                recipients: vec![],
            };
            assert_eq!(c.send_message(&msg).unwrap(), want);
        }),
    );
    m.insert(
        "sendPageEach",
        Box::new(|c| {
            let msg = Message {
                external_user_ids: vec!["alice".into(), "bob".into()],
                level: Some(Level::Page),
                page: Some(PageOptions { interval_seconds: Some(60), max_retries: Some(3), ack: Some(PageAck::Each) }),
                ..Message::new("", "ops", "Primary DB down", "Failover did not complete")
            };
            let got = c.send_message(&msg).unwrap();
            assert_eq!(got.level, Level::Page);
            let want = vec![
                Recipient {
                    external_user_id: "alice".into(),
                    message_id: "msg_3".into(),
                    status: "queued".into(),
                    devices: 1,
                    deliveries: vec![delivery("dev_1", "queued", "")],
                },
                Recipient {
                    external_user_id: "bob".into(),
                    message_id: "msg_4".into(),
                    status: "queued".into(),
                    devices: 1,
                    deliveries: vec![delivery("dev_2", "queued", "")],
                },
            ];
            assert_eq!(got.recipients, want);
        }),
    );
    m.insert(
        "getMessageAckedNoName",
        Box::new(|c| {
            let want = MessageStatus {
                message_id: "msg_3".into(),
                status: "acked".into(),
                level: Level::Page,
                attempts: 1,
                deliveries: vec![delivery("dev_1", "sent", "")],
                acked_at: Some(at(1_794_139_500)),
                acked_by: "alice".into(),
                acked_by_name: String::new(),
            };
            assert_eq!(c.get_message("msg_3").unwrap(), want);
        }),
    );
    m.insert(
        "getMessage",
        Box::new(|c| {
            let want = MessageStatus {
                message_id: "msg_2".into(),
                status: "acked".into(),
                level: Level::Page,
                attempts: 2,
                deliveries: vec![delivery("dev_1", "sent", ""), delivery("dev_2", "failed", "unregistered")],
                acked_at: Some(at(1_794_139_500)), // 2026-11-08T12:05:00Z
                acked_by: "user-42".into(),
                acked_by_name: "Zoë 日本語".into(),
            };
            assert_eq!(c.get_message("msg_2").unwrap(), want);
        }),
    );
    m.insert(
        "startActivity",
        Box::new(|c| {
            let a = Activity {
                external_user_id: "user-42".into(),
                channel: "ops".into(),
                template_id: "deploy".into(),
                data: json!({"progress": 0.1}),
                alert: Some(ActivityAlert { title: Some("Deploy started".into()), body: Some("v1.2.3".into()) }),
            };
            assert_eq!(c.start_activity(&a).unwrap(), ActivityStarted { activity_id: "act_1".into(), devices: 1 });
        }),
    );
    m.insert(
        "updateActivity",
        Box::new(|c| {
            let u = ActivityUpdate {
                data: json!({"progress": 0.6}),
                priority: Some(10),
                alert: Some(ActivityAlert { title: Some("Halfway".into()), body: Some("60%".into()) }),
            };
            assert_eq!(c.update_activity("act_1", &u).unwrap(), ActivityUpdated { activity_id: "act_1".into(), update: 3 });
        }),
    );
    m.insert("endActivity", Box::new(|c| c.end_activity("act_1").unwrap()));
    m.insert(
        "putFeed",
        Box::new(|c| {
            let f = Feed {
                external_user_id: Some("user-42".into()),
                template_id: "status".into(),
                title: Some("Uptime".into()),
                data: json!({"up": true, "ms": 41}),
                ..Default::default()
            };
            assert_eq!(c.put_feed("uptime", &f).unwrap(), FeedResult { feed_id: "uptime".into(), devices: 2 });
        }),
    );
    m.insert(
        "putGroup",
        Box::new(|c| {
            let got = c.put_group("oncall", &["alice", "bob"]).unwrap();
            assert_eq!(got, Group { id: "oncall".into(), members: vec!["alice".into(), "bob".into()] });
        }),
    );
    m.insert(
        "getGroup",
        Box::new(|c| {
            let got = c.get_group("oncall").unwrap();
            assert_eq!(got, Group { id: "oncall".into(), members: vec!["alice".into(), "bob".into()] });
        }),
    );
    m.insert("deleteGroup", Box::new(|c| c.delete_group("oncall").unwrap()));
    m.insert("kick", Box::new(|c| c.kick("user-42").unwrap()));
    m.insert(
        "putChannelWithAudience",
        Box::new(|c| {
            let ch = Channel {
                name: "Operations".into(),
                default_level: Level::Active,
                max_level: Level::Critical,
                audience: Some(only()),
                ..Default::default()
            };
            c.put_channel("ops", &ch).unwrap();
        }),
    );
    m.insert(
        "putFeedWithChannel",
        Box::new(|c| {
            let f = Feed {
                template_id: "status".into(),
                channel: Some("ops".into()),
                audience: Some(only()),
                data: json!({"up": true}),
                ..Default::default()
            };
            assert_eq!(c.put_feed("uptime", &f).unwrap(), FeedResult { feed_id: "uptime".into(), devices: 1 });
        }),
    );
    m.insert(
        "sendToAudience",
        Box::new(|c| {
            let msg = Message { to_audience: Some(true), ..Message::new("", "ops", "Deploy done", "v1.2.3 is live") };
            let want = MessageResult {
                level: Level::Active,
                recipients: vec![
                    Recipient {
                        external_user_id: "alice".into(),
                        message_id: "msg_7".into(),
                        status: "queued".into(),
                        devices: 1,
                        deliveries: vec![delivery("dev_1", "queued", "")],
                    },
                    Recipient { external_user_id: "carol".into(), status: RECIPIENT_NO_ACCESS.into(), ..Default::default() },
                ],
                ..Default::default()
            };
            assert_eq!(c.send_message(&msg).unwrap(), want);
        }),
    );
    m.insert(
        "listChannels",
        Box::new(|c| {
            let want = vec![Channel {
                id: Some("ops".into()),
                name: "Operations".into(),
                default_level: Level::Active,
                max_level: Level::Critical,
                thread: Some("ops".into()),
                audience: Some(Audience::default()),
                ..Default::default()
            }];
            assert_eq!(c.list_channels().unwrap(), want);
        }),
    );
    m.insert(
        "listTemplates",
        Box::new(|c| {
            let want = vec![Template { id: "status".into(), version: 2, body: json!({"type": "text", "text": "{{title}}"}) }];
            assert_eq!(c.list_templates().unwrap(), want);
        }),
    );
    m.insert(
        "getTemplate",
        Box::new(|c| {
            let want = Template { id: "status".into(), version: 2, body: json!({"type": "text", "text": "{{title}}"}) };
            assert_eq!(c.get_template("status").unwrap(), want);
        }),
    );
    m.insert("deleteTemplate", Box::new(|c| c.delete_template("status").unwrap()));
    m.insert(
        "listSounds",
        Box::new(|c| {
            let want = vec![Sound { id: "ding".into(), content_type: "audio/wav".into(), bytes: 12, sha256: "00".into() }];
            assert_eq!(c.list_sounds().unwrap(), want);
        }),
    );
    m.insert(
        "getSound",
        Box::new(|c| {
            let want = Sound { id: "ding".into(), content_type: "audio/wav".into(), bytes: 12, sha256: "00".into() };
            assert_eq!(c.get_sound("ding").unwrap(), want);
        }),
    );
    m.insert("deleteSound", Box::new(|c| c.delete_sound("ding").unwrap()));
    m.insert(
        "listGroups",
        Box::new(|c| {
            let want = vec![Group { id: "oncall".into(), members: vec!["alice".into(), "bob".into()] }];
            assert_eq!(c.list_groups().unwrap(), want);
        }),
    );
    m.insert(
        "getFeed",
        Box::new(|c| {
            let want = FeedSettings {
                feed_id: "uptime".into(),
                external_user_id: "user-42".into(),
                template_id: "status".into(),
                title: "Uptime".into(),
                channel: "ops".into(),
                audience: Some(Audience { mode: AudienceMode::Only, users: vec!["alice".into()], groups: vec![] }),
                updated_at: 1_762_603_200,
            };
            assert_eq!(c.get_feed("uptime", Some("user-42")).unwrap(), want);
        }),
    );
    m
}

fn only() -> Audience {
    Audience { mode: AudienceMode::Only, users: vec!["alice".into()], groups: vec!["oncall".into()] }
}

#[test]
fn every_spec_operation() {
    let spec = conformance();
    let calls = calls();
    for op in spec["operations"].as_array().unwrap() {
        let name = op["name"].as_str().unwrap();
        let call = calls.get(name).unwrap_or_else(|| panic!("no SDK call for spec operation {name}"));
        assert!(SPEC_OPERATIONS.contains(&op["operation"].as_str().unwrap()), "{name}: operation is not in the spec");
        let (status, resp_body) = (op["response"]["status"].as_u64().unwrap() as u16, op["response"]["body"].to_string());
        let hub = start(move |_| Reply::default().status(status).body(resp_body.clone()));
        call(&hub.client());

        let reqs = hub.requests();
        assert_eq!(reqs.len(), 1, "{name}");
        let r = &reqs[0];
        assert_eq!(r.method, op["method"].as_str().unwrap(), "{name}");
        assert_eq!(r.url, op["path"].as_str().unwrap(), "{name}");
        assert_eq!(r.headers["authorization"], format!("Bearer {TEST_KEY}"), "{name}");
        assert_eq!(r.headers["accept"], "application/json", "{name}");
        assert!(r.headers["user-agent"].starts_with("hansho-rust/"), "{name}");
        let req = &op["request"];
        if req.is_null() {
            assert!(r.body.is_empty(), "{name}");
            assert!(!r.headers.contains_key("content-type"), "{name}");
        } else if let Some(b64) = req.get("raw_base64") {
            assert_eq!(r.headers["content-type"], req["content_type"].as_str().unwrap(), "{name}");
            assert_eq!(r.body, base64_decode(b64.as_str().unwrap()), "{name}");
        } else {
            assert_eq!(r.headers["content-type"], req["content_type"].as_str().unwrap(), "{name}");
            let sent: Value = serde_json::from_slice(&r.body).unwrap();
            assert_eq!(sent, req["body"], "{name}");
        }
        assert!(!r.headers.contains_key("idempotency-key"), "{name}");
    }
}

#[test]
fn covers_every_sdk_call() {
    let spec = conformance();
    let names: Vec<&str> = spec["operations"].as_array().unwrap().iter().map(|o| o["name"].as_str().unwrap()).collect();
    for k in calls().keys() {
        assert!(names.contains(k), "SDK call {k} has no case in x-conformance of sdk/spec/openapi.json");
    }
    assert_eq!(names.len(), calls().len());
}

#[test]
fn every_spec_error() {
    let spec = conformance();
    let errors = spec["errors"].as_array().unwrap();
    assert!(errors.len() >= 5);
    for e in errors {
        let status = e["status"].as_u64().unwrap() as u16;
        let body = e.get("raw").and_then(Value::as_str).map_or_else(|| e["body"].to_string(), str::to_owned);
        let headers: Vec<(String, String)> =
            e["headers"].as_object().unwrap().iter().map(|(k, v)| (k.clone(), v.as_str().unwrap().to_owned())).collect();
        let hub = start(move |_| {
            let mut r = Reply::default().status(status).body(body.clone());
            r.headers = headers.clone();
            r
        });
        let err = hub.client().send_message(&Message::new("u", "c", "t", "b")).unwrap_err();
        let api = err.as_api().unwrap_or_else(|| panic!("{status}: not an API error: {err}"));
        assert_eq!(api.status, status);
        assert_eq!(api.code, e["code"].as_str().unwrap());
        assert_eq!(api.field.clone().unwrap_or_default(), e["field"].as_str().unwrap());
        assert_eq!(api.retry_after.unwrap_or_default().as_millis() as u64, e["retry_after_ms"].as_u64().unwrap());
        assert_eq!(hub.count(), 1, "POST /messages must not be retried");
        assert!(hub.waits().is_empty());
    }
}

#[test]
fn client_has_a_method_for_every_spec_operation() {
    let client = include_str!("../src/client.rs");
    let missing: Vec<&str> = SPEC_OPERATIONS.iter().copied().filter(|id| !client.contains(&format!("pub fn {}(", snake(id)))).collect();
    assert!(missing.is_empty(), "Client has no method for the spec operations {missing:?}");
}

#[test]
fn the_generated_tables_match_the_spec() {
    let doc = load_spec("openapi.json");
    let mut ids: Vec<&str> = doc["paths"]
        .as_object()
        .unwrap()
        .values()
        .flat_map(|item| item.as_object().unwrap().values())
        .filter_map(|op| op["operationId"].as_str())
        .collect();
    ids.sort_unstable();
    let mut have: Vec<&str> = SPEC_OPERATIONS.to_vec();
    have.sort_unstable();
    assert_eq!(have, ids);

    let mut events: Vec<&str> = doc["webhooks"].as_object().unwrap().keys().map(String::as_str).collect();
    events.sort_unstable();
    let mut have: Vec<&str> = EVENTS.iter().map(|(name, _)| *name).collect();
    have.sort_unstable();
    assert_eq!(have, events);

    assert_eq!(VERSION, doc["info"]["version"].as_str().unwrap());
    assert_eq!(VERSION, env!("CARGO_PKG_VERSION"));
}

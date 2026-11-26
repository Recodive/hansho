//! Webhook verification: the vectors signed by the hub's own dispatcher, plus replay, raw-body and forward-compatibility checks.

mod common;

use std::io::Write;
use std::net::{TcpListener, TcpStream};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use common::{load_spec, read_request};
use hansho::*;
use hmac::{Hmac, Mac};
use serde_json::Value;
use sha2::Sha256;
use std::result::Result;

fn at(secs: u64) -> SystemTime {
    UNIX_EPOCH + Duration::from_secs(secs)
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

fn sign(secret: &str, t: u64, body: &str) -> String {
    let mut mac = Hmac::<Sha256>::new_from_slice(secret.as_bytes()).unwrap();
    mac.update(format!("{t}.{body}").as_bytes());
    format!("t={t},v1={}", hex(&mac.finalize().into_bytes()))
}

fn now_secs() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_secs()
}

fn code(r: Result<Event, WebhookError>) -> &'static str {
    r.expect_err("expected a rejection").code.as_str()
}

fn vectors() -> (u64, Vec<Value>) {
    let v = load_spec("webhook-vectors.json");
    (v["tolerance_seconds"].as_u64().unwrap(), v["vectors"].as_array().unwrap().clone())
}

fn find(name: &str) -> Value {
    vectors().1.into_iter().find(|v| v["name"] == name).unwrap_or_else(|| panic!("no vector {name}"))
}

#[test]
fn shared_vectors_signed_by_the_hub() {
    let (tol, vs) = vectors();
    assert!(vs.len() >= 20);
    let mut classes = std::collections::BTreeSet::new();
    for v in &vs {
        let name = v["name"].as_str().unwrap();
        let body = v["body"].as_str().unwrap();
        let verifier = Verifier::new(v["secret"].as_str().unwrap()).tolerance(Duration::from_secs(tol));
        let got = verifier.verify_at(v["header"].as_str().unwrap(), body, at(v["now"].as_u64().unwrap()));
        let expect = &v["expect"];
        if expect["ok"] != true {
            let want = expect["error"].as_str().unwrap();
            classes.insert(want.to_owned());
            assert_eq!(code(got), want, "{name}");
            continue;
        }
        let ev = got.unwrap_or_else(|e| panic!("{name}: {e}"));
        let want_type = expect["type"].as_str().unwrap();
        assert_eq!(ev.id, expect["id"].as_str().unwrap(), "{name}");
        assert_eq!(ev.created_at, at(expect["created_at"].as_u64().unwrap()), "{name}");
        assert_eq!(ev.raw, body.as_bytes(), "{name}");
        match &ev.data {
            EventData::Unknown { name: n, data } => {
                classes.insert("unknown".to_owned());
                assert_eq!(want_type, "unknown", "{name}");
                assert_eq!(n, expect["name"].as_str().unwrap(), "{name}");
                assert_eq!(data, &expect["data"], "{name}");
                assert_eq!(ev.name(), n.as_str());
            }
            _ => assert_eq!(ev.name(), want_type, "{name}"),
        }
        let raw: Value = serde_json::from_slice(&ev.raw).unwrap();
        assert_eq!(raw["data"], expect["data"], "{name}");
    }
    for c in ["stale", "bad_signature", "malformed_header", "unknown"] {
        assert!(classes.contains(c), "no vector covers {c}");
    }
}

#[test]
fn maps_every_known_event_to_typed_payloads() {
    let (_, vs) = vectors();
    let mut seen = std::collections::BTreeSet::new();
    for v in vs {
        let name = v["name"].as_str().unwrap();
        if !name.starts_with("valid ") || v["expect"]["type"] == "unknown" || !seen.insert(v["expect"]["type"].as_str().unwrap().to_owned())
        {
            continue;
        }
        // The vectors are old; verify at their own clock.
        let ev = Verifier::new(v["secret"].as_str().unwrap())
            .verify_at(v["header"].as_str().unwrap(), v["body"].as_str().unwrap(), at(v["now"].as_u64().unwrap()))
            .unwrap();
        let s = |x: &str| x.to_owned();
        let want = match ev.name() {
            "pairing.created" => EventData::PairingCreated(PairingCreated {
                pairing_id: s("pair_1"),
                external_user_id: s("user-42"),
                device_name: s("Bas’s iPhone <Ünï>"),
            }),
            "pairing.revoked" => {
                EventData::PairingRevoked(PairingRevoked { pairing_id: s("pair_1"), external_user_id: s("user-42"), by: s("sender") })
            }
            "message.delivered" => EventData::MessageDelivered(MessageDelivered {
                message_id: s("msg_1"),
                external_user_id: s("user-42"),
                channel: s("ops"),
                level: Level::Page,
                devices: 2,
            }),
            "message.undeliverable" => EventData::MessageUndeliverable(MessageUndeliverable {
                message_id: s("msg_2"),
                external_user_id: s("user-42"),
                channel: s("ops"),
                reason: s("no_devices"),
            }),
            "message.acked" => EventData::MessageAcked(MessageAcked {
                message_id: s("msg_1"),
                external_user_id: s("user-42"),
                device_id: s("dev_1"),
                acked_by: None,
                acked_by_name: None,
            }),
            "message.dismissed" => EventData::MessageDismissed(MessageDismissed {
                message_id: s("msg_1"),
                external_user_id: s("user-42"),
                device_id: s("dev_1"),
            }),
            "message.expired" => EventData::MessageExpired(MessageExpired {
                message_id: s("msg_3"),
                external_user_id: s("user-42"),
                channel: s("ops"),
                attempts: 6,
            }),
            "action" => EventData::Action(ActionTaken {
                message_id: s("msg_1"),
                action_id: s("approve"),
                device_id: s("dev_1"),
                external_user_id: s("user-42"),
                text: Some(s("ship it")),
            }),
            "activity.ended" => EventData::ActivityEnded(ActivityEnded {
                activity_id: s("act_1"),
                external_user_id: s("user-42"),
                channel: s("ops"),
                reason: s("timeout"),
            }),
            other => panic!("unexpected event {other}"),
        };
        assert_eq!(ev.data, want);
    }
    assert_eq!(seen.len(), 9);
}

mod replay_protection {
    use super::*;

    const BODY: &str = r#"{"id":"1","event":"message.acked","created_at":1,"data":{}}"#;
    const T: u64 = 1_700_000_000;

    fn check(offset: i64, tolerance: Option<Duration>) -> Result<Event, WebhookError> {
        let mut v = Verifier::new("s");
        if let Some(t) = tolerance {
            v = v.tolerance(t);
        }
        v.verify_at(&sign("s", T, BODY), BODY, at((T as i64 + offset) as u64))
    }

    #[test]
    fn accepts_up_to_300_s_either_side_and_rejects_beyond() {
        for o in [0, 300, -300] {
            assert!(check(o, None).is_ok(), "{o}");
        }
        for o in [301, -301, 86_400] {
            assert_eq!(code(check(o, None)), "stale", "{o}");
        }
    }

    #[test]
    fn tolerance_is_configurable_and_zero_means_default() {
        assert!(check(301, Some(Duration::from_secs(600))).is_ok());
        assert!(check(11, Some(Duration::from_secs(10))).is_err());
        assert!(check(10, Some(Duration::from_secs(10))).is_ok());
        assert!(check(300, Some(Duration::ZERO)).is_ok());
        assert_eq!(code(check(301, Some(Duration::ZERO))), "stale");
    }

    #[test]
    fn defaults_to_the_clock() {
        let n = now_secs();
        assert!(verify_webhook("s", &sign("s", n, BODY), BODY).is_ok());
        assert_eq!(code(verify_webhook("s", &sign("s", T, BODY), BODY)), "stale");
        assert_eq!(DEFAULT_TOLERANCE, Duration::from_secs(300));
    }

    #[test]
    fn a_timestamp_that_does_not_fit_is_stale_not_a_panic() {
        let huge = "9".repeat(60);
        let mut mac = Hmac::<Sha256>::new_from_slice(b"s").unwrap();
        mac.update(format!("{huge}.{BODY}").as_bytes());
        let header = format!("t={huge},v1={}", hex(&mac.finalize().into_bytes()));
        assert_eq!(code(verify_webhook("s", &header, BODY)), "stale");
    }

    #[test]
    fn a_negative_timestamp_is_stale() {
        let mut mac = Hmac::<Sha256>::new_from_slice(b"s").unwrap();
        mac.update(format!("-5.{BODY}").as_bytes());
        let header = format!("t=-5,v1={}", hex(&mac.finalize().into_bytes()));
        assert_eq!(code(verify_webhook("s", &header, BODY)), "stale");
        assert!(Verifier::new("s").verify_at(&header, BODY, at(100)).is_ok());
    }
}

mod raw_body {
    use super::*;

    fn valid() -> (Value, u64) {
        let v = find("valid message.acked");
        let now = v["now"].as_u64().unwrap();
        (v, now)
    }

    #[test]
    fn accepts_bytes_a_vec_and_a_str() {
        let (v, now) = valid();
        let (h, body) = (v["header"].as_str().unwrap(), v["body"].as_str().unwrap());
        let ver = Verifier::new("whsec_sdk_vectors");
        assert!(ver.verify_at(h, body, at(now)).is_ok());
        assert!(ver.verify_at(h, body.as_bytes(), at(now)).is_ok());
        assert!(ver.verify_at(h, Vec::from(body), at(now)).is_ok());
    }

    #[test]
    fn rejects_a_body_that_was_parsed_and_reserialised() {
        let (v, now) = valid();
        let body = v["body"].as_str().unwrap();
        let parsed: Value = serde_json::from_str(body).unwrap();
        for re in [serde_json::to_string_pretty(&parsed).unwrap(), format!("{body}\n"), format!(" {body}")] {
            let r = Verifier::new("whsec_sdk_vectors").verify_at(v["header"].as_str().unwrap(), re, at(now));
            assert_eq!(code(r), "bad_signature");
        }
    }

    #[test]
    fn verifies_the_bytes_it_is_given_including_non_ascii_from_a_tcp_server() {
        let (_, now) = valid();
        let seen: Arc<Mutex<Vec<String>>> = Arc::default();
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = listener.local_addr().unwrap();
        let s2 = seen.clone();
        let server = thread::spawn(move || {
            for _ in 0..3 {
                let (mut conn, _) = listener.accept().unwrap();
                let req = read_request(&mut conn).unwrap();
                let header = req.headers.get(&WEBHOOK_SIGNATURE_HEADER.to_ascii_lowercase()).cloned().unwrap_or_default();
                let status = match Verifier::new("whsec_sdk_vectors").verify_at(&header, &req.body, at(now)) {
                    Ok(e) => {
                        s2.lock().unwrap().push(e.name().to_owned());
                        200
                    }
                    Err(_) => 400,
                };
                conn.write_all(format!("HTTP/1.1 {status} X\r\nContent-Length: 0\r\nConnection: close\r\n\r\n").as_bytes()).unwrap();
            }
        });
        let post = |header: &str, body: &str| -> u16 {
            let mut c = TcpStream::connect(addr).unwrap();
            let req = format!(
                "POST / HTTP/1.1\r\nHost: x\r\n{WEBHOOK_SIGNATURE_HEADER}: {header}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                body.len()
            );
            c.write_all(req.as_bytes()).unwrap();
            c.write_all(body.as_bytes()).unwrap();
            let mut resp = String::new();
            std::io::Read::read_to_string(&mut c, &mut resp).unwrap();
            resp.split(' ').nth(1).unwrap().parse().unwrap()
        };
        let acked = find("valid message.acked");
        let pairing = find("valid pairing.created");
        let pretty = serde_json::to_string_pretty(&serde_json::from_str::<Value>(acked["body"].as_str().unwrap()).unwrap()).unwrap();
        assert_eq!(post(acked["header"].as_str().unwrap(), acked["body"].as_str().unwrap()), 200);
        assert_eq!(post(acked["header"].as_str().unwrap(), &pretty), 400);
        assert_eq!(post(pairing["header"].as_str().unwrap(), pairing["body"].as_str().unwrap()), 200); // non-ASCII, byte for byte
        server.join().unwrap();
        assert_eq!(*seen.lock().unwrap(), ["message.acked", "pairing.created"]);
    }

    #[test]
    fn exposes_the_header_name() {
        assert_eq!(WEBHOOK_SIGNATURE_HEADER, "X-Hansho-Webhook-Signature");
    }
}

mod secret_and_header_validation {
    use super::*;

    const BODY: &str = r#"{"id":"1","event":"action","created_at":1,"data":{}}"#;

    #[test]
    fn accepts_bytes_as_a_secret_and_refuses_an_empty_one() {
        let h = sign("s", now_secs(), BODY);
        assert!(verify_webhook(b"s", &h, BODY).is_ok());
        assert!(verify_webhook(Vec::from("s"), &h, BODY).is_ok());
        for empty in [&b""[..], b""] {
            assert_eq!(code(verify_webhook(empty, &sign("", now_secs(), BODY), BODY)), "invalid_secret");
        }
        assert_eq!(code(verify_webhook("", "garbage", BODY)), "invalid_secret", "the secret is checked first");
    }

    #[test]
    fn treats_a_missing_header_as_malformed() {
        for h in ["", "   "] {
            assert_eq!(code(verify_webhook("s", h, BODY)), "malformed_header");
        }
    }

    #[test]
    fn rejects_a_valid_signature_over_a_body_that_is_not_a_webhook_envelope() {
        for bad in ["not json", "[]", r#"{"id":"1"}"#, "", r#"{"event":""}"#, r#"{"event":5}"#, "\u{feff}{}"] {
            let r = verify_webhook("s", &sign("s", now_secs(), bad), bad);
            assert_eq!(code(r), "malformed_body", "{bad:?}");
        }
        let invalid_utf8 = [b'{', 0xff, b'}'];
        let mut mac = Hmac::<Sha256>::new_from_slice(b"s").unwrap();
        let t = now_secs();
        mac.update(format!("{t}.").as_bytes());
        mac.update(&invalid_utf8);
        let header = format!("t={t},v1={}", hex(&mac.finalize().into_bytes()));
        assert_eq!(code(verify_webhook("s", &header, invalid_utf8)), "malformed_body");
    }

    #[test]
    fn accepts_upper_case_hex_and_surrounding_spaces() {
        let t = now_secs();
        let h = sign("s", t, BODY);
        let upper = h.replace(&h[h.find("v1=").unwrap() + 3..], &h[h.find("v1=").unwrap() + 3..].to_uppercase());
        assert!(verify_webhook("s", &upper, BODY).is_ok());
        let spaced = h.replace(',', " , ");
        assert!(verify_webhook("s", &spaced, BODY).is_ok());
    }

    #[test]
    fn several_v1_values_any_match_wins_and_junk_is_skipped() {
        let t = now_secs();
        let good = sign("s", t, BODY);
        let sig = good.split("v1=").nth(1).unwrap();
        let ok = |h: String| verify_webhook("s", &h, BODY).is_ok();
        assert!(ok(format!("t={t},v1={},v1={sig}", "0".repeat(64))));
        assert!(ok(format!("t={t},v1={sig},v1={}", "0".repeat(64))));
        assert!(ok(format!("t={t},v1=short,v1=zz,v1={sig}")));
        assert!(!ok(format!("t={t},v1={},v1={}", "0".repeat(64), "1".repeat(64))));
        assert!(ok(format!("t={t},v0=legacy,v1={sig}")));
    }

    #[test]
    fn errors_are_std_errors_with_stable_codes() {
        let e = verify_webhook("s", "x", "{}").unwrap_err();
        assert_eq!(e.code, WebhookErrorCode::MalformedHeader);
        let _: &dyn std::error::Error = &e;
        assert!(e.to_string().starts_with("hansho webhook: "));
    }
}

mod forward_compatibility {
    use super::*;

    #[test]
    fn delivers_an_unknown_event_as_unknown_with_the_raw_payload() {
        let body = r#"{"id":7,"event":"message.holographic","created_at":5,"extra":1,"data":{"message_id":"m","new_field":[1,2]}}"#;
        let ev = verify_webhook("s", &sign("s", now_secs(), body), body).unwrap();
        assert_eq!(ev.id, "7");
        assert_eq!(ev.created_at, at(5));
        match ev.data {
            EventData::Unknown { name, data } => {
                assert_eq!(name, "message.holographic");
                assert_eq!(data, serde_json::json!({"message_id": "m", "new_field": [1, 2]}));
            }
            other => panic!("{other:?}"),
        }
    }

    #[test]
    fn an_unknown_event_without_data_has_null_data() {
        let body = r#"{"id":"1","event":"x.y","created_at":5}"#;
        let ev = verify_webhook("s", &sign("s", now_secs(), body), body).unwrap();
        assert!(matches!(ev.data, EventData::Unknown { data: Value::Null, .. }));
    }

    #[test]
    fn ignores_unknown_fields_on_known_events() {
        let body = r#"{"id":"8","event":"message.acked","created_at":5,"data":{"message_id":"m","device_id":"d","external_user_id":"u","from_the_future":true}}"#;
        let ev = verify_webhook("s", &sign("s", now_secs(), body), body).unwrap();
        let want = EventData::MessageAcked(MessageAcked {
            message_id: "m".into(),
            external_user_id: "u".into(),
            device_id: "d".into(),
            acked_by: None,
            acked_by_name: None,
        });
        assert_eq!(ev.data, want);
    }

    #[test]
    fn message_acked_carries_who_acked() {
        let body = r#"{"id":"9","event":"message.acked","created_at":5,"data":{"message_id":"m","device_id":"d","external_user_id":"u","acked_by":"bob","acked_by_name":"Bob ボブ"}}"#;
        let ev = verify_webhook("s", &sign("s", now_secs(), body), body).unwrap();
        let want = EventData::MessageAcked(MessageAcked {
            message_id: "m".into(),
            external_user_id: "u".into(),
            device_id: "d".into(),
            acked_by: Some("bob".into()),
            acked_by_name: Some("Bob ボブ".into()),
        });
        assert_eq!(ev.data, want);
    }

    #[test]
    fn an_absurd_created_at_does_not_panic() {
        for created in ["999999999999999", "-999999999999999", "1e300", "1.5"] {
            let body = format!(r#"{{"id":"1","event":"x.y","created_at":{created},"data":{{}}}}"#);
            assert!(verify_webhook("s", &sign("s", now_secs(), &body), &body).is_ok(), "{created}");
        }
    }

    #[test]
    fn a_known_event_with_missing_data_reads_as_empty() {
        let body = r#"{"id":"8","event":"action","created_at":5}"#;
        let ev = verify_webhook("s", &sign("s", now_secs(), body), body).unwrap();
        let EventData::Action(a) = ev.data else { panic!() };
        assert_eq!((a.message_id.as_str(), a.text), ("", None));
    }
}

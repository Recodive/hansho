//! Client behaviour beyond the shared spec: headers, forward compatibility, retries, path escaping, errors.

mod common;

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use common::{closed_port_url, silent_server, start, start_default, Hub, Reply, TEST_KEY};
use hansho::*;
use serde_json::{json, Value};

fn msg() -> Message {
    Message::new("u", "c", "t", "b")
}

fn channel() -> Channel {
    Channel { name: "Ops".into(), default_level: Level::Active, max_level: Level::Critical, ..Default::default() }
}

fn api(err: &Error) -> &ApiError {
    err.as_api().unwrap_or_else(|| panic!("not an API error: {err}"))
}

fn secs(n: u64) -> Duration {
    Duration::from_secs(n)
}

fn ms(n: u64) -> Duration {
    Duration::from_millis(n)
}

fn body_json(h: &Hub, i: usize) -> Value {
    serde_json::from_slice(&h.requests()[i].body).unwrap()
}

mod basics {
    use super::*;

    #[test]
    fn sends_headers_and_the_default_user_agent() {
        let h = start_default();
        h.client().put_channel("ops", &channel()).unwrap();
        let r = &h.requests()[0];
        assert_eq!(r.headers["authorization"], format!("Bearer {TEST_KEY}"));
        assert_eq!(r.headers["accept"], "application/json");
        assert_eq!(r.headers["content-type"], "application/json");
        assert_eq!(r.headers["user-agent"], format!("hansho-rust/{VERSION}"));
        h.builder().user_agent("acme/2").build().unwrap().end_activity("a").unwrap();
        let r = &h.requests()[1];
        assert_eq!(r.headers["user-agent"], "acme/2");
        assert!(!r.headers.contains_key("content-type"));
    }

    #[test]
    fn handles_a_trailing_slash_and_a_path_prefix_in_the_base_url() {
        let h = start_default();
        Client::new(format!("{}/hansho/", h.url), TEST_KEY).unwrap().get_message("m1").unwrap();
        assert_eq!(h.requests()[0].url, "/hansho/api/v1/messages/m1");
    }

    #[test]
    fn exports_the_codename_constant() {
        assert_eq!(NAME, "hansho");
    }

    #[test]
    fn requires_a_base_url_and_an_api_key() {
        for (url, key) in [("", "k"), ("http://x", ""), ("not a url", "k"), ("ftp://x", "k"), ("hub.example.com", "k")] {
            assert!(matches!(Client::new(url, key), Err(Error::InvalidArgument(_))), "{url:?} {key:?}");
        }
        assert!(Client::new("https://hub.example.com", "k").is_ok());
    }

    #[test]
    fn does_not_expose_the_key_when_inspected() {
        let c = Client::new("http://x", TEST_KEY).unwrap();
        assert!(!format!("{c:?}").contains(TEST_KEY));
        assert!(!format!("{c:#?}").contains(TEST_KEY));
        let v = Verifier::new("whsec_do_not_leak");
        assert!(!format!("{v:?}").contains("whsec_do_not_leak"));
    }

    #[test]
    fn omits_unset_optional_fields_from_the_wire() {
        let h = start_default();
        let c = h.client();
        c.send_message(&msg()).unwrap();
        assert_eq!(body_json(&h, 0), json!({"external_user_id": "u", "channel": "c", "title": "t", "body": "b"}));
        c.put_channel("ops", &channel()).unwrap();
        assert_eq!(body_json(&h, 1), json!({"name": "Ops", "default_level": "active", "max_level": "critical"}));
    }

    #[test]
    fn display_name_put_path_escaping_and_empty_id() {
        let h = start(|_| Reply::default().status(204).body(""));
        let c = h.client();
        c.set_user_display_name("a/b c", "Ada").unwrap();
        c.set_user_display_name("u", "").unwrap();
        let rs = h.requests();
        assert_eq!((rs[0].method.as_str(), rs[0].url.as_str()), ("PUT", "/api/v1/users/a%2Fb%20c"));
        assert_eq!(body_json(&h, 0), json!({"display_name": "Ada"}));
        assert_eq!(body_json(&h, 1), json!({"display_name": ""}));
        assert!(matches!(c.set_user_display_name("", "x"), Err(Error::InvalidArgument(_))));
        assert!(matches!(c.create_pairing_ticket_with_name("", "x"), Err(Error::InvalidArgument(_))));
        assert_eq!(h.count(), 2);
    }

    #[test]
    fn pairing_ticket_body_with_and_without_display_name() {
        let h = start(|_| Reply::default().status(201).body(r#"{"ticket":"t","qr":"q","expires_at":"2026-11-08T12:10:00Z"}"#));
        let c = h.client();
        c.create_pairing_ticket("u").unwrap();
        c.create_pairing_ticket_with_name("u", "").unwrap();
        c.create_pairing_ticket_with_name("u", "Ada").unwrap();
        assert_eq!(body_json(&h, 0), json!({"external_user_id": "u"}));
        assert_eq!(body_json(&h, 1), json!({"external_user_id": "u", "display_name": ""}));
        assert_eq!(body_json(&h, 2), json!({"external_user_id": "u", "display_name": "Ada"}));
    }

    #[test]
    fn page_ack_is_sent_only_when_set() {
        let h = start_default();
        let c = h.client();
        for ack in [None, Some(PageAck::Each), Some(PageAck::Any)] {
            let page = PageOptions { ack, ..Default::default() };
            c.send_message(&Message { level: Some(Level::Page), page: Some(page), ..msg() }).unwrap();
        }
        assert_eq!(body_json(&h, 0)["page"], json!({}));
        assert_eq!(body_json(&h, 1)["page"], json!({"ack": "each"}));
        assert_eq!(body_json(&h, 2)["page"], json!({"ack": "any"}));
    }

    #[test]
    fn message_status_acked_by_present_and_absent() {
        let h = start(|_| Reply::default().body(r#"{"message_id":"m","status":"acked","acked_by":"bob","acked_by_name":"Bob ボブ"}"#));
        let st = h.client().get_message("m").unwrap();
        assert_eq!((st.acked_by.as_str(), st.acked_by_name.as_str()), ("bob", "Bob ボブ"));
        let h = start(|_| Reply::default().body(r#"{"message_id":"m","status":"acked"}"#));
        let st = h.client().get_message("m").unwrap();
        assert_eq!((st.acked_by.as_str(), st.acked_by_name.as_str()), ("", ""));
    }

    #[test]
    fn sends_max_retries_zero_as_given() {
        let h = start_default();
        let m = Message { level: Some(Level::Page), page: Some(PageOptions { max_retries: Some(0), ..Default::default() }), ..msg() };
        h.client().send_message(&m).unwrap();
        assert_eq!(body_json(&h, 0)["page"], json!({"max_retries": 0}));
    }

    #[test]
    fn the_client_is_shareable_across_threads() {
        fn assert_send_sync<T: Send + Sync + Clone>() {}
        assert_send_sync::<Client>();
        assert_send_sync::<Verifier>();
    }

    #[test]
    fn a_redirect_is_an_api_error_and_is_not_followed() {
        let other = start_default();
        let target = format!("{}/stolen", other.url);
        let h = start(move |_| Reply::default().status(302).header("Location", &target).body(""));
        let err = h.client().get_message("m").unwrap_err();
        assert_eq!(api(&err).status, 302);
        assert_eq!(other.count(), 0, "the key must not follow a redirect");
    }
}

mod forward_compatibility {
    use super::*;

    #[test]
    fn ignores_response_fields_it_does_not_know() {
        let h = start(|_| {
            Reply::default().body(
                r#"{"message_id":"m","status":"delivered","level":"page","attempts":1,"deliveries":[{"device_id":"d","status":"sent","brand_new":{"x":[1]}}],"brand_new":"v","acked_at":"2026-11-08T12:05:00Z"}"#,
            )
        });
        let ms = h.client().get_message("m").unwrap();
        assert_eq!(ms.message_id, "m");
        assert_eq!(ms.deliveries.len(), 1);
    }

    #[test]
    fn keeps_a_level_from_a_newer_hub() {
        let h = start(|_| Reply::default().status(202).body(r#"{"message_id":"m","level":"urgent-plus","devices":1,"status":"queued"}"#));
        let res = h.client().send_message(&msg()).unwrap();
        assert_eq!(res.level, Level::Other("urgent-plus".into()));
        assert_eq!(res.level.as_str(), "urgent-plus");
        let ser = serde_json::to_value(&res.level).unwrap();
        assert_eq!(ser, json!("urgent-plus"));
    }

    #[test]
    fn missing_response_fields_read_as_empty() {
        let h = start(|_| Reply::default().body("{}"));
        let st = h.client().get_message("m").unwrap();
        assert_eq!(st.message_id, "");
        assert_eq!(st.acked_at, None);
        assert!(st.deliveries.is_empty());
    }

    #[test]
    fn an_empty_2xx_body_is_fine() {
        let h = start(|_| Reply::default().status(204).body(""));
        h.client().delete_channel("ops").unwrap();
        h.client().end_activity("a").unwrap();
    }

    #[test]
    fn a_2xx_body_that_is_not_json_is_a_network_error_without_the_body() {
        let h = start(|_| Reply::default().body("SECRET-BODY <html>"));
        let err = h.client().get_message("m").unwrap_err();
        let n = err.as_network().expect("network error");
        assert!(n.message.contains("invalid JSON"));
        assert!(!err.to_string().contains("SECRET-BODY"));
        assert_eq!(h.count(), 1);
    }

    #[test]
    fn levels_round_trip_through_their_wire_names() {
        for (l, s) in [
            (Level::Passive, "passive"),
            (Level::Active, "active"),
            (Level::TimeSensitive, "time-sensitive"),
            (Level::Critical, "critical"),
            (Level::Page, "page"),
        ] {
            assert_eq!(l.as_str(), s);
            assert_eq!(Level::from(s), l);
            assert_eq!(serde_json::to_value(&l).unwrap(), json!(s));
            assert_eq!(serde_json::from_value::<Level>(json!(s)).unwrap(), l);
            assert_eq!(l.to_string(), s);
        }
    }
}

mod retries {
    use super::*;

    /// A hub that fails the first `n` requests with `fail` and then answers 200.
    fn failing(n: usize, fail: Reply) -> Hub {
        let seen = AtomicUsize::new(0);
        start(move |_| if seen.fetch_add(1, Ordering::SeqCst) < n { fail.clone() } else { Reply::default() })
    }

    #[test]
    fn retries_put_on_429_and_honours_retry_after() {
        let h = failing(2, Reply::default().status(429).header("Retry-After", "2").body(r#"{"error":"rate_limited"}"#));
        h.client().put_channel("ops", &channel()).unwrap();
        assert_eq!(h.count(), 3);
        assert_eq!(h.waits(), [secs(2), secs(2)]);
    }

    #[test]
    fn understands_an_http_date_retry_after() {
        // A date far in the future is beyond the 60 s cap: the error carries the parsed wait and nothing is slept.
        let h = start(|_| Reply::default().status(429).header("Retry-After", "Thu, 01 Jan 2099 00:00:00 GMT"));
        let err = h.client().delete_channel("ops").unwrap_err();
        assert!(api(&err).retry_after.unwrap() > Duration::from_secs(365 * 24 * 3600));
        assert!(h.waits().is_empty());
        // A date with an absurd year is not a date: no panic, back off instead.
        let h = failing(1, Reply::default().status(429).header("Retry-After", "Sun, 06 Nov 99999999999999999999 08:49:37 GMT"));
        h.client().delete_channel("ops").unwrap();
        assert_eq!(h.waits().len(), 1);
        // A date in the past is no usable wait: back off instead.
        let h = failing(1, Reply::default().status(429).header("Retry-After", "Sun, 06 Nov 1994 08:49:37 GMT"));
        h.client().delete_channel("ops").unwrap();
        assert_eq!(h.waits().len(), 1);
    }

    #[test]
    fn stops_after_max_retries_and_surfaces_the_error() {
        let h = start(|_| Reply::default().status(429).header("Retry-After", "1").body(r#"{"error":"rate_limited"}"#));
        let err = h.client().put_channel("ops", &channel()).unwrap_err();
        assert_eq!((api(&err).status, api(&err).retry_after), (429, Some(secs(1))));
        assert_eq!(h.count(), 4);
        assert_eq!(h.waits().len(), 3);
        h.builder().max_retries(0).build().unwrap().put_channel("ops", &channel()).unwrap_err();
        assert_eq!(h.count(), 5);
        h.builder().max_retries(1).build().unwrap().put_channel("ops", &channel()).unwrap_err();
        assert_eq!(h.count(), 7);
    }

    #[test]
    fn retries_get_and_delete_on_503_with_jittered_backoff() {
        let seen = Arc::new(Mutex::new(std::collections::HashSet::new()));
        let h = start(move |r| {
            if seen.lock().unwrap().insert(format!("{} {}", r.method, r.url)) {
                Reply::default().status(503).body(r#"{"error":"unavailable"}"#)
            } else {
                Reply::default()
            }
        });
        let c = h.client();
        c.get_channel("x").unwrap();
        c.delete_channel("x").unwrap();
        c.end_activity("x").unwrap();
        let waits = h.waits();
        assert_eq!(waits.len(), 3);
        for w in waits {
            assert!(w >= ms(125) && w <= ms(250), "{w:?}");
        }
    }

    #[test]
    fn backs_off_further_on_each_attempt() {
        let h = start(|_| Reply::default().status(503));
        h.client().get_channel("x").unwrap_err();
        let w = h.waits();
        assert_eq!(w.len(), 3);
        assert!(w[0] <= ms(250) && w[1] >= ms(250) && w[1] <= ms(500) && w[2] >= ms(500) && w[2] <= ms(1000), "{w:?}");
    }

    #[test]
    fn retries_a_sound_upload() {
        let h = failing(1, Reply::default().status(503));
        h.client().put_sound("ding", "audio/wav", &[1, 2, 3]).unwrap();
        assert_eq!(h.count(), 2);
        assert_eq!(h.requests()[1].body, [1, 2, 3]);
    }

    #[test]
    fn does_not_retry_statuses_other_than_429_and_503() {
        for status in [400, 401, 404, 409, 500, 502] {
            let h = start(move |_| Reply::default().status(status));
            h.client().put_channel("ops", &channel()).unwrap_err();
            assert_eq!(h.count(), 1, "status {status}");
        }
    }

    #[test]
    fn surfaces_429_and_503_on_post_messages_untouched() {
        for status in [429, 503] {
            let h = start(move |_| Reply::default().status(status).header("Retry-After", "3").body(r#"{"error":"rate_limited"}"#));
            let err = h.client().send_message(&msg()).unwrap_err();
            let a = api(&err);
            assert_eq!((a.status, a.code.as_str(), a.retry_after), (status, "rate_limited", Some(secs(3))));
            assert_eq!(h.count(), 1);
            assert!(h.waits().is_empty());
        }
    }

    #[test]
    fn retries_a_message_with_an_idempotency_key_without_leaking_it_into_the_body() {
        let h2 = start({
            let n = AtomicUsize::new(0);
            move |_| {
                if n.fetch_add(1, Ordering::SeqCst) == 0 {
                    Reply::default().status(429).header("Retry-After", "1")
                } else {
                    Reply::default().status(202).body(r#"{"message_id":"m1","status":"queued"}"#)
                }
            }
        });
        let res = h2.client().send_message(&Message { idempotency_key: Some("order-7".into()), ..msg() }).unwrap();
        assert_eq!(res.message_id, "m1");
        let reqs = h2.requests();
        assert_eq!(reqs.len(), 2);
        for r in &reqs {
            assert_eq!(r.headers["idempotency-key"], "order-7");
            assert!(!String::from_utf8_lossy(&r.body).contains("order-7"));
        }
        assert_eq!(reqs[0].body, reqs[1].body);
    }

    #[test]
    fn an_empty_idempotency_key_does_not_enable_retries() {
        let h = start(|_| Reply::default().status(503));
        h.client().send_message(&Message { idempotency_key: Some(String::new()), ..msg() }).unwrap_err();
        assert_eq!(h.count(), 1);
        assert!(!h.requests()[0].headers.contains_key("idempotency-key"));
    }

    #[test]
    fn an_idempotency_key_that_is_not_a_header_value_is_refused_before_sending() {
        let h = start_default();
        let err = h.client().send_message(&Message { idempotency_key: Some("a\nb".into()), ..msg() }).unwrap_err();
        assert!(matches!(err, Error::InvalidArgument(_)));
        assert_eq!(h.count(), 0);
    }

    #[test]
    fn does_not_retry_patch() {
        let h = start(|_| Reply::default().status(429));
        let err = h.client().update_activity("a", &ActivityUpdate { data: json!(1), ..Default::default() }).unwrap_err();
        assert_eq!(api(&err).status, 429);
        assert_eq!(h.count(), 1);
    }

    #[test]
    fn does_not_retry_post_activities_or_pairing_tickets() {
        let h = start(|_| Reply::default().status(503));
        let c = h.client();
        c.start_activity(&Activity { template_id: "t".into(), ..Default::default() }).unwrap_err();
        c.create_pairing_ticket("u").unwrap_err();
        assert_eq!(h.count(), 2);
    }

    #[test]
    fn surfaces_a_retry_after_beyond_the_cap() {
        let h = start(|_| Reply::default().status(429).header("Retry-After", "3600"));
        let err = h.client().put_channel("ops", &channel()).unwrap_err();
        assert_eq!(api(&err).retry_after, Some(secs(3600)));
        assert_eq!(h.count(), 1);
        assert!(h.waits().is_empty());
    }

    #[test]
    fn waits_out_a_retry_after_of_exactly_the_cap() {
        let h = failing(1, Reply::default().status(429).header("Retry-After", "60"));
        h.client().put_channel("ops", &channel()).unwrap();
        assert_eq!(h.waits(), [secs(60)]);
        let h = start(|_| Reply::default().status(429).header("Retry-After", "61"));
        h.client().put_channel("ops", &channel()).unwrap_err();
        assert_eq!(h.count(), 1);
    }

    #[test]
    fn the_real_sleep_is_used_without_a_sleeper() {
        let h = failing(1, Reply::default().status(429).header("Retry-After", "0.3"));
        let t0 = Instant::now();
        Client::new(&h.url, TEST_KEY).unwrap().put_channel("ops", &channel()).unwrap();
        assert!(t0.elapsed() >= ms(300));
    }

    #[test]
    fn network_errors_are_not_retried() {
        let url = closed_port_url();
        let waits = Arc::new(Mutex::new(0));
        let w = waits.clone();
        let c = Client::builder(&url, TEST_KEY).sleeper(move |_| *w.lock().unwrap() += 1).build().unwrap();
        assert!(c.put_channel("ops", &channel()).unwrap_err().as_network().is_some());
        assert_eq!(*waits.lock().unwrap(), 0);
    }
}

mod path_escaping {
    use super::*;

    type Boxed<'a> = Box<dyn Fn() + 'a>;

    const IDS: [&str; 9] = ["a/b", "a?b=c", "a b", "ü", "日本", "a#b", "100%", "a/../b", "a%2Fb"];

    fn decode(seg: &str) -> String {
        let b = seg.as_bytes();
        let mut out = Vec::new();
        let mut i = 0;
        while i < b.len() {
            if b[i] == b'%' {
                out.push(u8::from_str_radix(&seg[i + 1..i + 3], 16).unwrap());
                i += 3;
            } else {
                out.push(b[i]);
                i += 1;
            }
        }
        String::from_utf8(out).unwrap()
    }

    #[test]
    fn escapes_ids_in_every_call() {
        for id in IDS {
            let h = start_default();
            let c = h.client();
            let calls: Vec<(&str, Boxed)> = vec![
                ("/api/v1/channels/", Box::new(|| c.put_channel(id, &channel()).unwrap())),
                ("/api/v1/channels/", Box::new(|| drop(c.get_channel(id).unwrap()))),
                ("/api/v1/channels/", Box::new(|| c.delete_channel(id).unwrap())),
                ("/api/v1/templates/", Box::new(|| c.put_template(id, 1, &json!({})).unwrap())),
                ("/api/v1/sounds/", Box::new(|| drop(c.put_sound(id, "audio/wav", &[1]).unwrap()))),
                (
                    "/api/v1/feeds/",
                    Box::new(|| drop(c.put_feed(id, &Feed { template_id: "t".into(), data: json!(1), ..Default::default() }).unwrap())),
                ),
                ("/api/v1/messages/", Box::new(|| drop(c.get_message(id).unwrap()))),
                (
                    "/api/v1/activities/",
                    Box::new(|| drop(c.update_activity(id, &ActivityUpdate { data: json!(1), ..Default::default() }).unwrap())),
                ),
                ("/api/v1/activities/", Box::new(|| c.end_activity(id).unwrap())),
            ];
            for (i, (prefix, call)) in calls.iter().enumerate() {
                call();
                let target = h.requests()[i].url.clone();
                assert!(target.starts_with(prefix), "{target}");
                let seg = &target[prefix.len()..];
                assert!(!seg.contains(['/', '?', '#', ' ']), "unescaped segment in {target}");
                assert_eq!(decode(seg), id);
            }
            c.unpair(id).unwrap();
            let target = h.requests().last().unwrap().url.clone();
            let seg = target.strip_prefix("/api/v1/users/").and_then(|t| t.strip_suffix("/pairings")).expect(&target);
            assert!(!seg.contains('/'));
            assert_eq!(decode(seg), id);
        }
    }

    #[test]
    fn produces_the_exact_expected_encoding() {
        let h = start_default();
        h.client().put_channel("ü x/y?z", &channel()).unwrap();
        assert_eq!(h.requests()[0].url, "/api/v1/channels/%C3%BC%20x%2Fy%3Fz");
        h.client().get_channel("A-z_0.9~").unwrap();
        assert_eq!(h.requests()[1].url, "/api/v1/channels/A-z_0.9~");
    }

    #[test]
    fn rejects_dot_segments_and_empty_ids_before_sending() {
        let h = start_default();
        let c = h.client();
        for bad in ["", ".", ".."] {
            assert!(matches!(c.get_channel(bad), Err(Error::InvalidArgument(_))), "{bad:?}");
            assert!(matches!(c.unpair(bad), Err(Error::InvalidArgument(_))));
            assert!(matches!(c.end_activity(bad), Err(Error::InvalidArgument(_))));
        }
        assert!(matches!(c.create_pairing_ticket(""), Err(Error::InvalidArgument(_))));
        assert_eq!(h.count(), 0);
    }

    #[test]
    fn refuses_a_sound_over_one_megabyte() {
        let h = start_default();
        let c = h.client();
        assert!(matches!(c.put_sound("s", "audio/wav", &vec![0; (1 << 20) + 1]), Err(Error::InvalidArgument(_))));
        assert_eq!(h.count(), 0);
        c.put_sound("s", "audio/wav", &vec![0; 1 << 20]).unwrap();
        assert_eq!(h.count(), 1);
    }
}

mod errors {
    use super::*;

    #[test]
    fn times_out() {
        let s = silent_server();
        let c = Client::builder(&s.url, TEST_KEY).timeout(ms(150)).build().unwrap();
        let t0 = Instant::now();
        let err = c.send_message(&msg()).unwrap_err();
        let n = err.as_network().expect("network error");
        assert!(n.timeout);
        assert!(t0.elapsed() < secs(5));
    }

    #[test]
    fn reports_network_errors_apart_from_api_errors_without_the_key() {
        let c = Client::new(closed_port_url(), TEST_KEY).unwrap();
        let err = c.put_channel("ops", &channel()).unwrap_err();
        assert!(err.as_api().is_none());
        let n = err.as_network().expect("network error");
        assert!(!n.timeout);
        assert!(!err.to_string().contains(TEST_KEY));
        assert_eq!((n.method.as_str(), n.path.as_str()), ("PUT", "/api/v1/channels/ops"));
        assert!(err.to_string().starts_with("hansho: PUT /api/v1/channels/ops: "));
    }

    #[test]
    fn never_lets_the_key_into_errors() {
        let h = start(|r| {
            let echoed = r.headers["authorization"].clone();
            Reply::default().status(401).body(json!({"error": "unauthorized", "message": format!("bad key {echoed}")}).to_string())
        });
        let err = h.client().put_channel("ops", &channel()).unwrap_err();
        let a = api(&err);
        assert!(!err.to_string().contains(TEST_KEY));
        assert!(!format!("{err:?}").contains(TEST_KEY));
        assert!(!a.detail.clone().unwrap().contains(TEST_KEY));
        assert!(a.detail.clone().unwrap().contains("[redacted]"));
    }

    #[test]
    fn leaves_the_hub_message_out_of_display() {
        let h = start(|_| {
            Reply::default()
                .status(400)
                .body(r#"{"error":"invalid_request","field":"title","message":"title is required, got my-secret-body"}"#)
        });
        let err = h.client().send_message(&msg()).unwrap_err();
        assert!(!err.to_string().contains("my-secret-body"));
        assert_eq!(api(&err).detail.as_deref(), Some("title is required, got my-secret-body"));
    }

    #[test]
    fn formats_api_error_messages() {
        let e = ApiError {
            method: "POST".into(),
            path: "/api/v1/messages".into(),
            status: 400,
            code: "invalid_request".into(),
            field: Some("title".into()),
            retry_after: None,
            detail: None,
        };
        assert_eq!(e.to_string(), "hansho: POST /api/v1/messages: 400 invalid_request (field title)");
        let e = ApiError { status: 429, code: "rate_limited".into(), field: None, retry_after: Some(secs(7)), ..e };
        assert_eq!(e.to_string(), "hansho: POST /api/v1/messages: 429 rate_limited (retry after 7000 ms)");
        let e = ApiError { status: 502, code: String::new(), retry_after: None, ..e };
        assert_eq!(e.to_string(), "hansho: POST /api/v1/messages: 502");
        let _: &dyn std::error::Error = &Error::from(e);
    }

    #[test]
    fn a_non_json_error_body_gives_status_only() {
        let h = start(|_| Reply::default().status(502).body("<html>bad gateway</html>"));
        let err = h.client().get_channel("x").unwrap_err();
        let a = api(&err);
        assert_eq!((a.status, a.code.as_str(), a.field.clone(), a.detail.clone()), (502, "", None, None));
    }

    #[test]
    fn invalid_arguments_name_the_problem() {
        let c = Client::new("http://127.0.0.1:1", TEST_KEY).unwrap();
        assert_eq!(c.get_channel("..").unwrap_err().to_string(), "hansho: id must not be \"..\"");
    }
}

mod answers_from_a_newer_hub {
    use super::*;

    #[test]
    fn an_unknown_enum_value_or_field_is_kept_not_rejected() {
        let body =
            r#"{"id":"c","name":"n","default_level":"urgent","max_level":"critical","audience":{"mode":"sometimes"},"added_later":1}"#;
        let h = start(move |_| Reply::default().body(body));
        let ch = h.client().get_channel("c").unwrap();
        assert_eq!(ch.default_level, Level::Other("urgent".into()));
        assert_eq!(ch.default_level.as_str(), "urgent");
        assert_eq!(ch.audience.unwrap().mode, AudienceMode::Other("sometimes".into()));
    }

    #[test]
    fn an_unknown_level_in_a_message_status_is_kept() {
        let h = start(|_| Reply::default().body(r#"{"message_id":"m","status":"acked","level":"holographic"}"#));
        assert_eq!(h.client().get_message("m").unwrap().level, Level::Other("holographic".into()));
    }

    #[test]
    fn a_list_answer_may_be_empty_or_null() {
        for body in ["[]", "null", ""] {
            let h = start(move |_| Reply::default().body(body));
            assert!(h.client().list_groups().unwrap().is_empty(), "{body:?}");
        }
    }

    #[test]
    fn a_list_that_is_not_an_array_is_a_network_error() {
        let h = start(|_| Reply::default().body(r#"{"id":"oncall"}"#));
        let err = h.client().list_sounds().unwrap_err();
        assert!(matches!(err, Error::Network(_)), "{err}");
    }
}

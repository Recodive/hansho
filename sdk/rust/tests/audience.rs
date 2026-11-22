//! Groups, audiences, kick and per-recipient send results.

mod common;

use common::{start, start_default, Reply};
use hansho::*;
use serde_json::{json, Value};

fn body(h: &common::Hub, i: usize) -> Value {
    serde_json::from_slice(&h.requests()[i].body).unwrap()
}

#[test]
fn groups_are_put_read_and_deleted() {
    let h = start(|r| match r.method.as_str() {
        "DELETE" => Reply::default().status(204).body(""),
        _ => Reply::default().body(r#"{"id":"on call","members":["alice","bob"]}"#),
    });
    let c = h.client();
    let got = c.put_group("on call", &["alice", "bob"]).unwrap();
    assert_eq!(got, Group { id: "on call".into(), members: vec!["alice".into(), "bob".into()] });
    assert_eq!((h.requests()[0].method.as_str(), h.requests()[0].url.as_str()), ("PUT", "/api/v1/groups/on%20call"));
    assert_eq!(body(&h, 0), json!({"members": ["alice", "bob"]}));

    c.put_group("empty", &[]).unwrap();
    assert_eq!(body(&h, 1), json!({"members": []}), "the hub requires the list, even an empty one");

    assert_eq!(c.get_group("on call").unwrap().members, ["alice", "bob"]);
    assert_eq!(h.requests()[2].method, "GET");
    c.delete_group("on call").unwrap();
    assert_eq!((h.requests()[3].method.as_str(), h.requests()[3].url.as_str()), ("DELETE", "/api/v1/groups/on%20call"));

    assert!(matches!(c.get_group(""), Err(Error::InvalidArgument(_))));
    assert_eq!(h.count(), 4);
}

#[test]
fn a_missing_group_is_unknown_group() {
    let h = start(|_| Reply::default().status(404).body(r#"{"error":"unknown_group"}"#));
    let err = h.client().get_group("nope").unwrap_err();
    assert_eq!(err.as_api().unwrap().code, CODE_UNKNOWN_GROUP);
    assert_eq!(err.as_api().unwrap().status, 404);
}

#[test]
fn a_channel_sends_its_audience_only_when_set() {
    let h = start_default();
    let c = h.client();
    let ch = Channel { name: "Pager".into(), default_level: Level::Page, max_level: Level::Page, ..Default::default() };
    c.put_channel("pager", &ch).unwrap();
    assert!(body(&h, 0).get("audience").is_none(), "absent keeps the stored audience on the hub");

    let only = Audience { mode: AudienceMode::Only, users: vec!["carol".into()], groups: vec!["oncall".into()] };
    c.put_channel("pager", &Channel { audience: Some(only), ..ch.clone() }).unwrap();
    assert_eq!(body(&h, 1)["audience"], json!({"mode": "only", "users": ["carol"], "groups": ["oncall"]}));

    c.put_channel("pager", &Channel { audience: Some(Audience::default()), ..ch }).unwrap();
    assert_eq!(body(&h, 2)["audience"], json!({"mode": "everyone"}), "an explicit everyone opens the channel");
}

#[test]
fn get_channel_reads_the_audience() {
    let h = start(|_| {
        let wire = r#"{"name":"Pager","default_level":"page","max_level":"page",
            "audience":{"mode":"except","users":["bob"],"groups":null}}"#;
        Reply::default().body(wire)
    });
    let got = h.client().get_channel("pager").unwrap();
    assert_eq!(got.audience, Some(Audience { mode: AudienceMode::Except, users: vec!["bob".into()], groups: vec![] }));
}

#[test]
fn feeds_carry_channel_and_audience_and_are_read_back() {
    let h = start(|r| {
        if r.method == "GET" {
            Reply::default().body(r#"{"feed_id":"queue","template_id":"status","channel":"pager","updated_at":1794139800}"#)
        } else {
            Reply::default().body(r#"{"feed_id":"queue","devices":1}"#)
        }
    });
    let c = h.client();
    let aud = Audience { mode: AudienceMode::Only, groups: vec!["oncall".into()], ..Default::default() };
    let feed =
        Feed { template_id: "status".into(), channel: Some("pager".into()), audience: Some(aud), data: json!(1), ..Default::default() };
    c.put_feed("queue", &feed).unwrap();
    assert_eq!(body(&h, 0)["channel"], "pager");
    assert_eq!(body(&h, 0)["audience"], json!({"mode": "only", "groups": ["oncall"]}));
    c.put_feed("queue", &Feed { template_id: "status".into(), data: json!(1), ..Default::default() }).unwrap();
    assert!(body(&h, 1).get("channel").is_none() && body(&h, 1).get("audience").is_none());

    let got = c.get_feed("queue", None).unwrap();
    assert_eq!(
        got,
        FeedSettings {
            feed_id: "queue".into(),
            template_id: "status".into(),
            channel: Some("pager".into()),
            updated_at: 1_794_139_800,
            ..Default::default()
        }
    );
    assert_eq!(h.requests()[2].url, "/api/v1/feeds/queue");
    c.get_feed("queue", Some("u 1")).unwrap();
    assert_eq!(h.requests()[3].url, "/api/v1/feeds/queue?external_user_id=u%201");
}

#[test]
fn kick_deletes_the_users_pairings() {
    let h = start_default();
    h.client().kick("user 42").unwrap();
    let r = &h.requests()[0];
    assert_eq!((r.method.as_str(), r.url.as_str()), ("DELETE", "/api/v1/users/user%2042/pairings"));
}

#[test]
fn send_targets_are_serialised_one_at_a_time() {
    let h = start(|_| Reply::default().status(202));
    let c = h.client();
    let base = Message { channel: "pager".into(), title: "t".into(), body: "b".into(), ..Default::default() };

    c.send_message(&Message { external_user_ids: vec!["alice".into(), "carol".into()], ..base.clone() }).unwrap();
    let b = body(&h, 0);
    assert_eq!(b["external_user_ids"], json!(["alice", "carol"]));
    assert!(b.get("external_user_id").is_none() && b.get("to_audience").is_none());

    c.send_message(&Message { to_audience: true, ..base.clone() }).unwrap();
    let b = body(&h, 1);
    assert_eq!(b["to_audience"], true);
    assert!(b.get("external_user_id").is_none() && b.get("external_user_ids").is_none());

    c.send_message(&Message { external_user_id: "alice".into(), ..base }).unwrap();
    let b = body(&h, 2);
    assert_eq!(b["external_user_id"], "alice");
    assert!(b.get("external_user_ids").is_none() && b.get("to_audience").is_none());
}

#[test]
fn per_recipient_results_and_the_old_top_level_fields() {
    let h = start(|r| {
        let body = String::from_utf8_lossy(&r.body).into_owned();
        let reply = if body.contains("external_user_ids") {
            r#"{"level":"page","recipients":[
                {"external_user_id":"alice","message_id":"msg_1","status":"queued","devices":1,
                 "deliveries":[{"device_id":"d1","status":"queued"}]},
                {"external_user_id":"carol","status":"no_access"}]}"#
        } else {
            r#"{"message_id":"","level":"page","devices":0,"status":"no_access","deliveries":[],
                "recipients":[{"external_user_id":"carol","status":"no_access"}]}"#
        };
        Reply::default().status(202).body(reply)
    });
    let c = h.client();
    let base = Message { channel: "pager".into(), title: "t".into(), body: "b".into(), ..Default::default() };

    let res = c.send_message(&Message { external_user_ids: vec!["alice".into(), "carol".into()], ..base.clone() }).unwrap();
    assert_eq!(res.message_id, "");
    assert_eq!(res.level, Level::Page);
    assert_eq!(res.recipients.len(), 2);
    assert_eq!(res.recipients[0].message_id.as_deref(), Some("msg_1"));
    assert_eq!(res.recipients[0].devices, 1);
    assert_eq!(res.recipients[0].deliveries.len(), 1);
    assert_eq!(res.recipients[1].message_id, None);
    assert_eq!(res.recipients[1].status, RECIPIENT_NO_ACCESS);

    let one = c.send_message(&Message { external_user_id: "carol".into(), ..base }).unwrap();
    assert_eq!((one.message_id.as_str(), one.status.as_str(), one.devices), ("", RECIPIENT_NO_ACCESS, 0));
    assert_eq!(one.recipients[0].external_user_id, "carol");
}

#[test]
fn a_send_without_recipients_still_parses() {
    let wire = r#"{"message_id":"msg_1","level":"active","devices":1,"status":"queued","deliveries":[]}"#;
    let h = start(move |_| Reply::default().status(202).body(wire));
    let res = h.client().send_message(&Message::new("u", "c", "t", "b")).unwrap();
    assert_eq!(res.message_id, "msg_1");
    assert!(res.recipients.is_empty());
}

#[test]
fn no_access_and_an_unknown_group_in_an_audience_are_api_errors() {
    let h = start(|r| {
        if r.url.contains("activities") {
            Reply::default().status(403).body(r#"{"error":"no_access"}"#)
        } else {
            Reply::default().status(400).body(r#"{"error":"invalid_request","field":"audience.groups","message":"unknown group"}"#)
        }
    });
    let c = h.client();
    let activity = Activity { external_user_id: "carol".into(), channel: "pager".into(), template_id: "t".into(), ..Default::default() };
    let err = c.start_activity(&activity).unwrap_err();
    assert_eq!((err.as_api().unwrap().status, err.as_api().unwrap().code.as_str()), (403, CODE_NO_ACCESS));

    let aud = Audience { mode: AudienceMode::Only, groups: vec!["ghost".into()], ..Default::default() };
    let err = c.put_channel("pager", &Channel { name: "n".into(), audience: Some(aud), ..Default::default() }).unwrap_err();
    let e = err.as_api().unwrap();
    assert_eq!((e.status, e.code.as_str()), (400, "invalid_request"));
    assert!(e.field.is_some());
}

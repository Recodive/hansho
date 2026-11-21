//! Test helpers: a local stand-in for the hub built on std::net, so the tests see the exact bytes the client sends.
#![allow(dead_code)]

use std::collections::HashMap;
use std::fs;
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

use hansho::{Client, ClientBuilder};
use serde_json::Value;

pub const TEST_KEY: &str = "ck_test_SECRET_do_not_leak";

#[derive(Debug, Clone)]
pub struct Recorded {
    pub method: String,
    /// The raw request target, exactly as sent.
    pub url: String,
    /// Header names are lower-cased.
    pub headers: HashMap<String, String>,
    pub body: Vec<u8>,
}

#[derive(Debug, Clone)]
pub struct Reply {
    pub status: u16,
    pub headers: Vec<(String, String)>,
    pub body: String,
}

impl Reply {
    pub fn status(mut self, status: u16) -> Self {
        self.status = status;
        self
    }
    pub fn header(mut self, k: &str, v: &str) -> Self {
        self.headers.push((k.into(), v.into()));
        self
    }
    pub fn body(mut self, body: impl Into<String>) -> Self {
        self.body = body.into();
        self
    }
}

impl Default for Reply {
    fn default() -> Self {
        Reply { status: 200, headers: Vec::new(), body: "{}".into() }
    }
}

type Handler = dyn Fn(&Recorded) -> Reply + Send + Sync;

pub struct Hub {
    pub url: String,
    pub requests: Arc<Mutex<Vec<Recorded>>>,
    pub waits: Arc<Mutex<Vec<Duration>>>,
    stop: Arc<AtomicBool>,
    thread: Option<JoinHandle<()>>,
}

/// Starts a hub whose answers come from `handler` (default: 200 `{}`).
pub fn start(handler: impl Fn(&Recorded) -> Reply + Send + Sync + 'static) -> Hub {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    listener.set_nonblocking(true).unwrap();
    let url = format!("http://{}", listener.local_addr().unwrap());
    let requests = Arc::new(Mutex::new(Vec::new()));
    let stop = Arc::new(AtomicBool::new(false));
    let handler: Arc<Handler> = Arc::new(handler);
    let (reqs, stop2) = (requests.clone(), stop.clone());
    let thread = thread::spawn(move || {
        while !stop2.load(Ordering::Relaxed) {
            match listener.accept() {
                Ok((conn, _)) => {
                    let (handler, reqs) = (handler.clone(), reqs.clone());
                    thread::spawn(move || serve(conn, &*handler, &reqs));
                }
                Err(_) => thread::sleep(Duration::from_millis(2)),
            }
        }
    });
    Hub { url, requests, waits: Arc::default(), stop, thread: Some(thread) }
}

pub fn start_default() -> Hub {
    start(|_| Reply::default())
}

impl Drop for Hub {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
        if let Some(t) = self.thread.take() {
            let _ = t.join();
        }
    }
}

impl Hub {
    /// A client for this hub whose retry waits are recorded in `waits` instead of slept.
    pub fn builder(&self) -> ClientBuilder {
        let waits = self.waits.clone();
        Client::builder(&self.url, TEST_KEY).sleeper(move |d| waits.lock().unwrap().push(d))
    }

    pub fn client(&self) -> Client {
        self.builder().build().unwrap()
    }

    pub fn requests(&self) -> Vec<Recorded> {
        self.requests.lock().unwrap().clone()
    }

    pub fn count(&self) -> usize {
        self.requests.lock().unwrap().len()
    }

    pub fn waits(&self) -> Vec<Duration> {
        self.waits.lock().unwrap().clone()
    }
}

/// A server that accepts connections and never answers.
pub struct SilentServer {
    pub url: String,
    stop: Arc<AtomicBool>,
    thread: Option<JoinHandle<()>>,
}

pub fn silent_server() -> SilentServer {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    listener.set_nonblocking(true).unwrap();
    let url = format!("http://{}", listener.local_addr().unwrap());
    let stop = Arc::new(AtomicBool::new(false));
    let stop2 = stop.clone();
    let thread = thread::spawn(move || {
        let mut held = Vec::new();
        while !stop2.load(Ordering::Relaxed) {
            match listener.accept() {
                Ok((conn, _)) => held.push(conn),
                Err(_) => thread::sleep(Duration::from_millis(2)),
            }
        }
    });
    SilentServer { url, stop, thread: Some(thread) }
}

impl Drop for SilentServer {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
        if let Some(t) = self.thread.take() {
            let _ = t.join();
        }
    }
}

/// A URL nothing listens on (port 1 is reserved and unused).
pub fn closed_port_url() -> String {
    "http://127.0.0.1:1".into()
}

/// Reads one HTTP/1.1 request (Content-Length bodies only) from the stream.
pub fn read_request(conn: &mut TcpStream) -> Option<Recorded> {
    conn.set_read_timeout(Some(Duration::from_secs(10))).ok()?;
    let mut buf = Vec::new();
    let mut chunk = [0u8; 4096];
    let head_end = loop {
        if let Some(i) = buf.windows(4).position(|w| w == b"\r\n\r\n") {
            break i;
        }
        let n = conn.read(&mut chunk).ok()?;
        if n == 0 {
            return None;
        }
        buf.extend_from_slice(&chunk[..n]);
    };
    let head = String::from_utf8_lossy(&buf[..head_end]).into_owned();
    let mut lines = head.split("\r\n");
    let mut first = lines.next()?.split(' ');
    let (method, url) = (first.next()?.to_owned(), first.next()?.to_owned());
    let headers: HashMap<String, String> =
        lines.filter_map(|l| l.split_once(':')).map(|(k, v)| (k.trim().to_ascii_lowercase(), v.trim().to_owned())).collect();
    let want: usize = headers.get("content-length").and_then(|v| v.parse().ok()).unwrap_or(0);
    let mut body = buf[head_end + 4..].to_vec();
    while body.len() < want {
        let n = conn.read(&mut chunk).ok()?;
        if n == 0 {
            return None;
        }
        body.extend_from_slice(&chunk[..n]);
    }
    body.truncate(want);
    Some(Recorded { method, url, headers, body })
}

pub fn write_reply(conn: &mut TcpStream, reply: &Reply) {
    let mut out = format!("HTTP/1.1 {} X\r\nConnection: close\r\n", reply.status);
    for (k, v) in &reply.headers {
        out.push_str(&format!("{k}: {v}\r\n"));
    }
    if reply.status == 204 {
        out.push_str("\r\n");
    } else {
        out.push_str(&format!("Content-Type: application/json\r\nContent-Length: {}\r\n\r\n{}", reply.body.len(), reply.body));
    }
    let _ = conn.write_all(out.as_bytes());
    let _ = conn.flush();
}

fn serve(mut conn: TcpStream, handler: &Handler, requests: &Mutex<Vec<Recorded>>) {
    conn.set_nonblocking(false).ok();
    let Some(rec) = read_request(&mut conn) else { return };
    let reply = handler(&rec);
    requests.lock().unwrap().push(rec);
    write_reply(&mut conn, &reply);
}

fn spec_path(name: &str) -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../spec").join(name)
}

pub fn load_spec(name: &str) -> Value {
    serde_json::from_str(&fs::read_to_string(spec_path(name)).unwrap()).unwrap()
}

/// Polls `f` until it returns Some or the timeout passes.
pub fn eventually<T>(timeout: Duration, mut f: impl FnMut() -> Option<T>) -> Option<T> {
    let deadline = Instant::now() + timeout;
    loop {
        if let Some(v) = f() {
            return Some(v);
        }
        if Instant::now() >= deadline {
            return None;
        }
        thread::sleep(Duration::from_millis(50));
    }
}

/// Standard base64 decoding for the spec's `raw_base64` bodies.
pub fn base64_decode(s: &str) -> Vec<u8> {
    let val = |c: u8| match c {
        b'A'..=b'Z' => c - b'A',
        b'a'..=b'z' => c - b'a' + 26,
        b'0'..=b'9' => c - b'0' + 52,
        b'+' => 62,
        b'/' => 63,
        _ => panic!("bad base64"),
    };
    let mut out = Vec::new();
    let (mut acc, mut bits) = (0u32, 0);
    for c in s.bytes().filter(|&c| c != b'=') {
        acc = acc << 6 | u32::from(val(c));
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((acc >> bits) as u8);
            acc &= (1 << bits) - 1;
        }
    }
    out
}

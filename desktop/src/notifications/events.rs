//! The daemon's events stream, read from Rust so notifications work while the
//! window is closed or hidden. The client is a small blocking HTTP/1.0 reader
//! (a response without chunked encoding, ended by closing the connection) so
//! the app needs no HTTP crate for a loopback socket.
//!
//! Authentication. The daemon accepts one bearer credential, the launch
//! capability the app already holds in `credential.json` and sends to
//! `/api/health`; it has no read-only or events-only scope. The app sends it
//! only in an `Authorization` header to the two routes it uses: `GET
//! /api/events` (read-only) and `POST /api/auth/exchange` (to learn the app
//! path for a click). It never goes in a URL, a log line or a page.

use std::io::{self, BufRead, BufReader, Read, Write};
use std::net::{SocketAddr, TcpStream};
use std::time::{Duration, Instant};

use serde::Deserialize;
use url::Url;

use crate::daemon::Launch;

const CONNECT_TIMEOUT: Duration = Duration::from_secs(5);
/// How often a quiet stream checks whether it should stop. The daemon pings
/// every 20 seconds, so a minute of silence is a dead connection.
const POLL: Duration = Duration::from_secs(2);
const IDLE_LIMIT: Duration = Duration::from_secs(60);
const MAX_LINE: usize = 64 << 10;
const MAX_BACKOFF: Duration = Duration::from_secs(30);
/// A cursor no event reaches: the daemon answers with a `reset` carrying the
/// latest id and then sends only new events, so history is never replayed as
/// news.
const NO_HISTORY: u64 = u64::MAX;

/// A daemon's app transport and the credential for it.
#[derive(Clone)]
pub struct Endpoint {
    addr: SocketAddr,
    authority: String,
    token: String,
}

impl std::fmt::Debug for Endpoint {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Endpoint")
            .field("authority", &self.authority)
            .finish_non_exhaustive()
    }
}

impl Endpoint {
    pub fn from_launch(launch: &Launch) -> Option<Endpoint> {
        let token = launch.url.fragment()?.strip_prefix("token=")?.to_string();
        Endpoint::new(launch.url.host_str()?, launch.url.port()?, token)
    }

    fn new(host: &str, port: u16, token: String) -> Option<Endpoint> {
        let authority = format!("{host}:{port}");
        Some(Endpoint {
            addr: authority.parse().ok()?,
            authority,
            token,
        })
    }

    pub fn origin(&self) -> url::Origin {
        Url::parse(&format!("http://{}/", self.authority))
            .expect("a socket address is a URL authority")
            .origin()
    }

    fn base(&self) -> Url {
        Url::parse(&format!("http://{}/", self.authority)).expect("a socket address is a URL")
    }
}

/// One server-sent event.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Event {
    pub id: Option<u64>,
    pub name: String,
    pub data: String,
}

/// Reads events until `stop` says so, reconnecting with a growing delay and
/// resuming after the last event seen.
pub fn run(endpoint: &Endpoint, stop: &dyn Fn() -> bool, on_event: &mut dyn FnMut(&Event)) {
    let mut last = NO_HISTORY;
    let mut delay = Duration::from_secs(1);
    let mut failing = false;
    while !stop() {
        let started = Instant::now();
        let result = stream(endpoint, &mut last, stop, on_event);
        if stop() {
            return;
        }
        if let Err(e) = result
            && !failing
        {
            failing = true;
            eprintln!("baum desktop: the daemon's events stream ended ({e}); reconnecting");
        }
        if started.elapsed() > MAX_BACKOFF {
            delay = Duration::from_secs(1);
            failing = false;
        }
        sleep(delay, stop);
        delay = (delay * 2).min(MAX_BACKOFF);
    }
}

fn sleep(total: Duration, stop: &dyn Fn() -> bool) {
    let end = Instant::now() + total;
    while Instant::now() < end && !stop() {
        std::thread::sleep(Duration::from_millis(100));
    }
}

fn invalid(message: impl Into<String>) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, message.into())
}

/// One connection's worth of events. Returns when the connection ends, which
/// is an error unless `stop` asked for it.
fn stream(
    endpoint: &Endpoint,
    last: &mut u64,
    stop: &dyn Fn() -> bool,
    on_event: &mut dyn FnMut(&Event),
) -> io::Result<()> {
    let mut socket = TcpStream::connect_timeout(&endpoint.addr, CONNECT_TIMEOUT)?;
    socket.set_read_timeout(Some(POLL))?;
    socket.set_write_timeout(Some(CONNECT_TIMEOUT))?;
    write!(
        socket,
        "GET /api/events HTTP/1.0\r\nHost: {}\r\nAuthorization: Bearer {}\r\nAccept: text/event-stream\r\nLast-Event-ID: {}\r\n\r\n",
        endpoint.authority, endpoint.token, last
    )?;
    let mut reader = BufReader::new(socket);
    let mut line = String::new();
    let mut idle = Duration::ZERO;
    let mut in_headers = true;
    let mut seen_status = false;
    let (mut id, mut name, mut data) = (None, String::new(), String::new());
    loop {
        match reader.read_line(&mut line) {
            Ok(0) => return Err(io::ErrorKind::UnexpectedEof.into()),
            Ok(_) if !line.ends_with('\n') => continue,
            Ok(_) => idle = Duration::ZERO,
            Err(e)
                if matches!(
                    e.kind(),
                    io::ErrorKind::WouldBlock | io::ErrorKind::TimedOut
                ) =>
            {
                if stop() {
                    return Ok(());
                }
                idle += POLL;
                if idle >= IDLE_LIMIT {
                    return Err(io::ErrorKind::TimedOut.into());
                }
                if line.len() > MAX_LINE {
                    return Err(invalid("event line too long"));
                }
                continue;
            }
            Err(e) => return Err(e),
        }
        if line.len() > MAX_LINE {
            return Err(invalid("event line too long"));
        }
        let text = line.trim_end_matches(['\r', '\n']).to_string();
        line.clear();
        if in_headers {
            if !seen_status {
                seen_status = true;
                if text.split(' ').nth(1) != Some("200") {
                    return Err(invalid(format!("the daemon answered {text:?}")));
                }
            } else if text.is_empty() {
                in_headers = false;
            }
            continue;
        }
        if text.is_empty() {
            if !name.is_empty() || !data.is_empty() {
                let event = Event {
                    id,
                    name: if name.is_empty() {
                        "message".into()
                    } else {
                        std::mem::take(&mut name)
                    },
                    data: std::mem::take(&mut data),
                };
                if let Some(id) = event.id {
                    *last = id;
                }
                on_event(&event);
            }
            (id, name, data) = (None, String::new(), String::new());
            continue;
        }
        let (field, value) = text.split_once(':').unwrap_or((&text, ""));
        let value = value.strip_prefix(' ').unwrap_or(value);
        match field {
            "id" => id = value.parse().ok(),
            "event" => name = value.to_string(),
            "data" => {
                if !data.is_empty() {
                    data.push('\n');
                }
                data.push_str(value);
            }
            _ => {}
        }
        if data.len() > MAX_LINE {
            return Err(invalid("event too large"));
        }
    }
}

#[derive(Deserialize)]
struct Exchanged {
    path: String,
}

/// The daemon's app path, `/s/<secret>/`, as an absolute URL. The window's
/// session cookie is scoped to it; the exchange hands out the path again and
/// changes nothing else.
pub fn app_url(endpoint: &Endpoint) -> io::Result<Url> {
    let body = format!(r#"{{"token":"{}"}}"#, endpoint.token);
    let mut socket = TcpStream::connect_timeout(&endpoint.addr, CONNECT_TIMEOUT)?;
    socket.set_read_timeout(Some(CONNECT_TIMEOUT))?;
    socket.set_write_timeout(Some(CONNECT_TIMEOUT))?;
    write!(
        socket,
        "POST /api/auth/exchange HTTP/1.0\r\nHost: {0}\r\nOrigin: http://{0}\r\nContent-Type: application/json\r\nContent-Length: {1}\r\n\r\n{2}",
        endpoint.authority,
        body.len(),
        body
    )?;
    let mut response = Vec::new();
    socket.take(8192).read_to_end(&mut response)?;
    let response = String::from_utf8_lossy(&response);
    let (head, body) = response
        .split_once("\r\n\r\n")
        .ok_or_else(|| invalid("no response body"))?;
    if head.lines().next().and_then(|l| l.split(' ').nth(1)) != Some("200") {
        return Err(invalid("the daemon refused the credential"));
    }
    let exchanged: Exchanged =
        serde_json::from_str(body).map_err(|e| invalid(format!("unreadable exchange: {e}")))?;
    let url = endpoint
        .base()
        .join(&exchanged.path)
        .map_err(|e| invalid(e.to_string()))?;
    let namespaced = url.path().starts_with("/s/") && url.path().ends_with('/');
    if url.origin() != endpoint.origin() || !namespaced || url.query().is_some() {
        return Err(invalid("the daemon named an unexpected app path"));
    }
    Ok(url)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::net::TcpListener;
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::{Arc, Mutex};
    use std::thread;

    const TOKEN: &str = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ";

    fn server() -> (TcpListener, Endpoint) {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        (
            listener,
            Endpoint::new("127.0.0.1", port, TOKEN.into()).unwrap(),
        )
    }

    /// Reads the request head the client sent.
    fn request(socket: &mut TcpStream) -> String {
        let mut reader = BufReader::new(socket.try_clone().unwrap());
        let mut head = String::new();
        loop {
            let mut line = String::new();
            reader.read_line(&mut line).unwrap();
            if line.trim().is_empty() {
                return head;
            }
            head.push_str(&line);
        }
    }

    #[test]
    fn reads_events_and_authenticates_with_a_header() {
        let (listener, endpoint) = server();
        let served = thread::spawn(move || {
            let (mut socket, _) = listener.accept().unwrap();
            let head = request(&mut socket);
            write!(
                socket,
                "HTTP/1.0 200 OK\r\nContent-Type: text/event-stream\r\n\r\n: connected\n\nid: 5\nevent: reset\ndata: {{}}\n\nid: 6\nevent: notice\ndata: {{\"a\":\ndata: 1}}\n\n: ping\n\nevent: index\ndata: {{}}\n\n"
            )
            .unwrap();
            head
        });
        let mut got = Vec::new();
        let mut last = NO_HISTORY;
        let result = stream(&endpoint, &mut last, &|| false, &mut |e| {
            got.push(e.clone())
        });
        assert!(result.is_err(), "the server closed the stream");
        let head = served.join().unwrap();
        assert!(head.starts_with("GET /api/events HTTP/1.0\r\n"), "{head}");
        assert!(head.contains(&format!("Authorization: Bearer {TOKEN}\r\n")));
        assert!(head.contains(&format!("Last-Event-ID: {}\r\n", u64::MAX)));
        assert!(head.contains("Host: 127.0.0.1:"));
        assert_eq!(
            got,
            [
                Event {
                    id: Some(5),
                    name: "reset".into(),
                    data: "{}".into()
                },
                Event {
                    id: Some(6),
                    name: "notice".into(),
                    data: "{\"a\":\n1}".into()
                },
                Event {
                    id: None,
                    name: "index".into(),
                    data: "{}".into()
                },
            ]
        );
        assert_eq!(last, 6);
    }

    #[test]
    fn a_refused_credential_is_an_error() {
        let (listener, endpoint) = server();
        thread::spawn(move || {
            let (mut socket, _) = listener.accept().unwrap();
            request(&mut socket);
            write!(socket, "HTTP/1.0 401 Unauthorized\r\n\r\nunauthorized\n").unwrap();
        });
        let mut last = NO_HISTORY;
        let error = stream(&endpoint, &mut last, &|| false, &mut |_| {
            panic!("no events")
        })
        .unwrap_err();
        assert!(error.to_string().contains("401"), "{error}");
        assert!(!error.to_string().contains(TOKEN));
    }

    #[test]
    fn reconnects_after_the_last_event_seen() {
        let (listener, endpoint) = server();
        let heads = Arc::new(Mutex::new(Vec::new()));
        let seen = heads.clone();
        let stopped = Arc::new(AtomicBool::new(false));
        let flag = stopped.clone();
        let served = thread::spawn(move || {
            for n in 0..2 {
                let (mut socket, _) = listener.accept().unwrap();
                seen.lock().unwrap().push(request(&mut socket));
                write!(
                    socket,
                    "HTTP/1.0 200 OK\r\n\r\nid: {0}\nevent: notice\ndata: {{}}\n\n",
                    10 + n
                )
                .unwrap();
            }
            flag.store(true, Ordering::SeqCst);
        });
        let mut ids = Vec::new();
        run(&endpoint, &|| stopped.load(Ordering::SeqCst), &mut |e| {
            ids.push(e.id)
        });
        served.join().unwrap();
        assert_eq!(ids, [Some(10), Some(11)]);
        let heads = heads.lock().unwrap();
        assert!(heads[0].contains(&format!("Last-Event-ID: {}", u64::MAX)));
        assert!(heads[1].contains("Last-Event-ID: 10"), "{}", heads[1]);
    }

    #[test]
    fn exchange_returns_the_app_url() {
        let (listener, endpoint) = server();
        let served = thread::spawn(move || {
            let (mut socket, _) = listener.accept().unwrap();
            let head = request(&mut socket);
            let body = br#"{"path":"/s/secret_ns/"}"#;
            socket
                .write_all(b"HTTP/1.0 200 OK\r\nContent-Type: application/json\r\n\r\n")
                .unwrap();
            socket.write_all(body).unwrap();
            head
        });
        let url = app_url(&endpoint).unwrap();
        assert_eq!(url.path(), "/s/secret_ns/");
        assert_eq!(url.origin(), endpoint.origin());
        let head = served.join().unwrap();
        assert!(head.starts_with("POST /api/auth/exchange HTTP/1.0\r\n"));
        assert!(head.contains(&format!("Origin: http://{}", endpoint.authority)));
    }

    #[test]
    fn exchange_refuses_paths_off_the_daemon() {
        for path in [
            "http://evil.example/s/x/",
            "/other/",
            "/s/x/?q=1",
            "//evil.example/s/x/",
        ] {
            let (listener, endpoint) = server();
            let path = path.to_string();
            thread::spawn(move || {
                let (mut socket, _) = listener.accept().unwrap();
                request(&mut socket);
                write!(socket, "HTTP/1.0 200 OK\r\n\r\n{{\"path\":\"{path}\"}}").unwrap();
            });
            assert!(app_url(&endpoint).is_err());
        }
    }

    #[test]
    fn the_credential_is_not_in_debug_output() {
        let (_listener, endpoint) = server();
        assert!(!format!("{endpoint:?}").contains(TOKEN));
    }
}

//! The daemon's shared native events stream, read from Rust while the
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
use std::net::{Shutdown, SocketAddr, TcpStream};
use std::sync::Mutex;
use std::time::Duration;

use serde::Deserialize;
use url::Url;

use crate::daemon::Launch;

/// The daemon pings every 20 s. A stream silent for this long is dead.
pub const READ_TIMEOUT: Duration = Duration::from_secs(70);

const CONNECT_TIMEOUT: Duration = Duration::from_secs(5);
const MAX_LINE: usize = 64 << 10;
/// A cursor no event reaches: the daemon answers with a `reset` carrying the
/// latest id and then sends only new events, so history is never replayed as
/// news.
pub const NO_HISTORY: u64 = u64::MAX;

/// A daemon's app transport and the credential for it.
#[derive(Clone, PartialEq, Eq)]
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
        let addr: SocketAddr = authority.parse().ok()?;
        // The credential is only ever sent to this machine.
        if !addr.ip().is_loopback() {
            return None;
        }
        Some(Endpoint {
            addr,
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

/// Lets attachment changes interrupt a blocked read without a polling timer.
#[derive(Default)]
pub struct Control(Mutex<Option<TcpStream>>);

impl Control {
    pub fn cancel(&self) {
        if let Some(socket) = self.0.lock().unwrap_or_else(|p| p.into_inner()).take() {
            let _ = socket.shutdown(Shutdown::Both);
        }
    }

    fn track(&self, socket: &TcpStream) -> io::Result<Reading<'_>> {
        *self.0.lock().unwrap_or_else(|p| p.into_inner()) = Some(socket.try_clone()?);
        Ok(Reading(self))
    }
}

struct Reading<'a>(&'a Control);

impl Drop for Reading<'_> {
    fn drop(&mut self) {
        self.0.cancel();
    }
}

/// One server-sent event.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Event {
    pub id: Option<u64>,
    pub name: String,
    pub data: String,
}

/// Items shared by the tray and notification consumers.
#[derive(Debug, PartialEq, Eq)]
pub enum StreamItem {
    Open,
    Event(Event),
    Ping,
}

fn invalid(message: impl Into<String>) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, message.into())
}

/// One connection's worth of events. Returns when the connection ends, which
/// is an error unless `stop` asked for it.
pub fn stream(
    endpoint: &Endpoint,
    last: &mut u64,
    timeout: Duration,
    stop: &dyn Fn() -> bool,
    control: &Control,
    on_item: &mut dyn FnMut(StreamItem),
) -> io::Result<()> {
    let mut socket = TcpStream::connect_timeout(&endpoint.addr, CONNECT_TIMEOUT)?;
    let _reading = control.track(&socket)?;
    if stop() {
        return Ok(());
    }
    socket.set_read_timeout(Some(timeout))?;
    socket.set_write_timeout(Some(CONNECT_TIMEOUT))?;
    write!(
        socket,
        "GET /api/events HTTP/1.0\r\nHost: {}\r\nAuthorization: Bearer {}\r\nAccept: text/event-stream\r\nLast-Event-ID: {}\r\n\r\n",
        endpoint.authority, endpoint.token, last
    )?;
    let mut reader = BufReader::new(socket);
    // Decode only complete lines, including multibyte characters split
    // across socket reads.
    let mut buf: Vec<u8> = Vec::new();
    let mut in_headers = true;
    let mut seen_status = false;
    let (mut id, mut name, mut data) = (None, String::new(), String::new());
    loop {
        if stop() {
            return Ok(());
        }
        // A peer that never sends a newline cannot grow the buffer past this.
        let room = MAX_LINE.saturating_sub(buf.len());
        if room == 0 {
            return Err(invalid("event line too long"));
        }
        match (&mut reader).take(room as u64).read_until(b'\n', &mut buf) {
            Ok(0) => return Err(io::ErrorKind::UnexpectedEof.into()),
            Ok(_) if buf.last() != Some(&b'\n') => continue,
            Ok(_) => {}
            Err(e) => return Err(e),
        }
        if stop() {
            return Ok(());
        }
        let text = String::from_utf8_lossy(&buf)
            .trim_end_matches(['\r', '\n'])
            .to_string();
        buf.clear();
        if in_headers {
            if !seen_status {
                seen_status = true;
                if text.split(' ').nth(1) != Some("200") {
                    return Err(invalid(format!("the daemon answered {text:?}")));
                }
            } else if text.is_empty() {
                in_headers = false;
                on_item(StreamItem::Open);
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
                on_item(StreamItem::Event(event));
            }
            (id, name, data) = (None, String::new(), String::new());
            continue;
        }
        if text.starts_with(':') {
            on_item(StreamItem::Ping);
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
    let body = serde_json::json!({ "token": endpoint.token }).to_string();
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
    use std::sync::{Arc, Mutex};
    use std::thread;

    const POLL: Duration = Duration::from_secs(2);

    fn read_events(
        endpoint: &Endpoint,
        last: &mut u64,
        stop: &dyn Fn() -> bool,
        on_event: &mut dyn FnMut(&Event),
    ) -> io::Result<()> {
        stream(
            endpoint,
            last,
            POLL,
            stop,
            &Control::default(),
            &mut |item| {
                if let StreamItem::Event(event) = item {
                    on_event(&event);
                }
            },
        )
    }

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
        let result = read_events(&endpoint, &mut last, &|| false, &mut |e| {
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
    fn one_stream_delivers_liveness_and_full_events() {
        let (listener, endpoint) = server();
        let served = thread::spawn(move || {
            let (mut socket, _) = listener.accept().unwrap();
            request(&mut socket);
            write!(socket, "HTTP/1.0 200 OK\r\n\r\n: ping\n\nid: 9\nevent: notice\ndata: {{\"kind\":\"review_finished\"}}\n\n").unwrap();
        });
        let mut items = Vec::new();
        let mut last = NO_HISTORY;
        let _ = stream(
            &endpoint,
            &mut last,
            POLL,
            &|| false,
            &Control::default(),
            &mut |item| items.push(item),
        );
        served.join().unwrap();
        assert_eq!(
            items,
            vec![
                StreamItem::Open,
                StreamItem::Ping,
                StreamItem::Event(Event {
                    id: Some(9),
                    name: "notice".into(),
                    data: r#"{"kind":"review_finished"}"#.into(),
                }),
            ]
        );
        assert_eq!(last, 9);
    }

    #[test]
    fn replacement_stops_before_dispatching_buffered_events() {
        let (listener, endpoint) = server();
        let served = thread::spawn(move || {
            let (mut socket, _) = listener.accept().unwrap();
            request(&mut socket);
            write!(
                socket,
                "HTTP/1.0 200 OK\r\n\r\nid: 9\nevent: notice\ndata: {{}}\n\n"
            )
            .unwrap();
        });
        let stopped = std::cell::Cell::new(false);
        let mut last = NO_HISTORY;
        stream(
            &endpoint,
            &mut last,
            POLL,
            &|| stopped.get(),
            &Control::default(),
            &mut |item| {
                assert_eq!(item, StreamItem::Open);
                stopped.set(true);
            },
        )
        .unwrap();
        served.join().unwrap();
        assert_eq!(last, NO_HISTORY);
    }

    #[test]
    fn cancellation_interrupts_a_quiet_socket() {
        let (listener, endpoint) = server();
        let (release, released) = std::sync::mpsc::channel();
        let served = thread::spawn(move || {
            let (mut socket, _) = listener.accept().unwrap();
            request(&mut socket);
            write!(socket, "HTTP/1.0 200 OK\r\n\r\n").unwrap();
            released.recv().unwrap();
        });
        let control = Arc::new(Control::default());
        let reader_control = control.clone();
        let (opened, ready) = std::sync::mpsc::channel();
        let (finished, done) = std::sync::mpsc::channel();
        let reader = thread::spawn(move || {
            let mut last = NO_HISTORY;
            let _ = stream(
                &endpoint,
                &mut last,
                READ_TIMEOUT,
                &|| false,
                &reader_control,
                &mut |item| {
                    assert_eq!(item, StreamItem::Open);
                    opened.send(()).unwrap();
                },
            );
            finished.send(()).unwrap();
        });
        ready.recv_timeout(Duration::from_secs(5)).unwrap();
        control.cancel();
        let result = done.recv_timeout(Duration::from_secs(2));
        // Release the server even if cancellation failed, so a failed test
        // never leaves a thread waiting for the full socket timeout.
        release.send(()).unwrap();
        served.join().unwrap();
        reader.join().unwrap();
        result.expect("cancellation must interrupt the read before its 70-second timeout");
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
        let error = read_events(&endpoint, &mut last, &|| false, &mut |_| {
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
        });
        let mut ids = Vec::new();
        let mut last = NO_HISTORY;
        for _ in 0..2 {
            let _ = read_events(&endpoint, &mut last, &|| false, &mut |e| ids.push(e.id));
        }
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
            // Read the request body too: closing a socket with unread data
            // resets the connection, which can reach the client before the
            // response does.
            socket
                .set_read_timeout(Some(Duration::from_millis(200)))
                .unwrap();
            let _ = socket.read(&mut [0; 4096]);
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
    fn a_peer_that_never_sends_a_newline_is_cut_off() {
        let (listener, endpoint) = server();
        thread::spawn(move || {
            let (mut socket, _) = listener.accept().unwrap();
            request(&mut socket);
            let _ = socket.write_all(b"HTTP/1.0 200 OK\r\n\r\ndata: ");
            let chunk = vec![b'x'; 8192];
            while socket.write_all(&chunk).is_ok() {}
        });
        let mut last = NO_HISTORY;
        let error = read_events(&endpoint, &mut last, &|| false, &mut |_| {
            panic!("no events")
        })
        .unwrap_err();
        assert!(error.to_string().contains("too long"), "{error}");
    }

    #[test]
    fn a_character_split_by_a_pause_is_not_lost() {
        let (listener, endpoint) = server();
        thread::spawn(move || {
            let (mut socket, _) = listener.accept().unwrap();
            request(&mut socket);
            let bytes = "event: notice\ndata: caf\u{e9}\n\n".as_bytes();
            let (head, tail) = bytes.split_at(bytes.len() - 3);
            socket.write_all(b"HTTP/1.0 200 OK\r\n\r\n").unwrap();
            socket.write_all(head).unwrap();
            socket.flush().unwrap();
            thread::sleep(Duration::from_millis(50));
            socket.write_all(tail).unwrap();
        });
        let mut got = Vec::new();
        let mut last = NO_HISTORY;
        let _ = read_events(&endpoint, &mut last, &|| false, &mut |e| {
            got.push(e.data.clone())
        });
        assert_eq!(got, ["caf\u{e9}"]);
    }

    #[test]
    fn only_loopback_endpoints_are_accepted() {
        assert!(Endpoint::new("127.0.0.1", 4100, TOKEN.into()).is_some());
        assert!(Endpoint::new("192.0.2.1", 4100, TOKEN.into()).is_none());
        assert!(Endpoint::new("localhost", 4100, TOKEN.into()).is_none());
    }

    #[test]
    fn the_credential_is_not_in_debug_output() {
        let (_listener, endpoint) = server();
        assert!(!format!("{endpoint:?}").contains(TOKEN));
    }
}

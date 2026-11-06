//! The tray's requests to the attached daemon's app API, over loopback HTTP.
//!
//! They authenticate like the health check: the launch capability from
//! `credential.json` as a bearer token, which the daemon accepts on `/api/`
//! routes only and for browsers never sends ambiently. The app holds that
//! capability already (the window loads it), so this adds no privilege. A
//! request that changes state also names the daemon's own origin and sends
//! JSON, which the daemon requires of every non-GET request.
//!
//! The request is HTTP/1.0 so the daemon sends the body unframed and closes
//! the connection, which keeps this client to a few lines.

use std::io::{BufRead, BufReader, Read, Write};
use std::net::{SocketAddr, TcpStream};
use std::time::Duration;

use crate::daemon::Launch;

/// Large enough for a long job journal, small enough to bound memory.
const MAX_BODY: u64 = 16 << 20;

pub const POLL_TIMEOUT: Duration = Duration::from_secs(5);
/// Diagnostics run the doctor's checks; the daemon gives them up to 45 s.
pub const DIAGNOSTICS_TIMEOUT: Duration = Duration::from_secs(50);
/// A plan usage reading may ask Claude's usage endpoint first.
pub const PLAN_USAGE_TIMEOUT: Duration = Duration::from_secs(20);

/// The daemon pings an idle event stream every 20 s, so a stream silent for
/// this long is dead and is dropped.
pub const STREAM_READ_TIMEOUT: Duration = Duration::from_secs(70);

#[derive(Debug)]
pub struct Response {
    pub status: u16,
    pub body: Vec<u8>,
}

/// One request with a deadline per read and write.
pub fn request(
    launch: &Launch,
    method: &str,
    path: &str,
    json: Option<&str>,
    timeout: Duration,
) -> Result<Response, String> {
    let (Some(host), Some(port)) = (launch.url.host_str(), launch.url.port()) else {
        return Err("the daemon's address is unusable".into());
    };
    let addr: SocketAddr = format!("{host}:{port}")
        .parse()
        .map_err(|_| "the daemon's address is unusable".to_string())?;
    let mut head = format!(
        "{method} {path} HTTP/1.0\r\nHost: {host}:{port}\r\nAuthorization: Bearer {}\r\nConnection: close\r\n",
        launch.bearer()
    );
    if let Some(body) = json {
        head.push_str(&format!(
            "Origin: http://{host}:{port}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n",
            body.len()
        ));
    }
    head.push_str("\r\n");
    let io = |e: std::io::Error| format!("cannot reach the baum daemon: {e}");
    let mut stream = TcpStream::connect_timeout(&addr, timeout).map_err(io)?;
    stream.set_read_timeout(Some(timeout)).map_err(io)?;
    stream.set_write_timeout(Some(timeout)).map_err(io)?;
    stream.write_all(head.as_bytes()).map_err(io)?;
    if let Some(body) = json {
        stream.write_all(body.as_bytes()).map_err(io)?;
    }
    let mut raw = Vec::new();
    stream.take(MAX_BODY).read_to_end(&mut raw).map_err(io)?;
    parse_response(&raw)
}

fn parse_response(raw: &[u8]) -> Result<Response, String> {
    let split = raw
        .windows(4)
        .position(|w| w == b"\r\n\r\n")
        .ok_or("the baum daemon sent an incomplete response")?;
    let head = String::from_utf8_lossy(&raw[..split]);
    let status = head
        .lines()
        .next()
        .and_then(|l| l.split(' ').nth(1))
        .and_then(|s| s.parse().ok())
        .ok_or("the baum daemon sent an unreadable response")?;
    Ok(Response {
        status,
        body: raw[split + 4..].to_vec(),
    })
}

/// What an event stream delivers to its reader.
#[derive(Debug, PartialEq, Eq)]
pub enum StreamItem {
    /// The daemon accepted the stream.
    Open,
    /// An event of this kind arrived.
    Event(String),
    /// A comment line (the daemon's ping): nothing happened, but it is alive.
    Ping,
}

/// Turns the lines of a server-sent event stream into items. Only the event
/// kind matters to the tray, so data and ids are ignored.
#[derive(Default)]
pub struct EventParser {
    kind: Option<String>,
}

impl EventParser {
    /// Feeds one line (without its line ending).
    pub fn line(&mut self, line: &str) -> Option<StreamItem> {
        if line.is_empty() {
            return self.kind.take().map(StreamItem::Event);
        }
        if line.starts_with(':') {
            return Some(StreamItem::Ping);
        }
        let (field, value) = line.split_once(':').unwrap_or((line, ""));
        if field == "event" {
            self.kind = Some(value.strip_prefix(' ').unwrap_or(value).to_string());
        }
        None
    }
}

/// Reads the daemon's event stream until it ends, errors or `on_item`
/// returns false. The request is HTTP/1.0, so the body is unframed.
pub fn stream_events(
    launch: &Launch,
    timeout: Duration,
    mut on_item: impl FnMut(StreamItem) -> bool,
) -> Result<(), String> {
    let (Some(host), Some(port)) = (launch.url.host_str(), launch.url.port()) else {
        return Err("the daemon's address is unusable".into());
    };
    let addr: SocketAddr = format!("{host}:{port}")
        .parse()
        .map_err(|_| "the daemon's address is unusable".to_string())?;
    let head = format!(
        "GET /api/events HTTP/1.0\r\nHost: {host}:{port}\r\nAuthorization: Bearer {}\r\nAccept: text/event-stream\r\n\r\n",
        launch.bearer()
    );
    let io = |e: std::io::Error| format!("cannot reach the baum daemon: {e}");
    let mut stream = TcpStream::connect_timeout(&addr, POLL_TIMEOUT).map_err(io)?;
    stream.set_read_timeout(Some(timeout)).map_err(io)?;
    stream.set_write_timeout(Some(POLL_TIMEOUT)).map_err(io)?;
    stream.write_all(head.as_bytes()).map_err(io)?;
    let mut reader = BufReader::new(stream);
    let mut line = String::new();
    reader.read_line(&mut line).map_err(io)?;
    let status: u16 = line
        .split(' ')
        .nth(1)
        .and_then(|s| s.trim().parse().ok())
        .ok_or("the baum daemon sent an unreadable response")?;
    if status != 200 {
        return Err(format!("/api/events answered {status}"));
    }
    loop {
        line.clear();
        if reader.read_line(&mut line).map_err(io)? == 0 {
            return Err("the event stream ended".into());
        }
        if line.trim_end_matches(['\r', '\n']).is_empty() {
            break;
        }
    }
    if !on_item(StreamItem::Open) {
        return Ok(());
    }
    let mut parser = EventParser::default();
    loop {
        line.clear();
        if reader.read_line(&mut line).map_err(io)? == 0 {
            return Err("the event stream ended".into());
        }
        let text = line.trim_end_matches(['\r', '\n']);
        if let Some(item) = parser.line(text)
            && !on_item(item)
        {
            return Ok(());
        }
    }
}

fn get(launch: &Launch, path: &str, timeout: Duration) -> Result<Vec<u8>, String> {
    let response = request(launch, "GET", path, None, timeout)?;
    if response.status != 200 {
        return Err(format!("{path} answered {}", response.status));
    }
    Ok(response.body)
}

pub fn jobs(launch: &Launch) -> Result<Vec<u8>, String> {
    get(launch, "/api/jobs", POLL_TIMEOUT)
}

/// How many pull requests wait on the user, which the daemon counts so the
/// shell need not download the whole index every few seconds.
pub fn waiting(launch: &Launch) -> Result<Vec<u8>, String> {
    get(launch, "/api/index?view=waiting", POLL_TIMEOUT)
}

pub fn automatic(launch: &Launch) -> Result<Vec<u8>, String> {
    get(launch, "/api/automatic", POLL_TIMEOUT)
}

/// What remains of the agents' plans, read afresh: the daemon serves a
/// reading up to a minute old and otherwise reads again.
pub fn plan_usage(launch: &Launch) -> Result<Vec<u8>, String> {
    get(launch, "/api/plan-usage?refresh=1", PLAN_USAGE_TIMEOUT)
}

/// The agent sessions in progress, for those waiting on the user.
pub fn live_sessions(launch: &Launch) -> Result<Vec<u8>, String> {
    get(launch, "/api/live-sessions", POLL_TIMEOUT)
}

pub fn diagnostics(launch: &Launch) -> Result<Vec<u8>, String> {
    get(launch, "/api/diagnostics", DIAGNOSTICS_TIMEOUT)
}

/// Sets the global automatic generation switch, which the daemon treats as the
/// master switch: off cancels queued automatic jobs and stops new ones.
pub fn set_automatic(launch: &Launch, enabled: bool) -> Result<(), String> {
    let body = crate::status::automatic_request(enabled);
    let response = request(launch, "PUT", "/api/automatic", Some(&body), POLL_TIMEOUT)?;
    if response.status != 200 {
        return Err(format!("/api/automatic answered {}", response.status));
    }
    Ok(())
}

/// Records in the daemon that the user has seen a failed job, so no client
/// flags it again.
pub fn job_seen(launch: &Launch, id: &str) -> Result<(), String> {
    if id.is_empty()
        || !id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
    {
        return Err(format!("not a job id: {id:?}"));
    }
    let path = format!("/api/jobs/{id}/seen");
    let response = request(launch, "POST", &path, Some("{}"), POLL_TIMEOUT)?;
    if response.status != 200 {
        return Err(format!("{path} answered {}", response.status));
    }
    Ok(())
}

/// Asks the daemon to queue automatic reviews of the latest pull requests
/// open when setup finished; it answers before it polls the repositories.
pub fn catch_up(launch: &Launch) -> Result<(), String> {
    let response = request(
        launch,
        "POST",
        "/api/automatic/catch-up",
        Some("{}"),
        POLL_TIMEOUT,
    )?;
    if response.status != 202 {
        return Err(format!(
            "/api/automatic/catch-up answered {}",
            response.status
        ));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::daemon::test_launch;
    use std::io::{BufRead, BufReader};
    use std::net::TcpListener;
    use std::thread;

    /// Answers one request and returns what it received.
    fn serve_once(response: &'static str) -> (u16, thread::JoinHandle<(String, String)>) {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        let handle = thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let mut head = String::new();
            let mut length = 0;
            loop {
                let mut line = String::new();
                if reader.read_line(&mut line).unwrap() == 0 || line == "\r\n" {
                    break;
                }
                if let Some(v) = line.strip_prefix("Content-Length: ") {
                    length = v.trim().parse().unwrap();
                }
                head.push_str(&line);
            }
            let mut body = vec![0; length];
            reader.read_exact(&mut body).unwrap();
            stream.write_all(response.as_bytes()).unwrap();
            (head, String::from_utf8(body).unwrap())
        });
        (port, handle)
    }

    #[test]
    fn get_sends_the_bearer_and_returns_the_body() {
        let (port, server) =
            serve_once("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"jobs\":[]}");
        let body = get(&test_launch(port), "/api/jobs", Duration::from_secs(2)).unwrap();
        assert_eq!(body, b"{\"jobs\":[]}");
        let (head, _) = server.join().unwrap();
        assert!(head.starts_with("GET /api/jobs HTTP/1.0\r\n"), "{head}");
        assert!(head.contains("Authorization: Bearer "), "{head}");
        assert!(!head.contains("Origin:"), "{head}");
    }

    #[test]
    fn the_parser_yields_event_kinds_and_pings() {
        let mut parser = EventParser::default();
        let lines = [
            ": connected",
            "",
            "id: 4",
            "event: progress",
            "data: {\"a\":1}",
            "",
            "event:live",
            "",
            "data: orphan",
            "",
        ];
        let items: Vec<_> = lines.iter().filter_map(|l| parser.line(l)).collect();
        assert_eq!(
            items,
            vec![
                StreamItem::Ping,
                StreamItem::Event("progress".into()),
                StreamItem::Event("live".into()),
            ]
        );
    }

    #[test]
    fn the_stream_reads_events_with_the_bearer() {
        let (port, server) = serve_once(
            "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n: connected\n\nid: 1\nevent: reset\ndata: {}\n\n",
        );
        let mut items = Vec::new();
        let result = stream_events(&test_launch(port), Duration::from_secs(2), |i| {
            items.push(i);
            true
        });
        assert!(result.is_err(), "the server closed the stream");
        assert_eq!(
            items,
            vec![
                StreamItem::Open,
                StreamItem::Ping,
                StreamItem::Event("reset".into())
            ]
        );
        let (head, _) = server.join().unwrap();
        assert!(head.starts_with("GET /api/events HTTP/1.0\r\n"), "{head}");
        assert!(head.contains("Authorization: Bearer "), "{head}");
    }

    #[test]
    fn a_refused_stream_is_an_error() {
        let (port, server) = serve_once("HTTP/1.1 401 Unauthorized\r\n\r\nunauthorized");
        let result = stream_events(&test_launch(port), Duration::from_secs(2), |_| true);
        assert!(result.unwrap_err().contains("401"));
        server.join().unwrap();
    }

    #[test]
    fn a_failing_status_is_an_error() {
        let (port, server) = serve_once("HTTP/1.1 401 Unauthorized\r\n\r\nunauthorized");
        assert!(get(&test_launch(port), "/api/jobs", Duration::from_secs(2)).is_err());
        server.join().unwrap();
    }

    #[test]
    fn pause_and_resume_put_the_switch_with_the_origin_the_daemon_requires() {
        for enabled in [false, true] {
            let (port, server) = serve_once("HTTP/1.1 200 OK\r\n\r\n{}");
            set_automatic(&test_launch(port), enabled).unwrap();
            let (head, body) = server.join().unwrap();
            assert!(
                head.starts_with("PUT /api/automatic HTTP/1.0\r\n"),
                "{head}"
            );
            assert!(
                head.contains(&format!("Origin: http://127.0.0.1:{port}\r\n")),
                "{head}"
            );
            assert!(
                head.contains("Content-Type: application/json\r\n"),
                "{head}"
            );
            assert_eq!(body, format!("{{\"enabled\":{enabled}}}"));
        }
    }

    #[test]
    fn a_refused_switch_change_is_an_error() {
        let (port, server) = serve_once("HTTP/1.1 403 Forbidden\r\n\r\nforbidden");
        assert!(set_automatic(&test_launch(port), false).is_err());
        server.join().unwrap();
    }

    #[test]
    fn an_unreachable_daemon_is_an_error() {
        let unused = TcpListener::bind("127.0.0.1:0")
            .unwrap()
            .local_addr()
            .unwrap()
            .port();
        assert!(
            get(
                &test_launch(unused),
                "/api/jobs",
                Duration::from_millis(200)
            )
            .is_err()
        );
    }

    #[test]
    fn rejects_a_response_without_a_status() {
        assert!(parse_response(b"garbage").is_err());
        assert!(parse_response(b"HTTP/1.1 abc\r\n\r\n").is_err());
        assert_eq!(
            parse_response(b"HTTP/1.1 204 No Content\r\n\r\n")
                .unwrap()
                .status,
            204
        );
    }
}

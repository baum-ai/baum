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

use std::io::{Read, Write};
use std::net::{SocketAddr, TcpStream};
use std::time::Duration;

use crate::daemon::Launch;

/// Large enough for a long job journal, small enough to bound memory.
const MAX_BODY: u64 = 16 << 20;

pub const POLL_TIMEOUT: Duration = Duration::from_secs(5);
/// Diagnostics run the doctor's checks; the daemon gives them up to 45 s.
pub const DIAGNOSTICS_TIMEOUT: Duration = Duration::from_secs(50);

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

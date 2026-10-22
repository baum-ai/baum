//! The app's side of the contract with `baum daemon`. The bundled `baum`
//! binary does everything that knows where the daemon lives:
//! `baum daemon ensure --json` starts the user's daemon if none is running and
//! prints its status, whose `state` directory holds the app credential and the
//! logs. This module only runs that command, reads the credential and checks
//! the daemon still answers.

use std::fs;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{SocketAddr, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::mpsc;
use std::thread;
use std::time::{Duration, Instant};

use serde::Deserialize;
use url::Url;

/// How long `baum daemon ensure` may take, including a first start that
/// resolves the login-shell environment.
pub const ENSURE_TIMEOUT: Duration = Duration::from_secs(45);

const CREDENTIAL_FILE: &str = "credential.json";
const LOG_FILES: [&str; 2] = ["daemon.log", "daemon.out"];
const MAX_CREDENTIAL_BYTES: u64 = 4096;
const TOKEN_LENGTH: usize = 43;

/// What `baum daemon ensure --json` prints: the running daemon's status.
#[derive(Debug, Clone, Deserialize)]
pub struct Status {
    #[serde(default)]
    pub version: String,
    #[serde(default)]
    pub pid: u32,
    /// The loopback origin of the app transport.
    #[serde(default)]
    pub app: Option<String>,
    /// The daemon's private state directory.
    pub state: PathBuf,
}

/// A daemon the window can load.
#[derive(Clone)]
pub struct Launch {
    pub status: Status,
    /// The launch URL, holding the credential in its fragment. Never log it.
    pub url: Url,
    pub origin: url::Origin,
    token: String,
}

/// Never prints the launch URL or token, so a stray `{:?}` cannot leak them.
impl std::fmt::Debug for Launch {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Launch")
            .field("status", &self.status)
            .field("origin", &self.origin.ascii_serialization())
            .field("credential", &"<redacted>")
            .finish()
    }
}

impl Launch {
    /// The launch capability, which the daemon also accepts as a bearer token
    /// on its `/api/` routes. Never log it.
    pub fn bearer(&self) -> &str {
        &self.token
    }
}

/// Why the app could not reach a daemon, with whatever the user needs to see.
#[derive(Debug, Clone)]
pub struct Failure {
    pub message: String,
    /// Recent daemon log or command output, newest last.
    pub details: Vec<String>,
}

impl Failure {
    fn new(message: impl Into<String>) -> Self {
        Failure {
            message: message.into(),
            details: Vec::new(),
        }
    }
}

/// The `baum` binary the app runs: `BAUM_DESKTOP_BAUM` while developing, else
/// the stable copy where the app keeps one, else the one bundled next to the
/// executable.
pub fn cli_path() -> Result<PathBuf, Failure> {
    if let Some(path) = std::env::var_os("BAUM_DESKTOP_BAUM") {
        return Ok(PathBuf::from(path));
    }
    if let Some(stable) = STABLE_CLI.get() {
        return stable().map_err(Failure::new);
    }
    bundled_cli_path()
}

/// Where the app keeps a stable copy of the bundled binary (an AppImage's
/// mount vanishes when it quits; see `onboarding::start`), a function that
/// refreshes the copy if needed and returns it. Every run of baum goes
/// through it, so a copy that could not be made is an error the connecting
/// screen shows and Retry tries again, never a fall back to the binary that
/// would vanish. Set once, at startup, before anything runs baum.
type StableCli = Box<dyn Fn() -> Result<PathBuf, String> + Send + Sync>;
static STABLE_CLI: std::sync::OnceLock<StableCli> = std::sync::OnceLock::new();

pub fn use_stable_cli(stable: impl Fn() -> Result<PathBuf, String> + Send + Sync + 'static) {
    let _ = STABLE_CLI.set(Box::new(stable));
}

/// Whether `BAUM_DESKTOP_BAUM` names the baum to run (development).
pub fn cli_overridden() -> bool {
    std::env::var_os("BAUM_DESKTOP_BAUM").is_some()
}

/// The binary bundled next to the app's executable.
pub fn bundled_cli_path() -> Result<PathBuf, Failure> {
    let exe = std::env::current_exe()
        .map_err(|e| Failure::new(format!("Cannot locate the app executable: {e}")))?;
    let name = if cfg!(windows) { "baum.exe" } else { "baum" };
    Ok(exe.with_file_name(name))
}

/// Starts the daemon if needed and returns the URL to load.
pub fn ensure(cli: &Path, timeout: Duration) -> Result<Launch, Failure> {
    let output = run(cli, &["daemon", "ensure", "--json"], timeout)?;
    let status = parse_status(&output.stdout).map_err(|message| Failure {
        message,
        details: output.stderr.clone(),
    })?;
    let mut launch = read_credential(&status.state).map_err(|message| Failure {
        message,
        details: log_tail(&status.state, 40),
    })?;
    if let Some(app) = &status.app {
        let matches = Url::parse(app).is_ok_and(|u| u.origin() == launch.origin);
        if !matches {
            return Err(Failure {
                message: "The daemon's credential belongs to a different daemon. It may have just restarted; try again.".into(),
                details: log_tail(&status.state, 40),
            });
        }
    }
    launch.status = status;
    Ok(launch)
}

pub struct Output {
    pub stdout: Vec<String>,
    pub stderr: Vec<String>,
}

/// Runs the CLI with a deadline. Output is read line by line on separate
/// threads, so a detached daemon that kept a copy of a pipe cannot hold the
/// app up once the command itself has exited.
pub fn run(cli: &Path, args: &[&str], timeout: Duration) -> Result<Output, Failure> {
    let mut command = Command::new(cli);
    command
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    // No console window flashing next to the app.
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        command.creation_flags(0x0800_0000);
    }
    let mut child = command.spawn().map_err(|e| {
        Failure::new(format!(
            "Cannot run the bundled baum command ({}): {e}",
            cli.display()
        ))
    })?;
    let stdout = lines(child.stdout.take());
    let stderr = lines(child.stderr.take());
    let deadline = Instant::now() + timeout;
    let exit = loop {
        match child.try_wait() {
            Ok(Some(exit)) => break exit,
            Ok(None) if Instant::now() < deadline => thread::sleep(Duration::from_millis(50)),
            Ok(None) => {
                let _ = child.kill();
                let _ = child.wait();
                return Err(Failure {
                    message: format!(
                        "The baum daemon did not start within {} seconds.",
                        timeout.as_secs()
                    ),
                    details: collect(&stderr),
                });
            }
            Err(e) => return Err(Failure::new(format!("Cannot wait for baum: {e}"))),
        }
    };
    let output = Output {
        stdout: collect(&stdout),
        stderr: collect(&stderr),
    };
    if !exit.success() {
        return Err(Failure {
            message: format!("baum daemon ensure failed ({exit})."),
            details: output.stderr,
        });
    }
    Ok(output)
}

fn lines<R: Read + Send + 'static>(stream: Option<R>) -> mpsc::Receiver<String> {
    let (send, receive) = mpsc::channel();
    if let Some(stream) = stream {
        thread::spawn(move || {
            for line in BufReader::new(stream).lines() {
                let Ok(line) = line else { break };
                if send.send(line).is_err() {
                    break;
                }
            }
        });
    }
    receive
}

/// Lines received until the writer closes the stream or briefly goes quiet.
fn collect(lines: &mpsc::Receiver<String>) -> Vec<String> {
    let mut out = Vec::new();
    while let Ok(line) = lines.recv_timeout(Duration::from_millis(500)) {
        out.push(line);
    }
    out
}

fn parse_status(stdout: &[String]) -> Result<Status, String> {
    let line = stdout
        .iter()
        .rev()
        .find(|l| !l.trim().is_empty())
        .ok_or("baum daemon ensure printed no status.")?;
    let status: Status = serde_json::from_str(line)
        .map_err(|e| format!("baum daemon ensure printed an unreadable status: {e}"))?;
    if !status.state.is_absolute() {
        return Err("baum daemon ensure reported a relative state directory.".into());
    }
    Ok(status)
}

#[derive(Deserialize)]
struct Credential {
    url: String,
}

/// Reads and checks the app credential. Only a private file naming a loopback
/// launch URL is accepted, the same checks the daemon applies.
pub fn read_credential(state: &Path) -> Result<Launch, String> {
    let path = state.join(CREDENTIAL_FILE);
    let info = fs::symlink_metadata(&path).map_err(|e| {
        format!(
            "The daemon's credential is missing ({}): {e}",
            path.display()
        )
    })?;
    if !info.is_file() || info.len() > MAX_CREDENTIAL_BYTES || !private(&info) {
        return Err(format!(
            "The daemon's credential ({}) must be a small file only you can read.",
            path.display()
        ));
    }
    let data = fs::read(&path).map_err(|e| format!("Cannot read the daemon's credential: {e}"))?;
    let credential: Credential = serde_json::from_slice(&data)
        .map_err(|e| format!("The daemon's credential is unreadable: {e}"))?;
    let (url, token) = launch_url(&credential.url)?;
    Ok(Launch {
        status: Status {
            version: String::new(),
            pid: 0,
            app: None,
            state: state.to_path_buf(),
        },
        origin: url.origin(),
        url,
        token,
    })
}

#[cfg(unix)]
fn private(info: &fs::Metadata) -> bool {
    use std::os::unix::fs::MetadataExt;
    // SAFETY: getuid has no preconditions and cannot fail.
    let uid = unsafe { libc::getuid() };
    info.mode() & 0o077 == 0 && info.uid() == uid
}

// The daemon restricts the file with an ACL on Windows; the app trusts the
// user profile's permissions there.
#[cfg(not(unix))]
fn private(_: &fs::Metadata) -> bool {
    true
}

fn launch_url(raw: &str) -> Result<(Url, String), String> {
    let invalid = || "The daemon's credential does not hold a loopback launch URL.".to_string();
    let url = Url::parse(raw).map_err(|_| invalid())?;
    let loopback = url.scheme() == "http"
        && url.host_str() == Some("127.0.0.1")
        && url.port().is_some()
        && url.username().is_empty()
        && url.password().is_none()
        && url.path() == "/"
        && url.query().is_none();
    if !loopback {
        return Err(invalid());
    }
    let token = url
        .fragment()
        .and_then(|f| f.strip_prefix("token="))
        .filter(|t| t.len() == TOKEN_LENGTH && t.bytes().all(token_byte))
        .ok_or_else(invalid)?
        .to_string();
    Ok((url, token))
}

fn token_byte(b: u8) -> bool {
    b.is_ascii_alphanumeric() || b == b'-' || b == b'_'
}

#[derive(Debug, PartialEq, Eq)]
pub enum Health {
    Ok,
    /// Something answers but rejects the credential: a newer daemon has
    /// replaced the one the window attached to, and rewritten the credential.
    Replaced,
    Down,
}

/// Asks the daemon's app transport for its health.
pub fn health(launch: &Launch, timeout: Duration) -> Health {
    let (Some(host), Some(port)) = (launch.url.host_str(), launch.url.port()) else {
        return Health::Down;
    };
    let Ok(addr) = format!("{host}:{port}").parse::<SocketAddr>() else {
        return Health::Down;
    };
    let check = || -> std::io::Result<Health> {
        let mut stream = TcpStream::connect_timeout(&addr, timeout)?;
        stream.set_read_timeout(Some(timeout))?;
        stream.set_write_timeout(Some(timeout))?;
        write!(
            stream,
            "GET /api/health HTTP/1.1\r\nHost: {host}:{port}\r\nAuthorization: Bearer {}\r\nConnection: close\r\n\r\n",
            launch.token
        )?;
        let mut response = Vec::new();
        stream.take(4096).read_to_end(&mut response)?;
        let response = String::from_utf8_lossy(&response);
        let status = response.lines().next().and_then(|l| l.split(' ').nth(1));
        Ok(match status {
            Some("200") if response.contains(r#""status":"ok""#) => Health::Ok,
            Some("401") => Health::Replaced,
            _ => Health::Down,
        })
    };
    check().unwrap_or(Health::Down)
}

/// The last lines of the daemon's logs, oldest first.
pub fn log_tail(state: &Path, max: usize) -> Vec<String> {
    let mut out = Vec::new();
    for name in LOG_FILES {
        let Ok(data) = fs::read(state.join(name)) else {
            continue;
        };
        let text = String::from_utf8_lossy(&data);
        let lines: Vec<&str> = text.lines().collect();
        let start = lines.len().saturating_sub(max);
        if start < lines.len() {
            out.push(format!("── {name}"));
            out.extend(lines[start..].iter().map(|l| l.to_string()));
        }
    }
    out
}

/// How many generation jobs the user's daemon is running, so an update can
/// wait for them. `baum daemon status` finds the daemon without starting one;
/// when it reports none, no job can be running and the answer is 0. Queued
/// jobs wait in the daemon's journal and resume after a restart; only a
/// running one would be interrupted.
pub fn running_jobs(cli: &Path, timeout: Duration) -> Result<usize, String> {
    let Ok(output) = run(cli, &["daemon", "status", "--json"], timeout) else {
        return Ok(0);
    };
    let status = parse_status(&output.stdout)?;
    let launch = read_credential(&status.state)?;
    let body = crate::client::jobs(&launch)?;
    Ok(crate::status::parse_jobs(&body)?.running)
}

/// A launch on a local test server, for other modules' tests.
#[cfg(test)]
pub fn test_launch(port: u16) -> Launch {
    let token = "abcdefghijklmnopqrstuvwxyz0123456789-_ABCDE";
    let (url, token) = launch_url(&format!("http://127.0.0.1:{port}/#token={token}")).unwrap();
    Launch {
        status: Status {
            version: String::new(),
            pid: 0,
            app: None,
            state: PathBuf::from("/s"),
        },
        origin: url.origin(),
        url,
        token,
    }
}

/// A stand-in for the bundled baum in tests, running `script` with sh. It is
/// written under another name and copied into place by `cp`: a file this
/// process held open for writing could still be open in a child another test
/// thread forks at that moment, and running it would then fail with ETXTBSY
/// (renaming would not help; the inode stays the same).
#[cfg(all(test, unix))]
pub fn fake_cli(dir: &Path, script: &str) -> PathBuf {
    use std::os::unix::fs::PermissionsExt;
    let source = dir.join("baum.source");
    fs::write(&source, format!("#!/bin/sh\n{script}\n")).unwrap();
    fs::set_permissions(&source, fs::Permissions::from_mode(0o755)).unwrap();
    let path = dir.join("baum");
    let copied = Command::new("cp")
        .arg("-p")
        .arg(&source)
        .arg(&path)
        .status();
    assert!(copied.unwrap().success());
    path
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::net::TcpListener;

    const TOKEN: &str = "abcdefghijklmnopqrstuvwxyz0123456789-_ABCDE";

    fn temp_dir(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("baum-desktop-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        dir
    }

    fn write_private(path: &Path, data: &str) {
        fs::write(path, data).unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(path, fs::Permissions::from_mode(0o600)).unwrap();
        }
    }

    #[test]
    fn accepts_only_loopback_launch_urls() {
        assert!(launch_url(&format!("http://127.0.0.1:4100/#token={TOKEN}")).is_ok());
        for bad in [
            format!("https://127.0.0.1:4100/#token={TOKEN}"),
            format!("http://localhost:4100/#token={TOKEN}"),
            format!("http://127.0.0.1/#token={TOKEN}"),
            format!("http://user@127.0.0.1:4100/#token={TOKEN}"),
            format!("http://127.0.0.1:4100/s/#token={TOKEN}"),
            format!("http://127.0.0.1:4100/?x=1#token={TOKEN}"),
            format!("http://127.0.0.1:4100/#token={TOKEN}&other=1"),
            "http://127.0.0.1:4100/#token=short".to_string(),
            "http://127.0.0.1:4100/".to_string(),
        ] {
            assert!(launch_url(&bad).is_err(), "{bad}");
        }
    }

    #[test]
    fn reads_a_private_credential() {
        let dir = temp_dir("credential");
        write_private(
            &dir.join(CREDENTIAL_FILE),
            &format!(r#"{{"url":"http://127.0.0.1:4100/#token={TOKEN}"}}"#),
        );
        let launch = read_credential(&dir).unwrap();
        assert_eq!(launch.url.port(), Some(4100));
        assert_eq!(launch.token, TOKEN);
    }

    #[cfg(unix)]
    #[test]
    fn rejects_a_credential_others_can_read() {
        use std::os::unix::fs::PermissionsExt;
        let dir = temp_dir("shared-credential");
        let path = dir.join(CREDENTIAL_FILE);
        write_private(
            &path,
            &format!(r#"{{"url":"http://127.0.0.1:4100/#token={TOKEN}"}}"#),
        );
        fs::set_permissions(&path, fs::Permissions::from_mode(0o644)).unwrap();
        assert!(read_credential(&dir).is_err());
    }

    #[test]
    fn status_needs_an_absolute_state_directory() {
        let ok = parse_status(&[
            r#"{"protocol":1,"version":"v1","pid":7,"app":"http://127.0.0.1:1","state":"/s"}"#
                .into(),
        ]);
        assert_eq!(ok.unwrap().state, PathBuf::from("/s"));
        assert!(parse_status(&[r#"{"state":"relative"}"#.into()]).is_err());
        assert!(parse_status(&[]).is_err());
        assert!(parse_status(&["not json".into()]).is_err());
    }

    #[test]
    fn debug_output_never_holds_the_credential() {
        let shown = format!("{:?}", launch_on(4100));
        assert!(!shown.contains(TOKEN), "{shown}");
        assert!(!shown.contains("token="), "{shown}");
    }

    #[test]
    fn log_tail_keeps_the_newest_lines() {
        let dir = temp_dir("logs");
        fs::write(dir.join("daemon.log"), "one\ntwo\nthree\n").unwrap();
        assert_eq!(log_tail(&dir, 2), vec!["── daemon.log", "two", "three"]);
        assert!(log_tail(&temp_dir("no-logs"), 2).is_empty());
    }

    fn serve_once(response: &'static str) -> (u16, thread::JoinHandle<String>) {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        let handle = thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut request = String::new();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            loop {
                let mut line = String::new();
                if reader.read_line(&mut line).unwrap() == 0 || line == "\r\n" {
                    break;
                }
                request.push_str(&line);
            }
            stream.write_all(response.as_bytes()).unwrap();
            request
        });
        (port, handle)
    }

    fn launch_on(port: u16) -> Launch {
        let (url, token) = launch_url(&format!("http://127.0.0.1:{port}/#token={TOKEN}")).unwrap();
        Launch {
            status: parse_status(&[r#"{"state":"/s"}"#.into()]).unwrap(),
            origin: url.origin(),
            url,
            token,
        }
    }

    #[test]
    fn health_check_sends_the_credential() {
        let (port, server) =
            serve_once("HTTP/1.1 200 OK\r\nContent-Length: 15\r\n\r\n{\"status\":\"ok\"}");
        assert_eq!(health(&launch_on(port), Duration::from_secs(2)), Health::Ok);
        let request = server.join().unwrap();
        assert!(request.starts_with("GET /api/health HTTP/1.1\r\n"));
        assert!(request.contains(&format!("Authorization: Bearer {TOKEN}\r\n")));
    }

    #[test]
    fn a_rejected_credential_means_the_daemon_was_replaced() {
        let (port, server) = serve_once("HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\n\r\n");
        assert_eq!(
            health(&launch_on(port), Duration::from_secs(2)),
            Health::Replaced
        );
        server.join().unwrap();
    }

    #[test]
    fn health_check_fails_on_errors_and_silence() {
        let (port, server) =
            serve_once("HTTP/1.1 500 Internal Server Error\r\nContent-Length: 0\r\n\r\n");
        assert_eq!(
            health(&launch_on(port), Duration::from_secs(2)),
            Health::Down
        );
        server.join().unwrap();
        let unused = TcpListener::bind("127.0.0.1:0")
            .unwrap()
            .local_addr()
            .unwrap()
            .port();
        assert_eq!(
            health(&launch_on(unused), Duration::from_millis(200)),
            Health::Down
        );
    }

    #[cfg(unix)]
    mod cli {
        use super::*;
        use std::sync::{Mutex, MutexGuard};

        /// Held by every test that writes or runs a fake CLI. A child another
        /// test forks holds the script's write descriptor until it execs, and
        /// running the script meanwhile fails with "Text file busy".
        static SPAWN: Mutex<()> = Mutex::new(());

        fn serial() -> MutexGuard<'static, ()> {
            SPAWN
                .lock()
                .unwrap_or_else(|poisoned| poisoned.into_inner())
        }

        #[test]
        fn ensure_loads_the_running_daemon() {
            let _serial = serial();
            let dir = temp_dir("ensure");
            write_private(
                &dir.join(CREDENTIAL_FILE),
                &format!(r#"{{"url":"http://127.0.0.1:4100/#token={TOKEN}"}}"#),
            );
            let cli = fake_cli(
                &dir,
                &format!(
                    r#"[ "$*" = "daemon ensure --json" ] || exit 9
echo '{{"protocol":1,"version":"v1","pid":42,"app":"http://127.0.0.1:4100","state":"{}"}}'"#,
                    dir.display()
                ),
            );
            let launch = ensure(&cli, Duration::from_secs(5)).unwrap();
            assert_eq!(launch.status.pid, 42);
            assert_eq!(
                launch.url.as_str(),
                format!("http://127.0.0.1:4100/#token={TOKEN}")
            );
        }

        #[test]
        fn ensure_rejects_a_credential_for_another_daemon() {
            let _serial = serial();
            let dir = temp_dir("ensure-mismatch");
            write_private(
                &dir.join(CREDENTIAL_FILE),
                &format!(r#"{{"url":"http://127.0.0.1:4100/#token={TOKEN}"}}"#),
            );
            let cli = fake_cli(
                &dir,
                &format!(
                    r#"echo '{{"app":"http://127.0.0.1:4200","state":"{}"}}'"#,
                    dir.display()
                ),
            );
            assert!(ensure(&cli, Duration::from_secs(5)).is_err());
        }

        #[test]
        fn ensure_reports_the_command_error() {
            let _serial = serial();
            let dir = temp_dir("ensure-fails");
            let cli = fake_cli(&dir, "echo 'cannot lock the state directory' >&2; exit 1");
            let failure = ensure(&cli, Duration::from_secs(5)).unwrap_err();
            assert!(failure.message.contains("failed"), "{}", failure.message);
            assert_eq!(failure.details, vec!["cannot lock the state directory"]);
        }

        #[test]
        fn ensure_gives_up_after_the_timeout() {
            let _serial = serial();
            let dir = temp_dir("ensure-hangs");
            let cli = fake_cli(&dir, "exec sleep 30");
            let started = Instant::now();
            let failure = ensure(&cli, Duration::from_millis(300)).unwrap_err();
            assert!(
                failure.message.contains("did not start"),
                "{}",
                failure.message
            );
            assert!(started.elapsed() < Duration::from_secs(5));
        }

        #[test]
        fn a_detached_child_holding_stdout_does_not_block() {
            let _serial = serial();
            let dir = temp_dir("ensure-detached");
            write_private(
                &dir.join(CREDENTIAL_FILE),
                &format!(r#"{{"url":"http://127.0.0.1:4100/#token={TOKEN}"}}"#),
            );
            let cli = fake_cli(
                &dir,
                &format!(
                    r#"sleep 30 &
echo '{{"app":"http://127.0.0.1:4100","state":"{}"}}'"#,
                    dir.display()
                ),
            );
            let started = Instant::now();
            assert!(ensure(&cli, Duration::from_secs(5)).is_ok());
            assert!(started.elapsed() < Duration::from_secs(5));
        }

        #[test]
        fn running_jobs_asks_the_daemon_status_reports() {
            let _serial = serial();
            let (port, server) = serve_once(
                "HTTP/1.1 200 OK\r\n\r\n{\"jobs\":[{\"state\":\"running\"},{\"state\":\"queued\"}]}",
            );
            let dir = temp_dir("jobs");
            write_private(
                &dir.join(CREDENTIAL_FILE),
                &format!(r#"{{"url":"http://127.0.0.1:{port}/#token={TOKEN}"}}"#),
            );
            let cli = fake_cli(
                &dir,
                &format!(
                    r#"[ "$*" = "daemon status --json" ] || exit 9
echo '{{"app":"http://127.0.0.1:{port}","state":"{}"}}'"#,
                    dir.display()
                ),
            );
            assert_eq!(running_jobs(&cli, Duration::from_secs(5)), Ok(1));
            server.join().unwrap();
        }

        #[test]
        fn no_daemon_means_no_running_jobs() {
            let _serial = serial();
            let dir = temp_dir("jobs-no-daemon");
            let cli = fake_cli(&dir, "echo 'the baum daemon is not running' >&2; exit 1");
            assert_eq!(running_jobs(&cli, Duration::from_secs(5)), Ok(0));
        }

        #[test]
        fn a_missing_cli_is_reported() {
            let _serial = serial();
            let failure =
                ensure(Path::new("/nonexistent/baum"), Duration::from_secs(1)).unwrap_err();
            assert!(
                failure.message.contains("Cannot run"),
                "{}",
                failure.message
            );
        }
    }
}

// No console window next to the app on Windows release builds.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

//! The baum desktop app: a window onto the user's `baum daemon`. The shell
//! adds no review features; the daemon serves the application. The window
//! starts on a bundled connecting screen, attaches to the daemon (starting it
//! if needed) and loads it, and returns to that screen with the daemon's
//! recent log when the daemon cannot be reached. Quitting the app leaves the
//! daemon running.

mod daemon;
mod navigation;

use std::sync::{Arc, Mutex, MutexGuard};
use std::thread;
use std::time::Duration;

use serde::Serialize;
use tauri::{AppHandle, Manager, WebviewUrl, WebviewWindowBuilder};
use tauri_plugin_opener::OpenerExt;

use navigation::Decision;

const MAIN: &str = "main";
/// How often the attached daemon is checked, and how many failed checks in a
/// row count as the daemon having gone away.
const HEALTH_INTERVAL: Duration = Duration::from_secs(5);
const HEALTH_TIMEOUT: Duration = Duration::from_secs(2);
const HEALTH_FAILURES: u32 = 3;
const LOG_LINES: usize = 40;

/// What the connecting screen shows.
#[derive(Debug, Clone, Default, Serialize)]
#[serde(tag = "phase", rename_all = "snake_case")]
enum Connection {
    #[default]
    Connecting,
    Connected,
    Failed {
        message: String,
        details: Vec<String>,
    },
}

#[derive(Default)]
struct Shared {
    connection: Connection,
    /// The origin the window may load while attached.
    daemon: Option<url::Origin>,
    /// Incremented by every connection attempt, so a stale attempt or health
    /// watch never overwrites a newer one.
    attempt: u64,
}

#[derive(Clone, Default)]
struct State(Arc<Mutex<Shared>>);

impl State {
    fn lock(&self) -> MutexGuard<'_, Shared> {
        self.0
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }
}

/// The connecting screen polls this.
#[tauri::command]
fn connection(state: tauri::State<'_, State>) -> Connection {
    state.lock().connection.clone()
}

/// The connecting screen's Retry and Restart buttons. A failed daemon is only
/// ever restarted here, never automatically, so a crashing daemon cannot
/// restart in a loop.
#[tauri::command]
fn reconnect(app: AppHandle) {
    connect(&app, Attach::First);
}

/// Why the app attaches. After a daemon replaced the attached one, the app
/// attaches once on its own; if the replacement's credential fails too, it
/// shows the error instead of trying again.
#[derive(Clone, Copy, PartialEq, Eq)]
enum Attach {
    First,
    Replacement,
}

fn connect(app: &AppHandle, why: Attach) {
    let state = app.state::<State>().inner().clone();
    let attempt = {
        let mut shared = state.lock();
        if matches!(shared.connection, Connection::Connecting) && shared.attempt > 0 {
            return;
        }
        shared.attempt += 1;
        shared.connection = Connection::Connecting;
        shared.daemon = None;
        shared.attempt
    };
    show_screen(app);
    let app = app.clone();
    thread::spawn(move || {
        let launch =
            daemon::cli_path().and_then(|cli| daemon::ensure(&cli, daemon::ENSURE_TIMEOUT));
        let launch = match launch {
            Ok(launch) => launch,
            Err(failure) => return fail(&app, &state, attempt, failure),
        };
        {
            let mut shared = state.lock();
            if shared.attempt != attempt {
                return;
            }
            shared.connection = Connection::Connected;
            shared.daemon = Some(launch.origin.clone());
        }
        eprintln!(
            "baum desktop: attached to baum daemon {} (pid {})",
            launch.status.version, launch.status.pid
        );
        let Some(window) = app.get_webview_window(MAIN) else {
            return;
        };
        if let Err(e) = window.navigate(launch.url.clone()) {
            let failure = daemon::Failure {
                message: format!("Cannot open the baum daemon: {e}"),
                details: Vec::new(),
            };
            return fail(&app, &state, attempt, failure);
        }
        watch(&app, &state, attempt, &launch, why);
    });
}

/// Checks the attached daemon until it stops answering or a newer attempt
/// replaces this one. A daemon replaced by a newer one (after an upgrade, the
/// first client to run the new `baum` restarts it) rejects the old credential,
/// or stops answering while the new daemon's credential names another port;
/// either way the app attaches to the new daemon. It never starts one here.
fn watch(app: &AppHandle, state: &State, attempt: u64, launch: &daemon::Launch, mut why: Attach) {
    let mut failures = 0;
    loop {
        thread::sleep(HEALTH_INTERVAL);
        if state.lock().attempt != attempt {
            return;
        }
        let replaced = match daemon::health(launch, HEALTH_TIMEOUT) {
            daemon::Health::Ok => {
                // A replacement that works may itself be replaced later.
                why = Attach::First;
                failures = 0;
                continue;
            }
            daemon::Health::Replaced => true,
            daemon::Health::Down => {
                failures += 1;
                if failures < HEALTH_FAILURES {
                    continue;
                }
                replaced_elsewhere(launch)
            }
        };
        if replaced && why == Attach::First {
            eprintln!("baum desktop: the baum daemon was replaced; attaching to the new one");
            return connect(app, Attach::Replacement);
        }
        let message = if replaced {
            "The new baum daemon did not accept the app's credential."
        } else {
            "The baum daemon stopped responding."
        };
        let failure = daemon::Failure {
            message: message.into(),
            details: daemon::log_tail(&launch.status.state, LOG_LINES),
        };
        return fail(app, state, attempt, failure);
    }
}

/// Whether the credential file now names a different daemon that answers.
fn replaced_elsewhere(launch: &daemon::Launch) -> bool {
    daemon::read_credential(&launch.status.state).is_ok_and(|current| {
        current.origin != launch.origin
            && daemon::health(&current, HEALTH_TIMEOUT) == daemon::Health::Ok
    })
}

fn fail(app: &AppHandle, state: &State, attempt: u64, failure: daemon::Failure) {
    {
        let mut shared = state.lock();
        if shared.attempt != attempt {
            return;
        }
        eprintln!("baum desktop: {}", failure.message);
        shared.connection = Connection::Failed {
            message: failure.message,
            details: failure.details,
        };
        shared.daemon = None;
    }
    show_screen(app);
}

/// Returns the window to the bundled screen unless it is already there.
fn show_screen(app: &AppHandle) {
    let Some(window) = app.get_webview_window(MAIN) else {
        return;
    };
    let screen = navigation::bundled_screen();
    let current = window.url().ok();
    if current.as_ref().map(url::Url::origin) != Some(screen.origin()) {
        let _ = window.navigate(screen);
    }
}

fn open_externally(app: &AppHandle, url: &url::Url) {
    if let Err(e) = app.opener().open_url(url.as_str(), None::<&str>) {
        eprintln!("baum desktop: cannot open a link in the browser: {e}");
    }
}

fn main() {
    let state = State::default();
    tauri::Builder::default()
        // Must be the first plugin: a second launch focuses this window and exits.
        .plugin(tauri_plugin_single_instance::init(|app, _, _| {
            if let Some(window) = app.get_webview_window(MAIN) {
                let _ = window.unminimize();
                let _ = window.show();
                let _ = window.set_focus();
            }
        }))
        // Used only from Rust; no page is granted its commands.
        .plugin(tauri_plugin_opener::init())
        .manage(state.clone())
        .invoke_handler(tauri::generate_handler![connection, reconnect])
        .setup(move |app| {
            let handle = app.handle().clone();
            let opener = app.handle().clone();
            WebviewWindowBuilder::new(app, MAIN, WebviewUrl::App("index.html".into()))
                .title("baum")
                .inner_size(1280.0, 860.0)
                .min_inner_size(720.0, 480.0)
                .on_navigation(move |url| {
                    let daemon = state.lock().daemon.clone();
                    match navigation::decide(url, daemon.as_ref()) {
                        Decision::Allow => true,
                        Decision::OpenInBrowser => {
                            open_externally(&handle, url);
                            false
                        }
                        Decision::Deny => false,
                    }
                })
                .on_new_window(move |url, _| {
                    if navigation::decide_new_window(&url) == Decision::OpenInBrowser {
                        open_externally(&opener, &url);
                    }
                    tauri::webview::NewWindowResponse::Deny
                })
                .build()?;
            connect(app.handle(), Attach::First);
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running the baum desktop app");
}

#[cfg(test)]
mod tests {
    /// The daemon's pages are remote to Tauri. They must get no capability,
    /// so the only one is the bundled screen's, with exactly its two commands.
    #[test]
    fn only_the_bundled_screen_has_a_capability() {
        let entries: Vec<_> =
            std::fs::read_dir(concat!(env!("CARGO_MANIFEST_DIR"), "/capabilities"))
                .unwrap()
                .map(|e| e.unwrap().path())
                .collect();
        assert_eq!(entries.len(), 1, "{entries:?}");
        let capability: serde_json::Value =
            serde_json::from_slice(&std::fs::read(&entries[0]).unwrap()).unwrap();
        assert_eq!(capability["identifier"], "connecting-screen");
        assert!(capability.get("remote").is_none());
        assert_eq!(
            capability["permissions"],
            serde_json::json!(["allow-connection", "allow-reconnect"])
        );
        let config: serde_json::Value = serde_json::from_slice(
            &std::fs::read(concat!(env!("CARGO_MANIFEST_DIR"), "/tauri.conf.json")).unwrap(),
        )
        .unwrap();
        assert_eq!(
            config["app"]["security"]["capabilities"],
            serde_json::json!(["connecting-screen"])
        );
        assert_eq!(config["app"]["withGlobalTauri"], false);
    }
}

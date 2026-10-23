// No console window next to the app on Windows release builds.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

//! The baum desktop app: a window onto the user's `baum daemon`. The shell
//! adds no review features; the daemon serves the application. The window
//! starts on a bundled connecting screen, attaches to the daemon (starting it
//! if needed) and loads it, and returns to that screen with the daemon's
//! recent log when the daemon cannot be reached. Closing the window hides it
//! into the tray (`tray`, `lifecycle`) and quitting the app leaves the daemon
//! running; `autostart` starts the app hidden at login.

mod autostart;
mod client;
mod daemon;
mod development;
mod install;
mod lifecycle;
mod navigation;
mod notifications;
mod onboarding;
mod status;
mod tray;
mod updater;

use std::sync::{Arc, Mutex, MutexGuard};
use std::thread;
use std::time::Duration;

use serde::Serialize;
use tauri::{AppHandle, Manager, WebviewUrl, WebviewWindowBuilder};
use tauri_plugin_opener::OpenerExt;

use navigation::Decision;

pub const MAIN: &str = "main";
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
    tray::connecting(app);
    show_screen(app);
    let app = app.clone();
    thread::spawn(move || {
        let launch =
            daemon::cli_path().and_then(|cli| daemon::ensure(&cli, daemon::ENSURE_TIMEOUT));
        let launch = match launch {
            Ok(launch) => launch,
            Err(failure) => return fail(&app, &state, attempt, failure),
        };
        let frontend = match development::frontend(&launch.url) {
            Ok(url) => url,
            Err(message) => {
                return fail(
                    &app,
                    &state,
                    attempt,
                    daemon::Failure {
                        message,
                        details: Vec::new(),
                    },
                );
            }
        };
        {
            let mut shared = state.lock();
            if shared.attempt != attempt {
                return;
            }
            shared.connection = Connection::Connected;
            shared.daemon = Some(frontend.origin());
        }
        eprintln!(
            "baum desktop: attached to baum daemon {} (pid {})",
            launch.status.version, launch.status.pid
        );
        tray::attached(&app, &launch);
        let Some(window) = app.get_webview_window(MAIN) else {
            return;
        };
        // The setup screen stays until the user leaves it, which attaches again.
        let Some(navigated) = onboarding::attach(&app, &window, &frontend) else {
            return watch(&app, &state, attempt, &launch, why);
        };
        if let Err(e) = navigated {
            let failure = daemon::Failure {
                message: format!("Cannot open the baum daemon: {e}"),
                details: Vec::new(),
            };
            return fail(&app, &state, attempt, failure);
        }
        let current = state.clone();
        notifications::attach(&app, &launch, move || current.lock().attempt != attempt);
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
        if replaced {
            let failure = daemon::Failure {
                message: "The new baum daemon did not accept the app's credential.".into(),
                details: daemon::log_tail(&launch.status.state, LOG_LINES),
            };
            return fail(app, state, attempt, failure);
        }
        let failure = daemon::Failure {
            message: "The baum daemon stopped responding.".into(),
            details: daemon::log_tail(&launch.status.state, LOG_LINES),
        };
        fail(app, state, attempt, failure);
        return await_replacement(app, state, attempt, launch);
    }
}

/// While the error screen shows a daemon that stopped, waits for another
/// client (a coding-agent hook that finds no daemon starts one in the
/// background, except after `baum daemon stop`) to start a new one, and
/// attaches to it. The app itself starts nothing; the
/// wait ends when the user restarts the daemon from the screen.
fn await_replacement(app: &AppHandle, state: &State, attempt: u64, launch: &daemon::Launch) {
    loop {
        thread::sleep(HEALTH_INTERVAL);
        if state.lock().attempt != attempt {
            return;
        }
        if replaced_elsewhere(launch) {
            eprintln!("baum desktop: a new baum daemon started; attaching to it");
            return connect(app, Attach::Replacement);
        }
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
    tray::detached(app);
    show_screen(app);
}

/// Returns the window to the bundled screen unless it is already there.
fn show_screen(app: &AppHandle) {
    // The initial setup navigation may still be pending when connecting starts.
    // Preserve it: attachment also waits until the user finishes setup.
    if onboarding::is_open(app) {
        return;
    }
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
    // Start at login runs the app with this flag: the app attaches to the
    // daemon (starting it) without showing the window.
    let hidden = std::env::args().any(|a| a == autostart::HIDDEN_FLAG);
    let app = tauri::Builder::default()
        // Must be the first plugin: a second launch focuses this window and exits.
        .plugin(tauri_plugin_single_instance::init(|app, _, _| {
            lifecycle::show_window(app);
        }))
        // Used only from Rust; no page is granted their commands.
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_dialog::init())
        // Updates are driven from Rust only (see updater.rs).
        .plugin(tauri_plugin_updater::Builder::new().build())
        .manage(state.clone())
        .manage(onboarding::Setup::default())
        .invoke_handler(tauri::generate_handler![
            connection,
            reconnect,
            onboarding::setup_state,
            onboarding::install_command,
            onboarding::uninstall_command,
            onboarding::install_hooks,
            onboarding::uninstall_hooks,
            onboarding::choose_repository,
            onboarding::add_repository,
            onboarding::review_settings,
            onboarding::save_review_settings,
            onboarding::github_connection,
            onboarding::finish_setup,
        ])
        .on_window_event(lifecycle::on_window_event)
        .setup(move |app| {
            let handle = app.handle().clone();
            let opener = app.handle().clone();
            // Before the tray, whose menus hold the notification toggles.
            notifications::install(app.handle())?;
            tray::setup(app.handle());
            let page = onboarding::start(app.handle());
            development::grant_setup(app.handle())?;
            let page = if page == onboarding::SETUP_PAGE {
                match development::setup_page() {
                    Some(url) => WebviewUrl::External(url),
                    None => WebviewUrl::App(page.into()),
                }
            } else {
                WebviewUrl::App(page.into())
            };
            WebviewWindowBuilder::new(app, MAIN, page)
                .title("baum")
                .inner_size(1280.0, 860.0)
                .min_inner_size(720.0, 480.0)
                .visible(!hidden)
                .on_navigation(move |url| {
                    let daemon = state.lock().daemon.clone();
                    let setup = onboarding::is_open(&handle);
                    if setup && development::is_setup_page(url) {
                        return true;
                    }
                    match navigation::decide(url, daemon.as_ref(), setup) {
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
            // A hidden start shows the window after all if no tray appears.
            tray::watch_host(app.handle(), hidden);
            connect(app.handle(), Attach::First);
            updater::start(app.handle());
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("error while building the baum desktop app");
    app.run(|app, event| {
        // Clicking the Dock icon with the window hidden reopens it.
        #[cfg(target_os = "macos")]
        if let tauri::RunEvent::Reopen { .. } = event {
            lifecycle::show_window(app);
        }
        #[cfg(not(target_os = "macos"))]
        let _ = (app, event);
    });
}

#[cfg(test)]
mod tests {
    /// The daemon's pages are remote to Tauri. They get only the folder
    /// picker, on loopback; the bundled screens get exactly their commands and
    /// no remote URL.
    #[test]
    fn only_the_bundled_screens_have_capabilities() {
        let expected = serde_json::json!({
            "connecting-screen": ["allow-connection", "allow-reconnect"],
            "daemon-pages": ["allow-choose-repository"],
            "setup-screen": [
                "allow-setup-state",
                "allow-install-command",
                "allow-uninstall-command",
                "allow-install-hooks",
                "allow-uninstall-hooks",
                "allow-choose-repository",
                "allow-add-repository",
                "allow-review-settings",
                "allow-save-review-settings",
                "allow-github-connection",
                "allow-finish-setup"
            ],
        });
        let mut found = serde_json::Map::new();
        for entry in
            std::fs::read_dir(concat!(env!("CARGO_MANIFEST_DIR"), "/capabilities")).unwrap()
        {
            let capability: serde_json::Value =
                serde_json::from_slice(&std::fs::read(entry.unwrap().path()).unwrap()).unwrap();
            if capability["identifier"] == "daemon-pages" {
                assert_eq!(
                    capability["remote"],
                    serde_json::json!({ "urls": ["http://127.0.0.1:*/*"] })
                );
                assert_eq!(capability["local"], false);
            } else {
                assert!(capability.get("remote").is_none(), "{capability}");
            }
            assert_eq!(capability["windows"], serde_json::json!(["main"]));
            found.insert(
                capability["identifier"].as_str().unwrap().to_string(),
                capability["permissions"].clone(),
            );
        }
        assert_eq!(
            found["setup-screen"],
            serde_json::json!(crate::onboarding::SETUP_PERMISSIONS)
        );
        assert_eq!(serde_json::Value::Object(found), expected);
        let config: serde_json::Value = serde_json::from_slice(
            &std::fs::read(concat!(env!("CARGO_MANIFEST_DIR"), "/tauri.conf.json")).unwrap(),
        )
        .unwrap();
        assert_eq!(
            config["app"]["security"]["capabilities"],
            serde_json::json!(["connecting-screen", "setup-screen", "daemon-pages"])
        );
        assert_eq!(config["app"]["withGlobalTauri"], false);
    }
}

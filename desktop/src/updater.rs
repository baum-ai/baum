//! Updates from GitHub Releases. The app checks the release manifest
//! (`latest.json`) shortly after it starts and then once a day. Every bundle
//! it downloads must carry a signature from the release key whose public half
//! is in `tauri.conf.json`, made for the version the manifest announces;
//! anything else is rejected before it is installed.
//!
//! The updater never interrupts: Settings shows its state (`update_state`),
//! and the sidebar counts an available update on Settings. Where the app can
//! replace itself (the macOS app, the Linux AppImage and the Windows
//! installer), it downloads the update in the background; installing it
//! (`install_update`) waits while the daemon runs generation jobs, then
//! restarts the app (`cancel_update` stops that wait). A deb install, or any
//! other copy the updater cannot replace, only says that a new version exists
//! and links to its release page. So does a version whose installation
//! failed before: it is remembered in the app's config directory and never
//! offered for installation again. `check_for_updates` checks at once. The
//! page also shows a toast once per version and app launch; closing it
//! (`dismiss_update_notice`) is remembered only in memory.

use std::ffi::OsStr;
#[cfg(test)]
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::{Condvar, Mutex, MutexGuard};
use std::thread;
use std::time::{Duration, SystemTime};

use serde::{Deserialize, Serialize};
use tauri::utils::config::BundleType;
use tauri::{AppHandle, Manager, Runtime, State};
use tauri_plugin_updater::{Update, UpdaterExt};

use crate::daemon;

/// Where the release pages live.
const RELEASES: &str = "https://github.com/baum-ai/baum/releases";
/// Set to `0` to turn update checks off.
const SETTING: &str = "BAUM_DESKTOP_UPDATES";

/// Lets the app attach to the daemon before the first check.
const FIRST_CHECK_DELAY: Duration = Duration::from_secs(30);
const CHECK_INTERVAL: Duration = Duration::from_secs(24 * 60 * 60);
/// How often the app looks at the clock between checks. Wall-clock time is
/// compared, so a computer that slept through the day checks when it wakes.
const CLOCK_TICK: Duration = Duration::from_secs(10 * 60);
const CONNECT_TIMEOUT: Duration = Duration::from_secs(30);
/// A request that receives nothing for this long is abandoned. A slow
/// download that keeps receiving data is not cut off.
const STALL_TIMEOUT: Duration = Duration::from_secs(2 * 60);
/// How often an install that waits for running jobs asks again.
const JOBS_POLL: Duration = Duration::from_secs(15);
const STATUS_TIMEOUT: Duration = Duration::from_secs(10);
/// How long `baum daemon stop` may take before a Windows update.
#[cfg(windows)]
const STOP_TIMEOUT: Duration = Duration::from_secs(30);
/// In the app's config directory: versions whose installation failed.
const SECTION: &str = "updates";

/// What the app does with an update.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Mode {
    /// Download it, then install it when asked, once no job runs.
    Install,
    /// Say it exists and link to the release page.
    Announce,
}

fn mode(bundle: Option<BundleType>) -> Mode {
    match bundle {
        Some(BundleType::App | BundleType::AppImage | BundleType::Nsis | BundleType::Msi) => {
            Mode::Install
        }
        _ => Mode::Announce,
    }
}

/// Builds without a release version (local and snapshot builds, which are
/// 0.0.0) and debug builds never check, and neither does an app started with
/// `BAUM_DESKTOP_UPDATES=0`.
fn enabled(version: &str, debug: bool, setting: Option<&OsStr>) -> bool {
    let off = setting
        .and_then(OsStr::to_str)
        .is_some_and(|v| matches!(v.trim(), "0" | "false" | "off" | "no"));
    !debug && version != "0.0.0" && !off
}

fn due(last: SystemTime, now: SystemTime) -> bool {
    now.duration_since(last)
        .is_ok_and(|elapsed| elapsed >= CHECK_INTERVAL)
}

fn release_page(version: &str) -> String {
    format!("{RELEASES}/tag/v{}", version.trim_start_matches('v'))
}

fn unix_seconds(at: SystemTime) -> u64 {
    at.duration_since(SystemTime::UNIX_EPOCH)
        .map_or(0, |d| d.as_secs())
}

/// What the updater remembers between app starts.
#[derive(Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
struct Remembered {
    #[serde(default)]
    failed_installs: Vec<String>,
}

fn load(path: &Path) -> Remembered {
    crate::settings::load(path, SECTION)
}

fn install_failed_before(path: &Path, version: &str) -> bool {
    load(path).failed_installs.iter().any(|v| v == version)
}

fn remember_failed_install(path: &Path, version: &str) -> std::io::Result<()> {
    crate::settings::update(path, SECTION, |remembered: &mut Remembered| {
        if !remembered.failed_installs.iter().any(|v| v == version) {
            remembered.failed_installs.push(version.to_string());
        }
    })
}

fn state_file<R: Runtime>(app: &AppHandle<R>) -> Option<PathBuf> {
    app.path()
        .app_config_dir()
        .ok()
        .map(|dir| dir.join(crate::settings::FILE))
}

fn running_jobs() -> usize {
    let Ok(cli) = daemon::cli_path() else {
        return 0;
    };
    // The update itself never stops the daemon, so a daemon that cannot be
    // asked does not hold it up.
    daemon::running_jobs(&cli, STATUS_TIMEOUT).unwrap_or_else(|e| {
        eprintln!("baum desktop: {e}");
        0
    })
}

/// What the updater is doing.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Phase {
    /// Nothing; `available` says what the last check found.
    Idle,
    Checking,
    Downloading,
    /// Downloaded and verified, waiting to be installed.
    Ready,
    /// Asked to install; waits while generation jobs run.
    Waiting,
    Installing,
}

/// The updater as Settings and the sidebar show it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct View {
    /// This app's version.
    version: String,
    /// Whether this build checks for updates at all.
    enabled: bool,
    /// Whether this copy can install `available` itself; otherwise Settings
    /// links to its release page.
    installs: bool,
    phase: Phase,
    /// The newer version the last check found.
    available: Option<String>,
    /// Its release page.
    page: Option<String>,
    /// When the last check succeeded, in seconds since the Unix epoch.
    checked_at: Option<u64>,
    /// The running jobs an install waits for.
    jobs: usize,
    /// Why the last check, download or install failed.
    error: Option<String>,
    /// The version whose notice the person closed since the app started;
    /// the page tells them about an update once per version and launch.
    noticed: Option<String>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Request {
    Check,
    Install,
}

struct Inner {
    view: View,
    request: Option<Request>,
    cancel: bool,
    /// The downloaded, verified update.
    ready: Option<(Update, Vec<u8>)>,
}

/// The updater's state, shared by its thread and the commands.
pub struct Updates {
    inner: Mutex<Inner>,
    wake: Condvar,
}

impl Updates {
    fn new(version: String, enabled: bool, installs: bool) -> Updates {
        Updates {
            inner: Mutex::new(Inner {
                view: View {
                    version,
                    enabled,
                    installs,
                    phase: Phase::Idle,
                    available: None,
                    page: None,
                    checked_at: None,
                    jobs: 0,
                    error: None,
                    noticed: None,
                },
                request: None,
                cancel: false,
                ready: None,
            }),
            wake: Condvar::new(),
        }
    }

    fn lock(&self) -> MutexGuard<'_, Inner> {
        self.inner.lock().unwrap_or_else(|p| p.into_inner())
    }

    fn view(&self) -> View {
        self.lock().view.clone()
    }

    fn edit<T>(&self, change: impl FnOnce(&mut Inner) -> T) -> T {
        change(&mut self.lock())
    }

    /// Asks the updater thread to check now. A check, download or install
    /// already under way answers for it.
    fn request_check(&self) -> Result<View, String> {
        let mut inner = self.lock();
        if !inner.view.enabled {
            return Err("This build does not check for updates.".into());
        }
        if matches!(inner.view.phase, Phase::Idle | Phase::Ready) {
            inner.request = Some(Request::Check);
            if inner.view.phase == Phase::Idle {
                inner.view.phase = Phase::Checking;
            }
            inner.view.error = None;
            self.wake.notify_all();
        }
        Ok(inner.view.clone())
    }

    /// Asks the updater thread to install the downloaded update.
    fn request_install(&self) -> Result<View, String> {
        let mut inner = self.lock();
        if inner.view.phase != Phase::Ready || !inner.view.installs {
            return Err("No update is ready to install.".into());
        }
        inner.request = Some(Request::Install);
        inner.cancel = false;
        inner.view.phase = Phase::Waiting;
        inner.view.jobs = 0;
        inner.view.error = None;
        self.wake.notify_all();
        Ok(inner.view.clone())
    }

    /// Stops an install that waits for running jobs.
    fn request_cancel(&self) -> View {
        let mut inner = self.lock();
        if inner.view.phase == Phase::Waiting {
            inner.cancel = true;
            self.wake.notify_all();
        }
        inner.view.clone()
    }

    /// Remembers, until the app quits, that the person saw `version`'s notice.
    fn dismiss_notice(&self, version: String) -> View {
        let mut inner = self.lock();
        inner.view.noticed = Some(version);
        inner.view.clone()
    }

    /// Waits up to `timeout` for a request and takes it.
    fn next_request(&self, timeout: Duration) -> Option<Request> {
        let mut inner = self.lock();
        if inner.request.is_none() {
            inner = self
                .wake
                .wait_timeout(inner, timeout)
                .unwrap_or_else(|p| p.into_inner())
                .0;
        }
        inner.request.take()
    }

    /// Waits up to `timeout`; true once the wait for jobs was cancelled.
    fn cancelled(&self, timeout: Duration) -> bool {
        let mut inner = self.lock();
        if !inner.cancel {
            inner = self
                .wake
                .wait_timeout(inner, timeout)
                .unwrap_or_else(|p| p.into_inner())
                .0;
        }
        std::mem::take(&mut inner.cancel)
    }

    /// Moves a waiting install to installing, unless it was cancelled; under
    /// the lock `request_cancel` takes, so an accepted cancel always wins.
    fn begin_install(&self) -> bool {
        let mut inner = self.lock();
        if std::mem::take(&mut inner.cancel) {
            return false;
        }
        inner.view.phase = Phase::Installing;
        inner.view.jobs = 0;
        true
    }

    fn fail(&self, phase: Phase, error: String) {
        eprintln!("baum desktop: {error}");
        self.edit(|inner| {
            inner.view.phase = phase;
            inner.view.jobs = 0;
            inner.view.error = Some(error);
        });
    }
}

/// Keeps the updater's state and starts its checks on their own thread.
pub fn start<R: Runtime>(app: &AppHandle<R>) {
    let version = app.package_info().version.to_string();
    let setting = std::env::var_os(SETTING);
    let enabled = enabled(&version, cfg!(debug_assertions), setting.as_deref());
    let installs = mode(tauri::utils::platform::bundle_type()) == Mode::Install;
    app.manage(Updates::new(version, enabled, installs));
    if !enabled {
        return;
    }
    let app = app.clone();
    let spawned = thread::Builder::new()
        .name("updater".into())
        .spawn(move || run(&app));
    if let Err(e) = spawned {
        eprintln!("baum desktop: cannot start update checks: {e}");
    }
}

fn run<R: Runtime>(app: &AppHandle<R>) {
    let updates = app.state::<Updates>();
    // A check asked for from Settings starts the first one early.
    let mut request = updates.next_request(FIRST_CHECK_DELAY);
    loop {
        if request == Some(Request::Install) {
            install(app, &updates);
        } else {
            check(app, &updates);
        }
        let last = SystemTime::now();
        request = None;
        while request.is_none() && !due(last, SystemTime::now()) {
            request = updates.next_request(CLOCK_TICK);
        }
    }
}

fn check<R: Runtime>(app: &AppHandle<R>, updates: &Updates) {
    updates.edit(|inner| {
        if inner.view.phase == Phase::Idle {
            inner.view.phase = Phase::Checking;
        }
    });
    let found = app
        .updater_builder()
        .configure_client(|client| {
            client
                .connect_timeout(CONNECT_TIMEOUT)
                .read_timeout(STALL_TIMEOUT)
        })
        .build()
        .and_then(|updater| tauri::async_runtime::block_on(updater.check()));
    let update = match found {
        Ok(update) => update,
        Err(e) => {
            let phase = updates.view().phase;
            let phase = if phase == Phase::Ready {
                Phase::Ready
            } else {
                Phase::Idle
            };
            updates.fail(phase, format!("cannot check for updates: {e}"));
            return;
        }
    };
    let now = unix_seconds(SystemTime::now());
    let Some(update) = update else {
        updates.edit(|inner| {
            inner.ready = None;
            inner.view.phase = Phase::Idle;
            inner.view.available = None;
            inner.view.page = None;
            inner.view.checked_at = Some(now);
            inner.view.error = None;
        });
        return;
    };
    let failed_before = state_file(app)
        .as_deref()
        .is_some_and(|path| install_failed_before(path, &update.version));
    let installs = !failed_before && mode(tauri::utils::platform::bundle_type()) == Mode::Install;
    let already = updates.edit(|inner| {
        inner.view.available = Some(update.version.clone());
        inner.view.page = Some(release_page(&update.version));
        inner.view.checked_at = Some(now);
        inner.view.installs = installs;
        inner.view.error = failed_before.then(|| {
            format!(
                "An earlier attempt to install baum {} failed; download it from its release page.",
                update.version
            )
        });
        let already = inner
            .ready
            .as_ref()
            .is_some_and(|(ready, _)| ready.version == update.version);
        inner.view.phase = if !installs {
            Phase::Idle
        } else if already {
            Phase::Ready
        } else {
            Phase::Downloading
        };
        already
    });
    if !installs || already {
        return;
    }
    // Verifies the signature, and the version it was made for, before
    // returning anything.
    match tauri::async_runtime::block_on(update.download(|_, _| {}, || {})) {
        Ok(bytes) => updates.edit(|inner| {
            inner.ready = Some((update, bytes));
            inner.view.phase = Phase::Ready;
        }),
        Err(e) => updates.fail(
            Phase::Idle,
            format!("cannot download baum {}: {e}", update.version),
        ),
    }
}

/// Installs the downloaded update once no generation job runs, then
/// restarts the app.
fn install<R: Runtime>(app: &AppHandle<R>, updates: &Updates) {
    // A check that ran since the request may have shown the update as ready
    // again; from here the install is waiting, and can be cancelled.
    let Some((update, bytes)) = updates.edit(|inner| {
        let ready = inner.ready.take()?;
        inner.view.phase = Phase::Waiting;
        Some(ready)
    }) else {
        updates.fail(Phase::Idle, "no update is ready to install".into());
        return;
    };
    loop {
        let jobs = running_jobs();
        let cancelled = if jobs == 0 {
            !updates.begin_install()
        } else {
            updates.edit(|inner| inner.view.jobs = jobs);
            updates.cancelled(JOBS_POLL)
        };
        if cancelled {
            updates.edit(|inner| {
                inner.ready = Some((update, bytes));
                inner.view.phase = Phase::Ready;
                inner.view.jobs = 0;
            });
            return;
        }
        if jobs == 0 {
            break;
        }
    }
    // Windows: the installer closes the app and the updated app starts
    // the daemon, so stop it now rather than leave the old version
    // running from the app's copy of baum.exe (or, for installs from
    // before that copy, from the installed one, which the installer could
    // then not replace). Stopping marks the daemon as stopped by the
    // user, so hooks leave it alone; the updated app's `baum daemon
    // ensure` starts it and clears that mark. The installer's pre-install
    // hook stops it too (windows/installer-hooks.nsh). Elsewhere nothing
    // is stopped: the AppImage's copy of baum is refreshed by renaming a
    // new file into place, so the running daemon keeps its binary until
    // the updated app's `ensure` replaces it, as after any upgrade.
    #[cfg(windows)]
    stop_daemon();
    if let Err(e) = update.install(&bytes) {
        #[cfg(windows)]
        restart_daemon();
        if let Some(path) = state_file(app)
            && let Err(e) = remember_failed_install(&path, &update.version)
        {
            eprintln!("baum desktop: cannot record the failed update: {e}");
        }
        updates.edit(|inner| inner.view.installs = false);
        updates.fail(
            Phase::Idle,
            format!(
                "This copy of baum could not update itself to {} ({e}); download it from its release page.",
                update.version
            ),
        );
        return;
    }
    // Windows does not get here: the installer has already closed the
    // app, and starts it again when it finishes.
    app.restart()
}

#[tauri::command]
pub fn update_state(updates: State<'_, Updates>) -> View {
    updates.view()
}

#[tauri::command]
pub fn check_for_updates(updates: State<'_, Updates>) -> Result<View, String> {
    updates.request_check()
}

#[tauri::command]
pub fn install_update(updates: State<'_, Updates>) -> Result<View, String> {
    updates.request_install()
}

#[tauri::command]
pub fn cancel_update(updates: State<'_, Updates>) -> View {
    updates.request_cancel()
}

#[tauri::command]
pub fn dismiss_update_notice(updates: State<'_, Updates>, version: String) -> View {
    updates.dismiss_notice(version)
}

#[cfg(windows)]
fn stop_daemon() {
    let stopped =
        daemon::cli_path().and_then(|cli| daemon::run(&cli, &["daemon", "stop"], STOP_TIMEOUT));
    if let Err(e) = stopped {
        eprintln!(
            "baum desktop: cannot stop the daemon before updating: {}",
            e.message
        );
    }
}

#[cfg(windows)]
fn restart_daemon() {
    let started = daemon::cli_path()
        .and_then(|cli| daemon::run(&cli, &["daemon", "ensure"], daemon::ENSURE_TIMEOUT));
    if let Err(e) = started {
        eprintln!("baum desktop: cannot start the daemon again: {}", e.message);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use base64::Engine;

    #[test]
    fn remembers_failed_installs() {
        let dir = std::env::temp_dir().join(format!("baum-desktop-updates-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        let path = dir.join("config").join(crate::settings::FILE);
        assert!(!install_failed_before(&path, "1.2.3"));
        remember_failed_install(&path, "1.2.3").unwrap();
        remember_failed_install(&path, "1.2.3").unwrap();
        assert!(install_failed_before(&path, "1.2.3"));
        assert!(!install_failed_before(&path, "1.2.4"));
        assert_eq!(load(&path).failed_installs, vec!["1.2.3"]);
        // A damaged file does not block checks, but must not be overwritten.
        fs::write(&path, "not json").unwrap();
        assert!(!install_failed_before(&path, "1.2.3"));
        assert!(remember_failed_install(&path, "1.2.4").is_err());
        assert_eq!(fs::read_to_string(&path).unwrap(), "not json");
    }

    #[test]
    fn concurrent_failed_updates_are_all_remembered() {
        let dir = std::env::temp_dir().join(format!(
            "baum-desktop-updates-concurrent-{}",
            std::process::id()
        ));
        let _ = fs::remove_dir_all(&dir);
        let path = dir.join(crate::settings::FILE);
        std::thread::scope(|scope| {
            for version in ["1.2.3", "1.2.4", "1.2.3"] {
                let path = &path;
                scope.spawn(move || remember_failed_install(path, version).unwrap());
            }
        });
        let mut failed = load(&path).failed_installs;
        failed.sort();
        assert_eq!(failed, vec!["1.2.3", "1.2.4"]);
        let _ = fs::remove_dir_all(dir);
    }

    fn config() -> serde_json::Value {
        serde_json::from_slice(
            &std::fs::read(concat!(env!("CARGO_MANIFEST_DIR"), "/tauri.conf.json")).unwrap(),
        )
        .unwrap()
    }

    #[test]
    fn only_release_builds_check() {
        assert!(enabled("1.2.3", false, None));
        assert!(enabled("1.2.3", false, Some(OsStr::new("1"))));
        assert!(!enabled("0.0.0", false, None));
        assert!(!enabled("1.2.3", true, None));
        for off in ["0", "false", "off", "no", " 0 "] {
            assert!(!enabled("1.2.3", false, Some(OsStr::new(off))), "{off}");
        }
    }

    #[test]
    fn only_self_replacing_bundles_install() {
        assert_eq!(mode(Some(BundleType::App)), Mode::Install);
        assert_eq!(mode(Some(BundleType::AppImage)), Mode::Install);
        assert_eq!(mode(Some(BundleType::Nsis)), Mode::Install);
        assert_eq!(mode(Some(BundleType::Deb)), Mode::Announce);
        assert_eq!(mode(Some(BundleType::Rpm)), Mode::Announce);
        assert_eq!(mode(None), Mode::Announce);
    }

    #[test]
    fn checks_once_a_day_by_the_wall_clock() {
        let last = SystemTime::UNIX_EPOCH + Duration::from_secs(1_000_000);
        assert!(!due(last, last + Duration::from_secs(60)));
        assert!(!due(last, last - Duration::from_secs(60)));
        assert!(due(last, last + CHECK_INTERVAL));
    }

    #[test]
    fn links_to_the_release_page() {
        assert_eq!(
            release_page("1.2.3"),
            "https://github.com/baum-ai/baum/releases/tag/v1.2.3"
        );
    }

    #[test]
    fn a_check_is_asked_for_once_and_only_when_updates_are_on() {
        let off = Updates::new("0.0.0".into(), false, true);
        assert!(off.request_check().is_err());
        assert_eq!(off.next_request(Duration::ZERO), None);

        let updates = Updates::new("1.2.3".into(), true, true);
        let view = updates.request_check().unwrap();
        assert_eq!(view.phase, Phase::Checking);
        assert_eq!(updates.next_request(Duration::ZERO), Some(Request::Check));
        // A check under way answers a second click.
        updates.request_check().unwrap();
        assert_eq!(updates.next_request(Duration::ZERO), None);
    }

    #[test]
    fn only_a_ready_update_is_installed_and_its_wait_can_be_cancelled() {
        let updates = Updates::new("1.2.3".into(), true, true);
        assert!(updates.request_install().is_err());
        assert!(!updates.cancelled(Duration::ZERO));

        updates.edit(|inner| inner.view.phase = Phase::Ready);
        let view = updates.request_install().unwrap();
        assert_eq!(view.phase, Phase::Waiting);
        assert_eq!(updates.next_request(Duration::ZERO), Some(Request::Install));
        updates.request_cancel();
        assert!(updates.cancelled(Duration::ZERO));
        assert!(!updates.cancelled(Duration::ZERO));
    }

    #[test]
    fn a_cancel_accepted_while_jobs_are_counted_stops_the_install() {
        let updates = Updates::new("1.2.3".into(), true, true);
        updates.edit(|inner| inner.view.phase = Phase::Ready);
        updates.request_install().unwrap();
        // The last job finishes while the click is on its way.
        updates.request_cancel();
        assert!(!updates.begin_install());

        updates.edit(|inner| inner.view.phase = Phase::Ready);
        updates.request_install().unwrap();
        assert!(updates.begin_install());
        assert_eq!(updates.view().phase, Phase::Installing);
        // Too late to cancel once installing.
        updates.request_cancel();
        assert!(!updates.cancelled(Duration::ZERO));
    }

    #[test]
    fn a_copy_that_cannot_replace_itself_never_installs() {
        let updates = Updates::new("1.2.3".into(), true, false);
        updates.edit(|inner| inner.view.phase = Phase::Ready);
        assert!(updates.request_install().is_err());
    }

    #[test]
    fn a_request_wakes_the_updater() {
        let updates = std::sync::Arc::new(Updates::new("1.2.3".into(), true, true));
        let waiter = {
            let updates = updates.clone();
            thread::spawn(move || updates.next_request(Duration::from_secs(30)))
        };
        thread::sleep(Duration::from_millis(50));
        updates.request_check().unwrap();
        assert_eq!(waiter.join().unwrap(), Some(Request::Check));
    }

    #[test]
    fn the_view_is_what_settings_reads() {
        let view = Updates::new("1.2.3".into(), true, true).view();
        assert_eq!(
            serde_json::to_value(view).unwrap(),
            serde_json::json!({
                "version": "1.2.3",
                "enabled": true,
                "installs": true,
                "phase": "idle",
                "available": null,
                "page": null,
                "checked_at": null,
                "jobs": 0,
                "error": null,
                "noticed": null,
            })
        );
    }

    #[test]
    fn a_closed_notice_is_remembered_until_the_app_quits() {
        let updates = Updates::new("1.2.3".into(), true, true);
        assert_eq!(
            updates.dismiss_notice("1.2.4".into()).noticed.as_deref(),
            Some("1.2.4")
        );
        assert_eq!(updates.view().noticed.as_deref(), Some("1.2.4"));
        // A new app starts with nothing noticed.
        assert_eq!(
            Updates::new("1.2.3".into(), true, true).view().noticed,
            None
        );
    }

    /// The release workflow signs bundles with the key whose public half is
    /// configured here, and the manifest lives next to the release assets.
    #[test]
    fn updater_trusts_only_the_release_key() {
        let config = config();
        let updater = &config["plugins"]["updater"];
        let key = base64::engine::general_purpose::STANDARD
            .decode(updater["pubkey"].as_str().unwrap())
            .unwrap();
        let key = String::from_utf8(key).unwrap();
        assert!(key.starts_with("untrusted comment: minisign public key: C42ACF774FCC1D5F\n"));
        minisign_verify::PublicKey::decode(&key).unwrap();
        assert_eq!(
            updater["endpoints"],
            serde_json::json!([
                "https://github.com/baum-ai/baum/releases/latest/download/latest.json"
            ])
        );
        // A manifest pairing a new version with an older, validly signed
        // bundle is rejected.
        assert_eq!(updater["requireSignedVersion"], true);
        assert!(updater.get("dangerousInsecureTransportProtocol").is_none());
        assert!(updater.get("allowDowngrades").is_none());
        assert_eq!(updater["windows"]["installMode"], "passive");
    }

    /// Signed updater bundles are made only by the release build, which has
    /// the private key; every other build leaves them out.
    #[test]
    fn updater_bundles_are_off_by_default() {
        assert_eq!(config()["bundle"]["createUpdaterArtifacts"], false);
    }

    /// The configured key rejects a signature made with another key.
    #[test]
    fn another_key_is_rejected() {
        let config = config();
        let key = base64::engine::general_purpose::STANDARD
            .decode(config["plugins"]["updater"]["pubkey"].as_str().unwrap())
            .unwrap();
        let key = minisign_verify::PublicKey::decode(&String::from_utf8(key).unwrap()).unwrap();
        // Made with a throwaway key (minisign -G -W) over "baum", with the
        // trusted comment the Tauri CLI writes.
        let signature = minisign_verify::Signature::decode(OTHER_KEY_SIGNATURE).unwrap();
        assert!(key.verify(b"baum", &signature, true).is_err());
        let other = minisign_verify::PublicKey::from_base64(OTHER_PUBLIC_KEY).unwrap();
        other.verify(b"baum", &signature, true).unwrap();
        assert!(other.verify(b"baum!", &signature, true).is_err());
    }

    const OTHER_PUBLIC_KEY: &str = "RWTRmLxRd4UKBlQP1/n6WFgBJhCIKQrMRRY1gZi/l8EeH9Gd/co+PB2B";
    const OTHER_KEY_SIGNATURE: &str = "untrusted comment: signature from minisign secret key
RUTRmLxRd4UKBnkkf1WYNkfDOLAH8Hil/0iR7CW34Keef2FTYexIM7i6PlNjbcaGQUCR5YcZgAm35/N4KhCffSZru1QQv33Gsg0=
trusted comment: timestamp:1700000000\tfile:m\tversion:1.2.3
vxRKTnoBag5ASJP3FT8bzyzjfEb0wpQE3kaTJ3+e4PEZVtcx3e1WSct0M18bgZDD1yMTQl9oyee+cW1+L8FNDw==
";
}

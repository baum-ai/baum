//! Updates from GitHub Releases. The app checks the release manifest
//! (`latest.json`) shortly after it starts and then once a day. Every bundle
//! it downloads must carry a signature from the release key whose public half
//! is in `tauri.conf.json`, made for the version the manifest announces;
//! anything else is rejected before it is installed.
//!
//! Where the app can replace itself (the macOS app, the Linux AppImage and
//! the Windows installer), it downloads the update in the background, waits
//! while the daemon runs generation jobs, and asks before it restarts. A deb
//! install, or any other copy the updater cannot replace, only says that a
//! new version exists and links to its release page. So does a version whose
//! installation failed before: it is remembered in the app's config
//! directory and never offered for installation again.

use std::ffi::OsStr;
use std::fs;
use std::path::{Path, PathBuf};
use std::thread;
use std::time::{Duration, SystemTime};

use serde::{Deserialize, Serialize};
use tauri::utils::config::BundleType;
use tauri::{AppHandle, Manager, Runtime};
use tauri_plugin_dialog::{DialogExt, MessageDialogButtons, MessageDialogKind};
use tauri_plugin_opener::OpenerExt;
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
const JOBS_POLL: Duration = Duration::from_secs(60);
const STATUS_TIMEOUT: Duration = Duration::from_secs(10);
/// How long `baum daemon stop` may take before a Windows update.
#[cfg(windows)]
const STOP_TIMEOUT: Duration = Duration::from_secs(30);
/// In the app's config directory: versions whose installation failed.
const STATE_FILE: &str = "updates.json";

/// What the app does with an update.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Mode {
    /// Download it, wait for running jobs, ask, install and restart.
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

/// Returns once `running` reports no running job, sleeping between asks.
fn wait_until_idle(mut running: impl FnMut() -> usize, mut sleep: impl FnMut(Duration)) {
    let mut waiting = 0;
    loop {
        let jobs = running();
        if jobs == 0 {
            return;
        }
        if jobs != waiting {
            eprintln!("baum desktop: the update waits for {jobs} running generation job(s)");
            waiting = jobs;
        }
        sleep(JOBS_POLL);
    }
}

/// What the updater remembers between app starts.
#[derive(Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
struct Remembered {
    #[serde(default)]
    failed_installs: Vec<String>,
}

fn load(path: &Path) -> Remembered {
    fs::read(path)
        .ok()
        .and_then(|data| serde_json::from_slice(&data).ok())
        .unwrap_or_default()
}

fn install_failed_before(path: &Path, version: &str) -> bool {
    load(path).failed_installs.iter().any(|v| v == version)
}

fn remember_failed_install(path: &Path, version: &str) -> std::io::Result<()> {
    let mut remembered = load(path);
    if !remembered.failed_installs.iter().any(|v| v == version) {
        remembered.failed_installs.push(version.to_string());
    }
    if let Some(dir) = path.parent() {
        fs::create_dir_all(dir)?;
    }
    let partial = path.with_extension("json.partial");
    fs::write(&partial, serde_json::to_vec_pretty(&remembered)?)?;
    fs::rename(partial, path)
}

fn state_file<R: Runtime>(app: &AppHandle<R>) -> Option<PathBuf> {
    app.path()
        .app_config_dir()
        .ok()
        .map(|dir| dir.join(STATE_FILE))
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

/// Starts the update checks on their own thread.
pub fn start<R: Runtime>(app: &AppHandle<R>) {
    let version = app.package_info().version.to_string();
    let setting = std::env::var_os(SETTING);
    if !enabled(&version, cfg!(debug_assertions), setting.as_deref()) {
        return;
    }
    let app = app.clone();
    let spawned = thread::Builder::new()
        .name("updater".into())
        .spawn(move || {
            thread::sleep(FIRST_CHECK_DELAY);
            let mut session = Session::default();
            loop {
                let last = SystemTime::now();
                if let Err(e) = session.check(&app) {
                    eprintln!("baum desktop: update check failed: {e}");
                }
                while !due(last, SystemTime::now()) {
                    thread::sleep(CLOCK_TICK);
                }
            }
        });
    if let Err(e) = spawned {
        eprintln!("baum desktop: cannot start update checks: {e}");
    }
}

#[derive(Default)]
struct Session {
    /// The version the user was last told about, so the daily check does not
    /// repeat itself.
    announced: Option<String>,
    /// A downloaded, verified update the user chose to install later.
    ready: Option<(Update, Vec<u8>)>,
}

impl Session {
    fn check<R: Runtime>(&mut self, app: &AppHandle<R>) -> Result<(), String> {
        let updater = app
            .updater_builder()
            .configure_client(|client| {
                client
                    .connect_timeout(CONNECT_TIMEOUT)
                    .read_timeout(STALL_TIMEOUT)
            })
            .build()
            .map_err(|e| e.to_string())?;
        let Some(update) =
            tauri::async_runtime::block_on(updater.check()).map_err(|e| e.to_string())?
        else {
            self.ready = None;
            return Ok(());
        };
        let state = state_file(app);
        let failed_before = state
            .as_deref()
            .is_some_and(|path| install_failed_before(path, &update.version));
        match mode(tauri::utils::platform::bundle_type()) {
            Mode::Install if !failed_before => self.install(app, update),
            _ => {
                let reason = failed_before.then_some("An earlier attempt to install it failed.");
                self.announce(app, &update, reason);
                Ok(())
            }
        }
    }

    fn install<R: Runtime>(&mut self, app: &AppHandle<R>, update: Update) -> Result<(), String> {
        let bytes = match self.ready.take() {
            Some((ready, bytes)) if ready.version == update.version => bytes,
            // Verifies the signature, and the version it was made for,
            // before returning anything.
            _ => tauri::async_runtime::block_on(update.download(|_, _| {}, || {}))
                .map_err(|e| format!("cannot download baum {}: {e}", update.version))?,
        };
        wait_until_idle(running_jobs, thread::sleep);
        let restart = app
            .dialog()
            .message(format!(
                "baum {} is ready to install. The app restarts to finish; no generation job is running.",
                update.version
            ))
            .title("Update ready")
            .kind(MessageDialogKind::Info)
            .buttons(MessageDialogButtons::OkCancelCustom(
                "Restart now".into(),
                "Later".into(),
            ))
            .blocking_show();
        if !restart {
            self.ready = Some((update, bytes));
            return Ok(());
        }
        // A job may have started while the question was open.
        wait_until_idle(running_jobs, thread::sleep);
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
            eprintln!("baum desktop: cannot install baum {}: {e}", update.version);
            #[cfg(windows)]
            restart_daemon();
            if let Some(path) = state_file(app)
                && let Err(e) = remember_failed_install(&path, &update.version)
            {
                eprintln!("baum desktop: cannot record the failed update: {e}");
            }
            self.announce(
                app,
                &update,
                Some("This copy of baum could not update itself."),
            );
            return Ok(());
        }
        // Windows does not get here: the installer has already closed the
        // app, and starts it again when it finishes.
        app.restart()
    }

    fn announce<R: Runtime>(&mut self, app: &AppHandle<R>, update: &Update, reason: Option<&str>) {
        if self.announced.as_deref() == Some(update.version.as_str()) {
            return;
        }
        self.announced = Some(update.version.clone());
        let mut message = format!(
            "baum {} is available. You have {}.",
            update.version, update.current_version
        );
        if let Some(reason) = reason {
            message = format!("{reason} {message}");
        }
        let open = app
            .dialog()
            .message(message)
            .title("Update available")
            .kind(MessageDialogKind::Info)
            .buttons(MessageDialogButtons::OkCancelCustom(
                "Open download page".into(),
                "Later".into(),
            ))
            .blocking_show();
        if open {
            let page = release_page(&update.version);
            if let Err(e) = app.opener().open_url(&page, None::<&str>) {
                eprintln!("baum desktop: cannot open {page}: {e}");
            }
        }
    }
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
        let path = dir.join("config").join(STATE_FILE);
        assert!(!install_failed_before(&path, "1.2.3"));
        remember_failed_install(&path, "1.2.3").unwrap();
        remember_failed_install(&path, "1.2.3").unwrap();
        assert!(install_failed_before(&path, "1.2.3"));
        assert!(!install_failed_before(&path, "1.2.4"));
        assert_eq!(load(&path).failed_installs, vec!["1.2.3"]);
        // A damaged file forgets, rather than blocking updates.
        fs::write(&path, "not json").unwrap();
        assert!(!install_failed_before(&path, "1.2.3"));
        remember_failed_install(&path, "1.2.4").unwrap();
        assert_eq!(load(&path).failed_installs, vec!["1.2.4"]);
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
    fn waits_while_jobs_run() {
        let mut answers = vec![0, 1, 2].into_iter();
        let mut slept = Vec::new();
        wait_until_idle(|| answers.next_back().unwrap(), |d| slept.push(d));
        assert_eq!(slept, vec![JOBS_POLL, JOBS_POLL]);
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

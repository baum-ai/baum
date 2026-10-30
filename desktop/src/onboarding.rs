//! First run and the "Command Line and Hooks" screen. Agent hooks run `baum`
//! by name, so on first run the app shows a bundled setup screen that
//! installs its bundled `baum` as the user's command (see `install`) and adds
//! baum's marketplace plugins through the Claude Code and Codex CLIs (the bundled
//! `baum hooks install --user`). The user starts each change on that screen;
//! the Rust side makes it. When the user continues, the window attaches to
//! the daemon and opens its inbox. The menu item
//! reopens the screen to see what is installed, update it or remove it.
//!
//! Whether setup ran and what the app installed live in `setup.json` in the
//! app's configuration directory.

use std::ffi::OsStr;
use std::fs;
use std::io::{BufRead, BufReader, Read};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Mutex, mpsc};
use std::thread;
use std::time::{Duration, Instant};

use serde::{Deserialize, Serialize};
use tauri::menu::MenuItem;
use tauri::{AppHandle, Manager};

use crate::install::{self, Record, SystemPrompt};

/// The bundled setup screen.
pub const SETUP_PAGE: &str = "setup.html";
/// The setup screen's commands, as `capabilities/setup-screen.json` grants
/// them; development grants Vite's copy of the screen the same.
pub const SETUP_PERMISSIONS: &[&str] = &[
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
    "allow-repositories",
    "allow-finish-setup",
    "allow-step-zoom",
];
/// The bundled connecting screen.
const CONNECTING_PAGE: &str = "index.html";
const STATE_FILE: &str = "setup.json";
/// The menu item's id in the window's menu bar, and its label there and in
/// the tray.
pub const MENU_ID: &str = "command-line-and-hooks";
pub const MENU_LABEL: &str = "Command Line and Hooks…";

/// `baum daemon login-path` runs the login shell (10 s at most) and the
/// `--version` of another baum (5 s).
const LOGIN_PATH_TIMEOUT: Duration = Duration::from_secs(30);
// Login PATH resolution (30 s), two native actions (90 s each), and overhead.
const HOOKS_TIMEOUT: Duration = Duration::from_secs(240);
/// Starting a GitHub sign-in asks GitHub for a device code.
const GITHUB_TIMEOUT: Duration = Duration::from_secs(45);

/// The setup screen's state while the app runs.
#[derive(Default)]
pub struct Setup {
    /// The setup screen shows, so attaching to the daemon must not navigate
    /// away from it.
    open: AtomicBool,
    /// One change at a time.
    busy: Mutex<()>,
}

/// What `setup.json` holds.
#[derive(Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
struct Saved {
    /// The user finished first-run setup, whatever they chose.
    #[serde(default)]
    onboarded: bool,
    /// Every link the app installed and has not removed yet: usually one,
    /// more when removing an earlier one failed (a cancelled password
    /// prompt), so that Remove can try it again.
    #[serde(default)]
    commands: Vec<Record>,
    /// Where `setup.json` files of the first release kept their one record;
    /// read, moved into `commands` and not written again.
    #[serde(default, skip_serializing)]
    command: Option<Record>,
}

impl Saved {
    fn migrate(mut self) -> Saved {
        if let Some(record) = self.command.take()
            && !self.commands.contains(&record)
        {
            self.commands.insert(0, record);
        }
        self
    }
}

fn state_file(app: &AppHandle) -> Result<PathBuf, String> {
    app.path()
        .app_config_dir()
        .map(|dir| dir.join(STATE_FILE))
        .map_err(|e| format!("Cannot find the app's configuration directory: {e}"))
}

/// An unreadable file reads as a first run, which only shows setup again.
fn load_from(path: &Path) -> Saved {
    fs::read(path)
        .ok()
        .and_then(|data| serde_json::from_slice::<Saved>(&data).ok())
        .map(Saved::migrate)
        .unwrap_or_default()
}

/// After installing `new`, removes the app's other recorded links (`remove`
/// is `install::uninstall`) and returns the records to keep, `new` first,
/// with what happened. A link that could not be removed stays recorded.
fn after_install(
    previous: Vec<Record>,
    new: Record,
    remove: impl Fn(&Record) -> Result<String, String>,
) -> (Vec<Record>, Vec<String>) {
    let mut kept = vec![new.clone()];
    let mut lines = Vec::new();
    for record in previous.into_iter().filter(|r| r.link != new.link) {
        match remove(&record) {
            Ok(message) => lines.push(message),
            Err(e) => {
                lines.push(format!(
                    "The app's earlier link at {} stays ({e}); Remove tries it again.",
                    record.link.display()
                ));
                kept.push(record);
            }
        }
    }
    (kept, lines)
}

/// Removes every recorded link; returns those that could not be removed,
/// which stay recorded, with what happened.
fn remove_all(
    records: Vec<Record>,
    remove: impl Fn(&Record) -> Result<String, String>,
) -> (Vec<Record>, Vec<String>) {
    let mut left = Vec::new();
    let mut lines = Vec::new();
    for record in records {
        match remove(&record) {
            Ok(message) => lines.push(message),
            Err(e) => {
                lines.push(format!("{} stays: {e}", record.link.display()));
                left.push(record);
            }
        }
    }
    (left, lines)
}

fn save_to(path: &Path, saved: &Saved) -> Result<(), String> {
    let fail = |e: std::io::Error| format!("Cannot save {}: {e}", path.display());
    let dir = path.parent().ok_or("The setup file has no directory.")?;
    fs::create_dir_all(dir).map_err(fail)?;
    let data = serde_json::to_vec_pretty(saved).map_err(|e| e.to_string())?;
    let temporary = dir.join(format!(".{STATE_FILE}.{}", std::process::id()));
    fs::write(&temporary, data).map_err(fail)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&temporary, fs::Permissions::from_mode(0o600)).map_err(fail)?;
    }
    fs::rename(&temporary, path).map_err(fail)
}

fn load(app: &AppHandle) -> Saved {
    state_file(app).map(|p| load_from(&p)).unwrap_or_default()
}

fn save(app: &AppHandle, saved: &Saved) -> Result<(), String> {
    save_to(&state_file(app)?, saved)
}

/// Prepares setup when the app starts and returns the bundled page the
/// window opens on: the setup screen on first run, else the connecting
/// screen. Where the app keeps a stable copy of baum (an AppImage, Windows),
/// it refreshes it here, before anything runs baum, and runs that copy from
/// then on, so updating the app updates the command and the daemon.
pub fn start(app: &AppHandle) -> &'static str {
    if let (Some(copy), Ok(bundled)) = (managed_copy(app), crate::daemon::bundled_cli_path()) {
        // One refresh at a time: they share the temporary file's name.
        let refreshing = Mutex::new(());
        crate::daemon::use_stable_cli(move || {
            let _one = refreshing.lock().unwrap_or_else(|p| p.into_inner());
            install::refresh_copy(&bundled, &copy)
                .map(|_| copy.clone())
                .map_err(|e| {
                    format!(
                        "Cannot copy baum out of the app to {}: {e}. Check that the folder can be written and the disk has room, then try again.",
                        copy.display()
                    )
                })
        });
    }
    if load(app).onboarded {
        return CONNECTING_PAGE;
    }
    app.state::<Setup>().open.store(true, Ordering::SeqCst);
    SETUP_PAGE
}

/// Whether the setup screen shows, so the window must stay on it.
pub fn is_open(app: &AppHandle) -> bool {
    app.state::<Setup>().open.load(Ordering::SeqCst)
}

/// Opens the setup screen in the main window. Runs on the main thread (menu
/// events), as do `attach`'s decision and `finish_setup`, so where the window
/// goes is decided in one place at a time.
pub fn show(app: &AppHandle) {
    let Some(window) = app.get_webview_window(crate::MAIN) else {
        return;
    };
    let _ = window.unminimize();
    let _ = window.show();
    let _ = window.set_focus();
    app.state::<Setup>().open.store(true, Ordering::SeqCst);
    let _ = window.navigate(setup_url());
}

/// Sends the window to the daemon's launch URL unless the setup screen
/// shows; then it returns None, and leaving the screen attaches again. The
/// check and the navigation run together on the main thread.
pub fn attach(
    app: &AppHandle,
    window: &tauri::WebviewWindow,
    launch: &url::Url,
) -> Option<tauri::Result<()>> {
    let (send, receive) = mpsc::channel();
    let (handle, window, launch) = (app.clone(), window.clone(), launch.clone());
    let queued = app.run_on_main_thread(move || {
        let open = handle.state::<Setup>().open.load(Ordering::SeqCst);
        let result = (!open).then(|| window.navigate(launch));
        let _ = send.send(result);
    });
    if let Err(e) = queued {
        return Some(Err(e));
    }
    receive.recv().unwrap_or(None)
}

/// Where the setup screen loads from: Vite's copy under `desktop::dev`.
pub fn setup_url() -> url::Url {
    crate::development::setup_page().unwrap_or_else(|| bundled(SETUP_PAGE))
}

fn bundled(page: &str) -> url::Url {
    let mut url = crate::navigation::bundled_screen();
    url.set_path(&format!("/{page}"));
    url
}

/// The "Command Line and Hooks…" item of the window's menu bar; the tray
/// builds the menu bar and its own menu, and calls `show` for it.
pub fn menu_item(app: &AppHandle) -> tauri::Result<MenuItem<tauri::Wry>> {
    MenuItem::with_id(app, MENU_ID, MENU_LABEL, true, None::<&str>)
}

fn cli() -> Result<PathBuf, String> {
    crate::daemon::cli_path().map_err(|f| f.message)
}

/// Where the app keeps a stable copy of baum, if it does: an AppImage's
/// files vanish when it quits, so the daemon and the command must not run
/// them; on Windows the copy sits alone in a directory the user's PATH can
/// name. `APPIMAGE` alone is not enough, since children of an AppImage (such
/// as an editor's terminal) inherit it: the app must run from `APPDIR`.
fn managed_copy(app: &AppHandle) -> Option<PathBuf> {
    if crate::daemon::cli_overridden() || !(cfg!(windows) || running_as_appimage()) {
        return None;
    }
    // Windows: the local, not the roaming, application data.
    let dir = app.path().app_local_data_dir().ok()?;
    Some(
        dir.join("bin")
            .join(if cfg!(windows) { "baum.exe" } else { "baum" }),
    )
}

fn running_as_appimage() -> bool {
    let (Some(_), Some(appdir)) = (std::env::var_os("APPIMAGE"), std::env::var_os("APPDIR")) else {
        return false;
    };
    std::env::current_exe().is_ok_and(|exe| !appdir.is_empty() && exe.starts_with(&appdir))
}

/// The stable path the command links to: the baum the app runs.
fn target(app: &AppHandle, cli: &Path) -> Result<PathBuf, String> {
    if let Some(copy) = managed_copy(app)
        && cli != copy
    {
        return Err(format!(
            "The app could not copy its baum to {}, so the command cannot be installed. Restart the app to try again.",
            copy.display()
        ));
    }
    install::stable_target(cli)
}

/// What the setup screen shows.
#[derive(Debug, Serialize)]
pub struct View {
    first_run: bool,
    home: Option<PathBuf>,
    command: install::Status,
    hooks: Hooks,
}

/// Native marketplace plugin installation and enablement, from `baum hooks status --user --json`.
#[derive(Debug, Default, Serialize, Deserialize)]
pub struct Hooks {
    #[serde(default)]
    agents: Vec<AgentHooks>,
    #[serde(default)]
    error: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AgentHooks {
    agent: String,
    name: String,
    #[serde(default)]
    detected: bool,
    plugin: String,
    #[serde(default)]
    installed: bool,
    #[serde(default)]
    enabled: bool,
    #[serde(default)]
    version: Option<String>,
    #[serde(default)]
    error: Option<String>,
}

/// What a change printed, for the setup screen.
#[derive(Debug, Serialize)]
pub struct Outcome {
    ok: bool,
    lines: Vec<String>,
}

fn command_status(app: &AppHandle, cli: &Path) -> install::Status {
    let login = login_path(cli);
    let home = app.path().home_dir().unwrap_or_default();
    install::status(target(app, cli), &login, load(app).commands, &home)
}

/// The login shell's PATH and the baum on it, as the bundled baum reads it.
/// If that fails, the app's own PATH stands in, with the reason.
fn login_path(cli: &Path) -> install::LoginPath {
    let read = run(cli, &["daemon", "login-path", "--json"], LOGIN_PATH_TIMEOUT).and_then(|out| {
        if !out.ok {
            return Err(out.stderr.join(" "));
        }
        last_json(&out.stdout)
    });
    read.unwrap_or_else(|why| install::LoginPath {
        path: std::env::var_os("PATH")
            .map(|p| std::env::split_paths(&p).collect())
            .unwrap_or_default(),
        baum: None,
        error: Some(format!("The baum command could not read your PATH: {why}")),
    })
}

fn hooks_status(cli: &Path) -> Hooks {
    run_hooks(cli, &["hooks", "status", "--user", "--json"])
        .and_then(|out| {
            if !out.ok {
                return Err(out.stderr.join(" "));
            }
            let mut hooks = last_json::<Hooks>(&out.stdout)?;
            if !out.stderr.is_empty() {
                hooks.error = Some(out.stderr.join(" "));
            }
            Ok(hooks)
        })
        .unwrap_or_else(|why| Hooks {
            agents: Vec::new(),
            error: Some(why),
        })
}

fn last_json<T: serde::de::DeserializeOwned>(lines: &[String]) -> Result<T, String> {
    let line = lines
        .iter()
        .rev()
        .find(|l| !l.trim().is_empty())
        .ok_or("baum printed nothing.")?;
    serde_json::from_str(line).map_err(|e| format!("baum printed an unreadable answer: {e}"))
}

fn view(app: &AppHandle) -> Result<View, String> {
    let cli = cli()?;
    Ok(View {
        first_run: !load(app).onboarded,
        home: app.path().home_dir().ok(),
        command: command_status(app, &cli),
        hooks: hooks_status(&cli),
    })
}

async fn blocking<T: Send + 'static>(
    work: impl FnOnce() -> Result<T, String> + Send + 'static,
) -> Result<T, String> {
    tauri::async_runtime::spawn_blocking(work)
        .await
        .map_err(|e| e.to_string())?
}

/// The setup screen reads what is installed.
#[tauri::command]
pub async fn setup_state(app: AppHandle) -> Result<View, String> {
    blocking(move || view(&app)).await
}

/// Installs the command as the current plan says. `replace` names the
/// different baum the user agreed to replace, which the plan must still name.
#[tauri::command]
pub async fn install_command(app: AppHandle, replace: Option<PathBuf>) -> Result<Outcome, String> {
    blocking(move || {
        let setup = app.state::<Setup>();
        let _busy = setup.busy.lock().unwrap_or_else(|p| p.into_inner());
        let cli = cli()?;
        let status = command_status(&app, &cli);
        if status.installed {
            return Ok(Outcome {
                ok: true,
                lines: vec!["The baum command is installed.".into()],
            });
        }
        let (Some(plan), Some(target)) = (status.plan, status.target) else {
            return Err(status
                .unavailable
                .unwrap_or_else(|| "The baum command cannot be installed.".into()));
        };
        let record = install::install(&plan, &target, replace.as_deref(), &SystemPrompt)?;
        let mut saved = load(&app);
        let mut lines = installed_lines(&plan, &record);
        // Links the app made elsewhere earlier (a baum earlier on the PATH
        // hid them) are removed, and stay recorded if that fails.
        let home = app.path().home_dir().unwrap_or_default();
        let (kept, removed) = after_install(std::mem::take(&mut saved.commands), record, |r| {
            install::uninstall(r, Some(target.as_path()), &home, &SystemPrompt)
        });
        lines.extend(removed);
        saved.commands = kept;
        save(&app, &saved)?;
        Ok(Outcome { ok: true, lines })
    })
    .await
}

fn installed_lines(plan: &install::Plan, record: &Record) -> Vec<String> {
    let mut lines = vec![format!(
        "Installed the baum command: {} runs {}.",
        record.link.display(),
        record.target.display()
    )];
    if let Some(backup) = &record.backup {
        lines.push(format!(
            "The baum that was there is kept as {}; removing the command puts it back.",
            backup.display()
        ));
    }
    if !plan.on_path {
        lines.push(format!(
            "{} is not on your PATH yet: add it in your shell's startup file, then open a new terminal.",
            record.link.parent().unwrap_or(Path::new("")).display()
        ));
    }
    lines
}

/// Removes the command the app installed, and only that.
#[tauri::command]
pub async fn uninstall_command(app: AppHandle) -> Result<Outcome, String> {
    blocking(move || {
        let setup = app.state::<Setup>();
        let _busy = setup.busy.lock().unwrap_or_else(|p| p.into_inner());
        let mut saved = load(&app);
        if saved.commands.is_empty() {
            return Ok(Outcome {
                ok: true,
                lines: vec!["The app has not installed a baum command.".into()],
            });
        }
        // The copy the app runs stays: only the links go.
        let current = cli().and_then(|cli| target(&app, &cli)).ok();
        let home = app.path().home_dir().unwrap_or_default();
        let (left, lines) = remove_all(std::mem::take(&mut saved.commands), |r| {
            install::uninstall(r, current.as_deref(), &home, &SystemPrompt)
        });
        let ok = left.is_empty();
        saved.commands = left;
        save(&app, &saved)?;
        Ok(Outcome { ok, lines })
    })
    .await
}

/// Runs the bundled `baum hooks install --user`, for the agents it finds.
#[tauri::command]
pub async fn install_hooks(app: AppHandle, agent: Option<String>) -> Result<Outcome, String> {
    change_hooks(app, "install", agent).await
}

/// Runs the bundled `baum hooks update --user`, which brings an installed
/// plugin to the marketplace's latest version.
#[tauri::command]
pub async fn update_hooks(app: AppHandle, agent: Option<String>) -> Result<Outcome, String> {
    change_hooks(app, "update", agent).await
}

/// Runs the bundled `baum hooks uninstall --user`.
#[tauri::command]
pub async fn uninstall_hooks(app: AppHandle, agent: Option<String>) -> Result<Outcome, String> {
    change_hooks(app, "uninstall", agent).await
}

async fn change_hooks(
    app: AppHandle,
    action: &'static str,
    agent: Option<String>,
) -> Result<Outcome, String> {
    blocking(move || {
        let setup = app.state::<Setup>();
        let _busy = setup.busy.lock().unwrap_or_else(|p| p.into_inner());
        let mut args = vec!["hooks", action, "--user"];
        if let Some(agent) = agent.as_deref() {
            if !matches!(agent, "codex" | "claude-code") {
                return Err("Choose Codex or Claude Code.".into());
            }
            args.extend(["--agent", agent]);
        }
        let out = run_hooks(&cli()?, &args)?;
        let mut lines = out.stdout;
        lines.extend(out.stderr);
        Ok(Outcome { ok: out.ok, lines })
    })
    .await
}

/// The native folder picker. The setup screen and the daemon's pages (the
/// `daemon-pages` capability) may call it: it changes nothing and returns only
/// the folder the user chose. With `clone_target` it asks where a new clone
/// should go rather than for an existing clone.
#[tauri::command]
pub async fn choose_repository(
    app: AppHandle,
    clone_target: Option<bool>,
) -> Result<Option<String>, String> {
    use tauri_plugin_dialog::DialogExt;
    let title = if clone_target.unwrap_or(false) {
        "Choose where to clone the repository"
    } else {
        "Choose a local GitHub repository"
    };
    blocking(move || {
        app.dialog()
            .file()
            .set_title(title)
            .blocking_pick_folder()
            .map(|path| {
                path.into_path()
                    .map(|p| p.to_string_lossy().into_owned())
                    .map_err(|e| e.to_string())
            })
            .transpose()
    })
    .await
}

/// Register one existing clone without changing its agent or Git hooks.
#[tauri::command]
pub async fn add_repository(app: AppHandle, path: PathBuf) -> Result<Outcome, String> {
    blocking(move || {
        let setup = app.state::<Setup>();
        let _busy = setup.busy.lock().unwrap_or_else(|p| p.into_inner());
        if !path.is_absolute() || !path.is_dir() {
            return Err("Choose an existing repository folder using an absolute path.".into());
        }
        let path = path
            .to_str()
            .ok_or("This folder path is not valid UTF-8.")?;
        let out = run(
            &cli()?,
            &["internal", "repos", "add", "--", path],
            LOGIN_PATH_TIMEOUT,
        )?;
        let mut lines = out.stdout;
        lines.extend(out.stderr);
        Ok(Outcome { ok: out.ok, lines })
    })
    .await
}

/// How the app generates reviews, as `baum internal reviews show` prints it.
#[derive(Debug, Serialize, Deserialize)]
pub struct ReviewSettings {
    backend: String,
    model: String,
    automatic: bool,
    pull_requests: String,
    /// The decision capture model, empty for the harness's default.
    #[serde(default)]
    decisions_model: String,
    /// The data extraction harness Settings chose, empty while it follows
    /// the reviews'; read only, never saved.
    #[serde(default)]
    decisions_chosen_backend: String,
    /// The harness and model decision capture uses (empty when unknown),
    /// and whether it ignores `decisions_model`; read only, never saved.
    #[serde(default)]
    decisions_backend: String,
    #[serde(default)]
    decisions_effective_model: String,
    #[serde(default)]
    decisions_model_ignored: bool,
}

#[tauri::command]
pub async fn review_settings() -> Result<ReviewSettings, String> {
    blocking(move || {
        let out = run(
            &cli()?,
            &["internal", "reviews", "show"],
            LOGIN_PATH_TIMEOUT,
        )?;
        if !out.ok {
            return Err(out.stderr.join("\n"));
        }
        serde_json::from_str(&out.stdout.join("\n"))
            .map_err(|e| format!("Cannot read the review settings: {e}"))
    })
    .await
}

/// Saves the review settings; the CLI validates every value.
#[tauri::command]
pub async fn save_review_settings(
    app: AppHandle,
    settings: ReviewSettings,
) -> Result<Outcome, String> {
    blocking(move || {
        let setup = app.state::<Setup>();
        let _busy = setup.busy.lock().unwrap_or_else(|p| p.into_inner());
        // `--flag=value` keeps a value starting with `-` from reading as a flag.
        let args = [
            format!("--backend={}", settings.backend),
            format!("--model={}", settings.model),
            format!("--automatic={}", settings.automatic),
            format!("--pull-requests={}", settings.pull_requests),
            format!("--decisions-model={}", settings.decisions_model),
        ];
        let mut command = vec!["internal", "reviews", "set"];
        command.extend(args.iter().map(String::as_str));
        let out = run(&cli()?, &command, LOGIN_PATH_TIMEOUT)?;
        let mut lines = out.stdout;
        lines.extend(out.stderr);
        Ok(Outcome { ok: out.ok, lines })
    })
    .await
}

/// The GitHub step signs in through the attached daemon's own API, which runs
/// the device flow and keeps the credential, so the screen never holds a
/// token. It reads the connection, or starts, cancels or rechecks a sign-in;
/// disconnecting stays in the app's settings.
#[tauri::command]
pub async fn github_connection(
    app: AppHandle,
    action: Option<String>,
) -> Result<serde_json::Value, String> {
    blocking(move || {
        let launch =
            crate::tray::launch(&app).ok_or("baum is still starting. Try again in a moment.")?;
        let response = match action.as_deref() {
            None => crate::client::request(
                &launch,
                "GET",
                "/api/github-auth",
                None,
                crate::client::POLL_TIMEOUT,
            )?,
            Some(name @ ("start" | "cancel" | "retry")) => crate::client::request(
                &launch,
                "POST",
                &format!("/api/github-auth/{name}"),
                Some("{}"),
                GITHUB_TIMEOUT,
            )?,
            Some(other) => return Err(format!("Unknown GitHub action: {other}")),
        };
        if response.status != 200 {
            return Err(format!(
                "The baum daemon answered {}: {}",
                response.status,
                String::from_utf8_lossy(&response.body).trim()
            ));
        }
        serde_json::from_slice(&response.body)
            .map_err(|e| format!("Cannot read the GitHub connection: {e}"))
    })
    .await
}

/// The bundled repository step reaches only these local API actions; it cannot
/// provide an arbitrary HTTP path, method or credential.
#[tauri::command(rename_all = "snake_case")]
pub async fn repositories(
    app: AppHandle,
    action: String,
    repository: Option<String>,
    query: Option<String>,
    path: Option<String>,
    dir: Option<String>,
    include_documents: Option<bool>,
) -> Result<serde_json::Value, String> {
    blocking(move || {
        let (method, route, body) = repository_request(
            &action,
            repository.as_deref(),
            query.as_deref(),
            path.as_deref(),
            dir.as_deref(),
            include_documents.unwrap_or(false),
        )?;
        let launch =
            crate::tray::launch(&app).ok_or("baum is still starting. Try again in a moment.")?;
        let body = body.map(|b| b.to_string());
        let response = crate::client::request(
            &launch,
            method,
            &route,
            body.as_deref(),
            Duration::from_secs(130),
        )?;
        if !(200..300).contains(&response.status) {
            let value: serde_json::Value =
                serde_json::from_slice(&response.body).unwrap_or_default();
            return Err(value["message"]
                .as_str()
                .map(str::to_owned)
                .unwrap_or_else(|| {
                    format!(
                        "The baum daemon answered {}: {}",
                        response.status,
                        String::from_utf8_lossy(&response.body).trim()
                    )
                }));
        }
        if response.status == 204 {
            return Ok(serde_json::Value::Null);
        }
        serde_json::from_slice(&response.body).map_err(|e| format!("Cannot read repositories: {e}"))
    })
    .await
}

fn repository_request(
    action: &str,
    repository: Option<&str>,
    query: Option<&str>,
    path: Option<&str>,
    dir: Option<&str>,
    documents: bool,
) -> Result<(&'static str, String, Option<serde_json::Value>), String> {
    use serde_json::json;
    let identity = || -> Result<&str, String> {
        let name = repository.ok_or("Choose a GitHub repository.")?;
        let parts: Vec<_> = name.split('/').collect();
        if parts.len() != 2
            || parts.iter().any(|p| {
                p.is_empty()
                    || *p == "."
                    || *p == ".."
                    || !p
                        .bytes()
                        .all(|c| c.is_ascii_alphanumeric() || b"-_.".contains(&c))
            })
        {
            return Err("Invalid GitHub repository.".into());
        }
        Ok(name)
    };
    let base = || identity().map(|name| format!("/api/repositories/{name}"));
    Ok(match action {
        "list" => (
            "GET",
            format!(
                "/api/github/repositories?{}",
                url::form_urlencoded::Serializer::new(String::new())
                    .append_pair("query", query.unwrap_or(""))
                    .finish()
            ),
            None,
        ),
        "picked" => ("GET", "/api/repositories".into(), None),
        "found" => ("GET", "/api/clones/found".into(), None),
        "refresh" => (
            "POST",
            "/api/clones/found/refresh".into(),
            Some(json!({"include_documents": documents})),
        ),
        "pick" => (
            "POST",
            "/api/repositories".into(),
            Some(json!({"repository": identity()?})),
        ),
        "unpick" => ("DELETE", base()?, Some(json!({}))),
        "target" => ("GET", format!("{}/clone-target", base()?), None),
        "status" => ("GET", format!("{}/clone", base()?), None),
        "clone" => (
            "POST",
            format!("{}/clone", base()?),
            Some(dir.map(|d| json!({"dir":d})).unwrap_or(json!({}))),
        ),
        "attach" => {
            let mut body = json!({"path":path.ok_or("Choose a local folder.")?});
            if repository.is_some() {
                body["repository"] = json!(identity()?);
            }
            ("POST", "/api/repos".into(), Some(body))
        }
        _ => return Err("Unknown repository action.".into()),
    })
}

#[cfg(test)]
mod repository_relay_tests {
    use super::*;
    #[test]
    fn relay_allows_only_named_actions_and_safe_identities() {
        assert!(repository_request("DELETE", None, None, None, None, false).is_err());
        for name in [
            "../repo",
            "owner/../repo",
            "owner/repo\r\nX: a",
            "owner/repo?x",
            "owner/",
        ] {
            assert!(repository_request("clone", Some(name), None, None, None, false).is_err());
        }
        let (_, route, _) =
            repository_request("list", None, Some("x\r\nHeader: bad"), None, None, false).unwrap();
        assert!(!route.contains('\r') && !route.contains('\n'));
        let (method, route, body) = repository_request(
            "attach",
            Some("owner/repo"),
            None,
            Some("/work/code/fork"),
            None,
            false,
        )
        .unwrap();
        assert_eq!(method, "POST");
        assert_eq!(route, "/api/repos");
        assert_eq!(body.unwrap()["repository"], "owner/repo");
        let (_, _, body) = repository_request("refresh", None, None, None, None, true).unwrap();
        assert_eq!(body.unwrap()["include_documents"], true);
    }
}

/// Leaves the setup screen (a synchronous command, so on the main thread):
/// records that first-run setup is done, returns
/// to the connecting screen and attaches to the daemon, opening its inbox.
#[tauri::command]
pub fn finish_setup(app: AppHandle) -> Result<(), String> {
    let mut saved = load(&app);
    let first = !saved.onboarded;
    if first {
        saved.onboarded = true;
        save(&app, &saved)?;
    }
    let setup = app.state::<Setup>();
    setup.open.store(false, Ordering::SeqCst);
    if let Some(window) = app.get_webview_window(crate::MAIN) {
        let _ = window.navigate(bundled(CONNECTING_PAGE));
    }
    crate::connect(&app, crate::Attach::First);
    Ok(())
}

struct Output {
    ok: bool,
    stdout: Vec<String>,
    stderr: Vec<String>,
}

/// Runs the bundled baum with a deadline, reading its output on separate
/// threads.
fn run(cli: &Path, args: &[&str], timeout: Duration) -> Result<Output, String> {
    run_with_path(cli, args, timeout, None)
}

// GUI launchers often omit shell/npm/Homebrew paths. Use the same login PATH
// resolution as command setup, without mutating the app's global environment.
fn run_hooks(cli: &Path, args: &[&str]) -> Result<Output, String> {
    let started = Instant::now();
    let login = login_path(cli);
    let path = std::env::join_paths(&login.path)
        .map_err(|e| format!("Cannot use the login shell's PATH for agent plugins: {e}"))?;
    let mut out = run_with_path(
        cli,
        args,
        HOOKS_TIMEOUT.saturating_sub(started.elapsed()),
        Some(&path),
    )?;
    if let Some(error) = login.error {
        out.stderr.insert(
            0,
            format!("warning: {error} Using the app's inherited PATH for agent plugins."),
        );
    }
    Ok(out)
}

fn run_with_path(
    cli: &Path,
    args: &[&str],
    timeout: Duration,
    path: Option<&OsStr>,
) -> Result<Output, String> {
    let mut command = Command::new(cli);
    if let Some(path) = path {
        command.env("PATH", path);
    }
    let mut child = command
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| format!("Cannot run the bundled baum ({}): {e}", cli.display()))?;
    let stdout = lines(child.stdout.take());
    let stderr = lines(child.stderr.take());
    let deadline = Instant::now() + timeout;
    let status = loop {
        match child.try_wait() {
            Ok(Some(status)) => break status,
            Ok(None) if Instant::now() < deadline => thread::sleep(Duration::from_millis(50)),
            Ok(None) => {
                let _ = child.kill();
                let _ = child.wait();
                return Err(format!(
                    "baum {} did not finish within {} seconds.",
                    args.join(" "),
                    timeout.as_secs()
                ));
            }
            Err(e) => return Err(format!("Cannot wait for baum: {e}")),
        }
    };
    // A process the command left behind may hold its output open.
    let collect = |lines: mpsc::Receiver<Vec<String>>| {
        lines
            .recv_timeout(Duration::from_secs(2))
            .unwrap_or_default()
    };
    Ok(Output {
        ok: status.success(),
        stdout: collect(stdout),
        stderr: collect(stderr),
    })
}

fn lines<R: Read + Send + 'static>(stream: Option<R>) -> mpsc::Receiver<Vec<String>> {
    let (send, receive) = mpsc::channel();
    thread::spawn(move || {
        let lines = stream
            .map(|s| BufReader::new(s).lines().map_while(Result::ok).collect())
            .unwrap_or_default();
        let _ = send.send(lines);
    });
    receive
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp_dir(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("baum-setup-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[test]
    fn first_run_state_round_trips() {
        let path = temp_dir("state").join("config/setup.json");
        assert_eq!(load_from(&path), Saved::default());
        let saved = Saved {
            onboarded: true,
            commands: vec![record("/home/user/.local/bin/baum")],
            command: None,
        };
        save_to(&path, &saved).unwrap();
        assert_eq!(load_from(&path), saved);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = fs::metadata(&path).unwrap().permissions().mode();
            assert_eq!(mode & 0o777, 0o600);
        }
        fs::write(&path, "not json").unwrap();
        assert_eq!(load_from(&path), Saved::default());
    }

    fn record(link: &str) -> Record {
        Record {
            link: link.into(),
            target: "/Applications/baum.app/Contents/MacOS/baum".into(),
            backup: None,
            admin: false,
        }
    }

    /// A setup.json of the first release, with one `command`, reads as a
    /// list and is written back as one.
    #[test]
    fn an_older_setup_file_is_migrated() {
        let path = temp_dir("migrate").join("setup.json");
        fs::write(
            &path,
            r#"{"onboarded":true,"command":{"link":"/usr/local/bin/baum","target":"/Applications/baum.app/Contents/MacOS/baum","backup":"/usr/local/bin/baum.replaced-by-baum-app","admin":true}}"#,
        )
        .unwrap();
        let saved = load_from(&path);
        assert!(saved.onboarded && saved.command.is_none());
        assert_eq!(saved.commands.len(), 1);
        assert_eq!(saved.commands[0].link, PathBuf::from("/usr/local/bin/baum"));
        assert!(saved.commands[0].admin && saved.commands[0].backup.is_some());
        save_to(&path, &saved).unwrap();
        let written = fs::read_to_string(&path).unwrap();
        assert!(
            written.contains("\"commands\"") && !written.contains("\"command\""),
            "{written}"
        );
        assert_eq!(load_from(&path), saved);
    }

    /// An earlier link that cannot be removed (a cancelled password prompt)
    /// stays recorded beside the new one, and Remove tries every one.
    #[test]
    fn earlier_links_stay_recorded_until_they_are_removed() {
        let old = record("/usr/local/bin/baum");
        let other = record("/opt/tools/bin/baum");
        let new = record("/home/user/.local/bin/baum");
        let cancelled = |r: &Record| {
            if r.link == old.link {
                Err("Cancelled.".to_string())
            } else {
                Ok(format!("Removed {}.", r.link.display()))
            }
        };
        let (kept, lines) = after_install(
            vec![old.clone(), other.clone(), new.clone()],
            new.clone(),
            cancelled,
        );
        assert_eq!(kept, vec![new.clone(), old.clone()]);
        assert!(
            lines.iter().any(
                |l| l.contains("/usr/local/bin/baum stays (Cancelled.); Remove tries it again")
            ),
            "{lines:?}"
        );
        assert!(
            lines.iter().any(|l| l == "Removed /opt/tools/bin/baum."),
            "{lines:?}"
        );

        let (left, lines) = remove_all(kept.clone(), cancelled);
        assert_eq!(left, vec![old.clone()]);
        assert_eq!(lines.len(), 2);
        let (left, _) = remove_all(left, |_| Ok(String::new()));
        assert!(left.is_empty());
    }

    #[test]
    fn the_setup_page_is_bundled() {
        let url = bundled(SETUP_PAGE);
        assert_eq!(
            crate::navigation::decide(&url, None, true),
            crate::navigation::Decision::Allow
        );
        assert!(url.path().ends_with("/setup.html"));
    }

    #[test]
    fn reads_the_hooks_status() {
        let hooks: Hooks = last_json(&[
            r#"{"agents":[{"agent":"claude-code","name":"Claude Code","detected":true,"plugin":"baum@baum","installed":true,"enabled":false},{"agent":"codex","name":"Codex","detected":false,"plugin":"baum@baum","installed":false,"error":"not valid JSON"}]}"#.into(),
        ])
        .unwrap();
        assert_eq!(hooks.agents.len(), 2);
        assert!(hooks.agents[0].installed && hooks.agents[0].detected);
        assert!(!hooks.agents[0].enabled);
        assert_eq!(hooks.agents[0].plugin, "baum@baum");
        assert_eq!(hooks.agents[1].error.as_deref(), Some("not valid JSON"));
        assert!(last_json::<Hooks>(&[]).is_err());
    }

    #[cfg(unix)]
    mod cli {
        use super::*;

        use crate::daemon::fake_cli;

        #[test]
        fn reads_the_login_path() {
            let dir = temp_dir("login-path");
            let cli = fake_cli(
                &dir,
                r#"[ "$*" = "daemon login-path --json" ] || exit 9
echo '{"shell":"/bin/zsh","login_shell":true,"path":["/home/user/.local/bin","/usr/bin"],"baum":{"path":"/home/user/go/bin/baum","resolved":"/home/user/go/bin/baum","version":"v0.2.9","self":false}}'"#,
            );
            let login = login_path(&cli);
            assert_eq!(login.path.len(), 2);
            assert_eq!(login.baum.unwrap().version, "v0.2.9");
            assert!(login.error.is_none());
        }

        #[test]
        fn falls_back_to_the_apps_path_when_baum_cannot_read_it() {
            let dir = temp_dir("login-path-fails");
            let cli = fake_cli(&dir, "echo 'unknown command' >&2; exit 1");
            let login = login_path(&cli);
            assert!(login.baum.is_none());
            assert!(login.error.unwrap().contains("unknown command"));
        }

        #[test]
        fn native_plugin_status_install_and_remove_use_the_login_path() {
            let dir = temp_dir("plugin-login-path");
            let cli = fake_cli(
                &dir,
                r#"
if [ "$*" = "daemon login-path --json" ]; then
  echo '{"path":["/resolved/login/agents"]}'
  exit 0
fi
[ "$PATH" = "/resolved/login/agents" ] || { echo 'wrong plugin PATH' >&2; exit 9; }
case "$*" in
  'hooks status --user --json') echo '{"agents":[{"agent":"codex","name":"Codex","detected":true,"plugin":"baum@baum","installed":true,"enabled":true}]}' ;;
  'hooks install --user'|'hooks uninstall --user') echo 'native plugin command saw login PATH' ;;
  *) exit 8 ;;
esac
"#,
            );
            let hooks = hooks_status(&cli);
            assert_eq!(hooks.agents.len(), 1);
            assert!(hooks.agents[0].detected && hooks.agents[0].enabled);
            assert!(hooks.error.is_none());
            for action in ["install", "uninstall"] {
                let out = run_hooks(&cli, &["hooks", action, "--user"]).unwrap();
                assert!(out.ok, "{:?}", out.stderr);
                assert_eq!(out.stdout, ["native plugin command saw login PATH"]);
            }
        }

        #[test]
        fn native_plugins_preserve_status_and_show_login_path_fallback() {
            let dir = temp_dir("plugin-login-path-fails");
            let cli = fake_cli(
                &dir,
                r#"
if [ "$*" = "daemon login-path --json" ]; then
  echo 'login shell unavailable' >&2
  exit 1
fi
printf '%s\n' "$PATH"
echo '{"agents":[{"agent":"codex","name":"Codex","detected":true,"plugin":"baum@baum","installed":false,"enabled":false}]}'
"#,
            );
            let hooks = hooks_status(&cli);
            assert_eq!(hooks.agents.len(), 1);
            let warning = hooks.error.unwrap();
            assert!(warning.contains("login shell unavailable"));
            assert!(warning.contains("inherited PATH"));
            for action in ["install", "uninstall"] {
                let out = run_hooks(&cli, &["hooks", action, "--user"]).unwrap();
                assert!(out.ok);
                assert_eq!(out.stdout[0], std::env::var("PATH").unwrap_or_default());
                assert!(out.stderr[0].contains("login shell unavailable"));
            }
        }

        #[test]
        fn a_hanging_command_times_out() {
            let dir = temp_dir("hangs");
            let cli = fake_cli(&dir, "exec sleep 30");
            let started = Instant::now();
            let err = run(&cli, &["hooks"], Duration::from_millis(300))
                .err()
                .unwrap();
            assert!(err.contains("did not finish"), "{err}");
            assert!(started.elapsed() < Duration::from_secs(5));
        }

        #[test]
        fn hooks_status_reports_a_failing_command() {
            let dir = temp_dir("hooks-fail");
            let cli = fake_cli(&dir, "echo 'find the home directory: no' >&2; exit 1");
            let hooks = hooks_status(&cli);
            assert!(hooks.agents.is_empty());
            assert!(hooks.error.unwrap().contains("home directory"));
        }
    }
}

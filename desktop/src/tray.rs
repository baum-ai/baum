//! The tray icon and its menu: the app's presence while the window is closed.
//!
//! The icon follows `status::View`, which a poll thread computes from the
//! attached daemon's `/api/jobs`, `/api/automatic`, `/api/index?view=waiting`
//! and `/api/live-sessions`. It reads when the daemon's event stream
//! (`/api/events`, kept open by its own thread, reconnecting with backoff)
//! announces a change, after a 500 ms pause for a burst of events to end; as a
//! safety net it also reads every 60 s while the stream is up and every 30 s
//! while it is down, and never with no daemon attached. A separate worker
//! computes from `/api/diagnostics` and `/api/plan-usage`
//! (diagnostics every 30 minutes, without `refresh`, so the daemon answers from
//! its cache of at most that age; plan usage every 5 minutes, cached that long
//! by the daemon). `main` tells this module when it attaches
//! to a daemon, loses it, or starts connecting.
//!
//! On macOS and Windows a left click opens `panel`, a small window drawn like
//! a menu with meters, and a right click this menu. Linux tray hosts report no
//! clicks, so there the menu is all, with plan usage as lines of text.
//!
//! Start at login can also be toggled from the window's menu bar, the app's
//! settings surface: the pages the daemon serves have no Tauri capability,
//! so they cannot offer it themselves.

use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc::{self, RecvTimeoutError};
use std::sync::{Arc, Mutex, MutexGuard};
use std::thread;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use tauri::image::Image;
use tauri::menu::{CheckMenuItem, IsMenuItem, Menu, MenuItem, PredefinedMenuItem, Submenu};
use tauri::tray::{TrayIcon, TrayIconBuilder};
use tauri::{AppHandle, Manager, Wry};
use tauri_plugin_opener::OpenerExt;

use crate::daemon::Launch;
use crate::notifications::Route;
use crate::status::{self, Icon, Link, Observed, Plan, Tracker, View};
use crate::{autostart, client, lifecycle, notifications, onboarding, panel};

/// Events wait this long for more before the tray reads (at most
/// `DEBOUNCE_MAX`), so a burst of job events costs one set of requests.
const DEBOUNCE: Duration = Duration::from_millis(500);
const DEBOUNCE_MAX: Duration = Duration::from_secs(2);
/// A read with no event, as a safety net while the event stream is up.
const FALLBACK_STREAM_UP: Duration = Duration::from_secs(60);
/// The same while the stream is down, when nothing announces changes.
const FALLBACK_STREAM_DOWN: Duration = Duration::from_secs(30);
const STREAM_BACKOFF_FIRST: Duration = Duration::from_secs(1);
const STREAM_BACKOFF_MAX: Duration = Duration::from_secs(30);
const DIAGNOSTICS_INTERVAL: Duration = Duration::from_secs(30 * 60);
/// The daemon caches plan usage this long, so asking sooner repeats it.
const PLAN_USAGE_INTERVAL: Duration = Duration::from_secs(300);
/// The prefix of a waiting session's menu id, before its `agent:id` key.
pub const SESSION_PREFIX: &str = "session:";

const TRAY_ID: &str = "baum";
/// The window menu's own item, so its events never match the tray's.
const MENU_LOGIN: &str = "menu-login";
/// The window menu's Help item; the tray's is "report".
const MENU_REPORT: &str = "menu-report";
/// Where a problem is reported, and what Report a problem opens when the
/// daemon cannot supply a prefilled link: the public repository's issue chooser.
const ISSUES_CHOOSE: &str = "https://github.com/baum-ai/baum/issues/new/choose";
/// A link the daemon built must open the bug report of that repository and
/// nothing else, whatever the daemon answers.
const ISSUES_NEW_PREFIX: &str = "https://github.com/baum-ai/baum/issues/new?";

const IDLE_ICON: &[u8] = include_bytes!("../icons/tray/idle.png");
const RUNNING_ICON: &[u8] = include_bytes!("../icons/tray/running.png");
const ATTENTION_ICON: &[u8] = include_bytes!("../icons/tray/attention.png");

#[derive(Debug, PartialEq, Eq)]
pub enum Kind {
    Item,
    Check(bool),
    Separator,
}

#[derive(Debug, PartialEq, Eq)]
pub struct Entry {
    pub id: String,
    pub text: String,
    pub kind: Kind,
    pub enabled: bool,
}

impl Entry {
    fn item(id: &str, text: impl Into<String>, enabled: bool) -> Entry {
        Entry {
            id: id.into(),
            text: text.into(),
            kind: Kind::Item,
            enabled,
        }
    }
}

/// Seconds since the Unix epoch, for "resets in" and "ago".
pub fn now() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| i64::try_from(d.as_secs()).unwrap_or(i64::MAX))
}

/// The tray menu for a view, in the order of the panel: what the daemon is
/// doing and anything needing the user (an alert goes where to look about
/// it, a waiting session opens its transcript), the plan usage left when any
/// can be read, then Open, pause
/// or resume, start at login, Quit, and Quit and stop.
pub fn menu_spec(view: &View, login: bool, now: i64) -> Vec<Entry> {
    let separator = |id: &str| Entry {
        id: id.into(),
        text: String::new(),
        kind: Kind::Separator,
        enabled: false,
    };
    // While reviews run or wait, the status opens the jobs list.
    let mut entries = vec![Entry::item(
        "summary",
        view.summary.clone(),
        view.running + view.queued > 0,
    )];
    if let Some(waiting) = view.waiting_label() {
        entries.push(Entry::item("waiting", waiting, false));
    }
    for alert in &view.attention {
        entries.push(Entry::item(
            &alert.id,
            format!("{} · {}", alert.text, alert.action),
            true,
        ));
    }
    for needs in &view.needs {
        entries.push(Entry::item(
            &format!("{SESSION_PREFIX}{}", needs.key),
            format!("{} · {}", needs.title, needs.detail),
            true,
        ));
    }
    if !view.usage.is_empty() {
        entries.push(separator("separator-usage"));
        entries.push(Entry::item("usage", "Plan usage left", false));
        for (i, meter) in view.usage.iter().enumerate() {
            entries.push(Entry::item(
                &format!("usage-{i}"),
                format!("{}: {}", meter.label, meter.text(now)),
                false,
            ));
        }
    }
    entries.push(separator("separator-1"));
    entries.push(Entry::item("open", "Open baum", true));
    entries.push(Entry::item(
        "pause",
        status::pause_label(view.automatic),
        view.automatic.is_some(),
    ));
    entries.push(Entry {
        id: "login".into(),
        text: "Start at login".into(),
        kind: Kind::Check(login),
        enabled: true,
    });
    entries.push(Entry::item("report", "Report a problem…", true));
    entries.push(separator("separator-2"));
    entries.push(Entry::item("quit", "Quit", true));
    entries.push(Entry::item(
        "quit-stop",
        "Quit and stop background reviews",
        true,
    ));
    entries
}

/// What a desktop's StatusNotifier host answered, from `NameHasOwner`.
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
fn parse_name_has_owner(output: &str) -> Option<bool> {
    if output.contains("true") {
        Some(true)
    } else if output.contains("false") {
        Some(false)
    } else {
        None
    }
}

#[derive(Debug, PartialEq, Eq)]
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
enum Host {
    Present,
    Absent,
    /// Neither `busctl` nor `gdbus` could ask.
    Unknown,
}

/// Whether something on this desktop shows tray icons. Linux icons need a
/// StatusNotifier watcher on the session bus (GNOME has none without an
/// extension); the icon builder succeeds without one and nothing appears.
/// When neither `busctl` nor `gdbus` can ask, the answer is Unknown, which
/// counts as no tray: the window then stays reachable.
#[cfg(target_os = "linux")]
fn host_check() -> Host {
    const WATCHER: &str = "org.kde.StatusNotifierWatcher";
    match run_bounded("busctl", &["--user", "status", WATCHER], HOST_CHECK_TIMEOUT) {
        Bounded::Done { success, .. } => {
            return if success { Host::Present } else { Host::Absent };
        }
        // A stalled bus will not answer gdbus either.
        Bounded::TimedOut => return Host::Unknown,
        Bounded::NotRun => {}
    }
    let gdbus = run_bounded(
        "gdbus",
        &[
            "call",
            "--session",
            "--dest",
            "org.freedesktop.DBus",
            "--object-path",
            "/org/freedesktop/DBus",
            "--method",
            "org.freedesktop.DBus.NameHasOwner",
            WATCHER,
        ],
        HOST_CHECK_TIMEOUT,
    );
    match gdbus {
        Bounded::Done { stdout, .. } => match parse_name_has_owner(&stdout) {
            Some(true) => Host::Present,
            Some(false) => Host::Absent,
            None => Host::Unknown,
        },
        Bounded::TimedOut | Bounded::NotRun => Host::Unknown,
    }
}

/// How long one bus query may take. The check runs when the window closes,
/// on the main thread, so a stalled session bus must not hang it.
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
const HOST_CHECK_TIMEOUT: Duration = Duration::from_secs(1);

#[derive(Debug, PartialEq, Eq)]
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
enum Bounded {
    Done {
        success: bool,
        stdout: String,
    },
    TimedOut,
    /// The program could not be started.
    NotRun,
}

/// Runs a program and waits at most `timeout`, killing it after that.
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
fn run_bounded(program: &str, args: &[&str], timeout: Duration) -> Bounded {
    use std::io::Read;
    use std::process::{Command, Stdio};
    let Ok(mut child) = Command::new(program)
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
    else {
        return Bounded::NotRun;
    };
    let deadline = Instant::now() + timeout;
    let status = loop {
        match child.try_wait() {
            Ok(Some(status)) => break status,
            Ok(None) if Instant::now() < deadline => thread::sleep(Duration::from_millis(10)),
            _ => {
                let _ = child.kill();
                let _ = child.wait();
                return Bounded::TimedOut;
            }
        }
    };
    // The answers are a few bytes, so they fit the pipe and are there to read.
    let mut stdout = String::new();
    if let Some(mut pipe) = child.stdout.take() {
        let _ = pipe.read_to_string(&mut stdout);
    }
    Bounded::Done {
        success: status.success(),
        stdout,
    }
}

#[cfg(not(target_os = "linux"))]
fn host_check() -> Host {
    Host::Present
}

/// How long a hidden start at login waits for the desktop's tray host, which
/// may start after the app does.
const HOST_WAIT: Duration = Duration::from_secs(10);

struct Ui {
    tray: Option<TrayIcon>,
    /// The window menu's start at login item, kept in step with the tray's.
    menu_login: Option<CheckMenuItem<Wry>>,
}

#[derive(Default)]
struct Inner {
    /// A tray icon was created; whether the desktop shows it is asked again
    /// when it matters.
    has_tray: AtomicBool,
    /// Counts attachments, so diagnostics run again for a new daemon.
    generation: AtomicU64,
    launch: Mutex<Option<Launch>>,
    observed: Mutex<Observed>,
    tracker: Mutex<Tracker>,
    /// The view last shown and its "resets in" texts, which change with the
    /// clock as well as the view.
    rendered: Mutex<Option<(View, Vec<String>)>>,
    ui: Mutex<Option<Ui>>,
    wake: Mutex<Option<mpsc::Sender<()>>>,
    /// The daemon's event stream is open.
    stream_up: AtomicBool,
    /// Wakes the stream thread (waiting for a daemon or to reconnect).
    stream_kick: Mutex<Option<mpsc::Sender<()>>>,
    /// Wakes the diagnostics and plan usage worker.
    periodic_kick: Mutex<Option<mpsc::Sender<()>>>,
}

#[derive(Clone, Default)]
pub struct Shared(Arc<Inner>);

fn lock<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(|p| p.into_inner())
}

fn shared(app: &AppHandle) -> Option<Shared> {
    app.try_state::<Shared>().map(|s| s.inner().clone())
}

/// Whether a tray icon is showing, so the window may hide into it. The
/// desktop is asked every time, never remembered: the host can appear after
/// the app, and it can go away (the panel exits, an extension is disabled),
/// after which hiding the window would leave nothing to reopen it.
pub fn available(app: &AppHandle) -> bool {
    let Some(shared) = shared(app) else {
        return false;
    };
    tray_usable(shared.0.has_tray.load(Ordering::Relaxed), host_check())
}

/// The decision: a tray was created and the desktop shows it now.
fn tray_usable(has_tray: bool, host: Host) -> bool {
    has_tray && host == Host::Present
}

/// Creates the tray and the window menu, and starts the poll threads. Runs on
/// the main thread, from `setup`.
pub fn setup(app: &AppHandle) {
    let shared = Shared::default();
    app.manage(shared.clone());
    autostart::refresh();
    let login = autostart::enabled();
    // The only menu listener: Tauri gives it the events of every menu, the
    // window's and the tray's, so no menu registers a second one (a second
    // would run each click twice, and a toggle would flip back).
    app.on_menu_event(|app, event| on_menu(app, event.id().as_ref()));
    // Only macOS gets a menu bar, its global one; on Linux and Windows the
    // window has none, and the tray and the app hold these choices.
    let menu_login = if cfg!(target_os = "macos") {
        match install_window_menu(app, login) {
            Ok(item) => Some(item),
            Err(e) => {
                eprintln!("baum desktop: no window menu: {e}");
                None
            }
        }
    } else {
        None
    };
    // A missing libayatana-appindicator panics in the tray library; the app
    // runs on without a tray instead of crashing.
    let tray =
        match std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| build_tray(app, login))) {
            Ok(Ok(tray)) => Some(tray),
            Ok(Err(e)) => {
                eprintln!("baum desktop: no tray icon: {e}");
                None
            }
            Err(_) => {
                eprintln!("baum desktop: no tray icon: the tray library failed to load");
                None
            }
        };
    shared.0.has_tray.store(tray.is_some(), Ordering::Relaxed);
    *lock(&shared.0.ui) = Some(Ui { tray, menu_login });
    spawn_polls(app, &shared);
}

/// Once the window exists: waits (on a hidden start at login, up to
/// `HOST_WAIT`) for the desktop to show tray icons, and shows the window when
/// none will, since nothing else could.
pub fn watch_host(app: &AppHandle, hidden: bool) {
    let app = app.clone();
    thread::spawn(move || {
        let deadline = Instant::now() + if hidden { HOST_WAIT } else { Duration::ZERO };
        while !available(&app) {
            if Instant::now() >= deadline {
                eprintln!("baum desktop: this desktop shows no tray icons");
                if hidden {
                    lifecycle::show_window(&app);
                }
                return;
            }
            thread::sleep(Duration::from_secs(1));
        }
    });
}

/// The Background submenu: start at login, with its item. Self-contained so
/// the menu bar can assemble it with other submenus.
pub fn background_submenu(
    app: &AppHandle,
    login: bool,
) -> tauri::Result<(Submenu<Wry>, CheckMenuItem<Wry>)> {
    let item =
        CheckMenuItem::with_id(app, MENU_LOGIN, "Start at login", true, login, None::<&str>)?;
    let submenu = Submenu::with_items(app, "Background", true, &[&item])?;
    Ok((submenu, item))
}

/// Sets the window's menu bar: the platform default plus Background and
/// Notifications, and "Command Line and Hooks…" in the application menu on
/// macOS, in Background elsewhere.
fn install_window_menu(app: &AppHandle, login: bool) -> Result<CheckMenuItem<Wry>, String> {
    let e = |e: tauri::Error| e.to_string();
    let (background, item) = background_submenu(app, login).map_err(e)?;
    let window_menu = Menu::default(app).map_err(e)?;
    let setup = onboarding::menu_item(app).map_err(e)?;
    let application = window_menu
        .items()
        .map_err(e)?
        .into_iter()
        .next()
        .and_then(|i| i.as_submenu().cloned());
    match application {
        // After "About baum" and its separator.
        Some(application) if cfg!(target_os = "macos") => {
            application.insert(&setup, 2).map_err(e)?;
            application
                .insert(&PredefinedMenuItem::separator(app).map_err(e)?, 3)
                .map_err(e)?;
        }
        _ => background.append(&setup).map_err(e)?,
    }
    window_menu.append(&background).map_err(e)?;
    let report =
        MenuItem::with_id(app, MENU_REPORT, "Report a problem…", true, None::<&str>).map_err(e)?;
    let help = window_menu
        .items()
        .map_err(e)?
        .into_iter()
        .filter_map(|i| i.as_submenu().cloned())
        .find(|s| s.text().is_ok_and(|t| t == "Help"));
    match help {
        Some(help) => help.append(&report).map_err(e)?,
        None => window_menu
            .append(&Submenu::with_items(app, "Help", true, &[&report]).map_err(e)?)
            .map_err(e)?,
    }
    if let Some(notifications) = notifications::submenu(app).map_err(e)? {
        window_menu.append(&notifications).map_err(e)?;
    }
    app.set_menu(window_menu).map_err(e)?;
    Ok(item)
}

fn build_tray(app: &AppHandle, login: bool) -> Result<TrayIcon, String> {
    let e = |e: tauri::Error| e.to_string();
    let view = View::default();
    TrayIconBuilder::with_id(TRAY_ID)
        .icon(icon(Icon::Idle)?)
        .tooltip(view.tooltip())
        .menu(&build_menu(app, &menu_spec(&view, login, now())).map_err(e)?)
        // A left click opens the panel and the right one keeps the menu. Linux
        // hosts report no clicks and show the menu on either.
        .show_menu_on_left_click(false)
        .on_tray_icon_event(|tray, event| {
            if let tauri::tray::TrayIconEvent::Click {
                button: tauri::tray::MouseButton::Left,
                button_state: tauri::tray::MouseButtonState::Up,
                rect,
                ..
            } = event
            {
                panel::toggle(tray.app_handle(), rect);
            }
        })
        .build(app)
        .map_err(e)
}

fn icon(state: Icon) -> Result<Image<'static>, String> {
    let bytes = match state {
        Icon::Idle => IDLE_ICON,
        Icon::Running => RUNNING_ICON,
        Icon::Attention => ATTENTION_ICON,
    };
    Image::from_bytes(bytes).map_err(|e| e.to_string())
}

fn build_menu(app: &AppHandle, spec: &[Entry]) -> tauri::Result<Menu<Wry>> {
    let mut items: Vec<Box<dyn IsMenuItem<Wry>>> = Vec::new();
    for entry in spec {
        items.push(match &entry.kind {
            Kind::Item => Box::new(MenuItem::with_id(
                app,
                entry.id.as_str(),
                &entry.text,
                entry.enabled,
                None::<&str>,
            )?),
            Kind::Check(checked) => Box::new(CheckMenuItem::with_id(
                app,
                entry.id.as_str(),
                &entry.text,
                entry.enabled,
                *checked,
                None::<&str>,
            )?),
            Kind::Separator => Box::new(PredefinedMenuItem::separator(app)?),
        });
    }
    // Notifications go before the last separator, above Quit. The submenu is
    // built afresh each time, from the notifier's settings, which are the one
    // source of truth for both menus.
    if let Some(notifications) = notifications::submenu(app)? {
        let at = spec
            .iter()
            .position(|entry| entry.id == "separator-2")
            .unwrap_or(items.len());
        items.insert(at, Box::new(notifications));
    }
    let refs: Vec<&dyn IsMenuItem<Wry>> = items.iter().map(|i| i.as_ref()).collect();
    Menu::with_items(app, &refs)
}

/// What a menu id does. Ids are unique across the window and tray menus,
/// except the notification toggles, which both hold and which mean the same.
#[derive(Debug, PartialEq, Eq)]
enum Action {
    Open,
    /// The status line while reviews run or wait: opens the jobs list.
    Jobs,
    /// An alert, by its menu id: goes where to look, and records it seen.
    Alert(String),
    /// A waiting agent session, by its `agent:id` key: opens its transcript.
    Session(String),
    Pause,
    Login,
    /// "Command Line and Hooks…", in the window's menu bar.
    Setup,
    /// "Report a problem…": opens a prefilled GitHub issue in the browser.
    Report,
    Quit,
    QuitStop,
    Notification,
    Ignore,
}

fn route(id: &str) -> Action {
    match id {
        "open" => Action::Open,
        "summary" => Action::Jobs,
        "pause" => Action::Pause,
        "login" | MENU_LOGIN => Action::Login,
        onboarding::MENU_ID => Action::Setup,
        "report" | MENU_REPORT => Action::Report,
        "quit" => Action::Quit,
        "quit-stop" => Action::QuitStop,
        _ if notifications::is_menu_id(id) => Action::Notification,
        _ if status::is_alert(id) => Action::Alert(id.to_string()),
        _ => match id.strip_prefix(SESSION_PREFIX) {
            Some(key) if !key.is_empty() => Action::Session(key.to_string()),
            _ => Action::Ignore,
        },
    }
}

pub fn on_menu(app: &AppHandle, id: &str) {
    match route(id) {
        Action::Open => lifecycle::show_window(app),
        Action::Jobs => notifications::open(app, Route::Jobs),
        Action::Alert(id) => follow_alert(app, &id),
        Action::Session(key) => notifications::open_session(app, key),
        Action::Pause => toggle_pause(app),
        Action::Login => toggle_login(app),
        Action::Setup => onboarding::show(app),
        Action::Report => report_problem(app),
        Action::Quit => lifecycle::quit(app),
        Action::QuitStop => lifecycle::quit_and_stop(app),
        Action::Notification => {
            notifications::on_menu(app, id);
        }
        Action::Ignore => {}
    }
}

/// Rebuilds the tray menu from the current settings, after a window menu
/// toggle changed one.
pub fn refresh(app: &AppHandle) {
    if let Some(shared) = shared(app) {
        render(app, &shared, true);
    }
}

/// The prefilled issue link in a `/api/diagnostics` answer, only when it opens
/// the public repository's new issue form.
fn issue_url(body: &[u8]) -> Option<String> {
    let value: serde_json::Value = serde_json::from_slice(body).ok()?;
    let url = value.get("report_issue_url")?.as_str()?;
    url.starts_with(ISSUES_NEW_PREFIX).then(|| url.to_string())
}

/// Opens a new GitHub issue in the browser with the version, OS and the
/// daemon's redacted Diagnostics summary filled in (the daemon builds the
/// link, so one redaction serves every surface). The user reads the form in the
/// browser and nothing is sent until they submit it. Without a daemon, or when
/// diagnostics fail, it opens the issue chooser instead. The checks can take
/// seconds, so they run off the menu's thread.
fn report_problem(app: &AppHandle) {
    let launch = launch(app);
    let app = app.clone();
    thread::spawn(move || {
        let prefilled = launch.and_then(|launch| match client::diagnostics(&launch) {
            Ok(body) => issue_url(&body),
            Err(e) => {
                eprintln!("baum desktop: no diagnostics for the problem report: {e}");
                None
            }
        });
        let url = prefilled.as_deref().unwrap_or(ISSUES_CHOOSE);
        if let Err(e) = app.opener().open_url(url, None::<&str>) {
            lifecycle::message(&app, "Cannot open the browser", &e.to_string());
        }
    });
}

/// Flips start at login and shows the real result in both menus.
fn toggle_login(app: &AppHandle) {
    let want = !autostart::enabled();
    if let Err(e) = autostart::set(want) {
        lifecycle::message(app, "Cannot change start at login", &e);
    }
    if let Some(shared) = shared(app) {
        render(app, &shared, true);
    }
}

/// Pauses or resumes automatic generation: the daemon's global switch.
fn toggle_pause(app: &AppHandle) {
    let Some(shared) = shared(app) else { return };
    let app = app.clone();
    thread::spawn(move || {
        let launch = lock(&shared.0.launch).clone();
        let current = lock(&shared.0.observed)
            .automatic
            .as_ref()
            .map(|a| a.enabled);
        let (Some(launch), Some(enabled)) = (launch, status::toggled(current)) else {
            return;
        };
        match client::set_automatic(&launch, enabled) {
            Ok(()) => wake(&shared),
            Err(e) => lifecycle::message(&app, "Cannot change automatic reviews", &e),
        }
    });
}

/// The attached daemon's launch credential, while one is attached.
pub fn launch(app: &AppHandle) -> Option<Launch> {
    let shared = shared(app)?;
    lock(&shared.0.launch).clone()
}

/// `main` attached to a daemon.
pub fn attached(app: &AppHandle, launch: &Launch) {
    let Some(shared) = shared(app) else { return };
    *lock(&shared.0.launch) = Some(launch.clone());
    {
        // A new attachment may be a replacement daemon: what the last one
        // said is stale.
        let mut observed = lock(&shared.0.observed);
        *observed = Observed {
            link: Link::Up,
            ..Observed::default()
        };
    }
    shared.0.generation.fetch_add(1, Ordering::Relaxed);
    render(app, &shared, false);
    for kick in [&shared.0.stream_kick, &shared.0.periodic_kick] {
        if let Some(sender) = lock(kick).as_ref() {
            let _ = sender.send(());
        }
    }
    wake(&shared);
}

/// `main` lost the daemon or failed to reach one.
pub fn detached(app: &AppHandle) {
    set_link(app, Link::Down);
}

/// `main` is attaching.
pub fn connecting(app: &AppHandle) {
    set_link(app, Link::Connecting);
}

fn set_link(app: &AppHandle, link: Link) {
    let Some(shared) = shared(app) else { return };
    *lock(&shared.0.launch) = None;
    lock(&shared.0.observed).link = link;
    render(app, &shared, false);
}

/// The window is showing: the diagnostics problems flagged have been seen.
/// Failed jobs stay flagged until their alert is followed.
pub fn window_seen(app: &AppHandle) {
    let Some(shared) = shared(app) else { return };
    lock(&shared.0.tracker).acknowledge();
    render(app, &shared, false);
}

/// An alert was followed: records what it is about as seen, then goes where
/// the user needs to look.
fn follow_alert(app: &AppHandle, id: &str) {
    let Some(shared) = shared(app) else { return };
    let jobs = lock(&shared.0.tracker).acknowledge_alert(id);
    record_seen(&shared, jobs);
    render(app, &shared, false);
    let route = if let Some(job) = id.strip_prefix(status::ALERT_JOB) {
        Route::Job { id: job.into() }
    } else {
        match id {
            status::ALERT_JOBS => Route::Jobs,
            status::ALERT_DIAGNOSTICS => Route::Diagnostics,
            // The window shows the connecting screen, which can restart the
            // daemon.
            _ => return lifecycle::show_window(app),
        }
    };
    notifications::open(app, route);
}

/// Records failed jobs as seen in the daemon, so no client flags them
/// again, and looks again once it has.
fn record_seen(shared: &Shared, jobs: Vec<String>) {
    if jobs.is_empty() {
        return;
    }
    let Some(launch) = lock(&shared.0.launch).clone() else {
        return;
    };
    let shared = shared.clone();
    thread::spawn(move || {
        for id in &jobs {
            if let Err(e) = client::job_seen(&launch, id) {
                eprintln!("baum desktop: cannot record job {id} as seen: {e}");
            }
        }
        wake(&shared);
    });
}

/// What the panel shows: the view last shown and the plans behind its meters.
pub fn snapshot(app: &AppHandle) -> Option<(View, Vec<Plan>)> {
    let shared = shared(app)?;
    let (view, _) = lock(&shared.0.rendered).clone()?;
    // The view already left the figures out when the daemon is not up.
    let plans = if view.usage.is_empty() {
        Vec::new()
    } else {
        lock(&shared.0.observed).plans.clone().unwrap_or_default()
    };
    Some((view, plans))
}

/// Running and queued jobs, asked of the daemon now. `None` when it cannot be
/// asked: the last poll may be seconds old, so it is not a substitute.
pub fn job_counts(app: &AppHandle) -> Option<(usize, usize)> {
    let shared = shared(app)?;
    let launch = lock(&shared.0.launch).clone()?;
    let jobs = client::jobs(&launch)
        .and_then(|b| status::parse_jobs(&b))
        .ok()?;
    Some((jobs.running, jobs.queued))
}

fn wake(shared: &Shared) {
    if let Some(sender) = lock(&shared.0.wake).as_ref() {
        let _ = sender.send(());
    }
}

/// Whether a computed view needs showing, and records it as shown.
fn next_view<T: PartialEq + Clone>(rendered: &mut Option<T>, view: T, force: bool) -> Option<T> {
    if !force && rendered.as_ref() == Some(&view) {
        return None;
    }
    *rendered = Some(view.clone());
    Some(view)
}

/// The plan usage lines as the menu would show them now. Part of what counts
/// as a change, so the reads (at least a minute apart) keep "resets in" current.
fn usage_texts(view: &View, now: i64) -> Vec<String> {
    view.usage.iter().map(|meter| meter.text(now)).collect()
}

/// Recomputes the view and queues the icon, tooltip and menu update when it
/// changed (or `force`, after start at login changed).
///
/// The Tauri menu and tray calls wait for the main thread, and several
/// callers of this function are the main thread (focus, Open, menu clicks).
/// So no lock is ever held across such a call: the update is queued with the
/// non-blocking `run_on_main_thread`, from under the `rendered` lock only so
/// that updates reach the main thread in the order they were computed, and
/// `apply` runs with no lock at all.
fn render(app: &AppHandle, shared: &Shared, force: bool) {
    let observed = lock(&shared.0.observed).clone();
    let view = lock(&shared.0.tracker).update(&observed);
    let login = autostart::enabled();
    let ui = lock(&shared.0.ui)
        .as_ref()
        .map(|ui| (ui.tray.clone(), ui.menu_login.clone()));
    let texts = usage_texts(&view, now());
    let mut rendered = lock(&shared.0.rendered);
    let Some((view, _)) = next_view(&mut rendered, (view, texts), force) else {
        return;
    };
    let Some((tray, menu_login)) = ui else {
        return;
    };
    let handle = app.clone();
    let _ = app.run_on_main_thread(move || apply(&handle, tray, menu_login, &view, login));
}

/// Updates the tray and menu items. Runs on the main thread, holding no lock.
fn apply(
    app: &AppHandle,
    tray: Option<TrayIcon>,
    menu_login: Option<CheckMenuItem<Wry>>,
    view: &View,
    login: bool,
) {
    if let Some(item) = menu_login {
        let _ = item.set_checked(login);
    }
    // The dock badge (macOS; the taskbar badge where the platform has one) is
    // the window's, not the tray's. Platforms without one answer with an
    // error, which is no reason to say anything.
    if let Some(window) = app.get_webview_window(crate::MAIN) {
        let _ = window.set_badge_count(view.badge());
    }
    let Some(tray) = tray else { return };
    if let Ok(image) = icon(view.icon) {
        let _ = tray.set_icon(Some(image));
    }
    let _ = tray.set_tooltip(Some(view.tooltip()));
    if let Ok(menu) = build_menu(app, &menu_spec(view, login, now())) {
        let _ = tray.set_menu(Some(menu));
    }
}

/// Event kinds after which the tray's numbers may have changed: job state
/// (running, queued, failures), the pause switch, sessions waiting on the
/// user, the waiting pull request count, and a missed replay window. A `job`
/// event, a running job's usage, changes none of them.
fn wakes_poll(kind: &str) -> bool {
    matches!(kind, "progress" | "automatic" | "live" | "index" | "reset")
}

/// The wait before reconnecting after the `attempt`th failure in a row.
fn backoff(attempt: u32) -> Duration {
    STREAM_BACKOFF_FIRST
        .saturating_mul(1 << attempt.min(8))
        .min(STREAM_BACKOFF_MAX)
}

/// How long the poll thread waits for an event before looking anyway: not at
/// all without a daemon, rarely while the stream delivers events.
fn fallback_interval(attached: bool, stream_up: bool) -> Option<Duration> {
    match (attached, stream_up) {
        (false, _) => None,
        (true, true) => Some(FALLBACK_STREAM_UP),
        (true, false) => Some(FALLBACK_STREAM_DOWN),
    }
}

/// Time left until a periodic read is due: none is due yet for a daemon
/// not read from, or one replaced since.
fn until_due(
    checked: Option<(u64, Instant)>,
    generation: u64,
    interval: Duration,
    now: Instant,
) -> Duration {
    match checked {
        Some((g, at)) if g == generation => {
            interval.saturating_sub(now.saturating_duration_since(at))
        }
        _ => Duration::ZERO,
    }
}

/// After a wake, waits for the burst of events around it to end (a quiet
/// `DEBOUNCE`, or `DEBOUNCE_MAX` in all). False when the poll thread should
/// stop.
fn settle(receiver: &mpsc::Receiver<()>) -> bool {
    let start = Instant::now();
    loop {
        let left = DEBOUNCE_MAX.saturating_sub(start.elapsed());
        if left.is_zero() {
            return true;
        }
        match receiver.recv_timeout(left.min(DEBOUNCE)) {
            Ok(()) => {}
            Err(RecvTimeoutError::Timeout) => return true,
            Err(RecvTimeoutError::Disconnected) => return false,
        }
    }
}

fn spawn_polls(app: &AppHandle, shared: &Shared) {
    let (sender, receiver) = mpsc::channel();
    // The first pass reads whatever is attached already.
    let _ = sender.send(());
    *lock(&shared.0.wake) = Some(sender);
    let (poll_app, poll_shared) = (app.clone(), shared.clone());
    thread::spawn(move || {
        loop {
            let attached = lock(&poll_shared.0.launch).is_some();
            let stream_up = poll_shared.0.stream_up.load(Ordering::Relaxed);
            let woke = match fallback_interval(attached, stream_up) {
                Some(interval) => match receiver.recv_timeout(interval) {
                    Ok(()) => true,
                    Err(RecvTimeoutError::Timeout) => false,
                    Err(RecvTimeoutError::Disconnected) => return,
                },
                None => match receiver.recv() {
                    Ok(()) => true,
                    Err(_) => return,
                },
            };
            if woke && !settle(&receiver) {
                return;
            }
            poll_once(&poll_app, &poll_shared);
        }
    });

    let (stream_sender, stream_receiver) = mpsc::channel();
    *lock(&shared.0.stream_kick) = Some(stream_sender);
    let stream_shared = shared.clone();
    thread::spawn(move || stream_loop(&stream_shared, &stream_receiver));

    let (kick_sender, kick_receiver) = mpsc::channel();
    *lock(&shared.0.periodic_kick) = Some(kick_sender);
    let (periodic_app, periodic_shared) = (app.clone(), shared.clone());
    thread::spawn(move || periodic_loop(&periodic_app, &periodic_shared, &kick_receiver));
}

/// Keeps the daemon's event stream open while a daemon is attached, waking
/// the poll thread on events that change what the tray shows. It reconnects
/// with backoff, and a connection (also a re-connection) wakes a read so
/// nothing missed meanwhile stays missed.
fn stream_loop(shared: &Shared, kick: &mpsc::Receiver<()>) {
    let mut attempt = 0u32;
    loop {
        let Some(launch) = lock(&shared.0.launch).clone() else {
            attempt = 0;
            if kick.recv().is_err() {
                return;
            }
            continue;
        };
        let generation = shared.0.generation.load(Ordering::Relaxed);
        let result = client::stream_events(&launch, client::STREAM_READ_TIMEOUT, |item| {
            match item {
                client::StreamItem::Open => {
                    attempt = 0;
                    shared.0.stream_up.store(true, Ordering::Relaxed);
                    wake(shared);
                }
                client::StreamItem::Event(kind) => {
                    if wakes_poll(&kind) {
                        wake(shared);
                    }
                }
                client::StreamItem::Ping => {}
            }
            // Stop for a detached or replaced daemon, at the next ping.
            shared.0.generation.load(Ordering::Relaxed) == generation
                && lock(&shared.0.launch).is_some()
        });
        if shared.0.stream_up.swap(false, Ordering::Relaxed) {
            // The fallback interval just shortened: look now.
            wake(shared);
        }
        if let Err(e) = result
            && attempt > 0
        {
            eprintln!("baum desktop: event stream unavailable: {e}");
        }
        if shared.0.generation.load(Ordering::Relaxed) != generation {
            attempt = 0;
            continue;
        }
        let wait = backoff(attempt);
        attempt = attempt.saturating_add(1);
        if let Err(RecvTimeoutError::Disconnected) = kick.recv_timeout(wait) {
            return;
        }
    }
}

/// Reads what no event announces: diagnostics and plan usage, each every
/// five minutes and again for a new daemon. Sleeps until the next is due,
/// and with no daemon until one is attached.
fn periodic_loop(app: &AppHandle, shared: &Shared, kick: &mpsc::Receiver<()>) {
    let mut diagnostics: Option<(u64, Instant)> = None;
    let mut plans: Option<(u64, Instant)> = None;
    loop {
        let generation = shared.0.generation.load(Ordering::Relaxed);
        let launch = lock(&shared.0.launch).clone();
        let Some(launch) = launch else {
            if kick.recv().is_err() {
                return;
            }
            continue;
        };
        let now = Instant::now();
        let plan_wait = until_due(plans, generation, PLAN_USAGE_INTERVAL, now);
        let diagnostics_wait = until_due(diagnostics, generation, DIAGNOSTICS_INTERVAL, now);
        let wait = plan_wait.min(diagnostics_wait);
        if !wait.is_zero() {
            if let Err(RecvTimeoutError::Disconnected) = kick.recv_timeout(wait) {
                return;
            }
            continue;
        }
        if plan_wait.is_zero() {
            plans = Some((generation, Instant::now()));
            // The figures are a convenience: a failed reading keeps the last
            // one, and a daemon without the route has none to show.
            if let Ok(found) =
                client::plan_usage(&launch).and_then(|b| status::parse_plan_usage(&b))
            {
                lock(&shared.0.observed).plans = Some(found);
                render(app, shared, false);
            }
        }
        if diagnostics_wait.is_zero() {
            diagnostics = Some((generation, Instant::now()));
            // A failed or timed-out run leaves the last answer standing.
            if let Ok(problems) =
                client::diagnostics(&launch).and_then(|b| status::parse_diagnostics(&b))
            {
                lock(&shared.0.observed).diagnostics = Some(problems);
                render(app, shared, false);
            }
        }
    }
}

/// One look at the daemon. A failed request forgets that answer, so the menu
/// does not show counts it cannot vouch for; the connection watch in `main`
/// decides when the daemon has gone.
fn poll_once(app: &AppHandle, shared: &Shared) {
    let launch = lock(&shared.0.launch).clone();
    let Some(launch) = launch else {
        return;
    };
    let jobs = client::jobs(&launch).and_then(|b| status::parse_jobs(&b));
    let automatic = client::automatic(&launch).and_then(|b| status::parse_automatic(&b));
    let waiting = client::waiting(&launch).and_then(|b| status::parse_waiting(&b));
    let needs = client::live_sessions(&launch).and_then(|b| status::parse_live(&b));
    {
        let mut observed = lock(&shared.0.observed);
        observed.jobs = jobs.ok();
        observed.automatic = automatic.ok().flatten();
        observed.waiting = waiting.ok();
        observed.needs = needs.ok();
    }
    render(app, shared, false);
    // A failed job seen here but still unseen in the daemon: recording it
    // failed (the daemon was briefly unreachable, or could not save), so it
    // is recorded again until the daemon stops reporting it, rather than
    // hidden only until the app restarts.
    let unrecorded = lock(&shared.0.tracker).unrecorded();
    for id in unrecorded {
        if let Err(e) = client::job_seen(&launch, &id) {
            eprintln!("baum desktop: cannot record job {id} as seen: {e}");
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::status::{Automatic, Jobs};

    #[test]
    fn only_state_events_wake_the_poll() {
        for kind in ["progress", "automatic", "live", "index", "reset"] {
            assert!(wakes_poll(kind), "{kind}");
        }
        for kind in ["job", "chat", "timeline", "decisions", "notice", ""] {
            assert!(!wakes_poll(kind), "{kind}");
        }
    }

    #[test]
    fn reconnects_back_off_to_a_cap() {
        let waits: Vec<u64> = (0..8).map(|a| backoff(a).as_secs()).collect();
        assert_eq!(waits, [1, 2, 4, 8, 16, 30, 30, 30]);
        assert_eq!(backoff(u32::MAX), STREAM_BACKOFF_MAX);
    }

    #[test]
    fn the_fallback_poll_is_slow_with_a_stream_and_absent_without_a_daemon() {
        assert_eq!(fallback_interval(false, true), None);
        assert_eq!(fallback_interval(false, false), None);
        assert_eq!(fallback_interval(true, true), Some(Duration::from_secs(60)));
        assert_eq!(
            fallback_interval(true, false),
            Some(Duration::from_secs(30))
        );
    }

    #[test]
    fn a_periodic_read_is_due_at_once_for_a_new_daemon_and_then_after_its_interval() {
        let now = Instant::now();
        let hour = Duration::from_secs(3600);
        let interval = Duration::from_secs(300);
        assert_eq!(until_due(None, 1, interval, now), Duration::ZERO);
        let read = Some((1, now));
        assert_eq!(until_due(read, 2, interval, now + hour), Duration::ZERO);
        assert_eq!(
            until_due(read, 1, interval, now + Duration::from_secs(100)),
            Duration::from_secs(200)
        );
        assert_eq!(until_due(read, 1, interval, now + hour), Duration::ZERO);
    }

    #[test]
    fn a_burst_of_wakes_settles_into_one_read() {
        let (sender, receiver) = mpsc::channel();
        for _ in 0..5 {
            sender.send(()).unwrap();
        }
        let start = Instant::now();
        assert!(settle(&receiver));
        assert!(start.elapsed() < DEBOUNCE_MAX);
        assert!(receiver.try_recv().is_err(), "the burst was drained");
        drop(sender);
        assert!(!settle(&receiver));
    }

    fn view(observed: Observed) -> View {
        Tracker::default().update(&observed)
    }

    fn up(running: usize, automatic: bool) -> Observed {
        Observed {
            link: Link::Up,
            jobs: Some(Jobs {
                running,
                ..Jobs::default()
            }),
            automatic: Some(Automatic { enabled: automatic }),
            diagnostics: Some(Vec::new()),
            waiting: None,
            plans: None,
            needs: None,
        }
    }

    const NOW: i64 = 1_793_016_000;

    fn texts(spec: &[Entry]) -> Vec<&str> {
        spec.iter()
            .filter(|e| e.kind != Kind::Separator)
            .map(|e| e.text.as_str())
            .collect()
    }

    #[test]
    fn every_menu_id_has_exactly_one_action() {
        use crate::notifications;
        let ids = menu_spec(&View::default(), false, NOW)
            .into_iter()
            .map(|e| e.id)
            .chain([
                MENU_LOGIN.to_string(),
                MENU_REPORT.to_string(),
                onboarding::MENU_ID.to_string(),
            ])
            .chain(notifications::menu_ids().map(String::from));
        for id in ids {
            let action = route(&id);
            let expected = match id.as_str() {
                "open" => Action::Open,
                "summary" => Action::Jobs,
                _ if id.starts_with(SESSION_PREFIX) => Action::Session(id[8..].to_string()),
                "pause" => Action::Pause,
                "login" | MENU_LOGIN => Action::Login,
                onboarding::MENU_ID => Action::Setup,
                "report" | MENU_REPORT => Action::Report,
                "quit" => Action::Quit,
                "quit-stop" => Action::QuitStop,
                _ if id.starts_with("notifications:") => Action::Notification,
                _ => Action::Ignore,
            };
            assert_eq!(action, expected, "{id}");
        }
        assert_eq!(route("unknown"), Action::Ignore);
        assert_eq!(route("setup"), Action::Ignore);
        assert_eq!(route("session:"), Action::Ignore);
        assert_eq!(
            route("session:claude-code:a"),
            Action::Session("claude-code:a".into())
        );
    }

    /// Tauri hands every menu's events to the app listener, so a listener on
    /// the tray or a menu as well runs each click twice.
    #[test]
    fn one_menu_listener_is_registered() {
        let source = include_str!("tray.rs");
        let code = &source[..source.find("#[cfg(test)]").unwrap()];
        let calls = code
            .lines()
            .filter(|l| !l.trim_start().starts_with("//"))
            .filter(|l| l.contains(".on_menu_event("))
            .count();
        assert_eq!(calls, 1);
    }

    #[test]
    fn the_menu_has_the_decided_entries_in_order() {
        let spec = menu_spec(&view(up(2, true)), false, NOW);
        assert_eq!(
            texts(&spec),
            vec![
                "2 running",
                "Open baum",
                "Pause automatic reviews",
                "Start at login",
                "Report a problem…",
                "Quit",
                "Quit and stop background reviews"
            ]
        );
        let ids: Vec<&str> = spec.iter().map(|e| e.id.as_str()).collect();
        assert_eq!(
            ids,
            vec![
                "summary",
                "separator-1",
                "open",
                "pause",
                "login",
                "report",
                "separator-2",
                "quit",
                "quit-stop"
            ]
        );
    }

    #[test]
    fn the_status_opens_the_jobs_list_only_while_reviews_run_or_wait() {
        let idle = menu_spec(&view(up(0, true)), false, NOW);
        assert_eq!((idle[0].id.as_str(), idle[0].enabled), ("summary", false));
        let busy = menu_spec(&view(up(2, true)), false, NOW);
        assert_eq!(busy[0].text, "2 running");
        assert!(busy[0].enabled);
        assert_eq!(route(&busy[0].id), Action::Jobs);
        let mut queued = up(0, true);
        queued.jobs = Some(Jobs {
            queued: 1,
            ..Jobs::default()
        });
        assert!(menu_spec(&view(queued), false, NOW)[0].enabled);
    }

    #[test]
    fn only_the_issue_form_of_the_public_repository_is_opened() {
        let ok = br#"{"report_issue_url":"https://github.com/baum-ai/baum/issues/new?template=bug_report.yml&version=1"}"#;
        assert_eq!(
            issue_url(ok).as_deref(),
            Some("https://github.com/baum-ai/baum/issues/new?template=bug_report.yml&version=1")
        );
        for body in [
            &br#"{}"#[..],
            br#"not json"#,
            br#"{"report_issue_url":5}"#,
            br#"{"report_issue_url":"https://evil.example/issues/new?x=1"}"#,
            br#"{"report_issue_url":"https://github.com/baum-ai/baum.evil/issues/new?x=1"}"#,
            br#"{"report_issue_url":"http://github.com/baum-ai/baum/issues/new?x=1"}"#,
            br#"{"report_issue_url":"file:///etc/passwd"}"#,
        ] {
            assert_eq!(issue_url(body), None, "{}", String::from_utf8_lossy(body));
        }
    }

    #[test]
    fn the_menu_says_how_many_pull_requests_wait_only_when_some_do() {
        let mut observed = up(0, true);
        observed.waiting = Some(3);
        let spec = menu_spec(&view(observed.clone()), false, NOW);
        assert_eq!(spec[0].text, "Idle");
        assert_eq!(spec[1].id, "waiting");
        assert_eq!(spec[1].text, "3 pull requests in your Inbox");
        assert!(!spec[1].enabled);
        for waiting in [Some(0), None] {
            observed.waiting = waiting;
            let spec = menu_spec(&view(observed.clone()), false, NOW);
            assert!(spec.iter().all(|e| e.id != "waiting"));
        }
    }

    #[test]
    fn the_menu_offers_resume_while_paused_and_disables_pause_while_unknown() {
        let paused = menu_spec(&view(up(0, false)), false, NOW);
        let pause = paused.iter().find(|e| e.id == "pause").unwrap();
        assert_eq!(pause.text, "Resume automatic reviews");
        assert!(pause.enabled);
        let unknown = menu_spec(&View::default(), false, NOW);
        let pause = unknown.iter().find(|e| e.id == "pause").unwrap();
        assert!(!pause.enabled);
    }

    #[test]
    fn the_start_at_login_item_shows_its_state() {
        for login in [false, true] {
            let spec = menu_spec(&View::default(), login, NOW);
            let item = spec.iter().find(|e| e.id == "login").unwrap();
            assert_eq!(item.kind, Kind::Check(login));
        }
    }

    #[test]
    fn alerts_are_items_under_the_summary_that_go_where_to_look() {
        let mut observed = up(0, true);
        observed.diagnostics = Some(vec!["git".into()]);
        let spec = menu_spec(&view(observed), false, NOW);
        assert_eq!(spec[0].text, "Idle");
        assert_eq!(
            spec[1].text,
            "A diagnostics check reports a problem · Open diagnostics"
        );
        assert!(spec[1].enabled);
        assert_eq!(
            route(&spec[1].id),
            Action::Alert("alert:diagnostics".into())
        );
        let down = view(Observed {
            link: Link::Down,
            ..Observed::default()
        });
        let spec = menu_spec(&down, false, NOW);
        assert_eq!(
            spec[1].text,
            "Background reviews are not running · Open baum to restart them"
        );
        assert_eq!(route(&spec[1].id), Action::Alert("alert:daemon".into()));
        assert_eq!(
            route("alert:job:6e280a68"),
            Action::Alert("alert:job:6e280a68".into())
        );
    }

    #[test]
    fn the_menu_is_shown_again_when_a_reset_time_reads_differently() {
        let mut observed = up(0, true);
        observed.plans = Some(
            status::parse_plan_usage(
                br#"{"plans":[{"provider":"claude","available":true,"windows":[
                    {"name":"5-hour","used_percent":38,"resets_at":"2026-10-26T12:30:00Z"}],
                    "fetched_at":"2026-10-26T12:00:00Z"}]}"#,
            )
            .unwrap(),
        );
        let view = view(observed);
        let mut rendered = None;
        let at = |now| (view.clone(), usage_texts(&view, now));
        assert!(next_view(&mut rendered, at(NOW), false).is_some());
        // Within the same minute nothing changes; a minute on, "30 min" is "29 min".
        assert!(next_view(&mut rendered, at(NOW + 10), false).is_none());
        assert!(next_view(&mut rendered, at(NOW + 60), false).is_some());
    }

    #[test]
    fn plan_usage_lines_sit_under_the_status_only_when_some_can_be_read() {
        let mut observed = up(0, true);
        observed.plans = Some(
            status::parse_plan_usage(
                br#"{"plans":[{"provider":"claude","available":true,"windows":[
                    {"name":"5-hour","used_percent":38,"resets_at":"2026-10-26T15:00:00Z"},
                    {"name":"weekly","used_percent":74,"resets_at":"2026-10-28T12:00:00Z"}],
                    "fetched_at":"2026-10-26T11:40:00Z"}]}"#,
            )
            .unwrap(),
        );
        let spec = menu_spec(&view(observed.clone()), false, NOW);
        assert_eq!(
            texts(&spec)[..4],
            [
                "Idle",
                "Plan usage left",
                "Claude Code · 5-hour: 62% left · resets in 3 h",
                "Claude Code · Weekly: 26% left · resets in 2 d",
            ]
        );
        assert!(
            spec.iter()
                .filter(|e| e.id.starts_with("usage"))
                .all(|e| !e.enabled)
        );
        // Neither agent readable: no heading, no separator.
        observed.plans = Some(Vec::new());
        let spec = menu_spec(&view(observed), false, NOW);
        assert!(
            spec.iter()
                .all(|e| !e.id.starts_with("usage") && e.id != "separator-usage")
        );
    }

    #[test]
    fn a_session_waiting_on_the_user_is_an_item_that_opens_it() {
        let mut observed = up(0, true);
        observed.needs = Some(vec![status::NeedsYou {
            key: "claude-code:a".into(),
            title: "Claude needs your permission to use Bash".into(),
            detail: "Tidy the app's buttons".into(),
        }]);
        let spec = menu_spec(&view(observed), false, NOW);
        let item = &spec[1];
        assert_eq!(item.id, "session:claude-code:a");
        assert_eq!(
            item.text,
            "Claude needs your permission to use Bash · Tidy the app's buttons"
        );
        assert!(item.enabled);
        assert_eq!(route(&item.id), Action::Session("claude-code:a".into()));
    }

    #[cfg(unix)]
    #[test]
    fn a_stalled_bus_query_is_cut_off_and_counts_as_unknown() {
        let started = Instant::now();
        let result = run_bounded("sleep", &["30"], Duration::from_millis(200));
        assert_eq!(result, Bounded::TimedOut);
        assert!(started.elapsed() < Duration::from_secs(5));
        // Unknown never lets the window hide.
        assert!(!tray_usable(true, Host::Unknown));
    }

    #[cfg(unix)]
    #[test]
    fn a_bounded_query_returns_its_answer_and_status() {
        assert_eq!(
            run_bounded("sh", &["-c", "echo '(true,)'"], Duration::from_secs(5)),
            Bounded::Done {
                success: true,
                stdout: "(true,)\n".into()
            }
        );
        assert!(matches!(
            run_bounded("sh", &["-c", "exit 1"], Duration::from_secs(5)),
            Bounded::Done { success: false, .. }
        ));
        assert_eq!(
            run_bounded("/nonexistent/busctl", &[], Duration::from_secs(1)),
            Bounded::NotRun
        );
    }

    #[test]
    fn the_window_hides_only_into_a_tray_the_desktop_shows_now() {
        assert!(tray_usable(true, Host::Present));
        // The watcher went away since the last check.
        assert!(!tray_usable(true, Host::Absent));
        assert!(!tray_usable(true, Host::Unknown));
        assert!(!tray_usable(false, Host::Present));
    }

    #[test]
    fn an_unchanged_view_is_not_shown_again_unless_forced() {
        let mut rendered = None;
        let idle = View::default();
        assert_eq!(
            next_view(&mut rendered, idle.clone(), false),
            Some(idle.clone())
        );
        assert_eq!(next_view(&mut rendered, idle.clone(), false), None);
        assert_eq!(next_view(&mut rendered, idle.clone(), true), Some(idle));
        let busy = view(up(1, true));
        assert_eq!(next_view(&mut rendered, busy.clone(), false), Some(busy));
    }

    #[test]
    fn linux_tray_hosts_are_read_from_the_bus_answer() {
        assert_eq!(parse_name_has_owner("(true,)\n"), Some(true));
        assert_eq!(parse_name_has_owner("(false,)\n"), Some(false));
        assert_eq!(parse_name_has_owner(""), None);
    }
}

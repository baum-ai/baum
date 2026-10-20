//! The tray icon and its menu: the app's presence while the window is closed.
//!
//! The icon follows `status::View`, which a poll thread computes from the
//! attached daemon's `/api/jobs` and `/api/automatic` (every 3 s while jobs
//! run, else every 10 s) and a slower thread computes from `/api/diagnostics`
//! (every 5 minutes, since it runs the doctor's checks). `main` tells this
//! module when it attaches to a daemon, loses it, or starts connecting.
//!
//! Start at login can also be toggled from the window's menu bar, the app's
//! settings surface: the pages the daemon serves have no Tauri capability,
//! so they cannot offer it themselves.

use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc::{self, RecvTimeoutError};
use std::sync::{Arc, Mutex, MutexGuard};
use std::thread;
use std::time::{Duration, Instant};

use tauri::image::Image;
use tauri::menu::{CheckMenuItem, IsMenuItem, Menu, MenuItem, PredefinedMenuItem, Submenu};
use tauri::tray::{TrayIcon, TrayIconBuilder};
use tauri::{AppHandle, Manager, Wry};

use crate::daemon::Launch;
use crate::status::{self, Icon, Link, Observed, Tracker, View};
use crate::{autostart, client, lifecycle};

const BUSY_INTERVAL: Duration = Duration::from_secs(3);
const IDLE_INTERVAL: Duration = Duration::from_secs(10);
const DIAGNOSTICS_INTERVAL: Duration = Duration::from_secs(300);

const TRAY_ID: &str = "baum";
/// The window menu's own item, so its events never match the tray's.
const MENU_LOGIN: &str = "menu-login";

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

/// The tray menu for a view: Open, what the daemon is doing, anything needing
/// attention, pause or resume, start at login, Quit, and Quit and stop.
pub fn menu_spec(view: &View, login: bool) -> Vec<Entry> {
    let separator = |id: &str| Entry {
        id: id.into(),
        text: String::new(),
        kind: Kind::Separator,
        enabled: false,
    };
    let mut entries = vec![
        Entry::item("open", "Open baum", true),
        Entry::item("summary", view.summary.clone(), false),
    ];
    for (i, line) in view.attention.iter().enumerate() {
        entries.push(Entry::item(
            &format!("attention-{i}"),
            format!("! {line}"),
            false,
        ));
    }
    entries.push(separator("separator-1"));
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

/// Whether something on this desktop shows tray icons. Linux icons need a
/// StatusNotifier watcher on the session bus (GNOME has none without an
/// extension); the icon builder succeeds without one and nothing appears.
/// When neither `busctl` nor `gdbus` can ask, assume there is one.
#[cfg(target_os = "linux")]
fn host_present() -> bool {
    use std::process::{Command, Stdio};
    const WATCHER: &str = "org.kde.StatusNotifierWatcher";
    let run = |program: &str, args: &[&str]| {
        Command::new(program)
            .args(args)
            .stdin(Stdio::null())
            .stderr(Stdio::null())
            .output()
    };
    if let Ok(out) = run("busctl", &["--user", "status", WATCHER]) {
        return out.status.success();
    }
    let gdbus = run(
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
    );
    match gdbus {
        Ok(out) => parse_name_has_owner(&String::from_utf8_lossy(&out.stdout)).unwrap_or(true),
        Err(_) => true,
    }
}

#[cfg(not(target_os = "linux"))]
fn host_present() -> bool {
    true
}

struct Ui {
    tray: TrayIcon,
    /// The window menu's start at login item, kept in step with the tray's.
    menu_login: CheckMenuItem<Wry>,
}

#[derive(Default)]
struct Inner {
    available: AtomicBool,
    /// Counts attachments, so diagnostics run again for a new daemon.
    generation: AtomicU64,
    launch: Mutex<Option<Launch>>,
    observed: Mutex<Observed>,
    tracker: Mutex<Tracker>,
    rendered: Mutex<Option<View>>,
    ui: Mutex<Option<Ui>>,
    wake: Mutex<Option<mpsc::Sender<()>>>,
}

#[derive(Clone, Default)]
pub struct Shared(Arc<Inner>);

fn lock<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(|p| p.into_inner())
}

fn shared(app: &AppHandle) -> Option<Shared> {
    app.try_state::<Shared>().map(|s| s.inner().clone())
}

/// Whether a tray icon is showing, so the window may hide into it.
pub fn available(app: &AppHandle) -> bool {
    shared(app).is_some_and(|s| s.0.available.load(Ordering::Relaxed))
}

/// Creates the tray and the window menu, and starts the poll threads. Runs on
/// the main thread, from `setup`. Returns whether a tray is showing.
pub fn setup(app: &AppHandle) -> bool {
    let shared = Shared::default();
    app.manage(shared.clone());
    autostart::refresh();
    let login = autostart::enabled();
    let ui = match build(app, login) {
        Ok(ui) => Some(ui),
        Err(e) => {
            eprintln!("baum desktop: no tray icon: {e}");
            None
        }
    };
    let available = ui.is_some() && host_present();
    shared.0.available.store(available, Ordering::Relaxed);
    if !available {
        eprintln!("baum desktop: this desktop shows no tray icons");
    }
    *lock(&shared.0.ui) = ui;
    spawn_polls(app, &shared);
    available
}

fn build(app: &AppHandle, login: bool) -> Result<Ui, String> {
    let e = |e: tauri::Error| e.to_string();
    let view = View::default();
    let menu_login =
        CheckMenuItem::with_id(app, MENU_LOGIN, "Start at login", true, login, None::<&str>)
            .map_err(e)?;
    let window_menu = Menu::default(app).map_err(e)?;
    let background = Submenu::with_items(app, "Background", true, &[&menu_login]).map_err(e)?;
    window_menu.append(&background).map_err(e)?;
    app.set_menu(window_menu).map_err(e)?;
    app.on_menu_event(|app, event| {
        if event.id().as_ref() == MENU_LOGIN {
            toggle_login(app);
        }
    });
    let tray = TrayIconBuilder::with_id(TRAY_ID)
        .icon(icon(Icon::Idle)?)
        .tooltip(format!("baum: {}", view.summary))
        .menu(&build_menu(app, &menu_spec(&view, login)).map_err(e)?)
        .on_menu_event(|app, event| on_tray_menu(app, event.id().as_ref()))
        .build(app)
        .map_err(e)?;
    Ok(Ui { tray, menu_login })
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
    let refs: Vec<&dyn IsMenuItem<Wry>> = items.iter().map(|i| i.as_ref()).collect();
    Menu::with_items(app, &refs)
}

fn on_tray_menu(app: &AppHandle, id: &str) {
    match id {
        "open" => lifecycle::show_window(app),
        "pause" => toggle_pause(app),
        "login" => toggle_login(app),
        "quit" => lifecycle::quit(app),
        "quit-stop" => lifecycle::quit_and_stop(app),
        _ => {}
    }
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

/// `main` attached to a daemon.
pub fn attached(app: &AppHandle, launch: &Launch) {
    let Some(shared) = shared(app) else { return };
    *lock(&shared.0.launch) = Some(launch.clone());
    lock(&shared.0.observed).link = Link::Up;
    shared.0.generation.fetch_add(1, Ordering::Relaxed);
    render(app, &shared, false);
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

/// The window is showing: what the tray flagged has been seen.
pub fn window_seen(app: &AppHandle) {
    let Some(shared) = shared(app) else { return };
    lock(&shared.0.tracker).acknowledge();
    render(app, &shared, false);
}

/// Running and queued jobs, asked of the daemon now; the last poll's answer
/// when it cannot be asked.
pub fn job_counts(app: &AppHandle) -> (usize, usize) {
    let Some(shared) = shared(app) else {
        return (0, 0);
    };
    let launch = lock(&shared.0.launch).clone();
    let fresh = launch.and_then(|l| client::jobs(&l).and_then(|b| status::parse_jobs(&b)).ok());
    let jobs = fresh.or_else(|| lock(&shared.0.observed).jobs.clone());
    jobs.map_or((0, 0), |j| (j.running, j.queued))
}

fn wake(shared: &Shared) {
    if let Some(sender) = lock(&shared.0.wake).as_ref() {
        let _ = sender.send(());
    }
}

/// Recomputes the view and updates the icon, tooltip and menu when it
/// changed (or `force`, after start at login changed).
fn render(app: &AppHandle, shared: &Shared, force: bool) {
    let observed = lock(&shared.0.observed).clone();
    let view = lock(&shared.0.tracker).update(&observed);
    let mut rendered = lock(&shared.0.rendered);
    if !force && rendered.as_ref() == Some(&view) {
        return;
    }
    let login = autostart::enabled();
    if let Some(ui) = lock(&shared.0.ui).as_ref() {
        let _ = ui.menu_login.set_checked(login);
        if let Ok(image) = icon(view.icon) {
            let _ = ui.tray.set_icon(Some(image));
        }
        let _ = ui.tray.set_tooltip(Some(format!("baum: {}", view.summary)));
        if let Ok(menu) = build_menu(app, &menu_spec(&view, login)) {
            let _ = ui.tray.set_menu(Some(menu));
        }
    }
    *rendered = Some(view);
}

fn spawn_polls(app: &AppHandle, shared: &Shared) {
    let (sender, receiver) = mpsc::channel();
    *lock(&shared.0.wake) = Some(sender);
    let (poll_app, poll_shared) = (app.clone(), shared.clone());
    thread::spawn(move || {
        loop {
            let interval = poll_once(&poll_app, &poll_shared);
            match receiver.recv_timeout(interval) {
                Ok(()) | Err(RecvTimeoutError::Timeout) => {}
                Err(RecvTimeoutError::Disconnected) => return,
            }
        }
    });
    let (diag_app, diag_shared) = (app.clone(), shared.clone());
    thread::spawn(move || {
        let mut checked: Option<(u64, Instant)> = None;
        loop {
            thread::sleep(Duration::from_secs(5));
            let generation = diag_shared.0.generation.load(Ordering::Relaxed);
            let due = checked
                .is_none_or(|(g, at)| g != generation || at.elapsed() >= DIAGNOSTICS_INTERVAL);
            let Some(launch) = lock(&diag_shared.0.launch).clone().filter(|_| due) else {
                continue;
            };
            checked = Some((generation, Instant::now()));
            // A failed or timed-out run leaves the last answer standing.
            if let Ok(problem) =
                client::diagnostics(&launch).and_then(|b| status::parse_diagnostics(&b))
            {
                lock(&diag_shared.0.observed).diagnostics_problem = problem;
                render(&diag_app, &diag_shared, false);
            }
        }
    });
}

/// One look at the daemon. A failed request leaves the last answer standing:
/// the connection watch in `main` decides when the daemon has gone.
fn poll_once(app: &AppHandle, shared: &Shared) -> Duration {
    let launch = lock(&shared.0.launch).clone();
    let Some(launch) = launch else {
        return IDLE_INTERVAL;
    };
    let jobs = client::jobs(&launch).and_then(|b| status::parse_jobs(&b));
    let automatic = client::automatic(&launch).and_then(|b| status::parse_automatic(&b));
    {
        let mut observed = lock(&shared.0.observed);
        if let Ok(jobs) = jobs {
            observed.jobs = Some(jobs);
        }
        if let Ok(automatic) = automatic {
            observed.automatic = automatic;
        }
    }
    render(app, shared, false);
    let busy = lock(&shared.0.observed)
        .jobs
        .as_ref()
        .is_some_and(|j| j.running + j.queued > 0);
    if busy { BUSY_INTERVAL } else { IDLE_INTERVAL }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::status::{Automatic, Jobs};

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
            automatic: Some(Automatic {
                enabled: automatic,
                capped: 0,
            }),
            diagnostics_problem: false,
        }
    }

    fn texts(spec: &[Entry]) -> Vec<&str> {
        spec.iter()
            .filter(|e| e.kind != Kind::Separator)
            .map(|e| e.text.as_str())
            .collect()
    }

    #[test]
    fn the_menu_has_the_decided_entries_in_order() {
        let spec = menu_spec(&view(up(2, true)), false);
        assert_eq!(
            texts(&spec),
            vec![
                "Open baum",
                "2 running",
                "Pause automatic reviews",
                "Start at login",
                "Quit",
                "Quit and stop background reviews"
            ]
        );
        let ids: Vec<&str> = spec.iter().map(|e| e.id.as_str()).collect();
        assert_eq!(
            ids,
            vec![
                "open",
                "summary",
                "separator-1",
                "pause",
                "login",
                "separator-2",
                "quit",
                "quit-stop"
            ]
        );
    }

    #[test]
    fn the_menu_offers_resume_while_paused_and_disables_pause_while_unknown() {
        let paused = menu_spec(&view(up(0, false)), false);
        assert_eq!(paused[3].text, "Resume automatic reviews");
        assert!(paused[3].enabled);
        let unknown = menu_spec(&View::default(), false);
        let pause = unknown.iter().find(|e| e.id == "pause").unwrap();
        assert!(!pause.enabled);
    }

    #[test]
    fn the_start_at_login_item_shows_its_state() {
        for login in [false, true] {
            let spec = menu_spec(&View::default(), login);
            let item = spec.iter().find(|e| e.id == "login").unwrap();
            assert_eq!(item.kind, Kind::Check(login));
        }
    }

    #[test]
    fn attention_lines_appear_under_the_summary() {
        let mut observed = up(0, true);
        observed.diagnostics_problem = true;
        let spec = menu_spec(&view(observed), false);
        assert_eq!(spec[1].text, "Idle");
        assert_eq!(spec[2].text, "! A diagnostics check reports a problem");
        assert!(!spec[2].enabled);
    }

    #[test]
    fn linux_tray_hosts_are_read_from_the_bus_answer() {
        assert_eq!(parse_name_has_owner("(true,)\n"), Some(true));
        assert_eq!(parse_name_has_owner("(false,)\n"), Some(false));
        assert_eq!(parse_name_has_owner(""), None);
    }
}

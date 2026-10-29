//! Native notifications from the daemon's events.
//!
//! The app subscribes to the daemon's events stream itself (`events`), so a
//! hidden or minimised window does not stop them (closing the window hides it
//! and the app keeps running). `toast` turns the events that matter into text: a `notice` event (review finished or failed,
//! review requested from the user, review generated automatically, news of
//! the user's own pull requests and fixes, an agent session waiting for the
//! user's permission or answer). `notifier` applies the
//! settings, the operating system's permission and the focused window;
//! `platform` shows the notification. A click makes Rust load the right app
//! URL in the window, so pages the daemon serves need no capability.
//!
//! The toggles are a "Notifications" submenu in both the window's menu bar
//! and the tray menu, one check item per kind, saved in `notifications.json`
//! in the app's config directory. The saved settings are the only state; a
//! click flips them and both menus are brought to them.

mod events;
mod notifier;
mod platform;
mod settings;
mod toast;

use std::sync::{Arc, Mutex, MutexGuard};
use std::thread;

use tauri::menu::{CheckMenuItem, MenuItemKind, Submenu};
use tauri::{AppHandle, Manager};
use url::Url;

use crate::daemon::Launch;
use events::Endpoint;
use notifier::{Host, Notifier};
use settings::Store;
use toast::{Kind, Route};

const MAIN_WINDOW: &str = "main";
const SUBMENU_ID: &str = "notifications";

/// The notifier the app manages, and the window it works through.
pub struct Service {
    notifier: Arc<Notifier>,
    host: Arc<AppHost>,
}

struct AppHost {
    app: AppHandle,
    /// The daemon the app is attached to, once it is.
    endpoint: Mutex<Option<Endpoint>>,
}

impl AppHost {
    fn endpoint(&self) -> MutexGuard<'_, Option<Endpoint>> {
        self.endpoint
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }
}

/// The pull request a URL on the daemon's origin shows, as clone and number.
fn pr_of(url: &Url, daemon: &url::Origin) -> Option<(String, u64)> {
    if &url.origin() != daemon {
        return None;
    }
    let (mut clone, mut number) = (None, None);
    for (key, value) in url.query_pairs() {
        match &*key {
            "clone" | "repository" => clone = Some(value.into_owned()),
            "pr" => number = value.parse().ok(),
            _ => {}
        }
    }
    Some((clone?, number?))
}

/// The agent session whose transcript a URL on the daemon's origin shows, by
/// its `agent:id` key.
fn session_of(url: &Url, daemon: &url::Origin) -> Option<String> {
    if &url.origin() != daemon {
        return None;
    }
    let (mut sessions, mut key) = (false, None);
    for (name, value) in url.query_pairs() {
        match &*name {
            "page" => sessions = value == "sessions",
            "session" => key = Some(value.into_owned()),
            _ => {}
        }
    }
    key.filter(|k| sessions && !k.is_empty())
}

impl AppHost {
    /// The window's page while it has focus, with the daemon's origin.
    fn focused_url(&self) -> Option<(Url, url::Origin)> {
        let origin = self.endpoint().as_ref()?.origin();
        let window = self.app.get_webview_window(MAIN_WINDOW)?;
        if !window.is_focused().unwrap_or(false) || window.is_minimized().unwrap_or(false) {
            return None;
        }
        Some((window.url().ok()?, origin))
    }
}

impl Host for AppHost {
    fn focused_pr(&self) -> Option<(String, u64)> {
        let (url, origin) = self.focused_url()?;
        pr_of(&url, &origin)
    }

    fn focused_session(&self) -> Option<String> {
        let (url, origin) = self.focused_url()?;
        session_of(&url, &origin)
    }

    fn open(&self, route: &Route) {
        let Some(endpoint) = self.endpoint().clone() else {
            return;
        };
        let base = match events::app_url(&endpoint) {
            Ok(base) => base,
            Err(e) => {
                eprintln!("baum desktop: cannot open the notification's page: {e}");
                return;
            }
        };
        let Some(window) = self.app.get_webview_window(MAIN_WINDOW) else {
            return;
        };
        crate::lifecycle::show_window(&self.app);
        if let Err(e) = window.navigate(route.url(&base)) {
            eprintln!("baum desktop: cannot open the notification's page: {e}");
        }
    }
}

/// Sets up notifications: the saved settings, the menu of toggles and the
/// managed state `attach` uses. Nothing is shown, and the operating system is
/// not asked for permission, until the first notification is due.
pub fn install(app: &AppHandle) -> tauri::Result<()> {
    #[cfg(target_os = "macos")]
    let _ = notify_rust::set_application("com.usebaum.desktop");
    let dir = app
        .path()
        .app_config_dir()
        .map_err(|e| tauri::Error::Anyhow(e.into()))?;
    let host = Arc::new(AppHost {
        app: app.clone(),
        endpoint: Mutex::new(None),
    });
    let notifier = Arc::new(Notifier::new(
        host.clone(),
        Arc::new(platform::Os),
        Store::in_dir(dir),
    ));
    app.manage(Service { notifier, host });
    Ok(())
}

/// A "Notifications" submenu: one check item per kind, built from the saved
/// settings, which are the only copy of the state. The window menu and the
/// tray each hold one; `on_menu` handles a click on either. `None` before
/// `install`.
pub fn submenu(app: &AppHandle) -> tauri::Result<Option<Submenu<tauri::Wry>>> {
    let Some(service) = app.try_state::<Service>() else {
        return Ok(None);
    };
    let kinds = service.notifier.settings().kinds;
    let submenu = Submenu::with_id(app, SUBMENU_ID, "Notifications", true)?;
    for kind in Kind::ALL {
        submenu.append(&CheckMenuItem::with_id(
            app,
            kind.menu_id(),
            kind.label(),
            true,
            kinds.get(kind),
            None::<&str>,
        )?)?;
    }
    Ok(Some(submenu))
}

/// The menu ids of the notification toggles.
#[cfg(test)]
pub fn menu_ids() -> impl Iterator<Item = &'static str> {
    Kind::ALL.into_iter().map(Kind::menu_id)
}

/// Whether `id` is one of the notification toggles.
pub fn is_menu_id(id: &str) -> bool {
    Kind::from_menu_id(id).is_some()
}

/// Handles a menu click: flips the kind the item names (from the saved
/// state, not the item's own check) and brings both menus to it. Returns
/// whether the id was one of ours.
pub fn on_menu(app: &AppHandle, id: &str) -> bool {
    let (Some(kind), Some(service)) = (Kind::from_menu_id(id), app.try_state::<Service>()) else {
        return false;
    };
    let on = !service.notifier.settings().kinds.get(kind);
    service.notifier.set_kind(kind, on);
    sync_window_menu(app);
    crate::tray::refresh(app);
    true
}

/// Sets the window menu's check items to the saved state.
fn sync_window_menu(app: &AppHandle) {
    let Some(service) = app.try_state::<Service>() else {
        return;
    };
    let kinds = service.notifier.settings().kinds;
    let Some(MenuItemKind::Submenu(submenu)) = app.menu().and_then(|menu| menu.get(SUBMENU_ID))
    else {
        return;
    };
    for kind in Kind::ALL {
        if let Some(MenuItemKind::Check(item)) = submenu.get(kind.menu_id()) {
            let _ = item.set_checked(kinds.get(kind));
        }
    }
}

/// Opens an agent session's transcript in the window, as a click on its
/// notification does, for the tray's waiting sessions. The app path is asked
/// of the daemon, so this runs off the main thread.
pub fn open_session(app: &AppHandle, key: String) {
    let Some(service) = app.try_state::<Service>() else {
        return;
    };
    let host = service.host.clone();
    thread::spawn(move || host.open(&Route::Session { key }));
}

/// Starts reading the attached daemon's events until `stop` returns true (the
/// app attached to another daemon, or gave up on this one).
pub fn attach(app: &AppHandle, launch: &Launch, stop: impl Fn() -> bool + Send + 'static) {
    let Some(service) = app.try_state::<Service>() else {
        return;
    };
    let Some(endpoint) = Endpoint::from_launch(launch) else {
        eprintln!("baum desktop: notifications are off: the daemon's address is unusable");
        return;
    };
    *service.host.endpoint() = Some(endpoint.clone());
    let notifier = service.notifier.clone();
    thread::spawn(move || {
        events::run(&endpoint, &stop, &mut |event| {
            notifier.handle_event(&event.name, &event.data);
        });
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    fn origin() -> url::Origin {
        Url::parse("http://127.0.0.1:4100/").unwrap().origin()
    }

    #[test]
    fn the_window_shows_a_pull_request_when_its_url_says_so() {
        let url = |s: &str| Url::parse(s).unwrap();
        assert_eq!(
            pr_of(&url("http://127.0.0.1:4100/s/ns/?clone=c1&pr=7"), &origin()),
            Some(("c1".into(), 7))
        );
        // Another page, a half-specified one, another origin or the bundled
        // screen is not a pull request.
        for other in [
            "http://127.0.0.1:4100/s/ns/?page=jobs",
            "http://127.0.0.1:4100/s/ns/?pr=7",
            "http://127.0.0.1:4100/s/ns/?clone=c1&pr=x",
            "http://127.0.0.1:4200/s/ns/?clone=c1&pr=7",
            "tauri://localhost/index.html",
        ] {
            assert_eq!(pr_of(&url(other), &origin()), None, "{other}");
        }
    }

    #[test]
    fn the_window_shows_a_session_when_its_url_says_so() {
        let url = |s: &str| Url::parse(s).unwrap();
        assert_eq!(
            session_of(
                &url("http://127.0.0.1:4100/s/ns/?page=sessions&session=claude-code%3As-1"),
                &origin()
            ),
            Some("claude-code:s-1".into())
        );
        for other in [
            "http://127.0.0.1:4100/s/ns/?page=sessions",
            "http://127.0.0.1:4100/s/ns/?session=claude-code%3As-1",
            "http://127.0.0.1:4100/s/ns/?page=jobs&session=claude-code%3As-1",
            "http://127.0.0.1:4200/s/ns/?page=sessions&session=claude-code%3As-1",
        ] {
            assert_eq!(session_of(&url(other), &origin()), None, "{other}");
        }
    }
}

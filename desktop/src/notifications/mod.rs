//! Native notifications from the daemon's events.
//!
//! The app subscribes to the daemon's events stream itself (`events`), so a
//! hidden or minimised window does not stop them. The app runs only while its
//! window exists (until the tray lands, closing the window quits it), and a
//! click needs that window. `toast` turns the
//! events that matter into text: a `notice` event (review finished or failed,
//! review requested from the user, review generated automatically) and an
//! `automatic` event reporting the daily cost cap. `notifier` applies the
//! settings, the operating system's permission and the focused window;
//! `platform` shows the notification. A click makes Rust load the right app
//! URL in the window, so pages the daemon serves need no capability.
//!
//! The toggles are a "Notifications" submenu of the app menu, one check item
//! per kind, saved in `notifications.json` in the app's config directory.

mod events;
mod notifier;
mod platform;
mod settings;
mod toast;

use std::sync::{Arc, Mutex, MutexGuard};
use std::thread;

use tauri::menu::{CheckMenuItem, Menu, MenuEvent, Submenu};
use tauri::{AppHandle, Manager};
use url::Url;

use crate::daemon::Launch;
use events::Endpoint;
use notifier::{Host, Notifier};
use settings::Store;
use toast::{Kind, Route};

const MAIN_WINDOW: &str = "main";

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
            "clone" => clone = Some(value.into_owned()),
            "pr" => number = value.parse().ok(),
            _ => {}
        }
    }
    Some((clone?, number?))
}

impl Host for AppHost {
    fn focused_pr(&self) -> Option<(String, u64)> {
        let origin = self.endpoint().as_ref()?.origin();
        let window = self.app.get_webview_window(MAIN_WINDOW)?;
        if !window.is_focused().unwrap_or(false) || window.is_minimized().unwrap_or(false) {
            return None;
        }
        pr_of(&window.url().ok()?, &origin)
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
        let _ = window.unminimize();
        let _ = window.show();
        let _ = window.set_focus();
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
    install_menu(app, &notifier)?;
    app.manage(Service { notifier, host });
    Ok(())
}

/// Adds the Notifications submenu to the app menu. All of the menu code is
/// `submenu`, so the submenu can be placed in another menu.
fn install_menu(app: &AppHandle, notifier: &Arc<Notifier>) -> tauri::Result<()> {
    let submenu = submenu(app, notifier)?;
    let menu = match app.menu() {
        Some(menu) => menu,
        None => Menu::new(app)?,
    };
    menu.append(&submenu)?;
    app.set_menu(menu)?;
    Ok(())
}

/// The "Notifications" submenu: one check item per kind, each saved as it is
/// toggled. Registers the menu event handler that does so.
pub fn submenu(app: &AppHandle, notifier: &Arc<Notifier>) -> tauri::Result<Submenu<tauri::Wry>> {
    let kinds = notifier.settings().kinds;
    let items = Kind::ALL
        .into_iter()
        .map(|kind| {
            CheckMenuItem::with_id(
                app,
                kind.menu_id(),
                kind.label(),
                true,
                kinds.get(kind),
                None::<&str>,
            )
        })
        .collect::<tauri::Result<Vec<_>>>()?;
    let submenu = Submenu::with_id(app, "notifications", "Notifications", true)?;
    for item in &items {
        submenu.append(item)?;
    }
    let notifier = notifier.clone();
    app.on_menu_event(move |_, event: MenuEvent| {
        let Some(kind) = Kind::from_menu_id(event.id().as_ref()) else {
            return;
        };
        if let Some(item) = items.iter().find(|item| item.id() == event.id()) {
            // The item has already flipped itself; keep what it shows.
            if let Ok(on) = item.is_checked() {
                notifier.set_kind(kind, on);
            }
        }
    });
    Ok(submenu)
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
}

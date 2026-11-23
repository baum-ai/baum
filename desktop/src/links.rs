//! Opening links to other sites in the default browser. A page asks in one
//! of two ways: a request for a new window (`target="_blank"`, `window.open`,
//! see `on_new_window` in `main.rs`), or the `open_link` command, which the
//! daemon's pages (the `daemon-pages` capability) may call.
//!
//! The command exists because WKWebView (macOS) asks the navigation handler
//! about a `target="_blank"` click before it asks for a new window, and wry
//! passes that handler only the URL, so the shell cannot tell such a click
//! from a frame navigating itself. The handler refuses it (see
//! `navigation::decide`), WebKit then never asks for the window, and the link
//! did nothing. The app calls `open_link` instead when the shell declares
//! `openLinks` (see `declaration`). Tauri injects its command bridge into the
//! page's main frame only on macOS and Linux, so a frame cannot call it there.
//!
//! Both ways take the same URLs (`navigation::decide_new_window`) and share
//! one `OpenLimit`, so a page opens the browser at most once a second however
//! it asks.

use std::sync::Mutex;
use std::time::Instant;

use tauri::{AppHandle, Manager};
use url::Url;

use crate::navigation::{self, NewWindow, OpenLimit};

/// The one `OpenLimit` of the app, managed state.
#[derive(Default)]
pub struct Limit(Mutex<OpenLimit>);

/// What a request to open `url` did.
#[derive(Debug, PartialEq, Eq)]
pub enum Opened {
    /// Handed to the browser.
    Browser,
    /// Allowed, but another link opened less than `OPEN_INTERVAL` ago.
    Dropped,
    /// Not a link to another site.
    Refused,
}

/// Opens `url` in the default browser if it is a link to another site and
/// no other link opened within the last `OPEN_INTERVAL`.
pub fn open(app: &AppHandle, url: &Url) -> Opened {
    let opened = decide(&app.state::<Limit>(), url, Instant::now());
    if opened == Opened::Browser {
        crate::open_externally(app, url);
    } else if opened == Opened::Dropped && cfg!(debug_assertions) {
        eprintln!(
            "baum desktop: dropped a second link to {} within a second",
            navigation::scheme_and_host(url)
        );
    }
    opened
}

fn decide(limit: &Limit, url: &Url, now: Instant) -> Opened {
    if navigation::decide_new_window(url) != NewWindow::OpenInBrowser {
        return Opened::Refused;
    }
    let allowed = limit
        .0
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner())
        .allow(now);
    if allowed {
        Opened::Browser
    } else {
        Opened::Dropped
    }
}

/// Opens a link to another site in the default browser. A URL the shell
/// would not open from a new-window request is an error; a second link
/// within a second is dropped without one.
#[tauri::command]
pub fn open_link(app: AppHandle, url: String) -> Result<(), String> {
    let url = Url::parse(&url).map_err(|_| "That is not a link.".to_string())?;
    match open(&app, &url) {
        Opened::Refused => Err("Only links to other web sites open in the browser.".into()),
        Opened::Browser | Opened::Dropped => Ok(()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn url(s: &str) -> Url {
        Url::parse(s).unwrap()
    }

    #[test]
    fn opens_only_links_to_other_sites() {
        for refused in [
            "http://127.0.0.1:4100/",
            "http://localhost/",
            "https://user:pass@github.com/",
            "file:///etc/passwd",
            "javascript:alert(1)",
            "mailto:someone@example.com",
        ] {
            let limit = Limit::default();
            assert_eq!(
                decide(&limit, &url(refused), Instant::now()),
                Opened::Refused,
                "{refused}"
            );
        }
        let limit = Limit::default();
        assert_eq!(
            decide(
                &limit,
                &url("https://github.com/acme/widgets/pull/7"),
                Instant::now()
            ),
            Opened::Browser
        );
    }

    #[test]
    fn a_second_link_within_the_interval_is_dropped() {
        let limit = Limit::default();
        let start = Instant::now();
        let link = url("https://github.com/");
        assert_eq!(decide(&limit, &link, start), Opened::Browser);
        assert_eq!(decide(&limit, &link, start), Opened::Dropped);
        // A refused URL neither opens nor counts.
        assert_eq!(
            decide(
                &limit,
                &url("http://localhost/"),
                start + navigation::OPEN_INTERVAL
            ),
            Opened::Refused
        );
        assert_eq!(
            decide(&limit, &link, start + navigation::OPEN_INTERVAL),
            Opened::Browser
        );
    }
}

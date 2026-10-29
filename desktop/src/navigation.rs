//! Where the window may go. It shows only the bundled connecting screen and
//! the daemon's loopback origin; web links open in the default browser and
//! everything else is refused.

use url::{Origin, Url};

#[derive(Debug, PartialEq, Eq)]
pub enum Decision {
    Allow,
    OpenInBrowser,
    Deny,
}

/// Decides a navigation of the main window. `daemon` is the origin of the
/// daemon the window is attached to, if any.
pub fn decide(url: &Url, daemon: Option<&Origin>, setup_open: bool) -> Decision {
    if is_bundled(url) {
        return if bundled_page_allowed(url.path(), setup_open) {
            Decision::Allow
        } else {
            Decision::Deny
        };
    }
    if daemon.is_some_and(|d| &url.origin() == d) {
        return Decision::Allow;
    }
    match url.scheme() {
        "http" | "https" if url.host().is_some() && !is_loopback(url) => Decision::OpenInBrowser,
        _ => Decision::Deny,
    }
}

/// The bundled pages the window may show, by their exact path: the
/// connecting screen, and the setup screen only while the app shows it,
/// never because a page navigated there. The asset server also answers other
/// spellings of a page (`/setup`, `/setup.html/`, `/setup%2Ehtml`), so every
/// other path, any percent-encoding included, is refused.
fn bundled_page_allowed(path: &str, setup_open: bool) -> bool {
    match path {
        "/" | "/index.html" => true,
        "/setup.html" => setup_open,
        _ => false,
    }
}

/// Requests for a new window (`target="_blank"`, `window.open`) never open
/// one: web links go to the browser.
pub fn decide_new_window(url: &Url) -> Decision {
    match decide(url, None, false) {
        Decision::OpenInBrowser => Decision::OpenInBrowser,
        _ => Decision::Deny,
    }
}

/// The tray panel's window loads its bundled page and nothing else. The check
/// compares scheme and host, as `is_bundled` does: `tauri://` is not a special
/// scheme, so its `origin()` is opaque and never equal to another.
pub fn is_tray_panel(url: &Url) -> bool {
    is_bundled(url) && url.path() == "/tray.html"
}

/// The bundled screen's own origin: `tauri://localhost` on macOS and Linux,
/// `http://tauri.localhost` on Windows.
fn is_bundled(url: &Url) -> bool {
    match url.scheme() {
        "tauri" => url.host_str() == Some("localhost"),
        "http" if cfg!(windows) => url.host_str() == Some("tauri.localhost"),
        _ => false,
    }
}

/// Another local server, such as a stale daemon address, is never opened:
/// neither in the window nor, with a credential in hand, in the browser.
fn is_loopback(url: &Url) -> bool {
    match url.host() {
        Some(url::Host::Ipv4(ip)) => ip.is_loopback() || ip.is_unspecified(),
        Some(url::Host::Ipv6(ip)) => match ip.to_ipv4_mapped() {
            // `[::ffff:127.0.0.1]` reaches 127.0.0.1.
            Some(v4) => v4.is_loopback() || v4.is_unspecified(),
            None => ip.is_loopback() || ip.is_unspecified(),
        },
        Some(url::Host::Domain(d)) => {
            let d = d.trim_end_matches('.').to_ascii_lowercase();
            d == "localhost" || d.ends_with(".localhost")
        }
        None => false,
    }
}

/// The URL of the bundled connecting screen.
pub fn bundled_screen() -> Url {
    let base = if cfg!(windows) {
        "http://tauri.localhost/index.html"
    } else {
        "tauri://localhost/index.html"
    };
    Url::parse(base).expect("static URL")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn url(s: &str) -> Url {
        Url::parse(s).unwrap()
    }

    #[test]
    fn the_tray_panel_loads_only_its_bundled_page() {
        let page = bundled_screen().join("tray.html").unwrap();
        assert!(is_tray_panel(&page));
        // Opaque origins never compare equal, which is why this is not an
        // origin check.
        assert_ne!(page.origin(), bundled_screen().origin());
        for other in [
            bundled_screen().as_str(),
            "http://127.0.0.1:4100/tray.html",
            "https://example.com/tray.html",
            "tauri://elsewhere/tray.html",
        ] {
            assert!(!is_tray_panel(&url(other)), "{other}");
        }
    }

    #[test]
    fn allows_the_bundled_screen_and_the_daemon() {
        let daemon = url("http://127.0.0.1:4100/").origin();
        assert_eq!(decide(&bundled_screen(), None, false), Decision::Allow);
        for u in [
            "http://127.0.0.1:4100/#token=x",
            "http://127.0.0.1:4100/s/abc/?page=jobs",
        ] {
            assert_eq!(
                decide(&url(u), Some(&daemon), false),
                Decision::Allow,
                "{u}"
            );
        }
    }

    #[test]
    fn sends_web_links_to_the_browser() {
        let daemon = url("http://127.0.0.1:4100/").origin();
        for u in [
            "https://github.com/acme/widgets/pull/7",
            "http://example.com/",
        ] {
            assert_eq!(
                decide(&url(u), Some(&daemon), false),
                Decision::OpenInBrowser,
                "{u}"
            );
        }
    }

    #[test]
    fn refuses_other_local_servers_and_schemes() {
        let daemon = url("http://127.0.0.1:4100/").origin();
        for u in [
            "http://127.0.0.1:4200/",
            "https://127.0.0.1:4100/",
            "http://localhost:4100/",
            "http://app.localhost/",
            "http://[::1]:4100/",
            "http://[::ffff:127.0.0.1]:8080/",
            "http://[::ffff:7f00:1]:4100/",
            "http://[::ffff:0.0.0.0]:4100/",
            "http://0.0.0.0:4100/",
            "file:///etc/passwd",
            "javascript:alert(1)",
            "data:text/html,hi",
            "tauri://elsewhere/",
            "mailto:someone@example.com",
        ] {
            assert_eq!(decide(&url(u), Some(&daemon), false), Decision::Deny, "{u}");
        }
    }

    #[test]
    fn the_setup_screen_loads_only_while_it_shows() {
        let page = |path: &str| {
            let mut url = bundled_screen();
            url.set_path(path);
            url
        };
        assert_eq!(decide(&page("/setup.html"), None, true), Decision::Allow);
        assert_eq!(decide(&page("/setup.html"), None, false), Decision::Deny);
        assert_eq!(decide(&bundled_screen(), None, false), Decision::Allow);
        assert_eq!(decide(&page("/"), None, false), Decision::Allow);
        // Other spellings the asset server maps to a page, and other files.
        for path in [
            "/setup",
            "/setup.html/",
            "/setup%2Ehtml",
            "/setup%2ehtml",
            "/%73etup.html",
            "/index",
            "/index.html/",
            "/screen.js",
        ] {
            for open in [false, true] {
                assert_eq!(decide(&page(path), None, open), Decision::Deny, "{path}");
            }
        }
    }

    #[test]
    fn without_a_daemon_loopback_is_refused() {
        assert_eq!(
            decide(&url("http://127.0.0.1:4100/"), None, false),
            Decision::Deny
        );
    }

    #[test]
    fn new_windows_never_open() {
        assert_eq!(
            decide_new_window(&url("https://github.com/")),
            Decision::OpenInBrowser
        );
        assert_eq!(decide_new_window(&bundled_screen()), Decision::Deny);
        assert_eq!(
            decide_new_window(&url("http://127.0.0.1:4100/")),
            Decision::Deny
        );
    }
}

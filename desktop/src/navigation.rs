//! Where the window may go. It shows only the bundled screens and the
//! daemon's loopback origin and refuses every other navigation. Web links
//! open in the default browser only through a request for a new window
//! (`target="_blank"`, `window.open`), never through a navigation.

use url::{Origin, Url};

/// What a navigation gets: it loads, or nothing happens.
#[derive(Debug, PartialEq, Eq)]
pub enum Decision {
    Allow,
    Deny,
}

/// What a request for a new window gets. No window ever opens.
#[derive(Debug, PartialEq, Eq)]
pub enum NewWindow {
    OpenInBrowser,
    Deny,
}

/// Decides a navigation in the main window. `daemon` is the origin of the
/// daemon the window is attached to, if any.
///
/// The webview asks this for a navigation in any frame, not only the page
/// itself, and says only where it goes. A frame inside the app's page (an
/// embedded document, a sandboxed frame, injected content) can navigate
/// itself. The page's `frame-src` stops that before this is asked on
/// WebKitGTK, but not on a page without that policy (the Vite development
/// server's), nor on an engine that asks first. So no answer here may act
/// outside the window: a navigation to another site is refused, never handed
/// to the browser, or any frame could open the browser at an address carrying
/// data it read, as often as it liked. The app opens its links to other sites
/// as new windows (`decide_new_window`), which a sandboxed frame without
/// `allow-popups` cannot request.
pub fn decide(url: &Url, daemon: Option<&Origin>, setup_open: bool) -> Decision {
    if is_bundled(url) {
        return if bundled_page_allowed(url.path(), setup_open) {
            Decision::Allow
        } else {
            Decision::Deny
        };
    }
    // The daemon's pages, and frames of its own documents. A `blob:` URL has
    // its creator's origin, so it is excluded by scheme: the app navigates to
    // none, and a document built from a script's data has no reason to load.
    let http = matches!(url.scheme(), "http" | "https");
    if http && daemon.is_some_and(|d| &url.origin() == d) {
        return Decision::Allow;
    }
    // Everything else: other sites, other local servers (a stale daemon
    // address), `about:`, `data:`, `blob:`, `javascript:`, `file:` and other
    // schemes. WebKitGTK asks about a new frame's `about:blank` and a
    // `srcdoc` frame's `about:srcdoc` too: refusing them keeps the empty
    // document a new frame starts with, and loads no `srcdoc`.
    Decision::Deny
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
/// one: links to other web sites go to the browser, and anything else is
/// refused. This is how every link of the app to another site opens. A page
/// can ask only while the person interacts with it (the webview blocks
/// pop-ups otherwise), and a sandboxed frame only with `allow-popups`.
pub fn decide_new_window(url: &Url) -> NewWindow {
    match url.scheme() {
        "http" | "https" if url.host().is_some() && !is_loopback(url) => NewWindow::OpenInBrowser,
        // Loopback servers, the daemon's included, and other schemes.
        _ => NewWindow::Deny,
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

    /// A navigation never opens the browser, whichever frame it comes from:
    /// links to other sites are refused in place.
    #[test]
    fn refuses_navigations_to_other_sites() {
        let daemon = url("http://127.0.0.1:4100/").origin();
        for u in [
            "https://github.com/acme/widgets/pull/7",
            "http://example.com/",
            "https://attacker.example/?d=secret",
            "https://github.com/login/device",
        ] {
            assert_eq!(decide(&url(u), Some(&daemon), false), Decision::Deny, "{u}");
            assert_eq!(decide(&url(u), None, true), Decision::Deny, "{u}");
        }
    }

    #[test]
    fn refuses_other_local_servers_and_schemes() {
        let daemon = url("http://127.0.0.1:4100/").origin();
        for u in [
            "about:blank",
            "about:srcdoc",
            "blob:http://127.0.0.1:4100/7d3c1a52-6f0e-4d7e-9a43-1f0b4c2b1e11",
            "blob:null/7d3c1a52-6f0e-4d7e-9a43-1f0b4c2b1e11",
            "tel:+15555550100",
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

    /// The app's links to other sites ask for a new window, which opens the
    /// browser; no window ever opens, and nothing else leaves the app.
    #[test]
    fn new_windows_never_open() {
        for u in [
            "https://github.com/",
            "https://github.com/acme/widgets/pull/7#discussion_r1",
            "http://example.com/",
            "https://docs.usebaum.com/",
        ] {
            assert_eq!(decide_new_window(&url(u)), NewWindow::OpenInBrowser, "{u}");
        }
        assert_eq!(decide_new_window(&bundled_screen()), NewWindow::Deny);
        for u in [
            "http://127.0.0.1:4100/",
            "http://localhost:4100/",
            "http://[::1]:4100/",
            "about:blank",
            "about:srcdoc",
            "data:text/html,hi",
            "blob:http://127.0.0.1:4100/7d3c1a52-6f0e-4d7e-9a43-1f0b4c2b1e11",
            "file:///etc/passwd",
            "javascript:alert(1)",
            "mailto:someone@example.com",
            "tel:+15555550100",
            "tauri://localhost/setup.html",
        ] {
            assert_eq!(decide_new_window(&url(u)), NewWindow::Deny, "{u}");
        }
    }
}

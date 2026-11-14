//! The tile check window: a second window, never shown, where the daemon's
//! page renders custom tiles to check them (`?page=tile-check`). WebKitGTK
//! gives it a web process of its own, so a tile that never yields stalls
//! that process and not the main window, and closing the window ends it.
//!
//! The main window's daemon page opens it (`open_check_window`) and closes it
//! (`close_check_window`) when a check hangs or after it has been idle. The
//! shell also closes it when the main window loads another page or the app
//! detaches from the daemon, since the page that opened it is gone. It loads
//! the same daemon origin as the main window, under the same navigation
//! rules, opens no browser, and its own capability (`tile-check`) grants its
//! page no command.

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use tauri::{AppHandle, Manager, Runtime, WebviewUrl, WebviewWindowBuilder};
use url::{Origin, Url};

use crate::declaration;
use crate::navigation::{self, Decision};

pub const LABEL: &str = "tile-check";
/// The size the runner lays tiles out in.
const WIDTH: f64 = 1280.0;
const HEIGHT: f64 = 900.0;

/// Serialises opening and closing, so two calls never build two windows.
static LOCK: Mutex<()> = Mutex::new(());
/// Counts the shell's own closes (`close_soon`), so a window that was being
/// built when one came is closed once it exists.
static GENERATION: AtomicU64 = AtomicU64::new(0);
/// How long a close waits for the window to be gone.
const GONE_TIMEOUT: Duration = Duration::from_secs(1);
/// How long a close lets the window's page unload before its web process
/// is ended (see `destroy`).
#[cfg(target_os = "linux")]
const UNLOAD_GRACE: Duration = Duration::from_millis(200);

/// The check page for a main window showing `main`: the root of the person's
/// own namespace on the daemon's origin (`/s/<namespace>/`, outside the sample
/// project, as the app's `ownBase` reads it) with `?page=tile-check`. `None`
/// when the main window does not show a page of the attached daemon.
pub fn page_url(main: &Url, daemon: &Origin) -> Option<Url> {
    if !matches!(main.scheme(), "http" | "https")
        || &main.origin() != daemon
        || !main.username().is_empty()
        || main.password().is_some()
    {
        return None;
    }
    let namespace = main.path().strip_prefix("/s/")?.split('/').next()?;
    // Only a namespace followed by a slash, as the app's `appBase` matches.
    if namespace.is_empty() || !main.path()[3 + namespace.len()..].starts_with('/') {
        return None;
    }
    let mut url = main.clone();
    url.set_path(&format!("/s/{namespace}/"));
    url.set_query(Some("page=tile-check"));
    url.set_fragment(None);
    Some(url)
}

/// Opens the check window at `url`, or does nothing when it is open. `daemon`
/// reads the origin the app is attached to now, which the window's
/// navigations are decided against as the main window's are.
pub fn open<R: Runtime>(
    app: &AppHandle<R>,
    url: Url,
    daemon: Arc<dyn Fn() -> Option<Origin> + Send + Sync>,
) -> tauri::Result<()> {
    let generation = GENERATION.load(Ordering::SeqCst);
    let _guard = LOCK.lock().unwrap_or_else(|poisoned| poisoned.into_inner());
    if app.get_webview_window(LABEL).is_some() {
        return Ok(());
    }
    let window = WebviewWindowBuilder::new(app, LABEL, WebviewUrl::External(url))
        .title("baum tile check")
        .inner_size(WIDTH, HEIGHT)
        // Never shown: WebKitGTK keeps running a hidden window's page, with
        // timers slowed to about once a second and no animation frames.
        .visible(false)
        .skip_taskbar(true)
        .focused(false)
        .focusable(false)
        .decorations(false)
        // The page reads the same declaration as the main window's.
        .initialization_script(declaration::script(declaration::SHELL))
        // The main window's rules, without the setup screen. Never opens
        // anything (see `navigation::decide`).
        .on_navigation(move |url| {
            navigation::decide(url, daemon().as_ref(), false) == Decision::Allow
        })
        // Nothing in a window the person cannot see opens the browser.
        .on_new_window(|_, _| tauri::webview::NewWindowResponse::Deny)
        .build()?;
    // The main window loaded another page or the app left the daemon while
    // the window was being built.
    if GENERATION.load(Ordering::SeqCst) != generation {
        destroy(app, &window);
    }
    Ok(())
}

/// Closes the check window from another thread, for callers on the main
/// thread: `open` holds the lock while the main thread builds the window.
pub fn close_soon<R: Runtime>(app: &AppHandle<R>) {
    // Before the check below: a window still being built closes itself.
    GENERATION.fetch_add(1, Ordering::SeqCst);
    if app.get_webview_window(LABEL).is_none() {
        return;
    }
    let app = app.clone();
    std::thread::spawn(move || close(&app));
}

/// Closes the check window if it is open. On Linux it first ends the
/// window's web process: destroying the window alone leaves a process that
/// is running a script that never yields at full CPU. Not on the main thread
/// (see `close_soon`).
pub fn close<R: Runtime>(app: &AppHandle<R>) {
    let _guard = LOCK.lock().unwrap_or_else(|poisoned| poisoned.into_inner());
    if let Some(window) = app.get_webview_window(LABEL) {
        destroy(app, &window);
    }
}

/// Ends the window's web process, destroys the window and waits until it is
/// gone, so an `open` right after builds a new one. Holding `LOCK`, and not
/// on the main thread.
///
/// A page that still answers is closed first and given `UNLOAD_GRACE`, so a
/// check it was drawing is reported abandoned (its `pagehide`) and ends as
/// unavailable, rather than as a hang that blocks a tile type which did
/// nothing wrong: the main window loading another page closes the window
/// mid-check. Ending the process at once skips `pagehide`. A page that never
/// yields ignores the close and is ended after the grace.
fn destroy<R: Runtime>(app: &AppHandle<R>, window: &tauri::WebviewWindow<R>) {
    #[cfg(target_os = "linux")]
    {
        // wry destroys the web view when its page agrees to close.
        let _ = window.with_webview(|webview| {
            use webkit2gtk::WebViewExt;
            webview.inner().try_close();
        });
        std::thread::sleep(UNLOAD_GRACE);
    }
    // Both calls run on the main thread, in this order.
    #[cfg(target_os = "linux")]
    if let Err(e) = window.with_webview(|webview| {
        use webkit2gtk::WebViewExt;
        webview.inner().terminate_web_process();
    }) {
        eprintln!("baum desktop: cannot end the check window's web process: {e}");
    }
    if let Err(e) = window.destroy() {
        eprintln!("baum desktop: cannot close the check window: {e}");
        return;
    }
    // `destroy` only asks; the window stays registered until it is destroyed.
    let start = Instant::now();
    while app.get_webview_window(LABEL).is_some() && start.elapsed() < GONE_TIMEOUT {
        std::thread::sleep(Duration::from_millis(5));
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn url(s: &str) -> Url {
        Url::parse(s).unwrap()
    }

    #[test]
    fn the_check_page_is_the_namespace_root_of_the_main_page() {
        let daemon = url("http://127.0.0.1:4100/").origin();
        for main in [
            "http://127.0.0.1:4100/s/abc/",
            "http://127.0.0.1:4100/s/abc/?page=jobs#x",
            "http://127.0.0.1:4100/s/abc/sample/?page=review&id=7",
            "http://127.0.0.1:4100/s/abc/api/x",
        ] {
            assert_eq!(
                page_url(&url(main), &daemon).map(String::from).as_deref(),
                Some("http://127.0.0.1:4100/s/abc/?page=tile-check"),
                "{main}"
            );
        }
    }

    #[test]
    fn no_check_page_without_a_daemon_page() {
        let daemon = url("http://127.0.0.1:4100/").origin();
        for main in [
            "http://127.0.0.1:4100/",
            "http://127.0.0.1:4100/#token=x",
            "http://127.0.0.1:4100/s/",
            "http://127.0.0.1:4100/s//",
            "http://127.0.0.1:4100/s/abc",
            "http://127.0.0.1:4200/s/abc/",
            "http://user@127.0.0.1:4100/s/abc/",
            "https://example.com/s/abc/",
            "tauri://localhost/index.html",
            "tauri://localhost/setup.html",
        ] {
            assert_eq!(page_url(&url(main), &daemon), None, "{main}");
        }
    }

    /// The window loads only what the main window may: the page it opens at
    /// is allowed, and another site or local server is refused.
    #[test]
    fn the_check_page_passes_the_main_windows_rules() {
        let daemon = url("http://127.0.0.1:4100/").origin();
        let page = page_url(&url("http://127.0.0.1:4100/s/abc/"), &daemon).unwrap();
        assert_eq!(
            navigation::decide(&page, Some(&daemon), false),
            Decision::Allow
        );
        assert_eq!(navigation::decide(&page, None, false), Decision::Deny);
    }
}

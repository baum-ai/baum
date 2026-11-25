//! What the shell tells the page in its main window and its check window,
//! before the page's own scripts run: `window.__BAUM_DESKTOP__`, a frozen
//! object the page can read but not replace. The app reads it to decide what
//! this shell can host.

/// What this shell offers the page.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Declaration {
    /// `tileFrames`: see `TILE_FRAMES`.
    pub tile_frames: bool,
    /// `checkWindow`: whether the page can open the tile check window
    /// (`CHECK_WINDOW`).
    pub check_window: bool,
    /// `openLinks`: whether the page may open links to other sites with the
    /// `open_link` command (see `links`). Every shell with the command says
    /// so; an older one does not, and the page asks for a new window there.
    pub open_links: bool,
}

/// What this build of the shell declares.
pub const SHELL: Declaration = Declaration {
    tile_frames: TILE_FRAMES,
    check_window: CHECK_WINDOW,
    open_links: true,
};

/// Whether the page may embed sandboxed frames of untrusted content served
/// from the daemon's origin. On macOS and Linux the shell refuses every
/// navigation of such a frame away from the app and injects its scripts, the
/// app's command bridge included, into the page's main frame only, so a frame
/// can neither leave the app nor call an app command.
#[cfg(not(windows))]
pub const TILE_FRAMES: bool = true;

/// Off on Windows until checked there. Tauri injects its scripts into every
/// frame on Windows, and WebView2 asks the shell only about the page's own
/// navigations. Before turning this on, run on a Windows machine a sandboxed
/// frame (`sandbox="allow-scripts"`) served from the daemon's origin that
/// tries every way to call an app command (`__TAURI_INTERNALS__.invoke`,
/// `window.chrome.webview.postMessage`, `fetch` to `http://ipc.localhost`)
/// and confirm that no command runs, as the WebKitGTK harness did (in git
/// history under tests/navigation; tests/shell checks the Electron shell).
#[cfg(windows)]
pub const TILE_FRAMES: bool = false;

/// Whether the page may open the tile check window (`check_window`), a
/// hidden second window for render checks of custom tiles. On Linux it was
/// checked under WebKitGTK (the harness in git history under
/// tests/check-window; tests/shell checks the Electron shell): the window
/// gets its own web process, a page spinning in it leaves the main window's
/// timers running, and ending its web process before destroying the window
/// stops the spin at once.
#[cfg(target_os = "linux")]
pub const CHECK_WINDOW: bool = true;

/// Off on macOS until checked there; the app then runs its checks in the main
/// window. WKWebView likely gives the window its own web content process, but
/// it has no public call to end one: destroying the window may leave a
/// spinning process running. Before turning this on, run on a Mac what the
/// WebKitGTK check-window harness did: that the window's page runs in a
/// web content process other than the main window's (Activity Monitor), that
/// a frame spinning in it leaves the main window responsive, that
/// `close_check_window` ends that process's CPU use within a second, that a
/// window that is hidden (or placed out of sight) still runs timers, and that
/// a new window works afterwards.
#[cfg(target_os = "macos")]
pub const CHECK_WINDOW: bool = false;

/// Off on Windows, where the page embeds no tile frames at all
/// (`TILE_FRAMES`).
#[cfg(not(any(target_os = "linux", target_os = "macos")))]
pub const CHECK_WINDOW: bool = false;

/// The initialization script that declares what the shell offers. It defines
/// the value only in the top-level document: Tauri runs it in the main frame
/// only on macOS and Linux, but in every frame on Windows.
pub fn script(declaration: Declaration) -> String {
    let Declaration {
        tile_frames,
        check_window,
        open_links,
    } = declaration;
    format!(
        "if (window.top === window) Object.defineProperty(window, \"__BAUM_DESKTOP__\", \
         {{ value: Object.freeze({{ tileFrames: {tile_frames}, checkWindow: {check_window}, \
         openLinks: {open_links} }}), \
         writable: false, configurable: false, enumerable: false }});"
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn frames_are_declared_only_where_the_shell_was_checked() {
        assert_eq!(TILE_FRAMES, !cfg!(windows));
    }

    #[test]
    fn the_check_window_is_declared_only_where_the_shell_was_checked() {
        assert_eq!(CHECK_WINDOW, cfg!(target_os = "linux"));
    }

    #[test]
    fn the_script_freezes_the_declaration_in_the_top_document() {
        for (declaration, value) in [
            (
                Declaration {
                    tile_frames: true,
                    check_window: true,
                    open_links: true,
                },
                "tileFrames: true, checkWindow: true, openLinks: true",
            ),
            (
                Declaration {
                    tile_frames: false,
                    check_window: false,
                    open_links: false,
                },
                "tileFrames: false, checkWindow: false, openLinks: false",
            ),
        ] {
            let script = script(declaration);
            assert!(
                script.starts_with("if (window.top === window) "),
                "{script}"
            );
            assert!(
                script.contains(&format!(
                    "Object.defineProperty(window, \"__BAUM_DESKTOP__\", {{ value: Object.freeze({{ {value} }}), writable: false, configurable: false"
                )),
                "{script}"
            );
        }
        assert_eq!(
            script(SHELL).contains("tileFrames: true"),
            cfg!(not(windows))
        );
        assert_eq!(
            script(SHELL).contains("checkWindow: true"),
            cfg!(target_os = "linux")
        );
        assert!(script(SHELL).contains("openLinks: true"));
    }
}

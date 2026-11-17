//! What the shell tells the page in its main window, before the page's own
//! scripts run: `window.__BAUM_DESKTOP__`, a frozen object the page can read
//! but not replace. The app reads it to decide what this shell can host.

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
/// and confirm that no command runs, as `just desktop::navigation-check`
/// does for WebKitGTK.
#[cfg(windows)]
pub const TILE_FRAMES: bool = false;

/// The initialization script that declares what the shell offers. It defines
/// the value only in the top-level document: Tauri runs it in the main frame
/// only on macOS and Linux, but in every frame on Windows.
pub fn script(tile_frames: bool) -> String {
    format!(
        "if (window.top === window) Object.defineProperty(window, \"__BAUM_DESKTOP__\", \
         {{ value: Object.freeze({{ tileFrames: {tile_frames} }}), writable: false, \
         configurable: false, enumerable: false }});"
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
    fn the_script_freezes_the_declaration_in_the_top_document() {
        for (frames, value) in [(true, "tileFrames: true"), (false, "tileFrames: false")] {
            let script = script(frames);
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
            script(TILE_FRAMES).contains("tileFrames: true"),
            cfg!(not(windows))
        );
    }
}

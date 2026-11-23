//! Tells the page when the pointer leaves the window, on Linux.
//!
//! On Wayland a leave event carries no position, so GTK3 fills in the last
//! one it saw, still inside the window after a quick move. WebKitGTK before
//! the fix for https://bugs.webkit.org/show_bug.cgi?id=326201 passes that to
//! the page as a move inside the view: the page never sees the pointer leave,
//! and the folded sidebar opened under it stays open over the page. The window
//! therefore announces the leave itself, as a `baum:pointer-left` event.

/// The script the window runs on each leave.
#[cfg(target_os = "linux")]
const SCRIPT: &str = r#"window.dispatchEvent(new Event("baum:pointer-left"))"#;

/// Starts announcing leaves from the main window's web view.
#[cfg(target_os = "linux")]
pub fn watch(window: &tauri::WebviewWindow) {
    let target = window.clone();
    if let Err(e) = window.with_webview(move |webview| {
        use gtk::prelude::WidgetExt;
        webview.inner().connect_leave_notify_event(move |_, event| {
            // A grab, such as a native menu opening, moves no pointer, and an
            // inferior leave stays inside the view.
            if event.mode() == gtk::gdk::CrossingMode::Normal
                && event.detail() != gtk::gdk::NotifyType::Inferior
                && let Err(e) = target.eval(SCRIPT)
            {
                eprintln!("baum desktop: cannot tell the page the pointer left: {e}");
            }
            gtk::glib::Propagation::Proceed
        });
    }) {
        eprintln!("baum desktop: cannot watch the pointer leave the window: {e}");
    }
}

#[cfg(not(target_os = "linux"))]
pub fn watch(_: &tauri::WebviewWindow) {}

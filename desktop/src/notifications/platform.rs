//! The operating system's notifications, behind a trait so the rest of the
//! module is tested without one. The real implementation uses `notify-rust`
//! directly rather than Tauri's notification plugin: the plugin reports no
//! clicks on desktop and would add a plugin permission to the app, while
//! `notify-rust` is used only from Rust and gives pages nothing.

use super::settings::Permission;
use super::toast::Toast;

#[derive(Debug, PartialEq, Eq)]
pub enum ShowError {
    /// The operating system refuses notifications from the app. The
    /// `notify-rust` backends do not report a refusal today; the notifier
    /// handles one for any platform that does.
    #[cfg_attr(not(test), allow(dead_code))]
    Denied,
    Failed(String),
}

pub trait Platform: Send + Sync {
    /// Asks the operating system to let the app notify. Called once, when
    /// the first notification is due, never at launch.
    fn request_permission(&self) -> Permission;

    /// Shows the toast. `on_click` runs if the user clicks it, on a thread
    /// that waits for that; a toast nobody clicks is never reported.
    fn show(&self, toast: &Toast, on_click: Box<dyn FnOnce() + Send>) -> Result<(), ShowError>;
}

/// Desktop notifications through the platform's own service: the freedesktop
/// notification service on Linux, Notification Center on macOS and toasts on
/// Windows.
pub struct Os;

/// Threads waiting for a click, bounded so a flood of unread notifications
/// cannot pile up threads.
const MAX_WAITING: usize = 32;
static WAITING: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);

#[cfg(all(unix, not(target_os = "macos")))]
fn supports_markup() -> bool {
    notify_rust::get_capabilities().is_ok_and(|caps| caps.iter().any(|c| c == "body-markup"))
}

/// Notification servers that render markup would show a title like
/// `Fix <b>x</b>` as bold text, so its characters are escaped; the title the
/// user sees is GitHub's, unchanged.
pub fn escape_markup(text: &str) -> String {
    text.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
}

impl Platform for Os {
    fn request_permission(&self) -> Permission {
        // Linux and Windows have no per-app permission, and macOS asks the
        // user when the first notification is posted, which this call
        // precedes. A refusal there is not reported to the app.
        Permission::Granted
    }

    fn show(&self, toast: &Toast, on_click: Box<dyn FnOnce() + Send>) -> Result<(), ShowError> {
        use std::sync::atomic::Ordering;

        let mut notification = notify_rust::Notification::new();
        notification.appname("baum").summary(&toast.title);
        #[cfg(all(unix, not(target_os = "macos")))]
        {
            let body = if supports_markup() {
                escape_markup(&toast.body)
            } else {
                toast.body.clone()
            };
            notification.body(&body).action("default", "Open");
        }
        #[cfg(not(all(unix, not(target_os = "macos"))))]
        notification.body(&toast.body);
        #[cfg(windows)]
        notification.app_id("com.usebaum.desktop");
        let handle = notification
            .show()
            .map_err(|e| ShowError::Failed(e.to_string()))?;
        if WAITING.load(Ordering::Relaxed) >= MAX_WAITING {
            return Ok(());
        }
        WAITING.fetch_add(1, Ordering::Relaxed);
        std::thread::spawn(move || {
            handle.wait_for_action(|action| {
                if action != "__closed" {
                    on_click();
                }
            });
            WAITING.fetch_sub(1, Ordering::Relaxed);
        });
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn markup_characters_are_escaped() {
        assert_eq!(
            escape_markup("Fix <b>x</b> & y"),
            "Fix &lt;b&gt;x&lt;/b&gt; &amp; y"
        );
    }
}

//! When the app and the daemon stop.
//!
//! The daemon outlives the app. Closing the window hides it, so reopening it
//! shows the same page in the same state, and the tray's Quit ends only the
//! app. "Quit and stop background reviews" also runs `baum daemon stop`,
//! which cancels running jobs, records them as interrupted and marks the
//! daemon stopped by the user, so agent hooks do not start it again. Where no
//! tray is available (some Linux desktops) nothing could reopen a hidden
//! window, so closing it quits the app after a confirmation.

use std::sync::atomic::{AtomicBool, Ordering};
use std::thread;
use std::time::Duration;

use tauri::{AppHandle, Manager, Window, WindowEvent};
use tauri_plugin_dialog::{DialogExt, MessageDialogButtons, MessageDialogKind};

use crate::{daemon, tray};

/// `baum daemon stop` waits for the daemon's own 20 s shutdown, plus a margin.
const STOP_TIMEOUT: Duration = Duration::from_secs(45);

/// Set while a confirmation is showing, so repeated clicks of the close
/// button or the tray item do not stack dialogs.
static PROMPT_OPEN: AtomicBool = AtomicBool::new(false);

/// Claims the one confirmation slot; false when a dialog is already up.
fn claim_prompt(flag: &AtomicBool) -> bool {
    !flag.swap(true, Ordering::SeqCst)
}

#[derive(Debug, PartialEq, Eq)]
pub enum CloseAction {
    /// Hide the window; the tray reopens it.
    Hide,
    /// Ask whether to quit the app.
    ConfirmQuit,
}

pub fn close_action(tray_available: bool) -> CloseAction {
    if tray_available {
        CloseAction::Hide
    } else {
        CloseAction::ConfirmQuit
    }
}

pub const NO_TRAY_PROMPT: &str = "This desktop has no system tray, so baum cannot stay in the background with its window closed. Quit baum?\n\nBackground reviews keep running; quitting only closes the app. Run \"baum daemon stop\" to stop them.";

/// What to ask before stopping the daemon, or `None` when nothing running
/// would be interrupted. Queued jobs are not interrupted: they have not
/// started.
pub fn stop_prompt(running: usize, queued: usize) -> Option<String> {
    if running == 0 {
        return None;
    }
    let jobs = if running == 1 {
        "1 review is running".to_string()
    } else {
        format!("{running} reviews are running")
    };
    let queued = match queued {
        0 => String::new(),
        n => format!(" and {n} queued"),
    };
    Some(format!(
        "{jobs}{queued}. Stopping background reviews will interrupt the running {} and record {} as interrupted. An interrupted automatic review is retried once when background reviews start again.",
        if running == 1 { "review" } else { "reviews" },
        if running == 1 { "it" } else { "them" }
    ))
}

/// The error shown when the daemon will not stop. The daemon keeps running,
/// and says so, because quitting the app was not what the user asked for.
fn stop_failure_text(message: &str, details: &[String]) -> String {
    let mut text = format!(
        "{message}\n\nThe daemon may still be running. Hooks will not restart a daemon you stopped until `baum daemon ensure` or the baum app starts it."
    );
    if !details.is_empty() {
        text.push_str("\n\n");
        text.push_str(&details.join("\n"));
    }
    text
}

/// The window's close button and focus.
pub fn on_window_event(window: &Window, event: &WindowEvent) {
    let app = window.app_handle();
    match event {
        WindowEvent::CloseRequested { api, .. } => {
            api.prevent_close();
            match close_action(tray::available(app)) {
                CloseAction::Hide => {
                    let _ = window.hide();
                }
                CloseAction::ConfirmQuit => {
                    confirm(app, "Quit baum?", NO_TRAY_PROMPT, "Quit", |app| app.exit(0))
                }
            }
        }
        WindowEvent::Focused(true) => tray::window_seen(app),
        _ => {}
    }
}

/// Brings the window back as it was.
pub fn show_window(app: &AppHandle) {
    if let Some(window) = app.get_webview_window(crate::MAIN) {
        let _ = window.unminimize();
        let _ = window.show();
        let _ = window.set_focus();
    }
    tray::window_seen(app);
}

/// Quits the app and leaves the daemon running.
pub fn quit(app: &AppHandle) {
    app.exit(0);
}

/// Stops the daemon, then quits. Asks first when that would interrupt a
/// running review, and does not quit when the daemon will not stop.
pub fn quit_and_stop(app: &AppHandle) {
    let app = app.clone();
    thread::spawn(move || {
        let (running, queued) = tray::job_counts(&app);
        match stop_prompt(running, queued) {
            Some(text) => confirm(
                &app,
                "Stop background reviews?",
                &text,
                "Stop and quit",
                stop_and_exit,
            ),
            None => stop_and_exit(&app),
        }
    });
}

fn stop_and_exit(app: &AppHandle) {
    let app = app.clone();
    thread::spawn(move || {
        let stopped =
            daemon::cli_path().and_then(|cli| daemon::run(&cli, &["daemon", "stop"], STOP_TIMEOUT));
        match stopped {
            Ok(_) => app.exit(0),
            Err(failure) => message(
                &app,
                "Cannot stop background reviews",
                &stop_failure_text(&failure.message, &failure.details),
            ),
        }
    });
}

/// Asks a yes/no question without blocking the caller; `then` runs on yes.
pub fn confirm(
    app: &AppHandle,
    title: &str,
    text: &str,
    yes: &str,
    then: impl FnOnce(&AppHandle) + Send + 'static,
) {
    if !claim_prompt(&PROMPT_OPEN) {
        return;
    }
    let handle = app.clone();
    app.dialog()
        .message(text)
        .title(title)
        .kind(MessageDialogKind::Warning)
        .buttons(MessageDialogButtons::OkCancelCustom(
            yes.to_string(),
            "Cancel".to_string(),
        ))
        .show(move |confirmed| {
            PROMPT_OPEN.store(false, Ordering::SeqCst);
            if confirmed {
                then(&handle);
            }
        });
}

/// Shows an error without blocking the caller.
pub fn message(app: &AppHandle, title: &str, text: &str) {
    app.dialog()
        .message(text)
        .title(title)
        .kind(MessageDialogKind::Error)
        .buttons(MessageDialogButtons::Ok)
        .show(|_| {});
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn closing_hides_the_window_when_a_tray_can_reopen_it() {
        assert_eq!(close_action(true), CloseAction::Hide);
        assert_eq!(close_action(false), CloseAction::ConfirmQuit);
    }

    #[test]
    fn only_one_confirmation_shows_at_a_time() {
        let flag = AtomicBool::new(false);
        assert!(claim_prompt(&flag));
        assert!(!claim_prompt(&flag));
        flag.store(false, Ordering::SeqCst);
        assert!(claim_prompt(&flag));
    }

    #[test]
    fn a_failed_stop_says_hooks_will_not_restart_the_daemon() {
        let text = stop_failure_text(
            "baum daemon stop failed (exit status: 1).",
            &["timeout".into()],
        );
        assert!(text.contains("Hooks will not restart"), "{text}");
        assert!(text.contains("baum daemon ensure"), "{text}");
        assert!(text.ends_with("timeout"), "{text}");
    }

    #[test]
    fn stopping_with_nothing_running_needs_no_confirmation() {
        assert_eq!(stop_prompt(0, 0), None);
        assert_eq!(stop_prompt(0, 4), None);
    }

    #[test]
    fn stopping_with_a_running_review_says_it_will_be_interrupted() {
        let one = stop_prompt(1, 0).unwrap();
        assert!(one.starts_with("1 review is running."), "{one}");
        assert!(one.contains("will interrupt the running review"), "{one}");
        let many = stop_prompt(2, 3).unwrap();
        assert!(
            many.starts_with("2 reviews are running and 3 queued."),
            "{many}"
        );
        assert!(many.contains("interrupt the running reviews"), "{many}");
    }
}

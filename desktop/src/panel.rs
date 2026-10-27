//! The tray panel: a small borderless window under the tray icon, drawn like
//! a menu but with meters, which a left click on the icon opens on macOS and
//! Windows. It shows what the tray menu does: what the daemon is doing,
//! anything waiting on the user, what is left of the agents' plans, and
//! Open, pause and Quit and stop. Linux tray hosts report no clicks, so
//! Linux has the menu only.
//!
//! The page is the bundled `tray.html`. Its capability allows exactly the
//! three commands here, and only in this window: it reads the tray's last
//! view, sets its own height, and runs one of the panel's actions. It loses
//! focus, it hides.

use std::sync::Mutex;
use std::time::{Duration, Instant};

use serde::Serialize;
use tauri::{
    AppHandle, Manager, PhysicalPosition, Rect, WebviewUrl, WebviewWindowBuilder, Window,
    WindowEvent,
};

use crate::status::{self, NeedsYou, Plan};
use crate::{navigation, tray};

pub const LABEL: &str = "tray-panel";
const WIDTH: f64 = 340.0;
/// Space between the icon and the panel, and the panel and a screen edge.
const GAP: f64 = 6.0;
/// A click on the icon while the panel shows first takes its focus, which
/// hides it; the click that follows must not show it again.
const REOPEN_GRACE: Duration = Duration::from_millis(300);

static HIDDEN_AT: Mutex<Option<Instant>> = Mutex::new(None);

/// What the panel page draws.
#[derive(Debug, Serialize)]
pub struct Panel {
    pub summary: String,
    pub waiting: Option<String>,
    pub attention: Vec<String>,
    pub needs: Vec<NeedsYou>,
    /// The global automatic switch; `None` while unknown.
    pub automatic: Option<bool>,
    pub pause_label: &'static str,
    /// Only plans the daemon could read; empty when there are none.
    pub plans: Vec<Plan>,
}

fn panel(view: &status::View, plans: Vec<Plan>) -> Panel {
    Panel {
        summary: view.summary.clone(),
        waiting: view.waiting_label(),
        attention: view.attention.clone(),
        needs: view.needs.clone(),
        automatic: view.automatic,
        pause_label: status::pause_label(view.automatic),
        plans,
    }
}

/// Creates the hidden panel window, once, at startup: building a webview
/// from a tray event can deadlock on Windows.
pub fn create(app: &AppHandle) -> tauri::Result<()> {
    if cfg!(target_os = "linux") {
        return Ok(());
    }
    let bundled = navigation::bundled_screen().origin();
    WebviewWindowBuilder::new(app, LABEL, WebviewUrl::App("tray.html".into()))
        .title("baum")
        .inner_size(WIDTH, 360.0)
        .resizable(false)
        .maximizable(false)
        .minimizable(false)
        .decorations(false)
        .always_on_top(true)
        .skip_taskbar(true)
        .visible_on_all_workspaces(true)
        .visible(false)
        .focused(false)
        .on_navigation(move |url| url.origin() == bundled)
        .build()?;
    Ok(())
}

/// The tray icon's left click: shows the panel under (or above) the icon, or
/// hides it.
pub fn toggle(app: &AppHandle, icon: Rect) {
    let Some(window) = app.get_webview_window(LABEL) else {
        return;
    };
    if window.is_visible().unwrap_or(false) {
        hide(&window);
        return;
    }
    let just_hidden = HIDDEN_AT
        .lock()
        .unwrap_or_else(|p| p.into_inner())
        .is_some_and(|at| at.elapsed() < REOPEN_GRACE);
    if just_hidden {
        return;
    }
    let scale = window.scale_factor().unwrap_or(1.0);
    let position = icon.position.to_physical::<f64>(scale);
    let size = icon.size.to_physical::<f64>(scale);
    let icon = Area {
        x: position.x,
        y: position.y,
        width: size.width,
        height: size.height,
    };
    let screen = app
        .monitor_from_point(icon.x + icon.width / 2.0, icon.y + icon.height / 2.0)
        .ok()
        .flatten()
        .map(|monitor| {
            let area = monitor.work_area();
            Area {
                x: f64::from(area.position.x),
                y: f64::from(area.position.y),
                width: f64::from(area.size.width),
                height: f64::from(area.size.height),
            }
        });
    if let (Some(screen), Ok(panel)) = (screen, window.outer_size()) {
        let (x, y) = place(
            &icon,
            (f64::from(panel.width), f64::from(panel.height)),
            &screen,
            GAP * scale,
        );
        let _ = window.set_position(PhysicalPosition::new(x, y));
    }
    let _ = window.show();
    let _ = window.set_focus();
}

fn hide(window: &tauri::WebviewWindow) {
    let _ = window.hide();
    *HIDDEN_AT.lock().unwrap_or_else(|p| p.into_inner()) = Some(Instant::now());
}

/// The panel's own window events: it hides when it loses focus or is asked
/// to close, and is never destroyed.
pub fn on_window_event(window: &Window, event: &WindowEvent) {
    let Some(window) = window.app_handle().get_webview_window(LABEL) else {
        return;
    };
    match event {
        WindowEvent::CloseRequested { api, .. } => {
            api.prevent_close();
            hide(&window);
        }
        WindowEvent::Focused(false) => hide(&window),
        _ => {}
    }
}

/// A rectangle in physical pixels.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Area {
    pub x: f64,
    pub y: f64,
    pub width: f64,
    pub height: f64,
}

/// Where the panel goes: centred on the icon and kept on its screen, below
/// an icon in the top half (the macOS menu bar, a top panel) and above one in
/// the bottom half (the Windows taskbar).
pub fn place(icon: &Area, panel: (f64, f64), screen: &Area, gap: f64) -> (f64, f64) {
    let (width, height) = panel;
    let left = screen.x + gap;
    let right = (screen.x + screen.width - width - gap).max(left);
    let x = (icon.x + icon.width / 2.0 - width / 2.0).clamp(left, right);
    let top = screen.y + gap;
    let bottom = (screen.y + screen.height - height - gap).max(top);
    let below = icon.y + icon.height / 2.0 < screen.y + screen.height / 2.0;
    let y = if below {
        icon.y + icon.height + gap
    } else {
        icon.y - height - gap
    };
    (x, y.clamp(top, bottom))
}

/// The panel page asks what to draw.
#[tauri::command]
pub fn tray_panel(app: AppHandle) -> Option<Panel> {
    let (view, plans) = tray::snapshot(&app)?;
    Some(panel(&view, plans))
}

/// The panel page's height in CSS pixels, so the window fits it.
#[tauri::command]
pub fn tray_panel_height(app: AppHandle, height: f64) {
    if let Some(window) = app.get_webview_window(LABEL) {
        let height = if height.is_finite() { height } else { 360.0 };
        let _ = window.set_size(tauri::LogicalSize::new(WIDTH, height.clamp(80.0, 720.0)));
    }
}

/// The panel's actions: the tray menu's, of which it offers Open, pause or
/// resume, Quit and stop, and the waiting sessions. Anything else is refused.
#[tauri::command]
pub fn tray_action(app: AppHandle, id: String) -> Result<(), String> {
    if !allowed(&id) {
        return Err(format!("the panel has no action {id:?}"));
    }
    if let Some(window) = app.get_webview_window(LABEL) {
        hide(&window);
    }
    tray::on_menu(&app, &id);
    Ok(())
}

fn allowed(id: &str) -> bool {
    matches!(id, "open" | "pause" | "quit-stop")
        || id
            .strip_prefix(tray::SESSION_PREFIX)
            .is_some_and(|key| !key.is_empty())
}

#[cfg(test)]
mod tests {
    use super::*;

    const SCREEN: Area = Area {
        x: 0.0,
        y: 0.0,
        width: 1440.0,
        height: 900.0,
    };

    #[test]
    fn under_a_menu_bar_icon_the_panel_hangs_below_it_centred() {
        let icon = Area {
            x: 1000.0,
            y: 0.0,
            width: 24.0,
            height: 24.0,
        };
        assert_eq!(place(&icon, (340.0, 400.0), &SCREEN, 6.0), (842.0, 30.0));
    }

    #[test]
    fn above_a_taskbar_icon_the_panel_stands_on_it() {
        let icon = Area {
            x: 1200.0,
            y: 860.0,
            width: 32.0,
            height: 40.0,
        };
        assert_eq!(place(&icon, (340.0, 400.0), &SCREEN, 6.0), (1046.0, 454.0));
    }

    #[test]
    fn the_panel_stays_on_the_icons_screen() {
        let corner = Area {
            x: 1430.0,
            y: 0.0,
            width: 10.0,
            height: 24.0,
        };
        assert_eq!(place(&corner, (340.0, 400.0), &SCREEN, 6.0), (1094.0, 30.0));
        let second = Area {
            x: 1440.0,
            y: 0.0,
            width: 1920.0,
            height: 1080.0,
        };
        let icon = Area {
            x: 1450.0,
            y: 0.0,
            width: 24.0,
            height: 24.0,
        };
        assert_eq!(place(&icon, (340.0, 400.0), &second, 6.0), (1446.0, 30.0));
    }

    #[test]
    fn the_page_reaches_only_the_panels_actions() {
        for id in ["open", "pause", "quit-stop", "session:claude-code:a"] {
            assert!(allowed(id), "{id}");
        }
        for id in [
            "quit",
            "login",
            "setup",
            "session:",
            "notifications:review_finished",
            "",
        ] {
            assert!(!allowed(id), "{id}");
        }
    }

    #[test]
    fn the_panel_shows_the_views_lines_and_only_readable_plans() {
        let view = status::View::default();
        let shown = panel(&view, Vec::new());
        assert_eq!(shown.summary, view.summary);
        assert_eq!(shown.pause_label, "Pause automatic reviews");
        assert!(shown.plans.is_empty());
        let json = serde_json::to_value(&shown).unwrap();
        assert_eq!(json["plans"], serde_json::json!([]));
        assert_eq!(json["automatic"], serde_json::Value::Null);
    }
}

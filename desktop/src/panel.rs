//! The tray panel: a small borderless window under the tray icon, drawn like
//! a menu but with meters, which a left click on the icon opens on macOS and
//! Windows. It shows what the tray menu does: what the daemon is doing,
//! anything waiting on the user, what is left of the agents' plans, and
//! pause and Quit and stop. It has no Open item: the Inbox's count opens the
//! app, and what the daemon is doing opens the jobs list while reviews run or
//! wait. Linux tray hosts report no clicks, so
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
    AppHandle, LogicalPosition, Manager, PhysicalPosition, Rect, WebviewUrl, WebviewWindowBuilder,
    Window, WindowEvent,
};

use crate::status::{self, Alert, NeedsYou, Plan};
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
    /// Reviews run or wait, so the summary opens the jobs list (`summary`).
    pub jobs: bool,
    /// The Inbox's row, which opens the app: its count, or that it is empty
    /// once the daemon counted none. `None` while the count is unknown.
    pub waiting: Option<String>,
    /// What the icon is red for, each an action (`alert:…`).
    pub attention: Vec<Alert>,
    pub needs: Vec<NeedsYou>,
    /// The global automatic switch; `None` while unknown.
    pub automatic: Option<bool>,
    pub pause_label: &'static str,
    /// Only plans the daemon could read; empty when there are none.
    pub plans: Vec<Plan>,
}

/// The Inbox's row. The panel has no Open item, so it shows even when the
/// Inbox is empty, as mail apps say so; an unknown count shows nothing.
fn inbox_line(view: &status::View) -> Option<String> {
    match view.waiting {
        Some(0) => Some("Your Inbox is empty".to_string()),
        _ => view.waiting_label(),
    }
}

fn panel(view: &status::View, plans: Vec<Plan>) -> Panel {
    Panel {
        summary: view.summary.clone(),
        jobs: view.running + view.queued > 0,
        waiting: inbox_line(view),
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
        .on_navigation(navigation::is_tray_panel)
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
    // The tray reports the icon in physical pixels of the icon's screen.
    let current = window.scale_factor().unwrap_or(1.0);
    let position = icon.position.to_physical::<f64>(current);
    let size = icon.size.to_physical::<f64>(current);
    let icon = Area {
        x: position.x,
        y: position.y,
        width: size.width,
        height: size.height,
    };
    // `monitor_from_point` is not used: on macOS it compares the point with
    // the displays' bounds in points, not pixels, so on a Retina screen it
    // missed the icon and the panel opened wherever it last was, often the
    // middle of the screen.
    let screens: Vec<Screen> = app
        .available_monitors()
        .unwrap_or_default()
        .iter()
        .map(|m| Screen {
            bounds: Area {
                x: f64::from(m.position().x),
                y: f64::from(m.position().y),
                width: f64::from(m.size().width),
                height: f64::from(m.size().height),
            },
            work: Area {
                x: f64::from(m.work_area().position.x),
                y: f64::from(m.work_area().position.y),
                width: f64::from(m.work_area().size.width),
                height: f64::from(m.work_area().size.height),
            },
            scale: m.scale_factor(),
        })
        .collect();
    let Ok(panel) = window.outer_size() else {
        return show(&window);
    };
    // The panel's size in points, as it is drawn on any screen.
    let panel = (
        f64::from(panel.width) / current,
        f64::from(panel.height) / current,
    );
    if cfg!(target_os = "macos") {
        // Every macOS rectangle here is in points times its own screen's
        // scale, so a pixel of one screen is not a pixel of another and
        // their rectangles can overlap. The cursor, which is on the icon at
        // the click, is in points times the primary screen's scale: back in
        // points, it says which screen the icon is on.
        let primary = app
            .primary_monitor()
            .ok()
            .flatten()
            .map_or(current, |m| m.scale_factor());
        let cursor = app.cursor_position().ok().map_or(
            (
                (icon.x + icon.width / 2.0) / primary,
                (icon.y + icon.height / 2.0) / primary,
            ),
            |p| (p.x / primary, p.y / primary),
        );
        let points: Vec<Area> = screens
            .iter()
            .map(|s| s.bounds.scaled(1.0 / s.scale))
            .collect();
        let Some(screen) = screen_of(cursor, &points).map(|i| &screens[i]) else {
            eprintln!("baum desktop: no screen found for the tray icon");
            return show(&window);
        };
        let (x, y) = place(
            &icon.scaled(1.0 / screen.scale),
            panel,
            &screen.work.scaled(1.0 / screen.scale),
            GAP,
        );
        let _ = window.set_position(LogicalPosition::new(x, y));
    } else {
        // Elsewhere the icon and the screens share one pixel space.
        let centre = (icon.x + icon.width / 2.0, icon.y + icon.height / 2.0);
        let bounds: Vec<Area> = screens.iter().map(|s| s.bounds).collect();
        let Some(screen) = screen_of(centre, &bounds).map(|i| &screens[i]) else {
            eprintln!("baum desktop: no screen found for the tray icon");
            return show(&window);
        };
        let (x, y) = place(
            &icon,
            (panel.0 * screen.scale, panel.1 * screen.scale),
            &screen.work,
            GAP * screen.scale,
        );
        let _ = window.set_position(PhysicalPosition::new(x, y));
    }
    show(&window);
}

fn show(window: &tauri::WebviewWindow) {
    let _ = window.show();
    let _ = window.set_focus();
}

/// A monitor: its whole rectangle, the part windows may use, and its scale.
struct Screen {
    bounds: Area,
    work: Area,
    scale: f64,
}

/// The screen a point is on: the one containing it, else the nearest, so the
/// panel never stays where it last was. `None` only without screens.
pub fn screen_of((cx, cy): (f64, f64), monitors: &[Area]) -> Option<usize> {
    let distance = |m: &Area| {
        let dx = (m.x - cx).max(cx - (m.x + m.width)).max(0.0);
        let dy = (m.y - cy).max(cy - (m.y + m.height)).max(0.0);
        dx * dx + dy * dy
    };
    monitors
        .iter()
        .enumerate()
        .min_by(|(_, a), (_, b)| distance(a).total_cmp(&distance(b)))
        .map(|(i, _)| i)
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

/// A rectangle, in pixels or points.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Area {
    pub x: f64,
    pub y: f64,
    pub width: f64,
    pub height: f64,
}

impl Area {
    fn scaled(&self, by: f64) -> Area {
        Area {
            x: self.x * by,
            y: self.y * by,
            width: self.width * by,
            height: self.height * by,
        }
    }
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

/// The panel's actions: the tray menu's, of which it offers `open` (the
/// Inbox's count), `summary` (the busy status), pause or
/// resume, Quit and stop, the alerts and the waiting sessions, and `close` (Escape),
/// which only hides the panel. Anything else is refused.
#[tauri::command]
pub fn tray_action(app: AppHandle, id: String) -> Result<(), String> {
    if !allowed(&id) {
        return Err(format!("the panel has no action {id:?}"));
    }
    if let Some(window) = app.get_webview_window(LABEL) {
        hide(&window);
    }
    if id != "close" {
        tray::on_menu(&app, &id);
    }
    Ok(())
}

fn allowed(id: &str) -> bool {
    matches!(id, "open" | "summary" | "pause" | "quit-stop" | "close")
        || status::is_alert(id)
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
    fn the_point_finds_its_screen_in_points() {
        // A 2x built-in screen of 1512x982 points and an external 1x screen
        // to its right, as macOS reports them: points times each one's scale.
        let built_in = Area {
            x: 0.0,
            y: 0.0,
            width: 3024.0,
            height: 1964.0,
        };
        let external = Area {
            x: 1512.0,
            y: 0.0,
            width: 2560.0,
            height: 1440.0,
        };
        // In those pixels the screens overlap; in points they do not.
        let points = [built_in.scaled(0.5), external];
        assert_eq!(screen_of((1180.0, 12.0), &points), Some(0));
        assert_eq!(screen_of((2000.0, 12.0), &points), Some(1));
    }

    #[test]
    fn a_point_on_no_screen_goes_to_the_nearest_and_none_without_screens() {
        assert_eq!(screen_of((1500.0, -30.0), &[SCREEN]), Some(0));
        let right = Area {
            x: 1440.0,
            ..SCREEN
        };
        assert_eq!(screen_of((3000.0, 10.0), &[SCREEN, right]), Some(1));
        assert_eq!(screen_of((10.0, 10.0), &[]), None);
    }

    #[test]
    fn a_panel_placed_in_points_hangs_under_a_retina_menu_bar_icon() {
        // The icon at 1180 points on a 2x screen, reported in pixels.
        let icon = Area {
            x: 2360.0,
            y: 0.0,
            width: 48.0,
            height: 48.0,
        };
        let work = Area {
            x: 0.0,
            y: 74.0,
            width: 3024.0,
            height: 1890.0,
        };
        let (x, y) = place(&icon.scaled(0.5), (340.0, 400.0), &work.scaled(0.5), GAP);
        assert_eq!((x, y), (1022.0, 43.0));
    }

    #[test]
    fn the_page_reaches_only_the_panels_actions() {
        for id in [
            "open",
            "summary",
            "pause",
            "quit-stop",
            "close",
            "session:claude-code:a",
            "alert:job:6e280a68",
            "alert:jobs",
            "alert:diagnostics",
            "alert:daemon",
        ] {
            assert!(allowed(id), "{id}");
        }
        for id in [
            "quit",
            "login",
            "setup",
            "session:",
            "alert:job:",
            "alert:other",
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
        assert!(!shown.jobs);
        // An unknown count shows no Inbox row; zero says the Inbox is empty.
        assert_eq!(shown.waiting, None);
        let inbox = |waiting| {
            panel(
                &status::View {
                    waiting,
                    ..status::View::default()
                },
                Vec::new(),
            )
            .waiting
        };
        assert_eq!(inbox(Some(0)).as_deref(), Some("Your Inbox is empty"));
        assert_eq!(
            inbox(Some(12)).as_deref(),
            Some("12 pull requests in your Inbox")
        );
        let busy = status::View {
            running: 1,
            ..status::View::default()
        };
        assert!(panel(&busy, Vec::new()).jobs);
        assert!(shown.plans.is_empty());
        let json = serde_json::to_value(&shown).unwrap();
        assert_eq!(json["plans"], serde_json::json!([]));
        assert_eq!(json["automatic"], serde_json::Value::Null);
    }
}

//! The window's zoom: the webview's own page zoom (WebKitGTK, WebView2 or
//! WKWebView), so it scales every page the window shows, bundled or served by
//! the daemon, the same way on X11, Wayland, Windows and macOS. Ctrl (Cmd on
//! macOS) with `+`, `-` and `0` changes it on any page through `SCRIPT`, which
//! the window injects into every page it loads; the desktop Settings page
//! offers the levels too. The level is saved in `window.json` in the app's
//! configuration directory.

use std::fs;
use std::io;
use std::path::{Path, PathBuf};
use std::sync::Mutex;

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Manager, Webview};

const FILE: &str = "window.json";

/// The levels the shortcuts step through and Settings offers.
pub const LEVELS: [f64; 10] = [0.67, 0.75, 0.8, 0.9, 1.0, 1.1, 1.25, 1.5, 1.75, 2.0];
const DEFAULT: f64 = 1.0;

/// Handles the shortcuts on every page. It calls the app at the time of the
/// key press, so it works whether it runs before or after Tauri's own
/// scripts, and tells the page the new level with a `baum:zoom` event.
pub const SCRIPT: &str = include_str!("zoom.js");

#[derive(Debug, Clone, Copy, PartialEq, Serialize, Deserialize)]
#[serde(default)]
struct Saved {
    zoom: f64,
}

impl Default for Saved {
    fn default() -> Self {
        Saved { zoom: DEFAULT }
    }
}

/// The current level and where it is saved; `None` keeps it in memory only.
pub struct Zoom {
    level: Mutex<f64>,
    path: Option<PathBuf>,
}

impl Zoom {
    fn in_dir(dir: PathBuf) -> Zoom {
        let path = dir.join(FILE);
        Zoom {
            level: Mutex::new(load(&path)),
            path: Some(path),
        }
    }

    #[cfg(test)]
    fn in_memory() -> Zoom {
        Zoom {
            level: Mutex::new(DEFAULT),
            path: None,
        }
    }

    pub fn level(&self) -> f64 {
        *self.level.lock().unwrap_or_else(|p| p.into_inner())
    }

    /// Sets and saves a level from `LEVELS`; any other value is refused.
    fn set(&self, level: f64) -> Result<f64, String> {
        self.update(|_| level)
    }

    /// Replaces the level with `next(current)` under one lock, so two
    /// changes at once cannot both start from the same level.
    fn update(&self, next: impl FnOnce(f64) -> f64) -> Result<f64, String> {
        let mut current = self.level.lock().unwrap_or_else(|p| p.into_inner());
        let level = next(*current);
        if !LEVELS.contains(&level) {
            return Err(format!("{level} is not a zoom level"));
        }
        *current = level;
        if let Some(path) = &self.path
            && let Err(e) = save(path, level)
        {
            eprintln!("baum desktop: cannot save the zoom level: {e}");
        }
        Ok(level)
    }
}

/// The saved level; a missing, damaged or unknown one is 100%, so a bad file
/// never leaves the window unreadable.
fn load(path: &Path) -> f64 {
    let Ok(data) = fs::read(path) else {
        return DEFAULT;
    };
    match serde_json::from_slice::<Saved>(&data) {
        Ok(saved) if LEVELS.contains(&saved.zoom) => saved.zoom,
        Ok(_) => DEFAULT,
        Err(e) => {
            eprintln!("baum desktop: window settings unreadable, using defaults: {e}");
            DEFAULT
        }
    }
}

fn save(path: &Path, zoom: f64) -> io::Result<()> {
    if let Some(dir) = path.parent() {
        fs::create_dir_all(dir)?;
    }
    let temporary = path.with_extension("json.tmp");
    fs::write(&temporary, serde_json::to_vec_pretty(&Saved { zoom })?)?;
    fs::rename(&temporary, path)
}

/// The level one step from `current` in `direction` (1 in, -1 out, 0 back to
/// 100%), staying within `LEVELS`.
fn step(current: f64, direction: i8) -> f64 {
    let nearest = LEVELS
        .iter()
        .enumerate()
        .min_by(|a, b| (a.1 - current).abs().total_cmp(&(b.1 - current).abs()))
        .map_or(0, |(i, _)| i);
    match direction {
        0 => DEFAULT,
        d if d > 0 => LEVELS[(nearest + 1).min(LEVELS.len() - 1)],
        _ => LEVELS[nearest.saturating_sub(1)],
    }
}

/// Loads the saved level. Runs in `setup`, before the window exists.
pub fn install(app: &AppHandle) -> tauri::Result<()> {
    let dir = app
        .path()
        .app_config_dir()
        .map_err(|e| tauri::Error::Anyhow(e.into()))?;
    app.manage(Zoom::in_dir(dir));
    Ok(())
}

/// Applies the current level to a webview. The window calls it when each page
/// starts loading, since a webview may reset its zoom on navigation.
pub fn apply(webview: &Webview) {
    let level = webview.state::<Zoom>().level();
    if let Err(e) = webview.set_zoom(level) {
        eprintln!("baum desktop: cannot zoom the window: {e}");
    }
}

fn change(webview: &Webview, level: Result<f64, String>) -> Result<f64, String> {
    let level = level?;
    webview.set_zoom(level).map_err(|e| e.to_string())?;
    Ok(level)
}

#[derive(Debug, Serialize)]
pub struct ZoomState {
    level: f64,
    levels: &'static [f64],
}

/// The current level and the levels Settings offers.
#[tauri::command]
pub fn zoom_state(zoom: tauri::State<'_, Zoom>) -> ZoomState {
    ZoomState {
        level: zoom.level(),
        levels: &LEVELS,
    }
}

/// Settings' choice of level.
#[tauri::command]
pub fn set_zoom(webview: Webview, zoom: tauri::State<'_, Zoom>, level: f64) -> Result<f64, String> {
    change(&webview, zoom.set(level))
}

/// The shortcuts: one level in (1), out (-1) or back to 100% (0).
#[tauri::command]
pub fn step_zoom(
    webview: Webview,
    zoom: tauri::State<'_, Zoom>,
    direction: i8,
) -> Result<f64, String> {
    change(&webview, zoom.update(|current| step(current, direction)))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn scratch(name: &str) -> PathBuf {
        let dir =
            std::env::temp_dir().join(format!("baum-desktop-zoom-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        dir
    }

    #[test]
    fn steps_stay_within_the_levels() {
        assert_eq!(step(1.0, 1), 1.1);
        assert_eq!(step(1.0, -1), 0.9);
        assert_eq!(step(2.0, 1), 2.0);
        assert_eq!(step(0.67, -1), 0.67);
        assert_eq!(step(1.75, 0), 1.0);
        // A level between steps moves from the nearest one.
        assert_eq!(step(1.3, 1), 1.5);
    }

    #[test]
    fn only_offered_levels_are_accepted() {
        let zoom = Zoom::in_memory();
        assert!(zoom.set(3.0).is_err());
        assert!(zoom.set(f64::NAN).is_err());
        assert_eq!(zoom.level(), DEFAULT);
        assert_eq!(zoom.set(1.25), Ok(1.25));
        assert_eq!(zoom.level(), 1.25);
    }

    #[test]
    fn the_level_survives_a_restart() {
        let dir = scratch("roundtrip");
        Zoom::in_dir(dir.clone()).set(1.5).unwrap();
        assert_eq!(Zoom::in_dir(dir.clone()).level(), 1.5);
        let _ = fs::remove_dir_all(dir);
    }

    #[test]
    fn a_damaged_or_unknown_level_is_the_default() {
        for (name, content) in [("damaged", "{"), ("unknown", r#"{"zoom":7}"#)] {
            let dir = scratch(name);
            fs::create_dir_all(&dir).unwrap();
            fs::write(dir.join(FILE), content).unwrap();
            assert_eq!(Zoom::in_dir(dir.clone()).level(), DEFAULT);
            let _ = fs::remove_dir_all(dir);
        }
    }
}

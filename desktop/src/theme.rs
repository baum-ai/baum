//! The active colour theme, cached for the screens that cannot ask the
//! daemon. The setup, connecting and tray screens run from `tauri://` and
//! allow only `ipc:` in their CSP, and the daemon's pages sit on a loopback
//! origin whose port changes, so their `localStorage` is not shared. A daemon
//! page therefore tells the app what the saved theme resolves to
//! (`save_theme`), the app keeps it in the `theme` section of `desktop.json`,
//! and the native screens read it (`theme_state`). The app checks only the
//! shape of what it stores; the daemon owns what a theme may contain.
//! `script` applies the saved theme before the first paint of the windows
//! that carry it.

use std::collections::BTreeMap;
use std::path::PathBuf;
use std::sync::Mutex;

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Manager};

const SECTION: &str = "theme";

const MAX_TOKENS: usize = 40;
const MAX_NAME: usize = 40;
const MAX_VALUE: usize = 64;
const MAX_TOTAL: usize = 32 * 1024;

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Mode {
    #[default]
    System,
    Light,
    Dark,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Base {
    Light,
    Dark,
}

/// A theme as a screen applies it: the built-in palette it changes and its
/// token overrides.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Palette {
    pub base: Base,
    #[serde(default)]
    pub tokens: BTreeMap<String, String>,
}

/// What the screens need to pick the active theme: the mode and the theme in
/// each slot. A slot that is `None` has its built-in theme.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Saved {
    pub mode: Mode,
    pub light: Option<Palette>,
    pub dark: Option<Palette>,
}

fn token_name_ok(name: &str) -> bool {
    name.strip_prefix("--").is_some_and(|rest| {
        !rest.is_empty()
            && name.len() <= MAX_NAME + 2
            && rest
                .bytes()
                .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-')
    })
}

fn check_palette(palette: &Palette) -> Result<usize, String> {
    if palette.tokens.len() > MAX_TOKENS {
        return Err(format!("a theme has at most {MAX_TOKENS} tokens"));
    }
    let mut size = 0;
    for (name, value) in &palette.tokens {
        if !token_name_ok(name) {
            return Err(format!("{name:?} is not a token name"));
        }
        if value.len() > MAX_VALUE {
            return Err(format!("the value of {name} is over {MAX_VALUE} bytes"));
        }
        size += name.len() + value.len();
    }
    Ok(size)
}

/// Shape only: names, counts and sizes. Mode and base are enums, so the
/// types already refuse any other word.
fn check(saved: &Saved) -> Result<(), String> {
    let mut total = 0;
    for palette in [&saved.light, &saved.dark].into_iter().flatten() {
        total += check_palette(palette)?;
    }
    within_total(total)
}

fn within_total(bytes: usize) -> Result<(), String> {
    if bytes > MAX_TOTAL {
        return Err(format!("a theme is over {} KiB", MAX_TOTAL / 1024));
    }
    Ok(())
}

/// The saved theme and where it is kept; `None` keeps it in memory only.
pub struct Store {
    saved: Mutex<Saved>,
    path: Option<PathBuf>,
}

impl Store {
    fn in_dir(dir: PathBuf) -> Store {
        let path = dir.join(crate::settings::FILE);
        let saved: Saved = crate::settings::load(&path, SECTION);
        let saved = if check(&saved).is_ok() {
            saved
        } else {
            eprintln!("baum desktop: the saved theme is malformed, using the default");
            Saved::default()
        };
        Store {
            saved: Mutex::new(saved),
            path: Some(path),
        }
    }

    #[cfg(test)]
    fn in_memory() -> Store {
        Store {
            saved: Mutex::new(Saved::default()),
            path: None,
        }
    }

    pub fn get(&self) -> Saved {
        self.saved.lock().unwrap_or_else(|p| p.into_inner()).clone()
    }

    /// Replaces and saves the theme, or refuses it and changes nothing.
    fn set(&self, next: Saved) -> Result<(), String> {
        check(&next)?;
        let mut current = self.saved.lock().unwrap_or_else(|p| p.into_inner());
        if *current == next {
            return Ok(());
        }
        *current = next.clone();
        if let Some(path) = &self.path
            && let Err(e) =
                crate::settings::update(path, SECTION, |saved: &mut Saved| *saved = next)
        {
            eprintln!("baum desktop: cannot save the theme: {e}");
        }
        Ok(())
    }
}

/// Loads the saved theme. Runs in `setup`, before the windows exist.
pub fn install(app: &AppHandle) -> tauri::Result<()> {
    let dir = app
        .path()
        .app_config_dir()
        .map_err(|e| tauri::Error::Anyhow(e.into()))?;
    app.manage(Store::in_dir(dir));
    Ok(())
}

/// The initialization script of a window: applies `saved` before the first
/// paint, as `applyTheme` in the app does (`data-theme`, `color-scheme` and
/// inline tokens), choosing the slot with `matchMedia` in system mode. The
/// data is JSON with `<`, U+2028 and U+2029 escaped, so no value can end a
/// script element or a string. The page's own code applies the live theme
/// right after.
pub fn script(saved: &Saved) -> String {
    let data = serde_json::to_string(saved)
        .unwrap_or_else(|_| "null".into())
        .replace('<', "\\u003c")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029");
    format!(
        r#"(function () {{
  try {{
    var saved = {data};
    var dark = typeof matchMedia === "function" && matchMedia("(prefers-color-scheme: dark)").matches;
    var slot = saved.mode === "light" || saved.mode === "dark" ? saved.mode : dark ? "dark" : "light";
    var theme = saved[slot];
    var base = theme ? theme.base : slot;
    var root = document.documentElement;
    root.dataset.theme = base;
    root.style.colorScheme = base;
    if (theme) for (var name in theme.tokens) root.style.setProperty(name, theme.tokens[name]);
  }} catch (e) {{}}
}})();"#
    )
}

/// The theme the native screens apply.
#[tauri::command]
pub fn theme_state(store: tauri::State<'_, Store>) -> Saved {
    store.get()
}

/// The daemon pages' report of the saved theme.
#[tauri::command]
pub fn save_theme(
    store: tauri::State<'_, Store>,
    mode: Mode,
    light: Option<Palette>,
    dark: Option<Palette>,
) -> Result<(), String> {
    store.set(Saved { mode, light, dark })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    fn scratch(name: &str) -> PathBuf {
        let dir =
            std::env::temp_dir().join(format!("baum-desktop-theme-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        dir
    }

    fn palette(base: Base, tokens: &[(&str, &str)]) -> Palette {
        Palette {
            base,
            tokens: tokens
                .iter()
                .map(|(k, v)| (k.to_string(), v.to_string()))
                .collect(),
        }
    }

    fn saved(light: Palette) -> Saved {
        Saved {
            mode: Mode::Light,
            light: Some(light),
            dark: None,
        }
    }

    #[test]
    fn a_saved_theme_survives_a_restart() {
        let dir = scratch("roundtrip");
        let next = Saved {
            mode: Mode::Dark,
            light: Some(palette(Base::Light, &[("--surface-page", "white")])),
            dark: Some(palette(Base::Dark, &[("--color-paper", "black")])),
        };
        Store::in_dir(dir.clone()).set(next.clone()).unwrap();
        assert_eq!(Store::in_dir(dir.clone()).get(), next);
        let _ = fs::remove_dir_all(dir);
    }

    #[test]
    fn nothing_saved_follows_the_system() {
        let dir = scratch("empty");
        assert_eq!(Store::in_dir(dir).get(), Saved::default());
        assert_eq!(Saved::default().mode, Mode::System);
    }

    #[test]
    fn each_limit_refuses_and_leaves_the_saved_theme() {
        let store = Store::in_memory();
        let good = saved(palette(Base::Light, &[("--surface-page", "white")]));
        store.set(good.clone()).unwrap();
        let many: Vec<(String, String)> = (0..=MAX_TOKENS)
            .map(|i| (format!("--t-{i}"), "black".into()))
            .collect();
        let too_many = Palette {
            base: Base::Light,
            tokens: many.into_iter().collect(),
        };
        let long_value = "x".repeat(MAX_VALUE + 1);
        let long_name = format!("--{}", "a".repeat(MAX_NAME + 1));
        let bad = [
            saved(too_many),
            saved(palette(Base::Light, &[("--surface-page", &long_value)])),
            saved(palette(Base::Light, &[(&long_name, "white")])),
            saved(palette(Base::Light, &[("surface-page", "white")])),
            saved(palette(Base::Light, &[("--", "white")])),
            saved(palette(Base::Light, &[("--Surface", "white")])),
            saved(palette(Base::Light, &[("--a b", "white")])),
            saved(palette(Base::Light, &[("--a;b", "white")])),
        ];
        for next in bad {
            assert!(store.set(next).is_err());
            assert_eq!(store.get(), good);
        }
        // The longest allowed name and value pass.
        let edge = saved(palette(
            Base::Light,
            &[(
                &format!("--{}", "a".repeat(MAX_NAME)),
                &"x".repeat(MAX_VALUE),
            )],
        ));
        store.set(edge.clone()).unwrap();
        assert_eq!(store.get(), edge);
    }

    #[test]
    fn the_total_size_is_limited() {
        assert!(within_total(MAX_TOTAL).is_ok());
        assert!(within_total(MAX_TOTAL + 1).is_err());
        // Two themes at every other limit stay under it.
        let tokens: BTreeMap<String, String> = (0..MAX_TOKENS)
            .map(|i| (format!("--{i:0>38}"), "x".repeat(MAX_VALUE)))
            .collect();
        let slot = Palette {
            base: Base::Dark,
            tokens,
        };
        let full = Saved {
            mode: Mode::System,
            light: Some(slot.clone()),
            dark: Some(slot),
        };
        assert!(check(&full).is_ok());
    }

    #[test]
    fn only_known_words_are_a_mode_or_a_base() {
        for mode in ["system", "light", "dark"] {
            assert!(serde_json::from_value::<Mode>(serde_json::json!(mode)).is_ok());
        }
        for word in ["auto", "Dark", "", "sepia"] {
            assert!(serde_json::from_value::<Mode>(serde_json::json!(word)).is_err());
            assert!(serde_json::from_value::<Base>(serde_json::json!(word)).is_err());
        }
        assert!(serde_json::from_value::<Base>(serde_json::json!("system")).is_err());
    }

    #[test]
    fn a_malformed_saved_section_is_the_default() {
        for (name, content) in [
            ("damaged", "{"),
            ("mode", r#"{"theme":{"mode":"sepia"}}"#),
            (
                "token",
                r#"{"theme":{"mode":"light","light":{"base":"light","tokens":{"x":"y"}}}}"#,
            ),
        ] {
            let dir = scratch(name);
            fs::create_dir_all(&dir).unwrap();
            fs::write(dir.join(crate::settings::FILE), content).unwrap();
            assert_eq!(Store::in_dir(dir.clone()).get(), Saved::default());
            let _ = fs::remove_dir_all(dir);
        }
    }

    #[test]
    fn the_script_cannot_be_broken_out_of() {
        // Shape checks keep such values out, but the script must not rely on it.
        let hostile = "\"</script><script>alert(1)</script>';\u{2028}\u{2029}\\";
        let next = saved(palette(Base::Light, &[("--surface-page", hostile)]));
        let script = script(&next);
        assert!(!script.contains("</script"));
        assert!(!script.contains("<script"));
        assert!(!script.contains('\u{2028}'));
        assert!(!script.contains('\u{2029}'));
        assert!(script.contains(r"\u003c/script>"));
        assert!(script.contains(r#"\"\u003c/script>"#));
        // The embedded data parses back to exactly the saved theme.
        let start = script.find("var saved = ").unwrap() + "var saved = ".len();
        let end = script[start..].find(";\n").unwrap() + start;
        let back: Saved = serde_json::from_str(&script[start..end]).unwrap();
        assert_eq!(back, next);
    }

    #[test]
    fn the_script_picks_the_slot_and_sets_the_base() {
        let script = script(&Saved::default());
        assert!(script.contains(r#""mode":"system""#));
        assert!(script.contains("matchMedia"));
        assert!(script.contains("root.dataset.theme = base"));
        assert!(script.contains("root.style.colorScheme = base"));
        assert!(script.contains("setProperty"));
    }
}

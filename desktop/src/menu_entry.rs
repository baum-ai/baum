//! The AppImage's applications menu entry. Without an integration tool such
//! as AppImageLauncher or Gear Lever, nothing adds a downloaded AppImage to
//! the menu, so the app does, as
//! `$XDG_DATA_HOME/applications/baum-appimage.desktop`, with the icon copied
//! beside its copy of baum, since the AppImage's own files vanish when it
//! quits.
//!
//! Every start of an installed AppImage rewrites the entry, so moving the file
//! keeps it working, and `TryExec` hides the entry once the file is gone. An
//! entry the user hid (`Hidden` or `NoDisplay`) is left alone. When another
//! visible entry in the same directory already starts this AppImage (one an
//! integration tool wrote), the app removes its own instead, so the menu does
//! not list baum twice. The deb installs its own entry and never runs this.

use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

use tauri::{AppHandle, Manager, Runtime};

use crate::autostart::{desktop_entry_pair, desktop_exec_quote, is_true, write_entry};

const ENTRY_FILE: &str = "baum-appimage.desktop";
/// The icon linuxdeploy puts at the root of the AppImage.
const BUNDLED_ICON: &str = "baum-desktop.png";
const ICON_FILE: &str = "baum.png";

pub fn refresh<R: Runtime>(app: &AppHandle<R>) {
    if cfg!(debug_assertions)
        || std::env::var_os("BAUM_DESKTOP_BAUM").is_some()
        || !crate::onboarding::running_as_appimage()
    {
        return;
    }
    let (Some(image), Some(appdir)) = (std::env::var_os("APPIMAGE"), std::env::var_os("APPDIR"))
    else {
        return;
    };
    let Some(path) = entry_path_in(std::env::var_os("XDG_DATA_HOME"), std::env::var_os("HOME"))
    else {
        return;
    };
    let icon = app
        .path()
        .app_local_data_dir()
        .map_err(|e| e.to_string())
        .and_then(|dir| {
            let icon = dir.join(ICON_FILE);
            copy_if_changed(&Path::new(&appdir).join(BUNDLED_ICON), &icon)?;
            Ok(icon)
        });
    let icon = match icon {
        Ok(icon) => Some(icon),
        Err(e) => {
            eprintln!("baum desktop: cannot copy the menu icon: {e}");
            None
        }
    };
    if let Err(e) = update(&path, Path::new(&image), icon.as_deref()) {
        eprintln!("baum desktop: cannot write the applications menu entry: {e}");
    }
}

/// `$XDG_DATA_HOME/applications`, else `~/.local/share/applications`.
fn entry_path_in(
    data: Option<std::ffi::OsString>,
    home: Option<std::ffi::OsString>,
) -> Option<PathBuf> {
    let base = data
        .map(PathBuf::from)
        .filter(|p| p.is_absolute())
        .or_else(|| home.map(|h| PathBuf::from(h).join(".local/share")))?;
    Some(base.join("applications").join(ENTRY_FILE))
}

/// Writes the entry unless the user hid it or it is already current, and
/// removes it when another visible entry already starts the AppImage.
fn update(path: &Path, image: &Path, icon: Option<&Path>) -> Result<(), String> {
    let existing = fs::read_to_string(path).ok();
    if existing.as_deref().is_some_and(hidden) {
        return Ok(());
    }
    if started_by_another_entry(path, image) {
        return match fs::remove_file(path) {
            Err(e) if e.kind() != std::io::ErrorKind::NotFound => {
                Err(format!("cannot remove {}: {e}", path.display()))
            }
            _ => Ok(()),
        };
    }
    let entry = menu_entry(image, icon)?;
    if existing.as_deref() == Some(entry.as_str()) {
        return Ok(());
    }
    write_entry(path, &entry)
}

fn menu_entry(image: &Path, icon: Option<&Path>) -> Result<String, String> {
    let text = image.to_string_lossy();
    if text.contains(['\n', '\r']) {
        return Err("the AppImage's path holds a line break, which a menu entry cannot".into());
    }
    let mut entry = format!(
        "[Desktop Entry]\n\
         Type=Application\n\
         Name=baum\n\
         Comment=Keep up with what your coding agents build\n\
         Exec={}\n\
         TryExec={}\n",
        desktop_exec_quote(&text),
        escape(&text)
    );
    if let Some(icon) = icon.map(|i| i.to_string_lossy())
        && !icon.contains(['\n', '\r'])
    {
        entry.push_str(&format!("Icon={}\n", escape(&icon)));
    }
    entry.push_str(
        "Terminal=false\n\
         Categories=Development;\n\
         StartupWMClass=baum-desktop\n",
    );
    Ok(entry)
}

/// A string value's escaping: only the backslash needs it in a path.
fn escape(value: &str) -> String {
    value.replace('\\', "\\\\")
}

/// Whether the `[Desktop Entry]` group hides the entry from menus.
fn hidden(content: &str) -> bool {
    let mut in_group = false;
    for line in content.lines() {
        let line = line.trim();
        if line.starts_with('[') {
            in_group = line == "[Desktop Entry]";
        } else if in_group
            && let Some((key, value)) = desktop_entry_pair(line)
            && (key == "Hidden" || key == "NoDisplay")
            && is_true(value)
        {
            return true;
        }
    }
    false
}

/// Whether a visible `.desktop` file beside `own` starts `image`, by its
/// `TryExec` or the program in its `Exec`, as AppImageLauncher's and Gear
/// Lever's do. A hidden one does not count: baum would be in no menu.
fn started_by_another_entry(own: &Path, image: &Path) -> bool {
    let Some(entries) = own.parent().and_then(|dir| fs::read_dir(dir).ok()) else {
        return false;
    };
    entries.flatten().any(|entry| {
        let path = entry.path();
        path != own
            && path.extension().is_some_and(|ext| ext == "desktop")
            && fs::read_to_string(&path)
                .is_ok_and(|content| !hidden(&content) && starts(&content, image))
    })
}

fn starts(content: &str, image: &Path) -> bool {
    let image = image.to_string_lossy();
    let mut in_group = false;
    for line in content.lines() {
        let line = line.trim();
        if line.starts_with('[') {
            in_group = line == "[Desktop Entry]";
            continue;
        }
        let Some((key, value)) = desktop_entry_pair(line).filter(|_| in_group) else {
            continue;
        };
        let value = unescape(value);
        let program = match key {
            "TryExec" => Some(value),
            "Exec" => exec_program(&value).map(|p| p.replace("%%", "%")),
            _ => None,
        };
        if program.as_deref() == Some(image.as_ref()) {
            return true;
        }
    }
    false
}

/// Undoes a string value's escapes (`\s`, `\n`, `\t`, `\r`, `\\`).
fn unescape(value: &str) -> String {
    let mut out = String::with_capacity(value.len());
    let mut chars = value.chars();
    while let Some(c) = chars.next() {
        if c != '\\' {
            out.push(c);
            continue;
        }
        match chars.next() {
            Some('s') => out.push(' '),
            Some('n') => out.push('\n'),
            Some('t') => out.push('\t'),
            Some('r') => out.push('\r'),
            Some(other) => out.push(other),
            None => out.push('\\'),
        }
    }
    out
}

/// The program, the first argument of an unescaped `Exec` value: a quoted
/// argument ends at the next unescaped `"`, a plain one at a space.
fn exec_program(exec: &str) -> Option<String> {
    let exec = exec.trim_start();
    let Some(quoted) = exec.strip_prefix('"') else {
        return exec
            .split(' ')
            .next()
            .filter(|p| !p.is_empty())
            .map(String::from);
    };
    let mut out = String::new();
    let mut chars = quoted.chars();
    while let Some(c) = chars.next() {
        match c {
            '"' => return Some(out),
            '\\' => out.push(chars.next()?),
            c => out.push(c),
        }
    }
    None
}

/// Copies `from` to `to` through a temporary file when their contents differ.
fn copy_if_changed(from: &Path, to: &Path) -> Result<(), String> {
    let bytes = fs::read(from).map_err(|e| format!("cannot read {}: {e}", from.display()))?;
    if fs::read(to).is_ok_and(|current| current == bytes) {
        return Ok(());
    }
    let dir = to.parent().ok_or("the icon has no directory")?;
    fs::create_dir_all(dir).map_err(|e| format!("cannot create {}: {e}", dir.display()))?;
    let temp = to.with_extension("tmp");
    let write = || -> std::io::Result<()> {
        let mut file = fs::File::create(&temp)?;
        file.write_all(&bytes)?;
        file.sync_all()?;
        fs::rename(&temp, to)
    };
    write().map_err(|e| {
        let _ = fs::remove_file(&temp);
        format!("cannot write {}: {e}", to.display())
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp_dir(name: &str) -> PathBuf {
        let dir =
            std::env::temp_dir().join(format!("baum-menu-entry-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[test]
    fn the_entry_starts_the_appimage_and_hides_once_it_is_gone() {
        let entry = menu_entry(
            Path::new("/home/user/Applications/baum.AppImage"),
            Some(Path::new(
                "/home/user/.local/share/com.usebaum.desktop/baum.png",
            )),
        )
        .unwrap();
        assert_eq!(
            entry,
            "[Desktop Entry]\nType=Application\nName=baum\n\
             Comment=Keep up with what your coding agents build\n\
             Exec=/home/user/Applications/baum.AppImage\n\
             TryExec=/home/user/Applications/baum.AppImage\n\
             Icon=/home/user/.local/share/com.usebaum.desktop/baum.png\n\
             Terminal=false\nCategories=Development;\nStartupWMClass=baum-desktop\n"
        );
    }

    #[test]
    fn paths_are_quoted_for_exec_and_escaped_elsewhere() {
        let entry = menu_entry(Path::new("/opt/a b/x\\y.AppImage"), None).unwrap();
        assert!(entry.contains("Exec=\"/opt/a b/x\\\\\\\\y.AppImage\"\n"));
        assert!(entry.contains("TryExec=/opt/a b/x\\\\y.AppImage\n"));
        assert!(!entry.contains("Icon="));
        assert!(menu_entry(Path::new("/tmp/a\nExec=evil"), None).is_err());
    }

    #[test]
    fn the_entry_goes_in_the_xdg_data_directory() {
        assert_eq!(
            entry_path_in(Some("/data".into()), Some("/home/user".into())),
            Some(PathBuf::from("/data/applications/baum-appimage.desktop"))
        );
        // A relative XDG_DATA_HOME is invalid and ignored.
        assert_eq!(
            entry_path_in(Some("data".into()), Some("/home/user".into())),
            Some(PathBuf::from(
                "/home/user/.local/share/applications/baum-appimage.desktop"
            ))
        );
        assert_eq!(entry_path_in(None, None), None);
    }

    #[test]
    fn update_writes_and_moves_the_entry_but_keeps_a_hidden_one() {
        let dir = temp_dir("update");
        let path = dir.join("applications").join(ENTRY_FILE);
        update(&path, Path::new("/a/baum.AppImage"), None).unwrap();
        assert!(
            fs::read_to_string(&path)
                .unwrap()
                .contains("Exec=/a/baum.AppImage\n")
        );

        update(&path, Path::new("/b/baum.AppImage"), None).unwrap();
        assert!(
            fs::read_to_string(&path)
                .unwrap()
                .contains("Exec=/b/baum.AppImage\n")
        );

        for hide in ["NoDisplay=true", "Hidden=true"] {
            let kept = format!("[Desktop Entry]\nExec=/b/baum.AppImage\n{hide}\n");
            fs::write(&path, &kept).unwrap();
            update(&path, Path::new("/c/baum.AppImage"), None).unwrap();
            assert_eq!(fs::read_to_string(&path).unwrap(), kept);
        }
        fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn an_integration_tools_entry_replaces_ours() {
        let dir = temp_dir("integrated");
        let apps = dir.join("applications");
        let path = apps.join(ENTRY_FILE);
        let image = Path::new("/home/user/.appimage-applications/baum.AppImage");
        update(&path, image, None).unwrap();
        assert!(path.exists());

        // An entry for another AppImage changes nothing.
        fs::write(
            apps.join("appimagekit_1-Other.desktop"),
            "[Desktop Entry]\nExec=/home/user/other.AppImage %F\n",
        )
        .unwrap();
        update(&path, image, None).unwrap();
        assert!(path.exists());

        // AppImageLauncher names the AppImage in TryExec and Exec.
        let theirs = apps.join("appimagekit_2-baum.desktop");
        fs::write(
            &theirs,
            "[Desktop Entry]\nName=baum\n\
             Exec=/home/user/.appimage-applications/baum.AppImage %U\n\
             TryExec=/home/user/.appimage-applications/baum.AppImage\n",
        )
        .unwrap();
        update(&path, image, None).unwrap();
        assert!(!path.exists());
        update(&path, image, None).unwrap();
        assert!(!path.exists());

        // Once it is gone, ours comes back.
        fs::remove_file(&theirs).unwrap();
        update(&path, image, None).unwrap();
        assert!(path.exists());

        // A hidden entry of theirs leaves ours in place.
        let starts_image = format!("Exec={}\n", image.display());
        fs::write(
            &theirs,
            format!("[Desktop Entry]\n{starts_image}NoDisplay=true\n"),
        )
        .unwrap();
        update(&path, image, None).unwrap();
        assert!(path.exists());

        // Ours, once the user hid it, is kept even beside a visible one.
        let ours_hidden = format!("[Desktop Entry]\n{starts_image}Hidden=true\n");
        fs::write(&path, &ours_hidden).unwrap();
        fs::write(&theirs, format!("[Desktop Entry]\n{starts_image}")).unwrap();
        update(&path, image, None).unwrap();
        assert_eq!(fs::read_to_string(&path).unwrap(), ours_hidden);
        fs::remove_dir_all(&dir).unwrap();
    }

    #[test]
    fn an_entry_starts_the_appimage_by_try_exec_or_its_exec_program() {
        let image = Path::new("/home/user/a b/x\\y.AppImage");
        let ours = menu_entry(image, None).unwrap();
        assert!(starts(&ours, image));
        // Our escaping round-trips through Exec alone and TryExec alone.
        let only = |key: &str| {
            ours.lines()
                .filter(|l| *l == "[Desktop Entry]" || l.starts_with(key))
                .collect::<Vec<_>>()
                .join("\n")
        };
        assert!(starts(&only("Exec="), image));
        assert!(starts(&only("TryExec="), image));
        let opt = |p: &str| PathBuf::from(p);
        assert!(starts(
            "[Desktop Entry]\nExec=\"/opt/a b.AppImage\" %F\n",
            &opt("/opt/a b.AppImage")
        ));
        assert!(starts(
            "[Desktop Entry]\nTryExec=/opt/a\\sb\n",
            &opt("/opt/a b")
        ));
        assert!(starts(
            "[Desktop Entry]\nExec=/opt/50%%.AppImage\n",
            &opt("/opt/50%.AppImage")
        ));
        // Another program, another group, or the AppImage only as an argument.
        assert!(!starts(
            "[Desktop Entry]\nExec=/opt/b.AppImage\n",
            &opt("/opt/a.AppImage")
        ));
        assert!(!starts(
            "[Desktop Action x]\nExec=/opt/a.AppImage\n",
            &opt("/opt/a.AppImage")
        ));
        assert!(!starts(
            "[Desktop Entry]\nExec=/usr/lib/appimagelauncher/remove /opt/a.AppImage\n",
            Path::new("/opt/a.AppImage")
        ));
    }

    #[test]
    fn the_icon_is_copied_only_when_it_changed() {
        let dir = temp_dir("icon");
        let (from, to) = (dir.join("in.png"), dir.join("out/baum.png"));
        fs::write(&from, b"one").unwrap();
        copy_if_changed(&from, &to).unwrap();
        assert_eq!(fs::read(&to).unwrap(), b"one");
        fs::write(&from, b"two").unwrap();
        copy_if_changed(&from, &to).unwrap();
        assert_eq!(fs::read(&to).unwrap(), b"two");
        assert!(copy_if_changed(&dir.join("missing.png"), &to).is_err());
        fs::remove_dir_all(&dir).unwrap();
    }
}

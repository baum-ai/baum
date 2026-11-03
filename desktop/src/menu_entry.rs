//! The AppImage's applications menu entry. The release AppImage carries no
//! AppImage magic bytes (scripts/bundle.sh says why), so integration tools
//! such as AppImageLauncher and Gear Lever do not add it to the menu; the app
//! does, as `$XDG_DATA_HOME/applications/baum-appimage.desktop`, with the icon
//! copied beside its copy of baum, since the AppImage's own files vanish when
//! it quits.
//!
//! Every start of an installed AppImage rewrites the entry, so moving the file
//! keeps it working, and `TryExec` hides the entry once the file is gone. An
//! entry the user hid (`Hidden` or `NoDisplay`) is left alone. The deb
//! installs its own entry and never runs this.

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

/// Writes the entry unless the user hid it or it is already current.
fn update(path: &Path, image: &Path, icon: Option<&Path>) -> Result<(), String> {
    let existing = fs::read_to_string(path).ok();
    if existing.as_deref().is_some_and(hidden) {
        return Ok(());
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
            Path::new("/home/a/Applications/baum.AppImage"),
            Some(Path::new(
                "/home/a/.local/share/com.usebaum.desktop/baum.png",
            )),
        )
        .unwrap();
        assert_eq!(
            entry,
            "[Desktop Entry]\nType=Application\nName=baum\n\
             Comment=Keep up with what your coding agents build\n\
             Exec=/home/a/Applications/baum.AppImage\n\
             TryExec=/home/a/Applications/baum.AppImage\n\
             Icon=/home/a/.local/share/com.usebaum.desktop/baum.png\n\
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
            entry_path_in(Some("/data".into()), Some("/home/a".into())),
            Some(PathBuf::from("/data/applications/baum-appimage.desktop"))
        );
        // A relative XDG_DATA_HOME is invalid and ignored.
        assert_eq!(
            entry_path_in(Some("data".into()), Some("/home/a".into())),
            Some(PathBuf::from(
                "/home/a/.local/share/applications/baum-appimage.desktop"
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

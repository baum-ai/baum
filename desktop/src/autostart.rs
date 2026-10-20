//! Start at login. Off by default; turning it on makes the operating system
//! start the app, hidden, when the user logs in. The app then ensures the
//! daemon is running, so automatic reviews resume after a reboot.
//!
//! One mechanism per platform, each a per-user entry the user can also see and
//! remove with the system's own tools:
//!
//! - Linux: an XDG autostart entry, `$XDG_CONFIG_HOME/autostart/baum.desktop`.
//! - macOS: a LaunchAgent, `~/Library/LaunchAgents/com.usebaum.desktop.plist`,
//!   which launchd runs at login. It appears under Login Items & Extensions.
//! - Windows: the per-user Run key,
//!   `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, value `baum`, set
//!   with the system's `reg.exe`.
//!
//! The entries are generated here rather than by a plugin so the contents are
//! ours to test and no page can reach a plugin command. The app rewrites an
//! enabled entry on every start, so moving or updating the app keeps it
//! pointing at the right executable.

use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

/// The argument the login entry passes: start without showing the window.
pub const HIDDEN_FLAG: &str = "--hidden";

#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
const DESKTOP_FILE: &str = "baum.desktop";
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
const LAUNCH_AGENT_LABEL: &str = "com.usebaum.desktop";
#[cfg_attr(not(windows), allow(dead_code))]
const RUN_KEY: &str = r"HKCU\Software\Microsoft\Windows\CurrentVersion\Run";
#[cfg_attr(not(windows), allow(dead_code))]
const RUN_VALUE: &str = "baum";

#[cfg_attr(not(windows), allow(dead_code))]
const APPROVED_KEY: &str =
    r"HKCU\Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run";

/// The executable the login entry starts. An AppImage runs from a temporary
/// mount, so its own path (`APPIMAGE`) is the stable one.
fn executable() -> Result<PathBuf, String> {
    if let Some(image) = std::env::var_os("APPIMAGE").filter(|v| !v.is_empty()) {
        return check_executable(Path::new(&image));
    }
    let exe =
        std::env::current_exe().map_err(|e| format!("cannot locate the app executable: {e}"))?;
    check_executable(&exe)
}

/// The path to write into an entry. Linux appends " (deleted)" to the path of
/// an executable replaced while it runs (an update); that is not a path. A
/// macOS app run from a quarantined download is copied to a random read-only
/// location (App Translocation) that changes on every launch, so no entry
/// can name it.
fn check_executable(exe: &Path) -> Result<PathBuf, String> {
    let text = exe.to_string_lossy();
    if text.contains("/AppTranslocation/") {
        return Err(
            "move baum to the Applications folder, open it from there, then turn on start at login"
                .into(),
        );
    }
    Ok(match text.strip_suffix(" (deleted)") {
        Some(path) => PathBuf::from(path),
        None => exe.to_path_buf(),
    })
}

/// Whether an entry should name this executable on its own: not a
/// development build or a build-directory binary. Only the user's own toggle
/// writes an entry for those.
fn installed(exe: &Path, debug_build: bool, dev_baum: bool) -> bool {
    !debug_build
        && !dev_baum
        && !exe.components().collect::<Vec<_>>().windows(2).any(|w| {
            w[0].as_os_str() == "target"
                && (w[1].as_os_str() == "debug" || w[1].as_os_str() == "release")
        })
}

/// Whether start at login is on. An entry the desktop environment has
/// disabled (GNOME Tweaks and KDE write `Hidden` or
/// `X-GNOME-Autostart-enabled=false`; Windows Task Manager writes
/// StartupApproved) is off.
pub fn enabled() -> bool {
    platform::enabled()
}

/// Turns start at login on or off. Turning it on also clears a disable the
/// desktop environment recorded, since the user is now asking for it.
pub fn set(on: bool) -> Result<(), String> {
    platform::set(on, &executable()?)
}

/// Rewrites an enabled entry so it names this executable. Never touches an
/// entry that is off, whoever turned it off, and does nothing for
/// development builds.
pub fn refresh() {
    let Ok(exe) = executable() else { return };
    if !installed(
        &exe,
        cfg!(debug_assertions),
        std::env::var_os("BAUM_DESKTOP_BAUM").is_some(),
    ) {
        return;
    }
    if enabled()
        && let Err(e) = platform::set(true, &exe)
    {
        eprintln!("baum desktop: cannot refresh the start at login entry: {e}");
    }
}

/// A new XDG autostart entry (Desktop Entry Specification 1.5).
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
fn desktop_entry(exe: &Path) -> Result<String, String> {
    Ok(format!(
        "[Desktop Entry]\n\
         Type=Application\n\
         Name=baum\n\
         Comment=Keep up with what your coding agents build\n\
         Exec={}\n\
         Terminal=false\n\
         X-GNOME-Autostart-enabled=true\n",
        exec_line(exe)?
    ))
}

/// The `Exec` value. A newline would end the line and let the rest of the
/// path become another key.
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
fn exec_line(exe: &Path) -> Result<String, String> {
    let text = exe.to_string_lossy();
    if text.contains(['\n', '\r']) {
        return Err("the app's path holds a line break, which an autostart entry cannot".into());
    }
    Ok(format!("{} {HIDDEN_FLAG}", desktop_exec_quote(&text)))
}

fn desktop_entry_pair(line: &str) -> Option<(&str, &str)> {
    let (key, value) = line.split_once('=')?;
    Some((key.trim(), value.trim()))
}

fn is_true(value: &str) -> bool {
    value.eq_ignore_ascii_case("true")
}

/// Whether the entry's `[Desktop Entry]` group disables it, as desktop
/// environments do when the user turns it off in their settings.
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
fn entry_disabled(content: &str) -> bool {
    let mut in_group = false;
    for line in content.lines() {
        let line = line.trim();
        if line.starts_with('[') {
            in_group = line == "[Desktop Entry]";
        } else if in_group
            && let Some((key, value)) = desktop_entry_pair(line)
            && ((key == "Hidden" && is_true(value))
                || (key == "X-GNOME-Autostart-enabled" && value.eq_ignore_ascii_case("false")))
        {
            return true;
        }
    }
    false
}

/// The entry with `Exec` set to this executable and any disable removed, and
/// every other key and group kept as the user or their desktop left it.
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
fn rewrite_entry(existing: &str, exe: &Path) -> Result<String, String> {
    let exec = format!("Exec={}", exec_line(exe)?);
    let mut out: Vec<String> = Vec::new();
    let (mut in_group, mut seen_group, mut has_exec, mut has_enabled) =
        (false, false, false, false);
    // Closing the group adds whatever it lacked.
    let close = |out: &mut Vec<String>, has_exec: bool, has_enabled: bool| {
        if !has_exec {
            out.push(exec.clone());
        }
        if !has_enabled {
            out.push("X-GNOME-Autostart-enabled=true".into());
        }
    };
    for line in existing.lines() {
        let trimmed = line.trim();
        if trimmed.starts_with('[') {
            if in_group {
                close(&mut out, has_exec, has_enabled);
            }
            in_group = trimmed == "[Desktop Entry]";
            seen_group |= in_group;
            out.push(line.to_string());
            continue;
        }
        if in_group && let Some((key, _)) = desktop_entry_pair(trimmed) {
            match key {
                "Exec" => {
                    has_exec = true;
                    out.push(exec.clone());
                    continue;
                }
                "Hidden" => continue,
                "X-GNOME-Autostart-enabled" => {
                    has_enabled = true;
                    out.push("X-GNOME-Autostart-enabled=true".into());
                    continue;
                }
                _ => {}
            }
        }
        out.push(line.to_string());
    }
    if !seen_group {
        return desktop_entry(exe);
    }
    if in_group {
        close(&mut out, has_exec, has_enabled);
    }
    Ok(out.join("\n") + "\n")
}

/// Quotes one `Exec` argument. The specification wraps an argument holding a
/// reserved character in double quotes and backslash-escapes `"`, `` ` ``,
/// `$` and `\` inside them (the file's own string escaping then doubles each
/// backslash again); `%` starts a field code, so a literal one is doubled.
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
fn desktop_exec_quote(arg: &str) -> String {
    let arg = arg.replace('%', "%%");
    let reserved = |c: char| " \t\n\"'\\><~|&;$*?#()`".contains(c);
    if !arg.chars().any(reserved) {
        return arg;
    }
    let mut out = String::from("\"");
    for c in arg.chars() {
        match c {
            '"' | '`' | '$' => {
                out.push_str("\\\\");
                out.push(c);
            }
            '\\' => out.push_str("\\\\\\\\"),
            _ => out.push(c),
        }
    }
    out.push('"');
    out
}

/// A launchd agent property list that starts the app at login.
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
fn launch_agent(exe: &Path) -> String {
    format!(
        r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{LAUNCH_AGENT_LABEL}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{}</string>
		<string>{HIDDEN_FLAG}</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
"#,
        xml_escape(&exe.to_string_lossy())
    )
}

#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
fn xml_escape(text: &str) -> String {
    text.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&quot;")
        .replace('\'', "&apos;")
}

/// The Run key's value: the quoted executable and the flag.
#[cfg_attr(not(windows), allow(dead_code))]
fn run_value(exe: &Path) -> String {
    format!("\"{}\" {HIDDEN_FLAG}", exe.display())
}

/// `reg.exe` arguments that set the Run value.
#[cfg_attr(not(windows), allow(dead_code))]
fn reg_add_args(exe: &Path) -> Vec<String> {
    [
        "add",
        RUN_KEY,
        "/v",
        RUN_VALUE,
        "/t",
        "REG_SZ",
        "/d",
        &run_value(exe),
        "/f",
    ]
    .map(String::from)
    .to_vec()
}

#[cfg_attr(not(windows), allow(dead_code))]
fn reg_delete_args() -> Vec<String> {
    ["delete", RUN_KEY, "/v", RUN_VALUE, "/f"]
        .map(String::from)
        .to_vec()
}

#[cfg_attr(not(windows), allow(dead_code))]
fn reg_approved_query_args() -> Vec<String> {
    ["query", APPROVED_KEY, "/v", RUN_VALUE]
        .map(String::from)
        .to_vec()
}

#[cfg_attr(not(windows), allow(dead_code))]
fn reg_approved_delete_args() -> Vec<String> {
    ["delete", APPROVED_KEY, "/v", RUN_VALUE, "/f"]
        .map(String::from)
        .to_vec()
}

/// Whether `reg query` of the StartupApproved value says Task Manager (or
/// Settings) disabled the entry. The value is binary and its first byte's low
/// bit is set when disabled: 02 and 06 are enabled, 03 and 07 disabled.
#[cfg_attr(not(windows), allow(dead_code))]
fn startup_disabled(output: &str) -> bool {
    output
        .lines()
        .filter(|l| l.contains("REG_BINARY"))
        .filter_map(|l| l.split_whitespace().last())
        .filter_map(|hex| u8::from_str_radix(hex.get(..2)?, 16).ok())
        .any(|first| first & 1 == 1)
}

#[cfg_attr(not(windows), allow(dead_code))]
fn reg_query_args() -> Vec<String> {
    ["query", RUN_KEY, "/v", RUN_VALUE]
        .map(String::from)
        .to_vec()
}

/// Writes `content` to `path` through a temporary file, so a crash leaves the
/// old entry or the new one and never half of one.
#[cfg_attr(windows, allow(dead_code))]
fn write_entry(path: &Path, content: &str) -> Result<(), String> {
    let dir = path
        .parent()
        .ok_or("the autostart entry has no directory")?;
    fs::create_dir_all(dir).map_err(|e| format!("cannot create {}: {e}", dir.display()))?;
    let temp = path.with_extension("tmp");
    let write = || -> std::io::Result<()> {
        let mut file = fs::File::create(&temp)?;
        file.write_all(content.as_bytes())?;
        file.sync_all()?;
        fs::rename(&temp, path)
    };
    write().map_err(|e| {
        let _ = fs::remove_file(&temp);
        format!("cannot write {}: {e}", path.display())
    })
}

/// Removes the entry; one that is already gone is fine.
#[cfg_attr(windows, allow(dead_code))]
fn remove_entry(path: &Path) -> Result<(), String> {
    match fs::remove_file(path) {
        Ok(()) => Ok(()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(format!("cannot remove {}: {e}", path.display())),
    }
}

#[cfg(target_os = "linux")]
mod platform {
    use super::*;

    /// `$XDG_CONFIG_HOME/autostart`, else `~/.config/autostart`.
    fn entry_path() -> Option<PathBuf> {
        entry_path_in(
            std::env::var_os("XDG_CONFIG_HOME"),
            std::env::var_os("HOME"),
        )
    }

    pub(super) fn entry_path_in(
        config: Option<std::ffi::OsString>,
        home: Option<std::ffi::OsString>,
    ) -> Option<PathBuf> {
        let base = config
            .map(PathBuf::from)
            .filter(|p| p.is_absolute())
            .or_else(|| home.map(|h| PathBuf::from(h).join(".config")))?;
        Some(base.join("autostart").join(DESKTOP_FILE))
    }

    pub fn enabled() -> bool {
        entry_path()
            .and_then(|p| fs::read_to_string(p).ok())
            .is_some_and(|content| !entry_disabled(&content))
    }

    pub fn set(on: bool, exe: &Path) -> Result<(), String> {
        let path = entry_path().ok_or("cannot find the XDG config directory")?;
        if on {
            let entry = match fs::read_to_string(&path) {
                Ok(existing) => rewrite_entry(&existing, exe)?,
                Err(_) => desktop_entry(exe)?,
            };
            write_entry(&path, &entry)
        } else {
            remove_entry(&path)
        }
    }
}

#[cfg(target_os = "macos")]
mod platform {
    use super::*;

    fn entry_path() -> Option<PathBuf> {
        let home = std::env::var_os("HOME").map(PathBuf::from)?;
        Some(
            home.join("Library/LaunchAgents")
                .join(format!("{LAUNCH_AGENT_LABEL}.plist")),
        )
    }

    pub fn enabled() -> bool {
        entry_path().is_some_and(|p| p.is_file())
    }

    pub fn set(on: bool, exe: &Path) -> Result<(), String> {
        let path = entry_path().ok_or("cannot find the home directory")?;
        if on {
            write_entry(&path, &launch_agent(exe))
        } else {
            remove_entry(&path)
        }
    }
}

#[cfg(windows)]
mod platform {
    use super::*;
    use std::os::windows::process::CommandExt;
    use std::process::{Command, Stdio};

    const CREATE_NO_WINDOW: u32 = 0x0800_0000;

    /// Runs `reg.exe`; whether it succeeded, and what it printed.
    fn reg(args: &[String]) -> Result<(bool, String), String> {
        let output = Command::new("reg.exe")
            .args(args)
            .stdin(Stdio::null())
            .stderr(Stdio::null())
            .creation_flags(CREATE_NO_WINDOW)
            .output()
            .map_err(|e| format!("cannot run reg.exe: {e}"))?;
        Ok((
            output.status.success(),
            String::from_utf8_lossy(&output.stdout).into_owned(),
        ))
    }

    pub fn enabled() -> bool {
        let present = reg(&reg_query_args()).is_ok_and(|(found, _)| found);
        let disabled = reg(&reg_approved_query_args())
            .is_ok_and(|(found, out)| found && startup_disabled(&out));
        present && !disabled
    }

    pub fn set(on: bool, exe: &Path) -> Result<(), String> {
        if on {
            // Asking for it again clears a Task Manager disable.
            let _ = reg(&reg_approved_delete_args());
            if !reg(&reg_add_args(exe))?.0 {
                return Err("reg.exe could not set the Run key".into());
            }
        } else if reg(&reg_query_args())?.0 && !reg(&reg_delete_args())?.0 {
            return Err("reg.exe could not remove the Run key value".into());
        }
        Ok(())
    }
}

#[cfg(not(any(target_os = "linux", target_os = "macos", windows)))]
mod platform {
    use super::*;

    pub fn enabled() -> bool {
        false
    }

    pub fn set(_: bool, _: &Path) -> Result<(), String> {
        Err("start at login is not supported on this platform".into())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp_dir(name: &str) -> PathBuf {
        let dir =
            std::env::temp_dir().join(format!("baum-autostart-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[test]
    fn the_linux_entry_starts_the_app_hidden() {
        let entry = desktop_entry(Path::new("/usr/bin/baum-desktop")).unwrap();
        assert_eq!(
            entry,
            "[Desktop Entry]\nType=Application\nName=baum\n\
             Comment=Keep up with what your coding agents build\n\
             Exec=/usr/bin/baum-desktop --hidden\nTerminal=false\n\
             X-GNOME-Autostart-enabled=true\n"
        );
    }

    #[test]
    fn exec_quoting_follows_the_desktop_entry_specification() {
        assert_eq!(desktop_exec_quote("/usr/bin/baum"), "/usr/bin/baum");
        assert_eq!(
            desktop_exec_quote("/home/a b/baum.AppImage"),
            "\"/home/a b/baum.AppImage\""
        );
        assert_eq!(desktop_exec_quote("/opt/100%/baum"), "/opt/100%%/baum");
        assert_eq!(desktop_exec_quote("/o/$x/b"), "\"/o/\\\\$x/b\"");
        assert_eq!(desktop_exec_quote("/o/\"q\"/b"), "\"/o/\\\\\"q\\\\\"/b\"");
        assert_eq!(desktop_exec_quote("/o/a\\b"), "\"/o/a\\\\\\\\b\"");
    }

    #[test]
    fn the_launch_agent_runs_the_app_hidden_at_login() {
        let plist = launch_agent(Path::new(
            "/Applications/baum.app/Contents/MacOS/baum-desktop",
        ));
        assert!(plist.contains("<key>Label</key>\n\t<string>com.usebaum.desktop</string>"));
        assert!(plist.contains(
            "<array>\n\t\t<string>/Applications/baum.app/Contents/MacOS/baum-desktop</string>\n\t\t<string>--hidden</string>\n\t</array>"
        ));
        assert!(plist.contains("<key>RunAtLoad</key>\n\t<true/>"));
        assert!(plist.starts_with("<?xml version=\"1.0\" encoding=\"UTF-8\"?>"));
        assert!(plist.trim_end().ends_with("</plist>"));
    }

    #[test]
    fn the_launch_agent_escapes_the_path() {
        let plist = launch_agent(Path::new("/Apps/a&b <c>/baum"));
        assert!(
            plist.contains("<string>/Apps/a&amp;b &lt;c&gt;/baum</string>"),
            "{plist}"
        );
    }

    #[test]
    fn the_run_key_holds_the_quoted_executable_and_flag() {
        let exe = Path::new(r"C:\Users\A B\AppData\Local\baum\baum-desktop.exe");
        assert_eq!(
            run_value(exe),
            r#""C:\Users\A B\AppData\Local\baum\baum-desktop.exe" --hidden"#
        );
        assert_eq!(
            reg_add_args(exe),
            vec![
                "add",
                r"HKCU\Software\Microsoft\Windows\CurrentVersion\Run",
                "/v",
                "baum",
                "/t",
                "REG_SZ",
                "/d",
                r#""C:\Users\A B\AppData\Local\baum\baum-desktop.exe" --hidden"#,
                "/f"
            ]
        );
        assert_eq!(
            reg_delete_args(),
            vec![
                "delete",
                r"HKCU\Software\Microsoft\Windows\CurrentVersion\Run",
                "/v",
                "baum",
                "/f"
            ]
        );
        assert_eq!(reg_query_args()[0], "query");
    }

    #[test]
    fn entries_are_written_replaced_and_removed() {
        let dir = temp_dir("entry");
        let path = dir.join("autostart").join(DESKTOP_FILE);
        write_entry(&path, "one").unwrap();
        assert_eq!(fs::read_to_string(&path).unwrap(), "one");
        write_entry(&path, "two").unwrap();
        assert_eq!(fs::read_to_string(&path).unwrap(), "two");
        assert_eq!(fs::read_dir(path.parent().unwrap()).unwrap().count(), 1);
        remove_entry(&path).unwrap();
        assert!(!path.exists());
        remove_entry(&path).unwrap();
    }

    const GNOME_DISABLED: &str = "[Desktop Entry]\nType=Application\nName=baum\nExec=/old/baum --hidden\nX-GNOME-Autostart-enabled=false\nX-GNOME-Autostart-Delay=7\n\n[Desktop Action x]\nName=Other\nHidden=true\n";

    #[test]
    fn a_desktop_environment_disable_is_seen() {
        assert!(entry_disabled(GNOME_DISABLED));
        assert!(entry_disabled("[Desktop Entry]\nHidden=true\n"));
        assert!(entry_disabled("[Desktop Entry]\nHidden = TRUE\n"));
        assert!(!entry_disabled(
            "[Desktop Entry]\nHidden=false\nX-GNOME-Autostart-enabled=true\n"
        ));
        // Keys in other groups do not count.
        assert!(!entry_disabled(
            "[Desktop Entry]\nName=baum\n[Desktop Action x]\nHidden=true\n"
        ));
        assert!(!entry_disabled(
            &desktop_entry(Path::new("/usr/bin/baum-desktop")).unwrap()
        ));
    }

    #[test]
    fn rewriting_updates_exec_and_keeps_everything_else() {
        let rewritten = rewrite_entry(GNOME_DISABLED, Path::new("/new/baum")).unwrap();
        assert_eq!(
            rewritten,
            "[Desktop Entry]\nType=Application\nName=baum\nExec=/new/baum --hidden\nX-GNOME-Autostart-enabled=true\nX-GNOME-Autostart-Delay=7\n\n[Desktop Action x]\nName=Other\nHidden=true\n"
        );
        assert!(!entry_disabled(&rewritten));
        // A group lacking the keys gets them; a file without the group is replaced.
        assert_eq!(
            rewrite_entry("[Desktop Entry]\nName=mine\n", Path::new("/b")).unwrap(),
            "[Desktop Entry]\nName=mine\nExec=/b --hidden\nX-GNOME-Autostart-enabled=true\n"
        );
        assert_eq!(
            rewrite_entry("garbage", Path::new("/b")).unwrap(),
            desktop_entry(Path::new("/b")).unwrap()
        );
    }

    #[test]
    fn a_path_with_a_line_break_is_refused() {
        assert!(desktop_entry(Path::new("/a\nExec=/evil")).is_err());
        assert!(rewrite_entry("[Desktop Entry]\n", Path::new("/a\rb")).is_err());
    }

    #[test]
    fn task_manager_disables_are_read_from_startup_approved() {
        let value = |hex: &str| format!("\n HKEY\\...\\Run\n    baum    REG_BINARY    {hex}\n\n");
        assert!(!startup_disabled(&value("020000000000000000000000")));
        assert!(!startup_disabled(&value("060000000000000000000000")));
        assert!(startup_disabled(&value("030000007C3B5D1A2E9CD801")));
        assert!(startup_disabled(&value("070000000000000000000000")));
        assert!(!startup_disabled(""));
        assert_eq!(reg_approved_query_args()[1], APPROVED_KEY);
        assert_eq!(reg_approved_delete_args().last().unwrap(), "/f");
    }

    #[test]
    fn the_entry_names_a_real_installed_path() {
        assert_eq!(
            check_executable(Path::new("/opt/baum/baum-desktop (deleted)")).unwrap(),
            PathBuf::from("/opt/baum/baum-desktop")
        );
        assert_eq!(
            check_executable(Path::new("/usr/bin/baum-desktop")).unwrap(),
            PathBuf::from("/usr/bin/baum-desktop")
        );
        let err = check_executable(Path::new(
            "/private/var/folders/x/AppTranslocation/ABC/d/baum.app/Contents/MacOS/baum-desktop",
        ))
        .unwrap_err();
        assert!(err.contains("Applications"), "{err}");
        let exe = Path::new("/usr/bin/baum-desktop");
        assert!(installed(exe, false, false));
        assert!(!installed(exe, true, false));
        assert!(!installed(exe, false, true));
        assert!(!installed(
            Path::new("/oss/desktop/target/debug/baum-desktop"),
            false,
            false
        ));
        assert!(!installed(
            Path::new("/oss/desktop/target/release/baum-desktop"),
            false,
            false
        ));
        assert!(installed(
            Path::new("/opt/target/baum-desktop"),
            false,
            false
        ));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn the_linux_entry_lives_under_the_xdg_config_directory() {
        use platform::entry_path_in;
        use std::ffi::OsString;
        let os = |s: &str| Some(OsString::from(s));
        assert_eq!(
            entry_path_in(os("/cfg"), os("/home/u")),
            Some(PathBuf::from("/cfg/autostart/baum.desktop"))
        );
        assert_eq!(
            entry_path_in(None, os("/home/u")),
            Some(PathBuf::from("/home/u/.config/autostart/baum.desktop"))
        );
        // A relative XDG_CONFIG_HOME is ignored, as the specification says.
        assert_eq!(
            entry_path_in(os("cfg"), os("/home/u")),
            Some(PathBuf::from("/home/u/.config/autostart/baum.desktop"))
        );
        assert_eq!(entry_path_in(None, None), None);
    }
}

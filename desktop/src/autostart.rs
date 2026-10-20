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

/// The executable the login entry starts. An AppImage runs from a temporary
/// mount, so its own path (`APPIMAGE`) is the stable one.
fn executable() -> Result<PathBuf, String> {
    if let Some(image) = std::env::var_os("APPIMAGE").filter(|v| !v.is_empty()) {
        return Ok(PathBuf::from(image));
    }
    std::env::current_exe().map_err(|e| format!("cannot locate the app executable: {e}"))
}

/// Whether start at login is on.
pub fn enabled() -> bool {
    platform::enabled()
}

/// Turns start at login on or off.
pub fn set(on: bool) -> Result<(), String> {
    platform::set(on, &executable()?)
}

/// Rewrites an enabled entry so it names this executable.
pub fn refresh() {
    if enabled()
        && let Err(e) = set(true)
    {
        eprintln!("baum desktop: cannot refresh the start at login entry: {e}");
    }
}

/// An XDG autostart entry (Desktop Entry Specification 1.5).
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
fn desktop_entry(exe: &Path) -> String {
    format!(
        "[Desktop Entry]\n\
         Type=Application\n\
         Name=baum\n\
         Comment=Keep up with what your coding agents build\n\
         Exec={} {HIDDEN_FLAG}\n\
         Terminal=false\n\
         X-GNOME-Autostart-enabled=true\n",
        desktop_exec_quote(&exe.to_string_lossy())
    )
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
        entry_path().is_some_and(|p| p.is_file())
    }

    pub fn set(on: bool, exe: &Path) -> Result<(), String> {
        let path = entry_path().ok_or("cannot find the XDG config directory")?;
        if on {
            write_entry(&path, &desktop_entry(exe))
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

    fn reg(args: &[String]) -> Result<bool, String> {
        let status = Command::new("reg.exe")
            .args(args)
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .creation_flags(CREATE_NO_WINDOW)
            .status()
            .map_err(|e| format!("cannot run reg.exe: {e}"))?;
        Ok(status.success())
    }

    pub fn enabled() -> bool {
        reg(&reg_query_args()).unwrap_or(false)
    }

    pub fn set(on: bool, exe: &Path) -> Result<(), String> {
        if on {
            if !reg(&reg_add_args(exe))? {
                return Err("reg.exe could not set the Run key".into());
            }
        } else if enabled() && !reg(&reg_delete_args())? {
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
        let entry = desktop_entry(Path::new("/usr/bin/baum-desktop"));
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

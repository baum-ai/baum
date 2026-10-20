//! The `baum` command on the user's PATH. Coding-agent hooks run `baum` by
//! name, so the app makes its bundled binary that command: on macOS and Linux
//! a symbolic link in a directory on the login shell's PATH, pointing at a
//! stable path of the bundled binary (so updating the app updates the
//! command); on Windows the bundled directory added to the user's PATH.
//!
//! What is on the PATH comes from the bundled `baum daemon login-path`, which
//! reads the login shell the way the daemon does. This module only decides
//! where the link goes and changes it, and only what the app recorded as its
//! own is ever removed. A different `baum` already on the PATH is replaced
//! only with the user's confirmation, and moved aside so that uninstalling
//! puts it back.

use std::fs;
use std::io;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

/// The name of the link and of the bundled binary.
#[cfg(unix)]
const NAME: &str = "baum";

/// Where a replaced baum is moved, beside the link it gave way to.
#[cfg(unix)]
const BACKUP_SUFFIX: &str = ".replaced-by-baum-app";

/// What `baum daemon login-path --json` prints.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct LoginPath {
    #[serde(default)]
    pub path: Vec<PathBuf>,
    #[serde(default)]
    pub baum: Option<Found>,
    /// Why the login shell could not be read; the PATH is then the app's.
    #[serde(default)]
    pub error: Option<String>,
}

/// A `baum` on the PATH: the first one, which is what the hooks run.
#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct Found {
    pub path: PathBuf,
    /// The file it runs, with symbolic links resolved.
    pub resolved: PathBuf,
    #[serde(default)]
    pub version: String,
}

/// What the app installed, kept in its configuration directory so that
/// uninstalling removes exactly that and nothing else.
#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct Record {
    /// The link the app created; on Windows, the directory it added to the
    /// user's PATH.
    pub link: PathBuf,
    /// What the link points at: the stable path of the bundled baum.
    pub target: PathBuf,
    /// Where a different baum the link replaced was moved.
    #[serde(default)]
    pub backup: Option<PathBuf>,
    /// Whether the link's directory needed an administrator.
    #[serde(default)]
    pub admin: bool,
}

/// What installing would do.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Plan {
    pub link: PathBuf,
    /// A different baum at `link`, which installing replaces only once the
    /// user confirmed this path. It is moved to `backup`.
    pub replaces: Option<Found>,
    pub backup: Option<PathBuf>,
    /// Whether the directory needs an administrator (a password prompt).
    pub admin: bool,
    /// Whether the directory is on the login shell's PATH. When it is not,
    /// the command works only once the user adds it.
    pub on_path: bool,
}

/// The command's state, for the setup screen.
#[derive(Debug, Clone, Serialize)]
pub struct Status {
    /// The stable path of the bundled baum that the command runs.
    pub target: Option<PathBuf>,
    /// Why the command cannot be installed from where the app runs now.
    pub unavailable: Option<String>,
    /// The baum that the hooks run now, if any.
    pub found: Option<Found>,
    /// Whether that is the app's own baum.
    pub installed: bool,
    /// What the app installed earlier, if it still holds.
    pub record: Option<Record>,
    /// What Install would do, unless the command is installed.
    pub plan: Option<Plan>,
    /// Why the login shell's PATH could not be read.
    pub path_error: Option<String>,
}

/// Runs a shell script with administrator rights: the password prompt of
/// the operating system, then `/bin/sh -c script sh args...`. Paths travel
/// only as arguments, never inside the script.
pub trait Elevate {
    fn run(&self, script: &str, args: &[&Path]) -> Result<(), String>;
}

/// The stable path the command links to. The binary next to the app's
/// executable is stable in an installed app bundle, which updates replace in
/// place. Two places are not: an AppImage runs from a mount that disappears
/// when it quits, so the app keeps a copy in its data directory and refreshes
/// it at every launch (`managed`); and a macOS app run from a disk image or
/// translocated by Gatekeeper must first be moved to Applications.
pub fn stable_target(bundled: &Path, managed: Option<&Path>) -> Result<PathBuf, String> {
    if let Some(copy) = managed {
        return Ok(copy.to_path_buf());
    }
    let text = bundled.to_string_lossy();
    if cfg!(target_os = "macos")
        && (text.contains("/AppTranslocation/") || text.starts_with("/Volumes/"))
    {
        return Err(
            "Move baum to your Applications folder and open it from there to install the baum command."
                .into(),
        );
    }
    Ok(bundled.to_path_buf())
}

/// Puts a copy of `source` at `copy` unless an identical one is there, for an
/// AppImage. The copy and its directory are mode 0755 so that a daemon
/// trusts it as the baum on its PATH.
pub fn refresh_copy(source: &Path, copy: &Path) -> io::Result<bool> {
    let data = fs::read(source)?;
    if fs::read(copy).is_ok_and(|current| current == data) {
        return Ok(false);
    }
    let dir = copy.parent().ok_or(io::ErrorKind::InvalidInput)?;
    create_dir(dir)?;
    let temporary = dir.join(format!(".baum-{}", std::process::id()));
    write_executable(&temporary, &data)?;
    fs::rename(&temporary, copy).inspect_err(|_| {
        let _ = fs::remove_file(&temporary);
    })?;
    Ok(true)
}

#[cfg(unix)]
fn create_dir(dir: &Path) -> io::Result<()> {
    use std::os::unix::fs::DirBuilderExt;
    fs::DirBuilder::new()
        .recursive(true)
        .mode(0o755)
        .create(dir)
}

#[cfg(not(unix))]
fn create_dir(dir: &Path) -> io::Result<()> {
    fs::create_dir_all(dir)
}

#[cfg(unix)]
fn write_executable(path: &Path, data: &[u8]) -> io::Result<()> {
    use std::io::Write;
    use std::os::unix::fs::OpenOptionsExt;
    let mut file = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o755)
        .open(path)?;
    file.write_all(data)?;
    file.sync_all()
}

#[cfg(not(unix))]
fn write_executable(path: &Path, data: &[u8]) -> io::Result<()> {
    fs::write(path, data)
}

/// Whether two paths name the same file once links are resolved.
fn same_file(a: &Path, b: &Path) -> bool {
    match (fs::canonicalize(a), fs::canonicalize(b)) {
        (Ok(a), Ok(b)) if cfg!(windows) => a
            .to_string_lossy()
            .eq_ignore_ascii_case(&b.to_string_lossy()),
        (Ok(a), Ok(b)) => a == b,
        _ => false,
    }
}

/// Whether the record still describes the filesystem: its link is a
/// symbolic link to its target, whether or not the target still exists.
#[cfg(unix)]
fn holds(record: &Record) -> bool {
    fs::read_link(&record.link).is_ok_and(|target| target == record.target)
}

/// Works out the command's state. `home` is the user's home directory.
#[cfg(unix)]
pub fn status(
    target: Result<PathBuf, String>,
    login: &LoginPath,
    record: Option<Record>,
    home: &Path,
) -> Status {
    let record = record.filter(holds);
    let found = login.baum.clone();
    let mut status = Status {
        target: None,
        unavailable: None,
        installed: false,
        plan: None,
        path_error: login.error.clone(),
        found,
        record,
    };
    let target = match target {
        Ok(target) => target,
        Err(why) => {
            status.unavailable = Some(why);
            return status;
        }
    };
    status.installed = status
        .found
        .as_ref()
        .is_some_and(|found| same_file(&found.resolved, &target));
    if !status.installed {
        status.plan = Some(plan(login, status.record.as_ref(), home, writable));
    }
    status.target = Some(target);
    status
}

/// Where the link goes. A different baum first on the PATH is replaced where
/// it is, so the link takes its place in the PATH order; otherwise the link
/// goes in the first of `~/.local/bin` and `~/bin` on the PATH, else in
/// `/usr/local/bin` (with an administrator's password unless it is writable),
/// else in `~/.local/bin`, which the user then has to add to the PATH.
#[cfg(unix)]
fn plan(
    login: &LoginPath,
    record: Option<&Record>,
    home: &Path,
    writable: impl Fn(&Path) -> bool,
) -> Plan {
    let on_path = |dir: &Path| login.path.iter().any(|entry| entry == dir);
    if let Some(found) = &login.baum {
        let dir = found.path.parent().unwrap_or(Path::new("/"));
        let ours = record.is_some_and(|r| r.link == found.path);
        return Plan {
            link: found.path.clone(),
            replaces: (!ours).then(|| found.clone()),
            backup: (!ours).then(|| backup_path(&found.path)),
            admin: !writable(dir),
            on_path: true,
        };
    }
    let personal = [home.join(".local/bin"), home.join("bin")];
    let system = PathBuf::from("/usr/local/bin");
    let dir = login
        .path
        .iter()
        .find(|entry| personal.contains(entry) && writable(entry))
        .cloned()
        .or_else(|| on_path(&system).then(|| system.clone()))
        .unwrap_or_else(|| personal[0].clone());
    let link = dir.join(NAME);
    // Something already there that is not on the PATH as a working baum: a
    // broken link (the app it pointed into was deleted) is replaced, anything
    // else only after confirmation.
    let occupied = fs::symlink_metadata(&link).is_ok()
        && fs::metadata(&link).is_ok()
        && !record.is_some_and(|r| r.link == link);
    Plan {
        replaces: occupied.then(|| Found {
            path: link.clone(),
            resolved: fs::canonicalize(&link).unwrap_or_else(|_| link.clone()),
            version: String::new(),
        }),
        backup: occupied.then(|| backup_path(&link)),
        admin: !writable(&dir),
        on_path: on_path(&dir),
        link,
    }
}

/// A free name beside `path` for the baum it replaces.
#[cfg(unix)]
fn backup_path(path: &Path) -> PathBuf {
    let base = format!("{}{BACKUP_SUFFIX}", path.display());
    let mut candidate = PathBuf::from(&base);
    let mut n = 1;
    while fs::symlink_metadata(&candidate).is_ok() {
        candidate = PathBuf::from(format!("{base}.{n}"));
        n += 1;
    }
    candidate
}

/// Whether this user can create files in `dir`, or create `dir` itself.
#[cfg(unix)]
pub fn writable(dir: &Path) -> bool {
    use std::ffi::CString;
    use std::os::unix::ffi::OsStrExt;
    let mut dir = dir;
    loop {
        if dir.exists() {
            let Ok(path) = CString::new(dir.as_os_str().as_bytes()) else {
                return false;
            };
            // SAFETY: path is a valid NUL-terminated string.
            return dir.is_dir() && unsafe { libc::access(path.as_ptr(), libc::W_OK) } == 0;
        }
        match dir.parent() {
            Some(parent) => dir = parent,
            None => return false,
        }
    }
}

/// Installs the plan's link to `target`. A plan that replaces a different
/// baum needs `confirmed` to name it: the setup screen passes the path it
/// showed the user and asked about.
#[cfg(unix)]
pub fn install(
    plan: &Plan,
    target: &Path,
    confirmed: Option<&Path>,
    elevate: &dyn Elevate,
) -> Result<Record, String> {
    if let Some(replaced) = &plan.replaces
        && confirmed != Some(replaced.path.as_path())
    {
        return Err(format!(
            "Confirm replacing {} before installing.",
            replaced.path.display()
        ));
    }
    let dir = plan.link.parent().ok_or("The link has no directory.")?;
    let backup = plan.replaces.as_ref().and(plan.backup.clone());
    if plan.admin {
        let none = PathBuf::new();
        elevate.run(
            INSTALL_SCRIPT,
            &[dir, &plan.link, backup.as_deref().unwrap_or(&none), target],
        )?;
    } else {
        link(dir, &plan.link, backup.as_deref(), target).map_err(|e| {
            format!(
                "Cannot install the baum command at {}: {e}",
                plan.link.display()
            )
        })?;
    }
    Ok(Record {
        link: plan.link.clone(),
        target: target.to_path_buf(),
        backup,
        admin: plan.admin,
    })
}

/// The same steps as `link`, as root: create the directory, move a replaced
/// baum aside, and put the link in place.
#[cfg(unix)]
const INSTALL_SCRIPT: &str = r#"set -e
mkdir -p "$1"
if [ -n "$3" ]; then mv "$2" "$3"; fi
ln -sfn "$4" "$2""#;

/// Removes the link only while it still points where the app put it, then
/// puts a baum it replaced back.
#[cfg(unix)]
const UNINSTALL_SCRIPT: &str = r#"set -e
[ "$(readlink "$1")" = "$2" ] || exit 3
rm -f "$1"
if [ -n "$3" ] && { [ -e "$3" ] || [ -L "$3" ]; }; then mv "$3" "$1"; fi"#;

#[cfg(unix)]
fn link(dir: &Path, link: &Path, backup: Option<&Path>, target: &Path) -> io::Result<()> {
    create_dir(dir)?;
    if let Some(backup) = backup {
        fs::rename(link, backup)?;
    }
    let temporary = dir.join(format!(".baum-link-{}", std::process::id()));
    let _ = fs::remove_file(&temporary);
    let placed =
        std::os::unix::fs::symlink(target, &temporary).and_then(|()| fs::rename(&temporary, link));
    if let Err(e) = placed {
        let _ = fs::remove_file(&temporary);
        if let Some(backup) = backup {
            let _ = fs::rename(backup, link);
        }
        return Err(e);
    }
    Ok(())
}

/// Removes what the app installed and nothing else: the link, while it still
/// points at the recorded target, and puts back a baum it replaced. A link
/// someone changed since is left alone. Returns what happened, for the user.
#[cfg(unix)]
pub fn uninstall(record: &Record, elevate: &dyn Elevate) -> Result<String, String> {
    uninstall_with(record, elevate, writable)
}

#[cfg(unix)]
fn uninstall_with(
    record: &Record,
    elevate: &dyn Elevate,
    writable: impl Fn(&Path) -> bool,
) -> Result<String, String> {
    if !holds(record) {
        return Ok(format!(
            "{} no longer points at the app's baum, so it was left alone.",
            record.link.display()
        ));
    }
    let restore = record
        .backup
        .as_deref()
        .filter(|b| fs::symlink_metadata(b).is_ok());
    let dir = record.link.parent().unwrap_or(Path::new("/"));
    if writable(dir) {
        fs::remove_file(&record.link)
            .and_then(|()| restore.map_or(Ok(()), |b| fs::rename(b, &record.link)))
            .map_err(|e| format!("Cannot remove {}: {e}", record.link.display()))?;
    } else {
        let none = PathBuf::new();
        elevate.run(
            UNINSTALL_SCRIPT,
            &[&record.link, &record.target, restore.unwrap_or(&none)],
        )?;
    }
    Ok(match restore {
        Some(_) => format!(
            "Removed the baum command and put back the baum it had replaced at {}.",
            record.link.display()
        ),
        None => format!("Removed the baum command at {}.", record.link.display()),
    })
}

/// The administrator prompt of the operating system.
pub struct SystemPrompt;

#[cfg(target_os = "macos")]
impl Elevate for SystemPrompt {
    fn run(&self, script: &str, args: &[&Path]) -> Result<(), String> {
        // AppleScript quotes every argument; nothing is interpolated.
        let apple = r#"on run argv
set shellCommand to "/bin/sh -c " & quoted form of (item 1 of argv) & " sh"
repeat with i from 2 to count of argv
set shellCommand to shellCommand & " " & quoted form of (item i of argv)
end repeat
do shell script shellCommand with prompt "baum needs your password to change the baum command." with administrator privileges
end run"#;
        let output = std::process::Command::new("/usr/bin/osascript")
            .arg("-e")
            .arg(apple)
            .arg(script)
            .args(args)
            .output()
            .map_err(|e| format!("Cannot ask for an administrator's password: {e}"))?;
        if output.status.success() {
            return Ok(());
        }
        let stderr = String::from_utf8_lossy(&output.stderr);
        if stderr.contains("-128") {
            return Err("Cancelled.".into());
        }
        Err(format!(
            "The change needs an administrator and did not succeed: {}",
            stderr.trim()
        ))
    }
}

#[cfg(all(unix, not(target_os = "macos")))]
impl Elevate for SystemPrompt {
    fn run(&self, script: &str, args: &[&Path]) -> Result<(), String> {
        let output = std::process::Command::new("pkexec")
            .arg("/bin/sh")
            .arg("-c")
            .arg(script)
            .arg("sh")
            .args(args)
            .output()
            .map_err(|e| {
                format!("Cannot ask for an administrator's password (pkexec: {e}). Install the command as root instead, or put ~/.local/bin on your PATH and try again.")
            })?;
        match output.status.code() {
            Some(0) => Ok(()),
            // pkexec: the user dismissed the prompt, or was not authorized.
            Some(126) | Some(127) => Err("Cancelled.".into()),
            _ => Err(format!(
                "The change needs an administrator and did not succeed: {}",
                String::from_utf8_lossy(&output.stderr).trim()
            )),
        }
    }
}

#[cfg(windows)]
impl Elevate for SystemPrompt {
    fn run(&self, _: &str, _: &[&Path]) -> Result<(), String> {
        Err("Not used on Windows.".into())
    }
}

/// Windows has no links on the PATH to place: the app adds the directory of
/// its bundled `baum.exe` to the front of the user's PATH (new terminals
/// pick it up) and removes it again on uninstall.
#[cfg(windows)]
pub fn status(
    target: Result<PathBuf, String>,
    login: &LoginPath,
    record: Option<Record>,
    _home: &Path,
) -> Status {
    let mut status = Status {
        target: None,
        unavailable: None,
        installed: false,
        plan: None,
        path_error: login.error.clone(),
        found: login.baum.clone(),
        record,
    };
    let target = match target {
        Ok(target) => target,
        Err(why) => {
            status.unavailable = Some(why);
            return status;
        }
    };
    let dir = target.parent().unwrap_or(Path::new("")).to_path_buf();
    let listed = windows::user_path()
        .map(|entries| entries.iter().any(|e| windows::same_dir(e, &dir)))
        .unwrap_or(false);
    status.installed = listed
        || status
            .found
            .as_ref()
            .is_some_and(|found| same_file(&found.resolved, &target));
    status.record = status.record.filter(|r| listed && r.link == dir);
    if !status.installed {
        status.plan = Some(Plan {
            link: dir,
            replaces: None,
            backup: None,
            admin: false,
            on_path: true,
        });
    }
    status.target = Some(target);
    status
}

#[cfg(windows)]
pub fn install(
    plan: &Plan,
    target: &Path,
    _confirmed: Option<&Path>,
    _elevate: &dyn Elevate,
) -> Result<Record, String> {
    windows::change_user_path(&plan.link, true)?;
    Ok(Record {
        link: plan.link.clone(),
        target: target.to_path_buf(),
        backup: None,
        admin: false,
    })
}

#[cfg(windows)]
pub fn uninstall(record: &Record, _elevate: &dyn Elevate) -> Result<String, String> {
    windows::change_user_path(&record.link, false)?;
    Ok(format!(
        "Removed {} from your PATH. Open a new terminal for it to take effect.",
        record.link.display()
    ))
}

#[cfg(windows)]
mod windows {
    use std::path::{Path, PathBuf};
    use std::process::Command;

    /// Reads and writes the user's PATH in the registry as PowerShell does,
    /// keeping unexpanded variables, and announces the change so new
    /// terminals see it. The directory travels in an environment variable.
    const SCRIPT: &str = r#"$ErrorActionPreference = 'Stop'
$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
$path = [string]$key.GetValue('Path', '', 'DoNotExpandEnvironmentNames')
if ($env:BAUM_PATH_ACTION -eq 'read') { Write-Output $path; exit 0 }
$dir = $env:BAUM_PATH_DIR.TrimEnd('\')
$rest = @($path -split ';' | Where-Object { $_ -and ($_.TrimEnd('\') -ne $dir) })
if ($env:BAUM_PATH_ACTION -eq 'add') { $rest = @($dir) + $rest }
$key.SetValue('Path', ($rest -join ';'), 'ExpandString')
[Environment]::SetEnvironmentVariable('BAUM_PATH_REFRESH', '1', 'User')
[Environment]::SetEnvironmentVariable('BAUM_PATH_REFRESH', $null, 'User')"#;

    fn powershell(action: &str, dir: &Path) -> Result<String, String> {
        let output = Command::new("powershell.exe")
            .args(["-NoProfile", "-NonInteractive", "-Command", SCRIPT])
            .env("BAUM_PATH_ACTION", action)
            .env("BAUM_PATH_DIR", dir)
            .output()
            .map_err(|e| format!("Cannot run PowerShell: {e}"))?;
        if !output.status.success() {
            return Err(format!(
                "Cannot change your PATH: {}",
                String::from_utf8_lossy(&output.stderr).trim()
            ));
        }
        Ok(String::from_utf8_lossy(&output.stdout).trim().to_string())
    }

    pub fn user_path() -> Result<Vec<PathBuf>, String> {
        Ok(powershell("read", Path::new(""))?
            .split(';')
            .filter(|e| !e.is_empty())
            .map(PathBuf::from)
            .collect())
    }

    pub fn change_user_path(dir: &Path, add: bool) -> Result<(), String> {
        powershell(if add { "add" } else { "remove" }, dir).map(|_| ())
    }

    pub fn same_dir(a: &Path, b: &Path) -> bool {
        let trim = |p: &Path| p.to_string_lossy().trim_end_matches('\\').to_lowercase();
        trim(a) == trim(b)
    }
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;
    use std::os::unix::fs::{PermissionsExt, symlink};

    /// Runs the administrator scripts as this user, so tests exercise them
    /// in directories this user can write.
    struct Shell;
    impl Elevate for Shell {
        fn run(&self, script: &str, args: &[&Path]) -> Result<(), String> {
            let status = std::process::Command::new("/bin/sh")
                .arg("-c")
                .arg(script)
                .arg("sh")
                .args(args)
                .status()
                .map_err(|e| e.to_string())?;
            status
                .success()
                .then_some(())
                .ok_or_else(|| format!("script failed: {status}"))
        }
    }

    /// Fails the test if a privileged script would run.
    struct NoPrompt;
    impl Elevate for NoPrompt {
        fn run(&self, _: &str, _: &[&Path]) -> Result<(), String> {
            panic!("asked for an administrator")
        }
    }

    struct World {
        root: PathBuf,
        home: PathBuf,
        app: PathBuf,
    }

    impl World {
        fn new(name: &str) -> World {
            let root =
                std::env::temp_dir().join(format!("baum-install-{name}-{}", std::process::id()));
            let _ = fs::remove_dir_all(&root);
            let home = root.join("home");
            let app = root.join("Applications/baum.app/Contents/MacOS/baum");
            fs::create_dir_all(&home).unwrap();
            executable(&app, "app");
            World { root, home, app }
        }

        fn local_bin(&self) -> PathBuf {
            self.home.join(".local/bin")
        }

        /// What login-path reports for this PATH: the first executable baum.
        fn login(&self, path: &[PathBuf]) -> LoginPath {
            let baum = path.iter().map(|dir| dir.join(NAME)).find(|p| {
                fs::metadata(p).is_ok_and(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
            });
            LoginPath {
                path: path.to_vec(),
                baum: baum.map(|path| Found {
                    resolved: fs::canonicalize(&path).unwrap(),
                    path,
                    version: "v0.2.9".into(),
                }),
                error: None,
            }
        }

        fn status(&self, path: &[PathBuf], record: Option<Record>) -> Status {
            status(Ok(self.app.clone()), &self.login(path), record, &self.home)
        }
    }

    impl Drop for World {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.root);
        }
    }

    fn executable(path: &Path, body: &str) {
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(path, format!("#!/bin/sh\necho {body}\n")).unwrap();
        fs::set_permissions(path, fs::Permissions::from_mode(0o755)).unwrap();
    }

    #[test]
    fn installs_into_local_bin_when_no_baum_is_on_the_path() {
        let w = World::new("fresh");
        let path = [w.local_bin(), PathBuf::from("/usr/bin")];
        let before = w.status(&path, None);
        assert!(!before.installed && before.found.is_none());
        let plan = before.plan.unwrap();
        assert_eq!(plan.link, w.local_bin().join("baum"));
        assert!(plan.replaces.is_none() && !plan.admin && plan.on_path);

        let record = install(&plan, &w.app, None, &NoPrompt).unwrap();
        assert_eq!(fs::read_link(&record.link).unwrap(), w.app);
        let after = w.status(&path, Some(record.clone()));
        assert!(after.installed && after.plan.is_none());
        assert_eq!(after.record, Some(record));
    }

    #[test]
    fn prefers_a_personal_directory_and_falls_back_to_usr_local_bin() {
        let w = World::new("choice");
        let home_bin = w.home.join("bin");
        let usr_local = PathBuf::from("/usr/local/bin");
        let plan = w
            .status(&[usr_local.clone(), home_bin.clone()], None)
            .plan
            .unwrap();
        assert_eq!(plan.link, home_bin.join("baum"));
        let plan = w
            .status(std::slice::from_ref(&usr_local), None)
            .plan
            .unwrap();
        assert_eq!(plan.link, usr_local.join("baum"));
        assert_eq!(plan.admin, !writable(&usr_local));
        // Nothing suitable on the PATH: ~/.local/bin, flagged as off the PATH.
        let plan = w.status(&[PathBuf::from("/usr/bin")], None).plan.unwrap();
        assert_eq!(plan.link, w.local_bin().join("baum"));
        assert!(!plan.on_path);
    }

    #[test]
    fn a_directory_this_user_cannot_write_needs_an_administrator() {
        let w = World::new("admin");
        let locked = w.root.join("locked");
        fs::create_dir_all(&locked).unwrap();
        let other = locked.join("baum");
        executable(&other, "other");
        fs::set_permissions(&locked, fs::Permissions::from_mode(0o555)).unwrap();
        let plan = w.status(std::slice::from_ref(&locked), None).plan.unwrap();
        fs::set_permissions(&locked, fs::Permissions::from_mode(0o755)).unwrap();
        assert!(plan.admin, "{plan:?}");
        // The administrator's script does what the unprivileged path does.
        let record = install(&plan, &w.app, Some(&other), &Shell).unwrap();
        assert_eq!(fs::read_link(&other).unwrap(), w.app);
        assert!(record.admin);
        let backup = record.backup.clone().unwrap();
        assert_eq!(
            fs::read_to_string(&backup).unwrap(),
            "#!/bin/sh\necho other\n"
        );
        let message = uninstall_with(&record, &Shell, |_| false).unwrap();
        assert!(message.contains("put back"), "{message}");
        assert_eq!(
            fs::read_to_string(&other).unwrap(),
            "#!/bin/sh\necho other\n"
        );
        assert!(fs::symlink_metadata(&backup).is_err());
    }

    /// A baum from `go install` comes first on the PATH: it is shown, and
    /// replaced only once the user confirmed that path; uninstalling puts it
    /// back.
    #[test]
    fn replaces_a_go_installed_baum_only_after_confirmation() {
        let w = World::new("go");
        let go_bin = w.home.join("go/bin");
        let theirs = go_bin.join("baum");
        executable(&theirs, "go");
        let path = [go_bin.clone(), w.local_bin()];
        let before = w.status(&path, None);
        assert!(!before.installed);
        assert_eq!(before.found.as_ref().unwrap().path, theirs);
        let plan = before.plan.unwrap();
        assert_eq!(plan.link, theirs);
        assert_eq!(plan.replaces.as_ref().unwrap().version, "v0.2.9");
        assert_eq!(
            plan.backup.as_deref(),
            Some(go_bin.join("baum.replaced-by-baum-app").as_path())
        );

        let refused = install(&plan, &w.app, None, &NoPrompt).unwrap_err();
        assert!(refused.contains("Confirm"), "{refused}");
        let wrong = install(&plan, &w.app, Some(&w.local_bin().join("baum")), &NoPrompt);
        assert!(wrong.is_err());
        assert_eq!(fs::read_to_string(&theirs).unwrap(), "#!/bin/sh\necho go\n");

        let record = install(&plan, &w.app, Some(&theirs), &NoPrompt).unwrap();
        assert_eq!(fs::read_link(&theirs).unwrap(), w.app);
        assert!(w.status(&path, Some(record.clone())).installed);

        uninstall(&record, &NoPrompt).unwrap();
        assert_eq!(fs::read_to_string(&theirs).unwrap(), "#!/bin/sh\necho go\n");
        assert!(fs::symlink_metadata(go_bin.join("baum.replaced-by-baum-app")).is_err());
    }

    /// After the app is deleted, its link points nowhere: no baum is found,
    /// and installing again (with or without the old record) replaces the
    /// broken link without asking.
    #[test]
    fn a_link_left_by_a_deleted_app_is_replaced_without_asking() {
        let w = World::new("deleted");
        let path = [w.local_bin()];
        let plan = w.status(&path, None).plan.unwrap();
        let record = install(&plan, &w.app, None, &NoPrompt).unwrap();
        fs::remove_dir_all(w.root.join("Applications")).unwrap();
        let link = w.local_bin().join("baum");
        assert!(fs::symlink_metadata(&link).is_ok() && fs::metadata(&link).is_err());

        let moved = w.root.join("Elsewhere/baum.app/Contents/MacOS/baum");
        executable(&moved, "moved");
        for record in [Some(record), None] {
            fs::remove_file(&link).unwrap();
            symlink(&w.app, &link).unwrap();
            let status = status(Ok(moved.clone()), &w.login(&path), record, &w.home);
            assert!(!status.installed && status.found.is_none());
            let plan = status.plan.unwrap();
            assert_eq!(plan.link, link);
            assert!(plan.replaces.is_none(), "{plan:?}");
            install(&plan, &moved, None, &NoPrompt).unwrap();
            assert_eq!(fs::read_link(&link).unwrap(), moved);
        }
    }

    /// The app's own link to an older copy of the app is updated in place
    /// without confirmation.
    #[test]
    fn updates_its_own_link_without_asking() {
        let w = World::new("update");
        let path = [w.local_bin()];
        let record = install(
            &w.status(&path, None).plan.unwrap(),
            &w.app,
            None,
            &NoPrompt,
        )
        .unwrap();
        let newer = w.root.join("New/baum.app/Contents/MacOS/baum");
        executable(&newer, "new");
        let status = status(Ok(newer.clone()), &w.login(&path), Some(record), &w.home);
        assert!(!status.installed);
        let plan = status.plan.unwrap();
        assert!(plan.replaces.is_none() && plan.link == w.local_bin().join("baum"));
        install(&plan, &newer, None, &NoPrompt).unwrap();
        assert_eq!(fs::read_link(w.local_bin().join("baum")).unwrap(), newer);
    }

    /// Uninstalling removes only the link the app recorded, and leaves a link
    /// or file someone put there since.
    #[test]
    fn uninstall_removes_only_the_apps_link() {
        let w = World::new("uninstall");
        let path = [w.local_bin()];
        let record = install(
            &w.status(&path, None).plan.unwrap(),
            &w.app,
            None,
            &NoPrompt,
        )
        .unwrap();
        let message = uninstall(&record, &NoPrompt).unwrap();
        assert!(message.starts_with("Removed the baum command"), "{message}");
        assert!(fs::symlink_metadata(&record.link).is_err());
        assert!(w.app.exists(), "the bundled binary was touched");

        // Someone replaced the link with their own baum: it stays.
        executable(&record.link, "mine");
        let message = uninstall(&record, &NoPrompt).unwrap();
        assert!(message.contains("left alone"), "{message}");
        assert_eq!(
            fs::read_to_string(&record.link).unwrap(),
            "#!/bin/sh\necho mine\n"
        );
        // And the record no longer describes it.
        assert!(w.status(&path, Some(record)).record.is_none());
    }

    /// A file named baum in the chosen directory that is not a working baum
    /// on the PATH is still replaced only with confirmation.
    #[test]
    fn something_else_at_the_link_needs_confirmation() {
        let w = World::new("occupied");
        let bin = w.local_bin();
        executable(&bin.join("baum"), "off-path");
        let plan = w.status(&[PathBuf::from("/usr/bin")], None).plan.unwrap();
        assert_eq!(plan.link, bin.join("baum"));
        assert!(plan.replaces.is_some() && !plan.on_path);
        assert!(install(&plan, &w.app, None, &NoPrompt).is_err());
    }

    #[test]
    fn the_appimage_copy_is_refreshed_only_when_it_changed() {
        let w = World::new("copy");
        let copy = w.root.join("data/bin/baum");
        assert!(refresh_copy(&w.app, &copy).unwrap());
        assert!(!refresh_copy(&w.app, &copy).unwrap());
        assert_eq!(
            fs::metadata(&copy).unwrap().permissions().mode() & 0o777,
            0o755
        );
        assert_eq!(
            fs::metadata(copy.parent().unwrap())
                .unwrap()
                .permissions()
                .mode()
                & 0o777,
            0o755
        );
        executable(&w.app, "updated");
        assert!(refresh_copy(&w.app, &copy).unwrap());
        assert_eq!(fs::read(&copy).unwrap(), fs::read(&w.app).unwrap());
        assert_eq!(stable_target(&w.app, Some(&copy)).unwrap(), copy);
        assert_eq!(stable_target(&w.app, None).unwrap(), w.app);
    }

    #[test]
    fn installed_when_the_path_already_runs_the_bundled_baum() {
        // A package install (the deb puts the bundled baum in /usr/bin).
        let w = World::new("package");
        let bin = w.root.join("usr/bin");
        fs::create_dir_all(&bin).unwrap();
        symlink(&w.app, bin.join("baum")).unwrap();
        let status = w.status(&[bin], None);
        assert!(status.installed && status.record.is_none() && status.plan.is_none());
    }
}

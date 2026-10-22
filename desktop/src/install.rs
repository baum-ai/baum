//! The `baum` command on the user's PATH. Coding-agent hooks run `baum` by
//! name, so the app makes its bundled binary that command: on macOS and Linux
//! a symbolic link in a directory on the login shell's PATH, pointing at a
//! stable path of the bundled binary (so updating the app updates the
//! command); on Windows a directory holding only the app's `baum.exe`, added
//! to the user's PATH.
//!
//! What is on the PATH comes from the bundled `baum daemon login-path`, which
//! reads the login shell the way the daemon does. This module only decides
//! where the link goes and changes it, and only what the app recorded as its
//! own is ever removed. A different `baum` already on the PATH is replaced
//! only with the user's confirmation, and moved aside so that uninstalling
//! puts it back; one a package manager owns is left to it.
//!
//! Root acts only on system directories. The app asks for an administrator's
//! password only for a link directory outside the home directory that root
//! owns and only root can write, reached without symbolic links through
//! directories that are also root's and not writable by others (sticky ones
//! aside), and only for a link to a baum outside the home directory. The
//! script root then runs works by relative names inside that directory after
//! changing into it and checking again that it is the directory named, that
//! root owns it and that no one else can write it, so swapping a directory
//! between the app's check and the prompt cannot redirect it; and it refuses
//! paths that are not shaped like what the app installs, so a tampered
//! `setup.json` cannot point root at anything else.

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
    /// Why the app will not install there, and what the user can do instead.
    pub blocked: Option<String>,
    /// Updating the app's own link keeps the backup of the baum it replaced
    /// back then, so that removing the command still puts that one back.
    pub kept_backup: Option<PathBuf>,
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
    /// Whether the app's baum is installed as the command.
    pub installed: bool,
    /// Installed, but a different baum still comes first (Windows, where a
    /// system PATH directory precedes the user's).
    pub shadowed: Option<Found>,
    /// What the app installed earlier, if it still holds.
    pub record: Option<Record>,
    /// What Install would do, unless the command is installed.
    pub plan: Option<Plan>,
    /// Why the login shell's PATH could not be read.
    pub path_error: Option<String>,
}

/// Runs a shell script with administrator rights: the password prompt of
/// the operating system, then `/bin/sh -c script sh args...`. Paths travel
/// only as arguments, never inside the script. An error is the message for
/// the user.
pub trait Elevate {
    fn run(&self, script: &str, args: &[&Path]) -> Result<(), String>;
}

/// The user-facing message for a privileged script that exited with `code`.
#[cfg(unix)]
fn script_failure(code: Option<i32>, stderr: &str) -> String {
    match code {
        Some(3) => {
            "The baum command changed since the app installed it, so it was left alone.".into()
        }
        Some(4) => {
            "The paths do not look like a baum command the app installs, so nothing was changed."
                .into()
        }
        _ => format!(
            "The change needs an administrator and did not succeed: {}",
            stderr.trim()
        ),
    }
}

/// The stable path the command links to: the baum the app runs. In an
/// installed app bundle that is the binary next to the app's executable,
/// which updates replace in place; an AppImage and Windows use a copy the app
/// refreshes at every launch (see `refresh_copy`). A macOS app run from a
/// disk image or translocated by Gatekeeper must first be moved.
pub fn stable_target(cli: &Path) -> Result<PathBuf, String> {
    let text = cli.to_string_lossy();
    if cfg!(target_os = "macos")
        && (text.contains("/AppTranslocation/") || text.starts_with("/Volumes/"))
    {
        return Err(
            "Move baum to your Applications folder and open it from there to install the baum command."
                .into(),
        );
    }
    Ok(cli.to_path_buf())
}

/// Puts a copy of `source` at `copy` unless the copy is the one made last
/// time from this very `source`: a stamp beside the copy records the
/// source's size and modification time and the copy's modification time, so
/// an unchanged launch reads neither binary, and a copy changed or replaced
/// since is copied again. The new file is written beside the copy and
/// renamed over it: a daemon running the old one keeps its file. Windows
/// cannot replace a running executable, so there the old one is first renamed
/// aside. The copy and its directory are mode 0755 on Unix, so that a daemon
/// trusts it as the baum on its PATH. Returns whether it copied.
pub fn refresh_copy(source: &Path, copy: &Path) -> io::Result<bool> {
    let wanted = fs::metadata(source)?;
    let stamp_path = stamp_path(copy);
    let current =
        |wanted: &fs::Metadata| Some(format!("{}{}", source_stamp(source)?, stamp(wanted, copy)?));
    if fs::read_to_string(&stamp_path).is_ok_and(|s| Some(s) == current(&wanted)) {
        return Ok(false);
    }
    let dir = copy.parent().ok_or(io::ErrorKind::InvalidInput)?;
    create_dir(dir)?;
    let name = copy.file_name().ok_or(io::ErrorKind::InvalidInput)?;
    let temporary = dir.join(format!(
        ".{}-{}",
        name.to_string_lossy(),
        std::process::id()
    ));
    let _ = fs::remove_file(&temporary);
    let _ = fs::remove_file(&stamp_path);
    let written = write_copy(source, &temporary).and_then(|()| replace(&temporary, copy));
    if written.is_err() {
        let _ = fs::remove_file(&temporary);
    }
    written?;
    if let Some(stamp) = current(&wanted) {
        fs::write(&stamp_path, stamp)?;
    }
    Ok(true)
}

fn stamp_path(copy: &Path) -> PathBuf {
    let mut name = copy.file_name().unwrap_or_default().to_os_string();
    name.push(".stamp");
    copy.with_file_name(name)
}

/// What the stamp says for this source and the copy as it is now. File
/// times tick coarsely (a few milliseconds on Linux), so an update or a
/// rewrite within one tick keeps them; the stamp therefore also holds each
/// file's identity (a renamed-in update is a new inode, and any write moves
/// the change time, which no one can set back) and a hash of its first and
/// last 64 KiB, where a binary's headers and build id live.
fn stamp(source: &fs::Metadata, copy: &Path) -> Option<String> {
    let copied = fs::metadata(copy).ok()?;
    (copied.len() == source.len()).then_some(())?;
    Some(format!(
        "source {}\ncopy {} {}\n",
        identity(source)?,
        identity(&copied)?,
        ends_hash(copy)?
    ))
}

/// The source's part of the stamp also hashes its ends; it is kept with the
/// source's identity so a changed source is noticed even if its size and
/// times match.
fn source_stamp(source: &Path) -> Option<String> {
    Some(format!(
        "{} {}",
        identity(&fs::metadata(source).ok()?)?,
        ends_hash(source)?
    ))
}

fn identity(m: &fs::Metadata) -> Option<String> {
    let nanos = |t: io::Result<std::time::SystemTime>| {
        t.ok()?
            .duration_since(std::time::UNIX_EPOCH)
            .ok()
            .map(|d| d.as_nanos())
    };
    #[cfg(unix)]
    let extra = {
        use std::os::unix::fs::MetadataExt;
        format!("{}:{} {}.{}", m.dev(), m.ino(), m.ctime(), m.ctime_nsec())
    };
    #[cfg(not(unix))]
    let extra = String::new();
    Some(format!("{} {} {extra}", m.len(), nanos(m.modified())?))
}

/// FNV-1a over the first and last 64 KiB of a file: cheap, and stable
/// across builds of the app.
fn ends_hash(path: &Path) -> Option<String> {
    use std::io::{Read, Seek, SeekFrom};
    const SPAN: u64 = 64 * 1024;
    let mut file = fs::File::open(path).ok()?;
    let len = file.metadata().ok()?.len();
    let mut hash: u64 = 0xcbf2_9ce4_8422_2325;
    let mut feed = |bytes: &[u8]| {
        for b in bytes {
            hash ^= u64::from(*b);
            hash = hash.wrapping_mul(0x0100_0000_01b3);
        }
    };
    let mut buffer = Vec::with_capacity(SPAN as usize);
    file.by_ref().take(SPAN).read_to_end(&mut buffer).ok()?;
    feed(&buffer);
    if len > SPAN {
        buffer.clear();
        file.seek(SeekFrom::Start(len.saturating_sub(SPAN).max(SPAN)))
            .ok()?;
        file.take(SPAN).read_to_end(&mut buffer).ok()?;
        feed(&buffer);
    }
    Some(format!("{hash:016x}"))
}

fn write_copy(source: &Path, temporary: &Path) -> io::Result<()> {
    let mut from = fs::File::open(source)?;
    let mut to = new_executable(temporary)?;
    io::copy(&mut from, &mut to)?;
    to.sync_all()
}

fn replace(temporary: &Path, copy: &Path) -> io::Result<()> {
    match fs::rename(temporary, copy) {
        Err(e) if cfg!(windows) && copy.exists() => {
            // A running baum.exe can be renamed but not replaced: move it to a
            // name of its own, and remove those no longer running.
            let dir = copy.parent().ok_or(io::ErrorKind::InvalidInput)?;
            let stamp = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_nanos())
                .unwrap_or_default();
            let old = dir.join(format!("baum.{}.{stamp}.old", std::process::id()));
            fs::rename(copy, &old).map_err(|_| e)?;
            fs::rename(temporary, copy)?;
            remove_old(dir);
            Ok(())
        }
        result => {
            if result.is_ok()
                && let Some(dir) = copy.parent()
            {
                remove_old(dir);
            }
            result
        }
    }
}

/// Removes the copies renamed aside earlier; one still running stays.
fn remove_old(dir: &Path) {
    let Ok(entries) = fs::read_dir(dir) else {
        return;
    };
    for entry in entries.flatten() {
        let name = entry.file_name();
        let name = name.to_string_lossy();
        if name.starts_with("baum.") && name.ends_with(".old") {
            let _ = fs::remove_file(entry.path());
        }
    }
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
fn new_executable(path: &Path) -> io::Result<fs::File> {
    use std::os::unix::fs::OpenOptionsExt;
    fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o755)
        .open(path)
}

#[cfg(not(unix))]
fn new_executable(path: &Path) -> io::Result<fs::File> {
    fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(path)
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

/// Whether a record has the shape of what the app installs: a link named
/// `baum`, and a backup beside it with the backup suffix. The record lives in
/// a file the user can edit, so anything else is refused before it reaches a
/// privileged script.
#[cfg(unix)]
fn check_shape(record: &Record) -> Result<(), String> {
    let refused = || {
        Err(format!(
            "The app's record of the baum command ({}) is not what the app installs, so it was not used.",
            record.link.display()
        ))
    };
    if record.link.file_name() != Some(NAME.as_ref()) || !record.link.is_absolute() {
        return refused();
    }
    if !app_target_shaped(&record.target) {
        return refused();
    }
    if let Some(backup) = &record.backup {
        let name = backup.file_name().map(|n| n.to_string_lossy().into_owned());
        let base = format!("{NAME}{BACKUP_SUFFIX}");
        let shaped = name.as_deref().is_some_and(|n| {
            n == base
                || n.strip_prefix(&format!("{base}."))
                    .is_some_and(|d| !d.is_empty() && d.bytes().all(|b| b.is_ascii_digit()))
        });
        if !shaped || backup.parent() != record.link.parent() {
            return refused();
        }
    }
    Ok(())
}

/// Whether a path could be the baum the app runs: absolute, named `baum`,
/// without `..`.
#[cfg(unix)]
fn app_target_shaped(target: &Path) -> bool {
    target.is_absolute()
        && target.file_name() == Some(NAME.as_ref())
        && !target
            .components()
            .any(|c| matches!(c, std::path::Component::ParentDir))
}

#[cfg(unix)]
fn under(path: &Path, home: &Path) -> bool {
    let canonical_home = fs::canonicalize(home).unwrap_or_else(|_| home.to_path_buf());
    path.starts_with(home) || path.starts_with(&canonical_home)
}

/// Whether root may change `dir` for the user: outside the home directory,
/// existing or with an existing parent (the script creates at most the last
/// directory), reached without symbolic links, owned by root and writable by
/// no one else, below directories that are root's and not writable by others
/// either (a sticky one, such as `/tmp`, aside, since no one else can rename
/// root's entries in it). Anything less, and the user or a process of theirs
/// could move another directory into its place while the password prompt
/// shows.
#[cfg(unix)]
pub fn may_elevate(dir: &Path, home: &Path) -> bool {
    use std::os::unix::fs::MetadataExt;
    if !dir.is_absolute() || under(dir, home) {
        return false;
    }
    let start = if fs::symlink_metadata(dir).is_ok() {
        dir
    } else {
        match dir.parent() {
            Some(parent) if fs::symlink_metadata(parent).is_ok() => parent,
            _ => return false,
        }
    };
    if fs::canonicalize(start).ok().as_deref() != Some(start) {
        return false;
    }
    let root_only = |p: &Path, sticky_ok: bool| {
        fs::symlink_metadata(p).is_ok_and(|m| {
            m.is_dir()
                && m.uid() == 0
                && (m.mode() & 0o022 == 0 || (sticky_ok && m.mode() & 0o1000 != 0))
        })
    };
    root_only(start, false) && start.ancestors().skip(1).all(|a| root_only(a, true))
}

/// Whether a package manager or version manager owns a baum: Homebrew's
/// cellar or caskroom, the Nix store, MacPorts (`/opt/local`), snaps, the
/// system's own directories, or a version manager's directory (asdf and mise
/// shims, aqua, volta). Replacing it would break that manager's bookkeeping.
#[cfg(unix)]
pub fn package_managed(found: &Found) -> bool {
    let system = ["/usr/bin", "/bin", "/usr/sbin", "/sbin"];
    let dir = found.path.parent().unwrap_or(Path::new(""));
    let text = |p: &Path| p.to_string_lossy().into_owned();
    let (path, resolved) = (text(&found.path), text(&found.resolved));
    system.iter().any(|s| dir == Path::new(s))
        || [&path, &resolved].iter().any(|p| {
            p.contains("/Cellar/")
                || p.contains("/Caskroom/")
                || p.starts_with("/nix/store/")
                || p.contains("/.nix-profile/")
                || p.starts_with("/opt/local/")
                || p.starts_with("/snap/")
                || p.contains("/shims/")
                || p.contains("/aquaproj-aqua/")
                || p.contains("/.volta/")
        })
}

/// The checks `plan` makes of the filesystem, which tests replace.
#[cfg(unix)]
struct Rules<'a> {
    home: &'a Path,
    /// What the link will point at: never elevated for when it is in the
    /// home directory.
    target: &'a Path,
    writable: &'a dyn Fn(&Path) -> bool,
    may_elevate: &'a dyn Fn(&Path) -> bool,
}

/// Works out the command's state. `home` is the user's home directory.
#[cfg(unix)]
pub fn status(
    target: Result<PathBuf, String>,
    login: &LoginPath,
    records: Vec<Record>,
    home: &Path,
) -> Status {
    let elevate = |dir: &Path| may_elevate(dir, home);
    let wanted = target.clone().unwrap_or_default();
    let rules = Rules {
        home,
        target: &wanted,
        writable: &writable,
        may_elevate: &elevate,
    };
    status_with(target, login, records, &rules)
}

#[cfg(unix)]
fn status_with(
    target: Result<PathBuf, String>,
    login: &LoginPath,
    records: Vec<Record>,
    rules: &Rules,
) -> Status {
    let records: Vec<Record> = records
        .into_iter()
        .filter(|r| check_shape(r).is_ok() && holds(r))
        .collect();
    // The record of the baum the hooks run, else any.
    let record = login
        .baum
        .as_ref()
        .and_then(|found| records.iter().find(|r| r.link == found.path))
        .or(records.first())
        .cloned();
    let mut status = Status {
        target: None,
        unavailable: None,
        installed: false,
        shadowed: None,
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
    status.installed = status
        .found
        .as_ref()
        .is_some_and(|found| same_file(&found.resolved, &target));
    if !status.installed {
        status.plan = Some(plan(login, &records, rules));
    }
    status.target = Some(target);
    status
}

/// Where the link goes. A different baum first on the PATH is replaced where
/// it is, so the link takes its place in the PATH order, unless a package
/// manager owns it; otherwise the link goes in the first of `~/.local/bin` and
/// `~/bin` on the PATH, else in `/usr/local/bin` (with an administrator's
/// password unless it is writable), else in `~/.local/bin`, which the user
/// then has to add to the PATH. A baum in the home directory (an AppImage's
/// copy, an app in `~/Applications`) never gets a password-prompted link: the
/// user could rewrite it, and it would then run for every user and under
/// sudo. Such a link goes in `~/.local/bin` instead.
#[cfg(unix)]
fn plan(login: &LoginPath, records: &[Record], rules: &Rules) -> Plan {
    let candidate = first_plan(login, records, rules);
    if !candidate.admin || !under(rules.target, rules.home) {
        return candidate;
    }
    let local = rules.home.join(".local/bin");
    let mut plan = plan_in(&local, login, records, rules);
    if let Some(found) = &login.baum {
        let position = |dir: &Path| login.path.iter().position(|e| e == dir);
        let found_dir = found.path.parent().unwrap_or(Path::new("/"));
        let first = matches!((position(&local), position(found_dir)), (Some(l), Some(f)) if l < f);
        if !first {
            plan.blocked = Some(format!(
                "{} comes first on your PATH, and the app does not ask for an administrator's password to link to a baum in your home folder. Remove it, or put {} before its directory on your PATH, then check again.",
                found.path.display(),
                local.display()
            ));
        }
    }
    plan
}

#[cfg(unix)]
fn first_plan(login: &LoginPath, records: &[Record], rules: &Rules) -> Plan {
    if let Some(found) = &login.baum {
        let dir = found.path.parent().unwrap_or(Path::new("/"));
        let managed = package_managed(found);
        // The app's own earlier link is updated without asking.
        let ours = (!managed)
            .then(|| records.iter().find(|r| r.link == found.path))
            .flatten();
        let admin = !(rules.writable)(dir);
        let blocked = if managed {
            Some(format!(
                "{} belongs to a package manager. Remove it with that package manager (for example `brew uninstall baum`), then check again.",
                found.path.display()
            ))
        } else {
            refuse_elevation(dir, admin, rules)
        };
        return Plan {
            link: found.path.clone(),
            replaces: ours.is_none().then(|| found.clone()),
            backup: ours.is_none().then(|| backup_path(&found.path)),
            admin,
            on_path: true,
            blocked,
            kept_backup: ours.and_then(|r| r.backup.clone()),
        };
    }
    let personal = [rules.home.join(".local/bin"), rules.home.join("bin")];
    let system = PathBuf::from("/usr/local/bin");
    let on_path = |dir: &Path| login.path.iter().any(|entry| entry == dir);
    let dir = login
        .path
        .iter()
        .find(|entry| personal.contains(entry) && (rules.writable)(entry))
        .cloned()
        .or_else(|| on_path(&system).then(|| system.clone()))
        .unwrap_or_else(|| personal[0].clone());
    plan_in(&dir, login, records, rules)
}

/// A link in `dir`.
#[cfg(unix)]
fn plan_in(dir: &Path, login: &LoginPath, records: &[Record], rules: &Rules) -> Plan {
    let link = dir.join(NAME);
    let ours = records.iter().find(|r| r.link == link);
    // Something already there that is not on the PATH as a working baum: a
    // broken link (the app it pointed into was deleted) is replaced, anything
    // else only after confirmation.
    let occupied =
        fs::symlink_metadata(&link).is_ok() && fs::metadata(&link).is_ok() && ours.is_none();
    let admin = !(rules.writable)(dir);
    Plan {
        replaces: occupied.then(|| Found {
            path: link.clone(),
            resolved: fs::canonicalize(&link).unwrap_or_else(|_| link.clone()),
            version: String::new(),
        }),
        backup: occupied.then(|| backup_path(&link)),
        admin,
        on_path: login.path.iter().any(|entry| entry == dir),
        blocked: refuse_elevation(dir, admin, rules),
        kept_backup: ours.and_then(|r| r.backup.clone()),
        link,
    }
}

#[cfg(unix)]
fn refuse_elevation(dir: &Path, admin: bool, rules: &Rules) -> Option<String> {
    if !admin {
        return None;
    }
    if under(rules.target, rules.home) {
        return Some(format!(
            "You cannot write to {}, and the app does not ask for an administrator's password to link to a baum in your home folder.",
            dir.display()
        ));
    }
    (!(rules.may_elevate)(dir)).then(|| {
        format!(
            "You cannot write to {}, and the app asks for an administrator's password only for system directories such as /usr/local/bin. Make the directory writable for yourself, or move what is in the way, then check again.",
            dir.display()
        )
    })
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
    if let Some(why) = &plan.blocked {
        return Err(why.clone());
    }
    if let Some(replaced) = &plan.replaces
        && confirmed != Some(replaced.path.as_path())
    {
        return Err(format!(
            "Confirm replacing {} before installing.",
            replaced.path.display()
        ));
    }
    let dir = plan.link.parent().ok_or("The link has no directory.")?;
    // A baum this install moves aside now, or the one the app's own link
    // replaced before and still keeps.
    let moving = plan.replaces.as_ref().and(plan.backup.clone());
    let record = Record {
        link: plan.link.clone(),
        target: target.to_path_buf(),
        backup: moving.clone().or_else(|| plan.kept_backup.clone()),
        admin: plan.admin,
    };
    check_shape(&record)?;
    if plan.admin {
        let none = PathBuf::new();
        elevate.run(
            INSTALL_SCRIPT,
            &[
                dir,
                &record.link,
                moving.as_deref().unwrap_or(&none),
                target,
            ],
        )?;
    } else {
        link(dir, &record.link, moving.as_deref(), target).map_err(|e| {
            format!(
                "Cannot install the baum command at {}: {e}",
                record.link.display()
            )
        })?;
    }
    Ok(record)
}

/// The steps of `link`, as root. It checks the arguments' shape (exit 4):
/// absolute paths, the link `$1/baum`, a backup `$2.replaced-by-baum-app`
/// with at most a numeric suffix. It then changes into the parent and the
/// directory (creating only the last one), checks that the physical path is
/// the one named and that root owns it and no one else can write it, and
/// works by relative names from there, so no directory swapped in after the
/// app's check is followed. If placing the link fails, the replaced baum is
/// put back.
#[cfg(unix)]
const INSTALL_SCRIPT: &str = r#"set -e
case "$1" in /*) ;; *) exit 4;; esac
[ "$2" = "$1/baum" ] || exit 4
case "$4" in /*) ;; *) exit 4;; esac
if [ -n "$3" ]; then
  n=${3#"$2.replaced-by-baum-app"}
  [ "$n" != "$3" ] || exit 4
  case "$n" in "") ;; .|.*[!0-9]*) exit 4;; .*) ;; *) exit 4;; esac
fi
parent=${1%/*}
cd -P -- "$parent/" || exit 4
[ "$(pwd -P)" = "${parent:-/}" ] || exit 4
name=${1##*/}
[ -d "$name" ] || mkdir -m 755 -- "$name"
cd -P -- "$name" || exit 4
[ "$(pwd -P)" = "$1" ] || exit 4
[ -n "$(find . -prune -user 0 ! -perm -g+w ! -perm -o+w)" ] || exit 4
if [ -n "$3" ]; then mv -- baum "${3##*/}"; fi
ln -sfn -- "$4" baum || { if [ -n "$3" ]; then mv -- "${3##*/}" baum; fi; exit 1; }"#;

/// Removes the link only while it is a link to the app's baum (exit 3
/// otherwise), then puts a baum it replaced back; the same shape and
/// directory checks as the install script (exit 4).
#[cfg(unix)]
const UNINSTALL_SCRIPT: &str = r#"set -e
case "$1" in /*/baum) ;; *) exit 4;; esac
case "$2" in /*) ;; *) exit 4;; esac
if [ -n "$3" ]; then
  n=${3#"$1.replaced-by-baum-app"}
  [ "$n" != "$3" ] || exit 4
  case "$n" in "") ;; .|.*[!0-9]*) exit 4;; .*) ;; *) exit 4;; esac
fi
dir=${1%/baum}
cd -P -- "$dir" || exit 4
[ "$(pwd -P)" = "$dir" ] || exit 4
[ -n "$(find . -prune -user 0 ! -perm -g+w ! -perm -o+w)" ] || exit 4
[ -L baum ] || exit 3
[ "$(readlink baum)" = "$2" ] || exit 3
rm -f -- baum
if [ -n "$3" ] && { [ -e "${3##*/}" ] || [ -L "${3##*/}" ]; }; then mv -- "${3##*/}" baum; fi"#;

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
/// someone changed since is left alone. A link in a directory the user cannot
/// write is removed through the password prompt only when the record is
/// shaped as the app makes them, names `current` (the app's stable target
/// now) and the directory is a system one. Returns what happened, for the
/// user.
#[cfg(unix)]
pub fn uninstall(
    record: &Record,
    current: Option<&Path>,
    home: &Path,
    elevate: &dyn Elevate,
) -> Result<String, String> {
    let elevate_here = |dir: &Path| may_elevate(dir, home);
    uninstall_with(record, current, elevate, &writable, &elevate_here)
}

#[cfg(unix)]
fn uninstall_with(
    record: &Record,
    current: Option<&Path>,
    elevate: &dyn Elevate,
    writable: &dyn Fn(&Path) -> bool,
    may_elevate: &dyn Fn(&Path) -> bool,
) -> Result<String, String> {
    check_shape(record)?;
    let restore = record
        .backup
        .as_deref()
        .filter(|b| fs::symlink_metadata(b).is_ok());
    if !holds(record) {
        let mut message = format!(
            "{} no longer points at the app's baum, so it was left alone.",
            record.link.display()
        );
        if let Some(backup) = restore {
            message.push_str(&format!(
                " The baum it had replaced is still at {}.",
                backup.display()
            ));
        }
        return Ok(message);
    }
    let dir = record.link.parent().unwrap_or(Path::new("/"));
    if writable(dir) {
        fs::remove_file(&record.link)
            .and_then(|()| restore.map_or(Ok(()), |b| fs::rename(b, &record.link)))
            .map_err(|e| format!("Cannot remove {}: {e}", record.link.display()))?;
    } else {
        if current != Some(record.target.as_path()) || !may_elevate(dir) {
            return Err(format!(
                "The app does not ask for an administrator's password to remove {}: it is not in a system directory or no longer points at this app. Remove it yourself if you no longer want it.",
                record.link.display()
            ));
        }
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
        // "execution error: … (N)": N is the script's exit status, or -128
        // when the user cancelled the prompt.
        let code = stderr
            .trim()
            .strip_suffix(')')
            .and_then(|s| s.rsplit('(').next())
            .and_then(|n| n.parse::<i32>().ok());
        if code == Some(-128) {
            return Err("Cancelled.".into());
        }
        Err(script_failure(code, &stderr))
    }
}

#[cfg(all(unix, not(target_os = "macos")))]
impl Elevate for SystemPrompt {
    fn run(&self, script: &str, args: &[&Path]) -> Result<(), String> {
        let output = std::process::Command::new("/usr/bin/pkexec")
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
            // pkexec: the user dismissed the prompt or was not authorized.
            Some(126) => Err("Cancelled.".into()),
            // pkexec: no authentication agent could ask.
            Some(127) => Err(
                "No authorization agent is running to ask for your password, so nothing was changed."
                    .into(),
            ),
            code => Err(script_failure(code, &String::from_utf8_lossy(&output.stderr))),
        }
    }
}

#[cfg(windows)]
impl Elevate for SystemPrompt {
    fn run(&self, _: &str, _: &[&Path]) -> Result<(), String> {
        Err("Not used on Windows.".into())
    }
}

/// Windows has no links on the PATH to place: the app puts its `baum.exe`
/// alone in a directory of its data directory (`target`, refreshed at every
/// launch), adds that directory to the front of the user's PATH (new
/// terminals pick it up) and removes it again on uninstall.
#[cfg(windows)]
pub fn status(
    target: Result<PathBuf, String>,
    login: &LoginPath,
    records: Vec<Record>,
    _home: &Path,
) -> Status {
    let mut status = Status {
        target: None,
        unavailable: None,
        installed: false,
        shadowed: None,
        plan: None,
        path_error: login.error.clone(),
        found: login.baum.clone(),
        record: None,
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
    let runs_target = |found: &Found| same_file(&found.resolved, &target);
    status.installed = listed || status.found.as_ref().is_some_and(runs_target);
    if status.installed {
        status.shadowed = status.found.clone().filter(|f| !runs_target(f));
    }
    status.record = records.into_iter().find(|r| listed && r.link == dir);
    if !status.installed {
        status.plan = Some(Plan {
            link: dir,
            replaces: None,
            backup: None,
            admin: false,
            on_path: true,
            blocked: None,
            kept_backup: None,
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
pub fn uninstall(
    record: &Record,
    _current: Option<&Path>,
    _home: &Path,
    _elevate: &dyn Elevate,
) -> Result<String, String> {
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

    /// Reads and writes the user's PATH in the registry, keeping unexpanded
    /// variables, and announces the change so new terminals see it. Entries
    /// are compared with their variables expanded, so `%LOCALAPPDATA%\…`
    /// counts as the same directory. The directory travels in an environment
    /// variable, never in the script.
    const SCRIPT: &str = r#"$ErrorActionPreference = 'Stop'
$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
$path = [string]$key.GetValue('Path', '', 'DoNotExpandEnvironmentNames')
$expand = { param($p) [Environment]::ExpandEnvironmentVariables($p).TrimEnd('\') }
if ($env:BAUM_PATH_ACTION -eq 'read') {
  Write-Output ((@($path -split ';' | Where-Object { $_ }) | ForEach-Object { & $expand $_ }) -join ';')
  exit 0
}
$dir = $env:BAUM_PATH_DIR.TrimEnd('\')
$rest = @($path -split ';' | Where-Object { $_ -and ((& $expand $_) -ne $dir) })
if ($env:BAUM_PATH_ACTION -eq 'add') { $rest = @($dir) + $rest }
$new = $rest -join ';'
if ($new.Length -gt 32000) { throw 'Your PATH is too long to add another directory; remove entries you no longer need.' }
$key.SetValue('Path', $new, 'ExpandString')
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

    /// The user's PATH entries, variables expanded.
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
            let output = std::process::Command::new("/bin/sh")
                .arg("-c")
                .arg(script)
                .arg("sh")
                .args(args)
                .output()
                .map_err(|e| e.to_string())?;
            if output.status.success() {
                return Ok(());
            }
            Err(script_failure(
                output.status.code(),
                &String::from_utf8_lossy(&output.stderr),
            ))
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
            self.status_for(&self.app, path, record)
        }

        fn status_for(&self, target: &Path, path: &[PathBuf], record: Option<Record>) -> Status {
            status(
                Ok(target.to_path_buf()),
                &self.login(path),
                record.into_iter().collect(),
                &self.home,
            )
        }

        /// As `status`, with every directory outside the home one root may
        /// change, so the administrator scripts can be exercised.
        fn status_elevating(&self, path: &[PathBuf]) -> Status {
            let home = self.home.clone();
            let elevate = move |dir: &Path| !dir.starts_with(&home);
            let rules = Rules {
                home: &self.home,
                target: &self.app,
                writable: &writable,
                may_elevate: &elevate,
            };
            status_with(Ok(self.app.clone()), &self.login(path), Vec::new(), &rules)
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

    fn running_as_root() -> bool {
        // SAFETY: getuid has no preconditions and cannot fail.
        unsafe { libc::getuid() == 0 }
    }

    #[test]
    fn installs_into_local_bin_when_no_baum_is_on_the_path() {
        let w = World::new("fresh");
        let path = [w.local_bin(), PathBuf::from("/usr/bin")];
        let before = w.status(&path, None);
        assert!(!before.installed && before.found.is_none());
        let plan = before.plan.unwrap();
        assert_eq!(plan.link, w.local_bin().join("baum"));
        assert!(plan.replaces.is_none() && !plan.admin && plan.on_path && plan.blocked.is_none());

        let record = install(&plan, &w.app, None, &NoPrompt).unwrap();
        assert_eq!(fs::read_link(&record.link).unwrap(), w.app);
        let after = w.status(&path, Some(record.clone()));
        assert!(after.installed && after.plan.is_none() && after.shadowed.is_none());
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
    fn root_may_change_only_system_directories() {
        let w = World::new("elevate");
        assert!(!may_elevate(&w.local_bin(), &w.home));
        assert!(!may_elevate(&w.home.join("anything/bin"), &w.home));
        // Owned by this user, not root (unless the tests run as root).
        let mine = w.root.join("opt/bin");
        fs::create_dir_all(&mine).unwrap();
        assert_eq!(may_elevate(&mine, &w.home), running_as_root());
        // Reached through a symbolic link.
        let linked = w.root.join("linked");
        symlink("/usr", &linked).unwrap();
        assert!(!may_elevate(&linked.join("bin"), &w.home));
        assert!(!may_elevate(Path::new("relative/bin"), &w.home));
        // /usr/bin and its parents are root's, canonical and writable by no
        // one else on every Unix the app supports.
        assert!(may_elevate(Path::new("/usr/bin"), &w.home));
        // A directory to create needs an existing parent.
        assert!(!may_elevate(Path::new("/usr/no-such-dir/bin"), &w.home));
        // Below a directory this user owns (unless root runs the tests).
        assert_eq!(may_elevate(&mine.join("bin"), &w.home), running_as_root());
    }

    #[test]
    fn a_home_directory_that_would_need_a_password_is_refused() {
        if running_as_root() {
            return;
        }
        let w = World::new("home-admin");
        let locked = w.home.join("tools/bin");
        executable(&locked.join("baum"), "theirs");
        fs::set_permissions(&locked, fs::Permissions::from_mode(0o555)).unwrap();
        let plan = w.status(std::slice::from_ref(&locked), None).plan.unwrap();
        fs::set_permissions(&locked, fs::Permissions::from_mode(0o755)).unwrap();
        assert!(plan.admin);
        let why = plan.blocked.clone().unwrap();
        assert!(why.contains("system directories"), "{why}");
        let refused = install(&plan, &w.app, Some(&locked.join("baum")), &NoPrompt).unwrap_err();
        assert_eq!(refused, why);
    }

    #[test]
    fn the_administrator_script_refuses_a_directory_root_does_not_own() {
        if running_as_root() {
            return;
        }
        let w = World::new("admin");
        let locked = w.root.join("locked");
        let other = locked.join("baum");
        executable(&other, "other");
        fs::set_permissions(&locked, fs::Permissions::from_mode(0o555)).unwrap();
        let plan = w
            .status_elevating(std::slice::from_ref(&locked))
            .plan
            .unwrap();
        fs::set_permissions(&locked, fs::Permissions::from_mode(0o755)).unwrap();
        assert!(plan.admin && plan.blocked.is_none(), "{plan:?}");
        // Had the app's check passed for a directory that is not root's (it
        // was swapped meanwhile), the script still refuses it. The scripts'
        // work as root is exercised by a harness run as root in a container.
        let refused = install(&plan, &w.app, Some(&other), &Shell).unwrap_err();
        assert_eq!(refused, script_failure(Some(4), ""));
        assert_eq!(
            fs::read_to_string(&other).unwrap(),
            "#!/bin/sh\necho other\n"
        );
        let record = Record {
            link: other.clone(),
            target: w.app.clone(),
            backup: None,
            admin: true,
        };
        fs::remove_file(&other).unwrap();
        symlink(&w.app, &other).unwrap();
        let refused =
            uninstall_with(&record, Some(&w.app), &Shell, &|_| false, &|_| true).unwrap_err();
        assert_eq!(refused, script_failure(Some(4), ""));
        assert!(fs::symlink_metadata(&other).is_ok());
    }

    /// The scripts root runs check the paths themselves, whatever the app
    /// passes them.
    #[test]
    fn the_administrator_scripts_refuse_other_paths() {
        let w = World::new("scripts");
        let dir = w.root.join("bin");
        fs::create_dir_all(&dir).unwrap();
        let victim = w.root.join("victim");
        fs::write(&victim, "keep").unwrap();
        let none = PathBuf::new();
        let refused = script_failure(Some(4), "");
        let run = |script: &str, args: &[&Path]| Shell.run(script, args).unwrap_err();
        let link = dir.join("baum");
        assert_eq!(
            run(INSTALL_SCRIPT, &[&dir, &dir.join("sh"), &none, &w.app]),
            refused
        );
        assert_eq!(
            run(INSTALL_SCRIPT, &[&w.root, &link, &none, &w.app]),
            refused
        );
        assert_eq!(run(UNINSTALL_SCRIPT, &[&victim, &w.app, &none]), refused);
        for backup in [
            victim.clone(),
            PathBuf::from(format!(
                "{}.replaced-by-baum-app.1/../../victim",
                link.display()
            )),
            PathBuf::from(format!("{}.replaced-by-baum-app.1\nevil", link.display())),
            PathBuf::from(format!("{}.replaced-by-baum-app.", link.display())),
            PathBuf::from(format!("{}.replaced-by-baum-app.1x", link.display())),
        ] {
            assert_eq!(
                run(INSTALL_SCRIPT, &[&dir, &link, &backup, &w.app]),
                refused,
                "{backup:?}"
            );
            assert_eq!(
                run(UNINSTALL_SCRIPT, &[&link, &w.app, &backup]),
                refused,
                "{backup:?}"
            );
        }
        // An option as the target, and relative paths.
        assert_eq!(
            run(INSTALL_SCRIPT, &[&dir, &link, &none, Path::new("-t/etc")]),
            refused
        );
        let relative = Path::new("rel");
        assert_eq!(
            run(
                INSTALL_SCRIPT,
                &[relative, &relative.join("baum"), &none, &w.app]
            ),
            refused
        );
        assert_eq!(
            run(UNINSTALL_SCRIPT, &[&relative.join("baum"), &w.app, &none]),
            refused
        );
        assert_eq!(
            run(UNINSTALL_SCRIPT, &[&link, Path::new("rel/baum"), &none]),
            refused
        );
        // An empty target never removes a regular file.
        fs::write(&link, "regular").unwrap();
        assert_eq!(run(UNINSTALL_SCRIPT, &[&link, &none, &none]), refused);
        assert_eq!(fs::read_to_string(&link).unwrap(), "regular");
        assert_eq!(fs::read_to_string(&victim).unwrap(), "keep");
    }

    /// A record edited to name other paths is never acted on.
    #[test]
    fn a_tampered_record_is_refused() {
        let w = World::new("tampered");
        let path = [w.local_bin()];
        let record = install(
            &w.status(&path, None).plan.unwrap(),
            &w.app,
            None,
            &NoPrompt,
        )
        .unwrap();
        let victim = w.root.join("victim");
        fs::write(&victim, "keep").unwrap();
        for tampered in [
            Record {
                link: victim.clone(),
                ..record.clone()
            },
            Record {
                backup: Some(victim.clone()),
                ..record.clone()
            },
            Record {
                backup: Some(w.local_bin().join("baum.replaced-by-baum-app.x")),
                ..record.clone()
            },
            Record {
                target: "baum".into(),
                ..record.clone()
            },
            Record {
                target: w.root.join("Applications/../victim/baum"),
                ..record.clone()
            },
            Record {
                target: w.root.join("Applications/sh"),
                ..record.clone()
            },
            Record {
                target: w.root.join("binaries/baum-x86_64-unknown-linux-gnu"),
                ..record.clone()
            },
        ] {
            let err = uninstall_with(&tampered, Some(&w.app), &NoPrompt, &writable, &|_| true)
                .unwrap_err();
            assert!(err.contains("not what the app installs"), "{err}");
            assert!(w.status(&path, Some(tampered)).record.is_none());
        }
        // A link the user cannot write, for another target: no prompt.
        let elsewhere = Record {
            target: w.root.join("other-app/baum"),
            ..record.clone()
        };
        symlink(&elsewhere.target, w.root.join("dangling")).unwrap();
        fs::remove_file(&record.link).unwrap();
        symlink(&elsewhere.target, &record.link).unwrap();
        let err =
            uninstall_with(&elsewhere, Some(&w.app), &NoPrompt, &|_| false, &|_| true).unwrap_err();
        assert!(err.contains("Remove it yourself"), "{err}");
        assert_eq!(fs::read_to_string(&victim).unwrap(), "keep");
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
        assert!(plan.blocked.is_none());
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

        uninstall(&record, Some(&w.app), &w.home, &NoPrompt).unwrap();
        assert_eq!(fs::read_to_string(&theirs).unwrap(), "#!/bin/sh\necho go\n");
        assert!(fs::symlink_metadata(go_bin.join("baum.replaced-by-baum-app")).is_err());
    }

    /// A baum a package manager owns is not replaced in place: the screen
    /// says to remove it with that manager.
    #[test]
    fn leaves_package_managed_baums_to_their_manager() {
        let found = |path: &str, resolved: &str| Found {
            path: path.into(),
            resolved: resolved.into(),
            version: String::new(),
        };
        for managed in [
            found(
                "/opt/homebrew/bin/baum",
                "/opt/homebrew/Cellar/baum/0.3.0/bin/baum",
            ),
            found(
                "/usr/local/bin/baum",
                "/usr/local/Cellar/baum/0.3.0/bin/baum",
            ),
            found(
                "/run/current-system/sw/bin/baum",
                "/nix/store/abc-baum/bin/baum",
            ),
            found("/usr/bin/baum", "/usr/bin/baum"),
            found("/sbin/baum", "/sbin/baum"),
            found("/opt/u/.asdf/shims/baum", "/opt/u/.asdf/shims/baum"),
            found(
                "/opt/u/.local/share/mise/shims/baum",
                "/opt/u/.local/share/mise/shims/baum",
            ),
            found("/opt/local/bin/baum", "/opt/local/bin/baum"),
            found("/snap/bin/baum", "/snap/baum/12/bin/baum"),
            found(
                "/opt/homebrew/bin/baum",
                "/opt/homebrew/Caskroom/baum/0.3.0/baum",
            ),
            found(
                "/opt/u/.local/share/aquaproj-aqua/bin/baum",
                "/opt/u/.local/share/aquaproj-aqua/pkgs/baum",
            ),
            found("/opt/u/.volta/bin/baum", "/opt/u/.volta/tools/image/baum"),
        ] {
            assert!(package_managed(&managed), "{managed:?}");
        }
        for own in [
            found("/opt/u/go/bin/baum", "/opt/u/go/bin/baum"),
            found("/usr/local/bin/baum", "/usr/local/bin/baum"),
            found("/opt/u/.local/bin/baum", "/opt/baum/baum"),
        ] {
            assert!(!package_managed(&own), "{own:?}");
        }

        let w = World::new("brew");
        let cellar = w.root.join("homebrew/Cellar/baum/0.3.0/bin/baum");
        executable(&cellar, "brew");
        let bin = w.root.join("homebrew/bin");
        fs::create_dir_all(&bin).unwrap();
        symlink(&cellar, bin.join("baum")).unwrap();
        let plan = w.status(std::slice::from_ref(&bin), None).plan.unwrap();
        let why = plan.blocked.unwrap();
        assert!(why.contains("brew uninstall baum"), "{why}");
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
            let status = w.status_for(&moved, &path, record);
            assert!(!status.installed && status.found.is_none());
            let plan = status.plan.unwrap();
            assert_eq!(plan.link, link);
            assert!(plan.replaces.is_none(), "{plan:?}");
            install(&plan, &moved, None, &NoPrompt).unwrap();
            assert_eq!(fs::read_link(&link).unwrap(), moved);
        }
    }

    /// After the app replaced a go-installed baum and then moved, updating
    /// its link keeps the backup, so removing the command still puts the
    /// original back.
    #[test]
    fn updating_the_apps_link_keeps_the_backup_of_what_it_replaced() {
        let w = World::new("keep-backup");
        let go_bin = w.home.join("go/bin");
        let theirs = go_bin.join("baum");
        executable(&theirs, "go");
        let path = [go_bin.clone()];
        let plan = w.status(&path, None).plan.unwrap();
        let first = install(&plan, &w.app, Some(&theirs), &NoPrompt).unwrap();
        let backup = first.backup.clone().unwrap();

        let moved = w.root.join("Moved/baum.app/Contents/MacOS/baum");
        executable(&moved, "moved");
        let status = w.status_for(&moved, &path, Some(first));
        let plan = status.plan.unwrap();
        assert!(plan.replaces.is_none() && plan.backup.is_none(), "{plan:?}");
        assert_eq!(plan.kept_backup.as_ref(), Some(&backup));
        let updated = install(&plan, &moved, None, &NoPrompt).unwrap();
        assert_eq!(updated.backup.as_ref(), Some(&backup));
        assert_eq!(fs::read_link(&theirs).unwrap(), moved);
        assert_eq!(fs::read_to_string(&backup).unwrap(), "#!/bin/sh\necho go\n");

        uninstall(&updated, Some(&moved), &w.home, &NoPrompt).unwrap();
        assert_eq!(fs::read_to_string(&theirs).unwrap(), "#!/bin/sh\necho go\n");
        assert!(fs::symlink_metadata(&backup).is_err());
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
        let status = w.status_for(&newer, &path, Some(record));
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
        let message = uninstall(&record, Some(&w.app), &w.home, &NoPrompt).unwrap();
        assert!(message.starts_with("Removed the baum command"), "{message}");
        assert!(fs::symlink_metadata(&record.link).is_err());
        assert!(w.app.exists(), "the bundled binary was touched");

        // Someone replaced the link with their own baum: it stays, and a
        // backup the app made is named.
        executable(&record.link, "mine");
        let backup = w.local_bin().join("baum.replaced-by-baum-app");
        fs::write(&backup, "old").unwrap();
        let with_backup = Record {
            backup: Some(backup.clone()),
            ..record.clone()
        };
        let message = uninstall(&with_backup, Some(&w.app), &w.home, &NoPrompt).unwrap();
        assert!(message.contains("left alone"), "{message}");
        assert!(message.contains(&backup.display().to_string()), "{message}");
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
    fn the_copy_is_refreshed_only_when_the_app_or_the_copy_changed() {
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
        // A temporary file a crash left behind does not stop the next copy.
        let stale = copy.with_file_name(format!(".baum-{}", std::process::id()));
        fs::write(&stale, "stale").unwrap();
        // An updated app: a different size.
        executable(&w.app, "updated");
        assert!(refresh_copy(&w.app, &copy).unwrap());
        assert_eq!(fs::read(&copy).unwrap(), fs::read(&w.app).unwrap());
        assert!(fs::symlink_metadata(&stale).is_err());
        // An update of the same size landing at once, within the same tick
        // of the file clock, and with its old times put back.
        let times = fs::metadata(&w.app).unwrap().modified().unwrap();
        let same_size = String::from_utf8(fs::read(&w.app).unwrap())
            .unwrap()
            .replace("updated", "patched");
        fs::write(&w.app, same_size).unwrap();
        fs::File::options()
            .write(true)
            .open(&w.app)
            .unwrap()
            .set_modified(times)
            .unwrap();
        assert!(refresh_copy(&w.app, &copy).unwrap());
        assert_eq!(fs::read(&copy).unwrap(), fs::read(&w.app).unwrap());
        assert!(!refresh_copy(&w.app, &copy).unwrap());
        // The same size and a new modification time.
        let app = fs::File::options().write(true).open(&w.app).unwrap();
        app.set_modified(std::time::SystemTime::UNIX_EPOCH).unwrap();
        assert!(refresh_copy(&w.app, &copy).unwrap());
        assert!(!refresh_copy(&w.app, &copy).unwrap());
        // A copy someone rewrote heals, whatever its size, and so does a
        // missing stamp.
        let rewritten = String::from_utf8(fs::read(&w.app).unwrap())
            .unwrap()
            .replace("patched", "changed");
        fs::write(&copy, rewritten).unwrap();
        assert!(refresh_copy(&w.app, &copy).unwrap());
        assert_eq!(fs::read(&copy).unwrap(), fs::read(&w.app).unwrap());
        fs::remove_file(stamp_path(&copy)).unwrap();
        assert!(refresh_copy(&w.app, &copy).unwrap());
        assert!(!refresh_copy(&w.app, &copy).unwrap());
        // A running daemon keeps the file it started from.
        let running = fs::File::open(&copy).unwrap();
        executable(&w.app, "newer again");
        assert!(refresh_copy(&w.app, &copy).unwrap());
        drop(running);
        assert_eq!(stable_target(&copy).unwrap(), copy);
    }

    /// A baum in the home directory never gets a link that needs a password:
    /// the link goes in ~/.local/bin, and when a baum in a system directory
    /// would still come first, the screen says so.
    #[test]
    fn a_baum_in_the_home_directory_is_never_linked_as_root() {
        let w = World::new("home-target");
        let copy = w.home.join(".local/share/app/bin/baum");
        executable(&copy, "copy");
        let writable = |dir: &Path| !dir.starts_with("/usr");
        let elevate = |_: &Path| true;
        let rules = Rules {
            home: &w.home,
            target: &copy,
            writable: &writable,
            may_elevate: &elevate,
        };
        let status = |path: &[PathBuf], baum: Option<Found>| {
            let login = LoginPath {
                path: path.to_vec(),
                baum,
                error: None,
            };
            status_with(Ok(copy.clone()), &login, Vec::new(), &rules)
        };
        // Only /usr/local/bin on the PATH: ~/.local/bin, not on the PATH.
        let plan = status(&[PathBuf::from("/usr/local/bin")], None)
            .plan
            .unwrap();
        assert_eq!(plan.link, w.local_bin().join("baum"));
        assert!(
            !plan.admin && !plan.on_path && plan.blocked.is_none(),
            "{plan:?}"
        );
        // A baum in a system directory first: ~/.local/bin works only before it.
        let theirs = Found {
            path: "/usr/local/bin/baum".into(),
            resolved: "/usr/local/bin/baum".into(),
            version: String::new(),
        };
        let before = [w.local_bin(), PathBuf::from("/usr/local/bin")];
        let plan = status(&before, Some(theirs.clone())).plan.unwrap();
        assert_eq!(plan.link, w.local_bin().join("baum"));
        assert!(plan.blocked.is_none() && !plan.admin, "{plan:?}");
        let after = [PathBuf::from("/usr/local/bin"), w.local_bin()];
        let plan = status(&after, Some(theirs)).plan.unwrap();
        let why = plan.blocked.unwrap();
        assert!(why.contains("home folder"), "{why}");
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

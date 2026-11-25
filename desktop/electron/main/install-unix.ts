// The baum command on macOS and Linux: a symbolic link on the login shell's
// PATH.
//
// Root acts only on system directories. The app asks for an administrator's
// password only for a link directory outside the home directory that root
// owns and only root can write, reached without symbolic links through
// directories that are also root's and not writable by others (sticky ones
// aside), and only for a link to a baum outside the home directory. The
// script root then runs works by relative names inside that directory after
// changing into it and checking again that it is the directory named, that
// root owns it and that no one else can write it, so swapping a directory
// between the app's check and the prompt cannot redirect it; and it refuses
// paths that are not shaped like what the app installs, so a tampered setup
// record in `desktop.json` cannot point root at anything else.
import {
  accessSync,
  constants,
  lstatSync,
  mkdirSync,
  readlinkSync,
  realpathSync,
  renameSync,
  rmSync,
  statSync,
  symlinkSync,
  unlinkSync,
} from "node:fs";
import { message } from "./decode.ts";
import type { Elevate } from "./elevation.ts";
import type { Found, InstallRecord, LoginPath, Plan, Status, Target } from "./install.ts";
import * as paths from "./path-rules.ts";
import { sameFile } from "./path-rules.ts";

const P = "linux" as const;

/** The name of the link and of the bundled binary. */
export const NAME = "baum";

/** Where a replaced baum is moved, beside the link it gave way to. */
export const BACKUP_SUFFIX = ".replaced-by-baum-app";

const same = (a: string, b: string) => paths.samePath(a, b, P);
const parentOf = (p: string) => paths.parent(p, P);
const join = (a: string, b: string) => paths.join(a, b, P);

function lexists(path: string): boolean {
  try {
    lstatSync(path);
    return true;
  } catch {
    return false;
  }
}

function exists(path: string): boolean {
  try {
    statSync(path);
    return true;
  } catch {
    return false;
  }
}

function canonical(path: string): string | null {
  try {
    return realpathSync.native(path);
  } catch {
    return null;
  }
}

/** Whether the record still describes the filesystem: its link is a
 *  symbolic link to its target, whether or not the target still exists. */
function holds(record: InstallRecord): boolean {
  try {
    return same(readlinkSync(record.link), record.target);
  } catch {
    return false;
  }
}

/** Whether a record has the shape of what the app installs: a link named
 *  `baum`, and a backup beside it with the backup suffix. The record lives in
 *  a file the user can edit, so anything else is refused before it reaches a
 *  privileged script. */
export function checkShape(record: InstallRecord): void {
  const refused = () =>
    new Error(
      `The app's record of the baum command (${record.link}) is not what the app installs, so it was not used.`,
    );
  if (paths.fileName(record.link, P) !== NAME || !paths.isAbsolute(record.link, P)) throw refused();
  if (!appTargetShaped(record.target)) throw refused();
  if (record.backup !== null) {
    const name = paths.fileName(record.backup, P);
    const base = `${NAME}${BACKUP_SUFFIX}`;
    const shaped =
      name !== null &&
      (name === base ||
        (name.startsWith(`${base}.`) && /^[0-9]+$/.test(name.slice(base.length + 1))));
    const backupParent = parentOf(record.backup);
    const linkParent = parentOf(record.link);
    const sameParent =
      backupParent === null || linkParent === null
        ? backupParent === linkParent
        : same(backupParent, linkParent);
    if (!shaped || !sameParent) throw refused();
  }
}

/** Whether a path could be the baum the app runs: absolute, named `baum`,
 *  without `..`. */
function appTargetShaped(target: string): boolean {
  return (
    paths.isAbsolute(target, P) &&
    paths.fileName(target, P) === NAME &&
    !paths.hasParentComponent(target, P)
  );
}

function under(path: string, home: string): boolean {
  const canonicalHome = canonical(home) ?? home;
  return paths.startsWith(path, home, P) || paths.startsWith(path, canonicalHome, P);
}

/** Whether root may change `dir` for the user: outside the home directory,
 *  existing or with an existing parent (the script creates at most the last
 *  directory), reached without symbolic links, owned by root and writable by
 *  no one else, below directories that are root's and not writable by others
 *  either (a sticky one, such as `/tmp`, aside, since no one else can rename
 *  root's entries in it). Anything less, and the user or a process of theirs
 *  could move another directory into its place while the password prompt
 *  shows. */
export function mayElevate(dir: string, home: string): boolean {
  if (!paths.isAbsolute(dir, P) || under(dir, home)) return false;
  let start: string;
  if (lexists(dir)) {
    start = dir;
  } else {
    const up = parentOf(dir);
    if (up === null || !lexists(up)) return false;
    start = up;
  }
  const real = canonical(start);
  if (real === null || !same(real, start)) return false;
  const rootOnly = (p: string, stickyOk: boolean) => {
    try {
      const m = lstatSync(p);
      return (
        m.isDirectory() &&
        m.uid === 0 &&
        ((m.mode & 0o022) === 0 || (stickyOk && (m.mode & 0o1000) !== 0))
      );
    } catch {
      return false;
    }
  };
  return (
    rootOnly(start, false) &&
    paths
      .ancestors(start, P)
      .slice(1)
      .every((a) => rootOnly(a, true))
  );
}

/** Whether a package manager or version manager owns a baum: Homebrew's
 *  cellar or caskroom, the Nix store, MacPorts (`/opt/local`), snaps, the
 *  system's own directories, or a version manager's directory (asdf and mise
 *  shims, aqua, volta). Replacing it would break that manager's bookkeeping. */
export function packageManaged(found: Found): boolean {
  const system = ["/usr/bin", "/bin", "/usr/sbin", "/sbin"];
  const dir = parentOf(found.path) ?? "";
  return (
    system.some((s) => same(dir, s)) ||
    [found.path, found.resolved].some(
      (p) =>
        p.includes("/Cellar/") ||
        p.includes("/Caskroom/") ||
        p.startsWith("/nix/store/") ||
        p.includes("/.nix-profile/") ||
        p.startsWith("/opt/local/") ||
        p.startsWith("/snap/") ||
        p.includes("/shims/") ||
        p.includes("/aquaproj-aqua/") ||
        p.includes("/.volta/"),
    )
  );
}

/** The checks `plan` makes of the filesystem, which tests replace. */
export interface Rules {
  home: string;
  /** What the link will point at: never elevated for when it is in the
   *  home directory. */
  target: string;
  writable: (dir: string) => boolean;
  mayElevate: (dir: string) => boolean;
}

/** Works out the command's state. `home` is the user's home directory. */
export function status(
  target: Target,
  login: LoginPath,
  records: InstallRecord[],
  home: string,
): Status {
  const rules: Rules = {
    home,
    target: "path" in target ? target.path : "",
    writable,
    mayElevate: (dir) => mayElevate(dir, home),
  };
  return statusWith(target, login, records, rules);
}

export function statusWith(
  target: Target,
  login: LoginPath,
  all: InstallRecord[],
  rules: Rules,
): Status {
  const records = all.filter((r) => {
    try {
      checkShape(r);
    } catch {
      return false;
    }
    return holds(r);
  });
  // The record of the baum the hooks run, else any.
  const found = login.baum;
  const record =
    (found !== null ? records.find((r) => same(r.link, found.path)) : undefined) ??
    records[0] ??
    null;
  const status: Status = {
    target: null,
    unavailable: null,
    found: login.baum,
    installed: false,
    shadowed: null,
    record,
    plan: null,
    path_error: login.error ?? login.warning,
  };
  if ("error" in target) {
    status.unavailable = target.error;
    return status;
  }
  status.installed = found !== null && sameFile(found.resolved, target.path, P);
  if (!status.installed) status.plan = plan(login, records, rules);
  status.target = target.path;
  return status;
}

/** Where the link goes. A different baum first on the PATH is replaced where
 *  it is, so the link takes its place in the PATH order, unless a package
 *  manager owns it; otherwise the link goes in the first of `~/.local/bin` and
 *  `~/bin` on the PATH, else in `/usr/local/bin` (with an administrator's
 *  password unless it is writable), else in `~/.local/bin`, which the user
 *  then has to add to the PATH. A baum in the home directory (an AppImage's
 *  copy, an app in `~/Applications`) never gets a password-prompted link: the
 *  user could rewrite it, and it would then run for every user and under
 *  sudo. Such a link goes in `~/.local/bin` instead. */
function plan(login: LoginPath, records: InstallRecord[], rules: Rules): Plan {
  const candidate = firstPlan(login, records, rules);
  if (!candidate.admin || !under(rules.target, rules.home)) return candidate;
  const local = join(rules.home, ".local/bin");
  const result = planIn(local, login, records, rules);
  const found = login.baum;
  if (found !== null) {
    const position = (dir: string) => login.path.findIndex((e) => same(e, dir));
    const foundDir = parentOf(found.path) ?? "/";
    const l = position(local);
    const f = position(foundDir);
    const first = l !== -1 && f !== -1 && l < f;
    if (!first) {
      result.blocked = `${found.path} comes first on your PATH, and the app does not ask for an administrator's password to link to a baum in your home folder. Remove it, or put ${local} before its directory on your PATH, then check again.`;
    }
  }
  return result;
}

function firstPlan(login: LoginPath, records: InstallRecord[], rules: Rules): Plan {
  const found = login.baum;
  if (found !== null) {
    const dir = parentOf(found.path) ?? "/";
    const managed = packageManaged(found);
    // The app's own earlier link is updated without asking.
    const ours = managed ? undefined : records.find((r) => same(r.link, found.path));
    const admin = !rules.writable(dir);
    const blocked = managed
      ? `${found.path} belongs to a package manager. Remove it with that package manager (for example \`brew uninstall baum\`), then check again.`
      : refuseElevation(dir, admin, rules);
    return {
      link: found.path,
      replaces: ours === undefined ? found : null,
      backup: ours === undefined ? backupPath(found.path) : null,
      admin,
      on_path: true,
      blocked,
      kept_backup: ours?.backup ?? null,
    };
  }
  const personal = [join(rules.home, ".local/bin"), join(rules.home, "bin")];
  const system = "/usr/local/bin";
  const onPath = (dir: string) => login.path.some((entry) => same(entry, dir));
  const dir =
    login.path.find((entry) => personal.some((p) => same(p, entry)) && rules.writable(entry)) ??
    (onPath(system) ? system : undefined) ??
    (personal[0] as string);
  return planIn(dir, login, records, rules);
}

/** A link in `dir`. */
function planIn(dir: string, login: LoginPath, records: InstallRecord[], rules: Rules): Plan {
  const link = join(dir, NAME);
  const ours = records.find((r) => same(r.link, link));
  // Something already there that is not on the PATH as a working baum: a
  // broken link (the app it pointed into was deleted) is replaced, anything
  // else only after confirmation.
  const occupied = lexists(link) && exists(link) && ours === undefined;
  const admin = !rules.writable(dir);
  return {
    link,
    replaces: occupied ? { path: link, resolved: canonical(link) ?? link, version: "" } : null,
    backup: occupied ? backupPath(link) : null,
    admin,
    on_path: login.path.some((entry) => same(entry, dir)),
    blocked: refuseElevation(dir, admin, rules),
    kept_backup: ours?.backup ?? null,
  };
}

function refuseElevation(dir: string, admin: boolean, rules: Rules): string | null {
  if (!admin) return null;
  if (under(rules.target, rules.home)) {
    return `You cannot write to ${dir}, and the app does not ask for an administrator's password to link to a baum in your home folder.`;
  }
  return rules.mayElevate(dir)
    ? null
    : `You cannot write to ${dir}, and the app asks for an administrator's password only for system directories such as /usr/local/bin. Make the directory writable for yourself, or move what is in the way, then check again.`;
}

/** A free name beside `path` for the baum it replaces. */
function backupPath(path: string): string {
  const base = `${path}${BACKUP_SUFFIX}`;
  let candidate = base;
  for (let n = 1; lexists(candidate); n++) candidate = `${base}.${n}`;
  return candidate;
}

/** Whether this user can create files in `dir`, or create `dir` itself. */
export function writable(start: string): boolean {
  for (let dir: string | null = start; dir !== null; dir = parentOf(dir)) {
    if (exists(dir)) {
      if (dir.includes("\0")) return false;
      try {
        if (!statSync(dir).isDirectory()) return false;
        accessSync(dir, constants.W_OK);
        return true;
      } catch {
        return false;
      }
    }
  }
  return false;
}

/** Installs the plan's link to `target`. A plan that replaces a different
 *  baum needs `confirmed` to name it: the setup screen passes the path it
 *  showed the user and asked about. */
export async function install(
  plan: Plan,
  target: string,
  confirmed: string | null,
  elevate: Elevate,
): Promise<InstallRecord> {
  if (plan.blocked !== null) throw new Error(plan.blocked);
  const replaced = plan.replaces;
  if (replaced !== null && (confirmed === null || !same(confirmed, replaced.path))) {
    throw new Error(`Confirm replacing ${replaced.path} before installing.`);
  }
  const dir = parentOf(plan.link);
  if (dir === null) throw new Error("The link has no directory.");
  // A baum this install moves aside now, or the one the app's own link
  // replaced before and still keeps.
  const moving = plan.replaces !== null ? plan.backup : null;
  const record: InstallRecord = {
    link: plan.link,
    target,
    backup: moving ?? plan.kept_backup,
    admin: plan.admin,
  };
  checkShape(record);
  if (plan.admin) {
    await elevate.run(INSTALL_SCRIPT, [dir, record.link, moving ?? "", target]);
  } else {
    try {
      link(dir, record.link, moving, target);
    } catch (e) {
      throw new Error(`Cannot install the baum command at ${record.link}: ${message(e)}`);
    }
  }
  return record;
}

/** The steps of `link`, as root. It checks the arguments' shape (exit 4):
 *  absolute paths, the link `$1/baum`, a backup `$2.replaced-by-baum-app`
 *  with at most a numeric suffix. It then changes into the parent and the
 *  directory (creating only the last one), checks that the physical path is
 *  the one named and that root owns it and no one else can write it, and
 *  works by relative names from there, so no directory swapped in after the
 *  app's check is followed. If placing the link fails, the replaced baum is
 *  put back. */
export const INSTALL_SCRIPT = `set -e
case "$1" in /*) ;; *) exit 4;; esac
[ "$2" = "$1/baum" ] || exit 4
case "$4" in /*) ;; *) exit 4;; esac
if [ -n "$3" ]; then
  n=\${3#"$2.replaced-by-baum-app"}
  [ "$n" != "$3" ] || exit 4
  case "$n" in "") ;; .|.*[!0-9]*) exit 4;; .*) ;; *) exit 4;; esac
fi
parent=\${1%/*}
cd -P -- "$parent/" || exit 4
[ "$(pwd -P)" = "\${parent:-/}" ] || exit 4
name=\${1##*/}
[ -d "$name" ] || mkdir -m 755 -- "$name"
cd -P -- "$name" || exit 4
[ "$(pwd -P)" = "$1" ] || exit 4
[ -n "$(find . -prune -user 0 ! -perm -g+w ! -perm -o+w)" ] || exit 4
if [ -n "$3" ]; then mv -- baum "\${3##*/}"; fi
ln -sfn -- "$4" baum || { if [ -n "$3" ]; then mv -- "\${3##*/}" baum; fi; exit 1; }`;

/** Removes the link only while it is a link to the app's baum (exit 3
 *  otherwise), then puts a baum it replaced back; the same shape and
 *  directory checks as the install script (exit 4). */
export const UNINSTALL_SCRIPT = `set -e
case "$1" in /*/baum) ;; *) exit 4;; esac
case "$2" in /*) ;; *) exit 4;; esac
if [ -n "$3" ]; then
  n=\${3#"$1.replaced-by-baum-app"}
  [ "$n" != "$3" ] || exit 4
  case "$n" in "") ;; .|.*[!0-9]*) exit 4;; .*) ;; *) exit 4;; esac
fi
dir=\${1%/baum}
cd -P -- "$dir" || exit 4
[ "$(pwd -P)" = "$dir" ] || exit 4
[ -n "$(find . -prune -user 0 ! -perm -g+w ! -perm -o+w)" ] || exit 4
[ -L baum ] || exit 3
[ "$(readlink baum)" = "$2" ] || exit 3
rm -f -- baum
if [ -n "$3" ] && { [ -e "\${3##*/}" ] || [ -L "\${3##*/}" ]; }; then mv -- "\${3##*/}" baum; fi`;

function link(dir: string, linkPath: string, backup: string | null, target: string): void {
  mkdirSync(dir, { recursive: true, mode: 0o755 });
  if (backup !== null) renameSync(linkPath, backup);
  const temporary = join(dir, `.baum-link-${process.pid}`);
  rmSync(temporary, { force: true });
  try {
    symlinkSync(target, temporary);
    renameSync(temporary, linkPath);
  } catch (e) {
    rmSync(temporary, { force: true });
    if (backup !== null) {
      try {
        renameSync(backup, linkPath);
      } catch {}
    }
    throw e;
  }
}

/** Removes what the app installed and nothing else: the link, while it still
 *  points at the recorded target, and puts back a baum it replaced. A link
 *  someone changed since is left alone. A link in a directory the user cannot
 *  write is removed through the password prompt only when the record is
 *  shaped as the app makes them, names `current` (the app's stable target
 *  now) and the directory is a system one. Returns what happened, for the
 *  user. */
export function uninstall(
  record: InstallRecord,
  current: string | null,
  home: string,
  elevate: Elevate,
): Promise<string> {
  return uninstallWith(record, current, elevate, writable, (dir) => mayElevate(dir, home));
}

export async function uninstallWith(
  record: InstallRecord,
  current: string | null,
  elevate: Elevate,
  canWrite: (dir: string) => boolean,
  canElevate: (dir: string) => boolean,
): Promise<string> {
  checkShape(record);
  const restore = record.backup !== null && lexists(record.backup) ? record.backup : null;
  if (!holds(record)) {
    let text = `${record.link} no longer points at the app's baum, so it was left alone.`;
    if (restore !== null) text += ` The baum it had replaced is still at ${restore}.`;
    return text;
  }
  const dir = parentOf(record.link) ?? "/";
  if (canWrite(dir)) {
    try {
      unlinkSync(record.link);
      if (restore !== null) renameSync(restore, record.link);
    } catch (e) {
      throw new Error(`Cannot remove ${record.link}: ${message(e)}`);
    }
  } else {
    if (current === null || !same(current, record.target) || !canElevate(dir)) {
      throw new Error(
        `The app does not ask for an administrator's password to remove ${record.link}: it is not in a system directory or no longer points at this app. Remove it yourself if you no longer want it.`,
      );
    }
    await elevate.run(UNINSTALL_SCRIPT, [record.link, record.target, restore ?? ""]);
  }
  return restore !== null
    ? `Removed the baum command and put back the baum it had replaced at ${record.link}.`
    : `Removed the baum command at ${record.link}.`;
}

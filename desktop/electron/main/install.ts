// The `baum` command on the user's PATH. Coding-agent hooks run `baum` by
// name, so the app makes its bundled binary that command: on macOS and Linux
// a symbolic link in a directory on the login shell's PATH, pointing at a
// stable path of the bundled binary (so updating the app updates the
// command); on Windows a directory holding only the app's `baum.exe`, added
// to the user's PATH.
//
// What is on the PATH comes from the bundled `baum daemon login-path`, which
// reads the login shell the way the daemon does. This module only decides
// where the link goes and changes it, and only what the app recorded as its
// own is ever removed. A different `baum` already on the PATH is replaced
// only with the user's confirmation, and moved aside so that uninstalling
// puts it back; one a package manager owns is left to it.
//
// Root acts only on system directories (see install-unix.ts).
import {
  type BigIntStats,
  closeSync,
  fstatSync,
  fsyncSync,
  mkdirSync,
  openSync,
  readdirSync,
  readFileSync,
  readSync,
  renameSync,
  rmSync,
  statSync,
  writeFileSync,
  writeSync,
} from "node:fs";
import * as decode from "./decode.ts";
import type { Elevate } from "./elevation.ts";
import * as unix from "./install-unix.ts";
import { fileName, join, type Platform, parent } from "./path-rules.ts";

export { sameFile } from "./path-rules.ts";
import * as windowsInstall from "./install-windows.ts";

/** What `baum daemon login-path --json` prints. */
export interface LoginPath {
  path: string[];
  baum: Found | null;
  /** Why the login shell could not be read; the PATH is then the app's. */
  error: string | null;
  /** Why the PATH is a login shell's that is not interactive, which may
   *  miss settings the interactive one makes. */
  warning: string | null;
}

/** A `baum` on the PATH: the first one, which is what the hooks run. */
export interface Found {
  path: string;
  /** The file it runs, with symbolic links resolved. */
  resolved: string;
  version: string;
}

/** What the app installed, kept in desktop.json so that uninstalling removes
 *  exactly that and nothing else. */
export interface InstallRecord {
  /** The link the app created; on Windows, the directory it added to the
   *  user's PATH. */
  link: string;
  /** What the link points at: the stable path of the bundled baum. */
  target: string;
  /** Where a different baum the link replaced was moved. */
  backup: string | null;
  /** Whether the link's directory needed an administrator. */
  admin: boolean;
}

/** What installing would do. */
export interface Plan {
  link: string;
  /** A different baum at `link`, which installing replaces only once the
   *  user confirmed this path. It is moved to `backup`. */
  replaces: Found | null;
  backup: string | null;
  /** Whether the directory needs an administrator (a password prompt). */
  admin: boolean;
  /** Whether the directory is on the login shell's PATH. When it is not,
   *  the command works only once the user adds it. */
  on_path: boolean;
  /** Why the app will not install there, and what the user can do instead. */
  blocked: string | null;
  /** Updating the app's own link keeps the backup of the baum it replaced
   *  back then, so that removing the command still puts that one back. */
  kept_backup: string | null;
}

/** The command's state, for the setup screen. */
export interface Status {
  /** The stable path of the bundled baum that the command runs. */
  target: string | null;
  /** Why the command cannot be installed from where the app runs now. */
  unavailable: string | null;
  /** The baum that the hooks run now, if any. */
  found: Found | null;
  /** Whether the app's baum is installed as the command. */
  installed: boolean;
  /** Installed, but a different baum still comes first (Windows, where a
   *  system PATH directory precedes the user's). */
  shadowed: Found | null;
  /** What the app installed earlier, if it still holds. */
  record: InstallRecord | null;
  /** What Install would do, unless the command is installed. */
  plan: Plan | null;
  /** Why the login shell's PATH could not be read. */
  path_error: string | null;
}

/** The stable target, or why there is none. */
export type Target = { path: string } | { error: string };

export function readFound(value: unknown): Found {
  const o = decode.object(value, "a baum on the PATH");
  return {
    path: decode.string(o, "path"),
    resolved: decode.string(o, "resolved"),
    version: decode.stringOr(o, "version"),
  };
}

export function readLoginPath(value: unknown): LoginPath {
  const o = decode.object(value, "the login PATH");
  return {
    path: decode.arrayOr(o, "path", (v) => {
      if (typeof v !== "string") throw new Error("invalid type: expected a path");
      return v;
    }),
    baum: decode.optional(o, "baum", readFound),
    error: decode.optionalString(o, "error"),
    warning: decode.optionalString(o, "warning"),
  };
}

export function readRecord(value: unknown): InstallRecord {
  const o = decode.object(value, "a baum command record");
  return {
    link: decode.string(o, "link"),
    target: decode.string(o, "target"),
    backup: decode.optionalString(o, "backup"),
    admin: decode.boolOr(o, "admin"),
  };
}

/** The stable path the command links to: the baum the app runs. In an
 *  installed app bundle that is the binary next to the app's executable,
 *  which updates replace in place; an AppImage and Windows use a copy the app
 *  refreshes at every launch (see `refreshCopy`). A macOS app run from a
 *  disk image or translocated by Gatekeeper must first be moved. */
export function stableTarget(cli: string, platform: Platform): Target {
  if (
    platform === "darwin" &&
    (cli.includes("/AppTranslocation/") || cli.startsWith("/Volumes/"))
  ) {
    return {
      error:
        "Move baum to your Applications folder and open it from there to install the baum command.",
    };
  }
  return { path: cli };
}

/** Works out the command's state. `home` is the user's home directory. */
export async function status(
  platform: Platform,
  target: Target,
  login: LoginPath,
  records: InstallRecord[],
  home: string,
  env: NodeJS.ProcessEnv,
): Promise<Status> {
  if (platform === "win32") return windowsInstall.status(target, login, records, env);
  return unix.status(target, login, records, home);
}

/** Installs the plan's link to `target`. A plan that replaces a different
 *  baum needs `confirmed` to name it: the setup screen passes the path it
 *  showed the user and asked about. */
export async function install(
  platform: Platform,
  plan: Plan,
  target: string,
  confirmed: string | null,
  elevate: Elevate,
  env: NodeJS.ProcessEnv,
): Promise<InstallRecord> {
  if (platform === "win32") return windowsInstall.install(plan, target, env);
  return unix.install(plan, target, confirmed, elevate);
}

/** Removes what the app installed and nothing else; returns what happened,
 *  for the user. */
export async function uninstall(
  platform: Platform,
  record: InstallRecord,
  current: string | null,
  home: string,
  elevate: Elevate,
  env: NodeJS.ProcessEnv,
): Promise<string> {
  if (platform === "win32") return windowsInstall.uninstall(record, env);
  return unix.uninstall(record, current, home, elevate);
}

/** Puts a copy of `source` at `copy` unless the copy is the one made last
 *  time from this very `source`: a stamp beside the copy records the
 *  source's size and modification time and the copy's modification time, so
 *  an unchanged launch reads neither binary, and a copy changed or replaced
 *  since is copied again. The new file is written beside the copy and
 *  renamed over it: a daemon running the old one keeps its file. Windows
 *  cannot replace a running executable, so there the old one is first renamed
 *  aside. The copy and its directory are mode 0755 on Unix, so that a daemon
 *  trusts it as the baum on its PATH. Returns whether it copied. */
export function refreshCopy(source: string, copy: string, platform: Platform): boolean {
  const wanted = statSync(source, { bigint: true });
  const stampFile = stampPath(copy, platform);
  const current = (): string | null => {
    const fromSource = sourceStamp(source, platform);
    const fromCopy = stamp(wanted, copy, platform);
    return fromSource === null || fromCopy === null ? null : `${fromSource}${fromCopy}`;
  };
  let saved: string | null = null;
  try {
    saved = readFileSync(stampFile, "utf8");
  } catch {}
  if (saved !== null && saved === current()) return false;
  const dir = parent(copy, platform);
  const name = fileName(copy, platform);
  if (dir === null || name === null) throw new Error("invalid input");
  mkdirSync(dir, { recursive: true, mode: 0o755 });
  const temporary = join(dir, `.${name}-${process.pid}`, platform);
  rmSync(temporary, { force: true });
  rmSync(stampFile, { force: true });
  try {
    writeCopy(source, temporary);
    replace(temporary, copy, dir, platform);
  } catch (e) {
    rmSync(temporary, { force: true });
    throw e;
  }
  const next = current();
  if (next !== null) writeFileSync(stampFile, next);
  return true;
}

export function stampPath(copy: string, platform: Platform): string {
  const dir = parent(copy, platform) ?? "";
  return join(dir, `${fileName(copy, platform) ?? ""}.stamp`, platform);
}

function bigStat(path: string): BigIntStats | null {
  try {
    return statSync(path, { bigint: true });
  } catch {
    return null;
  }
}

/** What the stamp says for this source and the copy as it is now. File
 *  times tick coarsely (a few milliseconds on Linux), so an update or a
 *  rewrite within one tick keeps them; the stamp therefore also holds each
 *  file's identity (a renamed-in update is a new inode, and any write moves
 *  the change time, which no one can set back) and a hash of its first and
 *  last 64 KiB, where a binary's headers and build id live. */
function stamp(source: BigIntStats, copy: string, platform: Platform): string | null {
  const copied = bigStat(copy);
  if (copied === null || copied.size !== source.size) return null;
  const hash = endsHash(copy);
  if (hash === null) return null;
  const from = identity(source, platform);
  const to = identity(copied, platform);
  if (from === null || to === null) return null;
  return `source ${from}\ncopy ${to} ${hash}\n`;
}

/** The source's part of the stamp also hashes its ends; it is kept with the
 *  source's identity so a changed source is noticed even if its size and
 *  times match. */
function sourceStamp(source: string, platform: Platform): string | null {
  const m = bigStat(source);
  if (m === null) return null;
  const id = identity(m, platform);
  const hash = endsHash(source);
  return id === null || hash === null ? null : `${id} ${hash}`;
}

function identity(m: BigIntStats, platform: Platform): string | null {
  if (m.mtimeNs < 0n) return null;
  const extra =
    platform === "win32"
      ? ""
      : `${m.dev}:${m.ino} ${m.ctimeNs / 1_000_000_000n}.${m.ctimeNs % 1_000_000_000n}`;
  return `${m.size} ${m.mtimeNs} ${extra}`;
}

const SPAN = 64 * 1024;

/** FNV-1a over the first and last 64 KiB of a file: cheap, and stable
 *  across builds of the app. */
export function endsHash(path: string): string | null {
  let fd: number;
  try {
    fd = openSync(path, "r");
  } catch {
    return null;
  }
  try {
    const hash = new Fnv1a64();
    const len = fstatSync(fd).size;
    const buffer = Buffer.alloc(SPAN);
    hash.feed(buffer.subarray(0, readFully(fd, buffer, 0)));
    if (len > SPAN) {
      hash.feed(buffer.subarray(0, readFully(fd, buffer, Math.max(len - SPAN, SPAN))));
    }
    return hash.hex();
  } catch {
    return null;
  } finally {
    closeSync(fd);
  }
}

function readFully(fd: number, buffer: Buffer, position: number): number {
  let read = 0;
  while (read < buffer.length) {
    const n = readSync(fd, buffer, read, buffer.length - read, position + read);
    if (n === 0) break;
    read += n;
  }
  return read;
}

/** 64-bit FNV-1a in two 32-bit halves, so it needs no BigInt per byte. The
 *  prime is 2^40 + 0x1b3. */
export class Fnv1a64 {
  private hi = 0xcbf29ce4;
  private lo = 0x84222325;

  feed(bytes: Uint8Array): void {
    let { hi, lo } = this;
    for (const b of bytes) {
      lo = (lo ^ b) >>> 0;
      const low = lo * 0x1b3;
      const carry = Math.floor(low / 0x1_0000_0000);
      hi = (hi * 0x1b3 + carry + ((lo << 8) >>> 0)) % 0x1_0000_0000;
      lo = low % 0x1_0000_0000;
    }
    this.hi = hi;
    this.lo = lo;
  }

  hex(): string {
    return this.hi.toString(16).padStart(8, "0") + this.lo.toString(16).padStart(8, "0");
  }
}

function writeCopy(source: string, temporary: string): void {
  const from = openSync(source, "r");
  try {
    const to = openSync(temporary, "wx", 0o755);
    try {
      const buffer = Buffer.alloc(1 << 20);
      for (;;) {
        const n = readSync(from, buffer, 0, buffer.length, null);
        if (n === 0) break;
        let written = 0;
        while (written < n) written += writeSync(to, buffer, written, n - written);
      }
      fsyncSync(to);
    } finally {
      closeSync(to);
    }
  } finally {
    closeSync(from);
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

function replace(temporary: string, copy: string, dir: string, platform: Platform): void {
  try {
    renameSync(temporary, copy);
  } catch (e) {
    if (!(platform === "win32" && exists(copy))) throw e;
    // A running baum.exe can be renamed but not replaced: move it to a
    // name of its own, and remove those no longer running.
    const nanos = BigInt(Date.now()) * 1_000_000n + (process.hrtime.bigint() % 1_000_000n);
    const old = join(dir, `baum.${process.pid}.${nanos}.old`, platform);
    try {
      renameSync(copy, old);
    } catch {
      throw e;
    }
    renameSync(temporary, copy);
    removeOld(dir, platform);
    return;
  }
  removeOld(dir, platform);
}

/** Removes the copies renamed aside earlier; one still running stays. */
function removeOld(dir: string, platform: Platform): void {
  let names: string[];
  try {
    names = readdirSync(dir);
  } catch {
    return;
  }
  for (const name of names) {
    if (name.startsWith("baum.") && name.endsWith(".old")) {
      try {
        rmSync(join(dir, name, platform));
      } catch {}
    }
  }
}

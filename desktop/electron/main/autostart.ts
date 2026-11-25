// Start at login. Off by default; turning it on makes the operating system
// start the app, hidden, when the user logs in. One per-user entry per
// platform, which the user can also see and remove with the system's tools:
//
// - Linux: an XDG autostart entry, `$XDG_CONFIG_HOME/autostart/baum.desktop`.
// - macOS: a LaunchAgent, `~/Library/LaunchAgents/com.usebaum.desktop.plist`.
// - Windows: the per-user Run key value `baum`, set with the system's `reg.exe`.
//
// The entries are written here rather than by a plugin so the contents are
// ours to test. An enabled entry is rewritten on every start, so moving or
// updating the app keeps it pointing at the right executable.
import { spawnSync } from "node:child_process";
import {
  closeSync,
  fsyncSync,
  mkdirSync,
  openSync,
  readFileSync,
  renameSync,
  rmSync,
  statSync,
  writeSync,
} from "node:fs";
import { dirname, isAbsolute, join, parse } from "node:path";
import { windowsSystemProgram } from "./path-rules.ts";

/** The argument the login entry passes: start without showing the window. */
export const HIDDEN_FLAG = "--hidden";

export const DESKTOP_FILE = "baum.desktop";
const LAUNCH_AGENT_LABEL = "com.usebaum.desktop";
const RUN_KEY = String.raw`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`;
const RUN_VALUE = "baum";
const APPROVED_KEY = String.raw`HKCU\Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`;

const MOVE_TO_APPLICATIONS =
  "move baum to the Applications folder, open it from there, then turn on start at login";

export type Env = Record<string, string | undefined>;

/** Runs `reg.exe`; whether it succeeded, and what it printed. */
export type Reg = (args: string[]) => { ok: boolean; stdout: string };

export interface AutostartDeps {
  platform: NodeJS.Platform;
  env: Env;
  home: string;
  /** The running executable (`process.execPath`). */
  exe: string;
  /** `app.isPackaged`: a build that is not packaged is a development build. */
  isPackaged: boolean;
  /** The app runs from a read-only volume or a translocated copy (macOS), where
   *  no entry can name it. */
  misplaced?: boolean;
  /** Defaults to the system's `reg.exe`. */
  reg?: Reg;
}

/** The system's `reg.exe`, by its full path: a bare name is searched for in
 *  the working directory first. */
export function regPath(env: Env): string {
  return windowsSystemProgram(env, "reg.exe");
}

export function runReg(args: string[]): { ok: boolean; stdout: string } {
  const result = spawnSync(regPath(process.env), args, {
    stdio: ["ignore", "pipe", "ignore"],
    windowsHide: true,
    encoding: "utf8",
  });
  if (result.error) throw new Error(`cannot run reg.exe: ${result.error.message}`);
  return { ok: result.status === 0, stdout: result.stdout };
}

/** The path to write into an entry. Linux appends " (deleted)" to the path of
 *  an executable replaced while it runs (an update); that is not a path. A
 *  macOS app run from a quarantined download is copied to a random read-only
 *  location (App Translocation) that changes on every launch, so no entry
 *  can name it. */
export function checkExecutable(exe: string): string {
  if (exe.includes("/AppTranslocation/")) throw new Error(MOVE_TO_APPLICATIONS);
  return exe.endsWith(" (deleted)") ? exe.slice(0, -" (deleted)".length) : exe;
}

/** Whether an entry should name this executable on its own: not a
 *  development build or a build-directory binary. Only the user's own toggle
 *  writes an entry for those. */
export function installed(exe: string, developmentBuild: boolean): boolean {
  if (developmentBuild) return false;
  const parts = exe.split(/[\\/]/);
  return !parts.some(
    (part, i) => part === "target" && (parts[i + 1] === "debug" || parts[i + 1] === "release"),
  );
}

/** A development build is one that is not packaged, or a run with
 *  `BAUM_DESKTOP_BAUM` set (to any value). */
export function isDevelopmentBuild(isPackaged: boolean, env: Env): boolean {
  return !isPackaged || env.BAUM_DESKTOP_BAUM !== undefined;
}

// --- Desktop entry text ---

/** The lines of a file: no empty last line for a trailing newline, and no
 *  carriage returns. */
export function lines(text: string): string[] {
  const all = text.split("\n").map((l) => (l.endsWith("\r") ? l.slice(0, -1) : l));
  if (all.at(-1) === "") all.pop();
  return all;
}

export function desktopEntryPair(line: string): [string, string] | null {
  const at = line.indexOf("=");
  if (at < 0) return null;
  return [line.slice(0, at).trim(), line.slice(at + 1).trim()];
}

export function isTrue(value: string): boolean {
  return value.toLowerCase() === "true";
}

/** The `Exec` value. A newline would end the line and let the rest of the
 *  path become another key. */
export function execLine(exe: string): string {
  if (/[\n\r]/.test(exe)) {
    throw new Error("the app's path holds a line break, which an autostart entry cannot");
  }
  return `${desktopExecQuote(exe)} ${HIDDEN_FLAG}`;
}

/** A new XDG autostart entry (Desktop Entry Specification 1.5). */
export function desktopEntry(exe: string): string {
  return (
    "[Desktop Entry]\n" +
    "Type=Application\n" +
    "Name=baum\n" +
    "Comment=Keep up with what your coding agents build\n" +
    `Exec=${execLine(exe)}\n` +
    "Terminal=false\n" +
    "X-GNOME-Autostart-enabled=true\n"
  );
}

/** Whether the entry's `[Desktop Entry]` group disables it, as desktop
 *  environments do when the user turns it off in their settings. */
export function entryDisabled(content: string): boolean {
  let inGroup = false;
  for (const raw of lines(content)) {
    const line = raw.trim();
    if (line.startsWith("[")) {
      inGroup = line === "[Desktop Entry]";
      continue;
    }
    const pair = inGroup ? desktopEntryPair(line) : null;
    if (!pair) continue;
    const [key, value] = pair;
    if (
      (key === "Hidden" && isTrue(value)) ||
      (key === "X-GNOME-Autostart-enabled" && value.toLowerCase() === "false")
    ) {
      return true;
    }
  }
  return false;
}

/** The entry with `Exec` set to this executable and any disable removed, and
 *  every other key and group kept as the user or their desktop left it. */
export function rewriteEntry(existing: string, exe: string): string {
  const exec = `Exec=${execLine(exe)}`;
  const out: string[] = [];
  let inGroup = false;
  let seenGroup = false;
  let hasExec = false;
  let hasEnabled = false;
  // Closing the group adds whatever it lacked.
  const close = () => {
    if (!hasExec) out.push(exec);
    if (!hasEnabled) out.push("X-GNOME-Autostart-enabled=true");
  };
  for (const line of lines(existing)) {
    const trimmed = line.trim();
    if (trimmed.startsWith("[")) {
      if (inGroup) close();
      inGroup = trimmed === "[Desktop Entry]";
      seenGroup ||= inGroup;
      out.push(line);
      continue;
    }
    const pair = inGroup ? desktopEntryPair(trimmed) : null;
    if (pair) {
      const [key] = pair;
      if (key === "Exec") {
        hasExec = true;
        out.push(exec);
        continue;
      }
      if (key === "Hidden") continue;
      if (key === "X-GNOME-Autostart-enabled") {
        hasEnabled = true;
        out.push("X-GNOME-Autostart-enabled=true");
        continue;
      }
    }
    out.push(line);
  }
  if (!seenGroup) return desktopEntry(exe);
  if (inGroup) close();
  return `${out.join("\n")}\n`;
}

const RESERVED = new Set(" \t\n\"'\\><~|&;$*?#()`");

/** Quotes one `Exec` argument. The specification wraps an argument holding a
 *  reserved character in double quotes and backslash-escapes `"`, `` ` ``,
 *  `$` and `\` inside them (the file's own string escaping then doubles each
 *  backslash again); `%` starts a field code, so a literal one is doubled. */
export function desktopExecQuote(arg: string): string {
  const text = arg.replaceAll("%", "%%");
  if (![...text].some((c) => RESERVED.has(c))) return text;
  let out = '"';
  for (const c of text) {
    if (c === '"' || c === "`" || c === "$") out += `\\\\${c}`;
    else if (c === "\\") out += "\\\\\\\\";
    else out += c;
  }
  return `${out}"`;
}

// --- macOS and Windows entries ---

/** A launchd agent property list that starts the app at login. */
export function launchAgent(exe: string): string {
  return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
\t<key>Label</key>
\t<string>${LAUNCH_AGENT_LABEL}</string>
\t<key>ProgramArguments</key>
\t<array>
\t\t<string>${xmlEscape(exe)}</string>
\t\t<string>${HIDDEN_FLAG}</string>
\t</array>
\t<key>RunAtLoad</key>
\t<true/>
\t<key>LimitLoadToSessionType</key>
\t<string>Aqua</string>
\t<key>ProcessType</key>
\t<string>Interactive</string>
</dict>
</plist>
`;
}

export function xmlEscape(text: string): string {
  return text
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&apos;");
}

/** The Run key's value: the quoted executable and the flag. */
export function runValue(exe: string): string {
  return `"${exe}" ${HIDDEN_FLAG}`;
}

export function regAddArgs(exe: string): string[] {
  return ["add", RUN_KEY, "/v", RUN_VALUE, "/t", "REG_SZ", "/d", runValue(exe), "/f"];
}

export const regDeleteArgs = (): string[] => ["delete", RUN_KEY, "/v", RUN_VALUE, "/f"];
export const regQueryArgs = (): string[] => ["query", RUN_KEY, "/v", RUN_VALUE];
export const regApprovedQueryArgs = (): string[] => ["query", APPROVED_KEY, "/v", RUN_VALUE];
export const regApprovedDeleteArgs = (): string[] => [
  "delete",
  APPROVED_KEY,
  "/v",
  RUN_VALUE,
  "/f",
];

/** Whether `reg query` of the StartupApproved value says Task Manager (or
 *  Settings) disabled the entry. The value is binary and its first byte's low
 *  bit is set when disabled: 02 and 06 are enabled, 03 and 07 disabled. */
export function startupDisabled(output: string): boolean {
  return lines(output)
    .filter((l) => l.includes("REG_BINARY"))
    .map((l) => l.trim().split(/\s+/).at(-1) ?? "")
    .filter((hex) => /^[0-9a-f]{2}/i.test(hex))
    .some((hex) => (Number.parseInt(hex.slice(0, 2), 16) & 1) === 1);
}

// --- Files ---

function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/** Writes `content` to `path` through a temporary file, so a crash leaves the
 *  old entry or the new one and never half of one. */
export function writeEntry(path: string, content: string): void {
  const dir = dirname(path);
  try {
    mkdirSync(dir, { recursive: true });
  } catch (e) {
    throw new Error(`cannot create ${dir}: ${errorMessage(e)}`);
  }
  const temp = join(dir, `${parse(path).name}.tmp`);
  try {
    const fd = openSync(temp, "w");
    try {
      writeSync(fd, content);
      fsyncSync(fd);
    } finally {
      closeSync(fd);
    }
    renameSync(temp, path);
  } catch (e) {
    rmSync(temp, { force: true });
    throw new Error(`cannot write ${path}: ${errorMessage(e)}`);
  }
}

/** Removes the entry; one that is already gone is fine. */
export function removeEntry(path: string): void {
  try {
    rmSync(path);
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code === "ENOENT") return;
    throw new Error(`cannot remove ${path}: ${errorMessage(e)}`);
  }
}

/** `$XDG_CONFIG_HOME/autostart`, else `~/.config/autostart`; a relative
 *  `XDG_CONFIG_HOME` is ignored, as the specification says. */
export function linuxEntryPath(
  config: string | undefined,
  home: string | undefined,
): string | null {
  const base = config && isAbsolute(config) ? config : home ? join(home, ".config") : null;
  return base === null ? null : join(base, "autostart", DESKTOP_FILE);
}

function readText(path: string): string | null {
  try {
    return readFileSync(path, "utf8");
  } catch {
    return null;
  }
}

// --- Per platform ---

interface Platform {
  enabled(): boolean;
  set(on: boolean, exe: string): void;
}

function linux(deps: AutostartDeps): Platform {
  const path = () => linuxEntryPath(deps.env.XDG_CONFIG_HOME, deps.home);
  return {
    enabled() {
      const entry = path();
      const content = entry === null ? null : readText(entry);
      return content !== null && !entryDisabled(content);
    },
    set(on, exe) {
      const entry = path();
      if (entry === null) throw new Error("cannot find the XDG config directory");
      if (!on) return removeEntry(entry);
      const existing = readText(entry);
      writeEntry(entry, existing === null ? desktopEntry(exe) : rewriteEntry(existing, exe));
    },
  };
}

function macos(deps: AutostartDeps): Platform {
  const path = join(deps.home, "Library/LaunchAgents", `${LAUNCH_AGENT_LABEL}.plist`);
  return {
    enabled() {
      try {
        return statSync(path).isFile();
      } catch {
        return false;
      }
    },
    set(on, exe) {
      if (on) writeEntry(path, launchAgent(exe));
      else removeEntry(path);
    },
  };
}

function windows(deps: AutostartDeps): Platform {
  const reg = deps.reg ?? runReg;
  const tryReg = (args: string[]) => {
    try {
      return reg(args);
    } catch {
      return { ok: false, stdout: "" };
    }
  };
  return {
    enabled() {
      const present = tryReg(regQueryArgs()).ok;
      const approved = tryReg(regApprovedQueryArgs());
      return present && !(approved.ok && startupDisabled(approved.stdout));
    },
    set(on, exe) {
      if (on) {
        // Asking for it again clears a Task Manager disable.
        tryReg(regApprovedDeleteArgs());
        if (!reg(regAddArgs(exe)).ok) throw new Error("reg.exe could not set the Run key");
      } else if (reg(regQueryArgs()).ok && !reg(regDeleteArgs()).ok) {
        throw new Error("reg.exe could not remove the Run key value");
      }
    },
  };
}

function unsupported(): Platform {
  return {
    enabled: () => false,
    set() {
      throw new Error("start at login is not supported on this platform");
    },
  };
}

export interface Autostart {
  /** Whether start at login is on. An entry the desktop environment has
   *  disabled (GNOME Tweaks and KDE write `Hidden` or
   *  `X-GNOME-Autostart-enabled=false`; Windows Task Manager writes
   *  StartupApproved) is off. */
  enabled(): boolean;
  /** Turns it on or off. Turning it on also clears a disable the desktop
   *  environment recorded, since the user is now asking for it. */
  set(on: boolean): void;
  /** Rewrites an enabled entry so it names this executable. Never touches an
   *  entry that is off, whoever turned it off, and does nothing for
   *  development builds. */
  refresh(): void;
}

export function createAutostart(deps: AutostartDeps): Autostart {
  const platform =
    deps.platform === "linux"
      ? linux(deps)
      : deps.platform === "darwin"
        ? macos(deps)
        : deps.platform === "win32"
          ? windows(deps)
          : unsupported();

  // An AppImage runs from a temporary mount, so its own path (`APPIMAGE`) is
  // the stable one. A macOS app on a disk image gets no entry.
  const executable = (): string => {
    if (deps.misplaced) throw new Error(MOVE_TO_APPLICATIONS);
    const image = deps.env.APPIMAGE;
    return checkExecutable(image ? image : deps.exe);
  };

  return {
    enabled: () => platform.enabled(),
    set: (on) => platform.set(on, executable()),
    refresh() {
      let exe: string;
      try {
        exe = executable();
      } catch {
        return;
      }
      if (!installed(exe, isDevelopmentBuild(deps.isPackaged, deps.env))) return;
      try {
        if (platform.enabled()) platform.set(true, exe);
      } catch (e) {
        console.error(`baum desktop: cannot refresh the start at login entry: ${errorMessage(e)}`);
      }
    },
  };
}

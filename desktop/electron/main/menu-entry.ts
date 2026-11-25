// The AppImage's applications menu entry. Without an integration tool such as
// AppImageLauncher or Gear Lever, nothing adds a downloaded AppImage to the
// menu, so the app does, as
// `$XDG_DATA_HOME/applications/baum-appimage.desktop`, with the icon copied
// beside its copy of baum, since the AppImage's own files vanish when it quits.
//
// Every start of an installed AppImage rewrites the entry, so moving the file
// keeps it working, and `TryExec` hides the entry once the file is gone. An
// entry the user hid (`Hidden` or `NoDisplay`) is left alone. When another
// visible entry in the same directory already starts this AppImage (one an
// integration tool wrote), the app removes its own instead, so the menu does
// not list baum twice. The deb installs its own entry and never runs this.
import {
  closeSync,
  fsyncSync,
  mkdirSync,
  openSync,
  readdirSync,
  readFileSync,
  renameSync,
  rmSync,
  writeSync,
} from "node:fs";
import { dirname, isAbsolute, join, parse } from "node:path";
import {
  desktopEntryPair,
  desktopExecQuote,
  type Env,
  isDevelopmentBuild,
  isTrue,
  lines,
  writeEntry,
} from "./autostart.ts";

const ENTRY_FILE = "baum-appimage.desktop";
/** The icon linuxdeploy puts at the root of the AppImage. */
const BUNDLED_ICON = "baum-desktop.png";
const ICON_FILE = "baum.png";

export interface MenuEntryDeps {
  isPackaged: boolean;
  env: Env;
  home: string;
  /** The running executable (`process.execPath`). */
  exe: string;
  /** The app's local data directory, where the icon is copied. */
  dataDir: string;
}

/** Whether the app runs from an AppImage's mount. */
export function runningAsAppImage(env: Env, exe: string): boolean {
  const appdir = env.APPDIR;
  if (env.APPIMAGE === undefined || appdir === undefined || appdir === "") return false;
  return exe === appdir || exe.startsWith(appdir.endsWith("/") ? appdir : `${appdir}/`);
}

export function refresh(deps: MenuEntryDeps): void {
  const { env } = deps;
  if (isDevelopmentBuild(deps.isPackaged, env) || !runningAsAppImage(env, deps.exe)) return;
  const image = env.APPIMAGE;
  const appdir = env.APPDIR;
  if (image === undefined || appdir === undefined) return;
  const path = entryPathIn(env.XDG_DATA_HOME, deps.home);
  if (path === null) return;
  let icon: string | undefined;
  try {
    icon = join(deps.dataDir, ICON_FILE);
    copyIfChanged(join(appdir, BUNDLED_ICON), icon);
  } catch (e) {
    console.error(`baum desktop: cannot copy the menu icon: ${message(e)}`);
    icon = undefined;
  }
  try {
    update(path, image, icon);
  } catch (e) {
    console.error(`baum desktop: cannot write the applications menu entry: ${message(e)}`);
  }
}

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/** `$XDG_DATA_HOME/applications`, else `~/.local/share/applications`; a
 *  relative `XDG_DATA_HOME` is invalid and ignored. */
export function entryPathIn(data: string | undefined, home: string | undefined): string | null {
  const base = data && isAbsolute(data) ? data : home ? join(home, ".local/share") : null;
  return base === null ? null : join(base, "applications", ENTRY_FILE);
}

function read(path: string): string | null {
  try {
    return readFileSync(path, "utf8");
  } catch {
    return null;
  }
}

/** Writes the entry unless the user hid it or it is already current, and
 *  removes it when another visible entry already starts the AppImage. */
export function update(path: string, image: string, icon?: string): void {
  const existing = read(path);
  if (existing !== null && hidden(existing)) return;
  if (startedByAnotherEntry(path, image)) {
    try {
      rmSync(path);
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code !== "ENOENT") {
        throw new Error(`cannot remove ${path}: ${message(e)}`);
      }
    }
    return;
  }
  const entry = menuEntry(image, icon);
  if (existing === entry) return;
  writeEntry(path, entry);
}

export function menuEntry(image: string, icon?: string): string {
  if (/[\n\r]/.test(image)) {
    throw new Error("the AppImage's path holds a line break, which a menu entry cannot");
  }
  let entry =
    "[Desktop Entry]\n" +
    "Type=Application\n" +
    "Name=baum\n" +
    "Comment=Keep up with what your coding agents build\n" +
    `Exec=${desktopExecQuote(image)}\n` +
    `TryExec=${escape(image)}\n`;
  if (icon !== undefined && !/[\n\r]/.test(icon)) entry += `Icon=${escape(icon)}\n`;
  entry += "Terminal=false\nCategories=Development;\nStartupWMClass=baum-desktop\n";
  return entry;
}

/** A string value's escaping: only the backslash needs it in a path. */
function escape(value: string): string {
  return value.replaceAll("\\", "\\\\");
}

/** Whether the `[Desktop Entry]` group hides the entry from menus. */
export function hidden(content: string): boolean {
  let inGroup = false;
  for (const raw of lines(content)) {
    const line = raw.trim();
    if (line.startsWith("[")) {
      inGroup = line === "[Desktop Entry]";
      continue;
    }
    const pair = inGroup ? desktopEntryPair(line) : null;
    if (pair && (pair[0] === "Hidden" || pair[0] === "NoDisplay") && isTrue(pair[1])) return true;
  }
  return false;
}

/** Whether a visible `.desktop` file beside `own` starts `image`, by its
 *  `TryExec` or the program in its `Exec`, as AppImageLauncher's and Gear
 *  Lever's do. A hidden one does not count: baum would be in no menu. */
function startedByAnotherEntry(own: string, image: string): boolean {
  let names: string[];
  try {
    names = readdirSync(dirname(own));
  } catch {
    return false;
  }
  return names.some((name) => {
    const path = join(dirname(own), name);
    if (path === own || parse(name).ext !== ".desktop") return false;
    const content = read(path);
    return content !== null && !hidden(content) && starts(content, image);
  });
}

export function starts(content: string, image: string): boolean {
  let inGroup = false;
  for (const raw of lines(content)) {
    const line = raw.trim();
    if (line.startsWith("[")) {
      inGroup = line === "[Desktop Entry]";
      continue;
    }
    const pair = inGroup ? desktopEntryPair(line) : null;
    if (!pair) continue;
    const [key, rawValue] = pair;
    const value = unescape(rawValue);
    let program: string | null = null;
    if (key === "TryExec") program = value;
    else if (key === "Exec") program = execProgram(value)?.replaceAll("%%", "%") ?? null;
    if (program === image) return true;
  }
  return false;
}

/** Undoes a string value's escapes (`\s`, `\n`, `\t`, `\r`, `\\`). */
function unescape(value: string): string {
  const named: Record<string, string> = { s: " ", n: "\n", t: "\t", r: "\r" };
  let out = "";
  const chars = [...value];
  for (let i = 0; i < chars.length; i++) {
    const c = chars[i] as string;
    if (c !== "\\") {
      out += c;
      continue;
    }
    const next = chars[++i];
    out += next === undefined ? "\\" : (named[next] ?? next);
  }
  return out;
}

/** The program, the first argument of an unescaped `Exec` value: a quoted
 *  argument ends at the next unescaped `"`, a plain one at a space. */
function execProgram(exec: string): string | null {
  const text = exec.trimStart();
  if (!text.startsWith('"')) {
    const program = text.split(" ")[0] ?? "";
    return program === "" ? null : program;
  }
  let out = "";
  const chars = [...text.slice(1)];
  for (let i = 0; i < chars.length; i++) {
    const c = chars[i] as string;
    if (c === '"') return out;
    if (c === "\\") {
      const next = chars[++i];
      if (next === undefined) return null;
      out += next;
    } else out += c;
  }
  return null;
}

/** Copies `from` to `to` through a temporary file when their contents differ. */
export function copyIfChanged(from: string, to: string): void {
  let bytes: Buffer;
  try {
    bytes = readFileSync(from);
  } catch (e) {
    throw new Error(`cannot read ${from}: ${message(e)}`);
  }
  const current = (() => {
    try {
      return readFileSync(to);
    } catch {
      return null;
    }
  })();
  if (current?.equals(bytes)) return;
  const dir = dirname(to);
  try {
    mkdirSync(dir, { recursive: true });
  } catch (e) {
    throw new Error(`cannot create ${dir}: ${message(e)}`);
  }
  const temp = join(dir, `${parse(to).name}.tmp`);
  try {
    const fd = openSync(temp, "w");
    try {
      writeSync(fd, bytes);
      fsyncSync(fd);
    } finally {
      closeSync(fd);
    }
    renameSync(temp, to);
  } catch (e) {
    rmSync(temp, { force: true });
    throw new Error(`cannot write ${to}: ${message(e)}`);
  }
}

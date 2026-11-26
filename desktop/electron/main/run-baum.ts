// Running the app's baum for setup: with a deadline, its output read as
// lines, and the AppImage's mount taken off its PATH.
import { spawn } from "node:child_process";
import type { Readable } from "node:stream";
import { joinPaths, type Platform, splitPaths, startsWith } from "./path-rules.ts";

/** What the setup code reads of the app's own process, injected so tests can
 *  stand in for an AppImage or another platform. */
export interface AppProcess {
  platform: Platform;
  env: NodeJS.ProcessEnv;
  /** The app's executable (`process.execPath`). */
  execPath: string;
}

export interface Output {
  ok: boolean;
  stdout: string[];
  stderr: string[];
}

/** Whether `BAUM_DESKTOP_BAUM` names the baum to run (development). */
export function cliOverridden(app: AppProcess): boolean {
  return app.env.BAUM_DESKTOP_BAUM !== undefined;
}

/** `APPIMAGE` alone is not enough, since children of an AppImage (such as an
 *  editor's terminal) inherit it: the app must run from `APPDIR`. */
export function runningAsAppImage(app: AppProcess): boolean {
  const { APPIMAGE, APPDIR } = app.env;
  if (APPIMAGE === undefined || APPDIR === undefined) return false;
  return APPDIR !== "" && startsWith(app.execPath, APPDIR, app.platform);
}

/** The PATH every baum the app runs gets, when it differs from the app's: an
 *  AppImage's runtime puts its own mount (`$APPDIR/usr/bin`) first, which
 *  holds the bundled baum that vanishes when the app quits. Left in, the
 *  daemon would inherit it whenever the login shell cannot be read, and
 *  setup would report that baum as a different one in the way. */
export function childPath(app: AppProcess): string | null {
  if (!runningAsAppImage(app)) return null;
  const { APPDIR, PATH } = app.env;
  if (APPDIR === undefined || PATH === undefined) return null;
  return withoutDir(PATH, APPDIR, app.platform);
}

/** Takes other AppImages' mounts out of `app.env`, in place, and returns the
 *  names of the variables it changed. An AppImage started by another one
 *  inherits that one's runtime variables: the Tauri app's updater (0.2.x)
 *  started its Electron successor with a PATH, LD_LIBRARY_PATH, GTK and XDG
 *  variables pointing into the old mount, which stayed mounted while the new
 *  app ran, so setup found the old bundled baum first on the PATH and the
 *  daemon and terminals would have inherited it. An entry is another
 *  AppImage's when it lies inside a `.mount_` directory other than this
 *  app's own `APPDIR`; a variable left with no entries is removed. Linux
 *  only, where AppImages run. */
export function dropForeignMounts(app: AppProcess): string[] {
  if (app.platform !== "linux") return [];
  const changed: string[] = [];
  for (const [name, value] of Object.entries(app.env)) {
    if (value === undefined || !value.includes(".mount_")) continue;
    const entries = value.split(":");
    const kept = entries.filter((entry) => !inForeignMount(app, entry));
    if (kept.length === entries.length) continue;
    changed.push(name);
    if (kept.every((entry) => entry === "")) delete app.env[name];
    else app.env[name] = kept.join(":");
  }
  return changed;
}

/** Whether `path` lies inside another AppImage's mount (see
 *  `dropForeignMounts`). */
export function inForeignMount(app: AppProcess, path: string): boolean {
  if (app.platform !== "linux") return false;
  if (!path.split("/").some((part) => part.startsWith(".mount_"))) return false;
  const own = runningAsAppImage(app) ? app.env.APPDIR : undefined;
  return own === undefined || own === "" || !startsWith(path, own, app.platform);
}

/** `path` without the entries inside `dir`. */
export function withoutDir(path: string, dir: string, platform: Platform): string {
  const kept = splitPaths(path, platform).filter((entry) => !startsWith(entry, dir, platform));
  try {
    return joinPaths(kept, platform);
  } catch {
    return path;
  }
}

/** Runs the bundled baum with a deadline, reading its output as lines. With
 *  `path`, that is its PATH; otherwise the app's, less an AppImage's mount. */
export function runBaum(
  app: AppProcess,
  cli: string,
  args: readonly string[],
  timeoutMs: number,
  path?: string,
): Promise<Output> {
  const childPathValue = path ?? childPath(app);
  const env = childPathValue === null ? app.env : { ...app.env, PATH: childPathValue };
  return new Promise((resolve, reject) => {
    const child = spawn(cli, args, { stdio: ["ignore", "pipe", "pipe"], env });
    const stdout = lines(child.stdout);
    const stderr = lines(child.stderr);
    let timedOut = false;
    const timer = setTimeout(
      () => {
        timedOut = true;
        child.kill("SIGKILL");
      },
      Math.max(0, timeoutMs),
    );
    child.on("error", (e) => {
      clearTimeout(timer);
      reject(new Error(`Cannot run the bundled baum (${cli}): ${e.message}`));
    });
    child.on("exit", (code) => {
      clearTimeout(timer);
      if (timedOut) {
        child.stdout.destroy();
        child.stderr.destroy();
        reject(
          new Error(
            `baum ${args.join(" ")} did not finish within ${Math.floor(timeoutMs / 1000)} seconds.`,
          ),
        );
        return;
      }
      // A process the command left behind may hold its output open.
      Promise.all([stdout.within(2000), stderr.within(2000)]).then(([out, err]) =>
        resolve({ ok: code === 0, stdout: out, stderr: err }),
      );
    });
  });
}

/** A stream's lines once it ends, as Rust's `BufRead::lines`: split at
 *  `\n`, a trailing `\r` dropped, stopping at the first line that is not
 *  UTF-8. `within` gives up after a while with no lines, and closes it. */
function lines(stream: Readable): { within(ms: number): Promise<string[]> } {
  const chunks: Buffer[] = [];
  const ended = new Promise<string[]>((resolve) => {
    stream.on("data", (b: Buffer) => chunks.push(b));
    stream.on("error", () => resolve(split(Buffer.concat(chunks))));
    stream.on("end", () => resolve(split(Buffer.concat(chunks))));
  });
  return {
    within(ms) {
      let timer: NodeJS.Timeout | undefined;
      const late = new Promise<string[]>((resolve) => {
        timer = setTimeout(() => {
          stream.destroy();
          resolve([]);
        }, ms);
      });
      return Promise.race([ended, late]).finally(() => clearTimeout(timer));
    },
  };
}

const utf8 = new TextDecoder("utf-8", { fatal: true });

export function split(data: Buffer): string[] {
  const result: string[] = [];
  let start = 0;
  while (start < data.length) {
    let end = data.indexOf(0x0a, start);
    const next = end === -1 ? data.length : end + 1;
    if (end === -1) end = data.length;
    let line = data.subarray(start, end);
    if (line.at(-1) === 0x0d) line = line.subarray(0, -1);
    try {
      result.push(utf8.decode(line));
    } catch {
      break;
    }
    start = next;
  }
  return result;
}

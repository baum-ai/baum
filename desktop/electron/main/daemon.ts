// The app's side of the contract with `baum daemon` (src/daemon.rs in the
// Tauri shell). The bundled `baum` binary does everything that knows where
// the daemon lives: `baum daemon ensure --json` starts the user's daemon if
// none is running and prints its status, whose `state` directory holds the
// app credential and the logs. This module only runs that command, reads the
// credential and checks the daemon still answers.
import { spawn } from "node:child_process";
import { accessSync, constants, lstatSync, readFileSync } from "node:fs";
import { request } from "node:http";
import { delimiter, dirname, isAbsolute, join, relative } from "node:path";
import type { Readable } from "node:stream";
import { inspect } from "node:util";

/** How long `baum daemon ensure` may take, including a first start that
 *  resolves the login-shell environment. */
export const ENSURE_TIMEOUT = 45_000;
export const HEALTH_TIMEOUT = 2_000;
export const LOG_LINES = 40;

const CREDENTIAL_FILE = "credential.json";
const LOG_FILES = ["daemon.log", "daemon.out"];
const MAX_CREDENTIAL_BYTES = 4096;
const TOKEN = /^[A-Za-z0-9_-]{43}$/;

/** What `baum daemon ensure --json` prints: the running daemon's status. */
export interface Status {
  version: string;
  pid: number;
  /** The loopback origin of the app transport. */
  app: string | null;
  /** The daemon's private state directory. */
  state: string;
}

/** Why the app could not reach a daemon, with whatever the user needs to
 *  see: recent daemon log or command output, newest last. */
export class Failure extends Error {
  constructor(
    message: string,
    readonly details: string[] = [],
  ) {
    super(message);
  }
}

/** A daemon the window can load. The launch URL holds the credential in its
 *  fragment; it is private and never printed, so a stray log or inspection
 *  cannot leak it. */
export class Launch {
  readonly #url: string;
  readonly #token: string;
  status: Status;
  readonly origin: string;

  constructor(url: string, token: string, status: Status) {
    this.#url = url;
    this.#token = token;
    this.status = status;
    this.origin = new URL(url).origin;
  }

  /** The launch URL. Never log it. */
  url(): string {
    return this.#url;
  }

  /** The launch capability, which the daemon also accepts as a bearer token
   *  on its `/api/` routes. Never log it. */
  bearer(): string {
    return this.#token;
  }

  toJSON(): object {
    return { status: this.status, origin: this.origin, credential: "<redacted>" };
  }

  [inspect.custom](): string {
    return `Launch ${inspect(this.toJSON())}`;
  }
}

export const DISK_IMAGE =
  "baum cannot run from a disk image or another read-only disk: once the disk is ejected, baum stops working in the background. Quit baum, drag it into the Applications folder, eject the disk image, then open baum from Applications.";
export const TRANSLOCATED =
  "macOS opened baum from a temporary read-only copy because it is still where it was downloaded. Quit baum, move it into the Applications folder, then open it from there.";

/** Why the app cannot run baum from where it is installed, if it cannot. A
 *  macOS disk image is read-only and goes away when it is ejected: a daemon
 *  started from one keeps running without its executable and stops
 *  answering, and no other client replaces it. App Translocation runs a
 *  quarantined app from a random read-only mount that also goes away. An app
 *  on a writable external volume is fine, so only a read-only one counts. */
export function misplacedMessage(
  exe: string,
  platform: NodeJS.Platform,
  readOnly: (path: string) => boolean = onReadOnlyVolume,
): string | null {
  if (platform !== "darwin") return null;
  if (exe.includes("/AppTranslocation/")) return TRANSLOCATED;
  if (readOnly(exe)) return DISK_IMAGE;
  return null;
}

/** Whether `path` is on a read-only file system. The Tauri shell read the
 *  mount's flags with statfs, which Node does not expose; asking for write
 *  access answers EROFS on a read-only mount whatever the file's mode. */
export function onReadOnlyVolume(path: string): boolean {
  try {
    accessSync(dirname(path), constants.W_OK);
    return false;
  } catch (e) {
    return (e as NodeJS.ErrnoException).code === "EROFS";
  }
}

/** The target triple the sidecar build names the development binary after
 *  (`binaries/baum-<triple>`). */
export function targetTriple(platform: NodeJS.Platform, arch: string): string {
  const cpu = arch === "arm64" ? "aarch64" : "x86_64";
  switch (platform) {
    case "darwin":
      return `${cpu}-apple-darwin`;
    case "win32":
      return `${cpu}-pc-windows-msvc`;
    default:
      return `${cpu}-unknown-linux-gnu`;
  }
}

export interface Where {
  platform: NodeJS.Platform;
  arch: string;
  packaged: boolean;
  /** `process.resourcesPath` of a packaged app. */
  resources: string;
  /** `app.getAppPath()`: oss/desktop in development. */
  appPath: string;
}

/** The baum bundled with the app: `bin/baum` among the packaged app's
 *  resources, else the sidecar build in `binaries/`. */
export function bundledCliPath(where: Where): string {
  const exe = where.platform === "win32" ? ".exe" : "";
  if (where.packaged) return join(where.resources, "bin", `baum${exe}`);
  return join(where.appPath, "binaries", `baum-${targetTriple(where.platform, where.arch)}${exe}`);
}

/** Which baum the app runs, and from where. */
export interface Cli {
  /** `BAUM_DESKTOP_BAUM`, which names the baum to run while developing. */
  override: string | undefined;
  /** The app's executable. */
  exe: string;
  platform: NodeJS.Platform;
  /** The bundled binary. */
  bundled: string;
  /** Where the app keeps a stable copy of the bundled binary (an AppImage's
   *  mount vanishes when it quits; Windows), a function that refreshes the
   *  copy if needed and returns it. The setup port (install.rs's
   *  `refresh_copy`) supplies it; without one the app runs the bundled
   *  binary. A copy that could not be made is an error the connecting screen
   *  shows, never a fall back to the binary that would vanish. */
  stable?: () => Promise<string>;
  /** Tests only: whether a path is on a read-only volume. */
  readOnly?: (path: string) => boolean;
}

/** The app cannot run baum from where it is, or null when it can. */
export function misplaced(cli: Cli): string | null {
  if (cli.override) return null;
  return misplacedMessage(cli.exe, cli.platform, cli.readOnly);
}

/** The `baum` binary the app runs: `BAUM_DESKTOP_BAUM` while developing,
 *  else the stable copy where the app keeps one, else the bundled one. */
export async function cliPath(cli: Cli): Promise<string> {
  if (cli.override) return cli.override;
  const message = misplaced(cli);
  if (message) throw new Failure(message);
  if (cli.stable) {
    try {
      return await cli.stable();
    } catch (e) {
      throw new Failure(e instanceof Error ? e.message : String(e));
    }
  }
  return cli.bundled;
}

/** Whether this process runs from an AppImage's mount. `APPIMAGE` alone is
 *  not enough: children of an AppImage (an editor's terminal) inherit it. */
export function runningAsAppImage(env: NodeJS.ProcessEnv, exe: string): boolean {
  const appdir = env.APPDIR;
  if (!env.APPIMAGE || !appdir) return false;
  const inside = relative(appdir, exe);
  return inside !== "" && !inside.startsWith("..") && !isAbsolute(inside);
}

/** `path` without the entries inside `dir`. */
export function withoutDir(path: string, dir: string, separator = delimiter): string {
  return path
    .split(separator)
    .filter((entry) => {
      const inside = relative(dir, entry);
      return !(inside === "" || (!inside.startsWith("..") && !isAbsolute(inside)));
    })
    .join(separator);
}

/** The PATH every baum the app runs gets, when it differs from the app's: an
 *  AppImage's runtime puts its own mount first, which holds the bundled baum
 *  that vanishes when the app quits. */
export function childPath(env: NodeJS.ProcessEnv, exe: string): string | undefined {
  if (!runningAsAppImage(env, exe) || !env.APPDIR || env.PATH === undefined) return undefined;
  return withoutDir(env.PATH, env.APPDIR);
}

export interface Output {
  stdout: string[];
  stderr: string[];
}

/** Lines of a stream, until it closes or, once the process has exited,
 *  briefly goes quiet: a detached daemon that kept a copy of the pipe cannot
 *  hold the app up. */
function lines(stream: Readable | null) {
  const out: string[] = [];
  let pending = "";
  let ended = stream === null;
  let last = Date.now();
  stream?.setEncoding("utf8");
  stream?.on("data", (chunk: string) => {
    last = Date.now();
    pending += chunk;
    const parts = pending.split(/\r?\n/);
    pending = parts.pop() ?? "";
    out.push(...parts);
  });
  const end = () => {
    ended = true;
  };
  stream?.on("end", end);
  stream?.on("error", end);
  stream?.on("close", end);
  return async (): Promise<string[]> => {
    while (!ended && Date.now() - last < 500) await sleep(50);
    stream?.destroy();
    if (pending) out.push(pending);
    pending = "";
    return out;
  };
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/** Runs the CLI with a deadline. */
export function run(
  cli: string,
  args: string[],
  timeout: number,
  env: NodeJS.ProcessEnv = process.env,
  exe: string = process.execPath,
): Promise<Output> {
  return new Promise((resolve, reject) => {
    const path = childPath(env, exe);
    const child = spawn(cli, args, {
      stdio: ["ignore", "pipe", "pipe"],
      env: path === undefined ? env : { ...env, PATH: path },
      // No console window flashing next to the app.
      windowsHide: true,
    });
    const stdout = lines(child.stdout);
    const stderr = lines(child.stderr);
    let settled = false;
    const timer = setTimeout(async () => {
      if (settled) return;
      settled = true;
      child.kill("SIGKILL");
      reject(
        new Failure(
          `The baum daemon did not start within ${Math.round(timeout / 1000)} seconds.`,
          await stderr(),
        ),
      );
    }, timeout);
    child.on("error", (e) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      reject(new Failure(`Cannot run the bundled baum command (${cli}): ${e.message}`));
    });
    child.on("exit", async (code, signal) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      const output = { stdout: await stdout(), stderr: await stderr() };
      if (code !== 0) {
        const exit = signal ? `signal: ${signal}` : `exit status: ${code}`;
        reject(new Failure(`baum ${args.join(" ")} failed (${exit}).`, output.stderr));
        return;
      }
      resolve(output);
    });
  });
}

export function parseStatus(stdout: string[]): Status {
  const line = [...stdout].reverse().find((l) => l.trim() !== "");
  if (line === undefined) throw new Error("baum daemon ensure printed no status.");
  let value: unknown;
  try {
    value = JSON.parse(line);
  } catch (e) {
    throw new Error(`baum daemon ensure printed an unreadable status: ${String(e)}`);
  }
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error("baum daemon ensure printed an unreadable status: not an object");
  }
  const status = value as Record<string, unknown>;
  if (typeof status.state !== "string") {
    throw new Error("baum daemon ensure printed an unreadable status: no state directory");
  }
  if (!isAbsolute(status.state)) {
    throw new Error("baum daemon ensure reported a relative state directory.");
  }
  return {
    version: typeof status.version === "string" ? status.version : "",
    pid: typeof status.pid === "number" ? status.pid : 0,
    app: typeof status.app === "string" ? status.app : null,
    state: status.state,
  };
}

/** Starts the daemon if needed and returns what to load. */
export async function ensure(cli: string, timeout = ENSURE_TIMEOUT): Promise<Launch> {
  const output = await run(cli, ["daemon", "ensure", "--json"], timeout);
  let status: Status;
  try {
    status = parseStatus(output.stdout);
  } catch (e) {
    throw new Failure((e as Error).message, output.stderr);
  }
  let launch: Launch;
  try {
    launch = readCredential(status.state);
  } catch (e) {
    throw new Failure((e as Error).message, logTail(status.state, LOG_LINES));
  }
  if (status.app !== null) {
    let matches = false;
    try {
      matches = new URL(status.app).origin === launch.origin;
    } catch {}
    if (!matches) {
      throw new Failure(
        "The daemon's credential belongs to a different daemon. It may have just restarted; try again.",
        logTail(status.state, LOG_LINES),
      );
    }
  }
  launch.status = status;
  return launch;
}

/** Whether the credential file is private: owned by the user and readable
 *  by nobody else on Unix. The daemon restricts the file with an ACL on
 *  Windows; the app trusts the user profile's permissions there. */
function isPrivate(mode: number, uid: number): boolean {
  if (process.platform === "win32" || !process.getuid) return true;
  return (mode & 0o077) === 0 && uid === process.getuid();
}

/** Reads and checks the app credential. Only a private file naming a
 *  loopback launch URL is accepted, the same checks the daemon applies. */
export function readCredential(state: string): Launch {
  const path = join(state, CREDENTIAL_FILE);
  let info: ReturnType<typeof lstatSync>;
  try {
    info = lstatSync(path);
  } catch (e) {
    throw new Error(`The daemon's credential is missing (${path}): ${(e as Error).message}`);
  }
  if (!info.isFile() || info.size > MAX_CREDENTIAL_BYTES || !isPrivate(info.mode, info.uid)) {
    throw new Error(`The daemon's credential (${path}) must be a small file only you can read.`);
  }
  let data: string;
  try {
    data = readFileSync(path, "utf8");
  } catch (e) {
    throw new Error(`Cannot read the daemon's credential: ${(e as Error).message}`);
  }
  let url: unknown;
  try {
    url = (JSON.parse(data) as { url?: unknown }).url;
  } catch (e) {
    throw new Error(`The daemon's credential is unreadable: ${(e as Error).message}`);
  }
  if (typeof url !== "string") throw new Error("The daemon's credential is unreadable: no url");
  const { token } = launchUrl(url);
  return new Launch(url, token, { version: "", pid: 0, app: null, state });
}

/** Checks a launch URL: `http://127.0.0.1:<port>/#token=<capability>`. */
export function launchUrl(raw: string): { url: URL; token: string } {
  const invalid = () => new Error("The daemon's credential does not hold a loopback launch URL.");
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw invalid();
  }
  const loopback =
    url.protocol === "http:" &&
    url.hostname === "127.0.0.1" &&
    url.port !== "" &&
    url.username === "" &&
    url.password === "" &&
    url.pathname === "/" &&
    url.search === "" &&
    !raw.slice(0, raw.indexOf("#") === -1 ? undefined : raw.indexOf("#")).includes("?");
  if (!loopback || !url.hash.startsWith("#token=")) throw invalid();
  const token = url.hash.slice("#token=".length);
  if (!TOKEN.test(token)) throw invalid();
  return { url, token };
}

export type Health = "ok" | "replaced" | "down";

/** Asks the daemon's app transport for its health. A 401 means a newer
 *  daemon replaced the one the window attached to, and rewrote the
 *  credential. */
export function health(launch: Launch, timeout = HEALTH_TIMEOUT): Promise<Health> {
  const url = new URL(launch.url());
  return new Promise((resolve) => {
    let done = false;
    const finish = (result: Health) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      resolve(result);
    };
    const timer = setTimeout(() => {
      req.destroy();
      finish("down");
    }, timeout);
    const req = request(
      {
        host: url.hostname,
        port: Number(url.port),
        path: "/api/health",
        method: "GET",
        agent: false,
        headers: { Authorization: `Bearer ${launch.bearer()}`, Connection: "close" },
      },
      (response) => {
        let body = "";
        response.setEncoding("utf8");
        response.on("data", (chunk: string) => {
          if (body.length < 4096) body += chunk;
        });
        response.on("end", () => {
          if (response.statusCode === 200 && body.includes('"status":"ok"')) finish("ok");
          else if (response.statusCode === 401) finish("replaced");
          else finish("down");
        });
        response.on("error", () => finish("down"));
      },
    );
    req.on("error", () => finish("down"));
    req.end();
  });
}

/** The last lines of the daemon's logs, oldest first. */
export function logTail(state: string, max = LOG_LINES): string[] {
  const out: string[] = [];
  for (const name of LOG_FILES) {
    let text: string;
    try {
      text = readFileSync(join(state, name), "utf8");
    } catch {
      continue;
    }
    const all = text.split(/\r?\n/);
    if (all.at(-1) === "") all.pop();
    const tail = all.slice(Math.max(0, all.length - max));
    if (tail.length > 0) out.push(`── ${name}`, ...tail);
  }
  return out;
}

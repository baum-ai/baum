// Updates from GitHub Releases. The app checks for a newer release shortly
// after it starts and then once a day. A release build reads the latest full
// release's feed; a release candidate (a version such as 1.2.3-rc.1) reads
// the one the release workflow keeps on the `desktop-rc-updates`
// pre-release, which every release replaces, so it moves to the next release
// candidate or full release. electron-updater (updater-electron.ts) finds
// and downloads updates; every file it downloads must also carry a minisign
// signature from the release key (RELEASE_PUBLIC_KEY), made for the version
// being installed, or it is rejected before it is installed. The signature
// (`FILE.sig`) is read from the update's own release, never from the feed.
//
// The updater never interrupts: Settings shows its state (`update_state`),
// and the sidebar counts an available update on Settings. Where the app can
// replace itself (the macOS app, the Linux AppImage and the Windows
// installer), it downloads the update in the background; installing it
// (`install_update`) waits while the daemon runs generation jobs, then
// restarts the app (`cancel_update` stops that wait). A deb install, or any
// other copy the updater cannot replace, only says that a new version exists
// and links to its release page. So does a version whose installation
// failed, or whose download was rejected, before: it is remembered in
// desktop.json and never offered for installation again.
// `check_for_updates` checks at once. The page also shows a toast once per
// version and app launch; closing it (`dismiss_update_notice`) is remembered
// only in memory.
import { rmSync } from "node:fs";
import { basename, dirname, relative, isAbsolute, sep } from "node:path";
import { registerCommand } from "./commands.ts";
import {
  MinisignError,
  parsePublicKey,
  parseReleaseSignature,
  verifyFile,
  type PublicKey,
  type Signature,
} from "./minisign.ts";
import { loadSection, updateSection } from "./settings.ts";

/** Where the release pages live. */
const RELEASES = "https://github.com/baum-ai/baum/releases";
/** The feed of the newest full release. */
export const STABLE_FEED = `${RELEASES}/latest/download/`;
/** The feed of the newest release, full or candidate, that release
 *  candidates update from. */
export const CANDIDATE_FEED = `${RELEASES}/download/desktop-rc-updates/`;
/** Set to `0` to turn update checks off. */
export const SETTING = "BAUM_DESKTOP_UPDATES";
/** Test builds only (testOverrides): the feed to read instead of the
 *  channel's, and the minisign public key file, base64 encoded as
 *  RELEASE_PUBLIC_KEY is, to trust instead of the release key. */
export const TEST_FEED = "BAUM_DESKTOP_UPDATE_FEED";
export const TEST_PUBLIC_KEY = "BAUM_DESKTOP_UPDATE_PUBKEY";
/** The release key's minisign public key file, base64 encoded:
 *  `plugins.updater.pubkey` in tauri.conf.json. */
export const RELEASE_PUBLIC_KEY =
  "dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWduIHB1YmxpYyBrZXk6IEM0MkFDRjc3NEZDQzFENUYKUldSZkhjeFBkODhxeEh0eWRKWTdxbkEzVHJQM1k5NmVteGxJODdWT2JpRElRWXBhZlEzT0phZnYK";

const SECOND = 1000;
const MINUTE = 60 * SECOND;
/** Lets the app attach to the daemon before the first check. */
export const FIRST_CHECK_DELAY = 30 * SECOND;
export const CHECK_INTERVAL = 24 * 60 * MINUTE;
/** How often the app looks at the clock between checks. Wall-clock time is
 *  compared, so a computer that slept through the day checks when it wakes. */
export const CLOCK_TICK = 10 * MINUTE;
/** A download that receives nothing for this long is abandoned. A slow
 *  download that keeps receiving data is not cut off. */
export const STALL_TIMEOUT = 2 * MINUTE;
/** How often an install that waits for running jobs asks again. */
export const JOBS_POLL = 15 * SECOND;
/** In desktop.json: versions whose installation failed or whose download was
 *  rejected. */
const SECTION = "updates";
const MAX_SIGNATURE = 4096;

export type Phase = "idle" | "checking" | "downloading" | "ready" | "waiting" | "installing";

/** The updater as Settings and the sidebar show it. */
export interface View {
  /** This app's version. */
  version: string;
  /** Whether this build checks for updates at all. */
  enabled: boolean;
  /** Whether this copy can install `available` itself; otherwise Settings
   *  links to its release page. */
  installs: boolean;
  phase: Phase;
  /** The newer version the last check found. */
  available: string | null;
  /** Its release page. */
  page: string | null;
  /** When the last check succeeded, in seconds since the Unix epoch. */
  checked_at: number | null;
  /** The running jobs an install waits for. */
  jobs: number;
  /** Why the last check, download or install failed. */
  error: string | null;
  /** The version whose notice the person closed since the app started; the
   *  page tells them about an update once per version and launch. */
  noticed: string | null;
}

/** What electron-updater does for the updater (updater-electron.ts). */
export interface UpdaterBackend {
  /** The newer version the feed offers, or null. */
  check(): Promise<string | null>;
  /** Downloads the update the last check found. Calls `onData` whenever data
   *  arrives; `signal` abandons the download. */
  download(onData: () => void, signal: AbortSignal): Promise<{ version: string; file: string }>;
  /** Installs the downloaded update and restarts the app. Rejects if the
   *  installation fails; otherwise the app quits. */
  install(): Promise<void>;
}

export interface UpdaterDeps {
  /** This app's version. */
  version: string;
  /** False for an unpackaged (development) build, which never checks. */
  packaged: boolean;
  /** BAUM_DESKTOP_UPDATES. */
  setting: string | undefined;
  /** Whether this copy can replace itself (copyInstalls). */
  installs: boolean;
  /** desktop.json. */
  settingsPath: string;
  backend: UpdaterBackend;
  /** Fetches a signature next to the update (Electron's net.fetch). */
  fetch: (url: string, init: { signal: AbortSignal }) => Promise<Response>;
  /** How many generation jobs the daemon runs: `baum daemon status`, then
   *  `GET /api/jobs`; 0 when no daemon runs. */
  runningJobs: () => Promise<number>;
  /** Windows: `baum daemon stop` before the installer runs, and `baum daemon
   *  ensure` if it fails. Elsewhere leave them out. */
  stopDaemon?: () => Promise<void>;
  restartDaemon?: () => Promise<void>;
  /** Tests and test builds only: the public key file to trust instead of the
   *  release key's. */
  publicKey?: string;
  /** Test builds only: the feed the update and its signature come from
   *  (testOverrides). */
  testFeed?: string;
  log?: (message: string) => void;
}

/** Builds without a release version (local and snapshot builds, which are
 *  0.0.0) and unpackaged builds never check, and neither does an app
 *  started with `BAUM_DESKTOP_UPDATES=0`. */
export function enabled(version: string, packaged: boolean, setting: string | undefined): boolean {
  const off = ["0", "false", "off", "no"].includes(setting?.trim() ?? "");
  return packaged && version !== "0.0.0" && !off;
}

export function due(last: number, now: number): boolean {
  return now - last >= CHECK_INTERVAL;
}

/** The base URL electron-updater reads `latest-*.yml` and the update files
 *  from, and the updater their `.sig` files. */
export function feedUrl(version: string): string {
  // A pre-release part follows the first `-`, before any `+build` part.
  const release = version.split("+")[0] ?? "";
  return release.includes("-") ? CANDIDATE_FEED : STABLE_FEED;
}

/** Where the signature of an update file is: beside the file on the
 *  release of the version being installed (whatever the channel), or beside
 *  it in a test build's feed. */
export function signatureUrl(version: string, file: string, testFeed?: string): string {
  const name = `${encodeURIComponent(basename(file))}.sig`;
  if (testFeed !== undefined) return `${testFeed}${name}`;
  return `${RELEASES}/download/v${encodeURIComponent(version.replace(/^v+/, ""))}/${name}`;
}

/** A test build's version: a pre-release `test`, as in 1.2.3-test.1. The
 *  release workflow refuses to release such a version (release.yml), so no
 *  installed copy honours the test variables. */
const TEST_VERSION = /^\d+\.\d+\.\d+-test(\.[0-9A-Za-z-]+)*(\+[0-9A-Za-z.-]+)?$/;

export function isTestVersion(version: string): boolean {
  return TEST_VERSION.test(version);
}

/** In a test build (isTestVersion), BAUM_DESKTOP_UPDATE_FEED and
 *  BAUM_DESKTOP_UPDATE_PUBKEY point the updater at a local feed signed by a
 *  throwaway key, so an update can be installed end to end
 *  (scripts/bridge-test.sh). Any other version ignores them: a released app
 *  only ever reads its channel and trusts the release key. */
export function testOverrides(
  version: string,
  env: Record<string, string | undefined>,
): { feed: string; publicKey: string | undefined } | null {
  const feed = env[TEST_FEED];
  if (!isTestVersion(version) || !feed) return null;
  const key = env[TEST_PUBLIC_KEY];
  return {
    feed: feed.endsWith("/") ? feed : `${feed}/`,
    publicKey: key ? Buffer.from(key, "base64").toString("utf8") : undefined,
  };
}

export function releasePage(version: string): string {
  return `${RELEASES}/tag/v${version.replace(/^v+/, "")}`;
}

/** Where this copy runs from, for copyInstalls. */
export interface Copy {
  platform: NodeJS.Platform;
  execPath: string;
  env: Record<string, string | undefined>;
  /** Whether the user can create files in a directory. */
  writable: (dir: string) => boolean;
}

function inside(parent: string, path: string): boolean {
  const rel = relative(parent, path);
  return rel !== "" && !rel.startsWith("..") && !isAbsolute(rel);
}

/** Which of electron-updater's updaters reads the feed for this copy. The
 *  Linux copy is an AppImage when the AppImage runtime set APPIMAGE; any
 *  other Linux copy (the deb) uses the deb updater, which checks in a
 *  packaged app without further configuration, and only ever checks: it
 *  never installs (copyInstalls). electron-updater's own choice reads a
 *  `package-type` file the deb target may also leave in the AppImage, and
 *  its AppImage updater checks nothing outside an AppImage unless forced
 *  into development mode, so the app chooses itself. */
export type UpdaterKind = "mac" | "nsis" | "appimage" | "deb";

export function updaterKind(
  platform: NodeJS.Platform,
  env: Record<string, string | undefined>,
): UpdaterKind | null {
  switch (platform) {
    case "darwin":
      return "mac";
    case "win32":
      return "nsis";
    case "linux":
      return env.APPIMAGE ? "appimage" : "deb";
    default:
      return null;
  }
}

/** Whether the updater can replace this copy: the Windows install; an
 *  AppImage in a directory the user can write; a macOS app that is neither
 *  translocated nor on a read-only volume. A deb (any Linux copy that is not
 *  an AppImage) only announces updates. */
export function copyInstalls(copy: Copy): boolean {
  switch (copy.platform) {
    case "win32":
      return true;
    case "linux": {
      const { APPIMAGE: image, APPDIR: dir } = copy.env;
      if (!image || !dir || !inside(dir, copy.execPath)) return false;
      return copy.writable(dirname(image));
    }
    case "darwin": {
      const at = copy.execPath.lastIndexOf(`.app${sep}`);
      if (at < 0) return false;
      const bundle = copy.execPath.slice(0, at + 4);
      if (bundle.includes("/AppTranslocation/")) return false;
      return copy.writable(dirname(bundle));
    }
    default:
      return false;
  }
}

/** What the updater remembers between app starts. */
interface Remembered {
  failed_installs: string[];
  rejected_downloads: string[];
}

function strings(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((v): v is string => typeof v === "string") : [];
}

export function remembered(path: string): Remembered {
  const section = loadSection(path, SECTION);
  return {
    failed_installs: strings(section.failed_installs),
    rejected_downloads: strings(section.rejected_downloads),
  };
}

export function remember(path: string, list: keyof Remembered, version: string): void {
  updateSection(path, SECTION, (section) => {
    const versions = strings(section[list]);
    if (!versions.includes(version)) versions.push(version);
    section[list] = versions;
  });
}

type Request = "check" | "install";

interface Ready {
  version: string;
  file: string;
  signature: Signature;
}

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/** The updater's state, the commands that change it and its checks. */
export class Updater {
  private current: View;
  private request: Request | null = null;
  private cancel = false;
  private ready: Ready | null = null;
  private stopped = false;
  private readonly waiters = new Set<() => void>();
  private readonly key: PublicKey;
  private readonly log: (message: string) => void;

  constructor(private readonly deps: UpdaterDeps) {
    this.current = {
      version: deps.version,
      enabled: enabled(deps.version, deps.packaged, deps.setting),
      installs: deps.installs,
      phase: "idle",
      available: null,
      page: null,
      checked_at: null,
      jobs: 0,
      error: null,
      noticed: null,
    };
    this.key = parsePublicKey(
      deps.publicKey ?? Buffer.from(RELEASE_PUBLIC_KEY, "base64").toString("utf8"),
    );
    this.log = deps.log ?? ((m) => console.error(`baum desktop: ${m}`));
  }

  view(): View {
    return { ...this.current };
  }

  /** Asks for a check now. A check, download or install already under way
   *  answers for it. */
  requestCheck(): View {
    if (!this.current.enabled) throw new Error("This build does not check for updates.");
    if (this.current.phase === "idle" || this.current.phase === "ready") {
      this.request = "check";
      if (this.current.phase === "idle") this.current.phase = "checking";
      this.current.error = null;
      this.notify();
    }
    return this.view();
  }

  /** Asks to install the downloaded update. */
  requestInstall(): View {
    if (this.current.phase !== "ready" || !this.current.installs) {
      throw new Error("No update is ready to install.");
    }
    this.request = "install";
    this.cancel = false;
    this.current.phase = "waiting";
    this.current.jobs = 0;
    this.current.error = null;
    this.notify();
    return this.view();
  }

  /** Stops an install that waits for running jobs. */
  requestCancel(): View {
    if (this.current.phase === "waiting") {
      this.cancel = true;
      this.notify();
    }
    return this.view();
  }

  /** Remembers, until the app quits, that the person saw `version`'s notice. */
  dismissNotice(version: string): View {
    this.current.noticed = version;
    return this.view();
  }

  /** Checks about 30 seconds from now, then daily. */
  start(): void {
    if (!this.current.enabled) return;
    this.run().catch((e: unknown) => this.log(`update checks stopped: ${message(e)}`));
  }

  stop(): void {
    this.stopped = true;
    this.notify();
  }

  private notify(): void {
    for (const wake of [...this.waiters]) wake();
  }

  /** Waits up to `ms`, or until a request, a cancel or stop() wakes it. */
  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => {
      const wake = () => {
        clearTimeout(timer);
        this.waiters.delete(wake);
        resolve();
      };
      const timer = setTimeout(wake, ms);
      this.waiters.add(wake);
    });
  }

  /** Waits up to `ms` for a request and takes it. */
  private async nextRequest(ms: number): Promise<Request | null> {
    if (this.request === null && !this.stopped) await this.sleep(ms);
    const request = this.request;
    this.request = null;
    return request;
  }

  /** Waits up to `ms`; true once the wait for jobs was cancelled. */
  private async cancelled(ms: number): Promise<boolean> {
    if (!this.cancel) await this.sleep(ms);
    const cancelled = this.cancel;
    this.cancel = false;
    return cancelled;
  }

  /** Moves a waiting install to installing, unless it was cancelled. */
  private beginInstall(): boolean {
    if (this.cancel) {
      this.cancel = false;
      return false;
    }
    this.current.phase = "installing";
    this.current.jobs = 0;
    return true;
  }

  private fail(phase: Phase, error: string): void {
    this.log(error);
    this.current.phase = phase;
    this.current.jobs = 0;
    this.current.error = error;
  }

  private async run(): Promise<void> {
    // A check asked for from Settings starts the first one early.
    let request = await this.nextRequest(FIRST_CHECK_DELAY);
    while (!this.stopped) {
      if (request === "install") await this.install();
      else await this.check();
      const last = Date.now();
      request = null;
      while (request === null && !this.stopped && !due(last, Date.now())) {
        request = await this.nextRequest(CLOCK_TICK);
      }
    }
  }

  private async check(): Promise<void> {
    if (this.current.phase === "idle") this.current.phase = "checking";
    let version: string | null;
    try {
      version = await this.deps.backend.check();
    } catch (e) {
      const phase = this.current.phase === "ready" ? "ready" : "idle";
      this.fail(phase, `cannot check for updates: ${message(e)}`);
      return;
    }
    const now = Math.floor(Date.now() / 1000);
    if (version === null) {
      this.ready = null;
      Object.assign(this.current, {
        phase: "idle",
        available: null,
        page: null,
        checked_at: now,
        error: null,
      });
      return;
    }
    const before = remembered(this.deps.settingsPath);
    const failed = before.failed_installs.includes(version);
    const rejected = before.rejected_downloads.includes(version);
    const installs = !failed && !rejected && this.deps.installs;
    const already = this.ready?.version === version;
    Object.assign(this.current, {
      available: version,
      page: releasePage(version),
      checked_at: now,
      installs,
      error: failed
        ? `An earlier attempt to install baum ${version} failed; download it from its release page.`
        : rejected
          ? `The download of baum ${version} was not signed by the release key; download it from its release page.`
          : null,
      phase: !installs ? "idle" : already ? "ready" : "downloading",
    });
    if (!installs || already) return;
    try {
      this.ready = await this.download(version);
      this.current.phase = "ready";
    } catch (e) {
      if (e instanceof MinisignError) this.reject(version);
      this.fail("idle", `cannot download baum ${version}: ${message(e)}`);
    }
  }

  /** Downloads the update and its signature, abandoning both after
   *  STALL_TIMEOUT without data, and verifies the file. */
  private async download(version: string): Promise<Ready> {
    const abort = new AbortController();
    const stalled = () => abort.abort(new Error("no data for two minutes"));
    let timer = setTimeout(stalled, STALL_TIMEOUT);
    const onData = () => {
      clearTimeout(timer);
      timer = setTimeout(stalled, STALL_TIMEOUT);
    };
    try {
      const downloaded = await this.deps.backend.download(onData, abort.signal);
      if (downloaded.version !== version) {
        throw new Error(`downloaded ${downloaded.version} instead`);
      }
      onData();
      const response = await this.deps.fetch(
        signatureUrl(version, downloaded.file, this.deps.testFeed),
        { signal: abort.signal },
      );
      if (!response.ok) throw new Error(`its signature: HTTP ${response.status}`);
      const text = await response.text();
      if (text.length > MAX_SIGNATURE) throw new MinisignError("the signature is too large");
      const signature = parseReleaseSignature(text);
      try {
        await verifyFile(downloaded.file, signature, this.key, version);
      } catch (e) {
        rmSync(downloaded.file, { force: true });
        throw e;
      }
      return { version, file: downloaded.file, signature };
    } finally {
      clearTimeout(timer);
    }
  }

  /** From now on `version` is only announced. */
  private reject(version: string): void {
    try {
      remember(this.deps.settingsPath, "rejected_downloads", version);
    } catch (e) {
      this.log(`cannot record the rejected update: ${message(e)}`);
    }
    this.current.installs = false;
  }

  private async runningJobs(): Promise<number> {
    // The update itself never stops the daemon (outside Windows), so a daemon
    // that cannot be asked does not hold it up.
    try {
      return await this.deps.runningJobs();
    } catch (e) {
      this.log(message(e));
      return 0;
    }
  }

  /** Installs the downloaded update once no generation job runs, then
   *  restarts the app. */
  private async install(): Promise<void> {
    // A check that ran since the request may have shown the update as ready
    // again; from here the install is waiting, and can be cancelled.
    const ready = this.ready;
    this.ready = null;
    if (!ready) {
      this.fail("idle", "no update is ready to install");
      return;
    }
    this.current.phase = "waiting";
    for (;;) {
      const jobs = await this.runningJobs();
      let cancelled: boolean;
      if (jobs === 0) {
        cancelled = !this.beginInstall();
      } else {
        this.current.jobs = jobs;
        cancelled = await this.cancelled(JOBS_POLL);
      }
      if (cancelled) {
        this.ready = ready;
        this.current.phase = "ready";
        this.current.jobs = 0;
        return;
      }
      if (jobs === 0) break;
    }
    try {
      // The file waited in the cache; check it again before it replaces the app.
      await verifyFile(ready.file, ready.signature, this.key, ready.version);
    } catch (e) {
      if (e instanceof MinisignError) this.reject(ready.version);
      this.fail("idle", `cannot install baum ${ready.version}: ${message(e)}`);
      return;
    }
    // Windows: the installer closes the app and the updated app starts the
    // daemon, so stop it now rather than leave the old version running from
    // the app's copy of baum.exe. Stopping marks the daemon as stopped by the
    // user, so hooks leave it alone; the updated app's `baum daemon ensure`
    // starts it and clears that mark. Elsewhere nothing is stopped: the
    // running daemon keeps its binary until the updated app replaces it.
    await this.daemon(this.deps.stopDaemon, "cannot stop the daemon before updating");
    try {
      await this.deps.backend.install();
    } catch (e) {
      await this.daemon(this.deps.restartDaemon, "cannot start the daemon again");
      try {
        remember(this.deps.settingsPath, "failed_installs", ready.version);
      } catch (e) {
        this.log(`cannot record the failed update: ${message(e)}`);
      }
      this.current.installs = false;
      this.fail(
        "idle",
        `This copy of baum could not update itself to ${ready.version} (${message(e)}); download it from its release page.`,
      );
    }
  }

  private async daemon(run: (() => Promise<void>) | undefined, failure: string): Promise<void> {
    try {
      await run?.();
    } catch (e) {
      this.log(`${failure}: ${message(e)}`);
    }
  }
}

/** Creates the updater, registers its commands and starts its checks. */
export function registerUpdaterCommands(deps: UpdaterDeps): Updater {
  const updater = new Updater(deps);
  registerCommand("update_state", () => updater.view());
  registerCommand("check_for_updates", () => updater.requestCheck());
  registerCommand("install_update", () => updater.requestInstall());
  registerCommand("cancel_update", () => updater.requestCancel());
  registerCommand("dismiss_update_notice", (args) => {
    if (typeof args.version !== "string") throw new Error("version must be a string");
    return updater.dismissNotice(args.version);
  });
  updater.start();
  return updater;
}

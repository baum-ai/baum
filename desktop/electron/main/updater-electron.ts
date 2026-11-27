// The updater's backend over electron-updater: the generic provider reads
// `latest-mac.yml`, `latest-linux.yml` or `latest-linux-arm64.yml` from the
// feed (updater.ts) and downloads the file it names, checking its SHA-512.
// Nothing is downloaded or installed unless updater.ts asks, and only after
// it has verified the file's minisign signature.
import { accessSync, constants } from "node:fs";
import { app, net } from "electron";
import {
  AppImageUpdater,
  type AppUpdater,
  CancellationToken,
  DebUpdater,
  MacUpdater,
  NsisUpdater,
  type UpdateDownloadedEvent,
} from "electron-updater";
import {
  copyInstalls,
  feedUrl,
  registerUpdaterCommands,
  SETTING,
  testOverrides,
  type Updater,
  type UpdaterBackend,
  type UpdaterDeps,
  type UpdaterKind,
  updaterKind,
} from "./updater.ts";

function writable(dir: string): boolean {
  try {
    accessSync(dir, constants.W_OK);
    return true;
  } catch {
    return false;
  }
}

function create(kind: UpdaterKind): AppUpdater {
  switch (kind) {
    case "mac":
      return new MacUpdater();
    case "nsis":
      return new NsisUpdater();
    case "appimage":
      return new AppImageUpdater();
    case "deb":
      return new DebUpdater();
  }
}

/** What the backend needs besides electron-updater. */
export interface BackendOptions {
  version: string;
  kind: UpdaterKind | null;
  feed: string;
  /** The app is about to quit for the installer, which starts the updated
   *  app; `failed` undoes it when the installer could not start. */
  beforeInstall?: () => void;
  installFailed?: () => void;
}

export class ElectronUpdaterBackend implements UpdaterBackend {
  private instance: AppUpdater | null = null;

  constructor(private readonly options: BackendOptions) {}

  /** electron-updater, configured on first use: a build that never checks
   *  never loads it. Checks run only in packaged builds (updater.ts). */
  private updater(): AppUpdater | null {
    if (this.instance !== null || this.options.kind === null) return this.instance;
    const updater = create(this.options.kind);
    updater.autoDownload = false;
    updater.autoInstallOnAppQuit = false;
    updater.allowDowngrade = false;
    // Whole files only: the update's minisign signature covers the whole
    // file anyway, and range requests through downloads.usebaum.com would
    // each count as a download.
    updater.disableDifferentialDownload = true;
    updater.requestHeaders = { "User-Agent": `baum-desktop/${this.options.version}` };
    updater.setFeedURL({ provider: "generic", url: this.options.feed });
    updater.logger = {
      info: () => {},
      warn: (m: unknown) => console.warn(`baum desktop: updater: ${String(m)}`),
      error: (m: unknown) => console.error(`baum desktop: updater: ${String(m)}`),
    };
    // Errors also reject the call that caused them; without a listener an
    // "error" event would throw.
    updater.on("error", () => {});
    this.instance = updater;
    return updater;
  }

  async check(): Promise<string | null> {
    const updater = this.updater();
    if (updater === null) return null;
    const result = await updater.checkForUpdates();
    return result?.isUpdateAvailable ? result.updateInfo.version : null;
  }

  async download(
    onData: () => void,
    signal: AbortSignal,
  ): Promise<{ version: string; file: string }> {
    const updater = this.updater();
    if (updater === null) throw new Error("this copy does not download updates");
    const token = new CancellationToken();
    const abort = () => token.cancel();
    signal.addEventListener("abort", abort, { once: true });
    let downloaded: UpdateDownloadedEvent | null = null;
    const done = (event: UpdateDownloadedEvent) => {
      downloaded = event;
    };
    updater.on("download-progress", onData);
    updater.on("update-downloaded", done);
    try {
      await updater.downloadUpdate(token);
    } catch (e) {
      throw signal.aborted ? signal.reason : e;
    } finally {
      signal.removeEventListener("abort", abort);
      updater.off("download-progress", onData);
      updater.off("update-downloaded", done);
    }
    const event = downloaded as UpdateDownloadedEvent | null;
    if (!event) throw new Error("the download did not finish");
    return { version: event.version, file: event.downloadedFile };
  }

  /** Rejects with the installer's error. On success the app quits and the
   *  promise stays pending. */
  install(): Promise<void> {
    const updater = this.updater();
    if (updater === null) return Promise.reject(new Error("this copy does not install updates"));
    return new Promise((_resolve, reject) => {
      const fail = (e: unknown) => {
        this.options.installFailed?.();
        reject(e);
      };
      updater.once("error", fail);
      this.options.beforeInstall?.();
      try {
        // Silent: the Windows installer shows no pages. Then the updated app starts: the AppImage
        // updater starts it before this one quits, Squirrel.Mac and the
        // Windows installer once it has.
        updater.quitAndInstall(true, true);
      } catch (e) {
        updater.off("error", fail);
        fail(e);
      }
    });
  }
}

/** What the main process provides: the daemon's jobs and, on Windows,
 *  stopping and starting the daemon around the installer. */
export type ElectronUpdaterDeps = Pick<
  UpdaterDeps,
  "settingsPath" | "runningJobs" | "stopDaemon" | "restartDaemon"
>;

/** Starts the updater in the app, once it is ready, and registers its
 *  commands. */
export function startUpdater(deps: ElectronUpdaterDeps): Updater {
  const version = app.getVersion();
  const windows = process.platform === "win32";
  const test = testOverrides(version, process.env);
  if (test !== null) console.error(`baum desktop: test build: updates from ${test.feed}`);
  return registerUpdaterCommands({
    version,
    packaged: app.isPackaged,
    setting: process.env[SETTING],
    installs: copyInstalls({
      platform: process.platform,
      execPath: process.execPath,
      env: process.env,
      writable,
    }),
    settingsPath: deps.settingsPath,
    backend: new ElectronUpdaterBackend({
      version,
      kind: updaterKind(process.platform, process.env),
      feed: test?.feed ?? feedUrl(version),
      // The updated app must be able to take the single-instance lock while
      // this one is still quitting (the AppImage updater starts it first).
      beforeInstall: () => app.releaseSingleInstanceLock(),
      installFailed: () => {
        app.requestSingleInstanceLock();
      },
    }),
    publicKey: test?.publicKey,
    testFeed: test?.feed,
    fetch: (url, init) => net.fetch(url, init),
    runningJobs: deps.runningJobs,
    stopDaemon: windows ? deps.stopDaemon : undefined,
    restartDaemon: windows ? deps.restartDaemon : undefined,
  });
}

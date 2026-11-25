// The updater's backend over electron-updater: the generic provider reads
// `latest-mac.yml`, `latest-linux.yml` or `latest-linux-arm64.yml` from the
// feed (updater.ts) and downloads the file it names, checking its SHA-512.
// Nothing is downloaded or installed unless updater.ts asks, and only after
// it has verified the file's minisign signature.
import { accessSync, constants } from "node:fs";
import { app, net } from "electron";
import {
  type AppUpdater,
  autoUpdater,
  CancellationToken,
  type UpdateDownloadedEvent,
} from "electron-updater";
import {
  copyInstalls,
  feedUrl,
  registerUpdaterCommands,
  SETTING,
  type Updater,
  type UpdaterBackend,
  type UpdaterDeps,
} from "./updater.ts";

function writable(dir: string): boolean {
  try {
    accessSync(dir, constants.W_OK);
    return true;
  } catch {
    return false;
  }
}

export class ElectronUpdaterBackend implements UpdaterBackend {
  private configured = false;

  constructor(private readonly version: string) {}

  /** electron-updater, configured on first use: a build that never checks
   *  never loads it. */
  private updater(): AppUpdater {
    if (this.configured) return autoUpdater;
    this.configured = true;
    autoUpdater.autoDownload = false;
    autoUpdater.autoInstallOnAppQuit = false;
    // Otherwise electron-updater checks only from an AppImage or a package
    // that names its type, and a copy that only announces updates would
    // never hear of one. Checks still run only in packaged builds.
    autoUpdater.forceDevUpdateConfig = true;
    autoUpdater.setFeedURL({ provider: "generic", url: feedUrl(this.version) });
    autoUpdater.allowDowngrade = false;
    autoUpdater.logger = {
      info: () => {},
      warn: (m: unknown) => console.warn(`baum desktop: updater: ${String(m)}`),
      error: (m: unknown) => console.error(`baum desktop: updater: ${String(m)}`),
    };
    // Errors also reject the call that caused them; without a listener an
    // "error" event would throw.
    autoUpdater.on("error", () => {});
    return autoUpdater;
  }

  async check(): Promise<string | null> {
    const result = await this.updater().checkForUpdates();
    return result?.isUpdateAvailable ? result.updateInfo.version : null;
  }

  async download(
    onData: () => void,
    signal: AbortSignal,
  ): Promise<{ version: string; file: string }> {
    const updater = this.updater();
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
    return new Promise((_resolve, reject) => {
      updater.once("error", reject);
      try {
        // Silent: the Windows installer shows no pages (Tauri's "passive"
        // mode came closest); then the updated app starts.
        updater.quitAndInstall(true, true);
      } catch (e) {
        reject(e);
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
    backend: new ElectronUpdaterBackend(version),
    fetch: (url, init) => net.fetch(url, init),
    runningJobs: deps.runningJobs,
    stopDaemon: windows ? deps.stopDaemon : undefined,
    restartDaemon: windows ? deps.restartDaemon : undefined,
  });
}

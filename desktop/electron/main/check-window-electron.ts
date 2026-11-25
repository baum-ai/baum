// The tile check window's Electron side (the rules are in check-window.ts):
// a hidden, frameless window that cannot take focus and stays off the
// taskbar, with the main window's navigation rules and no new windows, and
// the `open_check_window` and `close_check_window` commands. It is never
// registered as one of the app's command windows, so the guard (bridge.ts)
// refuses every command its page calls.
//
// It runs in a session of its own, in memory (`PARTITION`), so Chromium
// never puts its frames in a process the main window uses. That session gets
// the default session's guards, and before each load a copy of the daemon's
// cookies from the default session, so its page is signed in as the main
// window's is; it is emptied when the window closes.
import { app, BrowserWindow, type Session, session, webContents } from "electron";
import {
  CheckWindow,
  type CheckWindowHost,
  checkPageUrl,
  HEIGHT,
  PARTITION,
  WIDTH,
} from "./check-window.ts";
import { registerCommand } from "./commands.ts";
import { CHECK_WINDOW_PAGE } from "./declaration.ts";
import { errorMessage, log } from "./log.ts";
import { decide } from "./navigation.ts";
import {
  guardFrameRequests,
  guardNavigation,
  guardPermissions,
  securePreferences,
} from "./windows.ts";

export interface CheckWindowDeps {
  /** The sandboxed preload of the app's windows. */
  preload: string;
  /** The main window, whose daemon page alone may open the check window. */
  main(): BrowserWindow | null;
  /** The attached daemon's origin, which the window's navigations are
   *  decided against as the main window's are. */
  daemon(): string | null;
  /** The origins whose pages may ask for notifications and clipboard
   *  writes, as in the default session. */
  isAppOrigin(origin: string): boolean;
  logRefusals: boolean;
}

/** The processes frames of `contents` run in. */
function framePids(contents: Electron.WebContents): number[] {
  try {
    return contents.mainFrame.framesInSubtree.map((frame) => frame.osProcessId);
  } catch {
    return [];
  }
}

/** Whether `pid` is still one of the app's renderer processes, so ending it
 *  can never hit a process that reused the number. */
function isRenderer(pid: number): boolean {
  return app.getAppMetrics().some((metric) => metric.pid === pid && metric.type === "Tab");
}

/** The processes only `window`'s frames run in. */
function ownProcesses(window: BrowserWindow): number[] {
  const own = new Set(framePids(window.webContents));
  for (const other of webContents.getAllWebContents()) {
    if (other === window.webContents || other.isDestroyed()) continue;
    for (const pid of framePids(other)) own.delete(pid);
  }
  own.delete(process.pid);
  own.delete(0);
  return [...own];
}

/** Copies the cookies the default session holds for `origin`'s host into
 *  `target`, with their attributes. A cookie's path can be narrower than
 *  the origin's root (the daemon scopes its session to `/s/<namespace>/`),
 *  so they are matched by host, as cookies ignore the port. */
export async function copyCookies(from: Session, target: Session, origin: string): Promise<void> {
  const host = new URL(origin).hostname;
  const cookies = (await from.cookies.get({})).filter(
    (cookie) => (cookie.domain ?? "").replace(/^\./, "") === host,
  );
  for (const cookie of cookies) {
    await target.cookies.set({
      url: `${origin}${cookie.path ?? "/"}`,
      name: cookie.name,
      value: cookie.value,
      ...(cookie.hostOnly ? {} : { domain: cookie.domain }),
      path: cookie.path,
      secure: cookie.secure,
      httpOnly: cookie.httpOnly,
      ...(cookie.session ? {} : { expirationDate: cookie.expirationDate }),
      sameSite: cookie.sameSite,
    });
  }
}

export function electronHost(deps: CheckWindowDeps): CheckWindowHost<BrowserWindow> {
  let guarded: Session | null = null;
  /** The check window's session, with the default session's guards. */
  const checkSession = (): Session => {
    if (guarded) return guarded;
    const own = session.fromPartition(PARTITION);
    guardPermissions(own, deps.isAppOrigin);
    guardFrameRequests(own);
    guarded = own;
    return own;
  };
  return {
    async create(url) {
      const own = checkSession();
      await copyCookies(session.defaultSession, own, new URL(url).origin);
      const window = new BrowserWindow({
        title: "baum tile check",
        width: WIDTH,
        height: HEIGHT,
        // Never shown. Chromium still lays out and runs a hidden window's
        // page, and without throttling its timers run at full rate.
        show: false,
        frame: false,
        focusable: false,
        skipTaskbar: true,
        // The page reads the main window's declaration, except that it
        // opens no links: it may call no command.
        webPreferences: {
          ...securePreferences(deps.preload, CHECK_WINDOW_PAGE),
          backgroundThrottling: false,
          session: own,
        },
      });
      // The main window's rules, without the setup screen. Nothing in a
      // window the person cannot see opens the browser.
      guardNavigation(window.webContents, {
        decide: (to) => decide(to, deps.daemon(), false),
        newWindows: false,
        logRefusals: deps.logRefusals,
      });
      window.loadURL(url).catch((e) => log(`cannot load the check window: ${errorMessage(e)}`));
      return window;
    },
    isGone: (window) => window.isDestroyed(),
    ownProcesses,
    askToClose: (window) => window.close(),
    destroy(window) {
      const contents = window.webContents;
      const main = contents.getOSProcessId();
      // Never a process another window's page also runs in.
      if (ownProcesses(window).includes(main)) contents.forcefullyCrashRenderer();
      window.destroy();
    },
    endProcess(pid) {
      if (!isRenderer(pid)) return;
      try {
        process.kill(pid, "SIGKILL");
      } catch (e) {
        log(`cannot end a process of the check window: ${errorMessage(e)}`);
      }
    },
    async clearStorage() {
      try {
        await checkSession().clearStorageData();
      } catch (e) {
        log(`cannot clear the check window's storage: ${errorMessage(e)}`);
      }
    },
    sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  };
}

/** Creates the check window's rules and registers its commands. */
export function registerCheckWindow(deps: CheckWindowDeps): CheckWindow<BrowserWindow> {
  const checkWindow = new CheckWindow(electronHost(deps));
  // Opens the window at the check page of the daemon page the main window
  // shows, or does nothing when it is open.
  registerCommand("open_check_window", async (_args, context) => {
    const main = deps.main();
    if (main === null || main.isDestroyed() || context.window !== main) {
      throw new Error("Only the main window can open the check window.");
    }
    const daemon = deps.daemon();
    if (daemon === null) throw new Error("The app is not attached to the baum daemon.");
    const url = checkPageUrl(main.webContents.getURL(), daemon);
    if (url === null) throw new Error("The main window does not show the baum daemon's page.");
    await checkWindow.open(url);
    return null;
  });
  // Closes the window, ending its processes, or does nothing when it is not
  // open.
  registerCommand("close_check_window", async (_args, context) => {
    if (context.window === deps.main()) await checkWindow.close();
    return null;
  });
  return checkWindow;
}

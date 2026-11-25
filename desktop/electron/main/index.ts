// The baum desktop app's Electron shell: a window onto the user's
// `baum daemon` (src/main.rs in the Tauri shell). The shell adds no review
// features; the daemon serves the application. The window starts on a
// bundled screen (setup on first run, else the connecting screen), attaches
// to the daemon (starting it if needed) and loads it, and returns to the
// connecting screen with the daemon's recent log when the daemon cannot be
// reached. Closing the window hides it; quitting leaves the daemon running.
//
// The rest of the Tauri shell is ported in parallel and plugs in where the
// comments marked "wired in N/7" say.
import { writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { app, BrowserWindow, ipcMain, protocol, session, shell } from "electron";
import { type AttachHooks, Attacher, HealthPause } from "./attach.ts";
import { type BridgeState, DEVELOPMENT_SETUP_ORIGIN, installBridge } from "./bridge.ts";
import { registerCommand } from "./commands.ts";
import * as daemon from "./daemon.ts";
import { SHELL } from "./declaration.ts";
import { developmentDir, frontend, isSetupPage, SETUP_PAGE } from "./development.ts";
import { startsHidden, claimSingleInstance, hideOnClose } from "./lifecycle.ts";
import { errorMessage, log } from "./log.ts";
import {
  BUNDLED_ORIGIN,
  BUNDLED_SCREEN,
  bundledUrl,
  decide,
  isBundled,
  schemeAndHost,
} from "./navigation.ts";
import { configDir, IDENTIFIER, localDataDir, sessionDataDir } from "./paths.ts";
import { registerAppScheme, serveAppScheme } from "./protocol.ts";
import { FILE as DESKTOP_JSON } from "./settings.ts";
import { initialPage, onboarded, SetupScreen } from "./setup.ts";
import {
  guardFrameRequests,
  guardNavigation,
  guardPermissions,
  kindOf,
  registerAppWindow,
  securePreferences,
  setOpenExternal,
  windowOf,
} from "./windows.ts";

// Before the app is ready: the identity and the directories Tauri used, so
// desktop.json, the stable copy of baum and start at login stay where they
// are; Chromium's own files go in a folder of their own.
const platform = { platform: process.platform, env: process.env, home: homedir() };
const config = configDir(platform);
app.setName(IDENTIFIER);
app.setPath("userData", config);
app.setPath("sessionData", sessionDataDir(config));
if (process.platform === "win32") app.setAppUserModelId(IDENTIFIER);
registerAppScheme(protocol);

const development = developmentDir(process.env, app.isPackaged);
// Start at login runs the app with this flag: the app attaches to the
// daemon (starting it) without showing the window.
const hidden = startsHidden(process.argv);
const setup = new SetupScreen();
let main: BrowserWindow | null = null;

const cli: daemon.Cli = {
  override: process.env.BAUM_DESKTOP_BAUM || undefined,
  exe: process.execPath,
  platform: process.platform,
  bundled: daemon.bundledCliPath({
    platform: process.platform,
    arch: process.arch,
    packaged: app.isPackaged,
    resources: process.resourcesPath,
    appPath: app.getAppPath(),
  }),
  // wired in 3/7: install. Where the app keeps a stable copy of baum (an
  // AppImage, Windows: `<localDataDir>/bin/baum`), install.rs's
  // `refresh_copy` refreshes it and returns its path here.
  stable: undefined,
};
/** Where the stable copy of baum goes (3/7: install). */
export const LOCAL_DATA_DIR = localDataDir(platform);

/** The pause between health checks. wired in 3/7: events. The event stream
 *  calls `healthPause.setStreamUp(up)` as it opens and closes, and
 *  `healthPause.wake()` to check at once. */
export const healthPause = new HealthPause();

/** What the rest of the app does as the attachment changes. wired in 3/7:
 *  tray (`connecting`, `attached`, `detached`) and notifications
 *  (`pageAttached`); 4/7: the check window closes on `connecting` and
 *  `detached`. */
export const attachHooks: AttachHooks = {};

const attacher = new Attacher({
  misplaced: () => daemon.misplaced(cli),
  cliPath: () => daemon.cliPath(cli),
  ensure: (path) => daemon.ensure(path),
  health: (launch) => daemon.health(launch),
  readCredential: daemon.readCredential,
  logTail: daemon.logTail,
  frontend: (launch) => frontend(launch, development),
  setupOpen: () => setup.open,
  load: (url) => {
    // The error names the URL; the log redacts its token.
    main?.loadURL(url).catch((e) => log(`cannot load the baum daemon: ${errorMessage(e)}`));
  },
  showScreen,
  pause: () => healthPause.pause(),
  sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  log,
  hooks: attachHooks,
});

/** Returns the window to the bundled screen unless it is already there. The
 *  setup screen stays while it shows: attaching waits until setup ends. */
function showScreen(): void {
  if (setup.open || !main || main.isDestroyed()) return;
  const current = main.webContents.getURL();
  // No URL yet: the first screen, always a bundled one, is still loading.
  if (current === "") return;
  let bundled = false;
  try {
    bundled = isBundled(new URL(current));
  } catch {}
  if (!bundled) {
    main.loadURL(BUNDLED_SCREEN).catch((e) => log(`cannot show the screen: ${errorMessage(e)}`));
  }
}

/** The origins whose pages may ask for notifications and clipboard writes. */
function isAppOrigin(origin: string): boolean {
  return (
    origin === BUNDLED_ORIGIN ||
    origin === attacher.daemon() ||
    (development !== null && origin === DEVELOPMENT_SETUP_ORIGIN)
  );
}

function start(): void {
  serveAppScheme(protocol, join(app.getAppPath(), "dist"), () => setup.open);
  guardPermissions(session.defaultSession, isAppOrigin);
  guardFrameRequests(session.defaultSession);
  setOpenExternal((url) => shell.openExternal(url));
  // Any web contents the app did not set up itself opens no window and
  // attaches no webview; the app's own windows replace the handler.
  app.on("web-contents-created", (_event, contents) => {
    contents.setWindowOpenHandler(() => ({ action: "deny" }));
    contents.on("will-attach-webview", (event) => event.preventDefault());
  });
  const state: BridgeState = {
    kindOf,
    windowOf,
    daemon: () => attacher.daemon(),
    setupOpen: () => setup.open,
    development: development !== null,
  };
  installBridge(ipcMain, state);
  // The connecting screen polls `connection`; its Retry and Restart buttons
  // call `reconnect`, the only place a failed daemon is started again.
  registerCommand("connection", () => attacher.connection());
  registerCommand("reconnect", () => {
    void attacher.connect();
    return null;
  });
  // wired in 3/7: onboarding (setup commands, finish_setup closes
  // `setup.open` and calls `attacher.connect()`), links (`open_link` through
  // windows.openOutside), zoom, theme (and their scripts in the main
  // window), tile_files, notifications, autostart, menu_entry, the tray and
  // its panel (registerAppWindow(panel, "tray-panel"), guardNavigation with
  // navigation.isTrayPanel); 4/7: the check window; later: the updater.

  const page = initialPage(onboarded(join(config, DESKTOP_JSON)), daemon.misplaced(cli) !== null);
  setup.open = page === "/setup.html";
  const url = setup.open && development !== null ? SETUP_PAGE : bundledUrl(page);

  main = new BrowserWindow({
    title: "baum",
    width: 1280,
    height: 860,
    minWidth: 720,
    minHeight: 480,
    show: !hidden,
    webPreferences: securePreferences(join(__dirname, "preload.cjs"), SHELL),
  });
  registerAppWindow(main, "main");
  guardNavigation(main.webContents, {
    decide: (to) =>
      setup.open && isSetupPage(to, development)
        ? "allow"
        : decide(to, attacher.daemon(), setup.open),
    newWindows: true,
    logRefusals: !app.isPackaged,
  });
  hideOnClose(main);
  screenshots(main);
  main.loadURL(url).catch((e) => log(`cannot show the first screen: ${errorMessage(e)}`));
  // wired in 3/7: tray. A hidden start shows the window after all if no
  // tray appears.
  void attacher.connect();
}

/** Development only: `BAUM_DESKTOP_SCREENSHOT=<file.png>` saves what the
 *  main window shows a few seconds after each page load, for checks that
 *  run the app under a virtual display. */
function screenshots(window: BrowserWindow): void {
  const file = process.env.BAUM_DESKTOP_SCREENSHOT;
  if (!file || app.isPackaged) return;
  let timer: NodeJS.Timeout | undefined;
  window.webContents.on("did-stop-loading", () => {
    clearTimeout(timer);
    timer = setTimeout(async () => {
      if (window.isDestroyed()) return;
      const image = await window.webContents.capturePage();
      writeFileSync(file, image.toPNG());
      log(`saved a screenshot of ${schemeAndHost(window.webContents.getURL())} to ${file}`);
    }, 4000);
  });
}

if (!claimSingleInstance(app, () => main)) {
  // Another instance runs: it shows its window, and this one quits.
  app.quit();
} else {
  app.whenReady().then(start, (e) => log(`cannot start: ${errorMessage(e)}`));
}

// The baum desktop app's Electron shell: a window onto the user's
// `baum daemon` (src/main.rs in the Tauri shell). The shell adds no review
// features; the daemon serves the application. The window starts on a
// bundled screen (setup on first run, else the connecting screen), attaches
// to the daemon (starting it if needed) and loads it, and returns to the
// connecting screen with the daemon's recent log when the daemon cannot be
// reached. Closing the window hides it into the tray; quitting leaves the
// daemon running.
//
// `start` sets the app up in the order main.rs's `setup` did: notifications
// before the tray, whose menus hold their toggles; zoom and theme; the tray,
// its menus and start at login; the applications menu entry; setup, which
// decides the first page and keeps the stable copy of baum; the main window
// and the tray panel; then attaching.
import { writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import {
  app,
  BrowserWindow,
  dialog,
  ipcMain,
  Menu,
  nativeTheme,
  protocol,
  session,
  shell,
} from "electron";
import { THEME_CHANNEL } from "../shared/contract.ts";
import { type AttachHooks, Attacher, HealthPause } from "./attach.ts";
import { createAutostart } from "./autostart.ts";
import { type BridgeState, DEVELOPMENT_SETUP_ORIGIN, installBridge } from "./bridge.ts";
import { DISABLED_FEATURES } from "./check-window.ts";
import { registerCheckWindow } from "./check-window-electron.ts";
import * as client from "./client.ts";
import { registerCommand } from "./commands.ts";
import * as daemon from "./daemon.ts";
import { appUrlOf, clientLaunch, EventFeed, noticeRoute, trayDaemon } from "./daemon-api.ts";
import { SHELL } from "./declaration.ts";
import { developmentDir, frontend, isSetupPage, setupPage } from "./development.ts";
import { EventStreamReader } from "./events.ts";
import {
  claimSingleInstance,
  hideOnClose,
  lifecycleHooks,
  showWindow,
  startsHidden,
} from "./lifecycle.ts";
import { LinkOpener, registerLinkCommands } from "./links.ts";
import { errorMessage, log } from "./log.ts";
import { refresh as refreshMenuEntry } from "./menu-entry.ts";
import {
  BUNDLED_ORIGIN,
  BUNDLED_SCREEN,
  bundledUrl,
  decide,
  isBundled,
  isTrayPanel,
  schemeAndHost,
} from "./navigation.ts";
import { ElectronPlatform } from "./notifications/electron.ts";
import { isMenuId, NotificationService } from "./notifications/index.ts";
import * as onboarding from "./onboarding.ts";
import { createPanel, type PanelWindow, registerPanel } from "./panel-electron.ts";
import { configDir, IDENTIFIER, localDataDir, sessionDataDir } from "./paths.ts";
import { registerAppScheme, serveAppScheme } from "./protocol.ts";
import { Quitter, STOP_TIMEOUT } from "./quit.ts";
import type { AppProcess } from "./run-baum.ts";
import { FILE as DESKTOP_JSON } from "./settings.ts";
import { registerThemeCommands, ThemeStore, themeBootstrap } from "./theme.ts";
import { registerTileFileCommands } from "./tile-files.ts";
import { closeAction, type TrayController } from "./tray.ts";
import { startTray } from "./tray-electron.ts";
import { hasWindowMenu, trayNotifications, windowMenuTemplate } from "./window-menu.ts";
import {
  guardFrameRequests,
  guardNavigation,
  guardPermissions,
  kindOf,
  registerAppWindow,
  securePreferences,
  setLinkOpener,
  windowOf,
} from "./windows.ts";
import { registerZoomCommands, ZoomStore } from "./zoom.ts";
import { applyZoom, installZoom } from "./zoom-electron.ts";

// Before the app is ready: the identity and the directories Tauri used, so
// desktop.json, the stable copy of baum and start at login stay where they
// are; Chromium's own files go in a folder of their own.
const platform = { platform: process.platform, env: process.env, home: homedir() };
const config = configDir(platform);
const desktopJson = join(config, DESKTOP_JSON);
app.setName(IDENTIFIER);
app.setPath("userData", config);
app.setPath("sessionData", sessionDataDir(config));
// Before the first notification, or Windows toasts carry no app identity.
if (process.platform === "win32") app.setAppUserModelId(IDENTIFIER);
registerAppScheme(protocol);
// Tile frames run in their page's renderer process, so the tile check
// window's tiles run apart from the main window's (check-window.ts).
app.commandLine.appendSwitch("disable-features", DISABLED_FEATURES);

const development = developmentDir(process.env, app.isPackaged);
// Start at login runs the app with this flag: the app attaches to the
// daemon (starting it) without showing the window.
const hidden = startsHidden(process.argv);
const thisApp: AppProcess = {
  platform: process.platform,
  env: process.env,
  execPath: process.execPath,
};
/** Where the stable copy of baum and the menu entry's icon go. */
const LOCAL_DATA_DIR = localDataDir(platform);
let main: BrowserWindow | null = null;
/** The daemon the app is attached to, whether or not the window shows it. */
let attached: daemon.Launch | null = null;

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
  // Set once setup has started: the stable copy where the app keeps one.
  stable: undefined,
};

/** The pause between health checks: 30 seconds while the event stream is
 *  open, 5 otherwise. */
const healthPause = new HealthPause();

/** What the rest of the app does as the attachment changes; set in `start`.
 *  The check window closes on `connecting` and `detached`. */
const attachHooks: AttachHooks = {};

/** First-run setup and "Command Line and Hooks…": the first page, the setup
 *  screen's commands, and the stable copy of baum every baum the app runs
 *  is. */
const setup = onboarding.registerSetupCommands<client.Launch>({
  app: thisApp,
  home: homedir(),
  settingsFile: desktopJson,
  localDataDir: LOCAL_DATA_DIR,
  bundledCli: cli.bundled,
  misplaced: () => daemon.misplaced(cli),
  launch: () => (attached === null ? null : clientLaunch(attached)),
  client: { request: client.request, catchUp: client.catchUp },
  chooseFolder: async (title) => {
    const options: Electron.OpenDialogOptions = {
      title,
      properties: ["openDirectory", "createDirectory"],
    };
    const chosen = main
      ? await dialog.showOpenDialog(main, options)
      : await dialog.showOpenDialog(options);
    return chosen.canceled ? null : (chosen.filePaths[0] ?? null);
  },
  openExternal: (url) => {
    shell.openExternal(url).catch((e) => log(`cannot open the browser: ${errorMessage(e)}`));
  },
  // finish_setup: back to the connecting screen, and attach again, which now
  // opens the daemon's inbox.
  leaveSetup: () => {
    main?.loadURL(BUNDLED_SCREEN).catch((e) => log(`cannot leave setup: ${errorMessage(e)}`));
    void attacher.connect();
  },
});

const attacher = new Attacher({
  misplaced: () => daemon.misplaced(cli),
  cliPath: () => daemon.cliPath(cli),
  ensure: (path) => daemon.ensure(path),
  health: (launch) => daemon.health(launch),
  readCredential: daemon.readCredential,
  logTail: daemon.logTail,
  frontend: (launch) => frontend(launch, development),
  setupOpen: () => setup.isOpen(),
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
  if (setup.isOpen() || !main || main.isDestroyed()) return;
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

/** Where the setup screen loads from: Vite's copy under `desktop::dev`. */
function setupUrl(): string {
  return setupPage(development) ?? bundledUrl(`/${onboarding.SETUP_PAGE}`);
}

/** "Command Line and Hooks…": reopens the setup screen in the window. */
function showSetup(): void {
  if (!main || main.isDestroyed()) return;
  setup.markOpen();
  showWindow(main);
  main.loadURL(setupUrl()).catch((e) => log(`cannot show setup: ${errorMessage(e)}`));
}

/** The origins whose pages may ask for notifications and clipboard writes. */
function isAppOrigin(origin: string): boolean {
  return (
    origin === BUNDLED_ORIGIN ||
    origin === attacher.daemon() ||
    (development !== null && origin === DEVELOPMENT_SETUP_ORIGIN)
  );
}

/** A non-blocking error dialog (lifecycle.rs's `message`). */
function message(title: string, text: string): void {
  const options: Electron.MessageBoxOptions = {
    type: "error",
    title,
    message: title,
    detail: text,
  };
  const shown = main ? dialog.showMessageBox(main, options) : dialog.showMessageBox(options);
  shown.catch((e) => log(`cannot show a message: ${errorMessage(e)}`));
}

/** A warning with `yes` and Cancel; resolves whether the person chose `yes`. */
async function confirm(title: string, text: string, yes: string): Promise<boolean> {
  const options: Electron.MessageBoxOptions = {
    type: "warning",
    title,
    message: title,
    detail: text,
    buttons: [yes, "Cancel"],
    defaultId: 0,
    cancelId: 1,
  };
  const answer = main
    ? await dialog.showMessageBox(main, options)
    : await dialog.showMessageBox(options);
  return answer.response === 0;
}

function start(): void {
  serveAppScheme(protocol, join(app.getAppPath(), "dist"), () => setup.isOpen());
  guardPermissions(session.defaultSession, isAppOrigin);
  guardFrameRequests(session.defaultSession);
  // `open_link` and every new-window request share this one opener.
  const links = new LinkOpener({
    openExternal: (url) => {
      if (process.env.BAUM_DESKTOP_LOG_LINKS && !app.isPackaged) {
        // Development checks under a virtual display have no browser.
        log(`would open ${schemeAndHost(url)} in the browser`);
        return;
      }
      return shell.openExternal(url);
    },
    debug: !app.isPackaged,
  });
  setLinkOpener(links);
  registerLinkCommands(links);
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
    setupOpen: () => setup.isOpen(),
    development: development !== null,
    logCalls: !app.isPackaged && Boolean(process.env.BAUM_DESKTOP_LOG_COMMANDS),
  };
  installBridge(ipcMain, state);
  // The connecting screen polls `connection`; its Retry and Restart buttons
  // call `reconnect`, the only place a failed daemon is started again.
  registerCommand("connection", () => attacher.connection());
  registerCommand("reconnect", () => {
    void attacher.connect();
    return null;
  });
  // The tile check window: its page shows a daemon the app may be leaving,
  // so it closes whenever the app starts attaching again or detaches, and
  // whenever the main window starts loading another page.
  const checkWindow = registerCheckWindow({
    preload: join(__dirname, "preload.cjs"),
    main: () => main,
    daemon: () => attacher.daemon(),
    logRefusals: !app.isPackaged,
  });
  const closeCheckWindow = () => {
    checkWindow.closeSoon().catch((e) => log(`cannot close the check window: ${errorMessage(e)}`));
  };
  registerTileFileCommands({
    chooseSavePath: async ({ title, defaultName, filters }) => {
      const options: Electron.SaveDialogOptions = { title, defaultPath: defaultName, filters };
      const chosen = main
        ? await dialog.showSaveDialog(main, options)
        : await dialog.showSaveDialog(options);
      return chosen.canceled || !chosen.filePath ? null : chosen.filePath;
    },
  });

  // Notifications, before the tray, whose menus hold their toggles. A click
  // opens its page where the window shows the app.
  const notifications = new NotificationService({
    configDir: config,
    platform: new ElectronPlatform(),
    client: { appUrl: () => appUrlOf(attached === null ? null : clientLaunch(attached)) },
    openInWindow: (path) => {
      const origin = attacher.daemon();
      if (!main || main.isDestroyed() || origin === null) return;
      showWindow(main);
      // The setup screen stays until the person leaves it.
      if (setup.isOpen()) return;
      main
        .loadURL(`${origin}${path}`)
        .catch((e) => log(`cannot open the page: ${errorMessage(e)}`));
    },
  });

  // Zoom and theme. The preload asks for the theme before the first paint
  // of every page in the app's windows; any other web contents gets nothing.
  const zoom = new ZoomStore(desktopJson);
  registerZoomCommands({
    store: zoom,
    apply: (window, level) => {
      if (window instanceof BrowserWindow) applyZoom(window.webContents, level);
    },
  });
  const themes = new ThemeStore(desktopJson);
  registerThemeCommands(themes);
  ipcMain.on(THEME_CHANNEL, (event) => {
    event.returnValue =
      kindOf(event.sender) && event.senderFrame?.parent === null
        ? themeBootstrap(themes.get(), nativeTheme.shouldUseDarkColors)
        : null;
  });

  // The tray, its menus and start at login. Start at login is rewritten so
  // it names this executable.
  const autostart = createAutostart({
    platform: process.platform,
    env: process.env,
    home: homedir(),
    exe: process.execPath,
    isPackaged: app.isPackaged,
    misplaced: daemon.misplaced(cli) !== null,
  });
  autostart.refresh();
  const quitter = new Quitter({
    jobCounts: () => tray.jobCounts(),
    confirm,
    misplaced: () => daemon.misplaced(cli),
    stopDaemon: async () => {
      await daemon.run(await daemon.cliPath(cli), ["daemon", "stop"], STOP_TIMEOUT);
    },
    exit: () => app.quit(),
  });
  // The window's menu bar (macOS only), built again from the saved settings
  // after each toggle.
  const windowMenu = (): void => {
    if (!hasWindowMenu(process.platform)) return Menu.setApplicationMenu(null);
    const template = windowMenuTemplate({
      login: autostart.enabled(),
      notifications: notifications.menu(),
      onClick: (id) => tray.onMenu(id),
    });
    Menu.setApplicationMenu(Menu.buildFromTemplate(template));
  };
  let panel: PanelWindow | undefined;
  const tray: TrayController = startTray({
    host: {
      showWindow: () => showWindow(main),
      openRoute: (route) => notifications.open(noticeRoute(route)),
      showSetup,
      quit: () => quitter.quit(),
      quitAndStop: () => void quitter.quitAndStop(),
      message,
      openUrl: (url) => shell.openExternal(url),
      isNotificationId: isMenuId,
      onNotificationMenu: (id) => {
        notifications.onMenu(id);
        windowMenu();
        tray.refresh();
      },
      notificationsSubmenu: () => trayNotifications(notifications.menu()),
      detachNotifications: () => notifications.detach(),
      // Notices reach the notifications from the event stream itself.
      handleEvent: () => {},
      autostart: {
        enabled: () => autostart.enabled(),
        set: (on) => {
          try {
            autostart.set(on);
          } finally {
            windowMenu();
          }
        },
      },
    },
    // macOS and Windows: a left click opens the panel.
    onClick: (icon) => panel?.toggle(icon),
  });
  windowMenu();

  // The one event stream: it wakes the tray, carries the notices, and while
  // it is open the daemon is checked every 30 seconds instead of every 5.
  const feed = new EventFeed();
  const events = new EventStreamReader({
    onOpen: () => {
      if (!app.isPackaged) log("the event stream is open; checking the daemon every 30 s");
      feed.opened();
      healthPause.setStreamUp(true);
    },
    onClosed: () => {
      if (!app.isPackaged) log("the event stream closed; checking the daemon every 5 s");
      feed.closed();
      healthPause.setStreamUp(false);
    },
    onChange: (event) => feed.change(event),
    onNotice: (_endpoint, event) => {
      notifications
        .handleNotice(event.data)
        .catch((e) => log(`cannot show a notification: ${errorMessage(e)}`));
    },
  });
  attachHooks.connecting = () => {
    closeCheckWindow();
    attached = null;
    events.detach();
    tray.connecting();
  };
  attachHooks.attached = (launch) => {
    attached = launch;
    const forClient = clientLaunch(launch);
    events.attach(forClient);
    tray.attached(trayDaemon(forClient, feed));
  };
  attachHooks.pageAttached = (_launch, page) => notifications.attach(page);
  attachHooks.detached = () => {
    closeCheckWindow();
    attached = null;
    events.detach();
    tray.detached();
  };
  app.on("will-quit", () => {
    events.close();
    tray.stop();
  });

  // The AppImage's applications menu entry.
  if (process.platform === "linux") {
    refreshMenuEntry({
      isPackaged: app.isPackaged,
      env: process.env,
      home: homedir(),
      exe: process.execPath,
      dataDir: LOCAL_DATA_DIR,
    });
  }

  // Setup decides the first page, and from now on the app runs its stable
  // copy of baum where it keeps one (refreshed from the bundled one first).
  const page = setup.start();
  if (setup.managedCopy() !== null) cli.stable = async () => setup.cliPath();
  const url = page === onboarding.SETUP_PAGE ? setupUrl() : bundledUrl(`/${page}`);

  main = new BrowserWindow({
    title: "baum",
    width: 1280,
    height: 860,
    minWidth: 720,
    minHeight: 480,
    show: !hidden,
    webPreferences: securePreferences(join(__dirname, "preload.cjs"), SHELL),
  });
  const window = main;
  registerAppWindow(window, "main");
  const decideMain = (to: string) =>
    setup.isOpen() && isSetupPage(to, development)
      ? "allow"
      : decide(to, attacher.daemon(), setup.isOpen());
  guardNavigation(window.webContents, {
    decide: decideMain,
    newWindows: true,
    logRefusals: !app.isPackaged,
  });
  installZoom(window.webContents, zoom);
  hideOnClose(window);
  // With no tray to reopen a hidden window, closing it asks to quit instead.
  lifecycleHooks.onClose = (closing) => {
    void tray.available().then((available) => {
      if (closeAction(available) === "hide") return closing.hide();
      log("no tray could reopen the window; asking whether to quit");
      void quitter.confirmQuit();
    });
  };
  lifecycleHooks.windowSeen = () => tray.windowSeen();
  // The notifications skip a toast about the page the window shows while it
  // has focus.
  const focused = () =>
    notifications.setFocusedPage(
      !window.isDestroyed() && window.isVisible() && window.isFocused() && !window.isMinimized()
        ? window.webContents.getURL()
        : null,
    );
  window.on("focus", focused);
  window.on("blur", focused);
  window.on("show", focused);
  window.on("hide", focused);
  window.on("minimize", focused);
  window.on("restore", focused);
  // The page that opened the check window is going (a navigation the guard
  // refuses leaves it).
  window.webContents.on("did-start-navigation", (event) => {
    if (event.isMainFrame && !event.isSameDocument && decideMain(event.url) === "allow") {
      closeCheckWindow();
    }
  });
  window.webContents.on("did-navigate", focused);
  window.webContents.on("did-navigate-in-page", focused);
  screenshots(window);
  window.loadURL(url).catch((e) => log(`cannot show the first screen: ${errorMessage(e)}`));

  // The panel is a convenience: the menu still works without it.
  try {
    panel = createPanel({ preload: join(__dirname, "preload.cjs") });
    if (panel.window) {
      registerAppWindow(panel.window, "tray-panel");
      guardNavigation(panel.window.webContents, {
        decide: (to) => (isTrayPanel(to) ? "allow" : "deny"),
        newWindows: false,
        logRefusals: !app.isPackaged,
      });
    }
  } catch (e) {
    log(`no tray panel: ${errorMessage(e)}`);
    panel = undefined;
  }
  registerPanel(
    tray,
    panel ?? { window: undefined, toggle: () => {}, hide: () => {}, setHeight: () => {} },
  );

  // A hidden start shows the window after all if no tray appears.
  void tray.watchHost(hidden);
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

// The tray panel's window: frameless, always on top, off the taskbar, created
// hidden at startup (building a window from a tray event can misbehave on
// Windows) and hidden again when it loses focus. The rules (view model,
// commands, placement) are in panel.ts.
import { join } from "node:path";
import { app, BrowserWindow, type Rectangle, screen } from "electron";
import {
  DEFAULT_HEIGHT,
  panelPosition,
  type PanelHost,
  ReopenGuard,
  registerPanelCommands,
  WIDTH,
} from "./panel.ts";
import type { TrayController } from "./tray.ts";
import { securePreferences } from "./windows.ts";

/** The bundled page the window loads. */
export const PANEL_URL = "app://bundle/tray.html";

export interface PanelWindow {
  /** The window, for the guard that resolves a caller's role. `undefined`
   *  where there is no panel (Linux tray hosts report no clicks). */
  readonly window: BrowserWindow | undefined;
  /** The tray icon's left click: shows the panel under (or above) the icon,
   *  or hides it. `icon` is the icon's rectangle in screen points. */
  toggle(icon: Rectangle): void;
  hide(): void;
  setHeight(height: number): void;
}

export interface PanelOptions {
  /** The sandboxed preload script that exposes the command bridge. */
  preload?: string;
}

export function createPanel(options: PanelOptions = {}): PanelWindow {
  if (process.platform === "linux") {
    return { window: undefined, toggle: () => {}, hide: () => {}, setHeight: () => {} };
  }
  const window = new BrowserWindow({
    title: "baum",
    width: WIDTH,
    height: DEFAULT_HEIGHT,
    useContentSize: true,
    show: false,
    frame: false,
    resizable: false,
    maximizable: false,
    minimizable: false,
    fullscreenable: false,
    alwaysOnTop: true,
    skipTaskbar: true,
    // The panel's page declares nothing (no tile frames, links or check
    // window).
    webPreferences: securePreferences(options.preload ?? join(__dirname, "preload.cjs"), null),
  });
  window.setVisibleOnAllWorkspaces(true, { visibleOnFullScreen: true });
  // The panel shows its bundled page and nothing else.
  window.webContents.on("will-navigate", (event) => event.preventDefault());
  window.webContents.setWindowOpenHandler(() => ({ action: "deny" }));

  const guard = new ReopenGuard();
  let quitting = false;
  app.on("before-quit", () => {
    quitting = true;
  });
  const hide = () => {
    if (window.isDestroyed()) return;
    window.hide();
    guard.hidden(Date.now());
  };
  // It hides when it loses focus or is asked to close, and is never destroyed
  // until the app quits.
  window.on("blur", hide);
  window.on("close", (event) => {
    if (quitting) return;
    event.preventDefault();
    hide();
  });
  void window.loadURL(PANEL_URL);

  const show = () => {
    window.show();
    window.focus();
  };
  return {
    window,
    hide,
    setHeight(height) {
      if (!window.isDestroyed()) window.setContentSize(WIDTH, Math.round(height));
    },
    toggle(icon) {
      if (window.isVisible()) return hide();
      if (guard.blocks(Date.now())) return;
      const [width = WIDTH, height = DEFAULT_HEIGHT] = window.getSize();
      const position = panelPosition({
        icon,
        cursor: screen.getCursorScreenPoint(),
        screens: screen.getAllDisplays().map((d) => ({
          bounds: d.bounds,
          work: d.workArea,
          scale: d.scaleFactor,
        })),
        panel: [width, height],
        platform: process.platform,
      });
      if (position === undefined) {
        console.error("baum desktop: no screen found for the tray icon");
      } else {
        window.setPosition(Math.round(position[0]), Math.round(position[1]));
      }
      show();
    },
  };
}

/** Registers `tray_panel`, `tray_panel_height` and `tray_action` for the
 *  panel, reading the tray's view and running its menu ids. */
export function registerPanel(controller: TrayController, panel: PanelWindow): void {
  const host: PanelHost = {
    hide: panel.hide,
    setHeight: panel.setHeight,
    snapshot: () => controller.snapshot(),
    onMenu: (id) => controller.onMenu(id),
  };
  registerPanelCommands(host);
}

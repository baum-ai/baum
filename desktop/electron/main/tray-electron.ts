// The tray icon, its menu and the dock badge: a thin adapter from the plain
// data tray.ts computes to Electron's `Tray` and `Menu`. The rules and the
// scheduling are in tray.ts.
import { join } from "node:path";
import { app, Menu, nativeImage, type NativeImage, type Rectangle, Tray } from "electron";
import type { Icon } from "./status.ts";
import { TrayController, type TrayEffects, type TrayUpdate, toTemplate } from "./tray.ts";

/** What the tray asks of the rest of the app: everything but showing itself. */
export type HostEffects = Omit<TrayEffects, "apply">;

export interface TrayStart {
  host: HostEffects;
  /** The panel's left-click handler (macOS and Windows); omitted where there
   *  is no panel. */
  onClick?: ((icon: Rectangle) => void) | undefined;
  /** The folder holding `idle.png`, `running.png` and `attention.png`
   *  (`oss/desktop/icons/tray`); the packaged app must ship it. */
  iconsDir?: string;
}

/** The menu bar icon's size in points on macOS, which draws a tray image at
 *  its size in points and crops what overflows the bar. */
const MAC_TRAY_POINTS = 18;

/** Fits a 64 px tray icon to the platform. Linux and Windows tray hosts scale
 *  icons themselves; macOS would show 64 px as 64 pt, so there it gets an
 *  18 pt image with a representation for each display scale. */
function trayImage(source: NativeImage): NativeImage {
  if (process.platform !== "darwin" || source.isEmpty()) return source;
  const image = nativeImage.createEmpty();
  for (const scaleFactor of [1, 2, 3]) {
    const size = MAC_TRAY_POINTS * scaleFactor;
    image.addRepresentation({
      scaleFactor,
      buffer: source.resize({ width: size, height: size, quality: "best" }).toPNG(),
    });
  }
  return image;
}

/** Creates the tray and starts its reading loops. Call it once the app is
 *  ready, then attach the daemon with `attached`, `detached` and `connecting`
 *  on the returned controller. A tray that cannot be created (a missing
 *  indicator library on Linux) leaves the app running without one. */
export function startTray(start: TrayStart): TrayController {
  const dir = start.iconsDir ?? join(app.getAppPath(), "icons", "tray");
  const images = new Map<Icon, NativeImage>();
  const image = (icon: Icon): NativeImage => {
    let found = images.get(icon);
    if (found === undefined) {
      found = trayImage(nativeImage.createFromPath(join(dir, `${icon}.png`)));
      images.set(icon, found);
    }
    return found;
  };

  let tray: Tray | undefined;
  let menu: Menu | undefined;
  // Linux hosts show the menu on any click; macOS would also show it on a
  // left click, which the panel owns, so there it pops up on a right click.
  const menuOnClick = process.platform === "linux";

  const apply = (update: TrayUpdate): void => {
    // The dock badge belongs to the app, not the tray. Platforms without one
    // ignore it, which is no reason to say anything.
    try {
      app.setBadgeCount(update.badge ?? 0);
    } catch {}
    if (tray === undefined) return;
    tray.setImage(image(update.icon));
    tray.setToolTip(update.tooltip);
    menu = Menu.buildFromTemplate(toTemplate(update.menu, (id) => controller.onMenu(id)));
    if (menuOnClick) tray.setContextMenu(menu);
  };

  const controller = new TrayController({ ...start.host, apply });
  try {
    tray = new Tray(image("idle"));
    if (!menuOnClick) {
      tray.setIgnoreDoubleClickEvents(true);
      tray.on("click", (_event, bounds) => {
        controller.requestPlanRefresh();
        start.onClick?.(bounds);
      });
      tray.on("right-click", () => {
        controller.requestPlanRefresh();
        if (menu !== undefined) tray?.popUpContextMenu(menu);
      });
    }
    controller.setHasTray(true);
  } catch (e) {
    console.error(`baum desktop: no tray icon: ${e instanceof Error ? e.message : String(e)}`);
    tray = undefined;
  }
  controller.refresh();
  controller.start();
  return controller;
}

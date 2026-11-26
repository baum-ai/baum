// The window's menu bar (tray.rs's `install_window_menu` in the Tauri shell).
// Only macOS has one, its global menu bar: the platform's usual menus plus
// "Command Line and Hooks…" in the application menu, a Background menu with
// start at login, "Report a problem…" in Help, and the Notifications toggles.
// On Linux and Windows the window has no menu bar, as under Tauri, and the
// tray and the app hold these choices.
//
// The template is plain data built from the saved settings, which are the
// only copy of the state: every click goes to `onClick` (the tray's one menu
// router), and the menu is built again after a toggle so it shows what was
// saved rather than what the click flipped. The tray menu holds the same
// Notifications toggles (`trayNotifications`).
import type { MenuItemConstructorOptions } from "electron";
import type { MenuSubmenu } from "./notifications/index.ts";
import { MENU_LABEL } from "./onboarding.ts";
import { MENU_LOGIN, MENU_REPORT, type MenuItemSpec, SETUP_MENU_ID } from "./tray.ts";

export interface WindowMenuState {
  /** Whether start at login is on. */
  login: boolean;
  /** The Notifications submenu from the saved settings. */
  notifications: MenuSubmenu;
  /** Every item's click, by its id. */
  onClick(id: string): void;
}

/** The macOS menu bar, in the order Tauri's default menu had, with the app's
 *  own items added where the Tauri shell put them. */
export function windowMenuTemplate(state: WindowMenuState): MenuItemConstructorOptions[] {
  const item = (id: string, label: string): MenuItemConstructorOptions => ({
    id,
    label,
    click: () => state.onClick(id),
  });
  const check = (id: string, label: string, checked: boolean): MenuItemConstructorOptions => ({
    ...item(id, label),
    type: "checkbox",
    checked,
  });
  return [
    {
      role: "appMenu",
      submenu: [
        { role: "about" },
        { type: "separator" },
        // After "About baum" and its separator.
        item(SETUP_MENU_ID, MENU_LABEL),
        { type: "separator" },
        { role: "services" },
        { type: "separator" },
        { role: "hide" },
        { role: "hideOthers" },
        { role: "unhide" },
        { type: "separator" },
        // Quits the app and leaves the daemon running.
        { role: "quit" },
      ],
    },
    { role: "fileMenu" },
    { role: "editMenu" },
    // Not Electron's View menu, whose reload, developer tools and zoom items
    // the app does not offer: its own shortcuts zoom (zoom.ts).
    { label: "View", submenu: [{ role: "togglefullscreen" }] },
    { role: "windowMenu" },
    { role: "help", submenu: [item(MENU_REPORT, "Report a problem…")] },
    { label: "Background", submenu: [check(MENU_LOGIN, "Start at login", state.login)] },
    {
      id: state.notifications.id,
      label: state.notifications.label,
      submenu: state.notifications.items.map((entry) =>
        check(entry.id, entry.label, entry.checked),
      ),
    },
  ];
}

/** The Notifications toggles as the tray menu holds them, from the same
 *  saved settings as the menu bar's. */
export function trayNotifications(notifications: MenuSubmenu): MenuItemSpec {
  return {
    kind: "submenu",
    id: notifications.id,
    label: notifications.label,
    enabled: true,
    items: notifications.items.map((entry) => ({
      kind: "check",
      id: entry.id,
      label: entry.label,
      enabled: true,
      checked: entry.checked,
    })),
  };
}

/** Whether this platform gets a menu bar. */
export function hasWindowMenu(platform: NodeJS.Platform): boolean {
  return platform === "darwin";
}

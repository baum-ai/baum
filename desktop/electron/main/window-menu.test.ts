import type { MenuItemConstructorOptions } from "electron";
import { describe, expect, it } from "vitest";
import { isMenuId, submenu } from "./notifications/index.ts";
import { defaultSettings } from "./notifications/settings.ts";
import { route } from "./tray.ts";
import { hasWindowMenu, windowMenuTemplate } from "./window-menu.ts";

function template(login = false) {
  const kinds = defaultSettings().kinds;
  kinds.failed = false;
  const clicked: string[] = [];
  const menu = windowMenuTemplate({
    login,
    notifications: submenu(kinds),
    onClick: (id) => clicked.push(id),
  });
  return { menu, clicked };
}

function items(menu: MenuItemConstructorOptions): MenuItemConstructorOptions[] {
  return Array.isArray(menu.submenu) ? menu.submenu : [];
}

function find(menu: MenuItemConstructorOptions[], id: string): MenuItemConstructorOptions {
  for (const entry of menu) {
    if (entry.id === id) return entry;
    const inner = items(entry);
    if (inner.length > 0) {
      try {
        return find(inner, id);
      } catch {}
    }
  }
  throw new Error(`no ${id}`);
}

describe("the window's menu bar", () => {
  it("exists on macOS only", () => {
    expect(hasWindowMenu("darwin")).toBe(true);
    expect(hasWindowMenu("linux")).toBe(false);
    expect(hasWindowMenu("win32")).toBe(false);
  });

  it("has the platform's menus, then Background and Notifications", () => {
    const { menu } = template();
    expect(menu.map((m) => m.role ?? m.label)).toEqual([
      "appMenu",
      "fileMenu",
      "editMenu",
      "View",
      "windowMenu",
      "help",
      "Background",
      "Notifications",
    ]);
  });

  it("puts Command Line and Hooks after About and its separator", () => {
    const app = items(template().menu[0] as MenuItemConstructorOptions);
    expect(app.slice(0, 4).map((m) => m.role ?? m.type ?? m.label)).toEqual([
      "about",
      "separator",
      "Command Line and Hooks…",
      "separator",
    ]);
    expect(app.at(-1)?.role).toBe("quit");
  });

  it("offers no reload, developer tools or zoom of Electron's own", () => {
    const roles = JSON.stringify(template().menu);
    for (const role of ["reload", "forceReload", "toggleDevTools", "zoomIn", "resetZoom"]) {
      expect(roles).not.toContain(`"${role}"`);
    }
  });

  it("shows start at login and the notification toggles as saved", () => {
    expect(find(template(true).menu, "menu-login")).toMatchObject({
      type: "checkbox",
      label: "Start at login",
      checked: true,
    });
    expect(find(template(false).menu, "menu-login").checked).toBe(false);
    const { menu } = template();
    expect(find(menu, "notifications:failed")).toMatchObject({ type: "checkbox", checked: false });
    expect(find(menu, "notifications:finished")).toMatchObject({ checked: true });
  });

  it("sends every click to the one menu router, whose actions know each id", () => {
    const { menu, clicked } = template();
    const ids = ["command-line-and-hooks", "menu-report", "menu-login", "notifications:finished"];
    for (const id of ids) find(menu, id).click?.({} as never, undefined, {} as never);
    expect(clicked).toEqual(ids);
    expect(clicked.map((id) => route(id, isMenuId).kind)).toEqual([
      "setup",
      "report",
      "login",
      "notification",
    ]);
  });
});

// Native notifications from the daemon's events.
//
// The event stream forwards `notice` events, so a hidden or minimised window
// does not stop them (closing the window hides it and the app keeps running).
// `toast` turns the events that matter into text: a `notice` event (review
// finished or failed, review requested from the user, review generated
// automatically, news of the user's own pull requests and fixes, an agent
// session waiting for the user's permission or answer). `notifier` applies the
// settings, the operating system's permission and the focused window;
// `platform` shows the notification (`electron.ts`). A click makes the main
// process load the right app URL in the window, so pages the daemon serves
// need no capability.
//
// The toggles are a "Notifications" submenu in both the window's menu bar and
// the tray menu, one check item per kind, saved in the `notifications` section
// of desktop.json in the app's config directory. The saved settings are the
// only state; a click flips them and both menus are brought to them.
import { Notifier, type Host } from "./notifier.ts";
import type { Platform } from "./platform.ts";
import { type Kinds, Store } from "./settings.ts";
import { fromMenuId, KINDS, label, menuId, type Route, routeUrl } from "./toast.ts";

export { Notifier, type Host, type Outcome } from "./notifier.ts";
export type { Platform, ShowResult } from "./platform.ts";
export type { Kinds, Permission, Settings } from "./settings.ts";
export { Store } from "./settings.ts";
export { KINDS, type Kind, type Route, type Toast } from "./toast.ts";

const SUBMENU_ID = "notifications";

/** What the notifications need of the daemon client (`app_url`). */
export interface DaemonClient {
  /** `POST /api/auth/exchange`: the daemon's secret app path, `/s/<secret>/`,
   *  as an absolute URL on the daemon's origin. */
  appUrl(): Promise<URL>;
}

export interface MenuCheckItem {
  id: string;
  label: string;
  type: "checkbox";
  checked: boolean;
}

export interface MenuSubmenu {
  id: string;
  label: string;
  items: MenuCheckItem[];
}

/** The "Notifications" submenu as plain data, from the saved settings. */
export function submenu(kinds: Kinds): MenuSubmenu {
  return {
    id: SUBMENU_ID,
    label: "Notifications",
    items: KINDS.map((kind) => ({
      id: menuId(kind),
      label: label(kind),
      type: "checkbox",
      checked: kinds[kind],
    })),
  };
}

/** Whether `id` is one of the notification toggles. */
export function isMenuId(id: string): boolean {
  return fromMenuId(id) !== undefined;
}

/** The pull request a URL on the window's origin shows, as clone and number. */
export function prOf(url: string, origin: string): [string, number] | undefined {
  const parsed = parseUrl(url);
  if (!parsed || parsed.origin !== origin) return undefined;
  let clone: string | undefined;
  let number: number | undefined;
  for (const [key, value] of parsed.searchParams) {
    if (key === "clone" || key === "repository") clone = value;
    else if (key === "pr") number = /^\d+$/.test(value) ? Number(value) : undefined;
  }
  return clone === undefined || number === undefined ? undefined : [clone, number];
}

/** The agent session whose transcript a URL on the window's origin shows, by
 *  its `agent:id` key. */
export function sessionOf(url: string, origin: string): string | undefined {
  const parsed = parseUrl(url);
  if (!parsed || parsed.origin !== origin) return undefined;
  let sessions = false;
  let key: string | undefined;
  for (const [name, value] of parsed.searchParams) {
    if (name === "page") sessions = value === "sessions";
    else if (name === "session") key = value;
  }
  return sessions && key ? key : undefined;
}

function parseUrl(url: string): URL | undefined {
  try {
    return new URL(url);
  } catch {
    return undefined;
  }
}

export interface ServiceOptions {
  /** The app's config directory (desktop.json lives there), or a store. */
  configDir: string | Store;
  platform: Platform;
  client: DaemonClient;
  /** Shows the window if it is hidden or minimised, brings it to the front
   *  and loads `path` (the daemon's app path and query, such as
   *  `/s/<secret>/?clone=c1&pr=7`) where the window shows the app: the
   *  daemon's origin, or under `desktop::dev` Vite's. */
  openInWindow(path: string): void;
}

/** The notifier the app holds, and the window it works through. */
export class NotificationService implements Host {
  readonly notifier: Notifier;
  private frontend: string | undefined;
  private attached = false;
  private focusedPage: string | undefined;

  constructor(private readonly options: ServiceOptions) {
    const store =
      typeof options.configDir === "string" ? Store.inDir(options.configDir) : options.configDir;
    this.notifier = new Notifier(this, options.platform, store);
  }

  /** Sets the notification click destination for the attached daemon.
   *  `frontend` is where the window shows the app, whose origin focus is
   *  judged on. */
  attach(frontend: string | URL): void {
    this.frontend = new URL(frontend).origin;
    this.attached = true;
  }

  /** Clears the click destination while disconnected or connecting; notices
   *  are dropped until `attach`. */
  detach(): void {
    this.attached = false;
  }

  /** The window's page while it has focus; null while it is unfocused,
   *  hidden, minimised or showing a page that is not the app. */
  setFocusedPage(url: string | null): void {
    this.focusedPage = url ?? undefined;
  }

  /** A `notice` event's payload, as the events stream delivers it: its JSON
   *  text or the parsed object. Resolves once the notice has been dealt with. */
  async handleNotice(payload: string | object): Promise<void> {
    if (!this.attached) return;
    await this.notifier.handleEvent("notice", payload);
  }

  /** The "Notifications" submenu, from the saved settings. */
  menu(): MenuSubmenu {
    return submenu(this.notifier.settings().kinds);
  }

  /** Handles a menu click: flips the kind the item names (from the saved
   *  state, not the item's own check). The caller then rebuilds both menus
   *  from `menu()`. Returns whether the id was one of ours. */
  onMenu(id: string): boolean {
    const kind = fromMenuId(id);
    if (kind === undefined) return false;
    this.notifier.setKind(kind, !this.notifier.settings().kinds[kind]);
    return true;
  }

  /** Opens an agent session's transcript, as a click on its notification
   *  does, for the tray's waiting sessions. */
  openSession(key: string): Promise<void> {
    return this.openRoute({ type: "session", key });
  }

  /** Opens a page of the app in the window, as a click on a notification
   *  does: for the tray's alerts and waiting sessions. */
  async openRoute(route: Route): Promise<void> {
    if (!this.attached) return;
    try {
      const base = await this.options.client.appUrl();
      const url = routeUrl(route, base);
      this.options.openInWindow(`${url.pathname}${url.search}`);
    } catch (e) {
      console.error(`baum desktop: cannot open the notification's page: ${String(e)}`);
    }
  }

  focusedPr(): [string, number] | undefined {
    if (this.focusedPage === undefined || this.frontend === undefined) return undefined;
    return prOf(this.focusedPage, this.frontend);
  }

  focusedSession(): string | undefined {
    if (this.focusedPage === undefined || this.frontend === undefined) return undefined;
    return sessionOf(this.focusedPage, this.frontend);
  }

  open(route: Route): void {
    void this.openRoute(route);
  }
}

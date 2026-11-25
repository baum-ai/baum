// The app's windows and what their pages may do: the secure web preferences
// every window gets, the registry the command guard reads, the navigation
// guard of every frame, new-window requests and permission requests.
import type { BrowserWindow, Session, WebContents, WebPreferences } from "electron";
import { type Declaration, declarationArgument } from "../shared/contract.ts";
import type { WindowKind } from "./bridge.ts";
import type { LinkOpener } from "./links.ts";
import { log } from "./log.ts";
import { type Decision, originOf, schemeAndHost } from "./navigation.ts";

/** Every window's web preferences: no Node in the page, the preload
 *  sandboxed, isolated from the page and in the main frame only, and no
 *  `<webview>`. Only the main window carries the declaration. */
export function securePreferences(
  preload: string,
  declaration: Declaration | null,
): WebPreferences {
  return {
    preload,
    contextIsolation: true,
    sandbox: true,
    nodeIntegration: false,
    nodeIntegrationInWorker: false,
    nodeIntegrationInSubFrames: false,
    webviewTag: false,
    webSecurity: true,
    allowRunningInsecureContent: false,
    additionalArguments: declaration ? [declarationArgument(declaration)] : [],
  };
}

const kinds = new Map<number, { kind: WindowKind; window: BrowserWindow }>();

/** Adds one of the app's windows to those whose main frame may call
 *  commands (the guard still resolves each page's role). The tray port
 *  (3/7) registers its panel as `tray-panel`. */
export function registerAppWindow(window: BrowserWindow, kind: WindowKind): void {
  const id = window.webContents.id;
  kinds.set(id, { kind, window });
  window.once("closed", () => kinds.delete(id));
}

export function kindOf(contents: WebContents): WindowKind | undefined {
  return kinds.get(contents.id)?.kind;
}

export function windowOf(contents: WebContents): BrowserWindow | undefined {
  return kinds.get(contents.id)?.window;
}

let links: LinkOpener | null = null;

/** The app's one `LinkOpener`, which the `open_link` command shares, so a
 *  page opens the browser at most once a second however it asks. */
export function setLinkOpener(opener: LinkOpener): void {
  links = opener;
}

/** Opens a link to another site in the default browser (links.ts). Returns
 *  whether it opened. */
export function openOutside(url: string): boolean {
  return links?.open(url) === "browser";
}

export interface NavigationGuard {
  decide(url: string): Decision;
  /** Whether new-window requests may open links in the browser. */
  newWindows: boolean;
  /** Log a refused navigation's scheme and host (unpackaged apps). */
  logRefusals: boolean;
}

const guards = new Map<number, Pick<NavigationGuard, "decide" | "logRefusals">>();

/** Refuses every frame load of a guarded window that its guard does not
 *  allow, at the network: the second check behind `will-frame-navigate`,
 *  which Electron 45 asks about a frame navigating itself, a page setting a
 *  frame's `src` and a new frame alike (tests/shell shows both refusing on
 *  their own), so a load the events miss is still refused. Loads that never
 *  reach the network (`about:blank`, `data:`, `blob:`) stay with the page's
 *  `frame-src`, as on Windows in the Tauri shell. */
export function guardFrameRequests(session: Session): void {
  session.webRequest.onBeforeRequest((details, callback) => {
    const frame = details.resourceType === "mainFrame" || details.resourceType === "subFrame";
    const guard = frame && details.webContents ? guards.get(details.webContents.id) : undefined;
    if (!guard || guard.decide(details.url) === "allow") return callback({});
    if (guard.logRefusals) log(`refused a frame load of ${schemeAndHost(details.url)}`);
    callback({ cancel: true });
  });
}

/** Refuses every navigation, redirect and frame navigation of `contents`
 *  that `decide` does not allow, opens no window and attaches no webview.
 *  New-window requests open links to other sites in the browser (Electron
 *  does not say which frame asked; a tile frame is sandboxed without
 *  `allow-popups`, so it cannot ask). */
export function guardNavigation(
  contents: WebContents,
  { decide, newWindows, logRefusals }: NavigationGuard,
): void {
  const check = (event: { preventDefault(): void }, url: string) => {
    if (decide(url) === "allow") return;
    event.preventDefault();
    if (logRefusals) log(`refused a navigation to ${schemeAndHost(url)}`);
  };
  const id = contents.id;
  guards.set(id, { decide, logRefusals });
  contents.once("destroyed", () => guards.delete(id));
  contents.on("will-navigate", (event) => check(event, event.url));
  contents.on("will-frame-navigate", (event) => check(event, event.url));
  contents.on("will-redirect", (event) => check(event, event.url));
  contents.on("will-attach-webview", (event) => event.preventDefault());
  contents.setWindowOpenHandler(({ url }) => {
    if (newWindows) openOutside(url);
    return { action: "deny" };
  });
}

/** The permissions a page of the app may have: notifications and writing
 *  to the clipboard, and only on the app's own origins. */
const ALLOWED_PERMISSIONS = new Set(["notifications", "clipboard-sanitized-write"]);

export function permissionAllowed(
  permission: string,
  url: string | undefined,
  isAppOrigin: (origin: string) => boolean,
): boolean {
  if (!ALLOWED_PERMISSIONS.has(permission) || !url) return false;
  try {
    return isAppOrigin(originOf(new URL(url)));
  } catch {
    return false;
  }
}

export function guardPermissions(session: Session, isAppOrigin: (origin: string) => boolean): void {
  session.setPermissionRequestHandler((_contents, permission, callback, details) => {
    callback(permissionAllowed(permission, details.requestingUrl, isAppOrigin));
  });
  session.setPermissionCheckHandler((_contents, permission, origin, details) =>
    permissionAllowed(permission, details.requestingUrl ?? origin, isAppOrigin),
  );
}

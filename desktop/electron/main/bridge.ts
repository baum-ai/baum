// The guard in front of every command. Pages call commands through the
// preload's `baumDesktop.invoke`, which sends them over one IPC channel. A
// call runs only when it comes from the main frame of one of the app's own
// windows, at a page whose role (resolved from the window, the frame's
// origin and path) the command table (commands.ts) allows. This replaces
// Tauri's capability files and its per-origin command checks.
import type { IpcMain, IpcMainInvokeEvent, WebContents } from "electron";
import { INVOKE_CHANNEL, type InvokeResult } from "../shared/contract.ts";
import { type CommandContext, dispatch, isCommand, type Role } from "./commands.ts";
import { errorMessage, log } from "./log.ts";
import { bundledPageAllowed, isBundled, isTrayPanel, originOf } from "./navigation.ts";

/** The app's windows whose pages may call commands. The tile check window
 *  (check-window.ts) is not one: its page may call none. */
export type WindowKind = "main" | "tray-panel";

/** Vite's copy of the setup screen under development (development.ts). */
export const DEVELOPMENT_SETUP_ORIGIN = "http://localhost:1420";

export interface RoleInput {
  window: WindowKind;
  /** The calling frame's URL. */
  url: string;
  /** The attached daemon's origin (the development proxy's under
   *  development), or null when the app is not attached. */
  daemon: string | null;
  /** Whether the main window shows the setup screen. */
  setupOpen: boolean;
  /** An unpackaged app run with `BAUM_DESKTOP_DEV_DIR`. */
  development: boolean;
}

/** The role of the page at `url` in `window`, or null when it has none and
 *  may call nothing. */
export function resolveRole({
  window,
  url: raw,
  daemon,
  setupOpen,
  development,
}: RoleInput): Role | null {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  if (url.username !== "" || url.password !== "") return null;
  if (window === "tray-panel") {
    return isTrayPanel(raw) ? "tray-panel" : null;
  }
  if (isBundled(url)) {
    if (url.pathname === "/" || url.pathname === "/index.html") return "connecting-screen";
    if (url.pathname === "/setup.html" && bundledPageAllowed(url.pathname, setupOpen)) {
      return "setup-screen";
    }
    return null;
  }
  if (
    development &&
    setupOpen &&
    url.origin === DEVELOPMENT_SETUP_ORIGIN &&
    url.pathname === "/setup.html" &&
    url.search === ""
  ) {
    return "setup-screen";
  }
  // Only the attached daemon's own origin: another loopback port is a stale
  // daemon or another server.
  if (url.protocol === "http:" && daemon !== null && url.origin === daemon) {
    return "daemon-pages";
  }
  return null;
}

/** What the guard reads about the app. */
export interface BridgeState {
  /** Which of the app's windows `contents` belongs to, if any. */
  kindOf(contents: WebContents): WindowKind | undefined;
  /** The window object handlers receive. */
  windowOf(contents: WebContents): unknown;
  daemon(): string | null;
  setupOpen(): boolean;
  development: boolean;
  /** Log every command that runs, with the caller's role (an unpackaged app
   *  run with `BAUM_DESKTOP_LOG_COMMANDS`, for the shell harness). Refusals
   *  are always logged. */
  logCalls?: boolean;
}

/** The parts of an invoke event the guard reads. */
type Sender = Pick<IpcMainInvokeEvent, "sender" | "senderFrame">;

/** The calling page's context, or null when the call must be refused: a
 *  frame other than the main one, a window that is not the app's, or a
 *  page without a role. */
export function callerOf(event: Sender, state: BridgeState): CommandContext | null {
  const frame = event.senderFrame;
  const main = event.sender.mainFrame;
  if (!frame || frame.parent !== null) return null;
  if (frame.processId !== main.processId || frame.routingId !== main.routingId) return null;
  const kind = state.kindOf(event.sender);
  if (!kind) return null;
  const role = resolveRole({
    window: kind,
    url: frame.url,
    daemon: state.daemon(),
    setupOpen: state.setupOpen(),
    development: state.development,
  });
  if (!role) return null;
  return { window: state.windowOf(event.sender), role, origin: originOf(new URL(frame.url)) };
}

function isArgs(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Answers one invoke. Refusals say only that the command is not allowed,
 *  as Tauri's did, whatever the reason. */
export async function answer(
  event: Sender,
  state: BridgeState,
  name: unknown,
  args: unknown,
): Promise<InvokeResult> {
  const refused = (): InvokeResult => ({
    ok: false,
    error: `command ${isCommand(name) ? name : "unknown"} not allowed`,
  });
  if (!isCommand(name) || (args !== undefined && !isArgs(args))) return refused();
  const caller = callerOf(event, state);
  if (!caller) {
    log(`refused the ${name} command from a page that may not call it`);
    return refused();
  }
  if (state.logCalls) log(`running the ${name} command for the ${caller.role}`);
  try {
    return { ok: true, value: (await dispatch(name, args ?? {}, caller)) ?? null };
  } catch (e) {
    return { ok: false, error: errorMessage(e) };
  }
}

export function installBridge(ipcMain: IpcMain, state: BridgeState): void {
  ipcMain.handle(INVOKE_CHANNEL, (event, name: unknown, args: unknown) =>
    answer(event, state, name, args),
  );
}

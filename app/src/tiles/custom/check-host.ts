import { invoke } from "../../desktop/native";
import { framesAllowedHere } from "./environment";

/** Where the render check's runner draws (see "Render check" in
 *  oss/ARCHITECTURE.md). A desktop shell that declares
 *  `window.__BAUM_DESKTOP__.checkWindow === true` (Linux) gives it a window of
 *  its own, `tile-check`, with its own web process: the main page opens it on
 *  the first `tile_check` event and closes it after a hang (which ends that
 *  process, and the loop with it) or 2 minutes without checks. Anywhere else
 *  frames are allowed (a browser, the macOS shell) the runner draws inside
 *  the page; where they are not (a shell that declares no frames), nothing
 *  draws and the daemon answers `unavailable`. */

type Shell = Window & { __TAURI_INTERNALS__?: unknown; __BAUM_DESKTOP__?: unknown };

/** Whether the shell gives the render check a window of its own. */
export function checkWindowHere(win: Shell = window): boolean {
  if (!win.__TAURI_INTERNALS__) return false;
  const declared = win.__BAUM_DESKTOP__;
  return (
    typeof declared === "object" &&
    declared !== null &&
    Object.hasOwn(declared, "checkWindow") &&
    (declared as { checkWindow?: unknown }).checkWindow === true &&
    framesAllowedHere(win)
  );
}

/** Whether the runner draws inside this page. */
export function inPageChecksHere(win: Shell = window): boolean {
  return !checkWindowHere(win) && framesAllowedHere(win);
}

/** The first-show preflight runs only where a hang cannot freeze the page:
 *  in the check window. */
export const preflightHere = checkWindowHere;

/** How long the check window stays open without a check. */
export const IDLE_CLOSE = 2 * 60 * 1000;

export type TileCheckEvent = { id: string; state: string };

let idle: ReturnType<typeof setTimeout> | undefined;
let inPage: ((event: TileCheckEvent) => void) | null = null;

/** Sets what draws checks inside the page (the runner's module registers
 *  it, so this module stays free of it). */
export function setInPageRunner(run: ((event: TileCheckEvent) => void) | null) {
  inPage = run;
}

/** The shell's commands, one after another: a close after a hang and the
 *  open for a check still waiting must reach the shell in that order. */
let commands: Promise<unknown> = Promise.resolve();
function command(name: "open_check_window" | "close_check_window") {
  commands = commands.then(() => invoke(name)).catch(() => undefined);
}

function closeWindow() {
  clearTimeout(idle);
  idle = undefined;
  command("close_check_window");
}

/** What the main page does with a `tile_check` event. */
export function onTileCheckEvent(raw: unknown, win: Shell = window) {
  let event: TileCheckEvent;
  try {
    event = (typeof raw === "string" ? JSON.parse(raw) : raw) as TileCheckEvent;
  } catch {
    return;
  }
  if (typeof event?.id !== "string" || typeof event.state !== "string") return;
  if (checkWindowHere(win)) {
    if (event.state === "hung") return closeWindow();
    // Opening is idempotent, and the shell may have closed the window
    // itself (a reconnection), so every new check asks.
    // After a hang the daemon announces the checks still waiting again, so
    // the window closed for the hang opens afresh for them.
    if (event.state === "queued") command("open_check_window");
    clearTimeout(idle);
    idle = setTimeout(closeWindow, IDLE_CLOSE);
    return;
  }
  if (event.state === "queued" && inPageChecksHere(win)) inPage?.(event);
}

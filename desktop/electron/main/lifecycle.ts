// When the app's window hides and shows. The daemon outlives the app: closing the window hides it, so
// reopening it shows the same page in the same state, and quitting ends
// only the app. A second launch, and on macOS the Dock icon, show the window
// again. The tray port (3/7) adds the tray's Open, Quit and "Quit and stop
// background reviews", and where no tray is available sets `onClose` to ask
// "Quit baum?" instead of hiding.
import type { App, BrowserWindow } from "electron";

/** The flag start at login runs the app with: it attaches to the daemon
 *  without showing the window. */
export const HIDDEN_FLAG = "--hidden";

export function startsHidden(argv: readonly string[]): boolean {
  return argv.includes(HIDDEN_FLAG);
}

export interface LifecycleHooks {
  /** The person closed the main window. The default hides it; the tray port
   *  replaces it where no tray could reopen a hidden window. */
  onClose(window: BrowserWindow): void;
  /** The window was shown or focused (the tray port clears its attention
   *  state). */
  windowSeen?(): void;
}

export const lifecycleHooks: LifecycleHooks = {
  onClose: (window) => window.hide(),
};

let quitting = false;

/** From now on the windows close instead of hiding: the app is quitting, or
 *  an update is about to close them (Electron's `before-quit-for-update`,
 *  which on macOS comes before the windows close and `before-quit` after). */
export function allowClose(): void {
  quitting = true;
}

/** Whether the windows close instead of hiding (allowClose). */
export function closing(): boolean {
  return quitting;
}

/** Brings the main window back as it was. */
export function showWindow(window: BrowserWindow | null): void {
  if (window && !window.isDestroyed()) {
    if (window.isMinimized()) window.restore();
    window.show();
    window.focus();
  }
  lifecycleHooks.windowSeen?.();
}

/** Takes the single-instance lock, or returns false when another instance
 *  holds it: this one then quits, and the other shows its window. */
export function claimSingleInstance(
  app: App,
  main: () => BrowserWindow | null,
  updater?: { on(event: "before-quit-for-update", listener: () => void): unknown },
): boolean {
  if (!app.requestSingleInstanceLock()) return false;
  app.on("second-instance", () => showWindow(main()));
  // Clicking the Dock icon with the window hidden reopens it.
  app.on("activate", () => showWindow(main()));
  app.on("before-quit", allowClose);
  updater?.on("before-quit-for-update", allowClose);
  // The window is hidden, never closed, while the app runs; quitting is
  // always explicit.
  app.on("window-all-closed", () => {});
  return true;
}

/** Closing the main window hides it unless the app is quitting. */
export function hideOnClose(window: BrowserWindow): void {
  window.on("close", (event) => {
    if (quitting) return;
    event.preventDefault();
    lifecycleHooks.onClose(window);
  });
  window.on("focus", () => lifecycleHooks.windowSeen?.());
}

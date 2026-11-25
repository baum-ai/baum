/** The command bridge a desktop shell puts on the page. */
export type DesktopShell = {
  invoke<T>(command: string, args?: Record<string, unknown>): Promise<T>;
};

/** A window as a desktop shell leaves it: the Electron shell's preload
 *  exposes `baumDesktop`, the Tauri shell `__TAURI_INTERNALS__`, and both
 *  declare `__BAUM_DESKTOP__`. */
export type DesktopWindow = Window & {
  baumDesktop?: DesktopShell;
  __TAURI_INTERNALS__?: DesktopShell;
  __BAUM_DESKTOP__?: unknown;
};

/** The shell's command bridge (Electron's, else Tauri's), or undefined in a
 *  browser. */
export function desktopShell(win: Window = window): DesktopShell | undefined {
  const w = win as DesktopWindow;
  return w.baumDesktop ?? w.__TAURI_INTERNALS__;
}

export function invoke<T>(command: string, args?: Record<string, unknown>): Promise<T> {
  const bridge = desktopShell();
  if (!bridge) return Promise.reject(new Error("Open this screen in the baum desktop app."));
  return bridge.invoke<T>(command, args);
}

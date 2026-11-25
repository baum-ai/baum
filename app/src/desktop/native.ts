/** The command bridge a desktop shell puts on the page. */
export type DesktopShell = {
  invoke<T>(command: string, args?: Record<string, unknown>): Promise<T>;
};

/** A window as the Electron shell leaves it: its preload exposes
 *  `baumDesktop` and declares `__BAUM_DESKTOP__`. */
export type DesktopWindow = Window & {
  baumDesktop?: DesktopShell;
  __BAUM_DESKTOP__?: unknown;
};

/** The Electron shell's command bridge, or undefined in a browser. */
export function desktopShell(win: Window = window): DesktopShell | undefined {
  const w = win as DesktopWindow;
  return w.baumDesktop;
}

export function invoke<T>(command: string, args?: Record<string, unknown>): Promise<T> {
  const bridge = desktopShell();
  if (!bridge) return Promise.reject(new Error("Open this screen in the baum desktop app."));
  return bridge.invoke<T>(command, args);
}

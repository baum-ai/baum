// What this shell declares to the page in its main window as
// `window.__BAUM_DESKTOP__`, a frozen object the preload defines in the main
// frame only (see preload/index.ts; src/declaration.rs in the Tauri shell).
import type { Declaration } from "../shared/contract.ts";

/** Electron injects the preload, and so the command bridge, into the main
 *  frame only (`nodeIntegrationInSubFrames: false`) on every platform, and
 *  asks the shell about navigations in every frame (`will-frame-navigate`),
 *  so tile frames are on everywhere. The check window stays off until it is
 *  ported (4/7 of the migration). */
export const SHELL: Declaration = {
  tileFrames: true,
  checkWindow: false,
  openLinks: true,
};

/** The check window's page may call no command, so it opens links as
 *  new-window requests. For 4/7: the check window. */
export const CHECK_WINDOW_PAGE: Declaration = { ...SHELL, openLinks: false };

// What this shell declares to the page in its main window as
// `window.__BAUM_DESKTOP__`, a frozen object the preload defines in the main
// frame only (see preload/index.ts).
import type { Declaration } from "../shared/contract.ts";

/** Electron injects the preload, and so the command bridge, into the main
 *  frame only (`nodeIntegrationInSubFrames: false`) on every platform, and
 *  asks the shell about navigations in every frame (`will-frame-navigate`,
 *  and a frame load at the network), so tile frames are on everywhere.
 *  Chromium runs the tile check window in renderer processes of its own on
 *  every platform (check-window.ts), so it is on everywhere too. The shell
 *  harness (tests/shell) checks both on Linux. */
export const SHELL: Declaration = {
  tileFrames: true,
  checkWindow: true,
  openLinks: true,
};

/** The check window's page may call no command, so it opens no links with
 *  `open_link`; its window refuses every new-window request too. */
export const CHECK_WINDOW_PAGE: Declaration = { ...SHELL, openLinks: false };

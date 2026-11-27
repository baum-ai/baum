// The preload of the app's windows. It runs sandboxed, in the main frame
// only (`nodeIntegrationInSubFrames: false`), before the page's scripts:
//
// - `window.baumDesktop.invoke(name, args)` sends a command over the one
//   invoke channel; the main process's guard (bridge.ts) decides whether it
//   runs. A refused or failed command rejects with its message as a string,
//   as Tauri's bridge did.
// - `window.__BAUM_DESKTOP__`, the shell's declaration, when the main process
//   passed one on the renderer's command line. contextBridge defines both as
//   read-only properties holding frozen copies.
// - The saved theme, set on the document element before the first paint.
import { contextBridge, ipcRenderer } from "electron";
import {
  applyThemeBootstrap,
  INVOKE_CHANNEL,
  type InvokeResult,
  parseDeclaration,
  parseThemeBootstrap,
  THEME_CHANNEL,
} from "../shared/contract.ts";

contextBridge.exposeInMainWorld("baumDesktop", {
  async invoke(name: unknown, args?: unknown): Promise<unknown> {
    const result = (await ipcRenderer.invoke(INVOKE_CHANNEL, name, args)) as InvokeResult;
    if (result.ok) return result.value;
    throw result.error;
  },
});

const declaration = parseDeclaration(process.argv);
if (declaration) {
  contextBridge.exposeInMainWorld("__BAUM_DESKTOP__", Object.freeze(declaration));
}

// The theme the native screens apply, before the page's first paint, so no
// screen flashes the wrong colours (theme.rs's initialization script in the
// Tauri shell). Electron has no initialization script, and the main
// process's `dom-ready` comes after the parser may have painted while
// `did-start-navigation` still has the old document, so the preload, which
// runs before any of the page's scripts, asks the main process synchronously
// and sets the document element as soon as the parser creates it: a
// MutationObserver's callback runs as a microtask, before the browser can
// paint. The DOM is shared with the page, so nothing else is exposed to it.
const theme = parseThemeBootstrap(ipcRenderer.sendSync(THEME_CHANNEL));
if (theme) {
  const apply = (): boolean => {
    const root = document.documentElement;
    if (!root) return false;
    try {
      applyThemeBootstrap(root, theme);
    } catch {}
    return true;
  };
  if (!apply()) {
    const observer = new MutationObserver(() => {
      if (apply()) observer.disconnect();
    });
    observer.observe(document, { childList: true });
  }
}

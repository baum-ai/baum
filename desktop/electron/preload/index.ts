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
import { contextBridge, ipcRenderer } from "electron";
import { INVOKE_CHANNEL, type InvokeResult, parseDeclaration } from "../shared/contract.ts";

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

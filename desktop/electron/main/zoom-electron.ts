// Electron's side of the zoom (zoom.ts): applies the level to a window's web
// contents, turns Chromium's own zoom off so only the shortcuts and Settings
// change it, handles the shortcuts in the main process before the page sees
// them, and tells the page the new level with a `baum:zoom` event.
import type { WebContents } from "electron";
import { directionOf, step, type ZoomStore, zoomShortcut } from "./zoom.ts";

/** Scales the page to `level`. */
export function applyZoom(contents: WebContents, level: number): void {
  if (contents.isDestroyed()) return;
  contents.setZoomFactor(level);
}

/** Tells the page the level, as an event on `window` whose detail is the level. */
export function announceZoom(contents: WebContents, level: number): void {
  if (contents.isDestroyed()) return;
  const script = `window.dispatchEvent(new CustomEvent("baum:zoom", {detail: ${JSON.stringify(level)}}))`;
  contents.executeJavaScript(script).catch((e: unknown) => {
    console.error(`baum desktop: cannot tell the page its zoom level: ${String(e)}`);
  });
}

/** Wires one window's web contents to the store: applies the saved level as
 *  each page starts loading (a navigation may reset it), keeps Chromium's
 *  pinch and wheel zoom from changing it, and handles the shortcuts. */
export function installZoom(
  contents: WebContents,
  store: ZoomStore,
  platform: NodeJS.Platform = process.platform,
): void {
  const reapply = () => applyZoom(contents, store.level());
  Promise.resolve(contents.setVisualZoomLevelLimits(1, 1)).catch((e: unknown) => {
    console.error(`baum desktop: cannot turn off pinch zoom: ${String(e)}`);
  });
  contents.on("did-start-loading", reapply);
  contents.on("dom-ready", reapply);
  // Ctrl with the mouse wheel zooms Chromium's own level; put ours back.
  contents.on("zoom-changed", reapply);
  contents.on("before-input-event", (event, input) => {
    const action = zoomShortcut(input, platform);
    if (action === null) return;
    event.preventDefault();
    try {
      const level = store.update((current) => step(current, directionOf(action)));
      applyZoom(contents, level);
      announceZoom(contents, level);
    } catch (e) {
      console.error(`baum: cannot zoom: ${String(e)}`);
    }
  });
  reapply();
}

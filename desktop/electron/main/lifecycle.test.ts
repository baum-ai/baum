// Closing the main window hides it, until the app quits or an update is
// about to close the windows to install itself.
import { EventEmitter } from "node:events";
import type { App, BrowserWindow } from "electron";
import { describe, expect, it } from "vitest";
import { claimSingleInstance, closing, hideOnClose, lifecycleHooks } from "./lifecycle.ts";

class FakeWindow extends EventEmitter {
  hidden = 0;
  hide(): void {
    this.hidden++;
  }
  /** Emits `close` and says whether the window was let close. */
  close(): boolean {
    let prevented = false;
    this.emit("close", { preventDefault: () => (prevented = true) });
    return !prevented;
  }
}

describe("closing the window", () => {
  // One module-wide flag: this test runs the whole sequence in order.
  it("hides it until an update or a quit lets it close", () => {
    const app = Object.assign(new EventEmitter(), { requestSingleInstanceLock: () => true });
    const updater = new EventEmitter();
    expect(claimSingleInstance(app as unknown as App, () => null, updater)).toBe(true);
    const window = new FakeWindow();
    hideOnClose(window as unknown as BrowserWindow);
    lifecycleHooks.onClose = (w) => w.hide();

    expect(window.close()).toBe(false);
    expect(window.hidden).toBe(1);
    expect(closing()).toBe(false);

    // Electron's autoUpdater (Squirrel.Mac) emits this before it closes
    // the windows, and quits only once they have closed.
    updater.emit("before-quit-for-update");
    expect(closing()).toBe(true);
    expect(window.close()).toBe(true);
    expect(window.hidden).toBe(1);
  });

  it("is not claimed when another instance holds the lock", () => {
    const app = Object.assign(new EventEmitter(), { requestSingleInstanceLock: () => false });
    expect(claimSingleInstance(app as unknown as App, () => null)).toBe(false);
    expect(app.listenerCount("before-quit")).toBe(0);
  });
});

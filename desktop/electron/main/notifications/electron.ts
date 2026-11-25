// Desktop notifications through Electron's `Notification`: the freedesktop
// notification service on Linux, Notification Center on macOS and toasts on
// Windows.
//
// On Windows the main process must call
// `app.setAppUserModelId("com.usebaum.desktop")` before the first
// notification, or toasts carry no app identity and clicks are not reported.
import { Notification } from "electron";
import { escapeMarkup, type Platform, type ShowResult } from "./platform.ts";
import type { Permission } from "./settings.ts";
import type { Toast } from "./toast.ts";

/** Notifications waiting for a click, bounded so a flood of unread
 *  notifications cannot pile up. Electron drops a notification, and its click
 *  handler, once it is garbage collected, so each is held until it ends. */
const MAX_WAITING = 32;

export interface ElectronPlatformOptions {
  /** Whether the Linux notification server renders markup in a body. Electron
   *  cannot ask, so by default none is assumed and the body is shown as is. */
  supportsMarkup?: () => boolean;
}

export class ElectronPlatform implements Platform {
  private readonly waiting = new Set<Notification>();
  private warned = false;

  constructor(private readonly options: ElectronPlatformOptions = {}) {}

  requestPermission(): Permission {
    // Linux and Windows have no per-app permission, and macOS asks the user
    // when the first notification is posted, which this call precedes. A
    // refusal there is not reported to the app.
    return "granted";
  }

  show(toast: Toast, onClick: () => void): ShowResult {
    if (!Notification.isSupported()) {
      return { ok: false, error: "failed", reason: "notifications are not supported" };
    }
    const markup = process.platform === "linux" && this.options.supportsMarkup?.() === true;
    const notification = new Notification({
      title: toast.title,
      body: markup ? escapeMarkup(toast.body) : toast.body,
    });
    const release = () => {
      this.waiting.delete(notification);
    };
    if (this.waiting.size >= MAX_WAITING) {
      if (!this.warned) {
        this.warned = true;
        console.error(
          `baum desktop: ${MAX_WAITING} notifications are unread; clicks on newer ones will not open the app`,
        );
      }
    } else {
      this.waiting.add(notification);
      notification.once("click", () => {
        release();
        onClick();
      });
      notification.once("close", release);
      notification.once("failed", release);
    }
    try {
      notification.show();
    } catch (e) {
      release();
      return { ok: false, error: "failed", reason: String(e) };
    }
    return { ok: true };
  }
}

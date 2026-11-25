// The operating system's notifications, behind an interface so the rest of
// the module is tested without one. `electron.ts` is the real implementation.
import type { Permission } from "./settings.ts";
import type { Toast } from "./toast.ts";

export type ShowResult =
  | { ok: true }
  // The operating system refuses notifications from the app. Electron does
  // not report a refusal today; the notifier handles one for any platform
  // that does.
  | { ok: false; error: "denied" }
  | { ok: false; error: "failed"; reason: string };

export interface Platform {
  /** Asks the operating system to let the app notify. Called once, when the
   *  first notification is due, never at launch. */
  requestPermission(): Permission | Promise<Permission>;

  /** Shows the toast. `onClick` runs if the user clicks it; a toast nobody
   *  clicks is never reported. */
  show(toast: Toast, onClick: () => void): ShowResult;
}

/** Notification servers that render markup would show a title like
 *  `Fix <b>x</b>` as bold text, so its characters are escaped; the title the
 *  user sees is GitHub's, unchanged. */
export function escapeMarkup(text: string): string {
  return text.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");
}

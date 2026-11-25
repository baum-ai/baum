// Quitting (lifecycle.rs's `quit`, `quit_and_stop` and the no-tray close in
// the Tauri shell). The daemon outlives the app: Quit ends only the app.
// "Quit and stop background reviews" also runs `baum daemon stop`, which
// cancels running jobs, records them as interrupted and marks the daemon
// stopped by the user, so agent hooks do not start it again. It asks first
// when that would interrupt a review, and when the daemon will not stop it
// offers to quit anyway. Where no tray is available nothing could reopen a
// hidden window, so closing it asks "Quit baum?" instead.
import { NO_TRAY_PROMPT } from "./tray.ts";

/** `baum daemon stop` waits for the daemon's own 20 s shutdown, plus a margin. */
export const STOP_TIMEOUT = 45_000;

/** What to ask before stopping the daemon, or null when nothing running
 *  would be interrupted. Queued jobs are not interrupted: they have not
 *  started. */
export function stopPrompt(running: number, queued: number): string | null {
  if (running === 0) return null;
  const jobs = running === 1 ? "1 review is running" : `${running} reviews are running`;
  const waiting = queued === 0 ? "" : ` and ${queued} queued`;
  const one = running === 1;
  return `${jobs}${waiting}. Stopping background reviews will interrupt the running ${one ? "review" : "reviews"} and record ${one ? "it" : "them"} as interrupted. An interrupted automatic review is retried once when background reviews start again.`;
}

/** What to ask before stopping, given the live job count, or null when
 *  nothing would be interrupted. When the daemon could not be asked it always
 *  asks, since a review may be running that no earlier poll saw. */
export function stopConfirmation(
  counts: { running: number; queued: number } | undefined,
): string | null {
  if (counts === undefined) {
    return "baum couldn't check whether reviews are running; stopping will interrupt any that are. Interrupted reviews are recorded as interrupted, and an interrupted automatic review is retried once when background reviews start again.";
  }
  return stopPrompt(counts.running, counts.queued);
}

/** The question asked when the daemon will not stop: the app can still quit,
 *  leaving the daemon, which may still be running, to the user. */
export function stopFailureText(message: string, details: readonly string[]): string {
  let text = `${message}\n\nThe daemon may still be running. Quit baum anyway? Hooks will not restart a daemon you stopped until \`baum daemon ensure\` or the baum app starts it.`;
  if (details.length > 0) text += `\n\n${details.join("\n")}`;
  return text;
}

export interface QuitDeps {
  /** Running and queued jobs, asked of the daemon now; undefined when it
   *  cannot be asked. */
  jobCounts(): Promise<{ running: number; queued: number } | undefined>;
  /** A warning with `yes` and Cancel; resolves whether the person chose
   *  `yes`. */
  confirm(title: string, text: string, yes: string): Promise<boolean>;
  /** Why the app cannot run baum from where it is, if it cannot: then it
   *  has no daemon to stop. */
  misplaced(): string | null;
  /** Runs `baum daemon stop`; rejects with the failure's message and
   *  details. */
  stopDaemon(): Promise<void>;
  /** Quits the app. */
  exit(): void;
}

/** The quit actions, with at most one question showing at a time, so
 *  repeated clicks of the close button or a menu item do not stack dialogs. */
export class Quitter {
  #asking = false;

  constructor(private readonly deps: QuitDeps) {}

  /** Asks `text`; resolves false at once when another question shows. */
  async #ask(title: string, text: string, yes: string): Promise<boolean> {
    if (this.#asking) return false;
    this.#asking = true;
    try {
      return await this.deps.confirm(title, text, yes);
    } finally {
      this.#asking = false;
    }
  }

  /** Quits the app and leaves the daemon running. */
  quit(): void {
    this.deps.exit();
  }

  /** The close button where no tray could reopen the window. */
  async confirmQuit(): Promise<void> {
    if (await this.#ask("Quit baum?", NO_TRAY_PROMPT, "Quit")) this.deps.exit();
  }

  /** Stops the daemon, then quits. Asks first when that would interrupt a
   *  running review, and does not quit when the daemon will not stop unless
   *  the person says to. */
  async quitAndStop(): Promise<void> {
    if (this.#asking) return;
    const text = stopConfirmation(await this.deps.jobCounts());
    if (text !== null && !(await this.#ask("Stop background reviews?", text, "Stop and quit"))) {
      return;
    }
    // An app on a disk image runs no baum, so it has no daemon to stop.
    if (this.deps.misplaced() !== null) return this.deps.exit();
    try {
      await this.deps.stopDaemon();
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e);
      const details = (e as { details?: unknown }).details;
      const lines = Array.isArray(details) ? details.map(String) : [];
      const failure = stopFailureText(message, lines);
      if (await this.#ask("Cannot stop background reviews", failure, "Quit anyway")) {
        this.deps.exit();
      }
      return;
    }
    this.deps.exit();
  }
}

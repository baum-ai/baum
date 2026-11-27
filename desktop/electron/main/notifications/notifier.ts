// The decisions: whether a toast is shown at all, and what a click does.
import type { Platform } from "./platform.ts";
import type { Settings, Store } from "./settings.ts";
import { fromEvent, type Kind, type Route, routePr, routeSession, type Toast } from "./toast.ts";

/** The window the toasts are about. */
export interface Host {
  /** The pull request the window shows while it has focus, as clone and
   *  number; undefined when the window is unfocused, hidden or elsewhere. */
  focusedPr(): [string, number] | undefined;
  /** The agent session whose transcript the window shows while it has focus,
   *  by its `agent:id` key; undefined otherwise. */
  focusedSession(): string | undefined;
  /** Brings the window to the front on `route`. */
  open(route: Route): void;
}

/** What became of a toast, for tests and the log. */
export type Outcome =
  | "shown"
  // Its kind is switched off.
  | "off"
  // The operating system refuses notifications.
  | "denied"
  // The window has focus on the same pull request, or on the session's
  // transcript.
  | "focused"
  | "failed";

export class Notifier {
  private state: Settings;

  constructor(
    private readonly host: Host,
    private readonly platform: Platform,
    private readonly store: Store,
  ) {
    this.state = store.load();
  }

  settings(): Settings {
    return { kinds: { ...this.state.kinds }, permission: this.state.permission };
  }

  /** Turns one kind on or off and keeps the choice. Turning one on after a
   *  refusal asks the operating system again, since the user may have changed
   *  their mind there. */
  setKind(kind: Kind, on: boolean): void {
    this.state.kinds[kind] = on;
    if (on && this.state.permission === "denied") this.state.permission = "unknown";
    this.save();
  }

  /** A daemon event, as the events stream delivers it. */
  async handleEvent(name: string, data: string | object): Promise<Outcome | undefined> {
    const toast = fromEvent(name, data);
    if (!toast) return undefined;
    const outcome = await this.deliver(toast);
    if (outcome !== "shown") console.error(`baum desktop: notification not shown (${outcome})`);
    return outcome;
  }

  async deliver(toast: Toast): Promise<Outcome> {
    // Decide from a snapshot; the state is read again only to record.
    const settings = this.settings();
    if (!settings.kinds[toast.kind]) return "off";
    if (settings.permission === "denied") return "denied";
    const pr = routePr(toast.route);
    if (pr) {
      const focused = this.host.focusedPr();
      if (focused && focused[0] === pr[0] && focused[1] === pr[1]) return "focused";
    }
    const session = routeSession(toast.route);
    if (session !== undefined && this.host.focusedSession() === session) return "focused";
    // The first notification is the first use: only now is the operating
    // system asked. The request can wait on a system prompt, during which the
    // menu may change a kind, so the answer is recorded into the current
    // state.
    if (settings.permission === "unknown") {
      const answer = await this.platform.requestPermission();
      if (this.state.permission === "unknown") {
        this.state.permission = answer;
        this.save();
      }
      if (this.state.permission === "denied") return "denied";
    }
    const route = toast.route;
    const result = this.platform.show(toast, () => this.host.open(route));
    if (result.ok) return "shown";
    if (result.error === "denied") {
      this.state.permission = "denied";
      this.save();
      return "denied";
    }
    console.error(`baum desktop: cannot show a notification: ${result.reason}`);
    return "failed";
  }

  private save(): void {
    try {
      this.store.save(this.state);
    } catch (e) {
      console.error(`baum desktop: cannot save notification settings: ${String(e)}`);
    }
  }
}

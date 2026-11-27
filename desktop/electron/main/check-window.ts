// The tile check window: a second
// window, never shown, where the daemon's page renders custom tiles to check
// them (`?page=tile-check`). Chromium runs it in renderer processes of its
// own, so a tile that never yields stalls those and not the main window, and
// closing the window ends them.
//
// The main window's daemon page opens it (`open_check_window`) and closes it
// (`close_check_window`) when a check hangs or after it has been idle. The
// shell also closes it when the main window starts loading another page or
// the app detaches from the daemon, since the page that opened it is gone. It
// loads the same daemon origin as the main window, under the same navigation
// rules, opens no browser, and is not one of the app's command windows
// (windows.ts's `registerAppWindow`), so its page can call no command. This
// module holds the rules; check-window-electron.ts builds the window.

/** The check window's session: in memory, apart from the main window's.
 *  Chromium puts a session's sandboxed frames of one site in one process of
 *  their own, which every window in the session shares, so in the main
 *  window's session a tile spinning in the check window would stall the main
 *  window's tiles and could not be ended alone. Chromium never shares a
 *  renderer between sessions. The daemon's cookies are copied in before each
 *  load (check-window-electron.ts). */
export const PARTITION = "check-window";

/** The size the runner lays tiles out in. */
export const WIDTH = 1280;
export const HEIGHT = 900;

/** How long a close lets the window's page unload before its processes are
 *  ended (see `CheckWindow.close`). */
export const UNLOAD_GRACE = 200;

/** The check page for a main window showing `main`: the root of the
 *  person's own namespace on the daemon's origin (`/s/<namespace>/`, outside
 *  the sample project, as the app's `ownBase` reads it) with
 *  `?page=tile-check`. Null when the main window does not show a page of the
 *  attached daemon. */
export function checkPageUrl(main: string, daemon: string | null): string | null {
  if (daemon === null) return null;
  let url: URL;
  try {
    url = new URL(main);
  } catch {
    return null;
  }
  if (
    (url.protocol !== "http:" && url.protocol !== "https:") ||
    url.origin !== daemon ||
    url.username !== "" ||
    url.password !== ""
  ) {
    return null;
  }
  // Only a namespace followed by a slash, as the app's `appBase` matches.
  const match = /^\/s\/([^/]+)\//.exec(url.pathname);
  if (!match) return null;
  return `${url.origin}/s/${match[1]}/?page=tile-check`;
}

/** What the window rules need of Electron. */
export interface CheckWindowHost<W> {
  /** Builds the hidden window and starts loading `url`. */
  create(url: string): Promise<W>;
  isGone(window: W): boolean;
  /** The OS processes that only this window's frames run in. */
  ownProcesses(window: W): number[];
  /** Asks the page to close, as closing a window does: a page that still
   *  answers unloads, so its `pagehide` runs. */
  askToClose(window: W): void;
  /** Ends the window's main renderer process if it is its own, and destroys
   *  the window. */
  destroy(window: W): void;
  /** Ends a process the window's frames ran in, if it is still one of the
   *  app's renderers. */
  endProcess(pid: number): void;
  /** Forgets what the window's pages stored (its session's cookies and
   *  storage), once it is gone. */
  clearStorage(): Promise<void>;
  sleep(ms: number): Promise<void>;
}

/** Opens and closes the one check window, one call after another, so two
 *  calls never build two windows and an open right after a close builds a
 *  new one. */
export class CheckWindow<W> {
  #window: W | null = null;
  /** Counts the shell's own closes (`closeSoon`): an open asked for before
   *  one, and still waiting for an earlier call, builds nothing, since the
   *  page that asked is gone. */
  #generation = 0;
  #queue: Promise<void> = Promise.resolve();
  readonly #host: CheckWindowHost<W>;

  constructor(host: CheckWindowHost<W>) {
    this.#host = host;
  }

  /** The window, while it is open. */
  get window(): W | null {
    if (this.#window !== null && this.#host.isGone(this.#window)) this.#window = null;
    return this.#window;
  }

  /** Opens the window at `url`, or does nothing when it is open. */
  open(url: string): Promise<void> {
    const generation = this.#generation;
    return this.#serial(async () => {
      if (generation !== this.#generation || this.window !== null) return;
      this.#window = await this.#host.create(url);
    });
  }

  /** Closes the window if it is open; resolves once it is gone. */
  close(): Promise<void> {
    return this.#serial(() => this.#destroy());
  }

  /** The shell's own close: the main window started loading another page,
   *  or the app left the daemon. An open still waiting builds nothing. */
  closeSoon(): Promise<void> {
    this.#generation += 1;
    return this.close();
  }

  /** A page that still answers is asked to close first and given
   *  `UNLOAD_GRACE`, so a check it was drawing is reported abandoned (its
   *  `pagehide`) and ends as unavailable, rather than as a hang that blocks a
   *  tile type which did nothing wrong: the main window loading another page
   *  closes the window mid-check. A page that never yields ignores the
   *  request. Either way every process only the window used is then ended:
   *  Chromium leaves a renderer that is spinning running for a while after
   *  its window goes, and a tile frame may run in a process of its own. */
  async #destroy(): Promise<void> {
    const window = this.window;
    if (window === null) return;
    this.#window = null;
    const host = this.#host;
    const processes = host.ownProcesses(window);
    host.askToClose(window);
    await host.sleep(UNLOAD_GRACE);
    if (!host.isGone(window)) host.destroy(window);
    for (const pid of processes) host.endProcess(pid);
    await host.clearStorage();
  }

  #serial(task: () => Promise<void>): Promise<void> {
    const run = this.#queue.then(task);
    this.#queue = run.catch(() => {});
    return run;
  }
}

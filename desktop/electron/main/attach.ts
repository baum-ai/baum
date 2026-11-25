// Attaching the main window to the daemon (`connect`, `watch` and
// `await_replacement`). The window starts on the
// bundled connecting screen, attaches to the daemon (starting it if needed)
// and loads it, and returns to that screen with the daemon's recent log when
// the daemon cannot be reached. The app never restarts the daemon on its
// own: only the connecting screen's buttons (`reconnect`) run `baum daemon
// ensure` again, so a crashing daemon cannot restart in a loop.
import { Failure, type Health, type Launch, LOG_LINES } from "./daemon.ts";
import { errorMessage } from "./log.ts";

/** How often the attached daemon is checked, and how many failed checks in a
 *  row count as the daemon having gone away. */
export const HEALTH_INTERVAL = 5_000;
/** The pause between checks while the event stream is open and receiving;
 *  the stream going down ends it at once. */
export const HEALTH_INTERVAL_STREAMING = 30_000;
export const HEALTH_FAILURES = 3;

/** What the connecting screen shows: the `connection` command's answer, in
 *  the shape the connecting screen reads. */
export type Connection =
  | { phase: "connecting" }
  | { phase: "connected" }
  | { phase: "failed"; message: string; details: string[] }
  /** The app cannot run baum from where it is installed (a disk image), so
   *  there is nothing to retry until it is moved. */
  | { phase: "misplaced"; message: string };

/** Why the app attaches. After a daemon replaced the attached one, the app
 *  attaches once on its own; if the replacement's credential fails too, it
 *  shows the error instead of trying again. */
export type Attach = "first" | "replacement";

/** The pause between health checks: 30 seconds while the event stream is
 *  up, 5 otherwise. The events port (3/7) calls `setStreamUp` as its stream
 *  opens and closes; a stream going down, or `wake`, ends the pause at
 *  once. */
export class HealthPause {
  #streamUp = false;
  /** The pauses still waiting; each removes itself when it ends. */
  readonly #pending = new Set<() => void>();

  setStreamUp(up: boolean): void {
    this.#streamUp = up;
    if (!up) this.wake();
  }

  wake(): void {
    const pending = [...this.#pending];
    this.#pending.clear();
    for (const done of pending) done();
  }

  /** Tests only. */
  get waiting(): number {
    return this.#pending.size;
  }

  pause(): Promise<void> {
    const ms = this.#streamUp ? HEALTH_INTERVAL_STREAMING : HEALTH_INTERVAL;
    return new Promise((resolve) => {
      const done = () => {
        clearTimeout(timer);
        this.#pending.delete(done);
        resolve();
      };
      const timer = setTimeout(done, ms);
      this.#pending.add(done);
    });
  }
}

/** What other parts of the app do as the attachment changes. The tray port
 *  (3/7) shows the state; the notifications port (3/7) follows the attached
 *  daemon; the check window (4/7) closes whenever the app leaves a daemon. */
export interface AttachHooks {
  /** An attempt started; the window shows the connecting screen. */
  connecting?(): void;
  /** The app attached to `launch` (whether or not the window shows it). */
  attached?(launch: Launch): void;
  /** The window loads the daemon's page at `frontend`. */
  pageAttached?(launch: Launch, frontend: string): void;
  /** The app is no longer attached: it failed, or cannot run baum. */
  detached?(): void;
}

export interface AttachDeps {
  /** Why the app cannot run baum from where it is, if it cannot. */
  misplaced(): string | null;
  cliPath(): Promise<string>;
  ensure(cli: string): Promise<Launch>;
  health(launch: Launch): Promise<Health>;
  readCredential(state: string): Launch;
  logTail(state: string, lines: number): string[];
  /** The URL the window loads for a launch URL: the launch URL itself, or
   *  the development proxy's (development.ts). */
  frontend(launch: string): string;
  /** Whether the setup screen shows: the window then stays on it, and
   *  leaving it attaches again. */
  setupOpen(): boolean;
  /** Loads the daemon's page in the main window. */
  load(url: string): void;
  /** Returns the main window to the bundled screen unless it is there. */
  showScreen(): void;
  pause(): Promise<void>;
  sleep(ms: number): Promise<void>;
  log(message: string): void;
  hooks: AttachHooks;
}

export class Attacher {
  #connection: Connection = { phase: "connecting" };
  /** The origin the window may load while attached. */
  #daemon: string | null = null;
  /** Incremented by every connection attempt, so a stale attempt or health
   *  watch never overwrites a newer one. */
  #attempt = 0;
  readonly #deps: AttachDeps;

  constructor(deps: AttachDeps) {
    this.#deps = deps;
  }

  connection(): Connection {
    return structuredClone(this.#connection);
  }

  /** The attached daemon's origin (the development proxy's under
   *  development), or null. */
  daemon(): string | null {
    return this.#daemon;
  }

  /** Starts an attempt unless one is already connecting. Resolves when the
   *  attempt has attached or failed (the health watch goes on after). */
  connect(why: Attach = "first"): Promise<void> {
    const deps = this.#deps;
    const message = deps.misplaced();
    if (message) {
      deps.log(message);
      this.#connection = { phase: "misplaced", message };
      this.#daemon = null;
      deps.hooks.detached?.();
      deps.showScreen();
      return Promise.resolve();
    }
    if (this.#connection.phase === "connecting" && this.#attempt > 0) return Promise.resolve();
    this.#attempt += 1;
    this.#connection = { phase: "connecting" };
    this.#daemon = null;
    const attempt = this.#attempt;
    deps.hooks.connecting?.();
    deps.showScreen();
    return this.#attach(attempt, why);
  }

  async #attach(attempt: number, why: Attach): Promise<void> {
    const deps = this.#deps;
    let launch: Launch;
    try {
      launch = await deps.ensure(await deps.cliPath());
    } catch (e) {
      return this.#fail(attempt, toFailure(e));
    }
    let frontend: string;
    try {
      frontend = deps.frontend(launch.url());
    } catch (e) {
      return this.#fail(attempt, new Failure(errorMessage(e)));
    }
    if (this.#attempt !== attempt) return;
    this.#connection = { phase: "connected" };
    this.#daemon = new URL(frontend).origin;
    deps.log(`attached to baum daemon ${launch.status.version} (pid ${launch.status.pid})`);
    // The setup screen stays until the user leaves it, which attaches again.
    if (!deps.setupOpen()) {
      try {
        deps.load(frontend);
      } catch (e) {
        return this.#fail(attempt, new Failure(`Cannot open the baum daemon: ${errorMessage(e)}`));
      }
      deps.hooks.pageAttached?.(launch, frontend);
    }
    deps.hooks.attached?.(launch);
    void this.#watch(attempt, launch, why);
  }

  /** Checks the attached daemon until it stops answering or a newer attempt
   *  replaces this one. A daemon replaced by a newer one rejects the old
   *  credential, or stops answering while the new daemon's credential names
   *  another port; either way the app attaches to the new daemon. */
  async #watch(attempt: number, launch: Launch, why: Attach): Promise<void> {
    const deps = this.#deps;
    let failures = 0;
    for (;;) {
      await deps.pause();
      if (this.#attempt !== attempt) return;
      const result = await deps.health(launch);
      if (this.#attempt !== attempt) return;
      let replaced: boolean;
      if (result === "ok") {
        // A replacement that works may itself be replaced later.
        why = "first";
        failures = 0;
        continue;
      }
      if (result === "replaced") {
        replaced = true;
      } else {
        failures += 1;
        if (failures < HEALTH_FAILURES) continue;
        replaced = await this.#replacedElsewhere(launch);
        if (this.#attempt !== attempt) return;
      }
      if (replaced && why === "first") {
        deps.log("the baum daemon was replaced; attaching to the new one");
        await this.connect("replacement");
        return;
      }
      const details = deps.logTail(launch.status.state, LOG_LINES);
      if (replaced) {
        this.#fail(
          attempt,
          new Failure("The new baum daemon did not accept the app's credential.", details),
        );
        return;
      }
      this.#fail(attempt, new Failure("The baum daemon stopped responding.", details));
      return this.#awaitReplacement(attempt, launch);
    }
  }

  /** While the connecting screen shows a daemon that stopped, waits for
   *  another client (a hook that finds no daemon starts one, except after
   *  `baum daemon stop`) to start a new one, watching its credential file,
   *  and attaches to it. The app itself starts nothing; the wait ends when
   *  the user restarts the daemon from the screen. */
  async #awaitReplacement(attempt: number, launch: Launch): Promise<void> {
    for (;;) {
      await this.#deps.sleep(HEALTH_INTERVAL);
      if (this.#attempt !== attempt) return;
      if (await this.#replacedElsewhere(launch)) {
        if (this.#attempt !== attempt) return;
        this.#deps.log("a new baum daemon started; attaching to it");
        await this.connect("replacement");
        return;
      }
    }
  }

  /** Whether the credential file now names a different daemon that answers. */
  async #replacedElsewhere(launch: Launch): Promise<boolean> {
    let current: Launch;
    try {
      current = this.#deps.readCredential(launch.status.state);
    } catch {
      return false;
    }
    return current.origin !== launch.origin && (await this.#deps.health(current)) === "ok";
  }

  #fail(attempt: number, failure: Failure): void {
    if (this.#attempt !== attempt) return;
    const deps = this.#deps;
    deps.log(failure.message);
    this.#connection = { phase: "failed", message: failure.message, details: failure.details };
    this.#daemon = null;
    deps.hooks.detached?.();
    deps.showScreen();
  }
}

function toFailure(e: unknown): Failure {
  return e instanceof Failure ? e : new Failure(errorMessage(e));
}

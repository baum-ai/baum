// The adapters between the modules that talk to the attached daemon. Each
// takes the daemon in a shape of its own; this file is the one place that
// fits them together:
//
// - daemon.ts hands out a `Launch` whose URL is a method; client.ts and
//   events.ts read `{ url: URL, bearer() }` (`clientLaunch`).
// - The tray (tray.ts) asks for `get`/`put`/`post` resolving the body, and an
//   event stream of its own (`trayDaemon`). The app keeps one stream, the
//   `EventStreamReader`, whose change notices `EventFeed` passes on to the
//   tray as if they were its own stream.
// - The notifications ask for `appUrl()` (`appUrlOf`) and take the window
//   routes in their own spelling (`noticeRoute`).
// - The updater asks how many jobs run (`runningJobs`).
import { inspect } from "node:util";
import * as client from "./client.ts";
import { type Launch, parseStatus, readCredential, run } from "./daemon.ts";
import { parseJobs } from "./status.ts";
import { appUrl, Endpoint, type SseEvent } from "./events.ts";
import type { Route as NoticeRoute } from "./notifications/index.ts";
import type { Daemon, DaemonEvent, Route as TrayRoute, StreamHandlers } from "./tray.ts";

/** A daemon launch as client.ts and events.ts read it. Its URL holds the
 *  credential, so it prints as nothing. */
class ClientLaunch implements client.Launch {
  readonly #launch: Launch;

  constructor(launch: Launch) {
    this.#launch = launch;
  }

  get url(): URL {
    return new URL(this.#launch.url());
  }

  bearer(): string {
    return this.#launch.bearer();
  }

  toJSON(): object {
    return { origin: this.#launch.origin };
  }

  [inspect.custom](): string {
    return `ClientLaunch(${this.#launch.origin})`;
  }
}

export function clientLaunch(launch: Launch): client.Launch {
  return new ClientLaunch(launch);
}

/** Passes the one event stream's state and change notices to whoever reads
 *  them as a stream of their own (the tray). The app's `EventStreamReader`
 *  calls `opened`, `closed` and `change`. */
export class EventFeed {
  #up = false;
  readonly #subscribers = new Set<{ handlers: StreamHandlers; end: () => void }>();

  /** Whether the stream is open and receiving. */
  get up(): boolean {
    return this.#up;
  }

  opened(): void {
    this.#up = true;
    for (const { handlers } of [...this.#subscribers]) handlers.onOpen();
  }

  closed(): void {
    this.#up = false;
    for (const { end } of [...this.#subscribers]) end();
  }

  change(event: SseEvent): void {
    const daemonEvent: DaemonEvent = {
      name: event.name,
      id: event.id === null ? undefined : event.id.toString(),
      data: event.data,
    };
    for (const { handlers } of [...this.#subscribers]) handlers.onEvent(daemonEvent);
  }

  /** One connection's worth of the stream, as tray.ts's `Daemon.stream`:
   *  `onOpen` at once if the stream is up, else when it opens; resolves when
   *  it closes or `signal` aborts. The reader reconnects, so this never
   *  rejects, and an event missed between two subscriptions is made up for
   *  by the full read `onOpen` asks for. */
  subscribe(handlers: StreamHandlers, signal: AbortSignal): Promise<void> {
    return new Promise((resolve) => {
      if (signal.aborted) return resolve();
      const subscriber = {
        handlers,
        end: () => {
          this.#subscribers.delete(subscriber);
          signal.removeEventListener("abort", subscriber.end);
          resolve();
        },
      };
      this.#subscribers.add(subscriber);
      signal.addEventListener("abort", subscriber.end, { once: true });
      if (this.#up) handlers.onOpen();
    });
  }
}

/** A request whose answer must have one of `ok`'s statuses; resolves to the
 *  body. */
async function body(
  launch: client.Launch,
  method: string,
  path: string,
  json: string | null,
  timeoutMs: number,
  ok: readonly number[],
): Promise<string> {
  const response = await client.request(launch, method, path, json, timeoutMs);
  if (!ok.includes(response.status)) throw new Error(`${path} answered ${response.status}`);
  return response.body.toString("utf8");
}

/** The attached daemon as the tray reads it: client.ts's requests, and the
 *  app's one event stream through `feed`. */
export function trayDaemon(launch: client.Launch, feed: EventFeed): Daemon {
  const timeout = client.POLL_TIMEOUT_MS;
  return {
    get: (path, timeoutMs = timeout) => body(launch, "GET", path, null, timeoutMs, [200]),
    put: (path, json, timeoutMs = timeout) => body(launch, "PUT", path, json, timeoutMs, [200]),
    // The catch-up answers 202.
    post: (path, json, timeoutMs = timeout) =>
      body(launch, "POST", path, json, timeoutMs, [200, 202]),
    stream: (handlers, signal) => feed.subscribe(handlers, signal),
  };
}

/** The daemon's app path for the attached daemon, for the notifications'
 *  `DaemonClient.appUrl`. */
export function appUrlOf(launch: client.Launch | null): Promise<URL> {
  if (launch === null) return Promise.reject(new Error("the app is not attached to the daemon"));
  const endpoint = Endpoint.fromLaunch(launch);
  if (endpoint === null) return Promise.reject(new Error("the daemon's address is unusable"));
  return appUrl(endpoint);
}

/** A tray route (an alert, the status line, a waiting session) as the
 *  notifications open it in the window. */
export function noticeRoute(route: TrayRoute): NoticeRoute {
  switch (route.kind) {
    case "jobs":
      return { type: "jobs" };
    case "job":
      return { type: "job", id: route.id };
    case "diagnostics":
      return { type: "diagnostics" };
    case "session":
      return { type: "session", key: route.key };
  }
}

/** How long `baum daemon status` may take before an update. */
export const STATUS_TIMEOUT = 10_000;

/** How many generation jobs the user's daemon runs, which an update waits
 *  for: `baum daemon status --json` names the daemon's state
 *  directory, whose credential reaches `GET /api/jobs`. 0 when no daemon
 *  runs; rejects when one runs but cannot be asked. */
export async function runningJobs(cli: string, timeout = STATUS_TIMEOUT): Promise<number> {
  let stdout: string[];
  try {
    stdout = (await run(cli, ["daemon", "status", "--json"], timeout)).stdout;
  } catch {
    return 0;
  }
  const launch = clientLaunch(readCredential(parseStatus(stdout).state));
  return parseJobs((await client.jobs(launch)).toString("utf8")).running;
}

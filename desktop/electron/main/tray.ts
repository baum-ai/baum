// The tray icon and its menu: the app's presence while the window is closed.
//
// The icon follows `status.View`, which the controller computes from the
// attached daemon's `/api/jobs`, `/api/automatic`, `/api/index?view=waiting`
// and `/api/live-sessions`. It reads when the daemon's event stream
// (`/api/events`, kept open by its own loop, reconnecting with backoff)
// announces a change, only the requests that kind of event can affect, after a
// 500 ms pause for a burst of events to end; as a safety net it also reads
// every 60 s while the stream is up and every 30 s while it is down, and never
// with no daemon attached. A separate loop computes from `/api/diagnostics`
// and `/api/plan-usage` (diagnostics every 30 minutes, without `refresh`, so
// the daemon answers from its cache of at most that age; plan usage every 15
// minutes, forced when the tray is clicked). The main process tells this
// module when it attaches to a daemon, loses it, or starts connecting.
//
// On macOS and Windows a left click opens the panel (panel.ts), a small window
// drawn like a menu with meters, and a right click this menu. Linux tray hosts
// report no clicks, so there the menu is all, with plan usage as lines of text.
//
// This file holds the rules and the scheduling. It never imports `electron`:
// the menu is plain data (`MenuItemSpec`), what the tray does to the rest of
// the app goes through `TrayEffects`, and the daemon through `Daemon`, so
// tray-electron.ts is the only adapter.
import { spawn } from "node:child_process";
import { isDeepStrictEqual } from "node:util";
import {
  ALERT_DIAGNOSTICS,
  ALERT_JOB,
  ALERT_JOBS,
  automaticRequest,
  badge,
  type Icon,
  isAlert,
  type Link,
  meterText,
  type Observed,
  type Plan,
  pauseLabel,
  parseAutomatic,
  parseDiagnostics,
  parseJobs,
  parseLive,
  parsePlanUsage,
  parseWaiting,
  toggled,
  Tracker,
  tooltip,
  type View,
  waitingLabel,
} from "./status.ts";

/** Events wait this long for more before the tray reads (at most
 *  `DEBOUNCE_MAX`), so a burst of job events costs one set of requests. */
export const DEBOUNCE = 500;
export const DEBOUNCE_MAX = 2_000;
/** A read with no event, as a safety net while the event stream is up. */
export const FALLBACK_STREAM_UP = 60_000;
/** The same while the stream is down, when nothing announces changes. */
export const FALLBACK_STREAM_DOWN = 30_000;
export const STREAM_BACKOFF_FIRST = 1_000;
export const STREAM_BACKOFF_MAX = 30_000;
export const DIAGNOSTICS_INTERVAL = 30 * 60_000;
/** The daemon caches plan usage this long, so asking sooner repeats it. */
export const PLAN_USAGE_INTERVAL = 15 * 60_000;
/** How long one bus query may take. */
export const HOST_CHECK_TIMEOUT = 1_000;
/** How long a hidden start at login waits for the desktop's tray host, which
 *  may start after the app does. */
export const HOST_WAIT = 10_000;

// The daemon's request deadlines.
export const POLL_TIMEOUT = 5_000;
/** Diagnostics run the doctor's checks; the daemon gives them up to 45 s. */
export const DIAGNOSTICS_TIMEOUT = 50_000;
/** A plan usage reading may ask Claude's usage endpoint first. */
export const PLAN_USAGE_TIMEOUT = 20_000;

/** The prefix of a waiting session's menu id, before its `agent:id` key. */
export const SESSION_PREFIX = "session:";
/** The window menu's own item, so its events never match the tray's. */
export const MENU_LOGIN = "menu-login";
/** The window menu's Help item; the tray's is "report". */
export const MENU_REPORT = "menu-report";
/** The menu id of "Command Line and Hooks…" (onboarding). */
export const SETUP_MENU_ID = "command-line-and-hooks";
/** Where a problem is reported, and what Report a problem opens when the
 *  daemon cannot supply a prefilled link: the public repository's issue chooser. */
export const ISSUES_CHOOSE = "https://github.com/baum-ai/baum/issues/new/choose";
/** A link the daemon built must open the bug report of that repository and
 *  nothing else, whatever the daemon answers. */
export const ISSUES_NEW_PREFIX = "https://github.com/baum-ai/baum/issues/new?";

export const NO_TRAY_PROMPT =
  'This desktop has no system tray, so baum cannot stay in the background with its window closed. Quit baum?\n\nBackground reviews keep running; quitting only closes the app. Run "baum daemon stop" to stop them.';

// ---------------------------------------------------------------------------
// The daemon, as the tray needs it. The HTTP client implements it.

/** The daemon's app API over loopback HTTP. Each call resolves with the
 *  response body of a successful answer (200, or 202 for the catch-up) and
 *  rejects with an Error otherwise.
 *  `timeoutMs` is the deadline per read and write. */
export interface DaemonApi {
  get(path: string, timeoutMs?: number): Promise<string>;
  put(path: string, body: string, timeoutMs?: number): Promise<string>;
  post(path: string, body: string, timeoutMs?: number): Promise<string>;
}

/** One event of the daemon's stream: its kind (`progress`, `live`, …). */
export interface DaemonEvent {
  name: string;
  [key: string]: unknown;
}

/** Where a stream resumes after a reconnection. The controller owns one per
 *  attachment; the stream reader reads and updates `lastId`. */
export interface StreamCursor {
  lastId: number | string | undefined;
}

export interface StreamHandlers {
  /** The stream is open and receiving. */
  onOpen(): void;
  onEvent(event: DaemonEvent): void;
}

/** An attached daemon: its API and its event stream. */
export interface Daemon extends DaemonApi {
  /** Reads one connection of `/api/events` until it closes, calling the
   *  handlers; rejects when it cannot connect or breaks, resolves when it
   *  closes cleanly, and returns promptly once `signal` aborts. */
  stream(handlers: StreamHandlers, signal: AbortSignal, cursor: StreamCursor): Promise<void>;
}

// ---------------------------------------------------------------------------
// The menu as data.

export type MenuItemSpec =
  | { kind: "item"; id: string; label: string; enabled: boolean }
  | { kind: "check"; id: string; label: string; enabled: boolean; checked: boolean }
  | { kind: "separator"; id: string }
  | { kind: "submenu"; id: string; label: string; enabled: boolean; items: MenuItemSpec[] };

function item(id: string, label: string, enabled: boolean): MenuItemSpec {
  return { kind: "item", id, label, enabled };
}

function separator(id: string): MenuItemSpec {
  return { kind: "separator", id };
}

/** Seconds since the Unix epoch, for "resets in" and "ago". */
export function now(): number {
  return Math.floor(Date.now() / 1000);
}

/** The tray menu for a view, in the order of the panel: what the daemon is
 *  doing and anything needing the user (an alert goes where to look about
 *  it, a waiting session opens its transcript), the plan usage left when any
 *  can be read, then Open, pause or resume, start at login, Quit, and Quit and
 *  stop. */
export function menuSpec(view: View, login: boolean, at: number): MenuItemSpec[] {
  // While reviews run or wait, the status opens the jobs list.
  const entries: MenuItemSpec[] = [item("summary", view.summary, view.running + view.queued > 0)];
  const waiting = waitingLabel(view);
  if (waiting !== undefined) entries.push(item("waiting", waiting, false));
  for (const alert of view.attention) {
    entries.push(item(alert.id, `${alert.text} · ${alert.action}`, true));
  }
  for (const needs of view.needs) {
    entries.push(item(`${SESSION_PREFIX}${needs.key}`, `${needs.title} · ${needs.detail}`, true));
  }
  if (view.usage.length > 0) {
    entries.push(separator("separator-usage"));
    entries.push(item("usage", "Plan usage left", false));
    view.usage.forEach((meter, i) => {
      entries.push(item(`usage-${i}`, `${meter.label}: ${meterText(meter, at)}`, false));
    });
  }
  entries.push(separator("separator-1"));
  entries.push(item("open", "Open baum", true));
  entries.push(item("pause", pauseLabel(view.automatic), view.automatic !== undefined));
  entries.push({
    kind: "check",
    id: "login",
    label: "Start at login",
    enabled: true,
    checked: login,
  });
  entries.push(item("report", "Report a problem…", true));
  entries.push(separator("separator-2"));
  entries.push(item("quit", "Quit", true));
  entries.push(item("quit-stop", "Quit and stop background reviews", true));
  return entries;
}

/** Notifications go before the last separator, above Quit. The submenu is
 *  built afresh each time, from the notifier's settings, which are the one
 *  source of truth for both menus. */
export function withNotifications(
  spec: MenuItemSpec[],
  submenu: MenuItemSpec | undefined,
): MenuItemSpec[] {
  if (submenu === undefined) return spec;
  const found = spec.findIndex((entry) => entry.id === "separator-2");
  const at = found === -1 ? spec.length : found;
  return [...spec.slice(0, at), submenu, ...spec.slice(at)];
}

/** The menu in the shape Electron's `Menu.buildFromTemplate` takes. Every
 *  item's click goes to the one `onClick`, so no menu has a listener of its
 *  own to run a click twice. */
export interface MenuTemplateItem {
  type: "normal" | "checkbox" | "separator" | "submenu";
  id: string;
  label?: string;
  enabled?: boolean;
  checked?: boolean;
  click?: () => void;
  submenu?: MenuTemplateItem[];
}

export function toTemplate(
  spec: readonly MenuItemSpec[],
  onClick: (id: string) => void,
): MenuTemplateItem[] {
  return spec.map((entry): MenuTemplateItem => {
    switch (entry.kind) {
      case "separator":
        return { type: "separator", id: entry.id };
      case "item":
        return {
          type: "normal",
          id: entry.id,
          label: entry.label,
          enabled: entry.enabled,
          click: () => onClick(entry.id),
        };
      case "check":
        return {
          type: "checkbox",
          id: entry.id,
          label: entry.label,
          enabled: entry.enabled,
          checked: entry.checked,
          click: () => onClick(entry.id),
        };
      case "submenu":
        return {
          type: "submenu",
          id: entry.id,
          label: entry.label,
          enabled: entry.enabled,
          submenu: toTemplate(entry.items, onClick),
        };
    }
  });
}

// ---------------------------------------------------------------------------
// What a menu id does.

/** Where an alert or a notification goes in the window. */
export type Route =
  | { kind: "jobs" }
  | { kind: "job"; id: string }
  | { kind: "diagnostics" }
  | { kind: "session"; key: string };

/** What a menu id does. Ids are unique across the window and tray menus,
 *  except the notification toggles, which both hold and which mean the same. */
export type Action =
  | { kind: "open" }
  /** The status line while reviews run or wait: opens the jobs list. */
  | { kind: "jobs" }
  /** An alert, by its menu id: goes where to look, and records it seen. */
  | { kind: "alert"; id: string }
  /** A waiting agent session, by its `agent:id` key: opens its transcript. */
  | { kind: "session"; key: string }
  | { kind: "pause" }
  | { kind: "login" }
  /** "Command Line and Hooks…", in the window's menu bar. */
  | { kind: "setup" }
  /** "Report a problem…": opens a prefilled GitHub issue in the browser. */
  | { kind: "report" }
  | { kind: "quit" }
  | { kind: "quit-stop" }
  | { kind: "notification" }
  | { kind: "ignore" };

export function route(id: string, isNotificationId: (id: string) => boolean): Action {
  switch (id) {
    case "open":
      return { kind: "open" };
    case "summary":
      return { kind: "jobs" };
    case "pause":
      return { kind: "pause" };
    case "login":
    case MENU_LOGIN:
      return { kind: "login" };
    case SETUP_MENU_ID:
      return { kind: "setup" };
    case "report":
    case MENU_REPORT:
      return { kind: "report" };
    case "quit":
      return { kind: "quit" };
    case "quit-stop":
      return { kind: "quit-stop" };
  }
  if (isNotificationId(id)) return { kind: "notification" };
  if (isAlert(id)) return { kind: "alert", id };
  if (id.startsWith(SESSION_PREFIX) && id.length > SESSION_PREFIX.length) {
    return { kind: "session", key: id.slice(SESSION_PREFIX.length) };
  }
  return { kind: "ignore" };
}

/** The prefilled issue link in a `/api/diagnostics` answer, only when it opens
 *  the public repository's new issue form. */
export function issueUrl(body: string): string | undefined {
  let value: unknown;
  try {
    value = JSON.parse(body);
  } catch {
    return undefined;
  }
  if (typeof value !== "object" || value === null) return undefined;
  const url = (value as Record<string, unknown>).report_issue_url;
  return typeof url === "string" && url.startsWith(ISSUES_NEW_PREFIX) ? url : undefined;
}

// ---------------------------------------------------------------------------
// Whether a tray icon is showing, so the window may hide into it.

/** What a desktop's StatusNotifier host answered, from `NameHasOwner`. */
export function parseNameHasOwner(output: string): boolean | undefined {
  if (output.includes("true")) return true;
  if (output.includes("false")) return false;
  return undefined;
}

export type Host = "present" | "absent" | "unknown";

export type Bounded =
  | { kind: "done"; success: boolean; stdout: string }
  | { kind: "timed_out" }
  /** The program could not be started. */
  | { kind: "not_run" };

/** Runs a program and resolves with its answer, killing it after `timeoutMs`. */
export type CommandRunner = (
  program: string,
  args: readonly string[],
  timeoutMs: number,
) => Promise<Bounded>;

export const runBounded: CommandRunner = (program, args, timeoutMs) =>
  new Promise((resolve) => {
    let stdout = "";
    let settled = false;
    const finish = (result: Bounded) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve(result);
    };
    const child = spawn(program, [...args], { stdio: ["ignore", "pipe", "ignore"] });
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      finish({ kind: "timed_out" });
    }, timeoutMs);
    child.stdout.setEncoding("utf8");
    child.stdout.on("data", (chunk: string) => {
      stdout += chunk;
    });
    child.on("error", () => finish({ kind: "not_run" }));
    child.on("close", (code) => finish({ kind: "done", success: code === 0, stdout }));
  });

/** Whether something on this desktop shows tray icons. Linux icons need a
 *  StatusNotifier watcher on the session bus (GNOME has none without an
 *  extension); the icon builder succeeds without one and nothing appears.
 *  When neither `busctl` nor `gdbus` can ask, the answer is unknown, which
 *  counts as no tray: the window then stays reachable. */
export async function hostCheck(
  run: CommandRunner = runBounded,
  platform: string = process.platform,
): Promise<Host> {
  if (platform !== "linux") return "present";
  const watcher = "org.kde.StatusNotifierWatcher";
  const busctl = await run("busctl", ["--user", "status", watcher], HOST_CHECK_TIMEOUT);
  if (busctl.kind === "done") return busctl.success ? "present" : "absent";
  // A stalled bus will not answer gdbus either.
  if (busctl.kind === "timed_out") return "unknown";
  const gdbus = await run(
    "gdbus",
    [
      "call",
      "--session",
      "--dest",
      "org.freedesktop.DBus",
      "--object-path",
      "/org/freedesktop/DBus",
      "--method",
      "org.freedesktop.DBus.NameHasOwner",
      watcher,
    ],
    HOST_CHECK_TIMEOUT,
  );
  if (gdbus.kind !== "done") return "unknown";
  switch (parseNameHasOwner(gdbus.stdout)) {
    case true:
      return "present";
    case false:
      return "absent";
    default:
      return "unknown";
  }
}

/** The decision: a tray was created and the desktop shows it now. */
export function trayUsable(hasTray: boolean, host: Host): boolean {
  return hasTray && host === "present";
}

export type CloseAction = "hide" | "confirm-quit";

/** Closing the window hides it when a tray can reopen it; otherwise the app
 *  asks "Quit baum?" (`NO_TRAY_PROMPT`) and quits, leaving the daemon. */
export function closeAction(trayAvailable: boolean): CloseAction {
  return trayAvailable ? "hide" : "confirm-quit";
}

// ---------------------------------------------------------------------------
// Read scheduling.

/** The tray's requests, as a set: what a poll has to read again. */
export const Dirty = {
  NONE: 0,
  JOBS: 1,
  AUTOMATIC: 2,
  WAITING: 4,
  LIVE: 8,
  ALL: 15,
} as const;

export function dirtyContains(dirty: number, other: number): boolean {
  return (dirty & other) === other;
}

/** What an event kind can change among the tray's numbers: job state
 *  (`progress`: running, queued, failures), the pause switch (`automatic`),
 *  sessions waiting on the user (`live`) and the waiting pull request count
 *  (`index`). A missed replay window (`reset`) and a kind this code does not
 *  know could change anything, so they read everything. A `job` event, a
 *  running job's usage, and the kinds that concern only the window change
 *  none of them. */
export function dirtyBy(kind: string): number {
  switch (kind) {
    case "progress":
      return Dirty.JOBS;
    case "automatic":
      return Dirty.AUTOMATIC;
    case "index":
      return Dirty.WAITING;
    case "live":
      return Dirty.LIVE;
    case "job":
    case "chat":
    case "timeline":
    case "decisions":
    case "notice":
    case "":
      return Dirty.NONE;
    default:
      return Dirty.ALL;
  }
}

/** The wait in milliseconds before reconnecting after the `attempt`th failure
 *  in a row. */
export function backoff(attempt: number): number {
  return Math.min(STREAM_BACKOFF_FIRST * 2 ** Math.min(attempt, 8), STREAM_BACKOFF_MAX);
}

/** How long the poll waits for an event before looking anyway: not at all
 *  without a daemon, rarely while the stream delivers events. */
export function fallbackInterval(attached: boolean, streamUp: boolean): number | undefined {
  if (!attached) return undefined;
  return streamUp ? FALLBACK_STREAM_UP : FALLBACK_STREAM_DOWN;
}

/** Adds a full read to `dirty` once `interval` has passed since the last one,
 *  so events that keep arriving never push the safety net back indefinitely
 *  (a failed fetch would otherwise stay stale). */
export function withSafetyNet(
  dirty: number,
  sinceFull: number,
  interval: number | undefined,
): number {
  return interval !== undefined && sinceFull >= interval ? Dirty.ALL : dirty;
}

/** Time left until a periodic read is due: none is due yet for a daemon
 *  not read from, or one replaced since. */
export function untilDue(
  checked: { generation: number; at: number } | undefined,
  generation: number,
  interval: number,
  at: number,
): number {
  if (checked !== undefined && checked.generation === generation) {
    return Math.max(0, interval - Math.max(0, at - checked.at));
  }
  return 0;
}

/** After a wake, waits for the burst of events around it to end (a quiet
 *  `debounce`, or `max` in all) and runs once with everything the burst made
 *  dirty, so each request is read once however many events asked for it.
 *  Wakes during a run wait for it to end, then settle again. */
export class Settler {
  private pending: number = Dirty.NONE;
  private started: number | undefined;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private running = false;
  private stopped = false;

  constructor(
    private readonly run: (dirty: number) => Promise<void> | void,
    private readonly debounce = DEBOUNCE,
    private readonly max = DEBOUNCE_MAX,
  ) {}

  wake(dirty: number): void {
    if (this.stopped) return;
    this.pending |= dirty;
    if (this.running) return;
    const at = Date.now();
    this.started ??= at;
    const left = Math.max(0, this.max - (at - this.started));
    clearTimeout(this.timer);
    this.timer = setTimeout(() => void this.fire(), Math.min(this.debounce, left));
  }

  /** Reads `dirty` now, without waiting, and forgets what was pending. */
  async runNow(dirty: number): Promise<void> {
    if (this.stopped || this.running) return;
    clearTimeout(this.timer);
    this.timer = undefined;
    this.started = undefined;
    this.pending = Dirty.NONE;
    await this.execute(dirty);
  }

  get busy(): boolean {
    return this.running;
  }

  stop(): void {
    this.stopped = true;
    clearTimeout(this.timer);
  }

  private async fire(): Promise<void> {
    this.timer = undefined;
    this.started = undefined;
    const dirty = this.pending;
    this.pending = Dirty.NONE;
    await this.execute(dirty);
  }

  private async execute(dirty: number): Promise<void> {
    this.running = true;
    try {
      await this.run(dirty);
    } finally {
      this.running = false;
    }
    if (this.pending !== Dirty.NONE) this.wake(Dirty.NONE);
  }
}

/** One wake-up that is remembered if nobody waits yet. */
class Kick {
  private pending = false;
  private resolve: (() => void) | undefined;

  kick(): void {
    const resolve = this.resolve;
    if (resolve === undefined) this.pending = true;
    else resolve();
  }

  /** Resolves on a kick, or after `timeoutMs` when given. */
  wait(timeoutMs?: number): Promise<void> {
    if (this.pending) {
      this.pending = false;
      return Promise.resolve();
    }
    return new Promise((resolve) => {
      const done = () => {
        clearTimeout(timer);
        this.resolve = undefined;
        resolve();
      };
      const timer = timeoutMs === undefined ? undefined : setTimeout(done, timeoutMs);
      this.resolve = done;
    });
  }
}

/** Holds the last thing shown, so an unchanged one is not shown again. */
export interface Shown<T> {
  value: T | undefined;
}

/** Whether a computed view needs showing, and records it as shown. */
export function nextView<T>(shown: Shown<T>, view: T, force: boolean): T | undefined {
  if (!force && shown.value !== undefined && isDeepStrictEqual(shown.value, view)) {
    return undefined;
  }
  shown.value = view;
  return view;
}

/** The plan usage lines as the menu would show them now. Part of what counts
 *  as a change, so the reads (at least a minute apart) keep "resets in" current. */
export function usageTexts(view: View, at: number): string[] {
  return view.usage.map((meter) => meterText(meter, at));
}

/** A valid job id for `POST /api/jobs/{id}/seen`. */
export function jobSeenPath(id: string): string {
  if (!/^[A-Za-z0-9_-]+$/.test(id)) throw new Error(`not a job id: ${JSON.stringify(id)}`);
  return `/api/jobs/${id}/seen`;
}

// ---------------------------------------------------------------------------
// The controller.

/** What the tray asks of the rest of the app. */
export interface TrayEffects {
  showWindow(): void;
  openRoute(route: Route): void;
  showSetup(): void;
  quit(): void;
  quitAndStop(): void;
  /** An error dialog. */
  message(title: string, text: string): void;
  openUrl(url: string): Promise<void>;
  isNotificationId(id: string): boolean;
  onNotificationMenu(id: string): void;
  /** The Notifications submenu, built from the notifier's saved settings. */
  notificationsSubmenu(): MenuItemSpec | undefined;
  /** The attachment changed: what the notifier read from the daemon is stale. */
  detachNotifications(): void;
  /** A daemon event, for the notification worker. */
  handleEvent(event: DaemonEvent, daemon: Daemon): void;
  autostart: {
    enabled(): boolean;
    set(enabled: boolean): void | Promise<void>;
  };
  /** Shows a new view: the icon, tooltip, menu and the dock or taskbar
   *  badge. Called only when something changed. */
  apply(update: TrayUpdate): void;
}

export interface TrayUpdate {
  icon: Icon;
  tooltip: string;
  menu: MenuItemSpec[];
  /** The badge count; `undefined` clears it. */
  badge: number | undefined;
  login: boolean;
}

export interface TrayOptions {
  runner?: CommandRunner;
  platform?: string;
  log?: (message: string) => void;
}

export class TrayController {
  private readonly tracker = new Tracker();
  private readonly shown: Shown<[View, string[]]> = { value: undefined };
  private readonly runner: CommandRunner;
  private readonly platform: string;
  private readonly log: (message: string) => void;
  private readonly settler: Settler;
  private readonly streamKick = new Kick();
  private readonly periodicKick = new Kick();
  private readonly downWaiters = new Set<() => void>();
  private daemonNow: Daemon | undefined;
  private observed: Observed = { link: "connecting" };
  private generation = 0;
  private streamAbort: AbortController | undefined;
  private streamUp = false;
  private hasTray = false;
  private planRefreshRequested = false;
  private lastFull = Date.now();
  private fallbackTimer: ReturnType<typeof setTimeout> | undefined;
  private stopped = false;

  constructor(
    private readonly effects: TrayEffects,
    options: TrayOptions = {},
  ) {
    this.runner = options.runner ?? runBounded;
    this.platform = options.platform ?? process.platform;
    this.log = options.log ?? ((message) => console.error(message));
    this.settler = new Settler((dirty) => this.run(dirty));
  }

  /** Starts the reading loops. The first pass reads whatever is attached
   *  already. */
  start(): void {
    this.settler.wake(Dirty.ALL);
    void this.streamLoop();
    void this.periodicLoop();
  }

  /** Tests and shutdown. */
  stop(): void {
    this.stopped = true;
    this.settler.stop();
    clearTimeout(this.fallbackTimer);
    this.streamAbort?.abort();
    this.streamKick.kick();
    this.periodicKick.kick();
    for (const waiter of this.downWaiters) waiter();
  }

  /** A tray icon was created; whether the desktop shows it is asked again
   *  when it matters. */
  setHasTray(hasTray: boolean): void {
    this.hasTray = hasTray;
  }

  /** Whether a tray icon is showing, so the window may hide into it. The
   *  desktop is asked every time, never remembered: the host can appear after
   *  the app, and it can go away (the panel exits, an extension is disabled),
   *  after which hiding the window would leave nothing to reopen it. */
  async available(): Promise<boolean> {
    return trayUsable(this.hasTray, await hostCheck(this.runner, this.platform));
  }

  /** Once the window exists: waits (on a hidden start at login, up to
   *  `HOST_WAIT`) for the desktop to show tray icons, and shows the window
   *  when none will, since nothing else could. */
  async watchHost(hidden: boolean): Promise<void> {
    const deadline = Date.now() + (hidden ? HOST_WAIT : 0);
    while (!this.stopped && !(await this.available())) {
      if (Date.now() >= deadline) {
        this.log("baum desktop: this desktop shows no tray icons");
        if (hidden) this.effects.showWindow();
        return;
      }
      await new Promise((resolve) => setTimeout(resolve, 1_000));
    }
  }

  /** Sleeps between two health checks of the daemon: `short` normally, and up
   *  to `long` while the tray's event stream is open and receiving (its pings
   *  show the daemon alive; a daemon that dies closes it), ending early the
   *  moment the stream goes down so a dead daemon is still noticed at once. */
  healthPause(short: number, long: number): Promise<void> {
    const wait = this.streamUp ? long : short;
    return new Promise((resolve) => {
      const done = () => {
        clearTimeout(timer);
        this.downWaiters.delete(done);
        resolve();
      };
      const timer = setTimeout(done, wait);
      if (this.streamUp) this.downWaiters.add(done);
    });
  }

  /** The attached daemon, while one is attached. */
  daemon(): Daemon | undefined {
    return this.daemonNow;
  }

  /** The main process attached to a daemon. */
  attached(daemon: Daemon): void {
    this.daemonNow = daemon;
    this.generation += 1;
    this.streamAbort?.abort();
    // A new attachment may be a replacement daemon: what the last one said is
    // stale.
    this.observed = { link: "up" };
    this.render(false);
    this.streamKick.kick();
    this.periodicKick.kick();
    this.settler.wake(Dirty.ALL);
    this.armFallback();
  }

  /** The main process lost the daemon or failed to reach one. */
  detached(): void {
    this.setLink("down");
  }

  /** The main process is attaching. */
  connecting(): void {
    this.setLink("connecting");
  }

  private setLink(link: Link): void {
    this.effects.detachNotifications();
    this.daemonNow = undefined;
    this.streamAbort?.abort();
    this.observed.link = link;
    this.render(false);
    this.armFallback();
  }

  /** The window is showing: the diagnostics problems flagged have been seen.
   *  Failed jobs stay flagged until their alert is followed. */
  windowSeen(): void {
    this.tracker.acknowledge();
    this.render(false);
  }

  /** Rebuilds the tray menu from the current settings, after a window menu
   *  toggle changed one. */
  refresh(): void {
    this.render(true);
  }

  /** A click on the tray icon opens the panel or the menu: read the plan usage
   *  afresh so it is current when shown. The daemon still reads at most once a
   *  minute. Hosts that report no clicks (Linux) keep the periodic reading. */
  requestPlanRefresh(): void {
    this.planRefreshRequested = true;
    this.periodicKick.kick();
  }

  /** What the panel shows: the view last shown and the plans behind its
   *  meters. */
  snapshot(): { view: View; plans: Plan[] } | undefined {
    const view = this.shown.value?.[0];
    if (view === undefined) return undefined;
    // The view already left the figures out when the daemon is not up.
    const plans = view.usage.length === 0 ? [] : (this.observed.plans ?? []);
    return { view, plans };
  }

  /** Running and queued jobs, asked of the daemon now. `undefined` when it
   *  cannot be asked: the last poll may be seconds old, so it is not a
   *  substitute. */
  async jobCounts(): Promise<{ running: number; queued: number } | undefined> {
    const daemon = this.daemonNow;
    if (daemon === undefined) return undefined;
    try {
      const jobs = parseJobs(await daemon.get("/api/jobs", POLL_TIMEOUT));
      return { running: jobs.running, queued: jobs.queued };
    } catch {
      return undefined;
    }
  }

  /** Asks the poll to read the requests `dirty` names (a daemon event). */
  wake(dirty: number): void {
    this.settler.wake(dirty);
  }

  /** What a menu id does. */
  onMenu(id: string): void {
    const action = route(id, (candidate) => this.effects.isNotificationId(candidate));
    switch (action.kind) {
      case "open":
        return this.effects.showWindow();
      case "jobs":
        return this.effects.openRoute({ kind: "jobs" });
      case "alert":
        return this.followAlert(action.id);
      case "session":
        return this.effects.openRoute({ kind: "session", key: action.key });
      case "pause":
        return void this.togglePause();
      case "login":
        return void this.toggleLogin();
      case "setup":
        return this.effects.showSetup();
      case "report":
        return void this.reportProblem();
      case "quit":
        return this.effects.quit();
      case "quit-stop":
        return this.effects.quitAndStop();
      case "notification":
        return this.effects.onNotificationMenu(id);
      case "ignore":
        return;
    }
  }

  /** Opens a new GitHub issue in the browser with the version, OS and the
   *  daemon's redacted Diagnostics summary filled in (the daemon builds the
   *  link, so one redaction serves every surface). The user reads the form in
   *  the browser and nothing is sent until they submit it. Without a daemon,
   *  or when diagnostics fail, it opens the issue chooser instead. */
  private async reportProblem(): Promise<void> {
    let prefilled: string | undefined;
    if (this.daemonNow !== undefined) {
      try {
        prefilled = issueUrl(await this.daemonNow.get("/api/diagnostics", DIAGNOSTICS_TIMEOUT));
      } catch (e) {
        this.log(`baum desktop: no diagnostics for the problem report: ${message(e)}`);
      }
    }
    try {
      await this.effects.openUrl(prefilled ?? ISSUES_CHOOSE);
    } catch (e) {
      this.effects.message("Cannot open the browser", message(e));
    }
  }

  /** Flips start at login and shows the real result in both menus. */
  private async toggleLogin(): Promise<void> {
    const want = !this.effects.autostart.enabled();
    try {
      await this.effects.autostart.set(want);
    } catch (e) {
      this.effects.message("Cannot change start at login", message(e));
    }
    this.render(true);
  }

  /** Pauses or resumes automatic generation: the daemon's global switch. */
  private async togglePause(): Promise<void> {
    const daemon = this.daemonNow;
    const enabled = toggled(this.observed.automatic?.enabled);
    if (daemon === undefined || enabled === undefined) return;
    try {
      await daemon.put("/api/automatic", automaticRequest(enabled), POLL_TIMEOUT);
      this.settler.wake(Dirty.ALL);
    } catch (e) {
      this.effects.message("Cannot change automatic reviews", message(e));
    }
  }

  /** An alert was followed: records what it is about as seen, then goes where
   *  the user needs to look. */
  private followAlert(id: string): void {
    const jobs = this.tracker.acknowledgeAlert(id);
    this.recordSeen(jobs);
    this.render(false);
    if (id.startsWith(ALERT_JOB)) {
      return this.effects.openRoute({ kind: "job", id: id.slice(ALERT_JOB.length) });
    }
    switch (id) {
      case ALERT_JOBS:
        return this.effects.openRoute({ kind: "jobs" });
      case ALERT_DIAGNOSTICS:
        return this.effects.openRoute({ kind: "diagnostics" });
      default:
        // The window shows the connecting screen, which can restart the daemon.
        return this.effects.showWindow();
    }
  }

  /** Records failed jobs as seen in the daemon, so no client flags them
   *  again, and looks again once it has. */
  private recordSeen(jobs: string[]): void {
    const daemon = this.daemonNow;
    if (jobs.length === 0 || daemon === undefined) return;
    void (async () => {
      for (const id of jobs) {
        try {
          await daemon.post(jobSeenPath(id), "{}", POLL_TIMEOUT);
        } catch (e) {
          this.log(`baum desktop: cannot record job ${id} as seen: ${message(e)}`);
        }
      }
      this.settler.wake(Dirty.ALL);
    })();
  }

  /** Recomputes the view and shows it when it changed (or `force`, after
   *  start at login changed). */
  private render(force: boolean): void {
    const view = this.tracker.update(this.observed);
    const login = this.effects.autostart.enabled();
    const at = now();
    if (nextView(this.shown, [view, usageTexts(view, at)], force) === undefined) return;
    this.effects.apply({
      icon: view.icon,
      tooltip: tooltip(view),
      menu: withNotifications(menuSpec(view, login, at), this.effects.notificationsSubmenu()),
      badge: badge(view),
      login,
    });
  }

  /** The poll's safety net: a full read once the interval passes with no
   *  event, never with no daemon attached. */
  private armFallback(): void {
    clearTimeout(this.fallbackTimer);
    this.fallbackTimer = undefined;
    if (this.stopped) return;
    const interval = fallbackInterval(this.daemonNow !== undefined, this.streamUp);
    if (interval === undefined) return;
    const wait = Math.max(0, interval - (Date.now() - this.lastFull));
    this.fallbackTimer = setTimeout(() => {
      this.fallbackTimer = undefined;
      if (this.settler.busy) return;
      void this.settler.runNow(Dirty.ALL);
    }, wait);
  }

  private setStreamUp(up: boolean): void {
    this.streamUp = up;
    if (!up) for (const waiter of [...this.downWaiters]) waiter();
    this.armFallback();
  }

  /** One look at the daemon. A failed request forgets that answer, so the menu
   *  does not show counts it cannot vouch for; the connection watch in the
   *  main process decides when the daemon has gone. */
  private async run(requested: number): Promise<void> {
    const daemon = this.daemonNow;
    if (daemon === undefined) return;
    // Partial reads must not postpone the safety net for good.
    const dirty = withSafetyNet(
      requested,
      Date.now() - this.lastFull,
      fallbackInterval(true, this.streamUp),
    );
    if (dirty === Dirty.ALL) this.lastFull = Date.now();
    // Only what the events named is read; the rest keeps its last answer.
    const read = async <T>(
      wanted: number,
      request: () => Promise<T>,
    ): Promise<{ value: T | undefined } | undefined> => {
      if (!dirtyContains(dirty, wanted)) return undefined;
      return { value: await request().catch(() => undefined) };
    };
    const [jobs, automatic, waiting, needs] = await Promise.all([
      read(Dirty.JOBS, async () => parseJobs(await daemon.get("/api/jobs", POLL_TIMEOUT))),
      read(Dirty.AUTOMATIC, async () =>
        parseAutomatic(await daemon.get("/api/automatic", POLL_TIMEOUT)),
      ),
      read(Dirty.WAITING, async () =>
        parseWaiting(await daemon.get("/api/index?view=waiting", POLL_TIMEOUT)),
      ),
      read(Dirty.LIVE, async () => parseLive(await daemon.get("/api/live-sessions", POLL_TIMEOUT))),
    ]);
    // The answers of a daemon since replaced are stale.
    if (this.daemonNow !== daemon) return;
    if (jobs) this.observed.jobs = jobs.value;
    if (automatic) this.observed.automatic = automatic.value;
    if (waiting) this.observed.waiting = waiting.value;
    if (needs) this.observed.needs = needs.value;
    this.render(false);
    this.armFallback();
    // A failed job seen here but still unseen in the daemon: recording it
    // failed (the daemon was briefly unreachable, or could not save), so it
    // is recorded again until the daemon stops reporting it, rather than
    // hidden only until the app restarts.
    for (const id of this.tracker.unrecorded()) {
      try {
        await daemon.post(jobSeenPath(id), "{}", POLL_TIMEOUT);
      } catch (e) {
        this.log(`baum desktop: cannot record job ${id} as seen: ${message(e)}`);
      }
    }
  }

  /** Keeps the daemon's event stream open while a daemon is attached, waking
   *  the poll on events that change what the tray shows. It reconnects with
   *  backoff, and a connection (also a re-connection) wakes a read so nothing
   *  missed meanwhile stays missed. */
  private async streamLoop(): Promise<void> {
    let attempt = 0;
    let cursorGeneration = 0;
    let cursor: StreamCursor = { lastId: undefined };
    while (!this.stopped) {
      const daemon = this.daemonNow;
      const generation = this.generation;
      if (daemon === undefined) {
        attempt = 0;
        await this.streamKick.wait();
        continue;
      }
      if (cursorGeneration !== generation) {
        cursorGeneration = generation;
        cursor = { lastId: undefined };
      }
      const abort = new AbortController();
      this.streamAbort = abort;
      let failure: unknown;
      try {
        await daemon.stream(
          {
            onOpen: () => {
              attempt = 0;
              this.setStreamUp(true);
              this.settler.wake(Dirty.ALL);
            },
            onEvent: (event) => {
              const dirty = dirtyBy(event.name);
              if (dirty !== Dirty.NONE) this.settler.wake(dirty);
              this.effects.handleEvent(event, daemon);
            },
          },
          abort.signal,
          cursor,
        );
      } catch (e) {
        failure = e;
      }
      if (this.streamUp) {
        this.setStreamUp(false);
        // The fallback interval just shortened: look now.
        this.settler.wake(Dirty.ALL);
      }
      if (failure !== undefined && attempt > 0 && !abort.signal.aborted) {
        this.log(`baum desktop: event stream unavailable: ${message(failure)}`);
      }
      if (this.stopped) return;
      if (this.generation !== generation) {
        attempt = 0;
        continue;
      }
      const wait = backoff(attempt);
      attempt += 1;
      await this.streamKick.wait(wait);
    }
  }

  /** Reads what no event announces: diagnostics every 30 minutes and plan usage
   *  every 15 (from the daemon's cache, forced when the tray is clicked), each
   *  again for a new daemon. Sleeps until the next is due, and with no daemon
   *  until one is attached. */
  private async periodicLoop(): Promise<void> {
    let diagnostics: { generation: number; at: number } | undefined;
    let plans: { generation: number; at: number } | undefined;
    while (!this.stopped) {
      const generation = this.generation;
      const daemon = this.daemonNow;
      if (daemon === undefined) {
        await this.periodicKick.wait();
        continue;
      }
      const at = Date.now();
      const forced = this.planRefreshRequested;
      this.planRefreshRequested = false;
      const planWait = forced ? 0 : untilDue(plans, generation, PLAN_USAGE_INTERVAL, at);
      const diagnosticsWait = untilDue(diagnostics, generation, DIAGNOSTICS_INTERVAL, at);
      const wait = Math.min(planWait, diagnosticsWait);
      if (wait > 0) {
        await this.periodicKick.wait(wait);
        continue;
      }
      if (planWait === 0) {
        plans = { generation, at: Date.now() };
        // The figures are a convenience: a failed reading keeps the last one,
        // and a daemon without the route has none to show.
        try {
          const path = forced ? "/api/plan-usage?refresh=1" : "/api/plan-usage";
          const found = parsePlanUsage(await daemon.get(path, PLAN_USAGE_TIMEOUT));
          if (this.daemonNow === daemon) {
            this.observed.plans = found;
            this.render(false);
          }
        } catch {}
      }
      if (diagnosticsWait === 0) {
        diagnostics = { generation, at: Date.now() };
        // A failed or timed-out run leaves the last answer standing.
        try {
          const problems = parseDiagnostics(
            await daemon.get("/api/diagnostics", DIAGNOSTICS_TIMEOUT),
          );
          if (this.daemonNow === daemon) {
            this.observed.diagnostics = problems;
            this.render(false);
          }
        } catch {}
      }
    }
  }
}

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

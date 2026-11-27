// What the tray shows, computed from what the daemon reports. Nothing here
// touches the network or the window: the daemon's JSON goes in and a `View`
// comes out, so the rules are unit-tested without either.
//
// The icon has three states. Idle is the default. Running means a job is
// running or queued. Attention means something needs the user and they have
// not yet seen it: a job failed, a diagnostics check reports a problem, or
// the daemon is not running. Each is an `Alert`, which the menu and the
// panel offer as an item that goes where the user needs to look: the failed
// job, the jobs list, diagnostics, or the window (which offers to restart
// the daemon). Following an alert acknowledges what it is about; opening the
// window acknowledges diagnostics problems only. A missing daemon stays
// until it runs again. Every failed job the daemon reports without
// `seen_at` is flagged, including one that failed while the app was closed;
// following its alert records it (`POST /api/jobs/{id}/seen`), and a seen
// failed job is never flagged again.
//
// The menu and the panel also show what is left of the agents' plans
// (`GET /api/plan-usage`) and the agent sessions waiting for the user's
// permission or answer (`GET /api/live-sessions`).

type Json = Record<string, unknown>;

/** Parses a JSON object. The parse functions throw an Error whose message
 *  names what could not be read. */
function parseObject(body: string, what: string): Json {
  let value: unknown;
  try {
    value = JSON.parse(body);
  } catch (e) {
    throw new Error(`unreadable ${what}: ${(e as Error).message}`);
  }
  if (!isObject(value)) throw new Error(`unreadable ${what}: expected an object`);
  return value;
}

function isObject(value: unknown): value is Json {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

// The field readers are strict: a field of the wrong type fails the row, and
// a missing field takes its default only where the reader is given one.
function text(o: Json, key: string, fallback?: string): string {
  const v = o[key];
  if (v === undefined && fallback !== undefined) return fallback;
  if (typeof v !== "string") throw new Error(`${key} is not a string`);
  return v;
}

function optText(o: Json, key: string): string | undefined {
  const v = o[key];
  if (v === undefined || v === null) return undefined;
  if (typeof v !== "string") throw new Error(`${key} is not a string`);
  return v;
}

function optCount(o: Json, key: string): number | undefined {
  const v = o[key];
  if (v === undefined || v === null) return undefined;
  if (typeof v !== "number" || !Number.isInteger(v) || v < 0) {
    throw new Error(`${key} is not a count`);
  }
  return v;
}

function flag(o: Json, key: string): boolean {
  const v = o[key];
  if (v === undefined) return false;
  if (typeof v !== "boolean") throw new Error(`${key} is not a boolean`);
  return v;
}

function rows(o: Json, key: string): unknown[] {
  const v = o[key];
  if (v === undefined) return [];
  if (!Array.isArray(v)) throw new Error(`${key} is not a list`);
  return v;
}

/** A failed job the user has not seen, with what its alert names. */
export interface Failed {
  id: string;
  /** `fix` or `feature_build`; a review has none. */
  kind?: string | undefined;
  repository?: string | undefined;
  pr?: number | undefined;
}

/** "Review failed: acme/widgets #7". */
function failedLabel(failed: Failed): string {
  let what: string;
  switch (failed.kind) {
    case undefined:
      what = "Review failed";
      break;
    case "fix":
      what = "Fix failed";
      break;
    case "feature_build":
      what = "Build failed";
      break;
    default:
      what = "Job failed";
  }
  if (failed.repository !== undefined && failed.pr !== undefined && failed.pr > 0) {
    return `${what}: ${failed.repository} #${failed.pr}`;
  }
  if (failed.repository !== undefined) return `${what}: ${failed.repository}`;
  return what;
}

/** The queue as the tray needs it. */
export interface Jobs {
  running: number;
  queued: number;
  /** Failed jobs not yet seen, in the daemon's order. The daemon keeps
   *  finished jobs in its journal, with when the user saw a failure. */
  failed: Failed[];
}

/** `GET /api/jobs`, the current jobs: every running, queued and unseen failed
 *  job, with the latest finished ones, and `next` only when paged, which the
 *  tray does not ask for. A daemon without a runner answers
 *  `{"available":false}`, which has no jobs. */
export function parseJobs(body: string): Jobs {
  const parsed = parseObject(body, "jobs list");
  const jobs: Jobs = { running: 0, queued: 0, failed: [] };
  try {
    for (const row of rows(parsed, "jobs")) {
      if (!isObject(row)) throw new Error("a job is not an object");
      const state = text(row, "state", "");
      const id = text(row, "id", "");
      const kind = optText(row, "kind");
      const repository = optText(row, "repository");
      const pr = optCount(row, "pr");
      const seenAt = optText(row, "seen_at");
      if (state === "running") jobs.running += 1;
      else if (state === "queued") jobs.queued += 1;
      else if (state === "failed" && seenAt === undefined) {
        jobs.failed.push({ id, kind, repository, pr });
      }
    }
  } catch (e) {
    throw new Error(`unreadable jobs list: ${(e as Error).message}`);
  }
  return jobs;
}

/** `GET /api/index?view=waiting`: how many pull requests are in the user's
 *  Inbox, counted by the daemon as the app's Inbox lists them. */
export function parseWaiting(body: string): number {
  const parsed = parseObject(body, "waiting count");
  const waiting = parsed.waiting;
  if (waiting === undefined || waiting === null) {
    throw new Error("the daemon did not report a waiting count");
  }
  if (typeof waiting !== "number" || !Number.isInteger(waiting) || waiting < 0) {
    throw new Error("unreadable waiting count: not a count");
  }
  return waiting;
}

/** Automatic generation as the tray needs it. */
export interface Automatic {
  /** The global switch. */
  enabled: boolean;
}

/** `GET /api/automatic`. `undefined` when the daemon has no scheduler. */
export function parseAutomatic(body: string): Automatic | undefined {
  const parsed = parseObject(body, "automatic status");
  const enabled = parsed.enabled;
  if (enabled === undefined || enabled === null) return undefined;
  if (typeof enabled !== "boolean") throw new Error("unreadable automatic status: not a boolean");
  return { enabled };
}

/** The ids of the checks `GET /api/diagnostics` reports as problems
 *  (warnings do not count). A report that is a problem without naming a check
 *  counts as one problem named "report". */
export function parseDiagnostics(body: string): string[] {
  const parsed = parseObject(body, "diagnostics");
  const report = parsed.report;
  if (report === undefined || report === null) return [];
  if (!isObject(report)) throw new Error("unreadable diagnostics: report is not an object");
  const problems: string[] = [];
  try {
    const status = text(report, "status", "");
    for (const check of rows(report, "checks")) {
      if (!isObject(check)) throw new Error("a check is not an object");
      const id = text(check, "id", "");
      if (text(check, "status", "") === "problem") problems.push(id);
    }
    if (problems.length === 0 && status === "problem") problems.push("report");
  } catch (e) {
    throw new Error(`unreadable diagnostics: ${(e as Error).message}`);
  }
  return problems;
}

/** One window of a plan, as `GET /api/plan-usage` reports it. */
export interface PlanWindow {
  name: string;
  /** `null` when the window reset since the figure was taken. */
  used_percent: number | null;
  resets_at: string | null;
}

/** A plan the daemon could read, or one whose sign-in needs refreshing. The
 *  tray panel receives it as is: an absent optional field is left out. */
export interface Plan {
  provider: string;
  available: boolean;
  reason?: string;
  /** The reason asks the person to open the agent to refresh its sign-in. */
  needs_sign_in?: boolean;
  plan?: string;
  windows: PlanWindow[];
  fetched_at: string;
  /** Set on a provider's second plan: the Claude Code configuration
   *  directory or Codex home whose sign-in it is, home written as `~`. */
  config_dir?: string;
}

function planWindow(value: unknown): PlanWindow {
  if (!isObject(value)) throw new Error("a window is not an object");
  const used = value.used_percent;
  if (used !== undefined && used !== null && typeof used !== "number") {
    throw new Error("used_percent is not a number");
  }
  return {
    name: text(value, "name"),
    used_percent: used ?? null,
    resets_at: optText(value, "resets_at") ?? null,
  };
}

function plan(value: unknown): Plan {
  if (!isObject(value)) throw new Error("a plan is not an object");
  const result: Plan = {
    provider: text(value, "provider"),
    available: flag(value, "available"),
    windows: rows(value, "windows").map(planWindow),
    fetched_at: text(value, "fetched_at", ""),
  };
  const reason = optText(value, "reason");
  if (reason !== undefined) result.reason = reason;
  if (flag(value, "needs_sign_in")) result.needs_sign_in = true;
  const kind = optText(value, "plan");
  if (kind !== undefined) result.plan = kind;
  const dir = optText(value, "config_dir");
  if (dir !== undefined) result.config_dir = dir;
  return result;
}

/** `GET /api/plan-usage`: the plans it could read, each with at least one
 *  window, and those whose sign-in needs refreshing, with their reason and no
 *  windows. A provider that is not installed or has not run yet is left
 *  out, as is a plan in a shape this app does not know, so the tray shows
 *  only what it can. */
export function parsePlanUsage(body: string): Plan[] {
  const parsed = parseObject(body, "plan usage");
  let values: unknown[];
  try {
    values = rows(parsed, "plans");
  } catch (e) {
    throw new Error(`unreadable plan usage: ${(e as Error).message}`);
  }
  const plans: Plan[] = [];
  for (const value of values) {
    let read: Plan;
    try {
      read = plan(value);
    } catch {
      continue;
    }
    if (read.available) {
      if (read.windows.length > 0) plans.push(read);
      continue;
    }
    if (!needsSignIn(read)) continue;
    read.windows = [];
    plans.push(read);
  }
  return plans;
}

/** A plan the daemon could not read because the agent's sign-in expired:
 *  shown with its reason, since only the person can fix it. */
export function needsSignIn(p: Plan): boolean {
  return !p.available && p.needs_sign_in === true && p.reason !== undefined;
}

/** One plan window as the menu shows it. */
export interface Meter {
  /** "Claude Code · 5-hour". */
  label: string;
  /** The share left, as a whole percent; `undefined` after a reset the figure
   *  predates. */
  left: number | undefined;
  /** When it resets, in Unix seconds. */
  resetsAt: number | undefined;
  /** Said instead of a figure: why the plan cannot be read ("Open Codex to
   *  refresh its sign-in."), under the provider's name alone. */
  note: string | undefined;
}

/** "62% left · resets in 3 h". */
export function meterText(meter: Meter, now: number): string {
  if (meter.note !== undefined) return meter.note;
  if (meter.left === undefined) return "reset since baum last saw it";
  if (meter.resetsAt !== undefined) {
    return `${meter.left}% left · resets in ${span(meter.resetsAt - now)}`;
  }
  return `${meter.left}% left`;
}

function providerName(provider: string): string {
  switch (provider) {
    case "claude":
      return "Claude Code";
    case "codex":
      return "Codex";
    default:
      return provider;
  }
}

function capital(value: string): string {
  const [first, ...rest] = Array.from(value);
  return first === undefined ? "" : first.toUpperCase() + rest.join("");
}

function meters(plans: Plan[]): Meter[] {
  const result: Meter[] = [];
  for (const p of plans) {
    const name =
      p.config_dir !== undefined && p.config_dir !== ""
        ? `${providerName(p.provider)} (${p.config_dir})`
        : providerName(p.provider);
    if (needsSignIn(p)) {
      result.push({ label: name, left: undefined, resetsAt: undefined, note: p.reason });
    }
    for (const win of p.windows) {
      const used = win.used_percent;
      result.push({
        label: `${name} · ${capital(win.name)}`,
        left:
          used !== null && Number.isFinite(used)
            ? Math.min(100, Math.max(0, Math.round(100 - used)))
            : undefined,
        resetsAt: win.resets_at === null ? undefined : (parseTime(win.resets_at) ?? undefined),
        note: undefined,
      });
    }
  }
  return result;
}

/** "3 h", "25 min", "2 d": a span in seconds rounded to a readable unit, as
 *  the app rounds it. */
export function span(seconds: number): string {
  const minutes = Math.round(Math.max(seconds, 0) / 60);
  if (minutes < 60) return `${Math.max(minutes, 1)} min`;
  if (minutes < 48 * 60) return `${Math.round(minutes / 60)} h`;
  return `${Math.round(minutes / 1440)} d`;
}

const DIGITS = /^[0-9]+$/;

function digits(part: string): number | null {
  return DIGITS.test(part) ? Number.parseInt(part, 10) : null;
}

/** An RFC 3339 time, as Go writes one, in Unix seconds. */
export function parseTime(value: string): number | null {
  // Only ASCII can be a time; this keeps character and byte positions equal.
  if (value.length < 20 || /[^\x00-\x7f]/.test(value)) return null;
  if (
    value[4] !== "-" ||
    value[7] !== "-" ||
    (value[10] !== "T" && value[10] !== "t") ||
    value[13] !== ":" ||
    value[16] !== ":"
  ) {
    return null;
  }
  const year = digits(value.slice(0, 4));
  const month = digits(value.slice(5, 7));
  const day = digits(value.slice(8, 10));
  const hour = digits(value.slice(11, 13));
  const minute = digits(value.slice(14, 16));
  const second = digits(value.slice(17, 19));
  if (
    year === null ||
    month === null ||
    day === null ||
    hour === null ||
    minute === null ||
    second === null
  ) {
    return null;
  }
  if (month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59) return null;
  let zone = value.slice(19);
  if (zone.startsWith(".")) zone = zone.slice(1).replace(/^[0-9]+/, "");
  let offset: number;
  if (zone === "Z" || zone === "z") {
    offset = 0;
  } else if (zone.length === 6 && zone[3] === ":") {
    const sign = zone[0] === "+" ? 1 : zone[0] === "-" ? -1 : 0;
    const h = digits(zone.slice(1, 3));
    const m = digits(zone.slice(4, 6));
    if (sign === 0 || h === null || m === null) return null;
    offset = sign * (h * 3600 + m * 60);
  } else {
    return null;
  }
  // Days since 1970-01-01, by Howard Hinnant's days_from_civil.
  const y = month <= 2 ? year - 1 : year;
  const era = Math.floor(y / 400);
  const yoe = y - era * 400;
  const doy = Math.floor((153 * ((month + 9) % 12) + 2) / 5) + day - 1;
  const days =
    era * 146_097 + yoe * 365 + Math.floor(yoe / 4) - Math.floor(yoe / 100) + doy - 719_468;
  return days * 86_400 + hour * 3600 + minute * 60 + second - offset;
}

/** An agent session waiting for the user's permission or answer. */
export interface NeedsYou {
  /** The session's `agent:id` key, which opens its transcript. */
  key: string;
  /** "Claude needs your permission to use Bash", as the agent said it, or
   *  what kind of wait it is. */
  title: string;
  /** The session's name, else its repository. */
  detail: string;
}

function agentName(agent: string): string {
  switch (agent) {
    case "claude-code":
    case "claude_code":
      return "Claude Code";
    case "codex":
      return "Codex";
    case "cursor":
      return "Cursor";
    default:
      return agent;
  }
}

interface LiveRow {
  agent: string;
  sessionId: string;
  state: string;
  waitingReason: string | undefined;
  waitingSince: string | undefined;
  waitingMessage: string | undefined;
  title: string | undefined;
  repository: string;
}

function liveRow(value: unknown): LiveRow {
  if (!isObject(value)) throw new Error("a session is not an object");
  return {
    agent: text(value, "agent"),
    sessionId: text(value, "session_id"),
    state: text(value, "state", ""),
    waitingReason: optText(value, "waiting_reason"),
    waitingSince: optText(value, "waiting_since"),
    waitingMessage: optText(value, "waiting_message"),
    title: optText(value, "title"),
    repository: text(value, "repository", ""),
  };
}

/** `GET /api/live-sessions`: the sessions asking for a permission or an
 *  answer, the longest wait first. A session that only finished its turn is
 *  not one: that is no question, and the Sessions page lists it. */
export function parseLive(body: string): NeedsYou[] {
  const parsed = parseObject(body, "live sessions");
  let values: unknown[];
  try {
    values = rows(parsed, "sessions");
  } catch (e) {
    throw new Error(`unreadable live sessions: ${(e as Error).message}`);
  }
  const waiting: { since: number | null; needs: NeedsYou }[] = [];
  for (const value of values) {
    let row: LiveRow;
    try {
      row = liveRow(value);
    } catch {
      continue;
    }
    if (row.state === "ended" || row.sessionId === "") continue;
    const agent = agentName(row.agent);
    let fallback: string;
    switch (row.waitingReason) {
      case "permission":
        fallback = `${agent} needs your permission`;
        break;
      case "input":
        fallback = `${agent} is waiting for your answer`;
        break;
      default:
        continue;
    }
    const title =
      row.waitingMessage !== undefined && row.waitingMessage.trim() !== ""
        ? row.waitingMessage
        : fallback;
    const detail = row.title !== undefined && row.title.trim() !== "" ? row.title : row.repository;
    waiting.push({
      since: row.waitingSince === undefined ? null : parseTime(row.waitingSince),
      needs: { key: `${row.agent}:${row.sessionId}`, title, detail },
    });
  }
  // A wait with no known start sorts first.
  waiting.sort((a, b) => {
    if (a.since !== b.since) {
      if (a.since === null) return -1;
      if (b.since === null) return 1;
      return a.since - b.since;
    }
    return a.needs.key < b.needs.key ? -1 : a.needs.key > b.needs.key ? 1 : 0;
  });
  return waiting.map((w) => w.needs);
}

/** The body of the request that sets the global automatic switch. */
export function automaticRequest(enabled: boolean): string {
  return `{"enabled":${enabled}}`;
}

/** Whether the app is attached to a running daemon. */
export type Link = "connecting" | "up" | "down";

/** Everything last observed. */
export interface Observed {
  link: Link;
  jobs?: Jobs | undefined;
  automatic?: Automatic | undefined;
  /** Ids of the checks reporting a problem; `undefined` until diagnostics ran. */
  diagnostics?: string[] | undefined;
  /** Pull requests in the user's Inbox; `undefined` while unknown, such as a
   *  daemon without GitHub discovery. */
  waiting?: number | undefined;
  /** The plans the daemon could read; `undefined` until it answered. */
  plans?: Plan[] | undefined;
  /** Agent sessions waiting on the user; `undefined` while unknown. */
  needs?: NeedsYou[] | undefined;
}

export function defaultObserved(): Observed {
  return { link: "connecting" };
}

/** The tray icon. Only work in progress and problems colour it: the Inbox
 *  count is the dock badge's and the menu's, not the icon's, so the icon's
 *  dot always means something is running or needs the user. */
export type Icon = "idle" | "running" | "attention";

/** The menu id of an alert about one failed job, before the job's id. */
export const ALERT_JOB = "alert:job:";
/** The menu id of an alert about several failed jobs: opens the jobs list. */
export const ALERT_JOBS = "alert:jobs";
export const ALERT_DIAGNOSTICS = "alert:diagnostics";
/** The daemon is not running: opens the window, which offers to restart it. */
export const ALERT_DAEMON = "alert:daemon";
/** More failed jobs than this make one alert that opens the jobs list. */
const JOB_ALERTS = 3;

/** Something the icon is red for, and where to go about it. The menu and the
 *  panel show each as an item; `id` is its menu id. */
export interface Alert {
  id: string;
  text: string;
  /** What following it does: "Open job". */
  action: string;
}

/** Whether a menu id is an alert's. */
export function isAlert(id: string): boolean {
  if (id.startsWith(ALERT_JOB)) return id.length > ALERT_JOB.length;
  return id === ALERT_JOBS || id === ALERT_DIAGNOSTICS || id === ALERT_DAEMON;
}

export interface View {
  icon: Icon;
  /** The first menu line and the tooltip: what the daemon is doing. */
  summary: string;
  /** Each thing needing the user. Empty unless the icon is attention, and
   *  never empty when it is: everything red can be followed. */
  attention: Alert[];
  /** The global automatic switch; `undefined` while unknown, which disables
   *  the pause item. */
  automatic: boolean | undefined;
  running: number;
  queued: number;
  /** Pull requests in the Inbox while the daemon is up and said so. */
  waiting: number | undefined;
  /** Agent sessions waiting for the user's permission or answer. */
  needs: NeedsYou[];
  /** One line per readable plan window; empty when none can be read. */
  usage: Meter[];
}

/** "3 pull requests in your Inbox"; nothing when it is empty or not
 *  known, which is also when the badge is clear. */
export function waitingLabel(view: View): string | undefined {
  switch (view.waiting) {
    case undefined:
    case 0:
      return undefined;
    case 1:
      return "1 pull request in your Inbox";
    default:
      return `${view.waiting} pull requests in your Inbox`;
  }
}

/** The tray's tooltip: what the daemon is doing, then who waits on whom. */
export function tooltip(view: View): string {
  const waiting = waitingLabel(view);
  return waiting === undefined ? `baum: ${view.summary}` : `baum: ${view.summary} · ${waiting}`;
}

/** The dock or taskbar badge; `undefined` clears it, as zero does. */
export function badge(view: View): number | undefined {
  return view.waiting !== undefined && view.waiting > 0 ? view.waiting : undefined;
}

export function defaultView(): View {
  return new Tracker().update(defaultObserved());
}

/** Remembers what the user has already seen, so Attention clears and returns
 *  only for something new. Whether a failed job was seen is the daemon's
 *  record: every failed job it reports without `seen_at` is flagged, whenever
 *  it failed, until the user follows its alert. Those the tracker
 *  acknowledges are returned to the caller, which records them in the
 *  daemon; until the daemon says so, they are remembered here. */
export class Tracker {
  private acknowledged = new Set<string>();
  private current = new Set<string>();

  update(seen: Observed): View {
    const running = seen.jobs?.running ?? 0;
    const queued = seen.jobs?.queued ?? 0;
    const keys = new Set<string>();
    for (const f of seen.jobs?.failed ?? []) keys.add(`failed:${f.id}`);
    for (const id of seen.diagnostics ?? []) keys.add(`diagnostics:${id}`);
    // A problem that went away and comes back is new again. What a report
    // we do not have (a failed poll) would have said is not forgotten. A
    // failed job leaves the list once the daemon records it seen, and
    // never fails again.
    for (const k of [...this.acknowledged]) {
      const kept =
        keys.has(k) ||
        (k.startsWith("failed:") && seen.jobs === undefined) ||
        (k.startsWith("diagnostics:") && seen.diagnostics === undefined);
      if (!kept) this.acknowledged.delete(k);
    }
    this.current = new Set(keys);
    const unseen = (key: string) => keys.has(key) && !this.acknowledged.has(key);

    // Only a daemon that is up can be asked about what it reported.
    const attention: Alert[] = [];
    if (seen.link === "up") {
      const failed = (seen.jobs?.failed ?? []).filter((f) => unseen(`failed:${f.id}`));
      if (failed.length > JOB_ALERTS) {
        attention.push({
          id: ALERT_JOBS,
          text: `${failed.length} jobs failed`,
          action: "Open jobs",
        });
      } else {
        for (const f of failed) {
          attention.push({ id: `${ALERT_JOB}${f.id}`, text: failedLabel(f), action: "Open job" });
        }
      }
      const problems = [...keys].filter((k) => k.startsWith("diagnostics:") && unseen(k)).length;
      if (problems > 0) {
        attention.push({
          id: ALERT_DIAGNOSTICS,
          text:
            problems === 1
              ? "A diagnostics check reports a problem"
              : `${problems} diagnostics checks report a problem`,
          action: "Open diagnostics",
        });
      }
    }
    if (seen.link === "down") {
      attention.push({
        id: ALERT_DAEMON,
        text: "Background reviews are not running",
        action: "Open baum to restart them",
      });
    }

    const plans = seen.link === "up" ? seen.plans : undefined;
    let icon: Icon;
    let line: string;
    if (seen.link === "connecting") {
      [icon, line] = ["idle", "Connecting to baum…"];
    } else if (seen.link === "down") {
      [icon, line] = ["attention", "Background reviews are not running"];
    } else if (attention.length > 0) {
      [icon, line] = ["attention", summary(running, queued)];
    } else if (seen.jobs === undefined) {
      [icon, line] = ["idle", "Checking background reviews…"];
    } else if (running + queued > 0) {
      [icon, line] = ["running", summary(running, queued)];
    } else {
      [icon, line] = ["idle", summary(running, queued)];
    }
    return {
      icon,
      summary: line,
      attention,
      automatic: seen.automatic?.enabled,
      running,
      queued,
      // A daemon that is not running has nothing in the Inbox.
      waiting: seen.link === "up" ? seen.waiting : undefined,
      needs: seen.link === "up" ? (seen.needs ?? []) : [],
      usage: plans === undefined ? [] : meters(plans),
    };
  }

  /** The user opened the window: the diagnostics problems showing have been
   *  seen. Failed jobs have not: the window may show anything, and it
   *  opens on its own at launch, so a job is seen only by following its
   *  alert. */
  acknowledge(): void {
    this.mark(this.sorted().filter((k) => k.startsWith("diagnostics:")));
  }

  /** The user followed an alert: what it is about has been seen. Returns
   *  the failed jobs this made seen. A missing daemon is not something to
   *  acknowledge. */
  acknowledgeAlert(id: string): string[] {
    if (id.startsWith(ALERT_JOB)) return this.mark([`failed:${id.slice(ALERT_JOB.length)}`]);
    let prefix: string;
    if (id === ALERT_JOBS) prefix = "failed:";
    else if (id === ALERT_DIAGNOSTICS) prefix = "diagnostics:";
    else return [];
    return this.mark(this.sorted().filter((k) => k.startsWith(prefix)));
  }

  /** Failed jobs acknowledged here that the daemon still reports unseen:
   *  recording them failed or has not landed yet, so the caller records
   *  them again. Once the daemon has, it stops reporting them. */
  unrecorded(): string[] {
    return this.sorted()
      .filter((k) => this.acknowledged.has(k) && k.startsWith("failed:"))
      .map((k) => k.slice("failed:".length));
  }

  private sorted(): string[] {
    return [...this.current].sort();
  }

  private mark(keys: string[]): string[] {
    const jobs: string[] = [];
    for (const key of keys) {
      // Recorded again even when known here: the daemon may not have it yet,
      // and recording is idempotent.
      if (key.startsWith("failed:")) jobs.push(key.slice("failed:".length));
      this.acknowledged.add(key);
    }
    return jobs;
  }
}

function summary(running: number, queued: number): string {
  if (running === 0 && queued === 0) return "Idle";
  if (queued === 0) return `${running} running`;
  if (running === 0) return `${queued} queued`;
  return `${running} running, ${queued} queued`;
}

/** What the pause item says, from the global switch. */
export function pauseLabel(automatic: boolean | undefined): string {
  return automatic === false ? "Resume automatic reviews" : "Pause automatic reviews";
}

/** The value a click on the pause item sets: the opposite of what the daemon
 *  last reported. `undefined` while that is unknown. */
export function toggled(automatic: boolean | undefined): boolean | undefined {
  return automatic === undefined ? undefined : !automatic;
}

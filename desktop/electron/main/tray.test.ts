import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  type Automatic,
  defaultView,
  type Jobs,
  type Observed,
  parsePlanUsage,
  Tracker,
  type View,
} from "./status.ts";
import {
  type Action,
  backoff,
  type Bounded,
  closeAction,
  DEBOUNCE_MAX,
  type Daemon,
  Dirty,
  dirtyBy,
  dirtyContains,
  fallbackInterval,
  hostCheck,
  ISSUES_CHOOSE,
  issueUrl,
  type MenuItemSpec,
  MENU_LOGIN,
  MENU_REPORT,
  menuSpec,
  nextView,
  parseNameHasOwner,
  route,
  runBounded,
  SETUP_MENU_ID,
  SESSION_PREFIX,
  STREAM_BACKOFF_MAX,
  Settler,
  type Shown,
  toTemplate,
  TrayController,
  type TrayEffects,
  type TrayUpdate,
  trayUsable,
  untilDue,
  usageTexts,
  withNotifications,
  withSafetyNet,
} from "./tray.ts";

function view(observed: Observed): View {
  return new Tracker().update(observed);
}

function up(running: number, automatic: boolean): Observed {
  const jobs: Jobs = { running, queued: 0, failed: [] };
  const auto: Automatic = { enabled: automatic };
  return { link: "up", jobs, automatic: auto, diagnostics: [] };
}

const NOW = 1_793_016_000;

const labels = (spec: MenuItemSpec[]) =>
  spec.flatMap((e) => (e.kind === "separator" ? [] : [e.label]));

const find = (spec: MenuItemSpec[], id: string) => spec.find((e) => e.id === id);

const isNotification = (id: string) => id.startsWith("notifications:");
const actionOf = (id: string) => route(id, isNotification);

const enabled = (entry: MenuItemSpec | undefined) =>
  entry !== undefined && entry.kind !== "separator" ? entry.enabled : undefined;

const label = (entry: MenuItemSpec | undefined) =>
  entry !== undefined && entry.kind !== "separator" ? entry.label : undefined;

describe("read scheduling", () => {
  it("reads only what each event can change", () => {
    expect(dirtyBy("progress")).toBe(Dirty.JOBS);
    expect(dirtyBy("automatic")).toBe(Dirty.AUTOMATIC);
    expect(dirtyBy("index")).toBe(Dirty.WAITING);
    expect(dirtyBy("live")).toBe(Dirty.LIVE);
    for (const kind of ["reset", "something-new"]) {
      expect(dirtyBy(kind), kind).toBe(Dirty.ALL);
    }
    for (const kind of ["job", "chat", "timeline", "decisions", "notice", ""]) {
      expect(dirtyBy(kind), kind).toBe(Dirty.NONE);
    }
    expect(dirtyContains(Dirty.ALL, Dirty.JOBS | Dirty.LIVE)).toBe(true);
    expect(dirtyContains(Dirty.LIVE, Dirty.JOBS)).toBe(false);
  });

  it("backs reconnects off to a cap", () => {
    const waits = Array.from({ length: 8 }, (_, a) => backoff(a) / 1000);
    expect(waits).toEqual([1, 2, 4, 8, 16, 30, 30, 30]);
    expect(backoff(4_294_967_295)).toBe(STREAM_BACKOFF_MAX);
  });

  it("polls slowly with a stream and not at all without a daemon", () => {
    expect(fallbackInterval(false, true)).toBeUndefined();
    expect(fallbackInterval(false, false)).toBeUndefined();
    expect(fallbackInterval(true, true)).toBe(60_000);
    expect(fallbackInterval(true, false)).toBe(30_000);
  });

  it("makes a periodic read due at once for a new daemon and then after its interval", () => {
    const now = 1_000_000;
    const hour = 3_600_000;
    const interval = 300_000;
    expect(untilDue(undefined, 1, interval, now)).toBe(0);
    const read = { generation: 1, at: now };
    expect(untilDue(read, 2, interval, now + hour)).toBe(0);
    expect(untilDue(read, 1, interval, now + 100_000)).toBe(200_000);
    expect(untilDue(read, 1, interval, now + hour)).toBe(0);
  });

  it("does not let steady events postpone the full read", () => {
    const interval = 60_000;
    expect(withSafetyNet(Dirty.LIVE, 59_000, interval)).toBe(Dirty.LIVE);
    expect(withSafetyNet(Dirty.LIVE, 60_000, interval)).toBe(Dirty.ALL);
    expect(withSafetyNet(Dirty.NONE, 60_000, interval)).toBe(Dirty.ALL);
    expect(withSafetyNet(Dirty.LIVE, 60_000, undefined)).toBe(Dirty.LIVE);
  });
});

describe("settling a burst of events", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("settles a burst of wakes into one read of their union", async () => {
    const reads: number[] = [];
    const settler = new Settler((dirty) => {
      reads.push(dirty);
    });
    settler.wake(Dirty.JOBS);
    for (let i = 0; i < 5; i++) settler.wake(Dirty.LIVE);
    settler.wake(Dirty.WAITING);
    await vi.advanceTimersByTimeAsync(499);
    expect(reads).toEqual([]);
    await vi.advanceTimersByTimeAsync(1);
    expect(reads).toEqual([Dirty.JOBS | Dirty.LIVE | Dirty.WAITING]);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(reads).toHaveLength(1);
  });

  it("reads after the cap even while events keep arriving", async () => {
    const reads: number[] = [];
    const settler = new Settler((dirty) => {
      reads.push(dirty);
    });
    for (let t = 0; t < DEBOUNCE_MAX; t += 400) {
      settler.wake(Dirty.JOBS);
      await vi.advanceTimersByTimeAsync(400);
    }
    expect(reads).toEqual([Dirty.JOBS]);
  });

  it("keeps an event after the window for the next read", async () => {
    const reads: number[] = [];
    const settler = new Settler((dirty) => {
      reads.push(dirty);
    });
    settler.wake(Dirty.LIVE);
    await vi.advanceTimersByTimeAsync(500);
    expect(reads).toEqual([Dirty.LIVE]);
    settler.wake(Dirty.JOBS);
    await vi.advanceTimersByTimeAsync(500);
    expect(reads).toEqual([Dirty.LIVE, Dirty.JOBS]);
  });

  it("holds a wake that arrives during a read until it ends", async () => {
    const reads: number[] = [];
    let release: () => void = () => {};
    const settler = new Settler(
      (dirty) =>
        new Promise<void>((resolve) => {
          reads.push(dirty);
          release = resolve;
        }),
    );
    settler.wake(Dirty.LIVE);
    await vi.advanceTimersByTimeAsync(500);
    settler.wake(Dirty.JOBS);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(reads).toEqual([Dirty.LIVE]);
    release();
    await vi.advanceTimersByTimeAsync(500);
    expect(reads).toEqual([Dirty.LIVE, Dirty.JOBS]);
  });
});

describe("menu", () => {
  it("routes every menu id to exactly one action", () => {
    const ids = [
      ...menuSpec(defaultView(), false, NOW).map((e) => e.id),
      MENU_LOGIN,
      MENU_REPORT,
      SETUP_MENU_ID,
      "notifications:review_finished",
    ];
    for (const id of ids) {
      const action = actionOf(id);
      let expected: Action;
      switch (id) {
        case "open":
          expected = { kind: "open" };
          break;
        case "summary":
          expected = { kind: "jobs" };
          break;
        case "pause":
          expected = { kind: "pause" };
          break;
        case "login":
        case MENU_LOGIN:
          expected = { kind: "login" };
          break;
        case SETUP_MENU_ID:
          expected = { kind: "setup" };
          break;
        case "report":
        case MENU_REPORT:
          expected = { kind: "report" };
          break;
        case "quit":
          expected = { kind: "quit" };
          break;
        case "quit-stop":
          expected = { kind: "quit-stop" };
          break;
        default:
          expected = id.startsWith("notifications:")
            ? { kind: "notification" }
            : id.startsWith(SESSION_PREFIX)
              ? { kind: "session", key: id.slice(SESSION_PREFIX.length) }
              : { kind: "ignore" };
      }
      expect(action, id).toEqual(expected);
    }
    expect(actionOf("unknown")).toEqual({ kind: "ignore" });
    expect(actionOf("setup")).toEqual({ kind: "ignore" });
    expect(actionOf("session:")).toEqual({ kind: "ignore" });
    expect(actionOf("session:claude-code:a")).toEqual({
      kind: "session",
      key: "claude-code:a",
    });
  });

  // Every item's click goes to the one handler the template is built with, so
  // a click is handled once, however many menus hold it.
  it("sends every click, in the menu and in submenus, to the one handler", () => {
    const submenu: MenuItemSpec = {
      kind: "submenu",
      id: "notifications",
      label: "Notifications",
      enabled: true,
      items: [{ kind: "check", id: "notifications:a", label: "A", enabled: true, checked: true }],
    };
    const spec = withNotifications(menuSpec(view(up(2, true)), true, NOW), submenu);
    const clicked: string[] = [];
    const handler = (id: string) => clicked.push(id);
    let count = 0;
    const click = (items: ReturnType<typeof toTemplate>) => {
      for (const entry of items) {
        if (entry.type === "separator") continue;
        if (entry.submenu) click(entry.submenu);
        else {
          entry.click?.();
          count += 1;
        }
      }
    };
    click(toTemplate(spec, handler));
    expect(clicked).toHaveLength(count);
    expect(clicked).toContain("notifications:a");
    expect(new Set(clicked).size).toBe(clicked.length);
  });

  it("has the decided entries in order", () => {
    const spec = menuSpec(view(up(2, true)), false, NOW);
    expect(labels(spec)).toEqual([
      "2 running",
      "Open baum",
      "Pause automatic reviews",
      "Start at login",
      "Report a problem…",
      "Quit",
      "Quit and stop background reviews",
    ]);
    expect(spec.map((e) => e.id)).toEqual([
      "summary",
      "separator-1",
      "open",
      "pause",
      "login",
      "report",
      "separator-2",
      "quit",
      "quit-stop",
    ]);
  });

  it("puts Notifications above the last separator", () => {
    const submenu: MenuItemSpec = {
      kind: "submenu",
      id: "notifications",
      label: "Notifications",
      enabled: true,
      items: [],
    };
    const spec = menuSpec(view(up(0, true)), false, NOW);
    const ids = withNotifications(spec, submenu).map((e) => e.id);
    expect(ids.slice(-4)).toEqual(["notifications", "separator-2", "quit", "quit-stop"]);
    expect(withNotifications(spec, undefined)).toBe(spec);
  });

  it("opens the jobs list from the status only while reviews run or wait", () => {
    const idle = menuSpec(view(up(0, true)), false, NOW);
    expect([idle[0]?.id, enabled(idle[0])]).toEqual(["summary", false]);
    const busy = menuSpec(view(up(2, true)), false, NOW);
    expect(label(busy[0])).toBe("2 running");
    expect(enabled(busy[0])).toBe(true);
    expect(actionOf(busy[0]?.id ?? "")).toEqual({ kind: "jobs" });
    const queued = up(0, true);
    queued.jobs = { running: 0, queued: 1, failed: [] };
    expect(enabled(menuSpec(view(queued), false, NOW)[0])).toBe(true);
  });

  it("opens only the issue form of the public repository", () => {
    const form = "https://github.com/baum-ai/baum/issues/new?template=bug_report.yml&version=1";
    expect(issueUrl(`{"report_issue_url":"${form}"}`)).toBe(form);
    for (const body of [
      "{}",
      "not json",
      '{"report_issue_url":5}',
      '{"report_issue_url":"https://evil.example/issues/new?x=1"}',
      '{"report_issue_url":"https://github.com/baum-ai/baum.evil/issues/new?x=1"}',
      '{"report_issue_url":"http://github.com/baum-ai/baum/issues/new?x=1"}',
      '{"report_issue_url":"file:///etc/passwd"}',
    ]) {
      expect(issueUrl(body), body).toBeUndefined();
    }
  });

  it("says how many pull requests wait only when some do", () => {
    const observed = up(0, true);
    observed.waiting = 3;
    const spec = menuSpec(view(observed), false, NOW);
    expect(label(spec[0])).toBe("Idle");
    expect(spec[1]?.id).toBe("waiting");
    expect(label(spec[1])).toBe("3 pull requests in your Inbox");
    expect(enabled(spec[1])).toBe(false);
    for (const waiting of [0, undefined]) {
      observed.waiting = waiting;
      expect(menuSpec(view(observed), false, NOW).every((e) => e.id !== "waiting")).toBe(true);
    }
  });

  it("offers resume while paused and disables pause while unknown", () => {
    const paused = menuSpec(view(up(0, false)), false, NOW);
    expect(label(find(paused, "pause"))).toBe("Resume automatic reviews");
    expect(enabled(find(paused, "pause"))).toBe(true);
    const unknown = menuSpec(defaultView(), false, NOW);
    expect(enabled(find(unknown, "pause"))).toBe(false);
  });

  it("shows the start at login item's state", () => {
    for (const login of [false, true]) {
      const entry = find(menuSpec(defaultView(), login, NOW), "login");
      expect(entry?.kind).toBe("check");
      expect(entry?.kind === "check" && entry.checked).toBe(login);
    }
  });

  it("puts alerts under the summary as items that go where to look", () => {
    const observed = up(0, true);
    observed.diagnostics = ["git"];
    let spec = menuSpec(view(observed), false, NOW);
    expect(label(spec[0])).toBe("Idle");
    expect(label(spec[1])).toBe("A diagnostics check reports a problem · Open diagnostics");
    expect(enabled(spec[1])).toBe(true);
    expect(actionOf(spec[1]?.id ?? "")).toEqual({ kind: "alert", id: "alert:diagnostics" });
    spec = menuSpec(view({ link: "down" }), false, NOW);
    expect(label(spec[1])).toBe("Background reviews are not running · Open baum to restart them");
    expect(actionOf(spec[1]?.id ?? "")).toEqual({ kind: "alert", id: "alert:daemon" });
    expect(actionOf("alert:job:6e280a68")).toEqual({ kind: "alert", id: "alert:job:6e280a68" });
  });

  it("shows the menu again when a reset time reads differently", () => {
    const observed = up(0, true);
    observed.plans = parsePlanUsage(
      `{"plans":[{"provider":"claude","available":true,"windows":[
        {"name":"5-hour","used_percent":38,"resets_at":"2026-10-26T12:30:00Z"}],
        "fetched_at":"2026-10-26T12:00:00Z"}]}`,
    );
    const shown = view(observed);
    const rendered: Shown<[View, string[]]> = { value: undefined };
    const at = (now: number): [View, string[]] => [shown, usageTexts(shown, now)];
    expect(nextView(rendered, at(NOW), false)).toBeDefined();
    // Within the same minute nothing changes; a minute on, "30 min" is "29 min".
    expect(nextView(rendered, at(NOW + 10), false)).toBeUndefined();
    expect(nextView(rendered, at(NOW + 60), false)).toBeDefined();
  });

  it("puts plan usage lines under the status only when some can be read", () => {
    const observed = up(0, true);
    observed.plans = parsePlanUsage(
      `{"plans":[{"provider":"claude","available":true,"windows":[
        {"name":"5-hour","used_percent":38,"resets_at":"2026-10-26T15:00:00Z"},
        {"name":"weekly","used_percent":74,"resets_at":"2026-10-28T12:00:00Z"}],
        "fetched_at":"2026-10-26T11:40:00Z"}]}`,
    );
    let spec = menuSpec(view(observed), false, NOW);
    expect(labels(spec).slice(0, 4)).toEqual([
      "Idle",
      "Plan usage left",
      "Claude Code · 5-hour: 62% left · resets in 3 h",
      "Claude Code · Weekly: 26% left · resets in 2 d",
    ]);
    expect(spec.filter((e) => e.id.startsWith("usage")).every((e) => !enabled(e))).toBe(true);
    // An expired sign-in says what to open, under the same heading.
    observed.plans = parsePlanUsage(
      `{"plans":[{"provider":"codex","available":false,"needs_sign_in":true,
        "reason":"Open Codex to refresh its sign-in.","windows":[],
        "fetched_at":"2026-10-26T11:40:00Z"}]}`,
    );
    spec = menuSpec(view(observed), false, NOW);
    expect(labels(spec).slice(0, 3)).toEqual([
      "Idle",
      "Plan usage left",
      "Codex: Open Codex to refresh its sign-in.",
    ]);
    // Neither agent readable: no heading, no separator.
    observed.plans = [];
    spec = menuSpec(view(observed), false, NOW);
    expect(spec.every((e) => !e.id.startsWith("usage") && e.id !== "separator-usage")).toBe(true);
  });

  it("makes a session waiting on the user an item that opens it", () => {
    const observed = up(0, true);
    observed.needs = [
      {
        key: "claude-code:a",
        title: "Claude needs your permission to use Bash",
        detail: "Tidy the app's buttons",
      },
    ];
    const spec = menuSpec(view(observed), false, NOW);
    const entry = spec[1];
    expect(entry?.id).toBe("session:claude-code:a");
    expect(label(entry)).toBe("Claude needs your permission to use Bash · Tidy the app's buttons");
    expect(enabled(entry)).toBe(true);
    expect(actionOf(entry?.id ?? "")).toEqual({ kind: "session", key: "claude-code:a" });
  });

  it("shows an unchanged view again only when forced", () => {
    const rendered: Shown<View> = { value: undefined };
    const idle = defaultView();
    expect(nextView(rendered, idle, false)).toEqual(idle);
    expect(nextView(rendered, idle, false)).toBeUndefined();
    expect(nextView(rendered, idle, true)).toEqual(idle);
    const busy = view(up(1, true));
    expect(nextView(rendered, busy, false)).toEqual(busy);
  });
});

describe.skipIf(process.platform === "win32")("the tray host check", () => {
  it("cuts off a stalled bus query and counts it as unknown", async () => {
    const started = Date.now();
    const result = await runBounded("sleep", ["30"], 200);
    expect(result).toEqual({ kind: "timed_out" });
    expect(Date.now() - started).toBeLessThan(5_000);
    // Unknown never lets the window hide.
    expect(trayUsable(true, "unknown")).toBe(false);
    const stalled = async (): Promise<Bounded> => ({ kind: "timed_out" });
    expect(await hostCheck(stalled, "linux")).toBe("unknown");
  });

  it("returns a bounded query's answer and status", async () => {
    expect(await runBounded("sh", ["-c", "echo '(true,)'"], 5_000)).toEqual({
      kind: "done",
      success: true,
      stdout: "(true,)\n",
    });
    expect(await runBounded("sh", ["-c", "exit 1"], 5_000)).toMatchObject({
      kind: "done",
      success: false,
    });
    expect(await runBounded("/nonexistent/busctl", [], 1_000)).toEqual({ kind: "not_run" });
  });
});

describe("the tray host", () => {
  it("hides the window only into a tray the desktop shows now", () => {
    expect(trayUsable(true, "present")).toBe(true);
    // The watcher went away since the last check.
    expect(trayUsable(true, "absent")).toBe(false);
    expect(trayUsable(true, "unknown")).toBe(false);
    expect(trayUsable(false, "present")).toBe(false);
  });

  it("reads Linux tray hosts from the bus answer", () => {
    expect(parseNameHasOwner("(true,)\n")).toBe(true);
    expect(parseNameHasOwner("(false,)\n")).toBe(false);
    expect(parseNameHasOwner("")).toBeUndefined();
  });

  it("asks busctl, then gdbus when busctl is missing", async () => {
    const calls: string[] = [];
    const runner =
      (answers: Record<string, Bounded>) =>
      async (program: string): Promise<Bounded> => {
        calls.push(program);
        return answers[program] ?? { kind: "not_run" };
      };
    const done = (success: boolean, stdout = ""): Bounded => ({ kind: "done", success, stdout });
    expect(await hostCheck(runner({ busctl: done(true) }), "linux")).toBe("present");
    expect(await hostCheck(runner({ busctl: done(false) }), "linux")).toBe("absent");
    expect(calls).toEqual(["busctl", "busctl"]);
    calls.length = 0;
    expect(await hostCheck(runner({ gdbus: done(true, "(true,)") }), "linux")).toBe("present");
    expect(await hostCheck(runner({ gdbus: done(true, "(false,)") }), "linux")).toBe("absent");
    expect(await hostCheck(runner({}), "linux")).toBe("unknown");
    expect(calls).toEqual(["busctl", "gdbus", "busctl", "gdbus", "busctl", "gdbus"]);
    // Other desktops always have a tray host.
    expect(await hostCheck(runner({}), "darwin")).toBe("present");
  });

  it("quits after asking only where no tray can reopen the window", () => {
    expect(closeAction(true)).toBe("hide");
    expect(closeAction(false)).toBe("confirm-quit");
  });
});

describe("the controller", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  interface Rig {
    controller: TrayController;
    updates: TrayUpdate[];
    requests: string[];
    puts: string[];
    posts: string[];
    routes: unknown[];
    daemon: Daemon;
  }

  function rig(answers: Record<string, string>): Rig {
    const updates: TrayUpdate[] = [];
    const requests: string[] = [];
    const puts: string[] = [];
    const posts: string[] = [];
    const routes: unknown[] = [];
    const effects: TrayEffects = {
      showWindow: () => routes.push("window"),
      openRoute: (r) => routes.push(r),
      showSetup: () => {},
      quit: () => {},
      quitAndStop: () => {},
      message: () => {},
      openUrl: async (url) => {
        routes.push(url);
      },
      isNotificationId: isNotification,
      onNotificationMenu: () => {},
      notificationsSubmenu: () => undefined,
      detachNotifications: () => {},
      handleEvent: () => {},
      autostart: { enabled: () => false, set: () => {} },
      apply: (u) => updates.push(u),
    };
    const daemon: Daemon = {
      get: async (path) => {
        requests.push(path);
        const answer = answers[path];
        if (answer === undefined) throw new Error(`${path} answered 404`);
        return answer;
      },
      put: async (path, body) => {
        puts.push(`${path} ${body}`);
        return "{}";
      },
      post: async (path, body) => {
        posts.push(`${path} ${body}`);
        return "{}";
      },
      stream: (_handlers, signal) =>
        new Promise<void>((resolve) => signal.addEventListener("abort", () => resolve())),
    };
    const controller = new TrayController(effects, { platform: "linux", log: () => {} });
    return { controller, updates, requests, puts, posts, routes, daemon };
  }

  const ANSWERS = {
    "/api/jobs": '{"jobs":[{"id":"a","state":"running"},{"id":"f","state":"failed"}]}',
    "/api/automatic": '{"enabled":true}',
    "/api/index?view=waiting": '{"waiting":2}',
    "/api/live-sessions": '{"sessions":[]}',
    "/api/diagnostics":
      '{"report":null,"report_issue_url":"https://github.com/baum-ai/baum/issues/new?x=1"}',
    "/api/plan-usage": '{"plans":[]}',
    "/api/plan-usage?refresh=1": '{"plans":[]}',
  };

  it("reads everything once on attaching, shows the result, and records seen alerts", async () => {
    const { controller, updates, requests, posts, routes, daemon } = rig(ANSWERS);
    controller.start();
    controller.attached(daemon);
    await vi.advanceTimersByTimeAsync(600);
    expect(requests.filter((r) => r === "/api/jobs")).toHaveLength(1);
    expect(requests).toEqual(
      expect.arrayContaining([
        "/api/jobs",
        "/api/automatic",
        "/api/index?view=waiting",
        "/api/live-sessions",
        "/api/diagnostics",
        "/api/plan-usage",
      ]),
    );
    const last = updates.at(-1);
    expect(last?.icon).toBe("attention");
    expect(last?.badge).toBe(2);
    expect(last?.tooltip).toBe("baum: 1 running · 2 pull requests in your Inbox");
    expect(controller.snapshot()?.view.summary).toBe("1 running");

    controller.onMenu("alert:job:f");
    expect(routes).toEqual([{ kind: "job", id: "f" }]);
    await vi.advanceTimersByTimeAsync(0);
    expect(posts).toEqual(["/api/jobs/f/seen {}"]);
    controller.stop();
  });

  it("reads only the request an event names, and forces plan usage on a click", async () => {
    const { controller, requests, daemon } = rig(ANSWERS);
    controller.start();
    controller.attached(daemon);
    await vi.advanceTimersByTimeAsync(600);
    requests.length = 0;
    controller.wake(dirtyBy("live"));
    controller.wake(dirtyBy("job"));
    await vi.advanceTimersByTimeAsync(600);
    expect(requests).toEqual(["/api/live-sessions"]);
    requests.length = 0;
    controller.requestPlanRefresh();
    await vi.advanceTimersByTimeAsync(0);
    expect(requests).toEqual(["/api/plan-usage?refresh=1"]);
    controller.stop();
  });

  it("reads again on the safety net interval, and diagnostics every 30 minutes", async () => {
    const { controller, requests, daemon } = rig(ANSWERS);
    controller.start();
    controller.attached(daemon);
    await vi.advanceTimersByTimeAsync(600);
    requests.length = 0;
    // The stream never opens here, so the quicker 30 s interval applies.
    await vi.advanceTimersByTimeAsync(30_000);
    expect(requests.filter((r) => r === "/api/jobs")).toHaveLength(1);
    expect(requests).not.toContain("/api/diagnostics");
    await vi.advanceTimersByTimeAsync(15 * 60_000);
    expect(requests).toContain("/api/plan-usage");
    expect(requests).not.toContain("/api/diagnostics");
    await vi.advanceTimersByTimeAsync(15 * 60_000);
    expect(requests).toContain("/api/diagnostics");
    controller.stop();
  });

  it("pauses through the daemon and reports a problem through the issue form", async () => {
    const { controller, puts, routes, daemon } = rig(ANSWERS);
    controller.start();
    controller.attached(daemon);
    await vi.advanceTimersByTimeAsync(600);
    controller.onMenu("pause");
    await vi.advanceTimersByTimeAsync(0);
    expect(puts).toEqual(['/api/automatic {"enabled":false}']);
    controller.onMenu("report");
    await vi.advanceTimersByTimeAsync(0);
    expect(routes).toEqual(["https://github.com/baum-ai/baum/issues/new?x=1"]);
    controller.detached();
    controller.onMenu("report");
    await vi.advanceTimersByTimeAsync(0);
    expect(routes.at(-1)).toBe(ISSUES_CHOOSE);
    controller.stop();
  });

  it("shows the missing daemon as an alert that opens the window", async () => {
    const { controller, updates, routes, daemon } = rig(ANSWERS);
    controller.start();
    controller.attached(daemon);
    await vi.advanceTimersByTimeAsync(600);
    controller.detached();
    expect(updates.at(-1)?.icon).toBe("attention");
    expect(updates.at(-1)?.badge).toBeUndefined();
    controller.onMenu("alert:daemon");
    expect(routes).toEqual(["window"]);
    controller.stop();
  });
});

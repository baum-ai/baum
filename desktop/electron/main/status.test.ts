import { describe, expect, it } from "vitest";
import {
  isAlert,
  type Jobs,
  type Observed,
  automaticRequest,
  badge,
  defaultObserved,
  meterText,
  parseAutomatic,
  parseDiagnostics,
  parseJobs,
  parseLive,
  parsePlanUsage,
  parseTime,
  parseWaiting,
  pauseLabel,
  span,
  toggled,
  Tracker,
  tooltip,
  type View,
  waitingLabel,
} from "./status.ts";

function jobs(running: number, queued: number, failed: string[]): Jobs {
  return { running, queued, failed: failed.map((id) => ({ id })) };
}

const texts = (view: View) => view.attention.map((a) => a.text);
const ids = (view: View) => view.attention.map((a) => a.id);

function up(j: Jobs): Observed {
  return {
    link: "up",
    jobs: j,
    automatic: { enabled: true },
    diagnostics: [],
    waiting: undefined,
    plans: undefined,
    needs: undefined,
  };
}

describe("status", () => {
  it("parses job counts and failures", () => {
    const body = `{"available":true,"jobs":[
      {"id":"a","state":"running","phase":"x"},
      {"id":"b","state":"queued"},{"id":"c","state":"queued"},
      {"id":"d","state":"failed","reason":"boom"},
      {"id":"e","state":"completed"},{"id":"f","state":"cancelled"},{"id":"g","state":"interrupted"}]}`;
    expect(parseJobs(body)).toEqual(jobs(1, 2, ["d"]));
    expect(parseJobs('{"available":false,"code":"runner_unavailable"}')).toEqual({
      running: 0,
      queued: 0,
      failed: [],
    });
    expect(() => parseJobs("not json")).toThrow();
  });

  it("parses the current jobs and ignores a next cursor", () => {
    const body = `{"available":true,"next":"older","jobs":[
      {"id":"a","state":"completed","kind":"fix","repository":"acme/widgets","pr":7},
      {"id":"b","state":"running"},
      {"id":"c","state":"failed","seen_at":"2026-11-03T09:00:00Z"},
      {"id":"d","state":"failed","kind":"fix","repository":"acme/widgets","pr":8}]}`;
    const parsed = parseJobs(body);
    expect([parsed.running, parsed.queued]).toEqual([1, 0]);
    expect(parsed.failed.map((f) => f.id)).toEqual(["d"]);
  });

  it("does not flag a failed job the daemon records as seen", () => {
    const body = `{"jobs":[
      {"id":"a","state":"failed","repository":"acme/widgets","pr":7},
      {"id":"b","state":"failed","seen_at":"2026-11-03T09:00:00Z"},
      {"id":"c","state":"failed","kind":"fix","repository":"acme/widgets","pr":8}]}`;
    expect(parseJobs(body).failed).toEqual([
      { id: "a", kind: undefined, repository: "acme/widgets", pr: 7 },
      { id: "c", kind: "fix", repository: "acme/widgets", pr: 8 },
    ]);
  });

  it("makes a new failure an alert that opens the job", () => {
    const tracker = new Tracker();
    tracker.update(up(jobs(0, 0, [])));
    const failed = jobs(0, 0, []);
    failed.failed = [
      { id: "a", kind: undefined, repository: "acme/widgets", pr: 7 },
      { id: "b", kind: "feature_build", repository: "acme/widgets", pr: 0 },
    ];
    let view = tracker.update(up(failed));
    expect(view.icon).toBe("attention");
    expect(ids(view)).toEqual(["alert:job:a", "alert:job:b"]);
    expect(texts(view)).toEqual(["Review failed: acme/widgets #7", "Build failed: acme/widgets"]);
    expect(view.attention[0]?.action).toBe("Open job");
    // Following one acknowledges that job only, and says to record it.
    expect(tracker.acknowledgeAlert("alert:job:a")).toEqual(["a"]);
    view = tracker.update(up(failed));
    expect(ids(view)).toEqual(["alert:job:b"]);
    expect(tracker.acknowledgeAlert("alert:job:b")).toEqual(["b"]);
    view = tracker.update(up(failed));
    expect(view.icon).toBe("idle");
    expect(view.attention).toEqual([]);
  });

  it("records a seen failure the daemon still reports again", () => {
    const tracker = new Tracker();
    tracker.update(up(jobs(0, 0, ["a", "b"])));
    expect(tracker.unrecorded()).toEqual([]);
    tracker.acknowledgeAlert("alert:job:a");
    // Recording failed: the daemon still reports it, and the alert stays
    // hidden while it is recorded again.
    const view = tracker.update(up(jobs(0, 0, ["a", "b"])));
    expect(ids(view)).toEqual(["alert:job:b"]);
    expect(tracker.unrecorded()).toEqual(["a"]);
    // Once recorded, the daemon no longer reports it.
    tracker.update(up(jobs(0, 0, ["b"])));
    expect(tracker.unrecorded()).toEqual([]);
  });

  it("makes many failures one alert that opens the jobs list", () => {
    const tracker = new Tracker();
    tracker.update(up(jobs(0, 0, [])));
    const seen = up(jobs(0, 0, ["a", "b", "c", "d"]));
    const view = tracker.update(seen);
    expect(ids(view)).toEqual(["alert:jobs"]);
    expect(texts(view)).toEqual(["4 jobs failed"]);
    const recorded = tracker.acknowledgeAlert("alert:jobs");
    recorded.sort();
    expect(recorded).toEqual(["a", "b", "c", "d"]);
    expect(tracker.update(seen).icon).toBe("idle");
  });

  it("gives every red icon an alert to follow", () => {
    const tracker = new Tracker();
    const cases: Observed[] = [{ ...defaultObserved(), link: "down" }, up(jobs(0, 0, []))];
    (cases[1] as Observed).diagnostics = ["git"];
    for (const seen of cases) {
      const view = tracker.update(seen);
      expect(view.icon).toBe("attention");
      expect(view.attention.length).toBeGreaterThan(0);
      expect(view.attention.every((a) => isAlert(a.id))).toBe(true);
    }
    tracker.update(up(jobs(0, 0, [])));
    const view = tracker.update(up(jobs(1, 0, ["x"])));
    expect(view.icon).toBe("attention");
    expect(view.attention.every((a) => isAlert(a.id))).toBe(true);
  });

  it("acknowledges the problems only when the diagnostics alert is followed", () => {
    const tracker = new Tracker();
    tracker.update(up(jobs(0, 0, [])));
    const seen = up(jobs(0, 0, ["x"]));
    seen.diagnostics = ["git"];
    const view = tracker.update(seen);
    expect(ids(view)).toEqual(["alert:job:x", "alert:diagnostics"]);
    expect(tracker.acknowledgeAlert("alert:diagnostics")).toEqual([]);
    expect(ids(tracker.update(seen))).toEqual(["alert:job:x"]);
    // A missing daemon is not acknowledged by following its alert.
    expect(tracker.acknowledgeAlert("alert:daemon")).toEqual([]);
  });

  it("does not see a failed job when the window opens", () => {
    // The window opens on its own at launch, so only following the
    // alert sees the job.
    const tracker = new Tracker();
    const seen = up(jobs(0, 0, ["x"]));
    seen.diagnostics = ["git"];
    tracker.update(seen);
    tracker.acknowledge();
    const view = tracker.update(seen);
    expect(view.icon).toBe("attention");
    expect(ids(view)).toEqual(["alert:job:x"]);
  });

  it("treats only alert ids as alerts", () => {
    for (const id of ["alert:job:a", "alert:jobs", "alert:diagnostics", "alert:daemon"]) {
      expect(isAlert(id), id).toBe(true);
    }
    for (const id of ["alert:job:", "alert:", "alert:other", "open", "session:a"]) {
      expect(isAlert(id), id).toBe(false);
    }
  });

  it("parses the global switch", () => {
    // An older daemon's daily cap fields are ignored.
    const body = `{"enabled":false,"repositories":[
      {"repository":"a/b","enabled":true,"daily_cap":5.0,"paused_until":"2026-10-21T00:00:00Z","notice":"daily_cap","pending":0},
      {"repository":"a/c","enabled":true,"notice":"unavailable","pending":0}]}`;
    expect(parseAutomatic(body)).toEqual({ enabled: false });
    expect(parseAutomatic('{"available":false,"code":"automatic_unavailable"}')).toBeUndefined();
  });

  it("counts only a problem as a diagnostics problem", () => {
    const report = (s: string) => `{"local":true,"report":{"status":"${s}","checks":[]}}`;
    expect(parseDiagnostics(report("problem"))).toEqual(["report"]);
    expect(parseDiagnostics(report("warning"))).toEqual([]);
    expect(parseDiagnostics(report("ok"))).toEqual([]);
    expect(parseDiagnostics('{"local":true,"report":null}')).toEqual([]);
    const checks = `{"report":{"status":"problem","checks":[
      {"id":"git","status":"problem"},{"id":"gh","status":"warning"},{"id":"agent","status":"problem"}]}}`;
    expect(parseDiagnostics(checks)).toEqual(["git", "agent"]);
  });

  it("shows idle, running and queued", () => {
    const tracker = new Tracker();
    let view = tracker.update(up(jobs(0, 0, [])));
    expect([view.icon, view.summary]).toEqual(["idle", "Idle"]);
    view = tracker.update(up(jobs(1, 0, [])));
    expect([view.icon, view.summary]).toEqual(["running", "1 running"]);
    view = tracker.update(up(jobs(2, 3, [])));
    expect(view.summary).toBe("2 running, 3 queued");
    view = tracker.update(up(jobs(0, 2, [])));
    expect([view.icon, view.summary]).toEqual(["running", "2 queued"]);
  });

  it("needs attention for a failure until its alert is followed", () => {
    const tracker = new Tracker();
    let view = tracker.update(up(jobs(0, 0, ["old"])));
    expect(view.icon).toBe("attention");
    expect(texts(view)).toEqual(["Review failed"]);
    expect(tracker.acknowledgeAlert("alert:job:old")).toEqual(["old"]);
    view = tracker.update(up(jobs(0, 0, ["old"])));
    expect(view.icon).toBe("idle");
    expect(view.attention).toEqual([]);
    view = tracker.update(up(jobs(0, 0, ["old", "new"])));
    expect(ids(view)).toEqual(["alert:job:new"]);
  });

  it("still needs attention for failures found on the first look", () => {
    // The daemon reports only failed jobs not yet seen, so one that
    // failed while the app was closed is still news.
    const tracker = new Tracker();
    const view = tracker.update(up(jobs(0, 0, ["a", "b"])));
    expect(view.icon).toBe("attention");
    expect(ids(view)).toEqual(["alert:job:a", "alert:job:b"]);
  });

  it("lets attention win over running and keeps the job summary", () => {
    const tracker = new Tracker();
    tracker.update(up(jobs(0, 0, [])));
    const view = tracker.update(up(jobs(1, 1, ["x", "y"])));
    expect(view.icon).toBe("attention");
    expect(view.summary).toBe("1 running, 1 queued");
    expect(ids(view)).toEqual(["alert:job:x", "alert:job:y"]);
  });

  it("makes each problem its own attention", () => {
    const tracker = new Tracker();
    const seen = up(jobs(0, 0, []));
    seen.diagnostics = ["git"];
    tracker.update(seen);
    tracker.acknowledge();
    expect(tracker.update(seen).icon).toBe("idle");
    // A second problem is new.
    seen.diagnostics = ["git", "gh"];
    const view = tracker.update(seen);
    expect(texts(view)).toEqual(["A diagnostics check reports a problem"]);
    expect(view.attention[0]?.action).toBe("Open diagnostics");
  });

  it("does not make seen failures new after a failed poll", () => {
    const tracker = new Tracker();
    tracker.update(up(jobs(0, 0, ["old"])));
    tracker.acknowledgeAlert("alert:job:old");
    const gap = up(jobs(0, 0, []));
    gap.jobs = undefined;
    gap.automatic = undefined;
    let view = tracker.update(gap);
    expect(view.summary).toBe("Checking background reviews…");
    expect(view.automatic).toBeUndefined();
    view = tracker.update(up(jobs(0, 0, ["old"])));
    expect(view.icon).toBe("idle");
  });

  it("needs attention for a diagnostics problem and again after it clears", () => {
    const tracker = new Tracker();
    const seen = up(jobs(0, 0, []));
    seen.diagnostics = ["git"];
    expect(tracker.update(seen).icon).toBe("attention");
    tracker.acknowledge();
    expect(tracker.update(seen).icon).toBe("idle");
    seen.diagnostics = [];
    tracker.update(seen);
    seen.diagnostics = ["git"];
    expect(tracker.update(seen).icon).toBe("attention");
  });

  it("needs attention for a missing daemon and not for connecting", () => {
    const tracker = new Tracker();
    const down: Observed = { ...defaultObserved(), link: "down" };
    let view = tracker.update(down);
    expect(view.icon).toBe("attention");
    expect(view.summary).toBe("Background reviews are not running");
    expect(ids(view)).toEqual(["alert:daemon"]);
    expect(view.attention[0]?.action).toBe("Open baum to restart them");
    tracker.acknowledge();
    view = tracker.update(down);
    expect(view.icon).toBe("attention");
    view = tracker.update(defaultObserved());
    expect(view.icon).toBe("idle");
  });

  it("reads the waiting count from the daemon's answer", () => {
    expect(parseWaiting('{"waiting":3}')).toBe(3);
    expect(parseWaiting('{"waiting":0}')).toBe(0);
    expect(() => parseWaiting('{"entries":[]}')).toThrow();
    expect(() => parseWaiting("nope")).toThrow();
  });

  it("shows the Inbox count in the tooltip and the badge but not the icon", () => {
    const waiting = (link: Observed["link"], count: number | undefined) =>
      new Tracker().update({ ...defaultObserved(), link, waiting: count });
    const view = waiting("up", 3);
    expect(waitingLabel(view)).toBe("3 pull requests in your Inbox");
    expect(tooltip(view)).toBe(
      "baum: Checking background reviews… · 3 pull requests in your Inbox",
    );
    expect(badge(view)).toBe(3);
    expect(view.icon).toBe("idle");
    expect(waitingLabel(waiting("up", 1))).toBe("1 pull request in your Inbox");
    // Zero clears the badge and says nothing; so does not knowing, and a
    // daemon that is not running.
    for (const quiet of [
      waiting("up", 0),
      waiting("up", undefined),
      waiting("down", 5),
      waiting("connecting", 5),
    ]) {
      expect(waitingLabel(quiet)).toBeUndefined();
      expect(badge(quiet)).toBeUndefined();
      expect(tooltip(quiet)).not.toContain("Inbox");
    }
  });

  it("leaves the icon to work and problems when pull requests wait", () => {
    const busy = up(jobs(1, 0, []));
    busy.waiting = 2;
    expect(new Tracker().update(busy).icon).toBe("running");
    const tracker = new Tracker();
    const quiet = up(jobs(0, 0, []));
    quiet.waiting = 2;
    expect(tracker.update(quiet).icon).toBe("idle");
    quiet.diagnostics = ["disk"];
    const view = tracker.update(quiet);
    expect(view.icon).toBe("attention");
    expect(badge(view)).toBe(2);
  });

  it("follows the global switch with the pause item", () => {
    expect(pauseLabel(true)).toBe("Pause automatic reviews");
    expect(pauseLabel(false)).toBe("Resume automatic reviews");
    expect(toggled(true)).toBe(false);
    expect(toggled(false)).toBe(true);
    expect(toggled(undefined)).toBeUndefined();
    expect(automaticRequest(false)).toBe('{"enabled":false}');
    expect(automaticRequest(true)).toBe('{"enabled":true}');
  });

  const PLANS = `{"plans":[
    {"provider":"claude","available":true,"windows":[
        {"name":"5-hour","used_percent":38,"resets_at":"2026-10-26T15:00:00Z"},
        {"name":"weekly","used_percent":74.4,"resets_at":"2026-10-28T12:00:00.123+01:00"}],
     "fetched_at":"2026-10-26T12:00:00Z"},
    {"provider":"codex","available":true,"plan":"pro","windows":[
        {"name":"weekly","used_percent":null,"resets_at":"2026-10-26T10:00:00Z","reset":true}],
     "fetched_at":"2026-10-26T11:40:00Z"}]}`;

  it("turns plan windows into menu lines", () => {
    const observed = up(jobs(0, 0, []));
    observed.plans = parsePlanUsage(PLANS);
    const view = new Tracker().update(observed);
    const now = parseTime("2026-10-26T12:00:00Z") as number;
    const lines = view.usage.map((m) => [m.label, meterText(m, now)]);
    expect(lines).toEqual([
      ["Claude Code · 5-hour", "62% left · resets in 3 h"],
      ["Claude Code · Weekly", "26% left · resets in 47 h"],
      ["Codex · Weekly", "reset since baum last saw it"],
    ]);
    // A daemon that is not running vouches for no figures.
    observed.link = "down";
    expect(new Tracker().update(observed).usage).toEqual([]);
  });

  it("names a second plan by its configuration directory", () => {
    const body = `{"plans":[
      {"provider":"claude","available":true,"windows":[{"name":"5-hour","used_percent":38}]},
      {"provider":"claude","available":true,"config_dir":"~/.claude.personal",
       "windows":[{"name":"5-hour","used_percent":10}]},
      {"provider":"codex","available":true,"windows":[{"name":"5-hour","used_percent":20}]},
      {"provider":"codex","available":true,"config_dir":"~/.codex-work",
       "windows":[{"name":"5-hour","used_percent":30}]}]}`;
    const observed = up(jobs(0, 0, []));
    observed.plans = parsePlanUsage(body);
    const labels = new Tracker().update(observed).usage.map((m) => m.label);
    expect(labels).toEqual([
      "Claude Code · 5-hour",
      "Claude Code (~/.claude.personal) · 5-hour",
      "Codex · 5-hour",
      "Codex (~/.codex-work) · 5-hour",
    ]);
  });

  it("keeps only plans that could be read", () => {
    const body = `{"plans":[
      {"provider":"claude","available":false,"reason":"Claude Code is not signed in on this computer.","windows":[],"fetched_at":"2026-10-26T12:00:00Z"},
      {"provider":"codex","available":true,"windows":[],"fetched_at":"2026-10-26T12:00:00Z"},
      {"provider":"codex","available":true,"windows":"weekly"},
      42]}`;
    expect(parsePlanUsage(body)).toEqual([]);
    expect(parsePlanUsage("{}")).toEqual([]);
    expect(() => parsePlanUsage("not json")).toThrow();
    const observed = up(jobs(0, 0, []));
    observed.plans = parsePlanUsage(body);
    expect(new Tracker().update(observed).usage).toEqual([]);
  });

  it("keeps an expired sign-in as a line saying what to open", () => {
    const body = `{"plans":[
      {"provider":"claude","available":false,"needs_sign_in":true,"reason":"Open Claude Code to refresh its sign-in.","windows":[{"name":"weekly","used_percent":1}],"fetched_at":"2026-10-26T12:00:00Z"},
      {"provider":"codex","available":false,"needs_sign_in":true,"windows":[],"fetched_at":"2026-10-26T12:00:00Z"}]}`;
    const plans = parsePlanUsage(body);
    expect(plans).toHaveLength(1);
    expect(plans[0]?.windows).toEqual([]);
    const observed = up(jobs(0, 0, []));
    observed.plans = plans;
    const view = new Tracker().update(observed);
    const lines = view.usage.map((m) => [m.label, meterText(m, 0)]);
    expect(lines).toEqual([["Claude Code", "Open Claude Code to refresh its sign-in."]]);
  });

  it("parses RFC 3339 times with any offset", () => {
    expect(parseTime("1970-01-01T00:00:00Z")).toBe(0);
    expect(parseTime("2026-10-26T12:00:00Z")).toBe(1_793_016_000);
    expect(parseTime("2026-10-26T13:30:00.5+01:30")).toBe(parseTime("2026-10-26T12:00:00Z"));
    expect(parseTime("2024-02-29T00:00:00-00:00")).toBe(1_709_164_800);
    for (const bad of [
      "",
      "2026-10-26",
      "2026-13-01T00:00:00Z",
      "2026-10-26T12:00:00",
      "2026-10-26T12:00:00+0100",
    ]) {
      expect(parseTime(bad), bad).toBeNull();
    }
  });

  it("rounds spans as the app rounds them", () => {
    expect(span(10)).toBe("1 min");
    expect(span(25 * 60)).toBe("25 min");
    expect(span(5 * 3600)).toBe("5 h");
    expect(span(72 * 3600)).toBe("3 d");
    expect(span(-5)).toBe("1 min");
  });

  it("needs the user for sessions asking for permission or an answer", () => {
    const body = `{"sessions":[
      {"agent":"claude-code","session_id":"b","state":"idle","waiting_reason":"input","waiting_since":"2026-10-26T12:05:00Z","repository":"acme/demo"},
      {"agent":"claude-code","session_id":"a","state":"running","waiting_reason":"permission","waiting_since":"2026-10-26T12:00:00Z","waiting_message":"Claude needs your permission to use Bash","title":"Tidy the app's buttons","repository":"acme/demo"},
      {"agent":"codex","session_id":"c","state":"idle","waiting_reason":"turn_finished","repository":"acme/demo"},
      {"agent":"codex","session_id":"d","state":"ended","waiting_reason":"permission","repository":"acme/demo"},
      {"agent":"codex","session_id":"e","state":"running","repository":"acme/demo"},
      {"session_id":"f"}]}`;
    expect(parseLive(body)).toEqual([
      {
        key: "claude-code:a",
        title: "Claude needs your permission to use Bash",
        detail: "Tidy the app's buttons",
      },
      {
        key: "claude-code:b",
        title: "Claude Code is waiting for your answer",
        detail: "acme/demo",
      },
    ]);
    expect(parseLive('{"available":false}')).toEqual([]);
    const observed = up(jobs(0, 0, []));
    observed.needs = parseLive(body);
    expect(new Tracker().update(observed).needs).toHaveLength(2);
    observed.link = "down";
    expect(new Tracker().update(observed).needs).toEqual([]);
  });
});

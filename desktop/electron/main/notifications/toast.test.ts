import { describe, expect, it } from "vitest";
import {
  fromEvent,
  fromMenuId,
  KINDS,
  MAX_TITLE,
  menuId,
  type Route,
  routePr,
  routeSession,
  routeUrl,
  type Toast,
} from "./toast.ts";

function notice(extra: string): string {
  return `{"repository":"acme/widgets","pr":7,"clone":"c1","title":"Fix <b>the</b> thing & more"${extra}}`;
}

function toast(kind: string, extra: string): Toast {
  const data = notice(extra).replace("{", `{"kind":"${kind}",`);
  const t = fromEvent("notice", data);
  if (!t) throw new Error("a toast");
  return t;
}

const c1: Route = { type: "pr", clone: "c1", number: 7 };

describe("toast", () => {
  it("review finished names the risk score", () => {
    const t = toast("review_finished", ',"risk":4,"risk_status":"complete"');
    expect(t.kind).toBe("finished");
    expect(t.title).toBe("Review finished: risk 4 of 5");
    // The title is GitHub's, unchanged.
    expect(t.body).toBe("Fix <b>the</b> thing & more\nacme/widgets #7");
    expect(t.route).toEqual(c1);
  });

  it("a lower bound score says so and no score is left out", () => {
    const t = toast("review_finished", ',"risk":3,"risk_status":"lower_bound"');
    expect(t.title).toBe("Review finished: risk at least 3 of 5");
    expect(toast("review_finished", "").title).toBe("Review finished");
    expect(toast("review_finished", ',"risk":9').title).toBe("Review finished");
  });

  it("review failed names the reason", () => {
    const t = toast("review_failed", ',"reason":"provider_timeout"');
    expect(t.kind).toBe("failed");
    expect(t.title).toBe("Review failed: provider timeout");
    expect(toast("review_failed", "").title).toBe("Review failed");
  });

  it("a failed details retry says what failed", () => {
    const t = toast("details_failed", ',"reason":"budget"');
    expect(t.kind).toBe("failed");
    expect(t.title).toBe("Couldn't write the missing section details: budget");
    expect(toast("details_failed", "").title).toBe("Couldn't write the missing section details");
  });

  it("review requested and automatic", () => {
    const t = toast("review_requested", "");
    expect([t.kind, t.title]).toEqual(["requested", "Review requested from you"]);
    const a = toast("review_automatic", ',"risk":2,"risk_status":"complete"');
    expect(a.kind).toBe("automatic");
    expect(a.title).toBe("Review generated automatically: risk 2 of 5");
  });

  it("news about my pull request leads with the reference", () => {
    const cases = [
      ["checks_failed", "checks_failed", "Checks failing"],
      ["conflict", "conflict", "Merge conflict"],
      ["changes_requested", "changes_requested", "Changes requested"],
      ["ready_to_merge", "ready_to_merge", "Ready to merge"],
    ] as const;
    for (const [name, kind, title] of cases) {
      const t = toast(name, "");
      expect(t.kind).toBe(kind);
      expect(t.title).toBe(`${title} · acme/widgets #7`);
      expect(t.body).toBe("Fix <b>the</b> thing & more");
      expect(t.route).toEqual(c1);
    }
    // Without a title the body still names the pull request.
    const data = '{"kind":"conflict","repository":"acme/widgets","pr":7}';
    expect(fromEvent("notice", data)?.body).toBe("acme/widgets #7");
  });

  it("fix notices name the commit or the reason", () => {
    const t = toast("fix_pushed", ',"commit":"0123456"');
    expect(t.kind).toBe("fix_pushed");
    expect(t.title).toBe("Fix pushed (0123456) · acme/widgets #7");
    expect(t.body).toBe("Fix <b>the</b> thing & more");
    expect(toast("fix_pushed", "").title).toBe("Fix pushed · acme/widgets #7");
    const f = toast("fix_failed", ',"reason":"conflicts_remain"');
    expect(f.kind).toBe("fix_failed");
    expect(f.title).toBe("Fix failed: conflicts remain · acme/widgets #7");
    expect(toast("fix_failed", "").title).toBe("Fix failed · acme/widgets #7");
  });

  it("a waiting session names its agent and opens its transcript", () => {
    let data =
      '{"kind":"session_permission","repository":"acme/demo","pr":7,"agent":"claude-code","session":"s-1","branch":"feature","message":"Claude needs your permission to use Bash"}';
    let t = fromEvent("notice", data);
    if (!t) throw new Error("a toast");
    expect(t.kind).toBe("session_permission");
    expect(t.title).toBe("Claude Code needs your permission · acme/demo");
    expect(t.body).toBe("Claude needs your permission to use Bash\nfeature · #7");
    expect(t.route).toEqual({ type: "session", key: "claude-code:s-1" });
    expect(routePr(t.route)).toBeUndefined();
    expect(routeSession(t.route)).toBe("claude-code:s-1");
    expect(routeUrl(t.route, new URL("http://127.0.0.1:1234/s/ns/")).search).toBe(
      "?page=sessions&session=claude-code%3As-1",
    );
    // Codex sends no message; a branch without a pull request is named alone.
    data =
      '{"kind":"session_input","repository":"acme/demo","pr":0,"agent":"codex","session":"c-1","branch":"main"}';
    t = fromEvent("notice", data);
    if (!t) throw new Error("a toast");
    expect(t.kind).toBe("session_input");
    expect(t.title).toBe("Codex is waiting for your answer · acme/demo");
    expect(t.body).toBe("It asked you a question.\nmain");
    data =
      '{"kind":"session_permission","repository":"acme/demo","pr":0,"agent":"codex","session":"c-1"}';
    expect(fromEvent("notice", data)?.body).toBe("It asks to run a tool.");
    // Without the session it names, there is nothing to open.
    expect(
      fromEvent("notice", '{"kind":"session_input","repository":"acme/demo","agent":"codex"}'),
    ).toBeUndefined();
    // The message is cleaned like a title.
    const long = JSON.stringify({
      kind: "session_permission",
      repository: "acme/demo",
      agent: "claude-code",
      session: "s",
      message: `use\u001b[0m‮ Bash ${"x".repeat(400)}`,
    });
    const cleaned = fromEvent("notice", long);
    expect(cleaned?.body.startsWith("use [0m Bash xx")).toBe(true);
    expect(Array.from(cleaned?.body ?? "")).toHaveLength(MAX_TITLE);
  });

  it("a notice without a clone opens the repository pull request", () => {
    const data = '{"kind":"review_requested","repository":"acme/widgets","pr":7}';
    const t = fromEvent("notice", data);
    if (!t) throw new Error("a toast");
    expect(t.route).toEqual({ type: "repository_pr", repository: "acme/widgets", number: 7 });
    expect(routeUrl(t.route, new URL("http://127.0.0.1:1234/s/ns/")).search).toBe(
      "?repository=acme%2Fwidgets&pr=7",
    );
    expect(t.body).toBe("acme/widgets #7");
  });

  it("text is stripped of control characters and bounded", () => {
    const data = JSON.stringify({
      kind: "review_finished",
      repository: "acme/widgets",
      pr: 1,
      clone: "c",
      title: `a\u001b[31m\r\nb\t${"x".repeat(500)}`,
    });
    const t = fromEvent("notice", data);
    const title = t?.body.split("\n")[0] ?? "";
    expect(title.startsWith("a [31m b xxx")).toBe(true);
    expect(Array.from(title)).toHaveLength(MAX_TITLE);
    // biome-ignore lint/suspicious/noControlCharactersInRegex: that is the check
    expect(/[\u0000-\u0009\u000b-\u001f\u007f-\u009f]/.test(t?.body ?? "")).toBe(false);
  });

  it("format characters are stripped", () => {
    const data = JSON.stringify({
      kind: "review_finished",
      repository: "acme/widgets",
      pr: 1,
      clone: "c",
      title: "safe‮gnp.exe​⁦x﻿",
    });
    expect(fromEvent("notice", data)?.body.split("\n")[0]).toBe("safegnp.exex");
  });

  it("unknown and malformed events call for nothing", () => {
    expect(fromEvent("index", "{}")).toBeUndefined();
    expect(fromEvent("progress", '{"job":{}}')).toBeUndefined();
    expect(fromEvent("notice", "not json")).toBeUndefined();
    expect(fromEvent("notice", '{"kind":"something_new","pr":1}')).toBeUndefined();
    expect(fromEvent("notice", '{"kind":"review_finished"}')).toBeUndefined();
    expect(fromEvent("notice", '{"kind":"review_finished","pr":"7"}')).toBeUndefined();
  });

  it("an automatic status is not a notification", () => {
    // An older daemon's daily cap pause is news no more.
    const paused =
      '{"repository":"acme/widgets","enabled":true,"daily_cap":5.0,"spent_today":5.5,"paused_until":"2026-10-21T00:00:00Z","notice":"daily_cap","pending":0}';
    expect(fromEvent("automatic", paused)).toBeUndefined();
  });

  it("routes become app urls", () => {
    const base = new URL("http://127.0.0.1:4100/s/abc/");
    const pr: Route = { type: "pr", clone: "c 1&x", number: 7 };
    expect(routeUrl(pr, base).href).toBe("http://127.0.0.1:4100/s/abc/?clone=c+1%26x&pr=7");
    expect(routeUrl({ type: "jobs" }, base).href).toBe("http://127.0.0.1:4100/s/abc/?page=jobs");
  });

  it("menu ids round trip", () => {
    for (const kind of KINDS) expect(fromMenuId(menuId(kind))).toBe(kind);
    expect(fromMenuId("notifications:other")).toBeUndefined();
  });
});

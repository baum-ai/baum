import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { UrlStateProvider, type ViewerLinkProps } from "@baum/viewer/navigation";
import {
  activeText,
  AnalyticsPage,
  AnalyticsReport,
  costText,
  nextRead,
  parseAnalytics,
  waitText,
  windowDays,
  type Analytics,
} from "./analytics";
import { niceMax } from "./analytics-charts";

const Link = (props: ViewerLinkProps) => <a {...props} />;
const noCost = { usd: null, incomplete: true, reasons: ["The price list has no price for x-1."] };

/** A report with everything unknown that can be. */
function report(over: Partial<Analytics> = {}): Analytics {
  return {
    window: { days: 7, start: "2026-10-21T00:00:00Z", end: "2026-10-27T12:00:00Z" },
    filters: { repository: "", agent: "" },
    options: { repositories: ["acme/demo"], agents: ["claude_code", "codex"] },
    indexing: { done: 4, total: 4, complete: true },
    summary: {
      sessions: 2,
      active_seconds: null,
      active_incomplete: true,
      prompts: 3,
      subagents: 0,
      cost: noCost,
      agent_share: null,
    },
    daily: [
      { date: "2026-10-26", agents: { claude_code: { sessions: 1, active_seconds: 0 } } },
      { date: "2026-10-27", agents: { codex: { sessions: 1, active_seconds: 0 } } },
    ],
    hours: Array.from({ length: 24 }, (_, hour) => ({ hour, active_seconds: 0 })),
    by_agent: [
      {
        key: "codex",
        sessions: 1,
        prompts: 1,
        tool_calls: 0,
        failed_tool_calls: 0,
        failed_rate: null,
        active_seconds: null,
        tokens: null,
        tokens_incomplete: true,
        cost: noCost,
      },
    ],
    by_model: [],
    by_repository: [],
    longest: [],
    commits: {
      available: false,
      reasons: ["The clone of acme/demo has no git user.email, so baum cannot tell your commits."],
      commits: 0,
      agent_commits: 0,
      by_session: 0,
      by_checkpoint: 0,
      by_trailer: 0,
      share: null,
      by_repository: [],
    },
    baum: {
      available: true,
      reviews: 0,
      group_reviews: 0,
      fixes: 0,
      automatic: 0,
      completed: 0,
      failed: 0,
      cancelled: 0,
      active: 0,
      tokens: 0,
      cost: { usd: null, incomplete: false, reasons: [] },
      failures: [],
      fixes_pushed: 0,
      fixes_failed: 0,
      threads_resolved: 0,
      threads_replied: 0,
      merges: null,
      branch_updates: null,
      check_reruns: null,
    },
    delivery: {
      status: "loading",
      reasons: [],
      fetched_at: null,
      repositories: 1,
      opened: 0,
      merged: 0,
      drafts: 0,
      median_merge_seconds: null,
      reviewed: 0,
      median_first_review_seconds: null,
      conflict_or_failing: 0,
      conflict_or_failing_share: null,
    },
    plan_usage: { available: true, series: [] },
    ...over,
  };
}

it("never shows an unknown figure as zero", () => {
  expect(costText(noCost)).toEqual({ text: "—", title: "The price list has no price for x-1." });
  expect(
    costText({ usd: 1.5, incomplete: true, reasons: ["Some model calls reported no token usage."] })
      .text,
  ).toBe("≥ $1.50");
  expect(costText({ usd: 0.25, incomplete: false, reasons: [] }).text).toBe("$0.2500");
  expect(activeText(null)).toBeNull();
  expect(activeText(3900)).toBe("1 h 5 min");
  expect(waitText(null)).toBeNull();
  expect(waitText(30)).toBe("1 min");
  expect(waitText(5 * 3600)).toBe("5 h");
  expect(waitText(72 * 3600)).toBe("3 d");
  expect(niceMax(0)).toBe(1);
  expect(niceMax(7)).toBe(10);
  expect(niceMax(3 * 3600, 3600)).toBe(5 * 3600);

  const html = renderToStaticMarkup(<AnalyticsReport data={report()} Link={Link} />);
  expect(html).not.toContain("$0.00");
  expect(html).toContain("The price list has no price for x-1.");
  expect(html).toContain("No session in this window recorded its activity.");
  expect(html).toContain("has no git user.email");
  // Tokens a session never reported are unknown, not zero.
  expect(html).toContain("Some model calls reported no token usage.");
  expect(html).toContain("Recorded from this version of baum on.");
});

it("says what is still loading and what is empty", () => {
  const loading = renderToStaticMarkup(
    <AnalyticsReport
      data={report({ indexing: { done: 12, total: 40, complete: false } })}
      Link={Link}
    />,
  );
  expect(loading).toContain("Reading agent sessions on this computer: 12 of 40");
  expect(loading).toContain("Loading from GitHub…");
  expect(nextRead(report())).toBe(2000);
  expect(nextRead(report({ delivery: { ...report().delivery, status: "ready" } }))).toBe(300000);

  const empty = renderToStaticMarkup(
    <AnalyticsReport
      data={report({
        summary: { ...report().summary, sessions: 0 },
        options: { repositories: [], agents: [] },
        delivery: {
          ...report().delivery,
          status: "unavailable",
          reasons: ["GitHub is not available to the daemon."],
        },
      })}
      Link={Link}
    />,
  );
  expect(empty).toContain("No agent sessions in this window");
  expect(empty).toContain("Pick repositories and give them a local clone");
  expect(empty).toContain("GitHub is not available to the daemon.");
  expect(empty).toContain("No plan usage sampled yet");
});

it("draws known figures, an incomplete estimate and the limit hits", () => {
  const html = renderToStaticMarkup(
    <AnalyticsReport
      data={report({
        summary: {
          ...report().summary,
          active_seconds: 3600,
          active_incomplete: false,
          cost: {
            usd: 12.5,
            incomplete: true,
            reasons: ["Some model calls did not record their model."],
          },
          agent_share: 0.5,
        },
        commits: {
          ...report().commits,
          available: true,
          reasons: [],
          commits: 4,
          agent_commits: 2,
          by_session: 1,
          by_trailer: 1,
          share: 0.5,
        },
        delivery: {
          ...report().delivery,
          status: "ready",
          opened: 3,
          merged: 2,
          drafts: 1,
          median_merge_seconds: 3 * 3600,
          median_first_review_seconds: 1800,
          conflict_or_failing: 1,
          conflict_or_failing_share: 1 / 3,
        },
        plan_usage: {
          available: true,
          series: [
            {
              provider: "claude",
              window: "5-hour",
              points: [
                { at: "2026-10-26T10:00:00Z", used_percent: 40 },
                { at: "2026-10-26T12:00:00Z", used_percent: 100 },
              ],
              peak: 100,
              limit_hits: 1,
            },
          ],
        },
      })}
      Link={Link}
    />,
  );
  expect(html).toContain("≥ $12.50");
  expect(html).toContain("Incomplete");
  expect(html).toContain("1 h");
  expect(html).toContain("2 of 4 commits");
  expect(html).toContain("3 h");
  expect(html).toContain("30 min");
  expect(html).toContain("1 (33%)");
  expect(html).toContain("at its limit 1 time");
  expect(html).toContain("Sessions per day by agent");
});

it("reads only a report", () => {
  expect(parseAnalytics({ available: false, code: "analytics_unavailable" })).toBeNull();
  expect(parseAnalytics(null)).toBeNull();
  expect(parseAnalytics(report())).not.toBeNull();
  expect(windowDays("90")).toBe(90);
  expect(windowDays("5")).toBe(30);
  expect(windowDays(null)).toBe(30);
});

let root: Root, node: HTMLDivElement;
const requests: string[] = [];
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/?page=analytics");
  requests.length = 0;
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      requests.push(path);
      return Response.json(report({ delivery: { ...report().delivery, status: "ready" } }));
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});

function Harness() {
  const [search, setSearch] = useState(() => new URLSearchParams("page=analytics"));
  return (
    <UrlStateProvider
      value={{ search, update: (patch) => setSearch((s) => patch(new URLSearchParams(s))) }}
    >
      <AnalyticsPage Link={Link} />
    </UrlStateProvider>
  );
}

it("asks for the window and filters the page shows", async () => {
  await act(async () => root.render(<Harness />));
  await act(async () => {});
  expect(requests.at(-1)).toBe("/s/test-ns/api/analytics?days=30");
  expect(node.textContent).toContain("Agent activity");
  const window = [...node.querySelectorAll("button")].find((b) => b.textContent === "90 days")!;
  await act(async () => window.click());
  // The choice shows at once, before the answer.
  expect(window.getAttribute("aria-pressed")).toBe("true");
  expect(requests.at(-1)).toBe("/s/test-ns/api/analytics?days=90");
  const [repository, agent] = [...node.querySelectorAll("select")];
  await act(async () => {
    agent!.value = "codex";
    agent!.dispatchEvent(new Event("change", { bubbles: true }));
  });
  expect(requests.at(-1)).toBe("/s/test-ns/api/analytics?days=90&agent=codex");
  await act(async () => {
    repository!.value = "acme/demo";
    repository!.dispatchEvent(new Event("change", { bubbles: true }));
  });
  expect(requests.at(-1)).toBe("/s/test-ns/api/analytics?days=90&repo=acme%2Fdemo&agent=codex");
  expect([...agent!.options].map((o) => o.textContent)).toEqual([
    "All agents",
    "Claude Code",
    "Codex",
  ]);
});

it("explains an older daemon and a failed read", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => Response.json({ available: false, code: "analytics_unavailable" })),
  );
  await act(async () => root.render(<Harness />));
  await act(async () => {});
  expect(node.textContent).toContain("Analytics are not available");
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response("boom", { status: 500 })),
  );
  await act(async () => root.render(<></>));
  await act(async () => root.render(<Harness />));
  await act(async () => {});
  expect(node.textContent).toContain("Retry");
});

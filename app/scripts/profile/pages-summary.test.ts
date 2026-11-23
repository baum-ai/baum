import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { Snapshot } from "../../src/perf-collector.ts";
import {
  attribute,
  emptyWarnings,
  navigationPages,
  navMark,
  pageMetrics,
  pageRows,
  pagesSection,
} from "./pages-summary.ts";
import { summarise, type Profile, type Run } from "./summary.ts";

function snapshot(change: Partial<Snapshot> = {}): Snapshot {
  return {
    version: 1,
    userAgent: "Mozilla/5.0 AppleWebKit/605.1.15 (KHTML, like Gecko)",
    since: 0,
    now: 1000,
    navigations: [],
    interactions: [],
    frames: {
      count: 0,
      buckets: { "≤17": 0, "≤33": 0, "≤50": 0, "≤100": 0, ">100": 0 },
      long: [],
    },
    renders: {},
    commits: [],
    requests: [],
    marks: [],
    dom: 100,
    heap: null,
    dropped: {},
    ...change,
  };
}

const commit = (id: string, at: number, actual: number) => ({
  id,
  phase: "update" as const,
  actual,
  base: actual,
  at,
  page: "inbox",
});
const request = (start: number, duration: number) => ({
  method: "GET",
  path: "api/x",
  status: 200,
  start,
  duration,
  bytes: null,
});

// Two pages in one snapshot: A from 100 to 400, B from 500 to 900.
const tour = snapshot({
  marks: [
    { name: navMark("A", "start"), at: 100 },
    { name: navMark("A", "end"), at: 400 },
    { name: navMark("B", "start"), at: 500 },
    { name: navMark("B", "end"), at: 900 },
  ],
  navigations: [
    { search: "?page=a", start: 110, content: 40, settled: 90 },
    { search: "?page=b", start: 510, content: 200, settled: 300 },
  ],
  commits: [
    commit("app", 120, 5),
    commit("app", 130, 7),
    commit("inbox", 130, 100), // a nested region: not counted
    commit("app", 600, 20),
    commit("app", 450, 1000), // between the windows: nobody's
  ],
  requests: [request(115, 10), request(120, 5), request(550, 70), request(950, 99)],
  frames: {
    count: 10,
    buckets: { "≤17": 5, "≤33": 0, "≤50": 0, "≤100": 3, ">100": 2 },
    long: [
      { start: 140, duration: 60 },
      { start: 520, duration: 80 },
      { start: 700, duration: 120 },
    ],
  },
});

describe("attribute", () => {
  it("counts only what began in the window, from the root region", () => {
    assert.deepEqual(attribute(tour, 100, 400), {
      content: 40,
      settled: 90,
      navigations_count: 1,
      commits_count: 2,
      render_total: 12,
      long_frames_count: 1,
      long_frame_total: 60,
      requests_count: 2,
      request_total: 15,
    });
    const b = attribute(tour, 500, 900);
    assert.equal(b.content, 200);
    assert.equal(b.commits_count, 1);
    assert.equal(b.render_total, 20);
    assert.equal(b.long_frames_count, 2);
    assert.equal(b.long_frame_total, 200);
    assert.equal(b.requests_count, 1);
    assert.equal(b.request_total, 70);
  });

  it("includes both ends of the window", () => {
    assert.equal(attribute(tour, 120, 130).commits_count, 2);
  });

  it("leaves the times out of a page that never showed content", () => {
    const none = attribute(snapshot({ navigations: [{ search: "", start: 5 }] }), 0, 10);
    assert.equal("content" in none, false);
    assert.equal("settled" in none, false);
  });

  it("measures from the first navigation's start to the last one's frames", () => {
    // The app pushed a second address itself, 10 ms in: the wait is both.
    const s = snapshot({
      navigations: [
        { search: "?a", start: 10, superseded: true },
        { search: "?b", start: 20, content: 7, settled: 12 },
      ],
    });
    const page = attribute(s, 0, 100);
    assert.equal(page.navigations_count, 2);
    assert.equal(page.content, 17);
    assert.equal(page.settled, 22);
  });

  it("has no times for a window without a navigation", () => {
    const page = attribute(
      snapshot({ navigations: [{ search: "", start: 500, content: 1 }] }),
      0,
      100,
    );
    assert.equal(page.navigations_count, 0);
    assert.equal("content" in page, false);
  });

  it("counts a request or commit by where it began, straddling an end mark", () => {
    const s = snapshot({
      requests: [request(395, 300), request(400, 1), request(401, 1)],
      commits: [commit("app", 400, 3), commit("app", 401, 9)],
    });
    // Began before the end of the window, ended long after it: the window's.
    const a = attribute(s, 100, 400);
    assert.equal(a.requests_count, 2);
    assert.equal(a.request_total, 301);
    assert.equal(a.commits_count, 1);
    assert.equal(a.render_total, 3);
    // The next window takes what began after the mark, once.
    const b = attribute(s, 401, 900);
    assert.equal(b.requests_count, 1);
    assert.equal(b.commits_count, 1);
  });

  it("covers a direct load with an unbounded window", () => {
    assert.equal(attribute(tour, 0, Infinity).commits_count, 4);
  });

  it("times a direct load from the document's start, bundle included", () => {
    // The collector installed at 300 ms, after the bundle was fetched and
    // parsed; its content frame came 50 ms later.
    const s = snapshot({ navigations: [{ search: "", start: 300, content: 50, settled: 80 }] });
    const page = attribute(s, 0, Infinity, true);
    assert.equal(page.content, 350);
    assert.equal(page.settled, 380);
    assert.equal(attribute(s, 0, Infinity).content, 50);
  });
});

describe("navigationPages", () => {
  it("attributes each marked page its own window, in order", () => {
    const pages = navigationPages(tour);
    assert.deepEqual(
      pages.map((p) => p.label),
      ["A", "B"],
    );
    assert.equal(pages[0]!.page.commits_count, 2);
    assert.equal(pages[1]!.page.requests_count, 1);
  });

  it("leaves out a page whose end mark is missing, and keeps the rest", () => {
    const s = snapshot({
      marks: [
        { name: navMark("A", "start"), at: 1 },
        { name: navMark("B", "start"), at: 5 },
        { name: navMark("B", "end"), at: 9 },
      ],
    });
    assert.deepEqual(
      navigationPages(s).map((p) => p.label),
      ["B"],
    );
  });
});

function run(index: number, metrics: Record<string, number>): Run {
  return { index, wall: 1, metrics, snapshot: snapshot() };
}

const metricsOf = (label: string, content: number, empty = 0) =>
  pageMetrics(
    label,
    "nav",
    { content, settled: content + 10, commits_count: 3 },
    {
      dom_count: 500,
      empty_count: empty,
    },
  );

describe("pageRows", () => {
  it("takes medians over runs and ranks slowest content first, empty last", () => {
    const runs = [
      run(1, { ...metricsOf("Fast", 10), ...metricsOf("Slow", 100), ...metricsOf("Gap", 1, 1) }),
      run(2, { ...metricsOf("Fast", 30), ...metricsOf("Slow", 300), ...metricsOf("Gap", 1, 1) }),
      run(3, { ...metricsOf("Fast", 20), ...metricsOf("Slow", 200), ...metricsOf("Gap", 1, 1) }),
    ];
    const rows = pageRows(runs);
    assert.deepEqual(
      rows.map((r) => [r.label, r.content, r.empty]),
      [
        ["Slow", 200, false],
        ["Fast", 20, false],
        ["Gap", 1, true],
      ],
    );
    assert.equal(rows[0]!.dom, 500);
  });

  it("flags a page empty in some runs only, and times it over the others", () => {
    const runs = [
      run(1, metricsOf("Flaky", 10)),
      run(2, metricsOf("Flaky", 500, 1)),
      run(3, metricsOf("Flaky", 30)),
    ];
    const [row] = pageRows(runs);
    assert.equal(row!.mixed, true);
    assert.equal(row!.empty, false);
    assert.equal(row!.emptyRuns, 1);
    assert.equal(row!.content, 20);
    assert.match(
      emptyWarnings(pageRows(runs)).join("\n"),
      /Flaky \(nav\) was empty in 1 of 3 runs/,
    );
    const text = summarise(profileOf(runs));
    assert.match(text, /\| Flaky \| nav \| empty in 1\/3 \|/);
  });

  it("shows in how many runs a page settled", () => {
    const runs = [
      run(1, pageMetrics("A", "nav", { content: 10, settled: 20 }, { empty_count: 0 })),
      run(2, pageMetrics("A", "nav", { content: 12 }, { empty_count: 0 })),
    ];
    const [row] = pageRows(runs);
    assert.equal(row!.settledRuns, 1);
    assert.equal(row!.contentRuns, 2);
    assert.equal(row!.countedRuns, 2);
    assert.match(summarise(profileOf(runs)), /\| A \| nav \| 11\.0 \| 20\.0 \(1\/2\) \|/);
  });

  it("says never for a page with no content time in any run", () => {
    const runs = [run(1, pageMetrics("G", "direct", {}, { empty_count: 0 }))];
    assert.match(
      summarise(profileOf(runs)),
      /\| G \| direct \| never \(0\/1\) \| never \(0\/1\) \|/,
    );
  });

  it("flags a page empty one way and not the other", () => {
    const runs = [
      run(1, {
        ...pageMetrics("P", "nav", { content: 5 }, { empty_count: 1 }),
        ...pageMetrics("P", "direct", { content: 9 }, { empty_count: 0 }),
      }),
    ];
    assert.match(
      emptyWarnings(pageRows(runs)).join("\n"),
      /P is empty by navigation and not empty by direct load/,
    );
  });

  it("sorts a page with no content time with the slowest, and mixed pages after the full ones", () => {
    const runs = [
      run(1, {
        ...metricsOf("Fast", 10),
        ...metricsOf("Mixed", 999, 1),
        ...pageMetrics("NoContent", "nav", { settled: 5 }, { empty_count: 0 }),
      }),
      run(2, { ...metricsOf("Fast", 10), ...metricsOf("Mixed", 999, 0) }),
    ];
    assert.deepEqual(
      pageRows(runs).map((r) => r.label),
      ["NoContent", "Fast", "Mixed"],
    );
  });

  it("keeps the two ways of a page apart", () => {
    const runs = [
      run(1, {
        ...pageMetrics("P", "nav", { content: 5 }, { empty_count: 0 }),
        ...pageMetrics("P", "direct", { content: 50 }, { empty_count: 0 }),
      }),
    ];
    assert.deepEqual(
      pageRows(runs).map((r) => [r.way, r.content]),
      [
        ["direct", 50],
        ["nav", 5],
      ],
    );
  });
});

const profileOf = (runs: Run[]): Profile => ({
  scenario: "pages",
  description: "d",
  started: "now",
  environment: {},
  notes: [],
  warmup: runs[0]!,
  runs,
  chunks: [],
});

describe("the summary", () => {
  const profile = (runs: Run[]): Profile => ({
    scenario: "pages",
    description: "d",
    started: "now",
    environment: {},
    notes: [],
    warmup: runs[0]!,
    runs,
    chunks: [],
  });

  it("has one Pages table, and keeps the page metrics out of Metrics", () => {
    const runs = [run(1, { ...metricsOf("Slow", 100), ...metricsOf("Gap", 1, 1), other_count: 2 })];
    const text = summarise(profile(runs));
    assert.match(text, /## Pages/);
    assert.match(text, /\| Slow \| nav \| 100\.0 \|/);
    assert.match(text, /\| Gap \| nav \| empty \| – \|/);
    assert.ok(text.indexOf("| Slow ") < text.indexOf("| Gap "));
    assert.doesNotMatch(text, /page:Slow/);
    assert.match(text, /other_count/);
  });

  it("adds nothing for a scenario without pages", () => {
    assert.deepEqual(pagesSection(profile([run(1, { x_count: 1 })])), []);
  });
});

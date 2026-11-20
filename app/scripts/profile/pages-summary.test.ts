import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { Snapshot } from "../../src/perf-collector.ts";
import {
  attribute,
  navigationPages,
  navMark,
  pageMetric,
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

  it("takes the last navigation of the window, as a redirect leaves it", () => {
    const s = snapshot({
      navigations: [
        { search: "?a", start: 10, superseded: true },
        { search: "?b", start: 20, content: 7 },
      ],
    });
    assert.equal(attribute(s, 0, 100).content, 7);
  });

  it("covers a direct load with an unbounded window", () => {
    assert.equal(attribute(tour, 0, Infinity).commits_count, 4);
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

  it("leaves out a page whose end mark is missing", () => {
    const s = snapshot({ marks: [{ name: navMark("A", "start"), at: 1 }] });
    assert.deepEqual(navigationPages(s), []);
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

  it("keeps the two ways of a page apart", () => {
    const runs = [
      run(1, {
        [pageMetric("P", "nav", "content")]: 5,
        [pageMetric("P", "direct", "content")]: 50,
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

import assert from "node:assert/strict";
import { describe, it } from "node:test";
import type { Snapshot } from "../../src/perf-collector.ts";
import { classify } from "./bundle.ts";
import {
  format,
  interactionGroups,
  metricStats,
  percentile,
  regions,
  requestGroups,
  slowest,
  standardMetrics,
  stats,
  summarise,
  table,
  type Profile,
  type Run,
} from "./summary.ts";

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

const key = (target: string, duration: number, start = 0) => ({
  type: "keydown" as const,
  target,
  start,
  delay: 1,
  processing: duration / 2,
  duration,
});

function run(index: number, metrics: Record<string, number>, change: Partial<Snapshot> = {}): Run {
  return { index, wall: 10, metrics, snapshot: snapshot(change) };
}

describe("percentile", () => {
  it("interpolates between the nearest ranks", () => {
    assert.equal(percentile([1, 2, 3, 4], 50), 2.5);
    assert.equal(percentile([10, 20, 30], 50), 20);
    assert.equal(percentile([30, 10, 20], 95), 29);
    assert.equal(percentile([7], 95), 7);
    assert.ok(Number.isNaN(percentile([], 50)));
  });

  it("leaves its input in order", () => {
    const values = [3, 1, 2];
    percentile(values, 50);
    assert.deepEqual(values, [3, 1, 2]);
  });

  it("gives the median, p95 and range together", () => {
    const { p95, ...rest } = stats([4, 1, 3, 2]);
    assert.deepEqual(rest, { n: 4, median: 2.5, min: 1, max: 4 });
    assert.ok(Math.abs(p95 - 3.85) < 1e-9);
  });
});

describe("standardMetrics", () => {
  it("summarises interactions, frames, renders and requests", () => {
    const metrics = standardMetrics(
      snapshot({
        interactions: [key("j", 10), key("j", 30), key("k", 20)],
        frames: {
          count: 10,
          buckets: { "≤17": 8, "≤33": 0, "≤50": 0, "≤100": 1, ">100": 1 },
          long: [
            { start: 0, duration: 60 },
            { start: 100, duration: 120 },
          ],
        },
        renders: {
          app: { commits: 2, mounts: 1, actual: 5, max: 4, base: 3 },
          main: { commits: 3, mounts: 1, actual: 7, max: 5, base: 2 },
        },
        requests: [
          { method: "GET", path: "api/index", status: 200, start: 0, duration: 5, bytes: 10 },
        ],
        heap: 2048,
      }),
    );
    assert.deepEqual(metrics, {
      interactions_count: 3,
      long_frames_count: 2,
      long_frame_total: 180,
      worst_frame: 120,
      commits_count: 5,
      render_total: 12,
      requests_count: 1,
      dom_count: 100,
      interaction_median: 20,
      interaction_p95: 29,
      interaction_max: 30,
      heap_bytes: 2048,
    });
  });

  it("leaves out interaction times and the heap when there are none", () => {
    const metrics = standardMetrics(snapshot());
    assert.equal("interaction_median" in metrics, false);
    assert.equal("heap_bytes" in metrics, false);
    assert.equal(metrics.worst_frame, 0);
  });
});

describe("across runs", () => {
  it("takes each metric's statistics, skipping runs without it", () => {
    const runs = [run(1, { a: 1, b: 5 }), run(2, { a: 3 }), run(3, { a: 2, b: 7 })];
    assert.deepEqual(metricStats(runs), [
      ["a", { n: 3, median: 2, p95: 2.9, min: 1, max: 3 }],
      ["b", { n: 2, median: 6, p95: 6.9, min: 5, max: 7 }],
    ]);
  });

  it("lists the slowest interactions with their run", () => {
    const runs = [
      run(1, {}, { interactions: [key("j", 10), key("k", 50)] }),
      run(2, {}, { interactions: [key("j", 40)] }),
    ];
    assert.deepEqual(
      slowest(runs, 2).map((i) => [i.run, i.target, i.duration]),
      [
        [1, "k", 50],
        [2, "j", 40],
      ],
    );
  });

  it("groups interactions by input, the worst p95 first", () => {
    const runs = [
      run(1, {}, { interactions: [key("j", 10), key("k", 50)] }),
      run(2, {}, { interactions: [key("j", 20), key("k", 60)] }),
    ];
    const groups = interactionGroups(runs);
    assert.deepEqual(
      groups.map((g) => [g.name, g.count, g.duration.median, g.processing]),
      [
        ["keydown k", 2, 55, 27.5],
        ["keydown j", 2, 15, 7.5],
      ],
    );
  });

  it("takes each region's median run, counting a run without it as zero", () => {
    const runs = [
      run(1, {}, { renders: { app: { commits: 2, mounts: 1, actual: 10, max: 6, base: 4 } } }),
      run(2, {}, { renders: { app: { commits: 4, mounts: 1, actual: 30, max: 9, base: 5 } } }),
      run(3, {}, {}),
    ];
    assert.deepEqual(regions(runs), [
      { id: "app", commits: 2, mounts: 1, actual: 10, max: 6, base: 4 },
    ]);
  });

  it("groups requests by path without the query, per run", () => {
    const request = (path: string, duration: number) => ({
      method: "GET",
      path,
      status: 200,
      start: 0,
      duration,
      bytes: null,
    });
    const runs = [
      run(1, {}, { requests: [request("api/document?pr=1", 10), request("api/index", 1)] }),
      run(2, {}, { requests: [request("api/document?pr=2", 30)] }),
    ];
    assert.deepEqual(
      requestGroups(runs).map((r) => [r.name, r.perRun, r.duration.median, r.total]),
      [
        ["GET api/document", 1, 20, 20],
        ["GET api/index", 0.5, 1, 0.5],
      ],
    );
  });
});

describe("format", () => {
  it("writes times, counts and sizes", () => {
    assert.equal(format(12.345), "12.3");
    assert.equal(format(3, "rows_count"), "3");
    assert.equal(format(2.5, "rows_count"), "2.5");
    assert.equal(format(3 * 1024 * 1024, "heap_bytes"), "3.00 MiB");
    assert.equal(format(Number.NaN), "–");
  });

  it("aligns only columns of numbers right", () => {
    assert.deepEqual(
      table(
        ["a", "b", "c"],
        [
          ["x", "1.0", "y"],
          ["z", "2.0 KiB", "–"],
        ],
      ).slice(0, 2),
      ["| a | b | c |", "| --- | ---: | --- |"],
    );
  });
});

describe("classify", () => {
  it("sorts the build's files by how index.html loads them", () => {
    const html = `<script type="module" crossorigin src="./assets/app-1.js"></script>
      <link rel="modulepreload" crossorigin href="./assets/shell-1.js">
      <link rel="stylesheet" crossorigin href="./assets/app-1.css">`;
    const file = (name: string) => ({ file: `assets/${name}`, bytes: 10, gzip: 5 });
    assert.deepEqual(
      classify(
        html,
        ["app-1.js", "shell-1.js", "app-1.css", "review-1.js", "review-1.css", "app-1.js.map"].map(
          file,
        ),
      ).map((c) => [c.file, c.kind]),
      [
        ["assets/app-1.js", "entry"],
        ["assets/shell-1.js", "preload"],
        ["assets/app-1.css", "css"],
        ["assets/review-1.js", "lazy"],
        ["assets/review-1.css", "lazy-css"],
      ],
    );
  });
});

describe("summarise", () => {
  const profile: Profile = {
    scenario: "jk",
    description: "Presses j and k.",
    started: "2026-11-20T00:00:00.000Z",
    environment: { commit: "abc", CPUs: "0-3" },
    notes: ["The inbox shows 30 rows."],
    warmup: run(0, { presses_count: 2 }),
    runs: [1, 2, 3].map((index) =>
      run(
        index,
        {
          presses_count: 2,
          ...standardMetrics(snapshot({ interactions: [key("j", 10 * index)] })),
        },
        {
          interactions: [key("j", 10 * index)],
          renders: { main: { commits: index, mounts: 0, actual: index, max: 1, base: 1 } },
        },
      ),
    ),
    chunks: [
      { file: "assets/app.js", kind: "entry", bytes: 2048, gzip: 1024 },
      { file: "assets/review.js", kind: "lazy", bytes: 4096, gzip: 2048 },
    ],
  };

  it("writes the metrics, interactions, components and bundle in a short page", () => {
    const text = summarise(profile);
    const lines = text.split("\n");
    assert.ok(lines.length < 100, `${lines.length} lines`);
    assert.equal(lines[0], "# Profile: jk");
    assert.match(text, /3 runs after one warm-up/);
    assert.match(text, /commit: abc; CPUs: 0-3/);
    assert.match(text, /- The inbox shows 30 rows\./);
    assert.match(text, /\| presses_count \| 2 \| 2 \| 2 \| 2 \|/);
    assert.match(text, /\| interaction_median \| 20\.0 \| 29\.0 \| 10\.0 \| 30\.0 \|/);
    assert.match(text, /\| keydown j \| 3 \| 20\.0 \| 29\.0 \| 30\.0 \| 1\.0 \| 10\.0 \|/);
    assert.match(text, /\| 3 \| keydown j \| 30\.0 \|/);
    assert.match(text, /\| main \| 2\.0 \| 2 \| 0 \|/);
    assert.match(text, /Loaded by index.html: 2.0 KiB \(1.0 KiB gzip\) in 1 files/);
    assert.match(text, /\| assets\/review.js \| lazy \| 4.0 KiB \| 2.0 KiB \|/);
  });

  it("leaves out sections with nothing in them", () => {
    const text = summarise({
      ...profile,
      notes: [],
      runs: [run(1, { content: 5 })],
      chunks: [],
    });
    assert.doesNotMatch(text, /## Interactions|## Components|## Requests|## Bundle/);
  });
});

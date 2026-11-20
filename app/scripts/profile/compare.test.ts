import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
  compareMetrics,
  comparisonJson,
  daemonFailure,
  holm,
  rankSumTest,
  schedule,
  smallestP,
  summariseComparison,
  type Comparison,
} from "./compare.ts";
import type { Chunk, Profile, Run } from "./summary.ts";

/** A deterministic stream of numbers in [0, 1) (mulberry32), whose seeds
 *  give independent-looking streams. */
function random(seed: number) {
  let state = seed >>> 0;
  return () => {
    state = (state + 0x6d2b79f5) >>> 0;
    let t = state;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 2 ** 32;
  };
}

/** `n` runs whose metrics come from `make`. */
function runs(n: number, make: (index: number) => Record<string, number>): Run[] {
  return Array.from({ length: n }, (_, i) => ({
    index: i + 1,
    wall: 10,
    metrics: make(i),
    snapshot: {} as Run["snapshot"],
  }));
}

/** Runs of a cold start whose times scatter as on a loaded machine (about
 *  ±15%), shifted by `shift` milliseconds. */
function coldStarts(n: number, seed: number, shift = 0, change: Record<string, number> = {}) {
  const next = random(seed);
  const noisy = (centre: number) => centre * (0.85 + 0.3 * next()) + shift;
  return runs(n, () => ({
    launch_to_content: noisy(820),
    content: noisy(480),
    settled: noisy(700),
    dom_content_loaded: noisy(140),
    long_frame_total: noisy(400),
    render_total: noisy(250),
    worst_frame: noisy(90),
    long_frames_count: Math.round(4 + 4 * next()),
    heap_bytes: Math.round(30e6 * (0.95 + 0.1 * next())),
    rows_count: 30,
    requests_count: 98,
    dom_count: 819,
    ...change,
  }));
}

describe("schedule", () => {
  it("runs both warm-ups, then pairs whose order alternates", () => {
    assert.deepEqual(
      schedule(3).map((s) => `${s.side}${s.index}`),
      ["head0", "base0", "head1", "base1", "base2", "head2", "head3", "base3"],
    );
  });

  it("gives each side every run once", () => {
    const steps = schedule(8);
    for (const side of ["base", "head"])
      assert.deepEqual(
        steps.filter((s) => s.side === side).map((s) => s.index),
        [0, 1, 2, 3, 4, 5, 6, 7, 8],
      );
  });
});

describe("rankSumTest", () => {
  it("gives the exact p of groups apart", () => {
    assert.ok(Math.abs(rankSumTest([1, 2, 3, 4], [5, 6, 7, 8]) - 2 / 70) < 1e-12);
    assert.ok(Math.abs(rankSumTest([1, 2, 3, 4, 5], [6, 7, 8, 9, 10]) - 2 / 252) < 1e-12);
    assert.equal(rankSumTest([6, 7, 8], [1, 2, 3]), 0.1);
  });

  it("finds nothing in interleaved groups", () => {
    assert.ok(rankSumTest([1, 3, 5, 7, 9], [2, 4, 6, 8, 10]) > 0.5);
    assert.equal(rankSumTest([5, 5, 5], [5, 5, 5]), 1);
  });

  it("counts ties at their mean rank", () => {
    // Ranks 1.5, 1.5, 3.5, 3.5 against 5..8: still the most extreme split.
    assert.ok(Math.abs(rankSumTest([1, 1, 2, 2], [3, 4, 5, 6]) - 2 / 70) < 1e-12);
    assert.ok(rankSumTest([1, 2, 2, 3], [2, 2, 3, 4]) > 0.2);
    // Unequal ties: three of the four 1s on one side.
    assert.ok(Math.abs(rankSumTest([1, 1, 1], [1, 2, 3]) - 0.4) < 1e-12);
  });

  it("matches the smallest p it can give", () => {
    assert.equal(smallestP(3, 3), 0.1);
    assert.ok(Math.abs(smallestP(5, 5) - 2 / 252) < 1e-12);
    assert.ok(Math.abs(smallestP(8, 8) - 2 / 12870) < 1e-12);
  });

  it("is two-sided and symmetric", () => {
    const a = [10, 12, 9, 14, 11];
    const b = [13, 15, 16, 12.5, 17];
    assert.ok(Math.abs(rankSumTest(a, b) - rankSumTest(b, a)) < 1e-12);
  });
});

describe("holm", () => {
  it("adjusts step-down and keeps the order", () => {
    const adjusted = holm([0.01, 0.04, 0.03]);
    assert.ok(Math.abs(adjusted[0]! - 0.03) < 1e-12);
    assert.ok(Math.abs(adjusted[1]! - 0.06) < 1e-12);
    assert.ok(Math.abs(adjusted[2]! - 0.06) < 1e-12);
    assert.deepEqual(holm([0.5, 0.9]), [1, 1]);
    // 0.012 × 4 = 0.048 lifts 0.015 × 3 = 0.045, and 0.04 × 2 = 0.08 lifts
    // 0.045 × 1; order kept.
    const lifted = holm([0.045, 0.012, 0.04, 0.015]);
    [0.08, 0.048, 0.08, 0.048].forEach((expected, i) =>
      assert.ok(Math.abs(lifted[i]! - expected) < 1e-12, `${i}: ${lifted[i]}`),
    );
  });
});

const verdicts = (base: Run[], head: Run[], headline = "launch_to_content") =>
  Object.fromEntries(compareMetrics(base, head, headline).map((r) => [r.metric, r.verdict]));

describe("compareMetrics", () => {
  it("calls identical distributions within noise but rarely, for many seeds", () => {
    // A/A tests: both sides from the same distribution. The headline is
    // tested at 5%, the others together with Holm's adjustment at 5%, so
    // each is called changed in at most about 1 of 20; the floor makes the
    // headline rarer still.
    let headline = 0;
    let others = 0;
    for (let seed = 1; seed <= 200; seed++) {
      const found = verdicts(coldStarts(8, seed), coldStarts(8, seed + 1000));
      const quiet = ["within noise", "same", "negligible"];
      if (!quiet.includes(found.launch_to_content!)) headline++;
      if (Object.entries(found).some(([m, v]) => m !== "launch_to_content" && !quiet.includes(v)))
        others++;
    }
    assert.ok(headline <= 10, `the headline was called changed in ${headline} of 200`);
    assert.ok(others <= 10, `another metric was called changed in ${others} of 200`);
  });

  it("calls a clear shift slower or faster", () => {
    const base = coldStarts(5, 1);
    assert.equal(verdicts(base, coldStarts(5, 2, 300)).launch_to_content, "slower");
    assert.equal(verdicts(base, coldStarts(5, 2, -300)).launch_to_content, "faster");
    const heavier = coldStarts(8, 2, 0, {});
    for (const run of heavier) run.metrics.heap_bytes! *= 1.5;
    assert.equal(verdicts(coldStarts(8, 1), heavier).heap_bytes, "higher");
  });

  it("does not over-claim with few runs", () => {
    const found = verdicts(coldStarts(3, 1), coldStarts(3, 2, 1000));
    assert.equal(found.launch_to_content, "within noise");
    // Five runs a side cannot pass Holm's threshold across these metrics,
    // however far apart: only the headline can.
    const five = verdicts(coldStarts(5, 1), coldStarts(5, 2, 1000));
    assert.equal(five.launch_to_content, "slower");
    assert.equal(five.content, "within noise");
    // Eight can.
    assert.equal(verdicts(coldStarts(8, 1), coldStarts(8, 2, 1000)).content, "slower");
  });

  it("calls a significant change under the floor negligible", () => {
    const steady = (value: number) => runs(8, (i) => ({ launch_to_content: value + i * 0.01 }));
    assert.equal(verdicts(steady(800), steady(801)).launch_to_content, "negligible");
    assert.equal(verdicts(steady(800), steady(830)).launch_to_content, "slower");
    // 2% of 50 ms is 1 ms, but the floor is 2 ms.
    assert.equal(verdicts(steady(50), steady(51.5)).launch_to_content, "negligible");
  });

  it("compares counts that never vary as they are", () => {
    const base = coldStarts(5, 1);
    const head = coldStarts(5, 2, 0, { requests_count: 112 });
    const rows = compareMetrics(base, head, "launch_to_content");
    const requests = rows.find((r) => r.metric === "requests_count")!;
    assert.equal(requests.verdict, "differs");
    assert.equal(requests.p, undefined);
    assert.equal(requests.change, 14);
    assert.equal(rows.find((r) => r.metric === "rows_count")!.verdict, "same");
  });

  it("puts the headline first and names metrics only one side has", () => {
    const base = coldStarts(5, 1);
    const head = coldStarts(5, 2, 0, { assets_count: 11 });
    const rows = compareMetrics(base, head, "settled");
    assert.equal(rows[0]!.metric, "settled");
    assert.ok(rows[0]!.headline);
    assert.equal(rows.find((r) => r.metric === "assets_count")!.verdict, "only in head");
  });
});

const chunk = (file: string, kind: Chunk["kind"], bytes: number): Chunk => ({
  file,
  kind,
  bytes,
  gzip: Math.round(bytes / 3),
});

function profile(list: Run[], change: Partial<Profile> = {}): Profile {
  return {
    scenario: "cold-start",
    description: "Quits and launches the app.",
    started: "2026-11-20T12:00:00.000Z",
    environment: {
      commit: "abc1234",
      CPUs: "0-11",
      WebKitGTK: "2.52.6",
      data: "seed 1 (digest c105)",
    },
    notes: [],
    warmup: { ...list[0]!, index: 0 },
    runs: list,
    chunks: [chunk("index-a.js", "entry", 400_000), chunk("lazy-a.js", "lazy", 100_000)],
    ...change,
  };
}

function comparison(base: Run[], head: Run[], change: Partial<Comparison> = {}): Comparison {
  return {
    scenario: "cold-start",
    description: "Quits and launches the app.",
    headline: "launch_to_content",
    baseLabel: "origin/main at def5678",
    headLabel: "abc1234+changes",
    base: profile(base, {
      environment: {
        commit: "def5678",
        ref: "origin/main",
        CPUs: "0-11",
        WebKitGTK: "2.52.6",
        data: "seed 1 (digest c105)",
      },
    }),
    head: profile(head, {
      chunks: [chunk("index-b.js", "entry", 410_000), chunk("lazy-b.js", "lazy", 100_000)],
    }),
    ...change,
  };
}

describe("summariseComparison", () => {
  it("puts the verdicts first and says when nothing changed", () => {
    const text = summariseComparison(comparison(coldStarts(8, 1), coldStarts(8, 2)));
    const verdict = text.indexOf("## Verdict");
    const metrics = text.indexOf("## Metrics");
    assert.ok(verdict > 0 && verdict < metrics);
    assert.match(text, /\*\*launch_to_content\*\* \(headline\): within noise/);
    assert.match(text, /Nothing changed beyond noise/);
    assert.match(text, /head abc1234\+changes against base origin\/main at def5678: 8 and 8 runs/);
    assert.match(text, /CPUs: 0-11; WebKitGTK: 2\.52\.6; data: seed 1 \(digest c105\)/);
    assert.match(text, /\| launch_to_content \(headline\) \| [\d.]+ \([\d.]+–[\d.]+\) \|/);
  });

  it("lists what changed, with the medians, the change and p", () => {
    const text = summariseComparison(
      comparison(coldStarts(5, 1), coldStarts(5, 2, 300, { requests_count: 112 })),
    );
    assert.match(
      text,
      /- \*\*launch_to_content\*\* \(headline\): \*\*slower\*\*, [\d.]+ → [\d.]+ ms \(\+[\d.]+, \+[\d.]+%\), p = 0\.008\./,
    );
    assert.match(text, /- \*\*requests_count\*\* differs: 98 → 112 in every run of each side/);
    assert.match(text, /With 5 and 5 runs only the headline can show a change/);
  });

  it("lists counts that differ before times, and metrics one side lacks apart", () => {
    const head = coldStarts(5, 2, 300, { requests_count: 112, assets_count: 11 });
    const text = summariseComparison(comparison(coldStarts(5, 1), head));
    const differs = text.indexOf("**requests_count** differs");
    const headline = text.indexOf("**launch_to_content** (headline)");
    const only = text.indexOf("**assets_count** is only in head");
    assert.ok(headline > 0 && headline < differs && differs < only, text);
    assert.match(text, /The other \d+ metrics are within noise or the same\./);
    assert.match(text, /Decide on the headline/);
  });

  it("says when too few runs can show nothing", () => {
    const text = summariseComparison(comparison(coldStarts(3, 1), coldStarts(3, 2)));
    assert.match(text, /3 and 3 runs cannot show any change \(the smallest possible p is 0\.100\)/);
  });

  it("flags journeys that found different data", () => {
    const c = comparison(coldStarts(5, 1), coldStarts(5, 2));
    c.head.notes = ["Inbox shows 30 rows"];
    c.base.notes = ["Inbox shows 20 rows"];
    assert.match(summariseComparison(c), /The journeys differ: base found "Inbox shows 20 rows"/);
  });

  it("compares the bundles", () => {
    const text = summariseComparison(comparison(coldStarts(5, 1), coldStarts(5, 2)));
    assert.match(
      text,
      /\| loaded by index\.html \| 390\.6 KiB \| 400\.4 KiB \| \+9\.8 KiB \+2\.5% \|/,
    );
    assert.match(text, /\| loaded later \| 97\.7 KiB \| 97\.7 KiB \| ±0 B ±0\.0% \|/);
  });

  it("marks a partial comparison", () => {
    const text = summariseComparison(
      comparison(coldStarts(5, 1), coldStarts(5, 2), { partial: "the base side failed: hung" }),
    );
    assert.match(text, /\*\*Partial: the base side failed: hung\*\*/);
  });
});

describe("comparisonJson", () => {
  it("records each run's side and place in the order", () => {
    const head = coldStarts(2, 2).map((run, i) => ({
      ...run,
      side: "head" as const,
      order: [2, 5][i],
    }));
    const base = coldStarts(2, 1).map((run, i) => ({
      ...run,
      side: "base" as const,
      order: [3, 4][i],
    }));
    const c = comparison(base, head);
    c.head.warmup = { ...c.head.warmup, order: 0 };
    c.base.warmup = { ...c.base.warmup, order: 1 };
    const json = comparisonJson(c);
    assert.deepEqual(
      json.runs.map((r) => `${r.side}${r.index}@${r.order}`),
      ["head0@0", "base0@1", "head1@2", "base1@3", "base2@4", "head2@5"],
    );
    assert.equal(json.metrics[0]!.metric, "launch_to_content");
  });
});

describe("daemonFailure", () => {
  const out =
    "baum: starting\nbaum: application database schema 3 is newer than supported version 2\n";

  it("quotes the daemon's last words, with a hint for a base that cannot read the schema", () => {
    const base = daemonFailure(out, "base")!;
    assert.match(base, /^The daemon said: baum: application database schema 3 is newer/);
    assert.match(base, /compare against a ref with the same store schemas/);
    assert.doesNotMatch(daemonFailure(out, "head")!, /compare against/);
    assert.equal(daemonFailure("\n"), undefined);
  });
});

// Comparing a profile of the working tree (head) with one of a baseline
// commit (base), run interleaved on the same data: the order of the runs,
// the test that tells a change from noise, and the comparison's summary.md.
// Apart from the browser and the driver so they can be tested
// (compare.test.ts); the runner (run.ts) uses them with --side.
import { bytes, format, percentile, table, type Chunk, type Profile, type Run } from "./summary.ts";

export type Side = "base" | "head";

export type Step = { side: Side; index: number };

/** The order of a comparison's runs: each side's warm-up, then the
 *  measured runs in pairs whose order alternates (head then base, base then
 *  head, ...), so drift from other work on the machine, and anything that
 *  favours the first or second run of a pair, falls on both sides alike. */
export function schedule(runs: number): Step[] {
  const steps: Step[] = [
    { side: "head", index: 0 },
    { side: "base", index: 0 },
  ];
  for (let index = 1; index <= runs; index++) {
    const pair: Step[] = [
      { side: "head", index },
      { side: "base", index },
    ];
    steps.push(...(index % 2 === 1 ? pair : pair.toReversed()));
  }
  return steps;
}

/** The two-sided p-value of the Mann-Whitney U (Wilcoxon rank-sum) test of
 *  `a` against `b`, exact: the share of all ways to split the pooled runs
 *  into groups of their sizes whose rank sum is at least as far from its
 *  expectation as the observed one. Ties take their mean rank. It assumes
 *  no distribution, and with few runs it cannot give a small p: 3 runs a
 *  side give at least 0.1, 5 a side at least 0.008. */
export function rankSumTest(a: number[], b: number[]): number {
  const n = a.length;
  const total = a.length + b.length;
  if (n === 0 || b.length === 0) return 1;
  const pooled = [
    ...a.map((value) => ({ value, a: true })),
    ...b.map((value) => ({ value, a: false })),
  ].toSorted((x, y) => x.value - y.value);
  // Ranks doubled, so mean ranks of ties stay whole numbers.
  const ranks: number[] = [];
  for (let i = 0; i < total; ) {
    let j = i;
    while (j + 1 < total && pooled[j + 1]!.value === pooled[i]!.value) j++;
    for (let k = i; k <= j; k++) ranks.push(i + j + 2);
    i = j + 1;
  }
  const sum = ranks.reduce((s, r) => s + r, 0);
  const observed = ranks.reduce((s, r, i) => s + (pooled[i]!.a ? r : 0), 0);
  const expected = (sum * n) / total;
  // ways[k][s]: the ways to choose k of the ranks so far with sum s.
  const ways = Array.from({ length: n + 1 }, () => new Float64Array(sum + 1));
  ways[0]![0] = 1;
  ranks.forEach((rank, seen) => {
    for (let k = Math.min(n, seen + 1); k >= 1; k--) {
      const from = ways[k - 1]!;
      const to = ways[k]!;
      for (let s = sum; s >= rank; s--) if (from[s - rank]) to[s]! += from[s - rank]!;
    }
  });
  const distance = Math.abs(observed - expected) - 1e-9;
  let extreme = 0;
  let all = 0;
  ways[n]!.forEach((count, s) => {
    all += count;
    if (Math.abs(s - expected) >= distance) extreme += count;
  });
  return Math.min(1, extreme / all);
}

/** The smallest p-value rankSumTest can give for groups of `n` and `m`. */
export function smallestP(n: number, m: number): number {
  let splits = 1;
  for (let i = 1; i <= n; i++) splits = (splits * (m + i)) / i;
  return Math.min(1, 2 / splits);
}

/** Holm's step-down adjustment of `ps`, tested together: a metric is
 *  called changed only if its adjusted p is under the threshold, so the
 *  chance that any of them is called changed when none changed stays under
 *  it, however many there are. */
export function holm(ps: number[]): number[] {
  const order = ps.map((p, i) => [p, i] as const).toSorted((x, y) => x[0] - y[0]);
  const adjusted: number[] = Array.from({ length: ps.length }, () => 1);
  let running = 0;
  order.forEach(([p, i], rank) => {
    running = Math.max(running, Math.min(1, (ps.length - rank) * p));
    adjusted[i] = running;
  });
  return adjusted;
}

/** The threshold of a verdict: the headline's p, or the others' adjusted p,
 *  must be under it. */
export const ALPHA = 0.05;
/** The smallest change of a median worth a verdict: at least this many
 *  milliseconds (or one, for counts) and this share of the baseline's. */
export const FLOOR_MS = 2;
export const FLOOR_SHARE = 0.02;

export type Verdict =
  | "faster"
  | "slower"
  | "lower"
  | "higher"
  | "differs"
  | "same"
  | "within noise"
  | "negligible"
  | "only in base"
  | "only in head";

/** Verdicts that report a change. */
export const CHANGED: Verdict[] = ["faster", "slower", "lower", "higher", "differs"];

export type Spread = { n: number; median: number; min: number; max: number };

export type Row = {
  metric: string;
  headline: boolean;
  base?: Spread;
  head?: Spread;
  /** head's median less base's, and that as a share of base's. */
  change: number;
  share: number;
  /** The test's p-value (Holm-adjusted except for the headline), when the
   *  metric was tested. */
  p?: number;
  verdict: Verdict;
};

const isTime = (metric: string) => !/_(count|bytes|px)$/.test(metric);
const isCount = (metric: string) => metric.endsWith("_count");

function values(runs: Run[], metric: string) {
  return runs.flatMap((run) => {
    const value = run.metrics[metric];
    return value !== undefined && Number.isFinite(value) ? [value] : [];
  });
}

function spread(list: number[]): Spread | undefined {
  if (!list.length) return undefined;
  return {
    n: list.length,
    median: percentile(list, 50),
    min: Math.min(...list),
    max: Math.max(...list),
  };
}

/** Each metric of either side, the headline first, with a verdict. A count
 *  that is the same in every run of each side (presses, rows, requests) is
 *  compared as it is: a difference there is no noise but a journey or work
 *  that differs. Every other metric is tested with rankSumTest; the
 *  headline on its own, the rest together with Holm's adjustment. A tested
 *  change is a verdict only if its median moved at least FLOOR_MS (one for
 *  counts) and FLOOR_SHARE; one that is significant but smaller is
 *  negligible. */
export function compareMetrics(base: Run[], head: Run[], headline: string): Row[] {
  const names: string[] = [];
  for (const run of [...base, ...head])
    for (const name of Object.keys(run.metrics)) if (!names.includes(name)) names.push(name);
  if (names.includes(headline)) names.splice(names.indexOf(headline), 1);
  names.unshift(headline);

  const rows: Row[] = [];
  const tested: Row[] = [];
  for (const metric of names) {
    const a = values(base, metric);
    const b = values(head, metric);
    const row: Row = {
      metric,
      headline: metric === headline,
      base: spread(a),
      head: spread(b),
      change: Number.NaN,
      share: Number.NaN,
      verdict: "within noise",
    };
    if (!row.base || !row.head) {
      if (row.base || row.head)
        rows.push({ ...row, verdict: row.base ? "only in base" : "only in head" });
      continue;
    }
    row.change = row.head.median - row.base.median;
    row.share = row.base.median === 0 ? Number.NaN : row.change / Math.abs(row.base.median);
    const constant = (s: Spread) => s.min === s.max;
    if (isCount(metric) && constant(row.base) && constant(row.head)) {
      row.verdict = row.change === 0 ? "same" : "differs";
    } else {
      row.p = rankSumTest(a, b);
      if (!row.headline) tested.push(row);
    }
    rows.push(row);
  }
  holm(tested.map((row) => row.p!)).forEach((p, i) => {
    tested[i]!.p = p;
  });
  for (const row of rows) {
    if (row.p === undefined) continue;
    if (row.p >= ALPHA) continue;
    const floor = isTime(row.metric) ? FLOOR_MS : isCount(row.metric) ? 1 : 0;
    const large =
      Math.abs(row.change) >= floor &&
      row.change !== 0 &&
      (Number.isNaN(row.share) || Math.abs(row.share) >= FLOOR_SHARE);
    if (!large) row.verdict = "negligible";
    else if (isTime(row.metric)) row.verdict = row.change < 0 ? "faster" : "slower";
    else row.verdict = row.change < 0 ? "lower" : "higher";
  }
  return rows;
}

export type Comparison = {
  scenario: string;
  description: string;
  headline: string;
  /** What each side is: a ref and commit. */
  baseLabel: string;
  headLabel: string;
  base: Profile;
  head: Profile;
  /** Why the comparison holds fewer runs than asked for, if it does. */
  partial?: string;
};

const signed = (value: number, metric: string) =>
  `${value > 0 ? "+" : value < 0 ? "−" : "±"}${format(Math.abs(value), metric)}`;
const percent = (share: number) =>
  Number.isNaN(share)
    ? ""
    : `${share > 0 ? "+" : share < 0 ? "−" : "±"}${(Math.abs(share) * 100).toFixed(1)}%`;
const pText = (p: number | undefined) =>
  p === undefined ? "–" : p < 0.001 ? "<0.001" : p >= 0.9995 ? "1" : p.toFixed(3);
const unit = (metric: string) => (isTime(metric) ? " ms" : "");
const cellOf = (s: Spread | undefined, metric: string) =>
  s
    ? s.min === s.max
      ? format(s.median, metric)
      : `${format(s.median, metric)} (${format(s.min, metric)}–${format(s.max, metric)})`
    : "–";

/** One line of the verdicts at the top of the summary. */
function verdictLine(row: Row) {
  const name = `**${row.metric}**${row.headline ? " (headline)" : ""}`;
  const medians = `${format(row.base?.median ?? Number.NaN, row.metric)} → ${format(row.head?.median ?? Number.NaN, row.metric)}${unit(row.metric)}`;
  const change = [signed(row.change, row.metric), percent(row.share)].filter(Boolean).join(", ");
  switch (row.verdict) {
    case "differs":
      return `- ${name} differs: ${medians} in every run of each side. Not noise: the journeys or the work differ; check that is meant before reading the times.`;
    case "only in base":
    case "only in head":
      return `- ${name} is ${row.verdict}.`;
    case "within noise":
    case "negligible":
    case "same":
      return `- ${name}: ${row.verdict}, ${medians} (${change}), p = ${pText(row.p)}.`;
    default:
      return `- ${name}: **${row.verdict}**, ${medians} (${change}), p = ${pText(row.p)}.`;
  }
}

function chunkTotals(chunks: Chunk[]) {
  const initial = chunks.filter((c) => c.kind !== "lazy" && c.kind !== "lazy-css");
  const lazy = chunks.filter((c) => c.kind === "lazy" || c.kind === "lazy-css");
  const sum = (list: Chunk[], field: "bytes" | "gzip") =>
    list.reduce((total, c) => total + c[field], 0);
  return {
    initial: sum(initial, "bytes"),
    initialGzip: sum(initial, "gzip"),
    initialFiles: initial.length,
    lazy: sum(lazy, "bytes"),
    lazyFiles: lazy.length,
  };
}

const byteChange = (base: number, head: number) => {
  const change = head - base;
  const share = base ? change / base : Number.NaN;
  return `${change > 0 ? "+" : change < 0 ? "−" : "±"}${bytes(Math.abs(change))} ${percent(share)}`.trim();
};

/** The comparison's summary.md: verdicts first, then every metric of both
 *  sides, then the bundles. Short enough to read whole; each side's own
 *  summary.md holds its interactions, components and requests. */
export function summariseComparison(c: Comparison): string {
  const rows = compareMetrics(c.base.runs, c.head.runs, c.headline);
  const n = c.base.runs.length;
  const m = c.head.runs.length;
  const env = c.head.environment;
  const shared = Object.entries(env)
    .filter(([key]) => key !== "commit" && key !== "instance")
    .map(([key, value]) =>
      c.base.environment[key] === value
        ? `${key}: ${value}`
        : `${key}: ${value} (base: ${c.base.environment[key] ?? "unknown"})`,
    );
  const lines: string[] = [
    `# Profile comparison: ${c.scenario}`,
    "",
    c.description,
    "",
    `head ${c.headLabel} against base ${c.baseLabel}: ${m} and ${n} runs after a warm-up each, ` +
      `interleaved in pairs of alternating order, ${c.head.started}. ${shared.join("; ")}.`,
  ];
  if (c.partial) lines.push("", `**Partial: ${c.partial}**`);

  const headlineRow = rows.find((r) => r.headline);
  const changed = rows.filter((r) => CHANGED.includes(r.verdict));
  const only = rows.filter((r) => r.verdict === "only in base" || r.verdict === "only in head");
  const notesDiffer =
    JSON.stringify(c.base.notes.toSorted()) !== JSON.stringify(c.head.notes.toSorted());
  lines.push("", "## Verdict", "");
  if (headlineRow?.base && headlineRow.head) lines.push(verdictLine(headlineRow));
  else lines.push(`- **${c.headline}** (headline): not measured on both sides.`);
  // What differs in every run first: the journeys or the work differ, which
  // matters before any time does.
  for (const row of changed)
    if (!row.headline && row.verdict === "differs") lines.push(verdictLine(row));
  if (notesDiffer)
    lines.push(
      `- The journeys differ: base found ${c.base.notes.map((t) => `"${t}"`).join(" ") || "nothing to note"}; ` +
        `head found ${c.head.notes.map((t) => `"${t}"`).join(" ") || "nothing to note"}.`,
    );
  for (const row of changed)
    if (!row.headline && row.verdict !== "differs") lines.push(verdictLine(row));
  for (const row of only) lines.push(verdictLine(row));
  const others = rows.filter(
    (r) => !r.headline && !CHANGED.includes(r.verdict) && !only.includes(r),
  ).length;
  if (!changed.length && !notesDiffer && !only.length)
    lines.push(`- Nothing changed beyond noise: the ${others} other metrics are within it.`);
  else lines.push(`- The other ${others} metrics are within noise or the same.`);
  const family = rows.filter((r) => !r.headline && r.p !== undefined).length;
  const least = smallestP(n, m);
  if (least >= ALPHA)
    lines.push(
      `- ${n} and ${m} runs cannot show any change (the smallest possible p is ${pText(least)}): use --runs 5 or more.`,
    );
  else if (family > 0 && least >= ALPHA / family)
    lines.push(
      `- With ${n} and ${m} runs only the headline can show a change: the ${family} other tested ` +
        `metrics need p under ${(ALPHA / family).toFixed(4)} after Holm's adjustment, and the smallest possible p ` +
        `is ${pText(least)}. Use --runs 8 or more for verdicts on them.`,
    );
  lines.push(
    "- Decide on the headline. When nothing changed, about one comparison in ten still flags " +
      "some metric, and one in twenty the headline: run it again before keeping a change.",
  );

  lines.push(
    "",
    "## Metrics",
    "",
    "Medians, with the range of the runs; milliseconds unless the name gives a unit. change is head " +
      "less base. p is the two-sided Mann-Whitney test's, Holm-adjusted across the metrics but the " +
      `headline. A verdict needs p < ${ALPHA} and a change of at least ${FLOOR_MS} ms (1 for counts) ` +
      `and ${FLOOR_SHARE * 100}%; a smaller one is negligible. Counts the same in every run are compared as they are.`,
    "",
    ...table(
      ["metric", "base", "head", "change", "p", "verdict"],
      rows.map((r) => [
        r.headline ? `${r.metric} (headline)` : r.metric,
        cellOf(r.base, r.metric),
        cellOf(r.head, r.metric),
        Number.isNaN(r.change)
          ? "–"
          : [signed(r.change, r.metric), percent(r.share)].filter(Boolean).join(" "),
        pText(r.p),
        r.verdict,
      ]),
    ),
  );

  if (c.base.chunks.length || c.head.chunks.length) {
    const a = chunkTotals(c.base.chunks);
    const b = chunkTotals(c.head.chunks);
    lines.push(
      "",
      "## Bundle (profiling build)",
      "",
      ...table(
        ["chunks", "base", "head", "change"],
        [
          [
            "loaded by index.html",
            bytes(a.initial),
            bytes(b.initial),
            byteChange(a.initial, b.initial),
          ],
          [
            "the same, gzip",
            bytes(a.initialGzip),
            bytes(b.initialGzip),
            byteChange(a.initialGzip, b.initialGzip),
          ],
          ["loaded later", bytes(a.lazy), bytes(b.lazy), byteChange(a.lazy, b.lazy)],
          [
            "files (at once, later)",
            `${a.initialFiles}, ${a.lazyFiles}`,
            `${b.initialFiles}, ${b.lazyFiles}`,
            a.initialFiles === b.initialFiles && a.lazyFiles === b.lazyFiles ? "same" : "differ",
          ],
        ],
      ),
    );
  }
  lines.push(
    "",
    "Each side's own profile, with its interactions, components and requests: base/summary.md " +
      "and head/summary.md beside this one.",
  );
  return `${lines.join("\n")}\n`;
}

/** metrics.json of a comparison: every run's metrics with its side and its
 *  place in the order (each side's metrics.json keeps the snapshots). */
export function comparisonJson(c: Comparison) {
  const runs = (profile: Profile, side: Side) =>
    [profile.warmup, ...profile.runs].map((run) => ({
      side,
      index: run.index,
      order: run.order,
      warmup: run.index === 0,
      wall: run.wall,
      metrics: run.metrics,
    }));
  return {
    scenario: c.scenario,
    headline: c.headline,
    base: { label: c.baseLabel, environment: c.base.environment, notes: c.base.notes },
    head: { label: c.headLabel, environment: c.head.environment, notes: c.head.notes },
    ...(c.partial ? { partial: c.partial } : {}),
    runs: [...runs(c.base, "base"), ...runs(c.head, "head")].toSorted(
      (x, y) => (x.order ?? 0) - (y.order ?? 0),
    ),
    metrics: compareMetrics(c.base.runs, c.head.runs, c.headline),
  };
}

/** Why a side's daemon would not start, from the end of its daemon.out,
 *  with a hint when the base's daemon refuses the dataset this checkout's
 *  generator wrote because its stores' schema is newer than it reads. */
export function daemonFailure(out: string, side?: Side): string | undefined {
  const line = out
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean)
    .at(-1);
  if (!line) return undefined;
  const schema = /newer than|schema/i.test(line);
  return (
    `The daemon said: ${line}` +
    (schema && side === "base"
      ? ". Both sides run on the dataset this checkout's generator writes, in this checkout's " +
        "schemas, which the baseline's daemon does not read: compare against a ref with the same " +
        "store schemas, such as a commit after the migration."
      : "")
  );
}

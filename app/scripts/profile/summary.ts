// Statistics over a profile's runs and the summary.md that reports them,
// apart from the browser and the driver so they can be tested
// (summary.test.ts). The runner (run.ts) collects one Run per run.
import type { Interaction, Snapshot } from "../../src/perf-collector.ts";
import { isPageMetric, pagesSection } from "./pages-summary.ts";

export type Run = {
  /** 0 for the warm-up, then 1..N. */
  index: number;
  /** Wall-clock milliseconds the run took, driver included. */
  wall: number;
  /** The scenario's own measurements, in milliseconds unless the name ends
   *  in a unit of its own (`_count`, `_bytes`, `_px`). */
  metrics: Record<string, number>;
  snapshot: Snapshot;
};

export type Chunk = {
  file: string;
  kind: "entry" | "preload" | "css" | "lazy" | "lazy-css";
  bytes: number;
  gzip: number;
};

export type Profile = {
  scenario: string;
  description: string;
  started: string;
  environment: Record<string, string>;
  /** What the scenario found in the data, e.g. how many rows the inbox had. */
  notes: string[];
  /** Set while runs are still to come, or when one failed: why the profile
   *  holds fewer runs than asked for. */
  partial?: string;
  warmup: Run;
  runs: Run[];
  chunks: Chunk[];
};

export type Stats = { n: number; median: number; p95: number; min: number; max: number };

/** The p-th percentile (0..100) of `values`, interpolating between the two
 *  nearest ranks; NaN when there are none. */
export function percentile(values: number[], p: number): number {
  if (values.length === 0) return Number.NaN;
  const sorted = values.toSorted((a, b) => a - b);
  const rank = (p / 100) * (sorted.length - 1);
  const low = Math.floor(rank);
  const high = Math.ceil(rank);
  return sorted[low]! + (sorted[high]! - sorted[low]!) * (rank - low);
}

export function stats(values: number[]): Stats {
  return {
    n: values.length,
    median: percentile(values, 50),
    p95: percentile(values, 95),
    min: values.length ? Math.min(...values) : Number.NaN,
    max: values.length ? Math.max(...values) : Number.NaN,
  };
}

const MODIFIERS = new Set(["Shift", "Control", "Alt", "AltGraph", "Meta", "CapsLock", "Fn"]);

/** A key press that is only a modifier (`Shift`, `Ctrl+Control`), which a
 *  person holds for the key after it rather than waits on. */
export function modifierOnly(interaction: Pick<Interaction, "type" | "target">) {
  return (
    interaction.type === "keydown" && MODIFIERS.has(interaction.target.split("+").at(-1) ?? "")
  );
}

/** The interactions a person waits on: modifier-only key presses left out. */
export function interactions(snapshot: Snapshot) {
  return snapshot.interactions.filter((i) => !modifierOnly(i));
}

/** The region around the whole app: the others are inside it, so totals
 *  come from it alone rather than from a sum that counts them twice. */
export const ROOT_REGION = "app";

/** What every run reports, whatever its scenario, from the collector's
 *  snapshot: added to the scenario's own metrics. */
export function standardMetrics(snapshot: Snapshot): Record<string, number> {
  const durations = interactions(snapshot).map((i) => i.duration);
  const long = snapshot.frames.long;
  const root = snapshot.renders[ROOT_REGION];
  const metrics: Record<string, number> = {
    interactions_count: durations.length,
    long_frames_count: long.length,
    long_frame_total: long.reduce((sum, frame) => sum + frame.duration, 0),
    worst_frame: long.reduce((worst, frame) => Math.max(worst, frame.duration), 0),
    commits_count: root?.commits ?? 0,
    render_total: root?.actual ?? 0,
    requests_count: snapshot.requests.length,
    dom_count: snapshot.dom,
  };
  if (durations.length) {
    metrics.interaction_median = percentile(durations, 50);
    metrics.interaction_p95 = percentile(durations, 95);
    metrics.interaction_max = Math.max(...durations);
  }
  if (snapshot.heap !== null) metrics.heap_bytes = snapshot.heap;
  return metrics;
}

/** Each metric's statistics over the runs, in the order first reported. A
 *  run that lacks a metric, or has no number for it (NaN), leaves it out
 *  rather than counting zero or spoiling the rest. */
export function metricStats(runs: Run[]): [string, Stats][] {
  const names: string[] = [];
  for (const run of runs)
    for (const name of Object.keys(run.metrics)) if (!names.includes(name)) names.push(name);
  return names.map((name) => [
    name,
    stats(
      runs.flatMap((run) => {
        const value = run.metrics[name];
        return value !== undefined && Number.isFinite(value) ? [value] : [];
      }),
    ),
  ]);
}

export type Slow = Interaction & { run: number };

/** The `limit` slowest interactions of all runs, slowest first. */
export function slowest(runs: Run[], limit = 10): Slow[] {
  return runs
    .flatMap((run) => interactions(run.snapshot).map((i) => ({ ...i, run: run.index })))
    .toSorted((a, b) => b.duration - a.duration)
    .slice(0, limit);
}

/** Interactions of all runs grouped by type and target (`keydown j`). */
export function interactionGroups(runs: Run[]) {
  const groups = new Map<string, Interaction[]>();
  for (const run of runs)
    for (const i of interactions(run.snapshot)) {
      const key = `${i.type} ${i.target}`;
      groups.set(key, [...(groups.get(key) ?? []), i]);
    }
  return [...groups]
    .map(([name, list]) => ({
      name,
      count: list.length,
      duration: stats(list.map((i) => i.duration)),
      delay: percentile(
        list.map((i) => i.delay),
        50,
      ),
      processing: percentile(
        list.flatMap((i) => (i.processing === undefined ? [] : [i.processing])),
        50,
      ),
    }))
    .toSorted((a, b) => b.duration.p95 - a.duration.p95);
}

/** Per `<Profiled>` region, the median over runs of its commits, mounts and
 *  render time, sorted by render time. */
export function regions(runs: Run[]) {
  const ids = [...new Set(runs.flatMap((run) => Object.keys(run.snapshot.renders)))];
  const median = (id: string, field: "commits" | "mounts" | "actual" | "max" | "base") =>
    percentile(
      runs.map((run) => run.snapshot.renders[id]?.[field] ?? 0),
      50,
    );
  return ids
    .map((id) => ({
      id,
      commits: median(id, "commits"),
      mounts: median(id, "mounts"),
      actual: median(id, "actual"),
      max: median(id, "max"),
      base: median(id, "base"),
    }))
    .toSorted((a, b) => b.actual - a.actual);
}

/** Request paths without their query, so `api/document?pr=1` and `?pr=2` are
 *  one row; per run, how many and how long. */
export function requestGroups(runs: Run[], limit = 8) {
  const groups = new Map<string, number[]>();
  for (const run of runs)
    for (const r of run.snapshot.requests) {
      const key = `${r.method} ${r.path.split("?")[0]}`;
      groups.set(key, [...(groups.get(key) ?? []), r.duration]);
    }
  return [...groups]
    .map(([name, durations]) => ({
      name,
      perRun: durations.length / Math.max(1, runs.length),
      duration: stats(durations),
      total: durations.reduce((sum, d) => sum + d, 0) / Math.max(1, runs.length),
    }))
    .toSorted((a, b) => b.total - a.total)
    .slice(0, limit);
}

/** A number for a table: milliseconds to one decimal, counts whole, bytes
 *  in KiB or MiB. */
export function format(value: number, name = ""): string {
  if (Number.isNaN(value)) return "–";
  if (name.endsWith("_bytes")) return bytes(value);
  if (name.endsWith("_count") || name.endsWith("_px")) {
    return Number.isInteger(value) ? String(value) : value.toFixed(1);
  }
  return value.toFixed(1);
}

export function bytes(value: number): string {
  if (value >= 1024 * 1024) return `${(value / 1024 / 1024).toFixed(2)} MiB`;
  if (value >= 1024) return `${(value / 1024).toFixed(1)} KiB`;
  return `${value} B`;
}

const cell = (text: string) => text.replace(/\|/g, "\\|").replace(/\n/g, " ");

const numeric = /^(–|-?[\d.]+( (B|KiB|MiB))?)$/;

/** A Markdown table, its columns of numbers aligned right. */
export function table(head: string[], rows: string[][]): string[] {
  const right = head.map((_, i) => i > 0 && rows.every((row) => numeric.test(row[i] ?? "")));
  return [
    `| ${head.join(" | ")} |`,
    `| ${right.map((r) => (r ? "---:" : "---")).join(" | ")} |`,
    ...rows.map((row) => `| ${row.map(cell).join(" | ")} |`),
  ];
}

/** summary.md: short enough to read whole. */
export function summarise(profile: Profile): string {
  const { runs } = profile;
  const lines: string[] = [
    `# Profile: ${profile.scenario}`,
    "",
    profile.description,
    "",
    `${runs.length} runs after one warm-up, ${profile.started}. ` +
      Object.entries(profile.environment)
        .map(([key, value]) => `${key}: ${value}`)
        .join("; ") +
      ".",
    "Weston renders in software: script, style, layout and React times are realistic, " +
      "paint and composite times are not.",
  ];
  if (profile.partial) lines.push("", `**Partial: ${profile.partial}**`);
  if (profile.notes.length) lines.push("", ...profile.notes.map((note) => `- ${note}`));
  lines.push(...pagesSection(profile));

  lines.push(
    "",
    "## Metrics",
    "",
    "Milliseconds over the runs unless the name gives a unit; p95 interpolates between the slowest runs.",
    "",
    ...table(
      ["metric", "median", "p95", "min", "max"],
      // The pages scenario's per-page numbers are in its Pages table.
      metricStats(runs)
        .filter(([name]) => !isPageMetric(name))
        .map(([name, s]) => [
          name,
          format(s.median, name),
          format(s.p95, name),
          format(s.min, name),
          format(s.max, name),
        ]),
    ),
  );

  const groups = interactionGroups(runs);
  if (groups.length) {
    lines.push(
      "",
      "## Interactions",
      "",
      "All runs together; from the input to the frame after it, modifier keys left out.",
      "",
      ...table(
        ["input", "count", "median", "p95", "max", "delay", "processing"],
        groups
          .slice(0, 8)
          .map((g) => [
            g.name,
            String(g.count),
            format(g.duration.median),
            format(g.duration.p95),
            format(g.duration.max),
            format(g.delay),
            format(g.processing),
          ]),
      ),
      "",
      "Slowest:",
      "",
      ...table(
        ["run", "input", "duration", "delay", "processing", "at"],
        slowest(runs, 8).map((i) => [
          String(i.run),
          `${i.type} ${i.target}`,
          format(i.duration),
          format(i.delay),
          i.processing === undefined ? "–" : format(i.processing),
          format(i.start),
        ]),
      ),
    );
  }

  const byRegion = regions(runs);
  if (byRegion.length) {
    lines.push(
      "",
      "## Components",
      "",
      "Per `<Profiled>` region, the median run: render time is React's actual duration. " +
        "`app` holds the others, so render_total and commits_count are its own.",
      "",
      ...table(
        ["region", "render ms", "commits", "mounts", "slowest commit", "full render (base)"],
        byRegion.map((r) => [
          r.id,
          format(r.actual),
          format(r.commits, "_count"),
          format(r.mounts, "_count"),
          format(r.max),
          format(r.base),
        ]),
      ),
    );
  }

  const requests = requestGroups(runs);
  if (requests.length) {
    lines.push(
      "",
      "## Requests",
      "",
      "By time spent per run, to the response's headers.",
      "",
      ...table(
        ["request", "per run", "median", "p95"],
        requests.map((r) => [
          r.name,
          format(r.perRun, "_count"),
          format(r.duration.median),
          format(r.duration.p95),
        ]),
      ),
    );
  }

  if (profile.chunks.length) {
    const initial = profile.chunks.filter((c) => c.kind !== "lazy" && c.kind !== "lazy-css");
    const lazy = profile.chunks
      .filter((c) => c.kind === "lazy" || c.kind === "lazy-css")
      .toSorted((a, b) => b.bytes - a.bytes);
    const sum = (list: Chunk[], field: "bytes" | "gzip") =>
      list.reduce((total, c) => total + c[field], 0);
    lines.push(
      "",
      "## Bundle (profiling build)",
      "",
      "The profiling build's chunks, which carry React's profiling build and the collector: " +
        "production's are somewhat smaller.",
      "",
      `Loaded by index.html: ${bytes(sum(initial, "bytes"))} (${bytes(sum(initial, "gzip"))} gzip) ` +
        `in ${initial.length} files. Loaded later: ${bytes(sum(lazy, "bytes"))} in ${lazy.length} files, ` +
        "the largest below.",
      "",
      ...table(
        ["file", "kind", "size", "gzip"],
        [...initial.toSorted((a, b) => b.bytes - a.bytes), ...lazy.slice(0, 6)].map((c) => [
          c.file,
          c.kind,
          bytes(c.bytes),
          bytes(c.gzip),
        ]),
      ),
    );
  }
  return `${lines.join("\n")}\n`;
}

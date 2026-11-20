// What the `pages` scenario (pages.ts) reports per page and way, apart from
// the browser so it can be tested (pages-summary.test.ts): which commits,
// requests and frames belong to which page, and the one table of the
// summary. Per-page numbers travel in a run's metrics named
// `page:<label>:<way>:<field>` (labels hold no colon), and the summary reads
// them from there, so the runner and the other scenarios need no change.
import type { Snapshot } from "../../src/perf-collector.ts";
import { format, percentile, ROOT_REGION, table, type Profile, type Run } from "./summary.ts";

/** In-app navigation (the page's key or a click), or a direct load of its
 *  address (a reload). */
export type Way = "nav" | "direct";

export const PAGE_PREFIX = "page:";
export const isPageMetric = (name: string) => name.startsWith(PAGE_PREFIX);
export const pageMetric = (label: string, way: Way, field: string) =>
  `${PAGE_PREFIX}${label}:${way}:${field}`;

/** What one page's window of a snapshot holds. */
export type Attributed = {
  /** The page's navigation: first content frame, and settled, in ms from
   *  its start. Absent when it never showed content or settle. */
  content?: number;
  settled?: number;
  /** React commits and render time of the root `app` region alone: the
   *  other regions are inside it. */
  commits_count: number;
  render_total: number;
  long_frames_count: number;
  long_frame_total: number;
  requests_count: number;
  /** The sum of the requests' time to their headers. */
  request_total: number;
};

/** The part of `snapshot` that belongs to the page shown from `from` to
 *  `to` (page clock, both ends included): the last navigation that started
 *  there, and the commits, long frames and requests that began there. */
export function attribute(snapshot: Snapshot, from: number, to: number): Attributed {
  const within = (at: number) => at >= from && at <= to;
  const navigation = snapshot.navigations.filter((n) => within(n.start)).at(-1);
  const commits = snapshot.commits.filter((c) => c.id === ROOT_REGION && within(c.at));
  const long = snapshot.frames.long.filter((f) => within(f.start));
  const requests = snapshot.requests.filter((r) => within(r.start));
  return {
    ...(navigation?.content === undefined ? {} : { content: navigation.content }),
    ...(navigation?.settled === undefined ? {} : { settled: navigation.settled }),
    commits_count: commits.length,
    render_total: commits.reduce((sum, c) => sum + c.actual, 0),
    long_frames_count: long.length,
    long_frame_total: long.reduce((sum, f) => sum + f.duration, 0),
    requests_count: requests.length,
    request_total: requests.reduce((sum, r) => sum + r.duration, 0),
  };
}

/** The mark names around a page visited by navigation. */
export const navMark = (label: string, edge: "start" | "end") => `nav:${label}:${edge}`;

/** Each page visited by navigation (marks `nav:<label>:start` and `:end`),
 *  with its window's numbers, in the order visited. A page lacking either
 *  mark is left out. */
export function navigationPages(snapshot: Snapshot): { label: string; page: Attributed }[] {
  const labels: string[] = [];
  for (const mark of snapshot.marks) {
    const found = /^nav:(.*):start$/.exec(mark.name);
    if (found && !labels.includes(found[1]!)) labels.push(found[1]!);
  }
  return labels.flatMap((label) => {
    const at = (edge: "start" | "end") =>
      snapshot.marks.findLast((m) => m.name === navMark(label, edge))?.at;
    const from = at("start");
    const to = at("end");
    return from === undefined || to === undefined
      ? []
      : [{ label, page: attribute(snapshot, from, to) }];
  });
}

/** `page:<label>:<way>:<field>` metrics of one page and way. */
export function pageMetrics(
  label: string,
  way: Way,
  page: Partial<Attributed>,
  extra: Record<string, number>,
) {
  const metrics: Record<string, number> = {};
  for (const [field, value] of Object.entries({ ...page, ...extra }))
    if (value !== undefined) metrics[pageMetric(label, way, field)] = value;
  return metrics;
}

export type PageRow = {
  label: string;
  way: Way;
  /** The page had no content to show in every run: it is not timed. */
  empty: boolean;
  /** Medians over the runs; NaN where no run has the number. */
  content: number;
  settled: number;
  commits: number;
  render: number;
  longFrames: number;
  requests: number;
  requestTime: number;
  dom: number;
};

/** One row per page and way, slowest content first; empty pages last. */
export function pageRows(runs: Run[]): PageRow[] {
  const keys = new Map<string, { label: string; way: Way }>();
  for (const run of runs)
    for (const name of Object.keys(run.metrics)) {
      if (!isPageMetric(name)) continue;
      const [label, way] = name.slice(PAGE_PREFIX.length).split(":");
      if (label && (way === "nav" || way === "direct")) keys.set(`${label}:${way}`, { label, way });
    }
  const rows = [...keys.values()].map(({ label, way }): PageRow => {
    const median = (field: string) =>
      percentile(
        runs.flatMap((run) => {
          const value = run.metrics[pageMetric(label, way, field)];
          return value !== undefined && Number.isFinite(value) ? [value] : [];
        }),
        50,
      );
    return {
      label,
      way,
      empty: median("empty_count") >= 0.5,
      content: median("content"),
      settled: median("settled"),
      commits: median("commits_count"),
      render: median("render_total"),
      longFrames: median("long_frames_count"),
      requests: median("requests_count"),
      requestTime: median("request_total"),
      dom: median("dom_count"),
    };
  });
  // NaN (no content frame) sorts with the slowest of the full pages.
  const rank = (row: PageRow) => (Number.isNaN(row.content) ? Infinity : row.content);
  return rows.toSorted(
    (a, b) =>
      Number(a.empty) - Number(b.empty) || rank(b) - rank(a) || a.label.localeCompare(b.label),
  );
}

/** The summary's table of pages: empty when the run measured none. */
export function pagesSection(profile: Profile): string[] {
  const rows = pageRows(profile.runs);
  if (!rows.length) return [];
  const none = "–";
  return [
    "",
    "## Pages",
    "",
    "Median over the runs, slowest content first. `nav` moves to the page the way a person does " +
      "(its key or a click); `direct` reloads its address. Content and settled are the " +
      "collector's navigation times; commits and render ms are the root `app` region's, " +
      "counted in the page's window alone; DOM is the elements after it settled. " +
      "`empty` pages had no data to show on this dataset and are not timed.",
    "",
    ...table(
      [
        "page",
        "way",
        "content",
        "settled",
        "commits",
        "render ms",
        "long frames",
        "requests",
        "request ms",
        "DOM",
      ],
      rows.map((r) => [
        r.label,
        r.way,
        r.empty ? "empty" : format(r.content),
        r.empty ? none : format(r.settled),
        r.empty ? none : format(r.commits, "_count"),
        r.empty ? none : format(r.render),
        r.empty ? none : format(r.longFrames, "_count"),
        r.empty ? none : format(r.requests, "_count"),
        r.empty ? none : format(r.requestTime),
        format(r.dom, "_count"),
      ]),
    ),
  ];
}

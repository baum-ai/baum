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
  /** The page's first content frame, and when it settled (no loader and no
   *  request in flight), in ms: from the start of its first navigation in
   *  the window (in-app), or from the document's own start (`origin`, a
   *  direct load), which the bundle's fetch and parse are part of. Absent
   *  when it never showed content or settled. */
  content?: number;
  settled?: number;
  /** How many navigations began in the window: more than one means the app
   *  moved on again by itself (a redirect it pushed). */
  navigations_count: number;
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
 *  `to` (page clock, both ends included): its navigations, and the commits,
 *  long frames and requests that began there (a request begun in the window
 *  counts whole, though it ends after it). The page's times run from its
 *  first navigation's start to the last one's content and settled frames,
 *  so a second navigation the app made itself is part of the wait; with
 *  `origin`, they run from the page clock's zero, the document's start
 *  (a direct load: the collector's own start is later, after the bundle). */
export function attribute(
  snapshot: Snapshot,
  from: number,
  to: number,
  origin = false,
): Attributed {
  const within = (at: number) => at >= from && at <= to;
  const navigations = snapshot.navigations.filter((n) => within(n.start));
  const first = navigations[0];
  const last = navigations.at(-1);
  const base = origin ? 0 : (first?.start ?? 0);
  const since = (n: number | undefined) =>
    last && n !== undefined ? last.start - base + n : undefined;
  const content = since(last?.content);
  const settled = since(last?.settled);
  const commits = snapshot.commits.filter((c) => c.id === ROOT_REGION && within(c.at));
  const long = snapshot.frames.long.filter((f) => within(f.start));
  const requests = snapshot.requests.filter((r) => within(r.start));
  return {
    ...(content === undefined ? {} : { content }),
    ...(settled === undefined ? {} : { settled }),
    navigations_count: navigations.length,
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
  /** How many runs reported the page, and in how many it had no content to
   *  show. All of them: it is `empty` and not timed. Some: it is `mixed`,
   *  which is a failure of the page or the data, not a result. */
  runs: number;
  emptyRuns: number;
  empty: boolean;
  mixed: boolean;
  /** Medians over the runs that showed content (all of them for an empty
   *  page); NaN where none has the number. */
  content: number;
  settled: number;
  /** In how many of the counted runs the page showed content and settled. */
  contentRuns: number;
  settledRuns: number;
  countedRuns: number;
  commits: number;
  render: number;
  longFrames: number;
  requests: number;
  requestTime: number;
  dom: number;
};

/** One row per page and way, slowest content first; mixed and empty pages
 *  last. */
export function pageRows(runs: Run[]): PageRow[] {
  const keys = new Map<string, { label: string; way: Way }>();
  for (const run of runs)
    for (const name of Object.keys(run.metrics)) {
      if (!isPageMetric(name)) continue;
      const [label, way] = name.slice(PAGE_PREFIX.length).split(":");
      if (label && (way === "nav" || way === "direct")) keys.set(`${label}:${way}`, { label, way });
    }
  const rows = [...keys.values()].map(({ label, way }): PageRow => {
    const seen = runs.filter(
      (run) => run.metrics[pageMetric(label, way, "empty_count")] !== undefined,
    );
    const isEmpty = (run: Run) => run.metrics[pageMetric(label, way, "empty_count")] === 1;
    const emptyRuns = seen.filter(isEmpty).length;
    // Mixed runs are timed over the ones that showed content.
    const counted = emptyRuns === seen.length ? seen : seen.filter((run) => !isEmpty(run));
    const median = (field: string) =>
      percentile(
        counted.flatMap((run) => {
          const value = run.metrics[pageMetric(label, way, field)];
          return value !== undefined && Number.isFinite(value) ? [value] : [];
        }),
        50,
      );
    return {
      label,
      way,
      runs: seen.length,
      emptyRuns,
      empty: seen.length > 0 && emptyRuns === seen.length,
      mixed: emptyRuns > 0 && emptyRuns < seen.length,
      content: median("content"),
      settled: median("settled"),
      contentRuns: counted.filter(
        (run) => run.metrics[pageMetric(label, way, "content")] !== undefined,
      ).length,
      settledRuns: counted.filter(
        (run) => run.metrics[pageMetric(label, way, "settled")] !== undefined,
      ).length,
      countedRuns: counted.length,
      commits: median("commits_count"),
      render: median("render_total"),
      longFrames: median("long_frames_count"),
      requests: median("requests_count"),
      requestTime: median("request_total"),
      dom: median("dom_count"),
    };
  });
  // NaN (no content frame) sorts with the slowest of the full pages; a
  // mixed page, whose numbers are not a result, goes with the empty ones.
  const rank = (row: PageRow) => (Number.isNaN(row.content) ? Infinity : row.content);
  const last = (row: PageRow) => Number(row.empty || row.mixed);
  return rows.toSorted(
    (a, b) => last(a) - last(b) || rank(b) - rank(a) || a.label.localeCompare(b.label),
  );
}

/** What is wrong with the data's emptiness, one line each: a page empty in
 *  some runs and not others, or empty one way and not the other. */
export function emptyWarnings(rows: PageRow[]): string[] {
  const lines: string[] = [];
  for (const row of rows)
    if (row.mixed)
      lines.push(
        `**${row.label} (${row.way}) was empty in ${row.emptyRuns} of ${row.runs} runs**, so its row is not a result.`,
      );
  for (const row of rows) {
    if (row.way !== "nav") continue;
    const other = rows.find((r) => r.label === row.label && r.way === "direct");
    if (other && row.empty !== other.empty && !row.mixed && !other.mixed)
      lines.push(
        `**${row.label} is ${row.empty ? "empty" : "not empty"} by navigation and ${other.empty ? "empty" : "not empty"} by direct load.**`,
      );
  }
  return lines;
}

/** A time over the runs that have it: plain when all do, `n/N` when some
 *  do, `never` when none (a page that waits on GitHub). */
function outOf(value: number, have: number, of: number) {
  if (have === of) return format(value);
  return have === 0 ? `never (0/${of})` : `${format(value)} (${have}/${of})`;
}

/** The summary's table of pages: empty when the run measured none. */
export function pagesSection(profile: Profile): string[] {
  const rows = pageRows(profile.runs);
  if (!rows.length) return [];
  const none = "–";
  const warnings = emptyWarnings(rows);
  return [
    "",
    "## Pages",
    "",
    "Median over the runs, slowest content first. `nav` moves to the page the way a person does " +
      "(its key, or a click on its link) from the page before; its times run from the start of " +
      "that navigation, with the app's code and its in-memory caches already loaded, so a nav " +
      "row depends on what came before it. `direct` reloads the page's address, and its times " +
      "run from the document's own start, so fetching and parsing the bundle is in them. " +
      "Which of the two goes first in a run alternates. Content is the first frame with no " +
      "loader and settled the first with no request in flight; commits and render ms are the " +
      "root `app` region's, counted in the page's window alone; DOM is the elements after it " +
      "settled; `n/N` in a time is the runs that reached it, for a page that waits on GitHub. `empty` pages had no data to show on this dataset and are not timed.",
    ...(warnings.length ? ["", ...warnings.map((w) => `- ${w}`)] : []),
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
        r.empty
          ? "empty"
          : r.mixed
            ? `empty in ${r.emptyRuns}/${r.runs}`
            : outOf(r.content, r.contentRuns, r.countedRuns),
        r.empty || r.mixed ? none : outOf(r.settled, r.settledRuns, r.countedRuns),
        r.empty || r.mixed ? none : format(r.commits, "_count"),
        r.empty || r.mixed ? none : format(r.render),
        r.empty || r.mixed ? none : format(r.longFrames, "_count"),
        r.empty || r.mixed ? none : format(r.requests, "_count"),
        r.empty || r.mixed ? none : format(r.requestTime),
        format(r.dom, "_count"),
      ]),
    ),
  ];
}

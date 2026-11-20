// The journeys `just desktop::profile` runs against the real app. Before
// each run the runner puts back the state the app keeps (run.ts,
// restoreState); the run then loads its start page afresh (or, for
// cold-start, launches the app), and resets the collector, so a run's
// snapshot holds the journey alone. A journey that does not go as planned
// fails rather than report numbers for something else. The runner supplies
// the Context.
import type { Navigation, Snapshot } from "../../src/perf-collector.ts";
import { pagesScenario } from "./pages.ts";
import { percentile, ROOT_REGION } from "./summary.ts";
import { KEY } from "./webdriver.ts";

/** A scenario that cannot run on this data says why. */
export class Skip extends Error {}

export type Context = {
  /** Quits the app and launches it again; resolves with the milliseconds
   *  until the daemon's page runs the collector, and the wall-clock time
   *  (Date.now()) the launch started. */
  launch(): Promise<{ wall: number; startedAt: number }>;
  /** Clears every page's typed filter through the daemon. */
  clearFilters(): Promise<void>;
  /** Loads the app's page at `search` (`?inbox=…`, or "" for the start)
   *  afresh and waits until it settled. */
  open(search: string): Promise<Navigation>;
  /** Runs `body`, the body of an async function, in the page and returns
   *  what it returns; `arguments[0]` and on are `args`. */
  execute<T>(body: string, args?: unknown[]): Promise<T>;
  /** Waits until `body`, a function body, returns something truthy, and
   *  returns that; fails with `what` after `timeout` milliseconds. */
  waitFor<T>(body: string, what: string, timeout?: number, args?: unknown[]): Promise<T>;
  /** Waits until the latest navigation settled, or until `timeout`, when
   *  it returns the navigation as it stands. */
  settled(timeout?: number): Promise<Navigation>;
  /** Presses `key` with `modifiers` held and waits for the frame after it,
   *  as the collector records it, then a short pause, as a person leaves. */
  press(key: string, modifiers?: string[]): Promise<void>;
  /** Forgets what the collector recorded so far. */
  reset(): Promise<void>;
  mark(name: string): Promise<void>;
  sleep(ms: number): Promise<void>;
  /** A line for the summary about what the data held. */
  note(text: string): void;
};

export type Scenario<State = unknown> = {
  name: string;
  description: string;
  /** The metric a comparison (--baseline) judges first and on its own:
   *  what a person waits on in this journey. */
  headline: string;
  /** Once, on the launched app, before the warm-up: finds what the runs
   *  need in the data, or throws Skip. */
  prepare?(ctx: Context): Promise<State>;
  /** One run; returns the scenario's own metrics. */
  run(ctx: Context, state: State): Promise<Record<string, number>>;
  /** Further metrics from the run's snapshot. */
  after?(snapshot: Snapshot): Record<string, number>;
  /** Undoes what the run left, after its snapshot, so it is not measured. */
  teardown?(ctx: Context, state: State): Promise<void>;
};

const ROWS = ".upnext-list [data-row]";
const SELECTED = ".upnext-list [data-row][data-focused]";

type InboxLink = { search: string; label: string; count: number; current: boolean };

/** The sidebar's inboxes, in order. */
function inboxes(ctx: Context) {
  return ctx.execute<InboxLink[]>(`
    return [...document.querySelectorAll("nav[aria-label=Main] a.local-rail-sub")]
      .filter((a) => /[?&]inbox=/.test(a.getAttribute("href") ?? ""))
      .map((a) => ({
        search: new URL(a.href).search,
        label: (a.querySelector(".local-rail-label")?.textContent ?? a.textContent ?? "").trim(),
        count: Number((a.querySelector(".local-rail-sub-count")?.textContent ?? "").replace(/[^0-9]/g, "")) || 0,
        current: a.getAttribute("aria-current") === "page",
      }));`);
}

/** How many rows the Inbox lists in all, on every page: the pagination's
 *  "1–30 of 75", or the rows shown when it fits on one page. */
const inboxTotal = (ctx: Context) =>
  ctx.execute<number>(
    `const pages = document.querySelector("nav[aria-label='Inbox pagination'] [aria-live]")?.textContent ?? "";
     const total = pages.match(/of\\s*([\\d,]+)/);
     return total ? Number(total[1].replace(/,/g, "")) : document.querySelectorAll(arguments[0]).length;`,
    [ROWS],
  );

const rowCount = (ctx: Context) =>
  ctx.execute<number>(`return document.querySelectorAll(${JSON.stringify(ROWS)}).length;`);
/** Waits until the inbox shows rows, or says it has none. */
const inboxShown = (ctx: Context) =>
  ctx.waitFor(
    `return document.querySelector(${JSON.stringify(ROWS)}) || (document.querySelector(".upnext-list") && !document.querySelector(".loading-status"));`,
    "the Inbox to show its list",
    30_000,
  );

/** The median and slowest content and settled times of `navigations`. */
function navigationMetrics(prefix: string, navigations: Navigation[]) {
  const done = navigations.filter((n) => n.content !== undefined);
  const content = done.map((n) => n.content!);
  const settled = done.flatMap((n) => (n.settled === undefined ? [] : [n.settled]));
  const metrics: Record<string, number> = {};
  if (content.length) {
    metrics[`${prefix}_content_median`] = percentile(content, 50);
    metrics[`${prefix}_content_max`] = Math.max(...content);
  }
  if (settled.length) {
    metrics[`${prefix}_settled_median`] = percentile(settled, 50);
    metrics[`${prefix}_settled_max`] = Math.max(...settled);
  }
  return metrics;
}

const coldStart: Scenario = {
  name: "cold-start",
  headline: "launch_to_content",
  description:
    "Quits and launches the app, then waits for the Inbox's content. launch_to_content runs from " +
    "the launch to the Inbox's first content frame; the other times are from the document's " +
    "navigation start unless they say otherwise. The daemon keeps running between launches, " +
    "as it does for a user, and WebKit's cache in the instance's home stays warm.",
  async prepare(ctx) {
    await ctx.open("");
  },
  async run(ctx) {
    const launch = await ctx.launch();
    const navigation = await ctx.settled(60_000);
    if (navigation.content === undefined)
      throw new Error("the Inbox showed no content within a minute of the launch");
    await inboxShown(ctx);
    const timing = await ctx.execute<Record<string, number>>(`
      const n = performance.getEntriesByType("navigation")[0];
      const origin = performance.timeOrigin;
      const scripts = performance.getEntriesByType("resource")
        .filter((r) => /\\.(js|css)$/.test(new URL(r.name).pathname));
      return {
        response_end: n.responseEnd,
        dom_content_loaded: n.domContentLoadedEventEnd,
        load_event: n.loadEventEnd,
        assets_count: scripts.length,
        assets_end: Math.max(0, ...scripts.map((r) => r.responseEnd)),
        origin,
      };`);
    // The content frame on the wall clock (the page's time origin plus the
    // page time), less when the launch started: not when polling saw it.
    const contentAt = timing.origin! + navigation.start + navigation.content;
    const metrics: Record<string, number> = {
      launch_to_content: contentAt - launch.startedAt,
      launch_to_page_wall: launch.wall,
      response_end: timing.response_end!,
      assets_count: timing.assets_count!,
      assets_end: timing.assets_end!,
      collector_start: navigation.start,
      dom_content_loaded: timing.dom_content_loaded!,
      content: navigation.start + navigation.content,
      load_event: timing.load_event!,
    };
    if (navigation.settled !== undefined) {
      metrics.settled = navigation.start + navigation.settled;
      metrics.launch_to_settled = timing.origin! + metrics.settled - launch.startedAt;
    }
    metrics.rows_count = await rowCount(ctx);
    return metrics;
  },
};

/** A list j and k move through: an inbox, or the Pull requests page. */
type List = { search: string; label: string; rows: string; selected: string; count: number };

const listRows = (ctx: Context, list: List) =>
  ctx.execute<number>("return document.querySelectorAll(arguments[0]).length;", [list.rows]);
/** Where the selection is among the rows shown, -1 for none. */
const listPosition = (ctx: Context, list: List) =>
  ctx.execute<number>(
    "const selected = document.querySelector(arguments[1]); return [...document.querySelectorAll(arguments[0])].indexOf(selected);",
    [list.rows, list.selected],
  );
const listShown = (ctx: Context, list: List) =>
  ctx.waitFor(
    `return document.querySelector(arguments[0]) || (document.querySelector("main.local-main") && !document.querySelector("main .loading-status"));`,
    `${list.label} to show its list`,
    30_000,
    [list.rows],
  );

const PRESSES = 50;

const jk: Scenario<List> = {
  name: "jk",
  headline: "interaction_median",
  description:
    `Presses j and k ${PRESSES} times on the list with the most pull requests (an inbox or ` +
    "Pull requests), down its page of rows and back up, each press after the frame of the one before.",
  async prepare(ctx) {
    await ctx.open("");
    const lists: List[] = [
      ...(await inboxes(ctx)).map((inbox) => ({
        search: inbox.search,
        label: `Inbox "${inbox.label}"`,
        rows: ROWS,
        selected: SELECTED,
        count: 0,
      })),
      {
        search: "?page=pulls",
        label: "Pull requests",
        rows: "[data-row-id]",
        selected: "[data-row-id][data-focused]",
        count: 0,
      },
    ];
    for (const list of lists) {
      await ctx.open(list.search);
      await listShown(ctx, list);
      list.count = await listRows(ctx, list);
    }
    const best = lists.reduce((a, b) => (b.count > a.count ? b : a));
    if (best.count < 2)
      throw new Skip(
        `no list shows two rows (${lists.map((l) => `${l.label}: ${l.count}`).join(", ")}); j/k needs two`,
      );
    ctx.note(
      `${best.label} shows ${best.count} rows on its first page, the most of ${lists.length} lists` +
        (best.count <= PRESSES / 2
          ? `, so the ${PRESSES} presses go down and up it more than once.`
          : "."),
    );
    return best;
  },
  async run(ctx, list) {
    await ctx.open(list.search);
    await listShown(ctx, list);
    const rows = await listRows(ctx, list);
    await ctx.press("g");
    let position = await listPosition(ctx, list);
    if (position < 0) throw new Error(`g selected no row of ${list.label}`);
    await ctx.reset();
    await ctx.mark("start");
    let down = true;
    for (let i = 0; i < PRESSES; i++) {
      if (down && position === rows - 1) down = false;
      if (!down && position === 0) down = true;
      const key = down ? "j" : "k";
      await ctx.press(key);
      const now = await listPosition(ctx, list);
      if (now !== position + (down ? 1 : -1))
        throw new Error(
          `press ${i + 1} (${key}) moved the selection from row ${position} to ${now} of ${rows}`,
        );
      position = now;
    }
    await ctx.mark("end");
    return { presses_count: PRESSES, rows_count: rows };
  },
};

type ReviewState = { search: string; places: number };
/** The review places (overview, sections) the review scenario reads at most. */
const PLACES = 12;
/** The most 400 px steps one place is scrolled. */
const SCROLL_STEPS = 500;

const reviewShown = (ctx: Context) =>
  ctx.waitFor(
    `return document.querySelector("[data-review-page]") && !document.querySelector(".local-main .loading-status");`,
    "the review page to show",
    60_000,
  );

/** Goes on to the review's next place (section) with Shift+J, as a person
 *  does; false when there is none. */
async function nextPlace(ctx: Context) {
  const before = await ctx.execute<string>("return location.search;");
  await ctx.press("J", [KEY.shift]);
  const moved = await ctx
    .waitFor("return location.search !== arguments[0];", "the next place", 3_000, [before])
    .then(() => true)
    .catch(() => false);
  if (moved) await ctx.settled(30_000);
  return moved;
}

const review: Scenario<ReviewState> = {
  name: "review",
  headline: "open_content",
  description:
    "From the Inbox, opens the largest pull request with a review (by lines changed, or by " +
    "review size without GitHub's counts) in the " +
    `app, then reads up to ${PLACES} of its places: scrolls each to its end, 400 px a frame, ` +
    "and goes on to the next with Shift+J.",
  async prepare(ctx) {
    await ctx.open("");
    type Entry = {
      clone_id?: string;
      latest?: unknown;
      pull_request: {
        repository: string;
        number: number;
        additions?: number;
        deletions?: number;
        changed_files?: number;
      };
    };
    const snapshot = await ctx.execute<{ entries?: Entry[] }>(
      `return fetch("api/index?history=1&detail=summary").then((r) => r.json());`,
    );
    const reviewed = (snapshot.entries ?? []).filter((e) => e.latest);
    if (reviewed.length === 0) throw new Skip("no pull request in the data has a review");
    const lines = (e: Entry) => (e.pull_request.additions ?? 0) + (e.pull_request.deletions ?? 0);
    const where = (e: Entry) =>
      e.clone_id
        ? `?clone=${encodeURIComponent(e.clone_id)}&pr=${e.pull_request.number}`
        : `?repository=${encodeURIComponent(e.pull_request.repository)}&pr=${e.pull_request.number}`;
    let largest: Entry;
    if (reviewed.some((e) => lines(e) > 0)) {
      largest = reviewed.reduce((a, b) => (lines(b) > lines(a) ? b : a));
      const pr = largest.pull_request;
      ctx.note(
        `Opens ${pr.repository}#${pr.number}: +${pr.additions} −${pr.deletions} in ` +
          `${pr.changed_files ?? "?"} files, the most lines changed of ${reviewed.length} reviewed pull requests.`,
      );
    } else {
      // Without GitHub's counts (no discovery data), the review document's own size.
      const sizes = await ctx.execute<number[]>(
        `const queries = arguments[0];
         const sizes = new Array(queries.length).fill(0);
         let next = 0;
         const worker = async () => {
           while (next < queries.length) {
             const i = next++;
             const r = await fetch("api/document" + queries[i]).catch(() => null);
             if (r && r.ok) sizes[i] = (await r.arrayBuffer()).byteLength;
           }
         };
         await Promise.all(Array.from({ length: 6 }, worker));
         return sizes;`,
        [reviewed.map(where)],
      );
      const at = sizes.indexOf(Math.max(...sizes));
      if (!(sizes[at]! > 0))
        throw new Skip("no reviewed pull request's review document could be read");
      largest = reviewed[at]!;
      const pr = largest.pull_request;
      ctx.note(
        `Opens ${pr.repository}#${pr.number}, whose review document (${Math.round(sizes[at]! / 1024)} KiB) is the largest ` +
          `of ${reviewed.length} reviewed pull requests; the data has no line counts from GitHub.`,
      );
    }
    // How many places Shift+J reaches, which every run must read.
    await ctx.open("");
    await ctx.execute(
      `history.pushState(null, "", arguments[0]); dispatchEvent(new PopStateEvent("popstate"));`,
      [where(largest)],
    );
    await ctx.settled(60_000);
    await reviewShown(ctx);
    let places = 1;
    while (places < PLACES && (await nextPlace(ctx))) places++;
    if (places < PLACES) ctx.note(`The review has ${places} places; every run reads them all.`);
    return { search: where(largest), places };
  },
  async run(ctx, state) {
    await ctx.open("");
    await inboxShown(ctx);
    await ctx.reset();
    await ctx.mark("open");
    // As the app's own links do: a history entry the app follows.
    await ctx.execute(
      `history.pushState(null, "", arguments[0]); dispatchEvent(new PopStateEvent("popstate"));`,
      [state.search],
    );
    const navigation = await ctx.settled(60_000);
    await reviewShown(ctx);
    await ctx.sleep(500);
    await ctx.mark("scroll");
    let places = 0;
    let steps = 0;
    let distance = 0;
    let scrolling = 0;
    for (;;) {
      places++;
      const scroll = await ctx.execute<{ steps: number; distance: number; time: number }>(`
        const main = document.querySelector(".local-main");
        const start = performance.now();
        const top = main.scrollTop;
        let steps = 0;
        return new Promise((resolve) => {
          const tick = () => {
            const before = main.scrollTop;
            main.scrollTop += 400;
            steps++;
            if (main.scrollTop === before || steps >= ${SCROLL_STEPS})
              return resolve({ steps, distance: main.scrollTop - top, time: performance.now() - start });
            requestAnimationFrame(tick);
          };
          requestAnimationFrame(tick);
        });`);
      steps += scroll.steps;
      distance += scroll.distance;
      scrolling += scroll.time;
      if (scroll.steps >= SCROLL_STEPS)
        ctx.note(
          `A place was taller than ${SCROLL_STEPS} steps of 400 px, so its scroll stopped there.`,
        );
      if (places >= state.places) break;
      if (!(await nextPlace(ctx)))
        throw new Error(`Shift+J did not move past place ${places} of ${state.places}`);
      await ctx.sleep(200);
    }
    await ctx.mark("end");
    const metrics: Record<string, number> = {
      places_count: places,
      scroll_steps_count: steps,
      scroll_distance_px: distance,
      scroll_time: scrolling,
    };
    if (navigation.content !== undefined) metrics.open_content = navigation.content;
    if (navigation.settled !== undefined) metrics.open_settled = navigation.settled;
    return metrics;
  },
  after(snapshot) {
    const from = snapshot.marks.find((m) => m.name === "scroll")?.at ?? Infinity;
    const long = snapshot.frames.long.filter((f) => f.start >= from);
    return {
      ...navigationMetrics(
        "place",
        snapshot.navigations.filter((n) => n.start >= from),
      ),
      scroll_long_frames_count: long.length,
      scroll_worst_frame: long.reduce((worst, f) => Math.max(worst, f.duration), 0),
    };
  },
};

// A word from titles: Enter then applies "Titles containing retry", the
// filter's first suggestion for text that is not a field.
const QUERY = "retry";

const filter: Scenario = {
  name: "filter",
  headline: "keystroke_median",
  description:
    `Opens the Inbox's filter with / and types \`${QUERY}\` a key at a time, then Enter applies ` +
    "it as a title search. Rows are the Inbox's, on every page.",
  async prepare(ctx) {
    await ctx.open("");
    await inboxShown(ctx);
    const found = await ctx.execute<boolean>(
      `return !!document.querySelector("summary.filter-menu-button");`,
    );
    if (!found) throw new Skip("the Inbox shows no filter button");
  },
  async run(ctx) {
    await ctx.open("");
    await inboxShown(ctx);
    const before = await inboxTotal(ctx);
    await ctx.reset();
    await ctx.mark("open");
    await ctx.press("/");
    await ctx.waitFor(
      `return document.activeElement?.getAttribute("aria-label") === "Add filter" || document.activeElement?.closest(".filter-menu-panel");`,
      "/ to focus the filter",
      5_000,
    );
    await ctx.mark("type");
    for (const key of QUERY) await ctx.press(key);
    await ctx.press(KEY.enter);
    await ctx.sleep(1_000);
    await ctx.mark("end");
    const after = await inboxTotal(ctx);
    if (after === 0 || after === before)
      throw new Error(
        `the filter left ${after} of the Inbox's ${before} rows; it should narrow them to some`,
      );
    return { typed_count: QUERY.length, rows_before_count: before, rows_after_count: after };
  },
  async teardown(ctx) {
    // The daemon keeps a typed filter: every later run and scenario would
    // see a filtered Inbox.
    await ctx.press(KEY.escape);
    await ctx.clearFilters();
  },
  after(snapshot): Record<string, number> {
    const typing = snapshot.interactions
      .filter((i) => i.target.length === 1 && i.target !== "/")
      .map((i) => i.duration);
    if (!typing.length) return {};
    return { keystroke_median: percentile(typing, 50), keystroke_max: Math.max(...typing) };
  },
};

type SwitchState = { first: string; count: number };
const SWITCH_LIMIT = 6;

const switchInboxes: Scenario<SwitchState> = {
  name: "switch-inboxes",
  headline: "switch_content_median",
  description: `Steps through up to ${SWITCH_LIMIT} inboxes with Shift+J and back with Shift+K, waiting for each to settle.`,
  async prepare(ctx) {
    await ctx.open("");
    const all = await inboxes(ctx);
    if (all.length < 2)
      throw new Skip(`switching needs two inboxes; the sidebar lists ${all.length}`);
    const count = Math.min(all.length, SWITCH_LIMIT);
    ctx.note(
      `Switches through ${count} of ${all.length} inboxes: ${all
        .slice(0, count)
        .map((i) => `"${i.label}" (${i.count})`)
        .join(", ")}.`,
    );
    return { first: all[0]!.search, count };
  },
  async run(ctx, state) {
    await ctx.open(state.first);
    await inboxShown(ctx);
    await ctx.reset();
    const step = async (key: string) => {
      const before = await ctx.execute<string>("return location.search;");
      await ctx.press(key, [KEY.shift]);
      await ctx.waitFor(
        "return location.search !== arguments[0];",
        `Shift+${key} to switch the inbox`,
        5_000,
        [before],
      );
      await ctx.settled(30_000);
      await inboxShown(ctx);
    };
    for (let i = 1; i < state.count; i++) await step("J");
    for (let i = 1; i < state.count; i++) await step("K");
    return { switches_count: 2 * (state.count - 1) };
  },
  after(snapshot) {
    return navigationMetrics("switch", snapshot.navigations);
  },
};

type PanelState = { conversation: string | null; running: boolean };
const PANEL_SECONDS = 60;

const sessionPanel: Scenario<PanelState> = {
  name: "session-panel",
  headline: "open_duration",
  description:
    `Opens the session panel with Control+\` on the Inbox, on the latest console conversation ` +
    `(a running one if any), and leaves it open for ${PANEL_SECONDS} s.`,
  async prepare(ctx) {
    await ctx.open("");
    type Conversation = { id: string; state: string; updated_at?: string };
    const answer = await ctx.execute<{ status: number; list: Conversation[] }>(`
      const r = await fetch("api/console/conversations");
      const body = r.ok ? await r.json() : null;
      return { status: r.status, list: Array.isArray(body) ? body : (body?.conversations ?? []) };`);
    if (answer.status !== 200)
      throw new Skip(
        `the daemon's console answered ${answer.status}, so the session panel is unavailable`,
      );
    const list = answer.list.toSorted((a, b) =>
      String(b.updated_at ?? "").localeCompare(String(a.updated_at ?? "")),
    );
    const chosen = list.find((c) => c.state === "running") ?? list[0];
    const running = chosen?.state === "running";
    ctx.note(
      chosen
        ? running
          ? "The panel shows a running conversation, so its transcript streams."
          : `No conversation was running, so nothing streams: this measures the panel open on a conversation that is ${chosen.state}.`
        : "There are no console conversations, so nothing streams: this measures the panel open on a new one.",
    );
    return { conversation: chosen?.id ?? null, running };
  },
  async run(ctx, state) {
    await ctx.open("");
    // The panel opens on the conversation it last showed.
    await ctx.execute(
      `const key = "baum.session-panel.v1";
       const saved = JSON.parse(localStorage.getItem(key) ?? "{}") ?? {};
       localStorage.setItem(key, JSON.stringify({ ...saved, open: false, conversation: arguments[0] }));`,
      [state.conversation],
    );
    await ctx.open("");
    await inboxShown(ctx);
    await ctx.reset();
    await ctx.mark("open");
    await ctx.press("`", [KEY.control]);
    await ctx.waitFor(
      `return document.querySelector("section.session-panel") && !document.querySelector("section.session-panel .loading-status");`,
      "the session panel to open and read its conversation",
      30_000,
    );
    await ctx.sleep(1_000);
    await ctx.mark("idle");
    await ctx.sleep(PANEL_SECONDS * 1_000);
    await ctx.mark("end");
    return { streaming_count: state.running ? 1 : 0 };
  },
  async teardown(ctx) {
    await ctx.press("`", [KEY.control]);
  },
  after(snapshot) {
    const from = snapshot.marks.find((m) => m.name === "idle")?.at ?? 0;
    const to = snapshot.marks.find((m) => m.name === "end")?.at ?? Infinity;
    // The root region holds the others: counting them too would add nested
    // renders twice.
    const commits = snapshot.commits.filter(
      (c) => c.id === ROOT_REGION && c.at >= from && c.at <= to,
    );
    const long = snapshot.frames.long.filter((f) => f.start >= from && f.start <= to);
    return {
      open_duration:
        snapshot.interactions.find((i) => i.target.endsWith("`"))?.duration ?? Number.NaN,
      idle_commits_count: commits.length,
      idle_render_total: commits.reduce((sum, c) => sum + c.actual, 0),
      idle_long_frames_count: long.length,
      idle_requests_count: snapshot.requests.filter((r) => r.start >= from && r.start <= to).length,
    };
  },
};

export const scenarios: Scenario<never>[] = [
  coldStart,
  jk,
  review,
  filter,
  switchInboxes,
  sessionPanel,
  pagesScenario,
] as Scenario<never>[];

/** What the profiling build measures, kept apart from the browser so it can be
 *  tested: the installer in `perf.ts` feeds it events and frames. Times are
 *  milliseconds on the page's clock (`performance.now()`, which an event's
 *  `timeStamp` shares). The local app's README says how scenarios read it. */

export type Navigation = {
  /** The address's search part: the app's pages are all query parameters.
   *  A redirect (a replaced history entry) updates it in place. */
  search: string;
  start: number;
  /** The first frame after `start` with no loader on screen. */
  content?: number;
  /** As `content`, with no request in flight as well. */
  settled?: number;
  /** Left for another navigation before it settled: its timings stop where
   *  they were, since the next page's frames say nothing about this one. */
  superseded?: true;
};
export type Interaction = {
  type: "keydown" | "click";
  /** The key with its modifiers, or what was clicked. */
  target: string;
  /** When the input happened (the event's timestamp). */
  start: number;
  /** When the first listener saw it: `delay` is how long it waited. */
  delay: number;
  /** Until listeners on the window ran, after React's: the handlers and any
   *  render they flushed. Absent when propagation stopped earlier. */
  processing?: number;
  /** From the input to the frame after it: what the person waited. */
  duration: number;
};
export type Commit = {
  id: string;
  phase: "mount" | "update" | "nested-update";
  actual: number;
  base: number;
  at: number;
  /** The page shown when it committed. */
  page: string;
};
export type RenderTotals = {
  commits: number;
  mounts: number;
  actual: number;
  max: number;
  /** The last commit's base duration: the cost of rendering it all afresh. */
  base: number;
};
export type RequestRecord = {
  method: string;
  path: string;
  status: number;
  start: number;
  /** Until the response's headers arrived. */
  duration: number;
  /** The Content-Length, when the daemon sent one. */
  bytes: number | null;
};
export type Snapshot = {
  version: 1;
  userAgent: string;
  since: number;
  now: number;
  navigations: Navigation[];
  interactions: Interaction[];
  frames: { count: number; buckets: Record<FrameBucket, number>; long: LongFrame[] };
  renders: Record<string, RenderTotals>;
  commits: Commit[];
  requests: RequestRecord[];
  marks: { name: string; at: number }[];
  dom: number;
  heap: number | null;
  /** How many records each list let go, oldest first, to stay bounded. */
  dropped: Record<string, number>;
};
export type LongFrame = { start: number; duration: number };
type FrameBucket = "≤17" | "≤33" | "≤50" | "≤100" | ">100";

export type CollectorEnv = {
  now(): number;
  /** The page shown now, as `inbox` or `pulls`. */
  page(): string;
  userAgent: string;
  dom(): number;
  heap(): number | null;
};

/** Frames apart by more than this are a visible stall. */
export const LONG_FRAME = 50;
/** Each list keeps this many records, enough for a long scenario. */
export const LIMIT = 5000;

export function createCollector(env: CollectorEnv) {
  let since = env.now();
  let navigations: Navigation[] = [];
  let interactions: Interaction[] = [];
  let commits: Commit[] = [];
  let requests: RequestRecord[] = [];
  let marks: { name: string; at: number }[] = [];
  let renders: Record<string, RenderTotals> = {};
  let frames = { count: 0, buckets: emptyBuckets(), long: [] as LongFrame[] };
  let dropped: Record<string, number> = {};
  let lastFrame: number | undefined;
  let loaders = 0;
  let inFlight = 0;
  let lastCommit = -Infinity;

  function keep<T>(name: string, list: T[], item: T) {
    list.push(item);
    if (list.length > LIMIT) {
      list.shift();
      dropped[name] = (dropped[name] ?? 0) + 1;
    }
  }

  return {
    /** The address changed to `search` (or the app started on it). A
     *  `redirect` replaced the history entry: while the current navigation
     *  is still on its way it continues there, rather than starting anew. */
    navigation(search: string, redirect = false) {
      const current = navigations.at(-1);
      if (current && current.settled === undefined) {
        if (redirect) {
          current.search = search;
          return;
        }
        current.superseded = true;
      }
      keep("navigations", navigations, { search, start: env.now() });
    },
    /** A loader is on screen until the returned function runs. */
    loading() {
      loaders++;
      let done = false;
      return () => {
        if (done) return;
        done = true;
        loaders--;
      };
    },
    /** A request started; the returned function records its answer. */
    request(method: string, path: string) {
      const start = env.now();
      inFlight++;
      let done = false;
      return (status: number, bytes: number | null) => {
        if (done) return;
        done = true;
        inFlight--;
        keep("requests", requests, {
          method,
          path,
          status,
          start,
          duration: env.now() - start,
          bytes,
        });
      };
    },
    interaction(record: Interaction) {
      keep("interactions", interactions, record);
    },
    render(id: string, phase: Commit["phase"], actual: number, base: number) {
      const at = env.now();
      lastCommit = at;
      keep("commits", commits, { id, phase, actual, base, at, page: env.page() });
      const totals = (renders[id] ??= { commits: 0, mounts: 0, actual: 0, max: 0, base: 0 });
      totals.commits++;
      if (phase === "mount") totals.mounts++;
      totals.actual += actual;
      totals.max = Math.max(totals.max, actual);
      totals.base = base;
    },
    /** A scenario's own marker, to cut the timeline into steps. */
    mark(name: string) {
      keep("marks", marks, { name, at: env.now() });
    },
    /** Called on every animation frame with its timestamp. */
    frame(at: number) {
      if (lastFrame !== undefined) {
        const gap = at - lastFrame;
        frames.count++;
        frames.buckets[bucket(gap)]++;
        if (gap > LONG_FRAME) keep("frames", frames.long, { start: lastFrame, duration: gap });
      }
      lastFrame = at;
      // The current navigation waits for React to commit after it, then for
      // a frame with no loader showing.
      const navigation = navigations.at(-1);
      if (
        navigation &&
        navigation.settled === undefined &&
        at > navigation.start &&
        lastCommit >= navigation.start &&
        loaders === 0
      ) {
        navigation.content ??= at - navigation.start;
        if (inFlight === 0) navigation.settled = at - navigation.start;
      }
    },
    /** What a scenario waits on, without copying every record as
     *  `snapshot` does: the latest navigation, and how many key presses and
     *  clicks have been recorded since the page opened or the last reset. */
    progress(): { navigation: Navigation | undefined; interactions: number } {
      const navigation = navigations.at(-1);
      return {
        navigation: navigation && { ...navigation },
        interactions: interactions.length + (dropped.interactions ?? 0),
      };
    },
    snapshot(): Snapshot {
      return structuredClone({
        version: 1 as const,
        userAgent: env.userAgent,
        since,
        now: env.now(),
        navigations,
        interactions,
        frames,
        renders,
        commits,
        requests,
        marks,
        dom: env.dom(),
        heap: env.heap(),
        dropped,
      });
    },
    /** Forgets everything recorded; loaders and requests in flight still count. */
    reset() {
      since = env.now();
      navigations = [];
      interactions = [];
      commits = [];
      requests = [];
      marks = [];
      renders = {};
      frames = { count: 0, buckets: emptyBuckets(), long: [] };
      dropped = {};
      lastFrame = undefined;
    },
  };
}
export type Collector = ReturnType<typeof createCollector>;

function emptyBuckets(): Record<FrameBucket, number> {
  return { "≤17": 0, "≤33": 0, "≤50": 0, "≤100": 0, ">100": 0 };
}
function bucket(gap: number): FrameBucket {
  if (gap <= 17) return "≤17";
  if (gap <= 33) return "≤33";
  if (gap <= 50) return "≤50";
  if (gap <= 100) return "≤100";
  return ">100";
}

/** A request's path without the origin or the secret `/s/<namespace>` prefix,
 *  which must not end up in a profile someone shares. */
export function requestPath(url: string, base: string) {
  const parsed = new URL(url, base);
  const api = parsed.pathname.indexOf("/api/");
  const path =
    api >= 0 ? parsed.pathname.slice(api + 1) : parsed.pathname.replace(/^\/s\/[^/]+\/?/, "/");
  return path + parsed.search;
}

/** A key press as `j` or `Meta+k`. */
export function keyLabel(event: Pick<KeyboardEvent, "key" | "metaKey" | "ctrlKey" | "altKey">) {
  const modifiers = [
    event.ctrlKey ? "Ctrl" : "",
    event.altKey ? "Alt" : "",
    event.metaKey ? "Meta" : "",
  ].filter(Boolean);
  return [...modifiers, event.key].join("+");
}

/** What was clicked, briefly: the nearest control's label or text. */
export function targetLabel(target: EventTarget | null) {
  if (!(target instanceof Element)) return "?";
  const control =
    target.closest("button, a, [role], input, select, textarea, summary, label") ?? target;
  const name = (
    control.getAttribute("aria-label") ??
    control.getAttribute("title") ??
    control.textContent ??
    ""
  )
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, 40);
  return name ? `${control.localName} "${name}"` : control.localName;
}

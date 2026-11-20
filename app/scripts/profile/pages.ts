// The `pages` scenario: renders every page of the app and ranks them
// (pages-summary.ts), so the page to optimise next is the top of one table.
// Each page is measured by in-app navigation (its key, or a click on its
// link, as a person moves) and by a direct load of its address. The pages
// come from the app's sidebar at run time, checked against `PAGES`
// (src/pages.ts): a page the scenario does not know fails the run, so a new
// page is not left out unseen.
import { readFileSync } from "node:fs";
import type { Snapshot } from "../../src/perf-collector.ts";
import {
  attribute,
  navigationPages,
  navMark,
  pageMetrics,
  type Attributed,
} from "./pages-summary.ts";
import type { Context, Scenario } from "./scenarios.ts";
import { KEY } from "./webdriver.ts";

/** What proves a page rendered its content, as selectors. */
type Check = {
  /** Content the page itself draws (a row, a field, a message): never a
   *  wrapper that is there while a loader fills it. */
  content: string;
  /** The page's own words for having nothing to show on this data. */
  empty?: string;
  /** Loaders that do not hold the page back: a card that waits on GitHub,
   *  which an offline dataset never answers promptly. The page then may
   *  never settle, which the table says (`n/N` runs). */
  ignoreLoader?: string;
};

const LOADER = ".local-main .loading-status";
/** Panels that say the page could not show, whatever they are called. */
const FAILED = [
  ".local-main [role=alert]",
  ".local-main .local-empty",
  ".local-main p.font-medium",
  ".local-main h2",
].join(", ");
const FAILED_TEXT = /not available|not found|couldn't load|could not load|failed to/i;

/** The sidebar's pages by label, and what each one shows. */
const KNOWN: Record<string, Check> = {
  Inbox: {
    content: ".upnext-list [data-row]",
    empty: ".upnext-list:not(:has([data-row]))",
  },
  Sessions: { content: "#sessions-running li, #sessions-ended li", empty: ".local-sessions-empty" },
  Timeline: {
    content: ".timeline-log [data-row-id]",
    empty: ".timeline-log:not(:has([data-row-id]))",
  },
  "Pull requests": { content: ".local-main [data-row-id]", empty: ".local-main .local-empty" },
  Analytics: {
    content: 'section[aria-labelledby="analytics-activity"] .analytics-plot',
    empty: 'section[aria-labelledby="analytics-activity"] .analytics-empty',
    // The Delivery card reads GitHub, and sits on "Loading from GitHub…"
    // for minutes on an offline instance.
    ignoreLoader: 'section[aria-labelledby="analytics-delivery"]',
  },
  Repositories: { content: ".local-main [data-row-id]", empty: ".repository-empty" },
  Jobs: { content: ".local-main [data-row-id]", empty: ".local-main .local-empty" },
  Settings: { content: ".settings-content .settings-row-label" },
};

/** Sidebar links that are not pages: the session panel's button. */
const NOT_PAGES = ["Session"];

/** The sub-views the sidebar does not list. */
const REVIEW: Check = { content: "[data-review-page] [data-review-block]" };
const TRANSCRIPT: Check = { content: ".local-session-page [data-message]" };
const NEW_INBOX: Check = { content: ".inbox-page .inbox-template" };
const EDIT_INBOX: Check = { content: ".inbox-edit .inbox-form input" };

type Step = {
  label: string;
  /** The page's address, as a direct load opens it. */
  search: string;
  check: Check;
  /** How a person gets there from another page. */
  via: { key: number } | { click: true };
  /** Why this page must not be empty on this data, when it must not. */
  expect?: string;
};

type State = {
  steps: Step[];
  /** Runs so far: the order of direct loads and the tour alternates. */
  turn: number;
};

/** Does `location.search` hold the page `want` names? Every parameter of
 *  `want`, and for a top-level page (`arguments[1]`) its page parameter
 *  too; the app may add others (an inbox redirect). */
const AT = `
  const want = new URLSearchParams(arguments[0]);
  const have = new URLSearchParams(location.search);
  if (arguments[1] && (want.get("page") ?? "inbox") !== (have.get("page") ?? "inbox")) return false;
  return [...want].every(([k, v]) => have.get(k) === v);`;

/** Waits until the page shows `check`'s content, or its own empty state, and
 *  says which. A failure panel or a loader that stays fails the step; they
 *  are checked before the content, so a loader around content never passes. */
async function shown(ctx: Context, step: Step): Promise<"content" | "empty"> {
  const { content, empty } = step.check;
  const found = await ctx
    .waitFor<string>(
      `const [content, empty, failed, loader, pattern, ignore] = arguments;
       const bad = [...document.querySelectorAll(failed)].find((el) => new RegExp(pattern, "i").test(el.textContent ?? ""));
       if (bad) return "failed: " + (bad.textContent ?? "").replace(/\\s+/g, " ").slice(0, 160);
       if ([...document.querySelectorAll(loader)].some((el) => !ignore || !el.closest(ignore))) return null;
       if (document.querySelector(content)) return "content";
       if (empty && document.querySelector(empty)) return "empty";
       return null;`,
      `${step.label} to show its content`,
      30_000,
      [content, empty ?? "", FAILED, LOADER, FAILED_TEXT.source, step.check.ignoreLoader ?? ""],
    )
    .catch(async (error: Error) => {
      const seen = await ctx.execute<string>(
        `const main = document.querySelector(".local-main");
         const loader = [...document.querySelectorAll(arguments[0])].find((el) => !arguments[1] || !el.closest(arguments[1]));
         return location.search + (loader ? " [loader on screen: " + loader.textContent.trim().slice(0, 60) + "]" : " [no loader]") + ": " + (main ? main.innerText.replace(/\\s+/g, " ").slice(0, 160) : "no main");`,
        [LOADER, step.check.ignoreLoader ?? ""],
      );
      throw new Error(`${error.message}; the page shows: ${seen}`);
    });
  if (found.startsWith("failed"))
    throw new Error(`${step.label} shows a failure instead of its content (${found})`);
  return found as "content" | "empty";
}

/** As `shown`, and fails an empty page that the data says must have content. */
async function arrived(ctx: Context, step: Step) {
  const result = await shown(ctx, step);
  if (result === "empty" && step.expect)
    throw new Error(
      `${step.label} showed nothing, but ${step.expect}: the data or the page is broken`,
    );
  return result === "empty";
}

const domSize = (ctx: Context) =>
  ctx.execute<number>(`return document.getElementsByTagName("*").length;`);

/** How a step was reached, for the summary's notes. */
type How = { sidebar: Set<string>; history: Set<string> };

/** Moves to `step`'s page as a person does: its key if keys work (`keys`),
 *  else a click on its sidebar link, and for a sub-view a click on its link
 *  on the page, else the history entry the app's own links make. The start
 *  mark is set immediately before the input. Fails when the address does not
 *  become the page's. */
async function goTo(ctx: Context, step: Step, keys: boolean, how: How) {
  if ("key" in step.via && keys) {
    await ctx.mark(navMark(step.label, "start"));
    await ctx.press(String(step.via.key), [KEY.control]);
  } else if ("key" in step.via) {
    how.sidebar.add(step.label);
    await ctx.mark(navMark(step.label, "start"));
    const clicked = await ctx.execute<boolean>(
      `const label = arguments[0];
       const link = [...document.querySelectorAll("nav a.local-rail-link")].find(
         (a) => a.querySelector(".local-rail-label")?.textContent.trim() === label);
       const target = link ?? (label === "Inbox" ? document.querySelector("a.local-rail-mark") : null);
       if (!target) return false;
       target.click();
       return true;`,
      [step.label],
    );
    if (!clicked) throw new Error(`no sidebar link opens ${step.label}`);
  } else {
    await ctx.mark(navMark(step.label, "start"));
    const clicked = await ctx.execute<boolean>(
      `const want = new URLSearchParams(arguments[0]);
       const link = [...document.querySelectorAll("a[href]")].find((a) => {
         const have = new URL(a.href).searchParams;
         return [...want].every(([k, v]) => have.get(k) === v);
       });
       if (!link) return false;
       link.click();
       return true;`,
      [step.search],
    );
    if (!clicked) {
      how.history.add(step.label);
      await ctx.execute(
        `history.pushState(null, "", arguments[0]); dispatchEvent(new PopStateEvent("popstate"));`,
        [step.search],
      );
    }
  }
  await ctx
    .waitFor(AT, `${step.label} to open`, 5_000, [step.search, "key" in step.via])
    .catch(async (error: Error) => {
      const at = await ctx.execute<string>("return location.search;");
      throw new Error(`${error.message}: the page is ${at}, not ${step.search}`);
    });
  await ctx.settled(30_000);
}

/** Whether Ctrl+digit opens a page in this window. WebDriver's synthesized
 *  key events may leave `code`, which the app reads, empty. Probed once a
 *  run, outside the marks. */
async function keysWork(ctx: Context, probe: Step) {
  await ctx.open("");
  await ctx.press(String("key" in probe.via ? probe.via.key : 2), [KEY.control]);
  return ctx
    .waitFor(AT, "the key's page", 2_000, [probe.search, true])
    .then(() => true)
    .catch(() => false);
}

type Entry = {
  clone_id?: string;
  latest?: unknown;
  pull_request: { repository: string; number: number; additions?: number; deletions?: number };
};

const pagesScenario: Scenario<State> = {
  name: "pages",
  description:
    "Renders every page of the app, and its main sub-views, two ways: by in-app navigation " +
    "(the sidebar's Ctrl+1 to Ctrl+8, else a click on its link; a click on a row's link for " +
    "sub-views) and by a direct load of its address. The pages are read from the sidebar and " +
    "checked against the app's page list, so a page the scenario does not know fails the run. " +
    "The table ranks them slowest content first; a page the data leaves empty is reported as " +
    "empty, and one the data should fill fails the run. The order of the two ways alternates " +
    "from run to run.",
  async prepare(ctx) {
    await ctx.open("");
    // The app's own list of pages, from its source (importing it would pull
    // React and the icons in).
    const source = readFileSync(new URL("../../src/pages.ts", import.meta.url), "utf8");
    const listed = [...source.matchAll(/\blabel: "([^"]+)"/g)].map((m) => m[1]!);
    if (listed.length === 0) throw new Error("src/pages.ts lists no page labels; update pages.ts");
    // The sidebar's links with their keys and addresses.
    const rail = await ctx.execute<{ label: string; key: string; search: string }[]>(`
      return [...document.querySelectorAll("nav[aria-label=Main] .local-rail-link, nav[aria-label=More] .local-rail-link")]
        .map((el) => ({
          label: (el.querySelector(".local-rail-label")?.textContent ?? "").trim(),
          key: (el.querySelector(".local-rail-key")?.textContent ?? "").trim(),
          search: el.tagName === "A" ? new URL(el.href).search : "",
        }));`);
    const withKey = rail.filter((p) => /\d$/.test(p.key));
    const strays = rail.filter((p) => !/\d$/.test(p.key) && !NOT_PAGES.includes(p.label));
    if (strays.length)
      throw new Error(
        `the sidebar has links with no page key the scenario does not know: ${strays.map((p) => p.label || "(unnamed)").join(", ")}`,
      );
    if (withKey.map((p) => p.label).join("|") !== listed.join("|"))
      throw new Error(
        `the sidebar's pages (${withKey.map((p) => p.label).join(", ")}) are not the app's PAGES ` +
          `(${listed.join(", ")}), in this order`,
      );
    withKey.forEach((p, i) => {
      if (Number(p.key.slice(-1)) !== i + 1)
        throw new Error(`${p.label}'s key ${p.key} is not number ${i + 1}`);
    });
    const unknown = withKey.filter((p) => !KNOWN[p.label]).map((p) => p.label);
    if (unknown.length)
      throw new Error(
        `the app has pages the pages scenario does not know: ${unknown.join(", ")}; ` +
          "add them to KNOWN in oss/app/scripts/profile/pages.ts",
      );
    const pages: Step[] = withKey.map((p, i) => ({
      label: p.label,
      search: p.search,
      check: KNOWN[p.label]!,
      via: { key: i + 1 },
    }));
    const page = (label: string) => pages.find((p) => p.label === label)!;

    const snapshot = await ctx.execute<{ entries?: Entry[] }>(
      `return fetch("api/index?history=1&detail=summary").then((r) => r.json());`,
    );
    const entries = snapshot.entries ?? [];
    // What the data must show, from the daemon's own reads.
    if (entries.length) {
      const why = `the daemon lists ${entries.length} pull requests`;
      page("Inbox").expect = why;
      page("Pull requests").expect = why;
      page("Repositories").expect = `${why}, so repositories exist`;
    }

    ctx.note(
      'Analytics: its Delivery card reads GitHub and can stay on "Loading from GitHub…" on an offline ' +
        "instance; the page is then ready without it, but the collector (which waits for every loader) " +
        "records no content or settled time, and the table says in how many runs it did.",
    );
    const gaps: string[] = [];
    // A pull request's review: the largest reviewed one the Pull requests
    // page links to (else the largest overall).
    const reviewed = entries.filter((e) => e.latest);
    const lines = (e: Entry) => (e.pull_request.additions ?? 0) + (e.pull_request.deletions ?? 0);
    const where = (e: Entry) =>
      e.clone_id
        ? `?clone=${encodeURIComponent(e.clone_id)}&pr=${e.pull_request.number}`
        : `?repository=${encodeURIComponent(e.pull_request.repository)}&pr=${e.pull_request.number}`;
    let review: Step | undefined;
    if (reviewed.length) {
      await ctx.open(page("Pull requests").search);
      await shown(ctx, page("Pull requests"));
      const hrefs = await ctx.execute<string[]>(
        `return [...document.querySelectorAll("a[href]")].map((a) => new URL(a.href).search).filter((s) => /[?&]pr=/.test(s));`,
      );
      const linked = (e: Entry) => {
        const want = new URLSearchParams(where(e));
        return hrefs.some((h) => {
          const have = new URLSearchParams(h);
          return [...want].every(([k, v]) => have.get(k) === v);
        });
      };
      const pool = reviewed.some(linked) ? reviewed.filter(linked) : reviewed;
      const largest = pool.reduce((a, b) => (lines(b) > lines(a) ? b : a));
      const pr = largest.pull_request;
      ctx.note(
        `Pull request review: ${pr.repository}#${pr.number} (+${pr.additions ?? "?"} −${pr.deletions ?? "?"}), ` +
          `the largest of ${pool.length} reviewed pull requests ${pool === reviewed ? "in the data" : "the Pull requests page links to"}.`,
      );
      review = {
        label: "PR review",
        search: where(largest),
        check: REVIEW,
        via: { click: true },
        expect: `the data has ${reviewed.length} reviewed pull requests`,
      };
    } else gaps.push("PR review (no pull request has a review)");

    // A session's transcript: the largest one.
    let transcript: Step | undefined;
    const sessions = await ctx.execute<{ key: string; state: string; bytes: number }[]>(`
      const body = await (await fetch("api/live-sessions")).json();
      const list = Array.isArray(body?.sessions) ? body.sessions : [];
      const sized = [];
      for (const s of list) {
        const key = s.agent + ":" + s.session_id;
        const r = await fetch("api/live-sessions/" + encodeURIComponent(s.agent) + "/" + encodeURIComponent(s.session_id) + "/transcript");
        sized.push({ key, state: s.state, bytes: r.ok ? (await r.arrayBuffer()).byteLength : 0 });
      }
      return sized;`);
    const biggest = sessions.toSorted((a, b) => b.bytes - a.bytes)[0];
    if (biggest && biggest.bytes > 0) {
      ctx.note(
        `Session transcript: ${biggest.key}, the largest of ${sessions.length} sessions (${Math.round(biggest.bytes / 1024)} KiB read).`,
      );
      transcript = {
        label: "Session transcript",
        search: `?page=sessions&session=${encodeURIComponent(biggest.key)}`,
        check: TRANSCRIPT,
        via: { click: true },
        expect: `the daemon lists ${sessions.length} sessions`,
      };
    } else gaps.push("Session transcript (no session has a transcript)");
    // The Sessions page lists running and waiting sessions; "Ended recently"
    // starts folded (sessions.tsx), so a dataset whose sessions all ended
    // shows none by default.
    const live = sessions.filter((s) => s.state !== "ended");
    if (live.length) page("Sessions").expect = `${live.length} sessions are running or waiting`;
    else if (sessions.length)
      ctx.note(
        `Sessions: all ${sessions.length} sessions in the data ended, and the page folds "Ended recently" ` +
          "by default, so it lists none; measuring it needs a running session.",
      );

    // Inbox editing: the first saved inbox. Opened only, never saved.
    const inbox = await ctx.execute<string | null>(`
      const a = [...document.querySelectorAll("nav[aria-label=Main] a.local-rail-sub")]
        .find((el) => /[?&]inbox=/.test(el.getAttribute("href") ?? ""));
      return a ? new URL(a.href).searchParams.get("inbox") : null;`);
    const edit: Step | undefined = inbox
      ? {
          label: "Edit inbox",
          search: `?page=inbox-edit&inbox=${encodeURIComponent(inbox)}`,
          check: EDIT_INBOX,
          via: { click: true },
          expect: "the sidebar lists the inbox",
        }
      : undefined;
    if (!edit) gaps.push("Edit inbox (the sidebar lists no inbox)");
    if (gaps.length) ctx.note(`Not measured: ${gaps.join("; ")}.`);

    // The tour's order: a sub-view follows the page it is opened from; the
    // Inbox comes last, reached from the page before it.
    const steps: Step[] = [
      page("Sessions"),
      ...(transcript ? [transcript] : []),
      page("Timeline"),
      page("Pull requests"),
      ...(review ? [review] : []),
      page("Analytics"),
      page("Repositories"),
      page("Jobs"),
      { label: "New inbox", search: "?page=inbox-new", check: NEW_INBOX, via: { click: true } },
      ...(edit ? [edit] : []),
      page("Settings"),
      page("Inbox"),
    ];

    // The routes the app serves that no step opens.
    const app = readFileSync(new URL("../../src/app.tsx", import.meta.url), "utf8");
    const routes = /const knownPages = \[([^\]]*)\]/.exec(app)?.[1];
    if (routes) {
      const covered = new Set(
        steps.map((s) => new URLSearchParams(s.search).get("page") ?? "inbox"),
      );
      const open = [...routes.matchAll(/"([^"]+)"/g)]
        .map((m) => m[1]!)
        .filter((r) => !covered.has(r));
      if (open.length)
        ctx.note(
          `Routes not measured: ${open.join(", ")} (the app's knownPages); also a group's page and the ` +
            "other sub-views of Repositories, Jobs and Settings.",
        );
    }
    return { steps, turn: 0 };
  },
  async run(ctx, state) {
    const metrics: Record<string, number> = {};
    const { steps } = state;
    // Direct loads: each is a fresh document, so its snapshot is that page's
    // alone, and its times are the document's own (bundle included).
    const direct = async () => {
      for (const step of steps) {
        await ctx.open(step.search);
        const empty = await arrived(ctx, step);
        const snapshot = await ctx.execute<Snapshot>("return window.__baumPerf.snapshot();");
        const page = attribute(snapshot, 0, Infinity, true);
        needTimes(step, "direct", page);
        if (empty) ctx.note(`Empty on this data: ${step.label}.`);
        Object.assign(
          metrics,
          pageMetrics(step.label, "direct", page, {
            dom_count: await domSize(ctx),
            empty_count: empty ? 1 : 0,
          }),
        );
      }
    };
    // The tour by navigation: its windows are the marks around each page.
    const tour = async () => {
      const keys = await keysWork(ctx, steps.find((s) => s.label === "Sessions") ?? steps[0]!);
      await ctx.open("");
      await shown(ctx, steps.at(-1)!);
      await ctx.reset();
      const how: How = { sidebar: new Set(), history: new Set() };
      const extra: Record<string, Record<string, number>> = {};
      for (const step of steps) {
        await goTo(ctx, step, keys, how);
        const empty = await arrived(ctx, step);
        // As `open` leaves it: what the page does after it settled ends.
        await ctx.sleep(500);
        await ctx.mark(navMark(step.label, "end"));
        extra[step.label] = { dom_count: await domSize(ctx), empty_count: empty ? 1 : 0 };
        if (empty) ctx.note(`Empty on this data: ${step.label}.`);
      }
      const snapshot = await ctx.execute<Snapshot>("return window.__baumPerf.snapshot();");
      const visited = navigationPages(snapshot);
      for (const step of steps) {
        const found = visited.find((v) => v.label === step.label);
        if (!found) throw new Error(`${step.label} has no window in the tour: a mark is missing`);
        needTimes(step, "nav", found.page);
        Object.assign(metrics, pageMetrics(step.label, "nav", found.page, extra[step.label]!));
      }
      if (!keys)
        ctx.note(
          "Ctrl+digit did not open a page (the app reads the key's place, `code`, which WebDriver's " +
            "synthesized key events leave empty), so navigation clicked the sidebar's link; the keys " +
            "themselves are not measured.",
        );
      if (how.history.size)
        ctx.note(
          `${[...how.history].join(", ")}: no link to it on the page before, so navigation used the history entry the app's own links make.`,
        );
    };
    // Which goes first alternates, so neither always has the other's warmth
    // (WebKit's cache, the daemon's).
    if (state.turn++ % 2 === 0) {
      await direct();
      await tour();
    } else {
      await tour();
      await direct();
    }
    // The runner's own sections come from the last snapshot: the same load
    // of the Inbox every run, whatever the order.
    await ctx.open("");
    return metrics;
  },
  async teardown(ctx) {
    // Typed filters are the daemon's: the Timeline's opening filter may stay.
    await ctx.clearFilters();
  },
};

/** A page without its times (never showed content, or no navigation in its
 *  window) is a broken journey, not a fast page; so is one that never
 *  settled, unless the page is known to wait on GitHub (`ignoreLoader`),
 *  when the table reports in how many runs it settled. */
function needTimes(step: Step, way: string, page: Attributed) {
  if (step.check.ignoreLoader) return;
  if (page.content === undefined)
    throw new Error(
      `${step.label} (${way}) recorded no content time: no navigation or no content frame`,
    );
  if (page.settled === undefined)
    throw new Error(`${step.label} (${way}) never settled: a request or loader stayed on`);
}

export { pagesScenario };

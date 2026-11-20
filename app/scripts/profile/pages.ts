// The `pages` scenario: renders every page of the app and ranks them
// (pages-summary.ts), so the page to optimise next is the top of one table.
// Each page is measured by in-app navigation (its key, or a click on its
// link, as a person moves) and by a direct load of its address. The pages
// come from the app's sidebar at run time: a page the scenario does not know
// fails the run, so a new page is not left out unseen.
import type { Snapshot } from "../../src/perf-collector.ts";
import { attribute, navigationPages, navMark, pageMetrics } from "./pages-summary.ts";
import type { Context, Scenario } from "./scenarios.ts";
import { KEY } from "./webdriver.ts";

/** What proves a page rendered its content, as selectors. */
type Check = {
  /** Real content of the page. */
  content: string;
  /** The page's own words for having nothing to show on this data. */
  empty?: string;
};

const LOADER = ".local-main .loading-status";
const FAILED = ".local-main .local-empty[role=alert]";

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
  "Pull requests": { content: "[data-row-id]", empty: ".local-main .local-empty" },
  Analytics: {
    content: "#analytics-activity:not(:has(.analytics-empty))",
    empty: "#analytics-activity .analytics-empty",
  },
  Repositories: { content: "[data-row-id]", empty: ".repository-empty" },
  Jobs: { content: "[data-row-id]", empty: ".local-main .local-empty" },
  Settings: { content: ".settings-layout" },
};

/** The sub-views the sidebar does not list. */
const REVIEW: Check = { content: "[data-review-page]" };
const TRANSCRIPT: Check = { content: ".local-session-page" };
const NEW_INBOX: Check = { content: ".inbox-page" };
const EDIT_INBOX: Check = { content: ".inbox-edit" };

type Step = {
  label: string;
  /** The page's address, as a direct load opens it. */
  search: string;
  check: Check;
  /** How a person gets there from another page. */
  via: { key: number } | { click: true };
};

type State = { steps: Step[] };

/** Does `location.search` hold the page `want` names? Its page, and every
 *  parameter of `want`; the app may add others (an inbox redirect). */
const AT = `
  const want = new URLSearchParams(arguments[0]);
  const have = new URLSearchParams(location.search);
  // A top-level page is told by its page parameter; a sub-view opened from
  // another page keeps that page's (a review opens under Pull requests).
  if (arguments[1] && (want.get("page") ?? "inbox") !== (have.get("page") ?? "inbox")) return false;
  return [...want].every(([k, v]) => have.get(k) === v);`;

/** Waits until the page shows `check`'s content, or its own empty state;
 *  fails for anything else (a loader that stays, an error). Resolves with
 *  whether it is empty. */
async function arrived(ctx: Context, step: Step): Promise<boolean> {
  const { content, empty } = step.check;
  const found = await ctx
    .waitFor<string>(
      `const [content, empty] = arguments;
       if (document.querySelector(content)) return "content";
       if (document.querySelector(${JSON.stringify(FAILED)})) return "failed";
       if (document.querySelector(${JSON.stringify(LOADER)})) return null;
       if (empty && document.querySelector(empty)) return "empty";
       return null;`,
      `${step.label} to show its content`,
      30_000,
      [content, empty ?? ""],
    )
    .catch(async (error: Error) => {
      const seen = await ctx.execute<string>(
        `const main = document.querySelector(".local-main");
         return location.search + ": " + (main ? main.innerText.replace(/\\s+/g, " ").slice(0, 160) : "no main");`,
      );
      throw new Error(`${error.message}; the page shows: ${seen}`);
    });
  if (found === "failed") {
    const text = await ctx.execute<string>(
      `return document.querySelector(arguments[0])?.textContent ?? "";`,
      [FAILED],
    );
    throw new Error(`${step.label} shows an error instead of its content: ${text.slice(0, 160)}`);
  }
  return found === "empty";
}

const domSize = (ctx: Context) =>
  ctx.execute<number>(`return document.getElementsByTagName("*").length;`);

/** Moves to `step`'s page as a person does: its key, or a click on its link
 *  where the current page has one (else the history entry the app's own links
 *  make). Fails when the address does not become the page's. */
async function goTo(ctx: Context, step: Step, how: { sidebar: Set<string>; history: Set<string> }) {
  if ("key" in step.via) {
    await ctx.press(String(step.via.key), [KEY.control]);
    const moved = await ctx
      .waitFor(AT, "the key's page", 2_000, [step.search, true])
      .catch(() => null);
    if (!moved) {
      // Ctrl+digit reads the key's place (`code`), which WebDriver's synthesized
      // events may leave empty: click the sidebar's link instead, and say so.
      how.sidebar.add(step.label);
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
      if (!clicked) throw new Error(`neither ${step.label}'s key nor a link to it opened it`);
    }
  } else {
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

type Entry = {
  clone_id?: string;
  latest?: unknown;
  pull_request: { repository: string; number: number; additions?: number; deletions?: number };
};

const pagesScenario: Scenario<State> = {
  name: "pages",
  description:
    "Renders every page of the app, and its main sub-views, two ways: by in-app navigation " +
    "(the sidebar's Ctrl+1 to Ctrl+8, or a click on a row's link) and by a direct load of its " +
    "address. Pages are read from the sidebar, so a page the scenario does not know fails the " +
    "run. The table ranks them slowest content first; a page the data leaves empty is reported " +
    "as empty. The summary's other sections are the navigation tour's alone.",
  async prepare(ctx) {
    await ctx.open("");
    // The sidebar's pages with their keys and addresses.
    const rail = await ctx.execute<{ label: string; key: string; search: string }[]>(`
      return [...document.querySelectorAll("nav[aria-label=Main] .local-rail-link, nav[aria-label=More] .local-rail-link")]
        .map((el) => ({
          label: (el.querySelector(".local-rail-label")?.textContent ?? "").trim(),
          key: (el.querySelector(".local-rail-key")?.textContent ?? "").trim(),
          search: el.tagName === "A" ? new URL(el.href).search : "",
        }))
        .filter((p) => p.label && /\\d$/.test(p.key));`);
    const unknown = rail.filter((p) => !KNOWN[p.label]).map((p) => p.label);
    if (unknown.length)
      throw new Error(
        `the sidebar has pages the pages scenario does not know: ${unknown.join(", ")}; ` +
          "add them to KNOWN in oss/app/scripts/profile/pages.ts",
      );
    const missing = Object.keys(KNOWN).filter((label) => !rail.some((p) => p.label === label));
    if (missing.length)
      throw new Error(
        `the sidebar does not list ${missing.join(", ")}, which the scenario expects`,
      );
    const pages: Step[] = rail.map((p) => {
      const digit = Number(p.key.slice(-1));
      if (!(digit >= 1)) throw new Error(`${p.label}'s key ${p.key} does not end in a digit`);
      return { label: p.label, search: p.search, check: KNOWN[p.label]!, via: { key: digit } };
    });
    const page = (label: string) => pages.find((p) => p.label === label)!;

    const gaps: string[] = [];
    // A pull request's review: the largest reviewed one the Pull requests
    // page links to (else the largest overall).
    const snapshot = await ctx.execute<{ entries?: Entry[] }>(
      `return fetch("api/index?history=1&detail=summary").then((r) => r.json());`,
    );
    const reviewed = (snapshot.entries ?? []).filter((e) => e.latest);
    const lines = (e: Entry) => (e.pull_request.additions ?? 0) + (e.pull_request.deletions ?? 0);
    const where = (e: Entry) =>
      e.clone_id
        ? `?clone=${encodeURIComponent(e.clone_id)}&pr=${e.pull_request.number}`
        : `?repository=${encodeURIComponent(e.pull_request.repository)}&pr=${e.pull_request.number}`;
    let review: Step | undefined;
    if (reviewed.length) {
      await ctx.open(page("Pull requests").search);
      await arrived(ctx, page("Pull requests"));
      const hrefs = await ctx.execute<string[]>(
        `return [...document.querySelectorAll("a[href]")].map((a) => new URL(a.href).search).filter((s) => /[?&]pr=/.test(s));`,
      );
      const listed = (e: Entry) => {
        const want = new URLSearchParams(where(e));
        return hrefs.some((h) => {
          const have = new URLSearchParams(h);
          return [...want].every(([k, v]) => have.get(k) === v);
        });
      };
      const pool = reviewed.some(listed) ? reviewed.filter(listed) : reviewed;
      const largest = pool.reduce((a, b) => (lines(b) > lines(a) ? b : a));
      const pr = largest.pull_request;
      ctx.note(
        `Pull request review: ${pr.repository}#${pr.number} (+${pr.additions ?? "?"} −${pr.deletions ?? "?"}), ` +
          `the largest of ${pool.length} reviewed pull requests ${pool === reviewed ? "in the data" : "the Pull requests page links to"}.`,
      );
      review = { label: "PR review", search: where(largest), check: REVIEW, via: { click: true } };
    } else gaps.push("PR review (no pull request has a review)");

    // A session's transcript: the largest one.
    let transcript: Step | undefined;
    const sessions = await ctx.execute<{ key: string; bytes: number }[]>(`
      const body = await (await fetch("api/live-sessions")).json();
      const list = Array.isArray(body?.sessions) ? body.sessions : [];
      const sized = [];
      for (const s of list) {
        const key = s.agent + ":" + s.session_id;
        const r = await fetch("api/live-sessions/" + encodeURIComponent(s.agent) + "/" + encodeURIComponent(s.session_id) + "/transcript");
        sized.push({ key, bytes: r.ok ? (await r.arrayBuffer()).byteLength : 0 });
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
      };
    } else gaps.push("Session transcript (no session has a transcript)");

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
    return { steps };
  },
  async run(ctx, state) {
    const metrics: Record<string, number> = {};
    // Direct loads first: each is a fresh document, so its snapshot is that
    // page's alone.
    for (const step of state.steps) {
      await ctx.open(step.search);
      const empty = await arrived(ctx, step);
      const snapshot = await ctx.execute<Snapshot>("return window.__baumPerf.snapshot();");
      Object.assign(
        metrics,
        pageMetrics(step.label, "direct", attribute(snapshot, 0, Infinity), {
          dom_count: await domSize(ctx),
          empty_count: empty ? 1 : 0,
        }),
      );
    }
    // The tour by navigation, last, so the runner's own snapshot is the
    // tour's: `after` attributes its windows to the pages.
    await ctx.open("");
    await arrived(ctx, state.steps.at(-1)!);
    await ctx.reset();
    const how = { sidebar: new Set<string>(), history: new Set<string>() };
    for (const step of state.steps) {
      await ctx.mark(navMark(step.label, "start"));
      await goTo(ctx, step, how);
      const empty = await arrived(ctx, step);
      // As `open` leaves it: what the page does after it settled ends.
      await ctx.sleep(500);
      await ctx.mark(navMark(step.label, "end"));
      Object.assign(
        metrics,
        pageMetrics(
          step.label,
          "nav",
          {},
          {
            dom_count: await domSize(ctx),
            empty_count: empty ? 1 : 0,
          },
        ),
      );
    }
    if (how.sidebar.size)
      ctx.note(
        `${[...how.sidebar].join(", ")}: Ctrl+digit did not open the page (the app reads the key's place, \`code\`, which WebDriver's synthesized key events leave empty), so navigation clicked the sidebar's link.`,
      );
    if (how.history.size)
      ctx.note(
        `${[...how.history].join(", ")}: no link to it on the page before, so navigation used the history entry the app's own links make.`,
      );
    return metrics;
  },
  after(snapshot) {
    const metrics: Record<string, number> = {};
    for (const { label, page } of navigationPages(snapshot))
      Object.assign(metrics, pageMetrics(label, "nav", page, {}));
    return metrics;
  },
  async teardown(ctx) {
    // Typed filters are the daemon's: the Timeline's opening filter may stay.
    await ctx.clearFilters();
  },
};

export { pagesScenario };

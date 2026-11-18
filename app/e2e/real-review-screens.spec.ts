import { mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { basename, join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

// Pictures of real saved reviews, taken only on request
// (REAL_REVIEW_SCREENSHOTS=1) against the real-reviews fixture daemon
// (TestLocalAppRealReviews, started by playwright.real-reviews.config.ts),
// which serves reviews copied from a private dataset. They are written to
// test-results/real-reviews, which is not committed, and hold private content.
//
// Per review, size and theme it opens the review page and takes each of STATES,
// as a viewport picture and a full-page one. Settings, all optional:
//   REAL_REVIEW_VIEW     the review page: full (default) or tiles
//   REAL_REVIEW_TILE     a selector for the tiles of the tiles page; the
//                        "detail" state opens the first one
//   REAL_REVIEW_ONLY     only the reviews whose repo#number contains this text
//   REAL_REVIEW_MINUTES  the time one review may take before it is left and
//                        recorded as a problem (default 8)
//   REAL_REVIEW_FULL=0   viewport pictures only
test.skip(!process.env.REAL_REVIEW_SCREENSHOTS, "screenshots are taken on request");

const VIEW = process.env.REAL_REVIEW_VIEW || "full";
const TILE = process.env.REAL_REVIEW_TILE || "[data-tile-id]";
const OUT = "test-results/real-reviews";
const BUDGET_MS = Number(process.env.REAL_REVIEW_MINUTES || 8) * 60_000;
const FULL_PAGE = process.env.REAL_REVIEW_FULL !== "0";

const SIZES = [
  { width: 1440, height: 900 },
  { width: 900, height: 900 },
  { width: 390, height: 844 },
];
const THEMES = ["light", "dark"] as const;
type Theme = (typeof THEMES)[number];

type Pick = {
  repository: string;
  number: number;
  title: string;
  version: number;
  versions: number[];
  clone: string;
  tags?: string[];
};

/** What a state's steps know about the review being photographed. */
type Context = {
  page: Page;
  pick: Pick;
  /** The id of the review's first section. */
  firstSection: string;
  /** The address of the review page, to which a state adds what it needs. */
  address: (extra?: Record<string, string>) => string;
};

/** A state of the page to photograph. `open` brings the page to it from the
 *  review's landing page and returns false when the review has no such state
 *  (it is then skipped). Add a state here to photograph it for every review;
 *  `views` limits it to some review pages. */
type State = {
  name: string;
  views?: string[];
  open: (c: Context) => Promise<boolean | void>;
};

export const STATES: State[] = [
  { name: "landing", open: async () => true },
  {
    // The first section; with the tiles page, the first tile.
    name: "detail",
    open: async ({ page, address, firstSection }) => {
      if (VIEW === "tiles") {
        const tile = page.locator(TILE).first();
        if ((await tile.count()) === 0) return false;
        await tile.click();
        // The pointer would otherwise rest on whatever the page drew under
        // it, such as a rail row, and the picture show that row hovered. The
        // page's title is a neutral place: the window's corner would open the
        // folded app sidebar over a phone's page.
        const title = await page.locator("main h1").first().boundingBox();
        if (title) await page.mouse.move(title.x + 8, title.y + title.height / 2);
      } else {
        if (!firstSection) return false;
        await page.goto(address({ pane: "section", section: firstSection }));
      }
    },
  },
  {
    // The tiles page with every tile and every group shown, where the
    // landing page holds some back.
    name: "more",
    views: ["tiles"],
    open: async ({ page, address }) => {
      await page.goto(address());
      await steady(page, "more");
      if ((await page.getByRole("button", { name: /^Show \d+ more / }).count()) === 0) return false;
      await page.goto(address({ more: "1", expand: "changes,obsolete" }));
    },
  },
  {
    name: "risks",
    views: ["tiles"],
    open: async ({ page, address }) => {
      await page.goto(address({ pane: "tile", tile: "risks" }));
    },
  },
  {
    name: "everything-else",
    views: ["tiles"],
    open: async ({ page, address }) => {
      await page.goto(address({ pane: "tile", tile: "everything_else" }));
    },
  },
  {
    // The first hunk excerpt of the detail, opened to code.
    name: "excerpt",
    open: async ({ page }) => {
      const summary = page.locator("summary[aria-label^='Code evidence']").first();
      if ((await summary.count()) === 0) return false;
      await summary.scrollIntoViewIfNeeded();
      await summary.click();
    },
  },
];

/** A problem a picture does not show clearly. `truncated` names a full-page
 *  picture that does not hold the whole page. */
type Issue = { where: string; what: string; truncated?: string };

/** Waits until the page has stopped changing: nothing spins, fonts and images
 *  have loaded, no excerpt is loading. What did not settle is returned. */
async function settle(page: Page, where: string): Promise<Issue[]> {
  const issues: Issue[] = [];
  // What is still going on, as names; empty once the page has settled.
  const pending = () =>
    page.evaluate(() => {
      const what: string[] = [];
      if (document.querySelector(".animate-spin")) what.push("a spinner");
      if (/Loading (excerpt|historical)/.test(document.body.innerText)) what.push("an excerpt");
      // A lazy image below the window loads only once scrolled to, so it is
      // not waited for: the window's picture does not show it.
      const below = (i: HTMLImageElement) =>
        i.loading === "lazy" && i.getBoundingClientRect().top > innerHeight;
      if ([...document.images].some((i) => !i.complete && !below(i))) what.push("an image");
      return what;
    });
  await page.evaluate(() => document.fonts.ready);
  try {
    await expect.poll(pending, { timeout: 20_000 }).toEqual([]);
  } catch {
    issues.push({
      where,
      what: `the page did not settle: ${(await pending()).join(", ")} still loading`,
    });
  }
  issues.push(...(await steady(page, where)));
  return issues;
}

/** Waits until the review pane holds its content (the review page's own
 *  loading status gone; on the tiles page, the landing page's band or a
 *  tile's heading drawn), every finite animation has finished, and its
 *  layout is the same twice running: the first picture of a run was taken
 *  while the pane was still arriving. */
async function steady(page: Page, where: string): Promise<Issue[]> {
  const layout = () =>
    page.evaluate((tiles) => {
      const pane = document.querySelector("#review-section");
      if (!pane || pane.querySelector(".loading-status")) return "loading";
      if (tiles && !pane.querySelector('[aria-label="Review at a glance"], h2:not(.sr-only)'))
        return "no content";
      const moving = document
        .getAnimations()
        .some(
          (animation) =>
            animation.playState === "running" &&
            animation.effect?.getComputedTiming().iterations !== Infinity,
        );
      if (moving) return "animating";
      return [pane, ...pane.querySelectorAll("article, h2, h3, section")]
        .slice(0, 40)
        .map((element) => {
          const box = element.getBoundingClientRect();
          return `${Math.round(box.x)},${Math.round(box.y)},${Math.round(box.width)},${Math.round(box.height)}`;
        })
        .join(";");
    }, VIEW === "tiles");
  const deadline = Date.now() + 20_000;
  let previous = "";
  while (Date.now() < deadline) {
    const now = await layout();
    if (now === previous && !["loading", "no content", "animating"].includes(now)) {
      // Transitions after the last change.
      await page.waitForTimeout(200);
      return [];
    }
    previous = now;
    await page.waitForTimeout(250);
  }
  return [{ where, what: `the review pane did not settle (${previous.split(";")[0]})` }];
}

/** What is wrong with a settled page that a picture would not show clearly. */
async function inspect(page: Page, where: string): Promise<Issue[]> {
  const found = await page.evaluate(() => {
    const what: string[] = [];
    for (const alert of document.querySelectorAll("[role='alert']")) {
      const text = (alert as HTMLElement).innerText.trim();
      if (text) what.push(`notice: ${text.slice(0, 160)}`);
    }
    const body = document.body.innerText;
    // An excerpt of a binary file is unavailable by nature: there is no text
    // to show. Said apart, so it is not taken for a missing commit.
    const unavailable = [...body.matchAll(/Excerpt unavailable\s*·\s*View (\S+)/g)].map(
      (match) => match[1]!,
    );
    const binary = /\.(png|jpe?g|gif|webp|ico|mp4|webm|mov|pdf|zip|gz|woff2?|ttf|otf|sqlite)$/i;
    if (unavailable.some((path) => !binary.test(path))) what.push("an excerpt is unavailable");
    else if (unavailable.length > 0)
      what.push("an excerpt of a binary file has no text (expected)");
    if ([...document.images].some((i) => i.complete && i.naturalWidth === 0 && i.currentSrc))
      what.push("an image failed to load");
    if (document.documentElement.scrollWidth > innerWidth + 1)
      what.push("the page scrolls sideways");
    return what;
  });
  return found.map((what) => ({ where, what }));
}

/** The tallest scrolling region and how far it scrolls: the app keeps its
 *  panes in regions of their own, so a full-page picture is a viewport as
 *  tall as the content. */
function scrollExtra(page: Page): Promise<number> {
  return page.evaluate(() => {
    let extra = document.documentElement.scrollHeight - innerHeight;
    for (const el of document.querySelectorAll<HTMLElement>("*")) {
      const overflow = getComputedStyle(el).overflowY;
      if ((overflow === "auto" || overflow === "scroll") && el.clientHeight > innerHeight / 3)
        extra = Math.max(extra, el.scrollHeight - el.clientHeight);
    }
    return Math.max(extra, 0);
  });
}

// Chromium cannot draw a picture much taller than this.
const MAX_HEIGHT = 16000;

async function capture(
  page: Page,
  base: string,
  size: { width: number; height: number },
  issues: Issue[],
) {
  await page.screenshot({ path: `${base}.png`, animations: "disabled" });
  if (!FULL_PAGE) return;
  // Growing the viewport can lengthen the content (regions that render what
  // fits), so grow until it all fits. A region whose overflow does not shrink
  // as the viewport grows (it is sized to the viewport) is not more content.
  let height = size.height;
  let previous = Number.POSITIVE_INFINITY;
  let truncated = false;
  for (let round = 0; ; round++) {
    const extra = await scrollExtra(page);
    if (extra === 0 || extra >= previous) break;
    if (round === 6 || height >= MAX_HEIGHT) {
      truncated = true;
      break;
    }
    previous = extra;
    height = Math.min(height + extra, MAX_HEIGHT);
    await page.setViewportSize({ width: size.width, height });
    issues.push(...(await settle(page, `${base} (full page)`)));
  }
  if (height === size.height) return;
  if (truncated)
    issues.push({
      where: base,
      what: "the full-page picture is truncated (the page kept growing or hit the height limit)",
      truncated: `${basename(base)}-full.png`,
    });
  await page.screenshot({ path: `${base}-full.png`, animations: "disabled" });
  await page.setViewportSize(size);
  await page.waitForTimeout(200);
}

const slugOf = (pick: Pick) =>
  `${pick.repository.replace("/", "-")}-${pick.number}-v${pick.version || pick.versions.at(-1)}`;

test("pictures of real saved reviews", async ({ page }) => {
  test.setTimeout(0);
  const state = process.env.BAUM_APP_STATE_DIR ?? "";
  const picks: Pick[] = JSON.parse(await readFile(join(state, "picks.json"), "utf8")).filter(
    (p: Pick) => `${p.repository}#${p.number}`.includes(process.env.REAL_REVIEW_ONLY ?? ""),
  );
  expect(picks.length).toBeGreaterThan(0);
  // The launch address signs the browser in and redirects to the session's own
  // path, dropping any query; review pages are addresses under that path.
  await page.goto(await launchURL());
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  const launch = new URL(page.url());
  launch.search = "";
  const consoleErrors: string[] = [];
  page.on("console", (m) => m.type() === "error" && consoleErrors.push(m.text()));
  page.on("pageerror", (e) => consoleErrors.push(String(e)));

  const ran: string[] = [];
  for (const pick of picks) {
    const slug = slugOf(pick);
    ran.push(slug);
    const dir = join(OUT, slug);
    await rm(dir, { recursive: true, force: true });
    await mkdir(dir, { recursive: true });
    const issues: Issue[] = [];
    const address = (extra: Record<string, string> = {}) => {
      const url = new URL(launch);
      url.search = new URLSearchParams({
        clone: pick.clone,
        pr: String(pick.number),
        view: VIEW,
        ...(pick.version ? { review: String(pick.version) } : {}),
        ...extra,
      }).toString();
      return url.href;
    };
    const started = Date.now();
    const over = () => {
      if (Date.now() - started > BUDGET_MS) throw new Error("the review's time budget ran out");
    };
    await test.step(`${pick.repository}#${pick.number}`, async () => {
      try {
        await page.setViewportSize(SIZES[0]!);
        await page.goto(address());
        issues.push(...(await settle(page, "first load")));
        const document = await page.evaluate(
          async ({ clone, pr, version }) => {
            const query = new URLSearchParams({
              clone,
              pr: String(pr),
              ...(version ? { version } : {}),
            });
            const answer = await fetch(`api/document?${query}`);
            return answer.ok
              ? {
                  status: answer.status,
                  firstSection: (await answer.json()).sections?.[0]?.id ?? "",
                }
              : { status: answer.status, firstSection: "" };
          },
          { clone: pick.clone, pr: pick.number, version: pick.version ? String(pick.version) : "" },
        );
        const firstSection: string = document.firstSection;
        if (!firstSection)
          issues.push({
            where: "document",
            what: `no first section (the document answered ${document.status}): the detail and excerpt states of the full page are skipped`,
          });
        for (const theme of THEMES as readonly Theme[]) {
          await page.emulateMedia({ colorScheme: theme });
          for (const size of SIZES) {
            await page.setViewportSize(size);
            await page.goto(address());
            const context: Context = { page, pick, firstSection, address };
            for (const s of STATES) {
              if (s.views && !s.views.includes(VIEW)) continue;
              over();
              const name = `${VIEW}-${s.name}-${size.width}-${theme}`;
              if ((await s.open(context)) === false) continue;
              issues.push(...(await settle(page, name)));
              const dark = await page.evaluate(() => document.documentElement.dataset.theme);
              if (dark !== theme)
                issues.push({ where: name, what: `the page is in the ${dark} theme` });
              issues.push(...(await inspect(page, name)));
              await capture(page, join(dir, name), size, issues);
            }
          }
        }
      } catch (error) {
        // A review that hangs or fails is recorded and left; the run goes on.
        issues.push({ where: "review", what: `left unfinished: ${String(error).split("\n")[0]}` });
      }
    });
    const unique = [...new Map(issues.map((i) => [`${i.where}|${i.what}`, i])).values()];
    await writeFile(
      join(dir, "review.json"),
      JSON.stringify(
        { ...pick, shown: pick.version || pick.versions.at(-1), issues: unique },
        null,
        2,
      ),
    );
  }
  await writeFile(join(OUT, "console-errors.txt"), [...new Set(consoleErrors)].join("\n"));
  // The requests the refusing GitHub and model backends answer with an error
  // are expected; any other console error is worth a look.
  const unexpected = consoleErrors.filter(
    (e) =>
      !/^Failed to load resource: the server responded with a status of (404|502|503)\b/.test(e),
  );
  if (unexpected.length) console.log(`console errors: ${unexpected.length}`);
  await writeFile(
    join(OUT, "run.json"),
    JSON.stringify(
      { view: VIEW, reviews: ran, unexpectedConsoleErrors: [...new Set(unexpected)] },
      null,
      2,
    ),
  );
  await writeContactSheet(OUT);
});

const escape = (s: string) =>
  s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]!);

/** A static page that lists the reviews of this run (run.json), their
 *  problems, and their pictures by state, size and theme. Folders of earlier
 *  runs stay in the directory but are not listed. */
async function writeContactSheet(out: string) {
  const run: { view: string; reviews: string[]; unexpectedConsoleErrors: string[] } = JSON.parse(
    await readFile(join(out, "run.json"), "utf8"),
  );
  const reviews: string[] = [];
  for (const slug of run.reviews) {
    const entry = { name: slug };
    let info: (Pick & { issues: Issue[]; shown: number }) | undefined;
    try {
      info = JSON.parse(await readFile(join(out, entry.name, "review.json"), "utf8"));
    } catch {
      continue;
    }
    const all = await readdir(join(out, entry.name));
    const byState = new Map<string, string[]>();
    for (const file of all.filter((f) => f.endsWith(".png") && !f.endsWith("-full.png"))) {
      const key = file.replace(/-\d+-(light|dark)\.png$/, "");
      byState.set(key, [...(byState.get(key) ?? []), file]);
    }
    const rows = [...byState.entries()].map(([key, list]) => {
      const cells = list
        .sort((a, b) => a.localeCompare(b))
        .map((file) => {
          const full = file.replace(/\.png$/, "-full.png");
          const link = all.includes(full) ? ` · <a href="${entry.name}/${full}">full page</a>` : "";
          const cut = info!.issues.some((i) => i.truncated === full) ? " (truncated)" : "";
          return `<figure><a href="${entry.name}/${file}"><img loading="lazy" src="${entry.name}/${file}" alt="${escape(file)}"></a><figcaption>${escape(file.replace(/\.png$/, ""))}${link}${cut}</figcaption></figure>`;
        });
      return `<h3>${escape(key)}</h3><div class="row">${cells.join("")}</div>`;
    });
    const issues = info!.issues.length
      ? `<ul class="issues">${info!.issues.map((i) => `<li>${escape(i.where)}: ${escape(i.what)}</li>`).join("")}</ul>`
      : `<p class="ok">no problems found</p>`;
    reviews.push(
      `<section id="${entry.name}"><h2>${escape(info!.repository)}#${info!.number} v${info!.shown} <small>${escape((info!.tags ?? []).join(" "))}</small></h2><p>${escape(info!.title)}</p>${issues}${rows.join("")}</section>`,
    );
  }
  const nav = reviews
    .map((r) => r.match(/id="([^"]+)"/)![1]!)
    .map((id) => `<a href="#${id}">${escape(id)}</a>`);
  await writeFile(
    join(out, "index.html"),
    `<!doctype html><meta charset="utf-8"><title>Real review screenshots</title>
<style>
body{font:14px system-ui,sans-serif;margin:16px;background:#fafafa;color:#111}
nav a{margin-right:10px}
h2{margin-top:40px} h2 small{font-weight:400;color:#666}
.row{display:flex;gap:12px;overflow-x:auto;align-items:flex-start;padding-bottom:8px}
figure{margin:0;flex:none}
img{height:260px;width:auto;border:1px solid #ccc;display:block}
figcaption{font-size:12px;color:#555}
.issues{color:#a00}.ok{color:#070}
</style>
<h1>Real review screenshots</h1><p class="${run.unexpectedConsoleErrors.length ? "issues" : "ok"}">${run.unexpectedConsoleErrors.length ? `console errors: ${escape(run.unexpectedConsoleErrors.join(" | ").slice(0, 600))}` : "no unexpected console errors"}</p><nav>${nav.join("")}</nav>${reviews.join("\n")}`,
  );
}

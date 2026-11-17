import { mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";
import { capture, inspect, settle, type Issue } from "./review-screens";

// Pictures of real saved reviews, taken only on request
// (REAL_REVIEW_SCREENSHOTS=1) against the real-reviews fixture daemon
// (TestLocalAppRealReviews, started by playwright.real-reviews.config.ts),
// which serves reviews copied from a private dataset. They are written to
// test-results/real-reviews, which is not committed, and hold private content.
//
// Per review, size and theme it opens the review page and takes each of STATES,
// as a viewport picture and a full-page one. Settings, all optional:
//   REAL_REVIEW_VIEW     the review page: full (default) or tiles (the
//                        overview tabs)
//   REAL_REVIEW_ONLY     only the reviews whose repo#number contains this text
//   REAL_REVIEW_MINUTES  the time one review may take before it is left and
//                        recorded as a problem (default 8)
//   REAL_REVIEW_FULL=0   viewport pictures only
test.skip(!process.env.REAL_REVIEW_SCREENSHOTS, "screenshots are taken on request");

const VIEW = process.env.REAL_REVIEW_VIEW || "full";
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
  // The full review's summary, or the Overview tab.
  { name: "landing", open: async () => true },
  {
    // The first section: on the overview tabs, How it works opened on it.
    name: "detail",
    open: async ({ page, address, firstSection }) => {
      if (!firstSection) return false;
      await page.goto(address({ pane: "section", section: firstSection }));
    },
  },
  {
    // "Where this fits", the review's context.
    name: "context",
    views: ["tiles"],
    open: async ({ page, address }) => {
      await page.goto(address({ pane: "section", section: "context" }));
    },
  },
  {
    // The first section's Code view, and then its Tests view when it has
    // tests: the section's own hunks with their notes.
    name: "section-code",
    views: ["tiles"],
    open: async ({ page, address, firstSection }) => {
      if (!firstSection) return false;
      await page.goto(address({ pane: "section", section: firstSection, section_view: "code" }));
    },
  },
  {
    name: "section-tests",
    views: ["tiles"],
    open: async ({ page, address, firstSection }) => {
      if (!firstSection) return false;
      await page.goto(address({ pane: "section", section: firstSection, section_view: "tests" }));
      const tests = page
        .getByRole("tablist", { name: "Section views" })
        .getByRole("tab", { name: /^Tests/ });
      if (
        !(await tests.waitFor().then(
          () => tests.isEnabled(),
          () => false,
        ))
      )
        return false;
    },
  },
  {
    // The Code tab on its first file.
    name: "code",
    views: ["tiles"],
    open: async ({ page, address }) => {
      await page.goto(address({ pane: "tile", tile: "code" }));
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
        issues.push(...(await settle(page, "first load", VIEW === "tiles")));
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
              issues.push(...(await settle(page, name, VIEW === "tiles")));
              const dark = await page.evaluate(() => document.documentElement.dataset.theme);
              if (dark !== theme)
                issues.push({ where: name, what: `the page is in the ${dark} theme` });
              issues.push(...(await inspect(page, name)));
              await capture(page, join(dir, name), size, issues, {
                fullPage: FULL_PAGE,
                tiles: VIEW === "tiles",
              });
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

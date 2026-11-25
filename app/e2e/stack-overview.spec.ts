import { expect, test, type Page } from "@playwright/test";
import type { Entry, ReviewGroup, Snapshot } from "../src/api";
import { launchURL } from "./inbox-helpers";

test.afterEach(async ({ page }) => {
  // A background index poll may still be in flight when the page closes.
  await page.unrouteAll({ behavior: "ignoreErrors" });
});

// A stack of nine open pull requests, past the eight a combined review once
// allowed, on two that merged.
const titles: [number, string][] = [
  [1281, "Remove the walkthrough from the desktop app"],
  [1283, "Describe tiles in the review schema"],
  [1286, "Write tiles in the staged review's details stage"],
  [1289, "Render a review's tiles in a grid on its Overview tab"],
  [1294, "Lay out the tile grid by the reader's chosen columns"],
  [1297, "Keep a tile's place when its section is rewritten"],
  [1302, "Draw diagram tiles with the native renderer"],
  [1306, "Frame a tile around its code excerpt"],
  [1311, "Save the reader's tile layout per repository"],
  [1316, "Guard tile writes against a stale layout"],
  [1321, "Make a tile in the session panel and add it to the reader's layout"],
];
const merged = new Set([1281, 1283]);
const risks: Record<number, [number, string]> = {
  1286: [
    2,
    "The details stage writes tiles beside sections, so a reply without them still validates.",
  ],
  1289: [2, "The grid reads tiles the schema marks optional."],
  1294: [
    3,
    "A narrow window falls back to one column, which the layout code computes on every resize.",
  ],
  1297: [2, "Placement is keyed by section id, which a rewrite keeps."],
  1302: [3, "The native renderer runs on untrusted diagram text from the model."],
  1306: [2, "Excerpts are read from the saved hunks, not the checkout."],
  1311: [
    4,
    "The layout is saved per repository with no version, so two windows write over each other's last change.",
  ],
  1316: [3, "The guard compares layout revisions and refuses a write made from an older one."],
  1321: [2, "The session panel adds a tile through the same save path as the grid."],
};
const sizes: Record<number, [number, number, number]> = {
  1281: [12, 640, 15],
  1283: [176, 8, 3],
  1286: [412, 96, 9],
  1289: [688, 140, 14],
  1294: [233, 61, 6],
  1297: [158, 40, 4],
  1302: [521, 37, 11],
  1306: [204, 18, 5],
  1311: [347, 72, 8],
  1316: [126, 22, 3],
  1321: [298, 54, 7],
};
const open = titles.map(([n]) => n).filter((n) => !merged.has(n));

const stack: ReviewGroup = {
  id: "tile-stack",
  repository: "acme/demo",
  name: "Tiles in the review",
  members: open,
  stack: true,
  landed: [...merged].map((number) => ({ number, state: "merged" as const })),
  created_at: "",
  updated_at: "",
  version_count: 0,
  latest: null,
  risk_score: {
    score: 4,
    status: "assessed",
    reasons: [{ section: "s3", text: risks[1311]![1] }],
  },
  reviewed: [],
  outdated: false,
  overview: {
    riskiest: 1311,
    reviewed: open.length,
    summary: {
      text: "The stack replaces the review's walkthrough with tiles that a reader arranges on the Overview tab. It adds tiles to the review schema and the staged review, renders them in a grid with diagram and code tiles, and saves each reader's layout per repository. Start with #1286 and #1289, which the rest build on; #1311 and #1316 hold the layout's saving and its guard.",
      model: "claude-haiku-5-5",
      created_at: "2026-11-17T09:00:00Z",
      members: open.map((number) => ({ number, review_version: 1 })),
    },
    current: true,
  },
};

async function routeStack(page: Page) {
  await page.route(
    (url) => url.pathname.endsWith("/api/index"),
    async (route) => {
      const response = await route.fetch();
      const snapshot = (await response.json()) as Snapshot;
      const original = snapshot.entries[0]!;
      const entries = titles.map(([number, title], i): Entry => {
        const done = merged.has(number);
        const [score, reason] = risks[number] ?? [2, ""];
        const [additions, deletions, files] = sizes[number] ?? [80, 20, 3];
        return {
          ...original,
          review: "commented",
          historical: done,
          status: done ? "unknown" : "current",
          version_count: 1,
          review_minutes: Math.round((additions + deletions) / 60) + 2,
          risk_score: { score, status: "assessed", reasons: [{ section: "s1", text: reason }] },
          checks: "passing",
          merge: "blocked",
          unresolved: 0,
          pull_request: {
            ...original.pull_request,
            repository: "acme/demo",
            number,
            title,
            url: `https://github.com/acme/demo/pull/${number}`,
            head_ref: `tiles-${i}`,
            // The bottom open one was retargeted to main when its parents merged.
            base_ref: i === 0 || i === merged.size ? "main" : `tiles-${i - 1}`,
            additions,
            deletions,
            changed_files: files,
            authored_by_me: true,
            review_requested_from_me: false,
            draft: false,
            state: done ? "MERGED" : "OPEN",
            checks: "passing",
            merge: "blocked",
            unresolved: 0,
          },
        } as Entry;
      });
      await route.fulfill({ response, json: { ...snapshot, entries } });
    },
  );
  await page.route(
    (url) => url.pathname.endsWith("/api/groups"),
    (route) => route.fulfill({ json: { groups: [stack] } }),
  );
  await page.route(
    (url) => url.pathname.endsWith("/api/jobs"),
    (route) => route.fulfill({ json: { jobs: [] } }),
  );
}

test("a stack's page is its overview, each pull request opening its own review", async ({
  page,
}) => {
  test.slow();
  let combined = 0;
  await page.route(
    (url) => /\/api\/groups\/[^/]+\/(document|versions)/.test(url.pathname),
    (route) => {
      combined++;
      return route.fulfill({ status: 404, body: "" });
    },
  );
  await routeStack(page);
  const shots = "test-results/stack-overview";
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height: 1000 });
      await page.goto(await launchURL());
      // The stack in the Inbox: its panel is the same overview.
      const row = page.locator(".upnext-row-title", { hasText: "Tiles in the review" });
      await expect(row).toBeVisible();
      await row.click();
      const panel = page.locator("article.upnext-panel");
      await expect(panel.getByRole("link", { name: "Open the stack" })).toBeVisible();
      // Snooze, beside it, snoozes the whole stack.
      await expect(panel.getByRole("button", { name: /^Snooze/ })).toBeVisible();
      await page.screenshot({
        path: `${shots}/inbox-panel-${width}-${scheme}.png`,
        animations: "disabled",
      });
      await panel.getByRole("link", { name: "Open the stack" }).click();
      const overview = page.locator('article[aria-label="Tiles in the review"]');
      await expect(overview.locator(".upnext-chip")).toHaveText("Stack");
      // Snoozing is the Inbox's: the stack's own page does not offer it.
      await expect(overview.getByRole("button", { name: /^Snooze/ })).toHaveCount(0);
      await expect(overview.locator(".upnext-risk")).toContainText("4 of 5");
      await expect(
        overview.locator(".upnext-risk").getByRole("link", {
          name: "Save the reader's tile layout per repository",
        }),
      ).toBeVisible();
      await expect(overview.getByRole("region", { name: "What changed" })).toContainText(
        "replaces the review's walkthrough with tiles",
      );
      await expect(overview.locator(".upnext-members > li")).toHaveCount(titles.length);
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(
        width,
      );
      // Tall enough for the whole list, which scrolls inside the page.
      await page.setViewportSize({ width, height: width < 760 ? 2200 : 1500 });
      await page.screenshot({
        path: `${shots}/stack-page-${width}-${scheme}.png`,
        animations: "disabled",
        fullPage: true,
      });
    }
  }
  // A pull request opens its own review page.
  await page
    .locator('article[aria-label="Tiles in the review"] .upnext-members')
    .getByRole("link", { name: /Frame a tile around its code excerpt/ })
    .click();
  await expect(page).toHaveURL(/pr=1306/);
  expect(combined).toBe(0);
});

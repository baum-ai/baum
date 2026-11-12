import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, test } from "@playwright/test";

// The fixture daemon publishes its launch URL in its credential file.
async function launchURL(): Promise<string> {
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      const path = join(process.env.BAUM_APP_STATE_DIR ?? "", "credential.json");
      const { url } = JSON.parse(await readFile(path, "utf8"));
      if (typeof url === "string") return url;
    } catch {
      // Not written yet.
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error("the daemon did not publish its credential");
}

test("a local feature opens its decision and redacted source transcript", async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 });
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Timeline", exact: true }).click();
  const features = page.getByRole("list", { name: "Features" });
  // A wide screen opens the feature beside the timeline.
  await features
    .getByRole("button", { name: /Local decision log/ })
    .first()
    .click();
  await expect(page).toHaveURL(/feature=feature-local/);
  const panel = page.locator("#opened-feature-panel");
  await expect(panel).toContainText("Serve the decision log locally");
  await panel.getByRole("button", { name: /#12 Serve the decision log locally/ }).click();
  await expect(panel).toContainText("Adds the local Timeline API and page.");
  await panel.getByRole("button", { name: /Serve the Timeline from the daemon/ }).click();
  await expect(panel).toContainText("Sync the hosted log down");
  // The prompt it cites, from the transcript on this computer, masked.
  await expect(panel).toContainText("Serve the Timeline from the daemon's own store; ask");
  await expect(panel).not.toContainText("ada.lovelace@example.com");
  // The session opens at the cited turn, though no tracker lists it.
  await panel.getByRole("button", { name: "Open session at turn 1" }).click();
  const card = panel.getByRole("region", { name: "Transcript" });
  await expect(card).toContainText("Draw it with the review viewer's decision log components.");
  await expect(card).not.toContainText("ada.lovelace@example.com");
});

test("the keys go on from a clicked feature, take the panel along and scroll only when needed", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1600, height: 1000 });
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Timeline", exact: true }).click();
  const features = page.getByRole("list", { name: "Features" });
  await features.getByRole("button", { name: /Keyword search over decisions/ }).click();
  await expect(page).toHaveURL(/feature=feature-search/);
  // k goes to the feature above the clicked one, not to the list's end, and
  // the panel beside the list opens it.
  await page.keyboard.press("k");
  await expect(features.locator("[data-focused]")).toHaveAttribute("data-row-id", "feature-local");
  await expect(page).toHaveURL(/feature=feature-local/);
  // A row already in sight is not scrolled to.
  const list = page.locator('[role="tabpanel"]:not([hidden])');
  const scrolled = await list.evaluate((element) => element.scrollTop);
  await page.keyboard.press("j");
  await expect(features.locator("[data-focused]")).toHaveAttribute("data-row-id", "feature-search");
  expect(await list.evaluate((element) => element.scrollTop)).toBe(scrolled);
});

test("only the timeline's list scrolls; the panel, name and filters stay", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 600 });
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Timeline", exact: true }).click();
  const features = page.getByRole("list", { name: "Features" });
  await features.waitFor();
  const frame = page.locator(".local-frame");
  const before = (await frame.boundingBox())?.y;
  await features.hover();
  await page.mouse.wheel(0, 2000);
  const list = page.locator('[role="tabpanel"]:not([hidden])');
  await expect.poll(() => list.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
  expect(await page.evaluate(() => window.scrollY)).toBe(0);
  expect((await frame.boundingBox())?.y).toBe(before);
  await expect(page.getByRole("heading", { name: "Timeline" })).toBeInViewport();
});

test("the keys back to the first feature bring its month back", async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 600 });
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Timeline", exact: true }).click();
  const features = page.getByRole("list", { name: "Features" });
  await features.waitFor();
  for (const key of ["j", "j", "j", "j", "k", "k", "k"]) await page.keyboard.press(key);
  await expect(features.locator("[data-focused]")).toHaveAttribute("data-row-id", "feature-local");
  const list = page.locator('[role="tabpanel"]:not([hidden])');
  await expect.poll(() => list.evaluate((element) => element.scrollTop)).toBe(0);
});

test("the Timeline filters with the Inbox's filter menu and query", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Timeline", exact: true }).click();
  const features = page.getByRole("list", { name: "Features" });
  await features.waitFor();
  await page.locator("summary[aria-label='Filter']").click();
  // An empty filter shows its query box once "Write a query…" asks for it.
  await page.getByRole("option", { name: "Write a query…" }).click();
  const bar = page.getByRole("combobox", { name: "Filter the Timeline" });
  await bar.fill("status=");
  await expect(page.getByRole("listbox", { name: "Suggestions" })).toContainText("status=shipped");
  await bar.fill("status=shipped");
  await bar.press("Enter");
  await expect(page).toHaveURL(/status=shipped/);
  await expect(features.getByRole("button", { name: /Keyword search over decisions/ })).toHaveCount(
    0,
  );
  // OR cannot be sent to the server: the bar says so and keeps the filter.
  await bar.fill("status=shipped OR status=abandoned");
  await bar.press("Enter");
  await expect(page.getByRole("status").filter({ hasText: "must all hold" })).toBeVisible();
  await expect(page).toHaveURL(/status=shipped/);
});

test("the Timeline's search box narrows by text, beside the filter", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Timeline", exact: true }).click();
  const features = page.getByRole("list", { name: "Features" });
  await features.waitFor();
  await page.getByRole("searchbox", { name: "Search features" }).fill("keyword");
  await expect(page).toHaveURL(/q=keyword/);
  await expect(features.getByRole("button", { name: /Local decision log/ })).toHaveCount(0);
  await expect(
    features.getByRole("button", { name: /Keyword search over decisions/ }),
  ).toBeVisible();
});

const buildCoverage = {
  protocol: "feature-build-public-v1",
  since: "2026-10-01",
  catalogued: 1200,
  inspected: 12,
  selected: 8,
  completed: 5,
  remaining: 3,
  truncated: true,
  source: {
    revision: "1234567890abcdef",
    omissions: 2,
    limitations: ["Uncommitted files are not included."],
  },
};

test("adaptive build coverage preserves the current Timeline and new-build requests stay namespaced", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route("**/api/timeline/build?**", (route) =>
    route.fulfill({
      json: {
        building: {
          placed: 5,
          total: 8,
          stopped: "no_progress",
          spent_usd: 1,
          workflow: buildCoverage.protocol,
          coverage: buildCoverage,
        },
        has_features: true,
      },
    }),
  );
  await page.goto(await launchURL());
  const base = page.url().split("?")[0]!;
  await page.goto(`${base}?page=timeline&repo=acme%2Fdemo`);
  const status = page.getByRole("region", { name: "Feature build", exact: true });
  await expect(status).toContainText("1,200 pull request titles available");
  await expect(status).toContainText("Evidence requested for 12 pull requests");
  await expect(status).toContainText("8 selected");
  await expect(status).toContainText("3 remaining");
  await expect(status).toContainText("older open work included");
  await expect(page.getByRole("list", { name: "Features" })).toContainText("Local decision log");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  const posted = page.waitForRequest(
    (request) => request.method() === "POST" && request.url().endsWith("/api/jobs"),
  );
  await page.route("**/api/jobs", async (route) => {
    if (route.request().method() !== "POST") return route.continue();
    await route.fulfill({ status: 409, json: { blocked_reason: "build_running" } });
  });
  await status.getByRole("button", { name: "Start new build", exact: true }).click();
  const request = await posted;
  expect(new URL(request.url()).pathname).toMatch(/^\/s\/[^/]+\/api\/jobs$/);
  expect(request.postDataJSON().items[0]).toMatchObject({
    kind: "feature_build",
    feature_build_mode: "fresh",
  });
  await expect(page.getByRole("list", { name: "Features" })).toContainText("Local decision log");
  await expect(page.getByText("Couldn't start the feature build:", { exact: false })).toContainText(
    "already queued or running",
  );
});

test("a completed empty window keeps its coverage after reopening", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.route("**/api/timeline/build?**", (route) =>
    route.fulfill({
      json: {
        building: null,
        has_features: false,
        current_since: "2026-10-31T00:00:00Z",
        current_coverage: { ...buildCoverage, selected: 0, completed: 0, remaining: 0 },
      },
    }),
  );
  await page.route("**/api/timeline/features?**", (route) =>
    route.fulfill({ json: { features: [], next_cursor: null } }),
  );
  await page.goto(await launchURL());
  const base = page.url().split("?")[0]!;
  await page.goto(`${base}?page=timeline&repo=acme%2Fdemo`);
  await expect(page.getByRole("region", { name: "Feature build coverage" })).toContainText(
    "No substantial features found in this window",
  );
  await page.reload();
  await expect(page.getByRole("region", { name: "Feature build coverage" })).toContainText(
    "1,200 pull request titles available",
  );
  await expect(page.getByRole("region", { name: "Feature build coverage" })).not.toContainText(
    "full history",
  );
});

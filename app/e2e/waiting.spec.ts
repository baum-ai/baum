import { expect, test } from "@playwright/test";
import type { Snapshot } from "../src/api";
import { launchURL } from "./inbox-helpers";

test.afterEach(async ({ page }) => {
  await page.unrouteAll({ behavior: "ignoreErrors" });
});

test("an unspecified merge blocker is not called a required review", async ({ page }) => {
  await page.route(
    (url) => url.pathname.endsWith("/api/index"),
    async (route) => {
      const response = await route.fetch();
      const snapshot = (await response.json()) as Snapshot;
      const entry = snapshot.entries[0]!;
      const state = {
        merge: "blocked" as const,
        checks: "passing" as const,
        review: "commented" as const,
      };
      await route.fulfill({
        response,
        json: {
          ...snapshot,
          entries: [
            {
              ...entry,
              ...state,
              historical: false,
              unresolved: 0,
              pull_request: {
                ...entry.pull_request,
                ...state,
                number: 1130,
                title: "Unspecified merge blocker",
                authored_by_me: true,
                review_requested_from_me: false,
                review_required: false,
                draft: false,
                state: "OPEN",
                behind: false,
                unresolved: 0,
              },
            },
          ],
        },
      });
    },
  );
  await page.route(
    (url) => url.pathname.endsWith("/api/prs"),
    (route) => route.fulfill({ json: [] }),
  );
  await page.route(
    (url) => url.pathname.endsWith("/api/jobs"),
    (route) => route.fulfill({ json: { jobs: [] } }),
  );
  await page.route(
    (url) => url.pathname.endsWith("/api/prs/checks"),
    (route) =>
      route.fulfill({
        json: { checks: [{ name: "CI / Tests", status: "completed", conclusion: "success" }] },
      }),
  );
  await page.goto(await launchURL());
  const row = page.locator("button.upnext-row").filter({ hasText: "Unspecified merge blocker" });
  await expect(row).toContainText("Blocked on GitHub");
  await expect(row).not.toContainText("Waiting on review");
  await row.click();
  await expect(page.locator(".upnext-panel .upnext-chip-waiting")).toHaveText("Blocked on GitHub");
  await expect(page.getByRole("region", { name: "Why this is waiting" })).toHaveCount(0);
  const explanation = page
    .locator(".upnext-panel .upnext-block")
    .filter({ has: page.getByRole("heading", { name: "On GitHub", exact: true }) });
  await expect(explanation).toContainText("Merge blocked by GitHub");
  await expect(explanation).toContainText("GitHub has not supplied the reason.");
  await expect(page.getByRole("link", { name: "Open on GitHub", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /^Merge all/ })).toHaveCount(0);
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await expect(row).toContainText("Blocked on GitHub");
    await expect(explanation).toBeVisible();
    await explanation.scrollIntoViewIfNeeded();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(
      width,
    );
    await page.screenshot({ path: `test-results/github-blocker-${width}.png` });
  }
});

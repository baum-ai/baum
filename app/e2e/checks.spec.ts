import { expect, test } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

test("live checks keep their rows aligned in the review Activity pane", async ({ page }) => {
  await page.route("**/api/prs/checks?*", async (route) => {
    await route.fulfill({
      json: {
        checks: [
          {
            name: "CI / Platform",
            status: "completed",
            conclusion: "failure",
            job_id: 21,
            url: "https://github.com/acme/demo/actions/runs/1/job/21",
          },
          {
            name: `Workers Builds: ${"a-long-project-name-".repeat(8)}`,
            title: "Build completed successfully",
            status: "completed",
            conclusion: "success",
            url: "https://github.com/acme/demo/actions/runs/2",
          },
          { name: "CI / Desktop app", status: "completed", conclusion: "skipped" },
        ],
      },
    });
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
  await page.getByRole("tab", { name: /^Activity/ }).click();
  const checks = page.getByRole("list", { name: "Checks", exact: true });
  await expect(checks.getByRole("listitem")).toHaveCount(1);
  await page.getByRole("button", { name: "Show 2 other checks", exact: true }).click();
  await expect(checks.getByRole("listitem")).toHaveCount(3);

  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 1000 });
    if (width < 1280) await page.keyboard.press("]");
    await expect(checks).toBeVisible();
    const layout = await checks.evaluate((list) => {
      const bounds = list.getBoundingClientRect();
      return [...list.children].map((row) => {
        const icon = row.firstElementChild!.getBoundingClientRect();
        const text = row.querySelector(".upnext-block-text")!.getBoundingClientRect();
        const actions = row.querySelector(".upnext-check-actions")!.getBoundingClientRect();
        return {
          textAfterIcon: text.left >= icon.right,
          actionsAfterText: actions.left >= text.right,
          actionsWithinList: actions.right <= bounds.right,
          textWidth: text.width,
        };
      });
    });
    for (const row of layout) {
      expect(row.textAfterIcon).toBe(true);
      expect(row.actionsAfterText).toBe(true);
      expect(row.actionsWithinList).toBe(true);
      expect(row.textWidth).toBeGreaterThan(40);
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(
      width,
    );
    await page.screenshot({ path: `test-results/checks-${width}.png` });
  }
  await page.getByRole("button", { name: "Hide other checks", exact: true }).click();
  await expect(checks.getByRole("listitem")).toHaveCount(1);
});

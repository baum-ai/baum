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

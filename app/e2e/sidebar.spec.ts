import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

const lane = (page: Page) => page.locator(".local-rail-lane");
const rail = (page: Page) => page.locator(".local-rail");
const pin = (page: Page) => page.getByRole("button", { name: "Pin sidebar" });
const width = async (page: Page, selector: string) =>
  (await page.locator(selector).boundingBox())!.width;

/** Puts the sidebar back as a fresh installation has it, so the specs that
 *  follow see it pinned. */
async function pinAgain(page: Page) {
  if (!page.url().includes("/s/")) return;
  await page.evaluate(() =>
    fetch("api/settings/appearance", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ sidebar: "pinned" }),
    }),
  );
}

test.afterEach(async ({ page }) => pinAgain(page));

test("folds the sidebar, opens it under the pointer and pins it again", async ({
  page,
  browser,
}) => {
  await page.goto(await launchURL());
  await expect(lane(page)).toHaveAttribute("data-rail", "pinned");
  await expect(pin(page)).toHaveAttribute("aria-pressed", "true");
  const panel = page.locator(".local-frame");
  const pinnedLeft = (await panel.boundingBox())!.x;

  // Folding gives the page the width.
  const saved = page.waitForResponse(
    (response) =>
      response.url().includes("api/settings/appearance") &&
      response.request().method() === "PUT" &&
      response.ok(),
  );
  await pin(page).click();
  await saved;
  await expect(lane(page)).toHaveAttribute("data-rail", "folded");
  await expect.poll(() => width(page, ".local-rail")).toBe(56);
  await expect.poll(async () => (await panel.boundingBox())!.x).toBeLessThan(pinnedLeft);

  // The pointer opens it over the page, which stays put.
  await page.mouse.move(800, 400);
  const foldedLeft = (await panel.boundingBox())!.x;
  await page.mouse.move(28, 300);
  await expect(lane(page)).toHaveAttribute("data-rail", "open");
  await expect.poll(() => width(page, ".local-rail")).toBe(244);
  expect((await panel.boundingBox())!.x).toBe(foldedLeft);
  await expect(rail(page).getByText("Jobs", { exact: true })).toBeVisible();
  await page.mouse.move(800, 400);
  await expect(lane(page)).toHaveAttribute("data-rail", "folded");

  // A place chosen from the open sidebar folds it.
  await page.mouse.move(28, 300);
  await expect(lane(page)).toHaveAttribute("data-rail", "open");
  await rail(page).getByRole("link", { name: /^Jobs/ }).click();
  await expect(page).toHaveURL(/page=jobs/);
  await expect(lane(page)).toHaveAttribute("data-rail", "folded");
  await page.mouse.move(800, 400);

  // The choice is the daemon's, not the window's.
  const fresh = await browser.newPage();
  await fresh.goto(await launchURL());
  await expect(lane(fresh)).toHaveAttribute("data-rail", "folded");
  await expect(fresh.locator(".local-rail-pin")).toHaveAttribute("aria-pressed", "false");
  await fresh.close();

  // Keyboard focus in the folded sidebar opens it too.
  for (let i = 0; i < 30; i++) {
    await page.keyboard.press("Tab");
    if (await page.evaluate(() => !!document.activeElement?.closest(".local-rail"))) break;
  }
  await expect.poll(() => width(page, ".local-rail")).toBe(244);
  await page.locator(".local-frame").click({ position: { x: 600, y: 10 } });
  await expect.poll(() => width(page, ".local-rail")).toBe(56);

  // Pinned again, it stands beside the page.
  await page.mouse.move(28, 300);
  await pin(page).click();
  await expect(lane(page)).toHaveAttribute("data-rail", "pinned");
  await page.mouse.move(800, 400);
  await expect.poll(async () => (await panel.boundingBox())!.x).toBe(pinnedLeft);
});

test("keeps the narrow window's sidebar folded, with no pin", async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 800 });
  await page.goto(await launchURL());
  await expect(lane(page)).toHaveAttribute("data-rail", "folded");
  await expect.poll(() => width(page, ".local-rail")).toBe(56);
  await page.mouse.move(28, 300);
  await expect(lane(page)).toHaveAttribute("data-rail", "open");
  await expect(pin(page)).toBeHidden();
});

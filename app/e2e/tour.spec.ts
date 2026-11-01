import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

// Pictures of each stop, light and dark, are written to
// test-results/tour-screens when asked for (TOUR_SCREENSHOTS=1).
const pictures = !!process.env.TOUR_SCREENSHOTS;

/** The daemon's record of whether the tour was seen, read or replaced. */
const tourState = (page: Page, value?: { seen: boolean }) =>
  page.evaluate(async (value) => {
    const read = await (await fetch("api/ui-state/tour", { cache: "no-store" })).json();
    if (!value) return read.value;
    const body = { revision: read.revision, mutation_id: `tour-spec-run:${Date.now()}`, value };
    await fetch("api/ui-state/tour", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
  }, value);

test("the tour walks the Inbox, a pull request and the other pages", async ({ page }) => {
  test.slow();
  await page.goto(await launchURL());
  await expect(page.locator("button.upnext-row").first()).toBeVisible({ timeout: 15000 });
  // Wide, and narrow enough that the review's sections become a sheet.
  const passes = [
    { scheme: "light", width: 1280 },
    { scheme: "light", width: 1100 },
    ...(pictures ? [{ scheme: "dark", width: 1280 }] : []),
  ] as const;
  for (const { scheme, width } of passes) {
    await page.emulateMedia({ colorScheme: scheme });
    await page.setViewportSize({ width, height: 900 });
    const start = new URL(page.url());
    start.search = "?tour=1";
    await page.goto(start.href);
    const card = page.getByRole("dialog", { name: "Welcome to baum" });
    await expect(card).toBeVisible();
    const titles: string[] = [];
    for (let stop = 1; ; stop++) {
      const tour = page.locator(".tour-card");
      titles.push((await tour.locator("h2").textContent()) ?? "");
      await expect(tour).toBeInViewport();
      // Every stop but the first and last points at what it talks about.
      if (stop > 1 && titles.at(-1) !== "That's the tour")
        await expect(page.locator(".tour-spot"), titles.at(-1)).toBeVisible();
      if (pictures && width === 1280) {
        await page.waitForTimeout(400);
        await page.screenshot({
          path: `test-results/tour-screens/${String(stop).padStart(2, "0")}-${scheme}.png`,
        });
      }
      if (titles.at(-1) === "Use the keys") {
        // The keys it teaches reach the page.
        await page.keyboard.press("?");
        await expect(page.locator("dialog[data-shortcuts-sheet]")).toBeVisible();
        await page.keyboard.press("Escape");
        await expect(tour).toBeVisible();
      }
      const next = tour.getByRole("button", { name: /^(Start|Next|Done)$/ });
      if ((await next.textContent()) === "Done") {
        await next.click();
        break;
      }
      await next.click();
      if (titles.at(-1) === "Open a pull request") await expect(page).toHaveURL(/[?&]pr=\d+/);
    }
    await expect(page.locator(".tour-card")).toHaveCount(0);
    expect(titles).toContain("Start with the summary");
    expect(titles.at(-1)).toBe("That's the tour");
  }
});

test("the tour shows on the first launch only, and Go to opens it again", async ({ page }) => {
  await page.goto(await launchURL());
  const rows = page.locator("button.upnext-row").first();
  await expect(rows).toBeVisible({ timeout: 15000 });
  const card = page.locator(".tour-card");
  await tourState(page, { seen: false });
  try {
    await page.reload();
    await expect(card).toContainText("Welcome to baum");
    await card.getByRole("button", { name: "Skip" }).click();
    await expect(card).toHaveCount(0);
    await expect.poll(() => tourState(page)).toEqual({ seen: true });
    await page.reload();
    await expect(rows).toBeVisible();
    await expect(card).toHaveCount(0);
    // Twice: skipping a replay leaves nothing in the way of the next one.
    for (let replay = 0; replay < 2; replay++) {
      await page.keyboard.press("ControlOrMeta+k");
      await page.keyboard.type("Take the tour");
      await page.keyboard.press("Enter");
      await expect(card).toContainText("Welcome to baum");
      await card.getByRole("button", { name: replay ? "Close the tour" : "Skip" }).click();
      await expect(card).toHaveCount(0);
      await expect(page).not.toHaveURL(/[?&]tour=/);
    }
  } finally {
    // The specs that follow expect no tour.
    await tourState(page, { seen: true });
  }
});

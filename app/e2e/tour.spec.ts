import { expect, test } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

// Pictures of each stop, light and dark, are written to
// test-results/tour-screens when asked for (TOUR_SCREENSHOTS=1).
const pictures = !!process.env.TOUR_SCREENSHOTS;

test("the tour walks the Inbox, a pull request and the other pages", async ({ page }) => {
  test.slow();
  await page.goto(await launchURL());
  await expect(page.locator("button.upnext-row").first()).toBeVisible({ timeout: 15000 });
  for (const scheme of pictures ? (["light", "dark"] as const) : (["light"] as const)) {
    await page.emulateMedia({ colorScheme: scheme });
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
      if (pictures) {
        await page.waitForTimeout(400);
        await page.screenshot({
          path: `test-results/tour-screens/${String(stop).padStart(2, "0")}-${scheme}.png`,
        });
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
    expect(await page.evaluate(() => localStorage.getItem("baum.tour.v1"))).toBe("done");
  }
});

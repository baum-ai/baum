import { mkdir } from "node:fs/promises";
import { expect, test } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

// One page of the app against the browser fixture, light and dark, for a
// pull request's description or review page: `just local_app::shot`. It runs
// only when asked for (SHOT_ROUTE) and writes outside test-results, which
// every Playwright run empties.
//
//   SHOT_ROUTE     the app route after the launch URL, such as
//                  "?page=settings&section=reviews"
//   SHOT_SELECTOR  optional: a CSS selector; the picture is that element
//   SHOT_WAIT      optional: a selector to wait for (default SHOT_SELECTOR,
//                  else the app's main landmark)
//   SHOT_NAME      the file name before -light.png and -dark.png ("shot")
//   SHOT_OUT       the directory, relative to oss/app ("screenshots")
//   SHOT_WIDTH, SHOT_HEIGHT  the viewport (1280×800)
const route = process.env.SHOT_ROUTE ?? "";
test.skip(!route, "screenshots are taken on request");

test("a screenshot of one page", async ({ page }) => {
  test.slow();
  const selector = process.env.SHOT_SELECTOR || "";
  const wait = process.env.SHOT_WAIT || selector || "main";
  const name = process.env.SHOT_NAME || "shot";
  const out = process.env.SHOT_OUT || "screenshots";
  await mkdir(out, { recursive: true });
  await page.setViewportSize({
    width: Number(process.env.SHOT_WIDTH) || 1280,
    height: Number(process.env.SHOT_HEIGHT) || 800,
  });
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
    await page.goto(await launchURL());
    await expect(page.locator("main").first()).toBeVisible({ timeout: 30000 });
    await page.goto(new URL(route, page.url()).toString());
    const target = page.locator(wait).first();
    await expect(target).toBeVisible({ timeout: 30000 });
    await page.waitForLoadState("networkidle");
    const path = `${out}/${name}-${scheme}.png`;
    if (selector) await page.locator(selector).first().screenshot({ path, animations: "disabled" });
    else await page.screenshot({ path, animations: "disabled" });
    console.log(`wrote oss/app/${path}`);
  }
});

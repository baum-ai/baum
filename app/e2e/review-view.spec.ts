import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { REVIEW_1303, VIEW_1303 } from "../src/review-view/fixture-1303";
import { launchURL } from "./inbox-helpers";

// The review page's overview tabs (`?view=tiles`) on the fixture's acme/demo#7,
// whose tile route answers with the review of #1303 and its view, as the
// daemon serves them. REVIEW_VIEW_SHOTS=<dir> also writes a picture of each
// tab at each width, light and dark.

const PR7 = "repository=acme%2Fdemo&pr=7&view=tiles";
const SHOTS = process.env.REVIEW_VIEW_SHOTS ?? "";

async function open(page: Page, search: string) {
  await page.route(/\/api\/tiles\?/, (route) =>
    route.fulfill({ json: { document: REVIEW_1303, tiles: null, view: VIEW_1303 } }),
  );
  if (!page.url().includes("/s/")) {
    await page.goto(await launchURL());
    await page.waitForURL(/\/s\//);
  }
  await page.goto(`${new URL(page.url()).pathname}?${search}`);
}

const overflows = (page: Page) =>
  page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);

function watchErrors(page: Page) {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    if (m.type() === "error" && !m.text().includes("Failed to load resource"))
      errors.push(m.text());
  });
  return errors;
}

const PLACES = [
  ["overview", ""],
  ["sections", "&pane=section&section=sec_allowlist"],
  ["context", "&pane=section&section=context"],
  ["encryption", "&pane=section&section=sec_encryption"],
  ["code", "&pane=tile&tile=code&tile_item=oss%2Finternal%2Fuiserver%2Fremote.go"],
] as const;

test("the tabs switch, keep their place in the address and survive a reload", async ({ page }) => {
  const errors = watchErrors(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, PR7);
  const tabs = page.getByRole("tablist", { name: "Review" });
  await expect(tabs.getByRole("tab", { name: "Overview" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await expect(page.getByRole("heading", { name: "How a phone reaches the daemon" })).toBeVisible();
  await tabs.getByRole("tab", { name: "How it works" }).click();
  await expect(page).toHaveURL(/section=sec_pairing/);
  await page.getByRole("button", { name: "What a phone can do" }).first().click();
  await expect(page.getByRole("heading", { name: "What a phone can reach" })).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: "What a phone can reach" })).toBeVisible();
  await tabs.getByRole("tab", { name: "Code" }).click();
  await expect(page).toHaveURL(/tile=code/);
  await page.goBack();
  await expect(page).toHaveURL(/section=sec_allowlist/);
  expect(errors).toEqual([]);
});

for (const width of [390, 900, 1440]) {
  test(`at ${width}px no tab scrolls the page sideways`, async ({ page }) => {
    // A picture shows more of the page than the check needs.
    if (SHOTS) test.setTimeout(120_000);
    await page.setViewportSize({ width, height: SHOTS ? 1800 : 900 });
    for (const [name, place] of PLACES) {
      await open(page, `${PR7}${place}`);
      await expect(page.getByRole("tablist", { name: "Review" })).toBeVisible();
      await expect(page.locator('#review-section [role="tabpanel"]:not([hidden])')).toBeVisible();
      expect(await overflows(page), `${name} at ${width}px`).toBe(false);
      if (!SHOTS) continue;
      mkdirSync(SHOTS, { recursive: true });
      for (const scheme of ["light", "dark"] as const) {
        await page.emulateMedia({ colorScheme: scheme });
        // The pane fades in as it arrives.
        await page.waitForTimeout(400);
        await page.screenshot({
          path: join(SHOTS, `${name}-${width}-${scheme}.png`),
          fullPage: true,
        });
      }
      await page.emulateMedia({ colorScheme: "light" });
    }
  });
}

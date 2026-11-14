import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, test } from "@playwright/test";
import {
  detail,
  everyTile,
  frameBox,
  frameReady,
  open,
  servePatch,
  SHOTS,
  watchErrors,
} from "./helpers";

// The well-behaved custom tile (widget_health) on the real tiles page: its
// card and detail drawn by the host in sandboxed frames from the daemon's
// frame route, at three widths in both themes, its detail sized by the
// frame, its citations leading to the page's own places, and the unavailable
// tile made for a newer kit.

const WIDTHS = [1440, 900, 390] as const;
const SCHEMES = ["light", "dark"] as const;

const widget = (page: import("@playwright/test").Page) =>
  page.frameLocator('[data-custom-tile="widget_health"] iframe');

for (const width of WIDTHS)
  for (const scheme of SCHEMES)
    test(`the custom tile's card and detail at ${width} in ${scheme}`, async ({ page }) => {
      const errors = watchErrors(page);
      await servePatch(page);
      await page.setViewportSize({ width, height: 900 });
      await page.emulateMedia({ colorScheme: scheme });
      await open(page, everyTile);
      const card = page.locator('article[data-tile="widget_health"]');
      await card.scrollIntoViewIfNeeded();
      await frameReady(page, "widget_health");
      // The host names the tile outside the frame: its kind, title and
      // headline, and the frame's own title for assistive technology.
      await expect(card.locator("header")).toContainText("Widget health");
      await expect(card.getByRole("heading", { name: "Widget health" })).toBeVisible();
      await expect(card).toContainText("One store added, one changed");
      await expect(card.locator("iframe")).toHaveAttribute("title", "Widget health");
      await expect(card.locator("iframe")).toHaveAttribute("tabindex", "-1");
      // The frame drew the tile's data in the app's theme.
      await expect(widget(page).locator("body")).toContainText("2 stores changed");
      await expect(widget(page).locator("html")).toHaveAttribute("data-theme", scheme);
      // The glance's box was reserved before it drew, and nothing scrolls
      // sideways.
      const box = await frameBox(page, "widget_health").boundingBox();
      expect(box!.height).toBeGreaterThanOrEqual(112);
      expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(
        false,
      );
      if (SHOTS) {
        mkdirSync(SHOTS, { recursive: true });
        await card.screenshot({ path: join(SHOTS, `card-${width}-${scheme}.png`) });
        await page.screenshot({ path: join(SHOTS, `landing-${width}-${scheme}.png`) });
      }

      // The card opens the detail, which the frame sizes to its content.
      await card.locator("[data-block-open]").click();
      await expect(page).toHaveURL(/tile=widget_health/);
      await frameReady(page, "widget_health");
      const pane = page.locator('[data-custom-tile="widget_health"]');
      await expect(widget(page).locator("body")).toContainText("widget_tags");
      // The cited code, read from the page's patch route, as the viewer's own
      // cited code is: the frame never reads anything itself.
      await expect(widget(page).locator("baum-diff[data-state=ok] tr[data-kind=add]")).toHaveCount(
        4,
      );
      const content = await widget(page)
        .locator("html")
        .evaluate((html) => Math.ceil(html.getBoundingClientRect().height));
      const height = (await pane.boundingBox())!.height;
      expect(Math.abs(height - content)).toBeLessThanOrEqual(2);
      // No scrollbar inside the frame: it is as tall as what it holds.
      expect(
        await widget(page)
          .locator("html")
          .evaluate((html) => html.scrollHeight <= html.clientHeight + 1),
      ).toBe(true);
      await expect(page.getByRole("heading", { name: "Widget health" })).toBeVisible();
      if (SHOTS) await page.screenshot({ path: join(SHOTS, `detail-${width}-${scheme}.png`) });
      expect(errors).toEqual([]);
    });

test("the detail follows the frame's height and the app's theme live", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.emulateMedia({ colorScheme: "light" });
  await open(page, detail("widget_health"));
  await frameReady(page, "widget_health");
  const pane = frameBox(page, "widget_health");
  const before = (await pane.boundingBox())!.height;
  // A narrower window reflows the tile; the frame asks for its new height.
  await page.setViewportSize({ width: 390, height: 900 });
  await expect
    .poll(async () => (await pane.boundingBox())!.height, { timeout: 10000 })
    .not.toBe(before);
  // ...and settles at the height of what it holds.
  await expect
    .poll(async () => {
      const content = await widget(page)
        .locator("html")
        .evaluate((html) => Math.ceil(html.getBoundingClientRect().height));
      return Math.abs((await pane.boundingBox())!.height - content);
    })
    .toBeLessThanOrEqual(2);
  // The theme follows the app's, without a reload.
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(widget(page).locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(frameBox(page, "widget_health")).toHaveAttribute("data-frame-state", "ready");
});

test("a citation in the frame opens the cited code on the page", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, detail("widget_health"));
  await frameReady(page, "widget_health");
  const url = page.url();
  // The hunk citation's chip, a button in the detail.
  const chip = widget(page).locator("baum-cite button").first();
  await expect(chip).toBeVisible();
  await chip.click();
  await expect(page).not.toHaveURL(url);
  await expect(page).toHaveURL(/pane=file/);
});

test("a citation of another tile opens it", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, detail("widget_health"));
  await frameReady(page, "widget_health");
  await widget(page).locator('baum-cite[data-kind="tile"] button').click();
  await expect(page).toHaveURL(/tile=overview/);
});

test("Tab reaches the detail's frame, and Escape inside it goes back", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, everyTile);
  await page.locator('article[data-tile="widget_health"] [data-block-open]').click();
  await frameReady(page, "widget_health");
  const iframe = frameBox(page, "widget_health").locator("iframe");
  await expect(iframe).toHaveAttribute("tabindex", "0");
  // Focus starts on the heading, never in the frame.
  expect(await page.evaluate(() => document.activeElement?.tagName)).not.toBe("IFRAME");
  for (let step = 0; step < 30; step++) {
    if (await page.evaluate(() => document.activeElement?.tagName === "IFRAME")) break;
    await page.keyboard.press("Tab");
  }
  expect(await page.evaluate(() => document.activeElement?.getAttribute("title"))).toBe(
    "Widget health",
  );
  await page.keyboard.press("Tab");
  await page.keyboard.press("Escape");
  await expect(page).not.toHaveURL(/tile=widget_health/);
  await expect(page.locator('article[data-tile="widget_health"]')).toBeVisible();
});

test("a tile made with a newer kit says so, without a frame", async ({ page }) => {
  await open(page, everyTile);
  const card = page.locator('article[data-tile="newer_kit"]');
  await card.scrollIntoViewIfNeeded();
  await expect(card).toContainText("This tile was made with a newer version of baum.");
  await expect(card.locator("iframe")).toHaveCount(0);
  await open(page, detail("newer_kit"));
  await expect(page.getByText("This tile was made with a newer version of baum.")).toBeVisible();
  if (SHOTS) await page.screenshot({ path: join(SHOTS, "newer-kit-detail.png") });
  await expect(page.locator("iframe")).toHaveCount(0);
});

test("frames mount near the visible area and unmount far from it", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 600 });
  await open(page, everyTile);
  await expect(page.locator('article[data-tile="overview"]')).toBeVisible();
  const last = frameBox(page, "escape_busy");
  /** How far the box is below the bottom of its scrolling area's view. */
  const below = () =>
    last.evaluate((box) => {
      let root: HTMLElement | null = box.parentElement;
      while (root && !/(auto|scroll)/.test(getComputedStyle(root).overflowY))
        root = root.parentElement;
      const bottom = root ? root.getBoundingClientRect().bottom : innerHeight;
      return box.getBoundingClientRect().top - bottom;
    });
  expect(await below()).toBeGreaterThan(2400);
  await expect(last).toHaveAttribute("data-frame-state", "waiting");
  await expect(last.locator("iframe")).toHaveCount(0);
  // Scrolled to, it mounts and draws; scrolled far away again, it goes.
  await last.scrollIntoViewIfNeeded();
  await frameReady(page, "escape_busy");
  await page.locator('article[data-tile="overview"]').scrollIntoViewIfNeeded();
  await expect(last).toHaveAttribute("data-frame-state", "waiting");
  await expect(last.locator("iframe")).toHaveCount(0);
  // Never more than the cap at once.
  expect(await page.locator("[data-custom-tile] iframe").count()).toBeLessThanOrEqual(16);
});

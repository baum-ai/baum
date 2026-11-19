import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, test } from "@playwright/test";
import {
  detail,
  frameBox,
  frameReady,
  open,
  railRow,
  sectionRow,
  servePatch,
  showOnly,
  SHOTS,
  tileOn,
  watchErrors,
} from "./helpers";

// The well-behaved custom tile (widget_health) on the review page: drawn by
// the host in a sandboxed frame from the daemon's frame route, on the How it
// works section the view places it on, at three widths in both themes, sized
// by the frame, its citations leading to the page's own places, and the
// unavailable tile made for a newer kit. The escape probes share its section;
// these tests serve the view without them.

const WIDTHS = [1440, 900, 390] as const;
const SCHEMES = ["light", "dark"] as const;

const widget = (page: import("@playwright/test").Page) =>
  page.frameLocator('[data-custom-tile="widget_health"] iframe');

for (const width of WIDTHS)
  for (const scheme of SCHEMES)
    test(`the custom tile on its section at ${width} in ${scheme}`, async ({ page }) => {
      const errors = watchErrors(page);
      await servePatch(page);
      await showOnly(page, ["widget_health"]);
      await page.setViewportSize({ width, height: 900 });
      await page.emulateMedia({ colorScheme: scheme });
      await open(page, detail("widget_health"));
      // The link to the tile opens How it works on its section.
      await expect(sectionRow(page)).toHaveCount(1);
      const tile = tileOn(page, "widget_health");
      await expect(tile).toBeVisible();
      await frameReady(page, "widget_health");
      // The host names the tile outside the frame, and the frame for
      // assistive technology.
      await expect(tile.getByRole("heading", { name: "Widget health" })).toBeVisible();
      await expect(tile.locator("iframe")).toHaveAttribute("title", "Widget health");
      // The frame drew the tile's data in the app's theme.
      await expect(widget(page).locator("body")).toContainText("widget_tags");
      await expect(widget(page).locator("html")).toHaveAttribute("data-theme", scheme);
      // The cited code, read from the page's patch route, as the viewer's own
      // cited code is: the frame never reads anything itself.
      await expect(widget(page).locator("baum-diff[data-state=ok] tr[data-kind=add]")).toHaveCount(
        4,
      );
      // The frame is as tall as what it holds, with no scrollbar inside it,
      // and nothing scrolls sideways.
      const content = await widget(page)
        .locator("html")
        .evaluate((html) => Math.ceil(html.getBoundingClientRect().height));
      const height = (await frameBox(page, "widget_health").boundingBox())!.height;
      expect(Math.abs(height - content)).toBeLessThanOrEqual(2);
      expect(
        await widget(page)
          .locator("html")
          .evaluate((html) => html.scrollHeight <= html.clientHeight + 1),
      ).toBe(true);
      expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(
        false,
      );
      if (SHOTS) {
        mkdirSync(SHOTS, { recursive: true });
        await tile.screenshot({ path: join(SHOTS, `tile-${width}-${scheme}.png`) });
        await page.screenshot({ path: join(SHOTS, `section-${width}-${scheme}.png`) });
      }
      expect(errors).toEqual([]);
    });

test("the frame follows its height and the app's theme live", async ({ page }) => {
  await showOnly(page, ["widget_health"]);
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

test("a citation in the frame opens the cited code in Files changed", async ({ page }) => {
  await showOnly(page, ["widget_health"]);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, detail("widget_health"));
  await frameReady(page, "widget_health");
  // The hunk citation's chip, a button in the frame.
  const chip = widget(page).locator('baum-cite[data-kind="hunk"] button').first();
  await expect(chip).toBeVisible();
  await chip.click();
  await expect(page).toHaveURL(/tile=code/);
  await expect(railRow(page, "Files changed")).toHaveAttribute("aria-pressed", "true");
});

test("a citation of another tile shows that tile", async ({ page }) => {
  // widget_health cites newer_kit, which stands on the same section.
  await showOnly(page, ["widget_health", "newer_kit"]);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, detail("widget_health"));
  await frameReady(page, "widget_health");
  await widget(page).locator('baum-cite[data-kind="tile"] button').click();
  await expect(page).toHaveURL(/tile=newer_kit/);
  await expect(tileOn(page, "newer_kit")).toBeInViewport();
});

test("Tab reaches the tile's frame, never on its own", async ({ page }) => {
  await showOnly(page, ["widget_health"]);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, detail("widget_health"));
  await frameReady(page, "widget_health");
  const iframe = frameBox(page, "widget_health").locator("iframe");
  await expect(iframe).toHaveAttribute("tabindex", "0");
  // Focus starts on the page, never in the frame.
  expect(await page.evaluate(() => document.activeElement?.tagName)).not.toBe("IFRAME");
  for (let step = 0; step < 60; step++) {
    if (await page.evaluate(() => document.activeElement?.tagName === "IFRAME")) break;
    await page.keyboard.press("Tab");
  }
  expect(await page.evaluate(() => document.activeElement?.getAttribute("title"))).toBe(
    "Widget health",
  );
});

test("a tile made with a newer kit says so, without a frame", async ({ page }) => {
  await showOnly(page, ["newer_kit"]);
  await open(page, detail("newer_kit"));
  const tile = tileOn(page, "newer_kit");
  await expect(tile).toContainText("This tile was made with a newer version of baum.");
  if (SHOTS) await page.screenshot({ path: join(SHOTS, "newer-kit.png") });
  await expect(tile.locator("iframe")).toHaveCount(0);
});

test("an earlier version alone and compared draws its own custom tiles", async ({ page }) => {
  await showOnly(page, ["widget_health"]);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, detail("widget_health"));
  await frameReady(page, "widget_health");
  const versions = page.getByRole("button", { name: "Review version", exact: true });
  await versions.click();
  await page.getByRole("button", { name: "View v1", exact: true }).click();
  await expect(versions).toContainText("v1");
  await frameReady(page, "widget_health");
  await expect(widget(page).locator("body")).toContainText("widget_tags");
  // Compared, each column mounts a frame of its own, and both draw.
  await versions.click();
  await page.getByRole("checkbox", { name: "Compare v1" }).check();
  await expect(page.locator('[data-custom-tile="widget_health"]')).toHaveCount(2);
  await expect(
    page.locator('[data-custom-tile="widget_health"][data-frame-state="ready"]'),
  ).toHaveCount(2, {
    timeout: 20000,
  });
});

test("a custom tile still being written, then written", async ({ page }) => {
  // As a draft serves it while its job runs: pending with no data, then
  // ready once the tile is written.
  let written = false;
  await showOnly(page, ["widget_health"], (tile) => {
    if (!written && tile.id === "widget_health")
      Object.assign(tile, { state: "pending", data: {} });
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, detail("widget_health"));
  const tile = tileOn(page, "widget_health");
  await expect(tile).toContainText("Being written");
  await expect(tile.locator("iframe")).toHaveCount(0);
  written = true;
  await page.reload();
  await frameReady(page, "widget_health");
  await expect(widget(page).locator("body")).toContainText("widget_tags");
});

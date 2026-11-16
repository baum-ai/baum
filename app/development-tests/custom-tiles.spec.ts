import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, test } from "@playwright/test";

// A custom tile's frame through Vite's development proxy: the page (served by
// Vite) mounts the frame, whose document the proxy fetches from the daemon's
// frame route with the browser's fetch metadata. This suite starts the
// fixture with BAUM_APP_CUSTOM_TILES=1.
test("a custom tile's frame loads and draws through the development proxy", async ({ page }) => {
  const credential = JSON.parse(
    await readFile(join(process.env.BAUM_APP_STATE_DIR!, "credential.json"), "utf8"),
  );
  const launch = new URL(credential.url);
  launch.port = "1420";
  await page.goto(launch.href);
  await page.waitForURL(/\/s\//);
  const base = new URL(page.url()).pathname;
  const frame = page.waitForResponse((response) =>
    new URL(response.url()).pathname.startsWith("/tile-frames/"),
  );
  await page.goto(`${base}?repository=acme%2Fdemo&pr=7&view=tiles&pane=tile&tile=widget_health`);
  expect((await frame).status()).toBe(200);
  const box = page.locator('[data-custom-tile="widget_health"]');
  await expect(box).toHaveAttribute("data-frame-state", "ready", { timeout: 20000 });
  await expect(
    page.frameLocator('[data-custom-tile="widget_health"] iframe').locator("body"),
  ).toContainText("widget_tags");
  expect(new URL(page.url()).port).toBe("1420");
});

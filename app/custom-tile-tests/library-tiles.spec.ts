import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { frameReady, open, servePatch, SHOTS, watchErrors } from "./helpers";

// A review generated with tile reviews on (the fixture's acme/demo#13): its
// composition holds two library tiles, a schema change that drops a column
// and an interface change, and two authored tiles, a table and a code
// excerpt. Library tiles are custom tiles of baum's own types: they mount in
// sandboxed frames but are named by their titles, as built-in tiles are.

const PR13 = "repository=acme%2Fdemo&pr=13&view=tiles";
const WIDTHS = [1440, 900, 390] as const;
const SCHEMES = ["light", "dark"] as const;

const card = (page: Page, tile: string) => page.locator(`article[data-tile="${tile}"]`);

test("library and authored tiles on the tiles page", async ({ page }) => {
  const errors = watchErrors(page);
  await servePatch(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, PR13);
  const schema = card(page, "schema_change");
  // The schema change drops a column, so it leads, on the landing page.
  await expect(schema).toBeVisible();
  await frameReady(page, "schema_change");
  // Named as baum's own: its title as the kicker, never "Custom".
  await expect(schema.locator("header")).toContainText("Schema changes");
  await expect(schema.locator("header")).not.toContainText("Custom");
  await expect(schema.locator("iframe")).toHaveAttribute("title", "Schema changes");

  // The authored table shows its rows; its kicker is its type's.
  await open(page, `${PR13}&more=1`);
  const table = card(page, "table_retry_states");
  await expect(table).toContainText("How a failed sync is retried");
  const order = await page
    .locator("article[data-tile]")
    .evaluateAll((cards) => cards.map((c) => c.getAttribute("data-tile")));
  const firstChange = order.findIndex((id) => id?.startsWith("sec_"));
  expect(firstChange).toBeGreaterThan(order.indexOf("schema_change"));
  await expect(table).toContainText("GitHub answers 502");
  await expect(card(page, "code_sync_backoff")).toContainText("func backoff");

  // The rail names the library tiles by their titles too.
  await open(page, `${PR13}&pane=tile&tile=api_change`);
  await expect(page.getByRole("heading", { name: "Interface changes" }).first()).toBeVisible();
  await expect(page.getByText("Custom · Interface changes")).toHaveCount(0);

  // A change whose code they cite lists them under "Also gathered in".
  const answer = page.waitForResponse((r) => r.url().includes("/api/tiles?"));
  await open(page, PR13);
  const body = (await (await answer).json()) as {
    tiles: { tiles: { id: string; type: string; covers?: string[] }[] };
  };
  const cited = new Set(body.tiles.tiles.find((t) => t.id === "schema_change")?.covers ?? []);
  const change = body.tiles.tiles.find(
    (t) => t.type === "change" && (t.covers ?? []).some((hunk) => cited.has(hunk)),
  );
  expect(change).toBeTruthy();
  await open(page, `${PR13}&pane=tile&tile=${change!.id}`);
  const nav = page.getByRole("navigation", { name: "Also on the tiles page" });
  await expect(nav).toContainText("Schema changes");
  await expect(nav).toContainText("How a failed sync is retried");
  await nav.getByRole("button", { name: /Schema changes/ }).click();
  await expect(page).toHaveURL(/tile=schema_change/);

  // The authored table's detail opens on its citations.
  await open(page, `${PR13}&pane=tile&tile=table_retry_states`);
  await expect(page.getByText("The token expired").first()).toBeVisible();
  expect(errors).toEqual([]);
});

for (const width of WIDTHS)
  for (const scheme of SCHEMES)
    test(`library and authored tiles at ${width} in ${scheme}`, async ({ page }) => {
      const errors = watchErrors(page);
      await servePatch(page);
      await page.setViewportSize({ width, height: 900 });
      await page.emulateMedia({ colorScheme: scheme });
      await open(page, `${PR13}&more=1`);
      for (const tile of ["schema_change", "api_change"]) {
        await card(page, tile).scrollIntoViewIfNeeded();
        await frameReady(page, tile);
      }
      await page.evaluate(() => window.scrollTo(0, 0));
      expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(
        false,
      );
      if (SHOTS) {
        mkdirSync(SHOTS, { recursive: true });
        await page.screenshot({
          path: join(SHOTS, `library-landing-${width}-${scheme}.png`),
          animations: "disabled",
        });
        await page.screenshot({
          path: join(SHOTS, `library-page-${width}-${scheme}.png`),
          fullPage: true,
          animations: "disabled",
        });
        for (const tile of ["schema_change", "table_retry_states", "code_sync_backoff"]) {
          await card(page, tile).scrollIntoViewIfNeeded();
          await card(page, tile).screenshot({
            path: join(SHOTS, `library-card-${tile}-${width}-${scheme}.png`),
            animations: "disabled",
          });
        }
        await open(page, `${PR13}&pane=tile&tile=schema_change`);
        await frameReady(page, "schema_change");
        await page.screenshot({
          path: join(SHOTS, `library-detail-${width}-${scheme}.png`),
          animations: "disabled",
        });
        await open(page, `${PR13}&pane=tile&tile=table_retry_states`);
        await expect(page.getByText("The token expired").first()).toBeVisible();
        await page.screenshot({
          path: join(SHOTS, `authored-detail-${width}-${scheme}.png`),
          animations: "disabled",
        });
        await open(page, `${PR13}&pane=tile&tile=code_sync_backoff`);
        await expect(page.getByText("A rate limit").first()).toBeVisible();
        await page.screenshot({
          path: join(SHOTS, `authored-code-detail-${width}-${scheme}.png`),
          animations: "disabled",
        });
      }
      expect(errors).toEqual([]);
    });

// While a review is written, a library tile still being filled holds its
// place as a card being written, without a frame, and its kicker is already
// its title; once filled it draws in that same place.
test("a library tile being filled holds its place", async ({ page }) => {
  let pending = true;
  await page.route(
    (url) => url.pathname.endsWith("/api/tiles"),
    async (route) => {
      const response = await route.fetch();
      const body = await response.json();
      if (pending)
        for (const tile of body.tiles?.tiles ?? [])
          if (tile.id === "schema_change") Object.assign(tile, { state: "pending", data: {} });
      await route.fulfill({ response, json: body });
    },
  );
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, PR13);
  const schema = card(page, "schema_change");
  await expect(schema).toContainText("Writing");
  await expect(schema.locator("header")).toContainText("Schema changes");
  await expect(schema.locator("iframe")).toHaveCount(0);
  const before = await schema.boundingBox();
  pending = false;
  await open(page, PR13);
  await frameReady(page, "schema_change");
  const after = await card(page, "schema_change").boundingBox();
  expect(Math.abs(after!.x - before!.x)).toBeLessThan(2);
  expect(Math.abs(after!.y - before!.y)).toBeLessThan(2);
});

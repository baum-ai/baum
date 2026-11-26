import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, type Page, test } from "@playwright/test";
import {
  frameReady,
  open,
  sectionRow,
  servePatch,
  serveTiles,
  SHOTS,
  tileOn,
  watchErrors,
} from "./helpers";

// A review generated with tile reviews on (the fixture's acme/demo#13): its
// composition holds two library tiles, a schema change that drops a column
// and an interface change, and two authored tiles, a table and a code
// excerpt, each placed on the How it works section whose hunks it covers
// most. Library tiles are custom tiles of baum's own types: they mount in
// sandboxed frames but read as the page's own, never as "Custom tile".

const PR13 = "repository=acme%2Fdemo&pr=13";
const WIDTHS = [1440, 900, 390] as const;
const SCHEMES = ["light", "dark"] as const;

/** The patch of the Go file the interface change cites (an added file of 56
 *  lines), so its detail has code to show. */
async function serveGoPatch(page: Page) {
  const body = [
    "package api",
    "",
    "// DeadLetters lists the jobs that ran out of retries.",
    "func DeadLetters(store Store) ([]Job, error) {",
    '\treturn store.List("dead")',
    "}",
  ];
  const lines = Array.from({ length: 56 }, (_, at) => body[at % body.length]!);
  const patch = ["@@ -0,0 +1,56 @@", ...lines.map((line) => `+${line}`)].join("\n");
  await page.route(
    (url) =>
      url.pathname.endsWith("/api/patch") && url.searchParams.get("path") === "api/deadletters.go",
    (route) => route.fulfill({ json: { patch } }),
  );
}

/** The section a tile stands on, scrolled to the tile. */
const at = (tile: string) => `${PR13}&pane=tile&tile=${tile}`;

test("library and authored tiles on their sections", async ({ page }) => {
  const errors = watchErrors(page);
  await servePatch(page);
  await serveGoPatch(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, at("schema_change"));
  await expect(sectionRow(page)).toHaveCount(1);
  const schema = tileOn(page, "schema_change");
  await expect(schema).toBeVisible();
  await frameReady(page, "schema_change");
  // Named as baum's own: its title, never "Custom".
  await expect(schema.getByRole("heading", { name: "Schema changes" })).toBeVisible();
  await expect(schema).not.toContainText("Custom tile");
  await expect(schema.locator("iframe")).toHaveAttribute("title", "Schema changes");

  await open(page, at("api_change"));
  const api = tileOn(page, "api_change");
  await expect(api.getByRole("heading", { name: "Interface changes" })).toBeVisible();
  await frameReady(page, "api_change");
  await expect(page.getByText("Custom · Interface changes")).toHaveCount(0);
  // The code it cites starts closed; opened, it is coloured as the review's
  // diffs are.
  const frame = page.frameLocator('[data-custom-tile="api_change"] iframe');
  const code = frame.locator(".baum-code");
  await expect(code.first()).toBeHidden();
  await frame.locator("details.kit-details > summary", { hasText: "Code" }).first().click();
  await expect(code.first()).toBeVisible();
  await expect(
    code.filter({ hasText: "func DeadLetters" }).first().locator("span").first(),
  ).toHaveAttribute("style", /--syntax-token-keyword/);

  // The authored table shows its rows and the code it cites.
  await open(page, at("table_retry_states"));
  const table = tileOn(page, "table_retry_states");
  await expect(table.getByRole("heading", { name: "How a failed sync is retried" })).toBeVisible();
  await expect(table).toContainText("GitHub answers 502");
  await expect(page.getByText("The token expired").first()).toBeVisible();

  // The authored code excerpt.
  await open(page, at("code_sync_backoff"));
  await expect(tileOn(page, "code_sync_backoff")).toContainText("func backoff");
  await expect(page.getByText("A rate limit").first()).toBeVisible();
  expect(errors).toEqual([]);
});

for (const width of WIDTHS)
  for (const scheme of SCHEMES)
    test(`library and authored tiles at ${width} in ${scheme}`, async ({ page }) => {
      const errors = watchErrors(page);
      await servePatch(page);
      await page.setViewportSize({ width, height: 900 });
      await page.emulateMedia({ colorScheme: scheme });
      for (const tile of [
        "schema_change",
        "api_change",
        "table_retry_states",
        "code_sync_backoff",
      ]) {
        await open(page, at(tile));
        await expect(tileOn(page, tile)).toBeVisible();
        if (tile === "schema_change" || tile === "api_change") await frameReady(page, tile);
        expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)).toBe(
          false,
        );
        if (SHOTS) {
          mkdirSync(SHOTS, { recursive: true });
          await tileOn(page, tile).screenshot({
            path: join(SHOTS, `library-tile-${tile}-${width}-${scheme}.png`),
            animations: "disabled",
          });
          await page.screenshot({
            path: join(SHOTS, `library-section-${tile}-${width}-${scheme}.png`),
            animations: "disabled",
          });
        }
      }
      expect(errors).toEqual([]);
    });

// While a review is written, a library tile still being filled stands on its
// section as being written, without a frame, already under its title; once
// filled it draws there.
test("a library tile being filled holds its place", async ({ page }) => {
  let pending = true;
  await serveTiles(page, (document) => {
    if (pending)
      for (const tile of document.tiles)
        if (tile.id === "schema_change") Object.assign(tile, { state: "pending", data: {} });
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, at("schema_change"));
  const schema = tileOn(page, "schema_change");
  await expect(schema).toContainText("Being written");
  await expect(schema.getByRole("heading", { name: "Schema changes" })).toBeVisible();
  await expect(schema.locator("iframe")).toHaveCount(0);
  pending = false;
  await open(page, at("schema_change"));
  await frameReady(page, "schema_change");
});

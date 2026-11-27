import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

// Settings › Tile library over the fixture daemon's real library: two saved
// types (seedTileLibrary in the uiserver fixture), "Migration steps" made in
// chat and pinned to acme/demo, and "Interface changes" imported. Each test
// leaves the library as it found it.

const section = (page: Page) => page.getByRole("region", { name: "Tile library", exact: true });
const pane = (page: Page) => page.locator("[data-tile-type-pane]");
const row = (page: Page, name: string) => page.locator(`[data-tile-type="${name}"]`);

async function openLibrary(page: Page) {
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Settings", exact: true }).first().click();
  await page.getByRole("button", { name: /^Tile library/ }).click();
  await expect(section(page)).toBeVisible();
}

/** Waits for the pane's detail preview to draw. A page load forgets its
 *  measured height, so it starts at a placeholder and grows when it draws,
 *  moving Pin to repositories and Versions down: a click on them before
 *  then can land on the preview instead. */
async function previewDrawn(page: Page) {
  const detail = pane(page).locator('[data-preview-frame="detail"]');
  await detail.scrollIntoViewIfNeeded();
  await expect(detail).toHaveAttribute("data-frame-state", "ready", { timeout: 20000 });
}

/** The page context bodies the app sends to `PUT api/console/page`. */
function reportedPages(page: Page) {
  const bodies: Record<string, unknown>[] = [];
  page.on("request", (request) => {
    if (request.method() === "PUT" && request.url().endsWith("/api/console/page"))
      bodies.push(request.postDataJSON() as Record<string, unknown>);
  });
  return bodies;
}

test("lists the saved types and previews one in its frames", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await openLibrary(page);
  await expect(page.getByRole("button", { name: /^Tile library/ })).toContainText("2 saved types");
  await expect(row(page, "migration_steps")).toContainText("Migration steps");
  await expect(row(page, "migration_steps")).toContainText("Made in chat · Pinned to acme/demo");
  await expect(row(page, "migration_steps")).toContainText("Not used in a review yet");
  await expect(row(page, "interface_changes")).toContainText("Imported · Not pinned");

  await row(page, "migration_steps").getByRole("link", { name: "Migration steps" }).click();
  await expect(page).toHaveURL(/section=tiles&type=migration_steps/);
  await expect(
    pane(page).getByRole("heading", { name: "Migration steps", level: 3 }),
  ).toBeVisible();
  // Every declared size's glance and the detail draw in their sandboxed frames.
  const frames = pane(page).locator("[data-preview-frame]");
  await expect(frames).not.toHaveCount(0);
  const count = await frames.count();
  // A frame mounts as it comes near the visible part of the section.
  for (let i = 0; i < count; i++) {
    await frames.nth(i).scrollIntoViewIfNeeded();
    await expect(frames.nth(i)).toHaveAttribute("data-frame-state", "ready", { timeout: 20000 });
  }
  await expect(pane(page).locator("iframe").first()).toHaveAttribute("sandbox", "allow-scripts");
  // Another sample draws in fresh frames.
  const samples = pane(page).getByRole("group", { name: "Sample" }).getByRole("button");
  if ((await samples.count()) > 1) {
    await samples.nth(1).click();
    // The sample's frames replace the last one's as it switches, so the
    // first may go between finding it and scrolling to it.
    await expect(async () => {
      await frames.first().scrollIntoViewIfNeeded({ timeout: 2000 });
    }).toPass({ timeout: 10000 });
    await expect(frames.first()).toHaveAttribute("data-frame-state", "ready", { timeout: 20000 });
  }
  // Back goes to the list.
  await pane(page).getByRole("button", { name: "All tile types" }).click();
  await expect(row(page, "interface_changes")).toBeVisible();
  await expect(page).not.toHaveURL(/type=/);
});

test("renames a type and restores the version before the rename", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1000 });
  await openLibrary(page);
  await row(page, "interface_changes").getByRole("link").click();
  await pane(page).getByRole("button", { name: "Rename" }).click();
  await pane(page).getByRole("textbox", { name: "Title" }).fill("Public interfaces");
  await pane(page).getByRole("textbox", { name: "Title" }).press("Enter");
  await expect(
    pane(page).getByRole("heading", { name: "Public interfaces", level: 3 }),
  ).toBeVisible();
  const versions = pane(page).locator("[data-version]");
  await expect(versions).toHaveCount(2);
  await expect(versions.first()).toContainText("Current");
  // A reload keeps the type open, renamed.
  await page.reload();
  await expect(
    pane(page).getByRole("heading", { name: "Public interfaces", level: 3 }),
  ).toBeVisible();
  await previewDrawn(page);
  await versions
    .nth(1)
    .getByRole("button", { name: /^Restore/ })
    .click();
  await expect(
    pane(page).getByRole("heading", { name: "Interface changes", level: 3 }),
  ).toBeVisible();
  // The restored version is current again, and the newest.
  await expect(versions).toHaveCount(2);
  await expect(versions.first()).toContainText("Current");
});

test("pins a type to a repository and unpins it", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1000 });
  await openLibrary(page);
  await row(page, "migration_steps").getByRole("link").click();
  await previewDrawn(page);
  const demo = pane(page).getByRole("switch", { name: "acme/demo" });
  const other = pane(page).getByRole("switch", { name: "acme/other" });
  await expect(demo).toBeChecked();
  await expect(other).not.toBeChecked();
  await other.check();
  await expect(other).toBeChecked();
  // Written into the repository's own settings.
  const pinned = await page.evaluate(async () => {
    const list = (await (await fetch("api/repositories", { cache: "no-store" })).json()) as {
      repository: string;
      settings: { tiles?: { types?: string[] } };
    }[];
    return list.find((r) => r.repository === "acme/other")?.settings.tiles?.types ?? [];
  });
  expect(pinned).toEqual(["migration_steps"]);
  await other.uncheck();
  await expect(other).not.toBeChecked();
  await pane(page).getByRole("button", { name: "All tile types" }).click();
  await expect(row(page, "migration_steps")).toContainText("Pinned to acme/demo");
});

test("Edit in chat opens the session panel about the open type", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  const reported = reportedPages(page);
  await openLibrary(page);
  // From the row's menu, as from the pane.
  await row(page, "migration_steps").hover();
  await row(page, "migration_steps")
    .getByRole("button", { name: "Actions for Migration steps" })
    .click();
  await page.getByRole("menuitem", { name: "Edit in chat" }).click();
  await expect(page.locator("section.session-panel")).toBeVisible();
  await expect(page.getByTestId("session-panel-page")).toHaveText(
    "Settings · Tile library · migration_steps",
  );
  await expect
    .poll(() => reported.at(-1))
    .toEqual({ page: "settings", section: "tiles", focus: { type: "migration_steps" } });
  await expect(pane(page)).toBeVisible();
});

test("deletes a type and brings it back with Undo", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 1000 });
  await openLibrary(page);
  await row(page, "interface_changes").getByRole("link").click();
  await pane(page).getByRole("button", { name: "Delete" }).click();
  await expect(page.getByText("Deleted Interface changes")).toBeVisible();
  await expect(row(page, "interface_changes")).toHaveCount(0);
  await expect(page).not.toHaveURL(/type=/);
  await page.getByRole("button", { name: "Undo" }).click();
  await expect(row(page, "interface_changes")).toBeVisible();
  await expect(row(page, "interface_changes")).toContainText("Imported");
});

// One state directory stands for two computers: the type is exported,
// deleted, then imported from its file. The deleted type holds its name
// until it is purged, so the import is renamed, and the dialog says so.
test("exports a type to a file and imports it back", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await openLibrary(page);
  await row(page, "interface_changes").getByRole("link").click();
  const downloading = page.waitForEvent("download");
  await pane(page).getByRole("button", { name: "Export…" }).click();
  const download = await downloading;
  expect(download.suggestedFilename()).toBe("interface_changes.baumtile.json");
  const path = await download.path();
  const file = JSON.parse(await (await import("node:fs/promises")).readFile(path, "utf8")) as {
    schema: string;
    definition: Record<string, unknown>;
  };
  expect(file.schema).toBe("baum.tile-type/1");
  expect(file.definition.digest).toBeUndefined();

  await pane(page).getByRole("button", { name: "Delete" }).click();
  await expect(row(page, "interface_changes")).toHaveCount(0);
  try {
    await section(page).locator("input[data-tile-type-file]").setInputFiles(path);
    const dialog = page.getByRole("dialog", { name: "Import Interface changes" });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText("saved as interface_changes_2");
    await expect(dialog).toContainText("Given to the review agent when this type is filled.");
    await expect(dialog).toContainText("**/api/**");
    const frames = dialog.locator("[data-preview-frame]");
    await expect(frames.first()).toHaveAttribute("data-frame-state", "ready", { timeout: 20000 });
    await dialog.getByRole("button", { name: "Import", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page).toHaveURL(/type=interface_changes_2/);
    await expect(pane(page)).toContainText("Imported");
    // The app is open and draws the samples, so the import's render check ran.
    await expect(pane(page)).not.toContainText("Not checked yet");
    await pane(page).locator("[data-preview-frame]").first().scrollIntoViewIfNeeded();
    await expect(pane(page).locator("[data-preview-frame]").first()).toHaveAttribute(
      "data-frame-state",
      "ready",
      { timeout: 20000 },
    );
  } finally {
    await page.evaluate(async () => {
      const json = {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        body: "{}",
      };
      await fetch("api/tile-types/interface_changes_2", json);
      await fetch("api/tile-types/interface_changes/undelete", { ...json, method: "POST" });
    });
  }
});

// Pictures of the library page for review, taken on request
// (TILE_LIBRARY_SCREENSHOTS=1): the list and a type's pane, light and dark,
// wide and narrow, into test-results/tile-library.
test("screens", async ({ page }) => {
  test.skip(!process.env.TILE_LIBRARY_SCREENSHOTS, "screenshots are taken on request");
  test.setTimeout(120000);
  for (const scheme of ["light", "dark"] as const) {
    for (const width of [1440, 420]) {
      await page.emulateMedia({ colorScheme: scheme });
      await page.setViewportSize({ width, height: 1000 });
      await openLibrary(page);
      await expect(row(page, "migration_steps")).toBeVisible();
      await page.screenshot({
        path: `test-results/tile-library/list-${width}-${scheme}.png`,
        animations: "disabled",
      });
      await row(page, "migration_steps").getByRole("link").click();
      // Tall enough for the pane's previews; Settings scrolls its own column.
      await page.setViewportSize({ width, height: 2600 });
      const frames = pane(page).locator("[data-preview-frame]");
      await expect(frames.first()).toHaveAttribute("data-frame-state", "ready", { timeout: 20000 });
      await expect(frames.last()).toHaveAttribute("data-frame-state", "ready", { timeout: 20000 });
      await page.screenshot({
        path: `test-results/tile-library/type-${width}-${scheme}.png`,
        animations: "disabled",
      });
    }
    // The import dialog, over a file of a type already in the library.
    await page.setViewportSize({ width: 1440, height: 1000 });
    await openLibrary(page);
    const file = await page.evaluate(async () =>
      (await fetch("api/tile-types/migration_steps/export", { cache: "no-store" })).text(),
    );
    await section(page)
      .locator("input[data-tile-type-file]")
      .setInputFiles({
        name: "migration_steps.baumtile.json",
        mimeType: "application/json",
        buffer: Buffer.from(file),
      });
    const dialog = page.getByRole("dialog", { name: "Import Migration steps" });
    await expect(dialog.locator("[data-preview-frame]").first()).toHaveAttribute(
      "data-frame-state",
      "ready",
      {
        timeout: 20000,
      },
    );
    await page.screenshot({
      path: `test-results/tile-library/import-1440-${scheme}.png`,
      animations: "disabled",
    });
    await dialog.getByRole("button", { name: "Cancel" }).click();
  }
});

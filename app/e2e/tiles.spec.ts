import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

// The tiles page (`?view=tiles`) on the fixture's saved review of acme/demo#7:
// two versions of the schema's example review.

/** Opens the app's address with `search`, signed in. */
async function open(page: Page, search: string) {
  if (!page.url().includes("/s/")) {
    await page.goto(await launchURL());
    await page.waitForURL(/\/s\//);
  }
  await page.goto(`${new URL(page.url()).pathname}?${search}`);
}

const PR7 = "repository=acme%2Fdemo&pr=7&view=tiles";

/** Whether the document scrolls sideways. */
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

test("the landing page leads to each tile and back", async ({ page }) => {
  const errors = watchErrors(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, PR7);
  const strip = page.getByRole("region", { name: "Review at a glance" });
  await expect(strip).toBeVisible();
  await expect(strip.getByRole("group", { name: "Risk score" })).toContainText("/5");
  const cards = page.locator("article[data-tile]");
  await expect(cards.first()).toHaveAttribute("data-tile", "overview");
  await expect(cards.nth(1)).toHaveAttribute("data-tile", "risks");
  // The landing grid is the way in and takes the width: the rail and the
  // side pane are folded to their strips, and the grid has three columns.
  await expect(page.getByRole("button", { name: "Expand sections" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Expand side pane" })).toBeVisible();
  const [third, next] = await Promise.all([cards.nth(2).boundingBox(), cards.nth(3).boundingBox()]);
  const [overview, risks] = await Promise.all([
    cards.nth(0).boundingBox(),
    cards.nth(1).boundingBox(),
  ]);
  expect(Math.abs(third!.y - next!.y)).toBeLessThan(2);
  expect(third!.width).toBeLessThan(overview!.width * 0.75);
  expect(Math.abs(overview!.y - risks!.y)).toBeLessThan(2);

  // The tiles past the landing area, then the obsolete change, stay open
  // across a tile and Back.
  await page.getByRole("button", { name: /^Show \d+ more tiles/ }).click();
  await expect(page).toHaveURL(/more=1/);
  await page.getByRole("button", { name: /^Show 1 more obsolete change/ }).click();
  await expect(page).toHaveURL(/expand=obsolete/);
  await expect(page.locator('article[data-tile="decisions"]')).toBeVisible();

  // A card opens its tile; the rail unfolds to step between tiles.
  await page.locator('article[data-tile="evidence"] [data-block-open]').click();
  await expect(page).toHaveURL(/pane=tile&tile=evidence/);
  await expect(page.getByRole("heading", { name: "Evidence", level: 2 })).toBeVisible();
  await expect(page.getByRole("button", { name: "Collapse sections" })).toBeVisible();
  // One row is the place, whatever the pointer passed over.
  await expect(
    page.locator('#review-rail [aria-pressed="true"][aria-controls~="review-section"]'),
  ).toHaveText(/Evidence/);
  await expect(page.locator('#review-rail [aria-pressed="true"]')).toHaveCount(1);
  // → steps to the next tile in the layout's order.
  await page.keyboard.press("ArrowRight");
  await expect(page).toHaveURL(/tile=media/);
  // Escape goes back to the landing page as the reader left it.
  await page.keyboard.press("Escape");
  await expect(page).not.toHaveURL(/pane=tile/);
  await expect(page).toHaveURL(/more=1/);
  await expect(page.locator('article[data-tile="decisions"]')).toBeVisible();
  // Back walks the tiles again, and then the landing page as it was.
  await page.goBack();
  await expect(page).toHaveURL(/tile=media/);
  await page.goBack();
  await expect(page).toHaveURL(/tile=evidence/);
  await page.goBack();
  await expect(page).not.toHaveURL(/pane=tile/);
  await expect(page.locator('article[data-tile="decisions"]')).toBeVisible();

  // j and k move through the cards and Enter opens one; u comes back to it.
  await open(page, PR7);
  await expect(cards.first()).toBeVisible();
  for (let i = 0; i < 5; i++) await page.keyboard.press("j");
  const active = page.locator("article[data-tile][data-review-block-active]");
  await expect(active).toHaveCount(1);
  const id = await active.getAttribute("data-tile");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(new RegExp(`tile=${id}`));
  await page.keyboard.press("u");
  await expect(page.locator(`article[data-tile="${id}"]`)).toHaveAttribute(
    "data-review-block-active",
    "",
  );
  await expect(page.locator(`article[data-tile="${id}"] [data-block-open]`)).toBeFocused();

  // An old link to a section opens its tile.
  await open(page, `${PR7}&pane=section&section=sec_retry`);
  await expect(
    page.getByRole("heading", { name: "Retry policy and backoff" }).first(),
  ).toBeVisible();
  await expect(
    page.locator('#review-rail [aria-pressed="true"][aria-controls~="review-section"]'),
  ).toHaveText(/Retry policy and backoff/);
  expect(errors).toEqual([]);
});

test("an earlier version and a comparison draw their own tiles", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, PR7);
  await expect(page.locator("article[data-tile]").first()).toBeVisible();
  const versions = page.getByRole("button", { name: "Review version", exact: true });
  await versions.click();
  await page.getByRole("button", { name: "View v1", exact: true }).click();
  await expect(page.getByRole("group", { name: "Version" })).toContainText("Review v1");
  await expect(page.locator("article[data-tile]").first()).toBeVisible();
  // Compared, each version draws its own landing page.
  await versions.click();
  await page.getByRole("checkbox", { name: "Compare v1" }).check();
  await expect(page.getByRole("region", { name: "Review at a glance" })).toHaveCount(2);
});

test("a comment on a tile is kept over a reload", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, `${PR7}&pane=tile&tile=evidence`);
  await expect(page.getByRole("heading", { name: "Evidence", level: 2 })).toBeVisible();
  const count = page.locator("[data-review-button] .review-count").first();
  const before = Number((await count.count()) ? await count.textContent() : 0);
  await page.keyboard.press("c");
  await expect(page.getByLabel("Comment on Evidence")).toBeFocused();
  await page.keyboard.type("Is the load run coming?");
  await page.keyboard.press("Control+Enter");
  await expect(count).toHaveText(String(before + 1));
  await open(page, PR7);
  await expect(page.locator('article[data-tile="evidence"]')).toContainText("1");
  await expect(
    page.locator('article[data-tile="evidence"]').getByTitle("1 pending comment"),
  ).toBeVisible();
  // Leave the fixture's review as it was: without this test's comment.
  await page.evaluate(async () => {
    const url = "api/ui-state/review?key=acme%2Fdemo%237";
    const { revision, value } = await (await fetch(url, { cache: "no-store" })).json();
    await fetch(url, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        revision,
        mutation_id: "tiles-e2e",
        value: { ...value, comments: [] },
      }),
    });
  });
});

test("a tile that cannot be drawn, and a review that cannot be tiled", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.route("**/api/tiles?*", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.tiles.tiles.push({
      id: "custom_one",
      type: "custom",
      title: "A custom tile",
      data: {},
      state: "unavailable",
      problems: ["Custom tiles aren't supported in this version"],
    });
    body.tiles.layout.entries.splice(2, 0, { tile: "custom_one", size: "third" });
    body.tiles.layout.landing += 1;
    await route.fulfill({ response, json: body });
  });
  await open(page, PR7);
  await expect(page.locator('article[data-tile="custom_one"]')).toContainText(
    "Custom tiles aren't supported in this version",
  );
  await expect(page.locator('article[data-tile="risks"]')).toBeVisible();
  await page.unroute("**/api/tiles?*");
  await page.route("**/api/tiles?*", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({ response, json: { ...body, tiles: null, problem: "broken" } });
  });
  await open(page, PR7);
  await expect(page.getByText("This review can't be shown as tiles")).toBeVisible();
  await expect(page.locator("article[data-tile]")).toHaveCount(0);
});

for (const width of [390, 800]) {
  test(`at ${width}px the page stacks without scrolling sideways`, async ({ page }) => {
    await page.setViewportSize({ width, height: 844 });
    await open(page, `${PR7}&more=1&expand=changes`);
    const cards = page.locator("article[data-tile]");
    await expect(cards.first()).toBeVisible();
    expect(await overflows(page)).toBe(false);
    // The same order, one column on a phone and two at 800px.
    const [first, second] = await Promise.all([
      cards.nth(0).boundingBox(),
      cards.nth(1).boundingBox(),
    ]);
    if (width === 390) expect(second!.y).toBeGreaterThan(first!.y + first!.height - 1);
    else expect(Math.abs(second!.y - first!.y)).toBeLessThan(2);
    await cards.nth(1).locator("[data-block-open]").click();
    await expect(page).toHaveURL(/tile=risks/);
    expect(await overflows(page)).toBe(false);
  });
}

test("changes still being written read as such", async ({ page }) => {
  // As a retry of details or a draft serves them: the outline of a change
  // without its details, and the context still to come.
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.route("**/api/tiles?*", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    for (const change of body.tiles.changes)
      if (change.id === "sec_retry") change.details = "pending";
    for (const tile of body.tiles.tiles) {
      if (tile.id === "sec_retry") tile.state = "pending";
      if (tile.id === "context") Object.assign(tile, { state: "pending", data: {} });
    }
    await route.fulfill({ response, json: body });
  });
  await open(page, PR7);
  await expect(page.getByRole("group", { name: "This version" })).toContainText(
    "Writing details for 1 of",
  );
  await expect(page.locator('article[data-tile="sec_retry"]')).toContainText("Writing details…");
  await expect(page.locator('article[data-tile="risks"]')).toContainText(
    "1 change still being written",
  );
  await page.getByRole("button", { name: /^Show \d+ more tiles/ }).click();
  await expect(page.locator('article[data-tile="context"]')).toContainText("Writing");
});

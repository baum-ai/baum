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
  // Escape took the place of the tile it left: Back finds the tile stepped
  // from, and then the landing page as it was.
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

test("the keys never drop to the page, and Back does not reopen a tile", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  // A link to a tile, with nothing remembered: Escape gives the keys to that
  // tile's card on the landing page.
  await open(page, `${PR7}&pane=tile&tile=evidence`);
  await expect(page.getByRole("heading", { name: "Evidence", level: 2 })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page).not.toHaveURL(/pane=tile/);
  await expect(page.locator('article[data-tile="evidence"] [data-block-open]')).toBeFocused();
  // A link to an item: brought into view, marked and given the keys.
  await open(page, `${PR7}&pane=tile&tile=risks&tile_item=rsk_api_header`);
  await expect(page.locator("#tile-item-rsk_api_header")).toBeFocused();

  // From the landing page into a tile and back with Escape, which goes back
  // through history: Forward opens the tile again, Back returns.
  await open(page, PR7);
  await page.locator('article[data-tile="risks"] [data-block-open]').click();
  await expect(page).toHaveURL(/tile=risks/);
  await page.keyboard.press("Escape");
  await expect(page).not.toHaveURL(/pane=tile/);
  await page.goForward();
  await expect(page).toHaveURL(/tile=risks/);
  await page.goBack();
  await expect(page).not.toHaveURL(/pane=tile/);
  // Stepping on to another tile, Escape takes the tile's place and comes back
  // to the card of the tile read last; Back then finds the tile stepped from.
  await page.locator('article[data-tile="risks"] [data-block-open]').click();
  await page.keyboard.press("ArrowRight");
  await expect(page).toHaveURL(/tile=sec_api/);
  await page.keyboard.press("Escape");
  await expect(page).not.toHaveURL(/pane=tile/);
  await expect(page.locator('article[data-tile="sec_api"] [data-block-open]')).toBeFocused();
  await page.goBack();
  await expect(page).toHaveURL(/tile=risks/);
});

test("a comment on a card or an item is sent on the line it cites", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  const submitted: { comments: { path: string; line: number; body: string }[] }[] = [];
  await page.route("**/api/prs/review", async (route) => {
    submitted.push(route.request().postDataJSON());
    await route.fulfill({
      json: { url: "https://github.com/acme/demo/pull/7#pullrequestreview-2" },
    });
  });
  // c on the card the keys are on comments on that tile.
  await open(page, PR7);
  await expect(page.locator("article[data-tile]").first()).toBeVisible();
  const active = page.locator("article[data-tile][data-review-block-active]");
  for (let i = 0; i < 4 && (await active.count()) === 0; i++) await page.keyboard.press("j");
  const id = await active.getAttribute("data-tile");
  await page.keyboard.press("c");
  const titles: Record<string, RegExp> = {
    overview: /^Comment on Add retry budgets/,
    risks: /^Comment on Risks/,
  };
  await expect(page.getByLabel(titles[id!] ?? /^Comment on /)).toBeFocused();
  await page.keyboard.press("Escape");
  // An evidence item opened by its link, then c: placed on the code it cites.
  await open(page, `${PR7}&pane=tile&tile=evidence&tile_item=ev_retry_tests`);
  await expect(page.locator("#tile-item-ev_retry_tests")).toBeFocused();
  await page.keyboard.press("c");
  await expect(page.getByLabel(/^Comment on Evidence: Delays double/)).toBeFocused();
  await page.keyboard.type("Is the cap tested at the boundary?");
  await page.keyboard.press("Control+Enter");
  await page.keyboard.press("v");
  await page.getByRole("menuitem", { name: "Comment", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: /Review pull request #7/ });
  await dialog.getByRole("button", { name: "Comment", exact: true }).click();
  await expect.poll(() => submitted.length).toBe(1);
  expect(submitted[0]!.comments).toContainEqual(
    expect.objectContaining({
      body: "Is the cap tested at the boundary?",
      path: expect.stringMatching(/\.go$/),
      line: expect.any(Number),
    }),
  );
});

test("the setting chooses the page, and old links land on their tiles", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, "page=settings");
  const select = page.locator("#review-page");
  await expect(select).toHaveValue("full");
  await select.selectOption("tiles");
  await open(page, "repository=acme%2Fdemo&pr=7");
  await expect(page.locator("article[data-tile]").first()).toBeVisible();
  // A link the full review wrote: a risk opens Risks at it, the Changes
  // overview scrolls the landing page to its group.
  await open(
    page,
    "repository=acme%2Fdemo&pr=7&pane=risk&risk_section=sec_api&risk_id=rsk_api_header",
  );
  await expect(page.locator("#tile-item-rsk_api_header")).toBeFocused();
  await open(page, "repository=acme%2Fdemo&pr=7&pane=changes");
  await expect(page.locator('[data-tile-group="changes"]')).toBeInViewport();
  // Back to the full review, mid-session.
  await open(page, "page=settings");
  await page.locator("#review-page").selectOption("full");
  await open(page, "repository=acme%2Fdemo&pr=7");
  await expect(page.getByRole("heading", { name: "Summary", level: 2 })).toBeVisible();
  await expect(page.locator("article[data-tile]")).toHaveCount(0);
});

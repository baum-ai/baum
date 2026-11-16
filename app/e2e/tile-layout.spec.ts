import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

// The person's own layout of a review's tiles, changed from the session
// panel, on the fixture's acme/demo#14: its scripted agent
// (`panelAgent.layout` in the uiserver fixture) calls the real layout tools
// of baum's MCP server over HTTP, as an agent CLI would, and the page shows
// each change as it lands.

const PR14 = "repository=acme%2Fdemo&pr=14&view=tiles";

async function open(page: Page, search: string) {
  if (!page.url().includes("/s/")) {
    await page.goto(await launchURL());
    await page.waitForURL(/\/s\//);
  }
  await page.goto(`${new URL(page.url()).pathname}?${search}`);
}

const panel = (page: Page) => page.locator("section.session-panel");
const cards = (page: Page) => page.locator("main article[data-tile]");
const card = (page: Page, id: string) => page.locator(`main article[data-tile="${id}"]`);
const strip = (page: Page) => page.getByRole("group", { name: "Your layout" });
const toast = (page: Page) => page.locator(".local-toast");

/** Sends a message to the panel's agent and waits for its answer. */
async function say(page: Page, message: string, answer: string | RegExp) {
  const composer = panel(page).getByRole("textbox", { name: "Message to the agent" });
  await composer.fill(message);
  await composer.press("Enter");
  // The turn runs, then ends with its answer.
  await expect(panel(page).getByRole("button", { name: "Stop", exact: true })).toBeVisible();
  await expect(panel(page).getByRole("button", { name: "Send" })).toBeVisible({ timeout: 20_000 });
  await expect(panel(page).getByRole("region", { name: "Transcript" })).toContainText(answer);
}

/** Opens the review on its generated layout: a layout left by an earlier
 *  test is reset first. */
async function fresh(page: Page) {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, PR14);
  await expect(cards(page).first()).toBeVisible();
  if ((await strip(page).count()) > 0) {
    await strip(page).getByRole("button", { name: "Reset" }).click();
    await expect(strip(page)).toHaveCount(0);
  }
  await expect(cards(page).first()).toHaveAttribute("data-tile", "overview");
}

/** Points at a card as a reader does from the keyboard: j until the card
 *  has the keys, then a, which asks the session panel about that tile. */
async function askAbout(page: Page, id: string) {
  // The panel's composer gives up the keys, as when the reader turns to the
  // page.
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await expect(card(page, id)).toBeVisible();
  for (let step = 0; step < 30; step++) {
    if ((await card(page, id).getAttribute("data-review-block-active")) !== null) break;
    await page.keyboard.press("j");
  }
  await expect(card(page, id)).toHaveAttribute("data-review-block-active", "");
  await page.keyboard.press("a");
  await expect(panel(page)).toBeVisible();
}

test("the session panel lays the tiles out, and undo and reset bring them back", async ({
  page,
}) => {
  test.setTimeout(120_000);
  await fresh(page);

  // "Move the risks to the top": the Risks card leads, the change is said,
  // with Undo, and the band says the layout is the reader's.
  await page.keyboard.press("`");
  await say(page, "Move the risks to the top", "Moved Risks to the top");
  await expect(cards(page).first()).toHaveAttribute("data-tile", "risks");
  await expect(toast(page)).toContainText("Moved Risks to the top");
  await expect(toast(page).getByRole("button", { name: "Undo" })).toBeVisible();
  await expect(strip(page)).toContainText("Your layout");

  // "Make this wider" about the tile the reader pointed at.
  await expect(card(page, "sec_api")).toHaveAttribute("data-tile-size", "third");
  await askAbout(page, "sec_api");
  await expect(panel(page).locator(".session-panel-focus")).toContainText("Operator-only");
  await say(page, "make this wider", "Made Operator-only dead-letter endpoints half width");
  await expect(card(page, "sec_api")).toHaveAttribute("data-tile-size", "half");

  // "Remove this": the card leaves the grid for the Hidden tiles group,
  // one click away.
  await say(page, "remove this", "Removed Operator-only dead-letter endpoints");
  await expect(card(page, "sec_api")).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Show 1 hidden tile/ })).toContainText(
    "Operator-only",
  );

  // "Undo that" in the panel, then the band's Undo: each takes one change
  // back.
  await say(page, "undo that", /Removed Operator-only dead-letter endpoints\. Undo/);
  await expect(card(page, "sec_api")).toBeVisible();
  await strip(page)
    .getByRole("button", { name: /^Undo: Made Operator-only dead-letter endpoints half width/ })
    .click();
  await expect(card(page, "sec_api")).toHaveAttribute("data-tile-size", "third");
  await expect(cards(page).first()).toHaveAttribute("data-tile", "risks");

  // Reset returns to the generated layout; Mod+Z brings the reader's back.
  await strip(page).getByRole("button", { name: "Reset" }).click();
  await expect(cards(page).first()).toHaveAttribute("data-tile", "overview");
  await expect(strip(page)).toHaveCount(0);
  await expect(toast(page)).toContainText("Reset the layout to the generated one");
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await page.keyboard.press("ControlOrMeta+z");
  await expect(cards(page).first()).toHaveAttribute("data-tile", "risks");
  await strip(page).getByRole("button", { name: "Reset" }).click();
  await expect(cards(page).first()).toHaveAttribute("data-tile", "overview");
});

test("a hidden tile comes back with Show, and the next version keeps the edits", async ({
  page,
}) => {
  test.setTimeout(120_000);
  await fresh(page);

  await page.keyboard.press("`");
  await say(page, "Move the risks to the top", "Moved Risks to the top");
  // The docs change, which the next version drops, and the dead-letter
  // store, which it keeps, are removed.
  await askAbout(page, "sec_docs");
  await say(page, "remove this", "Removed Docs and dependency bump");
  await askAbout(page, "sec_deadletter");
  await say(page, "remove this", "Removed Dead-letter store and migration");
  const hidden = page.getByRole("button", { name: /Show 2 hidden tiles/ });
  await expect(hidden).toBeVisible();

  // Show on a hidden card brings it back, as one more change to undo.
  await hidden.click();
  await card(page, "sec_deadletter")
    .getByRole("button", { name: "Show Dead-letter store and migration in your layout again" })
    .click();
  await expect(toast(page)).toContainText("Brought back Dead-letter store and migration");
  await expect(card(page, "sec_deadletter")).toBeVisible();
  // The docs change stays in the opened group, with its Show.
  await expect(
    card(page, "sec_docs").getByRole("button", { name: /^Show Docs and dependency bump/ }),
  ).toBeVisible();

  // A push: the next version has no docs change. The edits carry over, and
  // the one whose tile vanished falls away.
  await writeFile(join(process.env.BAUM_APP_STATE_DIR ?? "", "layout-next-version"), "");
  await expect(page.getByRole("button", { name: /hidden tile/ })).toHaveCount(0, {
    timeout: 20_000,
  });
  await expect(cards(page).first()).toHaveAttribute("data-tile", "risks");
  await expect(card(page, "sec_docs")).toHaveCount(0);
  await expect(card(page, "sec_deadletter")).toBeVisible();
  await expect(strip(page)).toContainText("Your layout");

  // Reset returns to the new version's generated layout.
  await strip(page).getByRole("button", { name: "Reset" }).click();
  await expect(cards(page).first()).toHaveAttribute("data-tile", "overview");
});

test("a library tile added from the panel is filled while the page waits", async ({ page }) => {
  test.setTimeout(120_000);
  await fresh(page);
  await page.keyboard.press("`");
  // The agent adds it without data: the card is there at once, being
  // filled, and shows its data when baum's fill lands.
  await say(page, "Add the API changes tile", "Added Interface changes");
  await expect(card(page, "api_change")).toBeVisible();
  await expect(card(page, "api_change")).toContainText("Filling this tile…");
  await expect(card(page, "api_change").locator("[data-added-note]")).toHaveText(
    "Filling for this version",
  );
  await expect(card(page, "api_change")).not.toContainText("Filling this tile…", {
    timeout: 15_000,
  });
  await expect(card(page, "api_change").locator("[data-added-note]")).toHaveCount(0);
  await expect(card(page, "api_change")).toHaveAttribute("data-tile-type", "custom");
  // Undo takes the tile away again.
  await say(page, "undo that", "Added Interface changes");
  await expect(card(page, "api_change")).toHaveCount(0);
});

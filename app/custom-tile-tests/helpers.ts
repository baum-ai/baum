import { expect, type Page } from "@playwright/test";
import { launchURL } from "../e2e/inbox-helpers";

/** Where pictures go, when they are asked for: CUSTOM_TILE_SHOTS=1. */
export const SHOTS = process.env.CUSTOM_TILE_SHOTS ? "test-results/custom-tiles" : "";

/** The tiles page of acme/demo#7, which the fixture serves with the synthetic
 *  custom tiles when started with BAUM_APP_CUSTOM_TILES=1. */
export const PR7 = "repository=acme%2Fdemo&pr=7&view=tiles";

/** Opens the app's address with `search`, signed in. */
export async function open(page: Page, search: string) {
  if (!page.url().includes("/s/")) {
    await page.goto(await launchURL());
    await page.waitForURL(/\/s\//);
  }
  await page.goto(`${new URL(page.url()).pathname}?${search}`);
}

/** A tile's detail on the tiles page. */
export const detail = (tile: string) => `${PR7}&pane=tile&tile=${tile}`;

/** The landing page with every tile shown, custom tiles included. */
export const everyTile = `${PR7}&more=1`;

/** A custom tile's frame box, as the page draws it. */
export const frameBox = (page: Page, tile: string) => page.locator(`[data-custom-tile="${tile}"]`);

/** Waits for a custom tile's frame to have drawn. */
export async function frameReady(page: Page, tile: string) {
  await expect(frameBox(page, tile)).toHaveAttribute("data-frame-state", "ready", {
    timeout: 20000,
  });
}

/** Page errors and console errors, but the resource loads a frame's own policy
 *  refuses, which the browser reports as console errors of the frame. */
export function watchErrors(page: Page) {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => {
    if (message.type() !== "error") return;
    // A tile's frame reports what its sandbox refuses: the escape probe's
    // attempts, which its own suite checks.
    if (message.location().url.includes("/tile-frames/")) return;
    const text = message.text();
    if (/sandboxed|Unsafe attempt to initiate navigation/.test(text)) return;
    if (
      text.includes("Failed to load resource") ||
      text.includes("Content Security Policy") ||
      text.includes("Content-Security-Policy")
    )
      return;
    errors.push(text);
  });
  return errors;
}

/** The fixture's clone does not hold the example review's files, so the
 *  page's patch route has nothing for the code a tile cites. This answers it
 *  for CHANGELOG.md, the hunk widget_health cites, with a synthetic patch that
 *  matches that hunk (+1,9, four lines added). */
export async function servePatch(page: Page) {
  const patch = [
    "@@ -1,5 +1,9 @@",
    " # Changelog",
    " ",
    "+## Unreleased",
    "+",
    "+- Add retry budgets and a dead-letter queue.",
    "+",
    " ## 0.4.0",
    " ",
    " - Initial release.",
  ].join("\n");
  await page.route(
    (url) => url.pathname.endsWith("/api/patch") && url.searchParams.get("path") === "CHANGELOG.md",
    (route) => route.fulfill({ json: { patch } }),
  );
}

import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

// Pictures of each stop, light and dark, are written to
// test-results/tour-screens when asked for (TOUR_SCREENSHOTS=1).
const pictures = !!process.env.TOUR_SCREENSHOTS;

/** The daemon's record of whether the tour was seen, read or replaced. */
const tourState = (page: Page, value?: { seen: boolean }) =>
  page.evaluate(async (value) => {
    const read = await (await fetch("api/ui-state/tour", { cache: "no-store" })).json();
    if (!value) return read.value;
    const body = { revision: read.revision, mutation_id: `tour-spec-run:${Date.now()}`, value };
    await fetch("api/ui-state/tour", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
  }, value);

/** Walks the tour from start, its address, to Done, checking each stop; at a
 *  stop of `act`'s, `act` runs first. With `keys` it moves on with Enter
 *  rather than the button. Pictures are named after `name`. */
async function walk(
  page: Page,
  start: string,
  name: string,
  act?: Record<string, () => Promise<void>>,
  keys = false,
) {
  await page.goto(start);
  await expect(page.getByRole("dialog", { name: "Your Inbox" })).toBeVisible({
    timeout: 15000,
  });
  const titles: string[] = [];
  for (let stop = 1; ; stop++) {
    const tour = page.locator(".tour-card");
    titles.push((await tour.locator("h2").textContent()) ?? "");
    await expect(tour).toBeInViewport();
    // Every stop points at what it talks about.
    await expect(page.locator(".tour-spot"), titles.at(-1)).toBeVisible();
    // The review's stop takes in its Details sidebar, where one is shown.
    const aside = page.locator("#review-aside");
    if (titles.at(-1) === "A pull request's review" && (await aside.isVisible())) {
      await expect
        .poll(async () => {
          const [spot, side] = await Promise.all([
            page.locator(".tour-spot").boundingBox(),
            aside.boundingBox(),
          ]);
          return !!spot && !!side && spot.x + spot.width >= side.x + side.width;
        })
        .toBe(true);
    }
    // Every stop but the first goes back.
    if (stop > 1) await expect(tour.getByRole("button", { name: "Back" })).toBeVisible();
    if (pictures && name) {
      await page.waitForTimeout(400);
      await page.screenshot({
        path: `test-results/tour-screens/${name}-${String(stop).padStart(2, "0")}.png`,
      });
    }
    await act?.[titles.at(-1)!]?.();
    const next = tour.getByRole("button", { name: /^(Next|Done)$/ });
    const done = (await next.textContent()) === "Done";
    if (keys) await page.keyboard.press("Enter");
    else await next.click();
    if (done) break;
    if (titles.at(-1) === "Your Inbox") await expect(page).toHaveURL(/[?&]pr=\d+/);
  }
  await expect(page.locator(".tour-card")).toHaveCount(0);
  expect(titles).toEqual(["Your Inbox", "A pull request's review", "Timeline", "Sessions"]);
}

/** The address of the app's own data, under its namespace. */
async function home(page: Page) {
  await page.goto(await launchURL());
  await expect(page.locator("button.upnext-row").first()).toBeVisible({ timeout: 15000 });
  return new URL(page.url()).pathname.replace(/sample\/$/, "");
}

test("the tour walks the Inbox, a pull request and the other pages", async ({ page }) => {
  test.slow();
  const base = await home(page);
  // Wide, and narrow enough that the review's sections become a sheet.
  const passes = [
    { scheme: "light", width: 1280 },
    { scheme: "light", width: 1100 },
    ...(pictures ? [{ scheme: "dark", width: 1280 }] : []),
  ] as const;
  for (const { scheme, width } of passes) {
    await page.emulateMedia({ colorScheme: scheme });
    await page.setViewportSize({ width, height: 900 });
    await walk(
      page,
      `${base}?tour=1`,
      width === 1280 ? `own-${scheme}` : "",
      {
        // ← and → step between the cards, wherever the focus is.
        Timeline: async () => {
          const title = page.locator(".tour-card h2");
          await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
          await page.keyboard.press("ArrowLeft");
          await expect(title).toHaveText("A pull request's review");
          await expect(page).toHaveURL(/[?&]pr=\d+/);
          await page.keyboard.press("ArrowRight");
          await expect(title).toHaveText("Timeline");
        },
      },
      width !== 1280,
    );
  }
});

test("with no pull requests of its own, the tour shows the sample project", async ({ page }) => {
  test.slow();
  const base = await home(page);
  for (const scheme of pictures ? (["light", "dark"] as const) : (["light"] as const)) {
    await page.emulateMedia({ colorScheme: scheme });
    await walk(page, `${base}sample/?tour=1`, `sample-${scheme}`, {
      // Its pull requests do not open on GitHub, where they do not exist,
      // and what would change something says it cannot, and nothing changes.
      // The pictures leave those out, as the notice stays on the pages after.
      "Your Inbox": async () => {
        // The tour picks the person's own pull request, written with their agent.
        await expect(page.locator(".upnext-panel")).toContainText("Add tags to notes");
        if (pictures) return;
        // The card covers the panel's link, so the click goes to it directly.
        await page
          .locator(".upnext-panel")
          .getByRole("link", { name: /^Open .* on GitHub$/ })
          .dispatchEvent("click");
        await expect(page.locator(".local-toast")).toContainText("Not in the sample project");
        expect(page.context().pages()).toHaveLength(1);
      },
      ...(pictures
        ? {}
        : {
            "A pull request's review": async () => {
              await page.getByRole("button", { name: "Review version", exact: true }).click();
              await page.getByRole("button", { name: "Regenerate review", exact: true }).click();
              await expect(page.locator(".local-toast")).toContainText("Not in the sample project");
            },
          }),
    });
    // Closing the tour leaves the sample for the app's own data.
    await expect(page).toHaveURL((url) => url.pathname === base);
    await expect(page.locator("button.upnext-row").first()).toBeVisible();
  }
});

test("the tour shows on the first launch only, and Go to opens it again", async ({ page }) => {
  await page.goto(await launchURL());
  const rows = page.locator("button.upnext-row").first();
  await expect(rows).toBeVisible({ timeout: 15000 });
  const card = page.locator(".tour-card");
  await tourState(page, { seen: false });
  try {
    const base = new URL(page.url()).pathname;
    await page.reload();
    // The first tour is the sample's, even with pull requests of its own.
    await expect(card).toContainText("Your Inbox", { timeout: 15000 });
    await expect(page).toHaveURL(/\/sample\//);
    await card.getByRole("button", { name: "Skip" }).click();
    await expect(page).toHaveURL((url) => url.pathname === base);
    await expect(card).toHaveCount(0);
    await expect.poll(() => tourState(page)).toEqual({ seen: true });
    await page.reload();
    await expect(rows).toBeVisible();
    await expect(card).toHaveCount(0);
    // Twice: skipping a replay leaves nothing in the way of the next one.
    for (let replay = 0; replay < 2; replay++) {
      await page.keyboard.press("ControlOrMeta+k");
      await page.keyboard.type("Take the tour");
      await page.keyboard.press("Enter");
      await expect(card).toContainText("Your Inbox");
      await expect(page).not.toHaveURL(/\/sample\//);
      // The second time Escape closes it, wherever the focus is.
      if (replay) {
        await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
        await page.keyboard.press("Escape");
      } else await card.getByRole("button", { name: "Skip" }).click();
      await expect(card).toHaveCount(0);
      await expect(page).not.toHaveURL(/[?&]tour=/);
    }
  } finally {
    // The specs that follow expect no tour.
    await tourState(page, { seen: true });
  }
});

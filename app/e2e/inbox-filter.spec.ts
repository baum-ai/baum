import { expect, test, type Page } from "@playwright/test";
import { deleteAllInboxes, launchURL } from "./inbox-helpers";

/** Clears every page's quick filter through the daemon, so a failed run
 *  leaves none for the specs that follow. */
async function clearFilters(page: Page) {
  try {
    if (!page.url().includes("/s/")) return;
    await page.evaluate(async () => {
      const read = await fetch("api/ui-state/filters", { cache: "no-store" });
      if (!read.ok) return;
      const { revision } = (await read.json()) as { revision: number };
      await fetch("api/ui-state/filters", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          revision,
          mutation_id: `${crypto.randomUUID()}:1`,
          value: { pages: {} },
        }),
      });
    });
  } catch {
    // A page that never loaded set none.
  }
}

test.afterEach(async ({ page }) => {
  await clearFilters(page);
  await deleteAllInboxes(page);
});

// Pictures for a pull request are taken only when asked for
// (FILTER_SCREENSHOTS=1), into test-results/filter-screens.
const shot = async (page: Page, name: string) => {
  if (!process.env.FILTER_SCREENSHOTS) return;
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await page.screenshot({
      path: `test-results/filter-screens/${name}-${scheme}.png`,
      animations: "disabled",
    });
  }
  await page.emulateMedia({ colorScheme: "light" });
};

test("narrows the Inbox with the filter menu, remembers it and saves it as an inbox", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(await launchURL());
  const rows = page.locator(".upnext-list button.upnext-row");
  await expect(rows.first()).toBeVisible({ timeout: 15000 });
  await expect(rows.filter({ hasText: "Reuse the existing PR page" })).toHaveCount(1);

  const filter = page.locator("summary.filter-menu-button");
  await filter.click();
  const menu = page.getByRole("group", { name: "Filter pull requests" });
  await expect(menu.getByText("Organizations and repositories")).toBeVisible();
  await menu.getByLabel(/^Created by me/).check();
  await shot(page, "1-inbox-menu");
  await expect(rows.filter({ hasText: "Reuse the existing PR page" })).toHaveCount(0);
  await expect(page.locator(".filter-menu-chips")).toContainText("Created by me");
  await expect(filter).toHaveAccessibleName("Filter, 1 in force");

  // Closed, the chips say what is in force; a reload keeps it.
  await page.keyboard.press("Escape");
  await shot(page, "2-inbox-chips");
  await page.reload();
  await expect(page.locator(".filter-menu-chips")).toContainText("Created by me");
  await expect(rows.filter({ hasText: "Reuse the existing PR page" })).toHaveCount(0);

  // Save as inbox makes an inbox that holds the same pull requests.
  await filter.click();
  await page.getByRole("button", { name: "Save as inbox…" }).click();
  await expect(page).toHaveURL(/page=inbox-edit&inbox=[0-9a-f]{32}/);
  await expect(page.getByLabel("Name")).toHaveValue("Filtered");
  await page.getByRole("button", { name: "Fine-tune" }).click();
  await expect(page.getByRole("button", { name: /^Created by me/ })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await shot(page, "3-saved-inbox-edit");

  // Back in the Inbox, removing the chip shows everything again.
  await page.getByRole("link", { name: "Inbox", exact: true }).first().click();
  await page.getByRole("button", { name: "Remove Created by me" }).click();
  await expect(page.locator(".filter-menu-chips")).toHaveCount(0);
  await expect(rows.filter({ hasText: "Reuse the existing PR page" })).toHaveCount(1);
  expect(errors).toEqual([]);
});

test("filters Pull requests by involvement, and by a repository named in a link", async ({
  page,
}) => {
  await page.goto(await launchURL());
  await expect(page.locator(".upnext-list button.upnext-row").first()).toBeVisible({
    timeout: 15000,
  });
  await page.goto(page.url().replace(/\?.*$/, "") + "?page=pulls&filter=all");
  await expect(page.getByText("An automatically reviewed change")).toBeVisible();
  await expect(page.getByText("A pull request without a review")).toBeVisible();

  await page.locator("summary.filter-menu-button").click();
  await page
    .getByRole("group", { name: "Filter pull requests" })
    .getByLabel(/^Assigned to me/)
    .check();
  await shot(page, "4-pull-requests-menu");
  await expect(page.getByText("A pull request without a review")).toHaveCount(0);
  await expect(page.getByText("An automatically reviewed change")).toBeVisible();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Remove Assigned to me" }).click();

  // An older link's repository becomes the page's filter.
  await page.goto(page.url().replace(/\?.*$/, "") + "?page=pulls&filter=all&repo=acme/missing");
  await expect(page.locator(".filter-menu-chips")).toContainText("acme/missing");
  await expect(page).not.toHaveURL(/repo=/);
  await expect(page.getByText("A pull request without a review")).toHaveCount(0);
});

import { expect, test, type Page } from "@playwright/test";
import { deleteAllInboxes, launchURL } from "./inbox-helpers";

/** Clears every page's filter and saved tab through the daemon, so a failed
 *  run leaves none for the specs that follow. */
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
          value: { pages: {}, tabs: [] },
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

test("narrows the Inbox from the filter menu and as typed, and saves it as an inbox", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(await launchURL());
  const rows = page.locator(".upnext-list button.upnext-row");
  const requested = rows.filter({ hasText: "Reuse the existing PR page" });
  await expect(rows.first()).toBeVisible({ timeout: 15000 });
  await expect(requested).toHaveCount(1);

  // The menu lists the fields; a field's values open beside it.
  const filter = page.locator("summary.filter-menu-button");
  await filter.click();
  await page.getByRole("option", { name: /^Author/ }).hover();
  await page.getByRole("listbox", { name: "Author" }).getByRole("option", { name: /^Me/ }).click();
  await shot(page, "1-inbox-menu");
  await expect(requested).toHaveCount(0);
  const bar = page.getByRole("combobox", { name: "Filter pull requests" });
  await expect(bar).toHaveValue("author:@me");
  await expect(filter).toHaveAccessibleName("Filter, in force");

  // Closed, the bar holds the query; a reload keeps it.
  await page.keyboard.press("Escape");
  await page.keyboard.press("Escape");
  await shot(page, "2-inbox-bar");
  await page.reload();
  await expect(bar).toHaveValue("author:@me");
  await expect(requested).toHaveCount(0);

  // Typed, it suggests the fields, then their values.
  await bar.fill("");
  await bar.pressSequentially("revi");
  await expect(page.getByRole("option", { name: /^review:\s*Review$/ })).toBeVisible();
  await page.keyboard.press("Tab");
  await expect(bar).toHaveValue("review:");
  await page.getByRole("option", { name: /^review:approved/ }).click();
  await expect(bar).toHaveValue("review:approved ");
  await shot(page, "3-inbox-suggestions");
  await expect(requested).toHaveCount(1);
  await expect(rows.filter({ hasText: "A pull request without a review" })).toHaveCount(0);
  // One that does not read says why and keeps the last filter.
  await bar.pressSequentially("OR");
  await expect(
    page.getByText("Not applied: The query ends where a filter should follow."),
  ).toBeVisible();
  await expect(requested).toHaveCount(1);
  await bar.pressSequentially(" is:conflict");
  await expect(rows.filter({ hasText: "A pull request without a review" })).toHaveCount(1);

  // Save as inbox makes an inbox with the same query.
  await page.getByRole("button", { name: "Save as inbox" }).click();
  await expect(page).toHaveURL(/page=inbox-edit&inbox=[0-9a-f]{32}/);
  await expect(page.getByLabel("Name")).toHaveValue("Filtered");
  await expect(page.getByRole("combobox", { name: "Filter pull requests" })).toHaveValue(
    "review:approved OR is:conflict",
  );
  await shot(page, "4-saved-inbox-edit");

  // Back in the Inbox, Clear shows everything again.
  await page.getByRole("link", { name: "Inbox", exact: true }).first().click();
  await page.locator(".filter-bar").getByRole("button", { name: "Clear", exact: true }).click();
  await expect(bar).toHaveCount(0);
  await expect(rows.filter({ hasText: "A pull request without a review" })).toHaveCount(1);
  expect(errors).toEqual([]);
});

test("filters Pull requests with the keys, saves a tab, and reads a repository from a link", async ({
  page,
}) => {
  await page.goto(await launchURL());
  await expect(page.locator(".upnext-list button.upnext-row").first()).toBeVisible({
    timeout: 15000,
  });
  await page.goto(page.url().replace(/\?.*$/, "") + "?page=pulls&filter=mine");
  await expect(page.getByText("An automatically reviewed change")).toBeVisible();
  await expect(page.getByText("A pull request without a review")).toBeVisible();

  await page.locator("summary.filter-menu-button").click();
  await page.getByRole("combobox", { name: "Add filter" }).fill("assig");
  await page.keyboard.press("Enter");
  await page.keyboard.press("Enter");
  await shot(page, "5-pull-requests-menu");
  await expect(page.getByText("A pull request without a review")).toHaveCount(0);
  await expect(page.getByText("An automatically reviewed change")).toBeVisible();
  await page.keyboard.press("Escape");
  await page.keyboard.press("Escape");
  const bar = page.getByRole("combobox", { name: "Filter pull requests" });
  await expect(bar).toHaveValue("assignee:@me");

  // Saved as a tab, the filter moves into it and stays across a reload.
  await page.getByRole("button", { name: "Save as tab" }).click();
  await page.getByLabel("Tab name").fill("Assigned");
  await page.keyboard.press("Enter");
  const tab = page.getByRole("button", { name: /^Assigned/ });
  await expect(tab).toHaveAttribute("aria-pressed", "true");
  await expect(page).toHaveURL(/filter=tab%3A[0-9a-f]{32}|filter=tab:[0-9a-f]{32}/);
  await shot(page, "6-pull-requests-tab");
  await page.reload();
  await expect(tab).toHaveAttribute("aria-pressed", "true");
  await expect(bar).toHaveValue("assignee:@me");
  await expect(page.getByText("A pull request without a review")).toHaveCount(0);
  await page.getByRole("button", { name: "Delete tab" }).click();
  await expect(tab).toHaveCount(0);
  await expect(page.getByText("A pull request without a review")).toBeVisible();

  // An older link's repository becomes the page's filter.
  await page.goto(page.url().replace(/\?.*$/, "") + "?page=pulls&filter=all&repo=acme/missing");
  await expect(bar).toHaveValue("repo:acme/missing");
  await expect(page).not.toHaveURL(/repo=/);
  await expect(page.getByText("A pull request without a review")).toHaveCount(0);
});

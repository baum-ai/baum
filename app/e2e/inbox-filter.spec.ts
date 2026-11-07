import { expect, test, type Page } from "@playwright/test";
import { resetInboxes, launchURL } from "./inbox-helpers";

/** Clears every page's filter and saved tab through the daemon, so a failed
 *  run leaves none for the specs that follow. */
async function clearFilters(page: Page) {
  try {
    if (!page.url().includes("/s/")) return;
    await page.evaluate(async () => {
      localStorage.removeItem("baum.pulls.tab.v1");
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
  await resetInboxes(page);
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

test("filters the Inbox from the menu and as typed, saves an inbox, and edits a saved one", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(await launchURL());
  const rows = page.locator(".upnext-list button.upnext-row");
  const requested = rows.filter({ hasText: "Reuse the existing PR page" });
  const conflicted = rows.filter({ hasText: "A pull request without a review" });
  await expect(rows.first()).toBeVisible({ timeout: 15000 });
  await expect(requested).toHaveCount(1);

  // The menu lists the fields; a field's values open beside it.
  const filter = page.locator("summary.filter-menu-button");
  const panel = page.locator(".filter-menu-panel");
  await page.keyboard.press("/");
  await expect(page.getByRole("combobox", { name: "Add filter", exact: true })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(panel).toHaveCount(0);
  await expect(filter).not.toBeFocused();
  await filter.click();
  await expect(page.getByRole("combobox", { name: "Add filter", exact: true })).toBeFocused();
  await page.getByRole("option", { name: /^Author/ }).hover();
  await page.getByRole("listbox", { name: "Author" }).getByRole("option", { name: /^Me/ }).click();
  await expect(requested).toHaveCount(0);
  // The query is at the menu's foot, and only there.
  const bar = panel.getByRole("combobox", { name: "Filter pull requests" });
  await expect(bar).toHaveValue("author=@me");
  await shot(page, "1-inbox-menu");
  // The panel stays on screen.
  const box = (await panel.boundingBox())!;
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(page.viewportSize()!.width);

  // Closed, the icon is lit and no query line shows; a reload keeps it.
  await page.keyboard.press("Escape");
  await page.keyboard.press("Escape");
  await expect(panel).toHaveCount(0);
  await expect(filter).toHaveAccessibleName("Filter, in force");
  await expect(page.locator(".upnext-list-head .filter-bar")).toHaveCount(0);
  await shot(page, "2-inbox-lit");
  await page.reload();
  await expect(requested).toHaveCount(0);

  // Typed, it suggests a field, then its operators, then its values.
  await filter.click();
  await page.keyboard.down("Alt");
  await expect(filter.locator(".key-hint")).toBeVisible();
  await expect(
    panel.getByRole("button", { name: "Clear", exact: true }).locator(".key-hint"),
  ).toBeVisible();
  await page.keyboard.up("Alt");
  await expect(filter.locator(".key-hint")).toBeHidden();
  await page.keyboard.press("Control+c");
  await expect(bar).toHaveCount(0);
  const addFilter = page.getByRole("combobox", { name: "Add filter", exact: true });
  await expect(addFilter).toBeFocused();
  await addFilter.fill("author=@me");
  await page.keyboard.press("Enter");
  await filter.click();
  await bar.fill("");
  await bar.pressSequentially("revi");
  await expect(page.getByRole("option", { name: /^review\s*Review$/ })).toBeVisible();
  await page.keyboard.press("Tab");
  await expect(bar).toHaveValue("review");
  await expect(page.getByRole("option", { name: /^review!=\s*is not$/ })).toBeVisible();
  await page.keyboard.press("Tab");
  await expect(bar).toHaveValue("review=");
  await page.getByRole("option", { name: /^review=approved/ }).click();
  await expect(bar).toHaveValue("review=approved ");
  await shot(page, "3-inbox-suggestions");
  await expect(requested).toHaveCount(1);
  await expect(conflicted).toHaveCount(0);
  // One that does not read says why and keeps the last filter.
  await bar.pressSequentially("OR");
  await expect(
    page.getByText("Not applied: The query ends where a filter should follow."),
  ).toBeVisible();
  await expect(requested).toHaveCount(1);
  await bar.pressSequentially(" is=conflict");
  await expect(conflicted).toHaveCount(1);

  // Create inbox makes an inbox with the same query.
  await bar.press("Control+Enter");
  await expect(page).toHaveURL(/page=inbox-edit&inbox=[0-9a-f]{32}/);
  const name = page.getByLabel("Name", { exact: true });
  // Made from the Default inbox's filter, it is named after it.
  await expect(name).toHaveValue("Default 2");
  await expect(name).toBeFocused();
  await name.fill("Filtered");
  await name.press("Control+Enter");
  await expect(page).toHaveURL(/\?inbox=[0-9a-f]{32}/);
  await expect(page.locator(".upnext-list-title h1")).toHaveText("Filtered");
  const edited = page.locator(".inbox-query textarea");
  const sidebar = page.getByRole("navigation", { name: "Main" });
  await sidebar
    .getByRole("link", { name: /^Default/ })
    .first()
    .click();
  await expect(filter).toHaveAccessibleName("Filter");
  await page.reload();
  await expect(filter).toHaveAccessibleName("Filter");
  await sidebar.getByRole("link", { name: /^Filtered/ }).click();
  await expect(filter).toHaveAccessibleName("Filter, in force");
  await filter.click();
  await expect(bar).toHaveValue("review=approved OR is=conflict");
  // A change is a draft until Update inbox saves it.
  await bar.fill("(review=approved OR is=conflict) is!=conflict");
  await page.keyboard.press("Enter");
  await expect(conflicted).toHaveCount(0);
  await expect(panel.locator(".filter-save-main")).toHaveText("Update inbox");
  await panel.locator("summary.filter-save-arrow").click();
  await expect(page.getByRole("menuitem", { name: "Create inbox" })).toBeVisible();
  await shot(page, "5-inbox-update");
  await panel.locator(".filter-save-main").click();
  await page.keyboard.press("Escape");
  await page.locator(".upnext-list-title h1").click({ button: "right" });
  await page.getByRole("menuitem", { name: "Edit…" }).click();
  await expect(edited).toHaveValue("(review=approved OR is=conflict) is!=conflict");

  // Its name and colour change in the queue's head.
  await page.getByRole("link", { name: /^← Filtered/ }).click();
  await page.locator(".upnext-list-title h1 button").click();
  await page.getByLabel("Inbox name").fill("Approved");
  await page.keyboard.press("Enter");
  await expect(page.locator(".upnext-list-title h1")).toHaveText("Approved");
  await page.locator("summary.inbox-head-dot").click();
  await page.getByRole("radio", { name: "Green" }).click();
  await expect(page.locator(".upnext-list-title")).toHaveClass(/inbox-green/);
  await expect(sidebar.getByRole("link", { name: /^Approved/ })).toBeVisible();

  // A right click on it in the sidebar offers Edit and Delete.
  await sidebar.getByRole("link", { name: /^Approved/ }).click({ button: "right" });
  await expect(page.locator(".local-rail-menu button")).toHaveText(["Edit inbox", "Delete inbox"]);
  await page.keyboard.press("Escape");
  expect(errors).toEqual([]);
});

test("filters Pull requests, saves and edits a tab, and reads a repository from a link", async ({
  page,
}) => {
  await page.goto(await launchURL());
  await expect(page.locator(".upnext-list button.upnext-row").first()).toBeVisible({
    timeout: 15000,
  });
  await page.goto(page.url().replace(/\?.*$/, "") + "?page=pulls");
  const tabs = page.getByRole("group", { name: "Show" }).getByRole("button");
  await expect(tabs).toHaveText([/^Open/, /^Closed/]);
  await expect(tabs.first()).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByText("An automatically reviewed change")).toBeVisible();
  await expect(page.getByText("A pull request without a review")).toBeVisible();

  const filter = page.locator("summary.filter-menu-button");
  const panel = page.locator(".filter-menu-panel");
  const bar = panel.getByRole("combobox", { name: "Filter pull requests" });
  await page.keyboard.press("/");
  await expect(page.getByRole("combobox", { name: "Add filter", exact: true })).toBeFocused();
  await page.getByRole("combobox", { name: "Add filter" }).fill("assig");
  await page.keyboard.press("Enter");
  await page.keyboard.press("Enter");
  // Tab moves the operator the values are picked with, and back.
  const assignee = page.getByRole("listbox", { name: "Assignee" });
  await page.keyboard.press("Tab");
  await expect(assignee.getByRole("radio", { name: "is not" })).toHaveAttribute(
    "aria-checked",
    "true",
  );
  await page.keyboard.press("Shift+Tab");
  await expect(assignee.getByRole("radio", { name: "is", exact: true })).toHaveAttribute(
    "aria-checked",
    "true",
  );
  await shot(page, "6-pull-requests-menu");
  const box = (await panel.boundingBox())!;
  expect(box.x + box.width).toBeLessThanOrEqual(page.viewportSize()!.width);
  await expect(page.getByText("A pull request without a review")).toHaveCount(0);
  await expect(page.getByText("An automatically reviewed change")).toBeVisible();
  await expect(bar).toHaveValue("assignee=@me");

  // Saved as a tab, it sits between Open and Closed.
  await panel.getByRole("button", { name: "Create tab" }).click();
  await page.getByLabel("Tab name").fill("Assigned");
  await page.keyboard.press("Enter");
  const tab = page.getByRole("button", { name: /^Assigned/ });
  await expect(tab).toHaveAttribute("aria-pressed", "true");
  await expect(tabs).toHaveText([/^Open/, /^Assigned/, /^Closed/]);
  await expect(page).toHaveURL(/filter=tab%3A[0-9a-f]{32}|filter=tab:[0-9a-f]{32}/);
  await page.keyboard.press("Escape");
  await expect(filter).toHaveAccessibleName("Filter, in force");
  await shot(page, "7-pull-requests-tab");

  // Back from another page, the tab last shown opens.
  const sidebar = page.getByRole("navigation", { name: "Main" });
  await page.locator(".local-rail").getByRole("link", { name: /^Jobs/ }).click();
  await sidebar.getByRole("link", { name: /^Pull requests/ }).click();
  await expect(tab).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByText("A pull request without a review")).toHaveCount(0);

  // The menu starts from the tab's query, its values ticked; a change is a
  // draft until Update tab.
  await filter.click();
  await expect(bar).toHaveValue("assignee=@me");
  await page.getByRole("option", { name: /^Assignee/ }).hover();
  await expect(
    page.getByRole("listbox", { name: "Assignee" }).getByRole("option", { name: /^Me/ }),
  ).toHaveAttribute("aria-selected", "true");
  await bar.fill("assignee=@me automatically");
  await page.keyboard.press("Enter");
  await panel.locator(".filter-save-main").click();
  await page.keyboard.press("Escape");

  // Edit and Delete are in the tab's right-click menu.
  await tab.click({ button: "right" });
  await page.getByRole("menuitem", { name: "Edit…" }).click();
  await expect(bar).toHaveValue("assignee=@me automatically");
  await page.getByLabel("Tab name").fill("Mine assigned");
  await page.getByRole("button", { name: "Done" }).click();
  const renamed = page.getByRole("button", { name: /^Mine assigned/ });
  await expect(renamed).toHaveAttribute("aria-pressed", "true");
  await page.keyboard.press("Escape");
  await renamed.click({ button: "right" });
  await page.getByRole("menuitem", { name: "Delete…" }).click();
  await expect(page.getByRole("dialog")).toContainText("Only this tab goes.");
  await page.getByRole("dialog").getByRole("button", { name: "Delete tab" }).click();
  await expect(renamed).toHaveCount(0);
  await expect(page.getByText("A pull request without a review")).toBeVisible();

  // An older link's repository becomes the page's filter.
  await page.goto(page.url().replace(/\?.*$/, "") + "?page=pulls&filter=open&repo=acme/missing");
  await expect(page).not.toHaveURL(/repo=/);
  await expect(page.getByText("A pull request without a review")).toHaveCount(0);
  await filter.click();
  await expect(bar).toHaveValue("repo=acme/missing");

  // The menu picks an operator, then values with it.
  await page.getByRole("option", { name: /^State/ }).hover();
  const state = page.getByRole("listbox", { name: "State" });
  await state.getByRole("radio", { name: "is not" }).click();
  await state.getByRole("option", { name: /^Draft/ }).click();
  await expect(bar).toHaveValue("repo=acme/missing is!=draft");
});

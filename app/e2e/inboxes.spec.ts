import { expect, test, type Locator, type Page } from "@playwright/test";
import { clearSnoozes, FOR_ME, launchURL, resetInboxes } from "./inbox-helpers";

/** The sidebar's Inbox heading, which is not a link. */
const heading = (page: Page): Locator =>
  page.getByRole("navigation", { name: "Main" }).locator(".local-rail-heading");

/** The total on the Inbox heading; 0 when it shows none. */
async function inboxTotal(page: Page): Promise<number> {
  const badge = heading(page).locator(".local-rail-badge");
  return (await badge.count()) === 0 ? 0 : Number(await badge.textContent());
}

const SHAPE = {
  color: "blue",
  what: "any",
  owners: [],
  risk: "any",
  repositories: [],
  no_bots: false,
};

/** Saves these inboxes (id, name, query, badge) through the daemon's API from
 *  the signed-in page, then reloads so the app reads them. */
async function saveInboxes(
  page: Page,
  list: { id: string; name: string; query: string; badge?: boolean }[],
): Promise<void> {
  await page.evaluate(
    async ({ list, shape }) => {
      const read = await fetch("api/inboxes", { cache: "no-store" });
      const { revision } = (await read.json()) as { revision: number };
      const write = await fetch("api/inboxes", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          revision,
          inboxes: list.map((one) => ({ ...shape, badge: true, ...one })),
        }),
      });
      if (!write.ok) throw new Error(`saving inboxes answered ${write.status}`);
    },
    { list, shape: SHAPE },
  );
  await page.reload();
}

const ONE = "0000000000000000000000000000000a";
const TWO = "0000000000000000000000000000000b";

// A failed or retried run leaves just the Default inbox and no snooze for the
// specs that follow.
test.afterEach(async ({ page }) => {
  await resetInboxes(page);
  await clearSnoozes(page);
});

test("a first launch opens the Default inbox, and a stale link opens the first inbox", async ({
  page,
}) => {
  await page.goto(await launchURL());
  await expect(page).toHaveURL(/\?inbox=[0-9a-f]{32}/);
  await expect(page.getByRole("heading", { name: "Default", exact: true })).toBeVisible();
  const sidebar = page.getByRole("navigation", { name: "Main" });
  await expect(sidebar.getByRole("link", { name: /^Default/ })).toHaveAttribute(
    "aria-current",
    "page",
  );
  // Inbox is a heading with the total, not a link.
  await expect(sidebar.getByRole("link", { name: /^Inbox/ })).toHaveCount(0);
  await expect(heading(page)).toContainText("Inbox");
  await expect(page.locator("button.upnext-row").first()).toBeVisible({ timeout: 15000 });

  const first = new URL(page.url()).searchParams.get("inbox")!;
  await page.goto(new URL("?inbox=nobody-has-this", page.url()).toString());
  await expect(page).toHaveURL(new RegExp(`\\?inbox=${first}$`));
  await expect(page.getByRole("heading", { name: "Default", exact: true })).toBeVisible();
});

test("the Inbox total counts each pull request once, those in no inbox, and badge-off inboxes", async ({
  page,
}) => {
  await page.goto(await launchURL());
  await expect(page.locator("button.upnext-row").first()).toBeVisible({ timeout: 15000 });
  await expect.poll(() => inboxTotal(page)).toBeGreaterThan(1);
  const total = await inboxTotal(page);
  await expect(heading(page).locator(".sr-only")).toHaveText(`, ${total} waiting`);

  // Two inboxes holding the whole queue: every pull request is in both, and
  // the second has its count switched off.
  await saveInboxes(page, [
    { id: ONE, name: "Everything", query: FOR_ME },
    { id: TWO, name: "Quiet everything", query: FOR_ME, badge: false },
  ]);
  const sidebar = page.getByRole("navigation", { name: "Main" });
  const everything = sidebar.getByRole("link", { name: /^Everything/ });
  const quiet = sidebar.getByRole("link", { name: /^Quiet everything/ });
  await expect(everything.locator(".local-rail-sub-count")).toHaveText(String(total));
  await expect(quiet.locator(".local-rail-sub-count")).toHaveCount(0);
  await expect(heading(page).locator(".local-rail-badge")).toHaveText(String(total));

  // Only the second, badge off: its pull requests still make the total.
  await saveInboxes(page, [{ id: TWO, name: "Quiet everything", query: FOR_ME, badge: false }]);
  await expect(quiet.locator(".local-rail-sub-count")).toHaveCount(0);
  await expect(heading(page).locator(".local-rail-badge")).toHaveText(String(total));

  // An inbox holding some of them leaves the others in none, yet counted.
  await saveInboxes(page, [{ id: ONE, name: "Conflicts", query: "is=conflict" }]);
  const conflicts = sidebar.getByRole("link", { name: /^Conflicts/ });
  await expect(conflicts.locator(".local-rail-sub-count")).toBeVisible();
  const some = Number(await conflicts.locator(".local-rail-sub-count").textContent());
  expect(some).toBeGreaterThan(0);
  expect(some).toBeLessThan(total);
  await expect(heading(page).locator(".local-rail-badge")).toHaveText(String(total));
  await expect(heading(page).locator(".sr-only")).toHaveText(`, ${total} waiting`);

  // An inbox without a query lists every open pull request, others' too, and
  // counts them; the total still counts only what waits on the person.
  await saveInboxes(page, [{ id: ONE, name: "All open", query: "" }]);
  const all = sidebar.getByRole("link", { name: /^All open/ });
  await expect(all.locator(".local-rail-sub-count")).toBeVisible();
  expect(Number(await all.locator(".local-rail-sub-count").textContent())).toBeGreaterThan(total);
  await expect(page.getByRole("region", { name: "Not waiting on you" })).toBeVisible();
  await expect(heading(page).locator(".local-rail-badge")).toHaveText(String(total));
});

test("a new inbox starts from Unclaimed, and a term's × in the query box removes it", async ({
  page,
}) => {
  await page.goto(await launchURL());
  await expect(page.locator("button.upnext-row").first()).toBeVisible({ timeout: 15000 });
  await page.goto(new URL("?page=inbox-new", page.url()).toString());
  await page.getByRole("button", { name: /^Unclaimed/ }).click();
  await expect(page).toHaveURL(/page=inbox-edit&inbox=[0-9a-f]{32}/);
  const box = page.locator(".inbox-query");
  const tokens = box.locator(".filter-token");
  await expect(tokens).toHaveCount(5);
  await box.getByRole("button", { name: "Remove State is not Draft" }).click();
  await expect(tokens).toHaveCount(4);
  const query = box.locator("textarea");
  await expect(query).toHaveValue("waiting=other assignee=none review-requested=none is!=bot");
  // A click on the tokens edits the text, and leaving shows them again.
  await query.click({ position: { x: 4, y: 4 } });
  await expect(query).toBeFocused();
  await expect(tokens).toHaveCount(0);
  await page.keyboard.type(" org=acme");
  await page.getByLabel("Name", { exact: true }).focus();
  await expect(tokens).toHaveCount(5);
  await expect(query).toHaveValue(
    "waiting=other assignee=none review-requested=none is!=bot org=acme",
  );
});

test("the last inbox cannot be deleted", async ({ page }) => {
  await page.goto(await launchURL());
  await expect(page.locator("button.upnext-row").first()).toBeVisible({ timeout: 15000 });
  const sidebar = page.getByRole("navigation", { name: "Main" });
  await sidebar.getByRole("link", { name: /^Default/ }).hover();
  await page.getByRole("button", { name: "More for Default" }).click();
  const remove = page.locator(".local-rail-menu").getByRole("button", { name: "Delete inbox" });
  await expect(remove).toBeDisabled();
  await expect(remove).toHaveAttribute("title", "Keep at least one inbox");
  await expect(page.locator(".local-rail-menu")).toContainText("Keep at least one inbox");
  await page.keyboard.press("Escape");

  // Nor from its own edit page.
  await page.locator(".upnext-list-title h1").click({ button: "right" });
  await page.getByRole("menuitem", { name: "Edit…" }).click();
  await expect(page.getByRole("button", { name: "Delete inbox" })).toBeDisabled();
  await expect(page.getByText("Keep at least one inbox")).toBeVisible();
});

test("a snooze in one inbox hides the pull request in the others, lowers the total and survives a reload", async ({
  page,
}) => {
  await page.goto(await launchURL());
  await expect(page.locator("button.upnext-row").first()).toBeVisible({ timeout: 15000 });
  await saveInboxes(page, [
    { id: ONE, name: "First", query: FOR_ME },
    { id: TWO, name: "Second", query: FOR_ME },
  ]);
  const sidebar = page.getByRole("navigation", { name: "Main" });
  const rows = page.locator("button.upnext-row");
  await expect(rows.first()).toBeVisible({ timeout: 15000 });
  await expect.poll(() => inboxTotal(page)).toBeGreaterThan(1);
  const total = await inboxTotal(page);
  const row = rows.first();
  const title = (await row.locator(".upnext-row-title").textContent())!;

  await row.click();
  await page.getByRole("button", { name: /^Snooze/ }).click();
  const saved = page.waitForResponse(
    (response) =>
      response.url().includes("api/ui-state/inbox") &&
      response.request().method() === "PUT" &&
      response.status() === 200,
  );
  await page.getByRole("menuitem", { name: /Tomorrow morning/ }).click();
  await saved;
  const hidden = rows.filter({ hasText: title });
  await expect(hidden).toHaveCount(0);
  await expect.poll(() => inboxTotal(page)).toBe(total - 1);

  const second = sidebar.getByRole("link", { name: /^Second/ });
  const checkOther = async () => {
    await second.click();
    await expect(page.locator(".upnext-list-title h1")).toHaveText("Second");
    await expect(hidden).toHaveCount(0);
    await expect(second.locator(".local-rail-sub-count")).toHaveText(String(total - 1));
    await expect(page.getByRole("region", { name: "Snoozed" })).toContainText(title);
    await expect(heading(page).locator(".local-rail-badge")).toHaveText(String(total - 1));
  };
  await checkOther();
  await page.evaluate(() => localStorage.clear());
  await page.reload();
  await checkOther();

  // Bring it back, so the fixture is as the specs that follow expect.
  const snoozed = page.getByRole("region", { name: "Snoozed" });
  await snoozed.getByRole("button", { name: /Snoozed/ }).click();
  await snoozed.getByRole("listitem").filter({ hasText: title }).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Bring back" }).click();
  await expect(heading(page).locator(".local-rail-badge")).toHaveText(String(total));
});

// The fixture runs a real inbox store, so an inbox made here is saved by the
// daemon and shows in the sidebar, in its queue and in the Inbox total.
test("makes an inbox from a template, edits it, hides its count and deletes it with Undo", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(await launchURL());
  await expect(page.getByRole("heading", { name: "Default", exact: true })).toBeVisible();
  await expect(page.locator("button.upnext-row").first()).toBeVisible({ timeout: 15000 });
  const sidebar = page.getByRole("navigation", { name: "Main" });

  // New inbox and Manage inboxes are in Inbox's right-click menu.
  await expect(sidebar.getByRole("link", { name: "New inbox" })).toHaveCount(0);
  const inboxMenu = async (item: string) => {
    await heading(page).click({ button: "right" });
    await page.getByRole("menuitem", { name: item }).click();
  };

  // Create one from a template: it opens its own page and joins the sidebar.
  await inboxMenu("New inbox…");
  await expect(page.getByRole("heading", { name: "What should this inbox catch?" })).toBeVisible();
  await page.getByRole("button", { name: /^For an agent/ }).click();
  await expect(page).toHaveURL(/page=inbox-edit&inbox=[0-9a-f]{32}/);
  // Nothing is saved until Save, which stands where Delete inbox would.
  await expect(page.getByText("Not saved yet. Adjust anything below, then Save.")).toBeVisible();
  await expect(page.getByLabel("Name")).toHaveValue("For an agent");
  await expect(page.getByLabel("Name")).toBeFocused();
  const inboxLink = sidebar.getByRole("link", { name: /^For an agent/ });
  await expect(inboxLink).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Delete inbox" })).toHaveCount(0);
  await page.keyboard.press("Control+Enter");
  await expect(page).toHaveURL(/\?inbox=[0-9a-f]{32}/);
  await expect(inboxLink).toBeVisible();
  // Shift+K and Shift+J move between the saved inboxes.
  await page.keyboard.press("Shift+K");
  await expect(page.getByRole("heading", { name: "Default", exact: true })).toBeVisible();
  await page.keyboard.press("Shift+J");
  await expect(page.locator(".upnext-list-title h1")).toHaveText("For an agent");
  await page.locator(".upnext-list-title h1").click({ button: "right" });
  await page.getByRole("menuitem", { name: "Edit…" }).click();
  await expect(page.getByLabel("Name")).toHaveValue("For an agent");

  // The preview holds what the inbox would show now.
  const count = page.locator(".inbox-preview-count");
  await expect(count).not.toHaveText("0");
  const reviews = Number(await count.textContent());
  await expect(page.locator(".inbox-preview-row")).toHaveCount(reviews);

  // Its query says what it holds; changing it saves, and the preview says
  // what changed.
  const query = page.locator(".inbox-query textarea");
  await expect(query).toHaveValue("waiting=agent");
  await query.fill("");
  await query.blur();
  await expect(page.locator(".inbox-diff")).toBeVisible();
  await expect(page.locator(".inbox-diff")).toContainText("added");
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  await expect(page.locator(".inbox-preview-row.gone")).toHaveCount(0);
  const everything = Number(await count.textContent());
  expect(everything).toBeGreaterThanOrEqual(reviews);
  // The queue the sidebar link opens holds what the preview showed.
  await page.reload();
  await expect(page.getByLabel("Name")).toHaveValue("For an agent");
  // The box stays, empty, saying what no filter means.
  await expect(query).toHaveValue("");
  await expect(query).toHaveAttribute("placeholder", "No filter: every open pull request");

  // Its badge switch only shows or hides the count on its own row: the
  // Inbox total never moves.
  const badge = page.getByRole("switch", { name: "Show its count in the sidebar" });
  await expect(badge).toBeChecked();
  await expect.poll(() => inboxTotal(page)).toBeGreaterThan(0);
  const total = await inboxTotal(page);
  await expect(inboxLink.locator(".local-rail-sub-count")).toHaveCount(1);
  await badge.uncheck();
  await expect(page.getByText("No count beside it, so it never nags.")).toBeVisible();
  await expect(inboxLink.locator(".local-rail-sub-count")).toHaveCount(0);
  expect(await inboxTotal(page)).toBe(total);
  await badge.check();
  await expect(inboxLink.locator(".local-rail-sub-count")).toHaveCount(1);
  expect(await inboxTotal(page)).toBe(total);

  // Manage lists it, with its count choice in its menu.
  await inboxMenu("Manage inboxes…");
  await expect(page.getByRole("heading", { name: "Inboxes", exact: true })).toBeVisible();
  await page.getByRole("listitem").filter({ hasText: "For an agent" }).hover();
  await page.getByRole("button", { name: "Actions for For an agent", exact: true }).click();
  await expect(
    page.getByRole("menuitemcheckbox", { name: "Show count in the sidebar" }),
  ).toHaveAttribute("aria-checked", "true");
  await page.keyboard.press("Escape");

  // Delete it from the sidebar, then Undo: it comes back where it was.
  await inboxLink.hover();
  await page.getByRole("button", { name: "More for For an agent" }).click();
  const deleted = page.waitForResponse(
    (response) =>
      response.request().method() === "PUT" &&
      response.url().endsWith("/api/inboxes") &&
      response.request().postDataJSON().inboxes.length === 1,
  );
  await page.locator(".local-rail-menu").getByRole("button", { name: "Delete inbox" }).click();
  // It asks first.
  await expect(page.getByRole("dialog")).toContainText("Only this view goes.");
  await page.getByRole("dialog").getByRole("button", { name: "Delete inbox" }).click();
  expect((await deleted).ok()).toBe(true);
  await expect(page.getByText("Deleted For an agent.")).toBeVisible();
  await expect(inboxLink).toHaveCount(0);
  const restored = page.waitForResponse(
    (response) =>
      response.request().method() === "PUT" &&
      response.url().endsWith("/api/inboxes") &&
      response.request().postDataJSON().inboxes.length === 2,
  );
  await page.getByRole("status").getByRole("button", { name: "Undo" }).click();
  expect((await restored).ok()).toBe(true);
  await expect(inboxLink).toBeVisible();
  // It was saved by the daemon, not just drawn.
  await page.reload();
  await expect(sidebar.getByRole("link", { name: /^For an agent/ })).toBeVisible();

  // Leave the fixture as the other specs expect it.
  await sidebar.getByRole("link", { name: /^For an agent/ }).click();
  await page.locator(".upnext-list-title h1").click({ button: "right" });
  await page.getByRole("menuitem", { name: "Edit…" }).click();
  await page.getByRole("button", { name: "Delete inbox" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Delete inbox" }).click();
  await expect(sidebar.getByRole("link", { name: /^For an agent/ })).toHaveCount(0);
  // Deleting the open inbox opens the first one left.
  await expect(page.getByRole("heading", { name: "Default", exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

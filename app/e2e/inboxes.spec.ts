import { expect, test, type Page } from "@playwright/test";
import { deleteAllInboxes, launchURL } from "./inbox-helpers";

/** The number on the Inbox link in the sidebar; 0 when it shows none. */
async function inboxBadge(page: Page): Promise<number> {
  const badge = page.locator('.local-rail-link[href$="/"] .local-rail-badge');
  return (await badge.count()) === 0 ? 0 : Number(await badge.textContent());
}

// A failed or retried run leaves no inbox for the specs that follow.
test.afterEach(async ({ page }) => {
  await deleteAllInboxes(page);
});

// The fixture runs a real inbox store, so an inbox made here is saved by the
// daemon and shows in the sidebar, in its queue and in the Inbox badge.
test("makes an inbox from a template, edits it, counts it in the badge and deletes it with Undo", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(await launchURL());
  await expect(page.getByRole("heading", { name: "Up next", exact: true })).toBeVisible();
  await expect(page.locator("button.upnext-row").first()).toBeVisible({ timeout: 15000 });
  const sidebar = page.getByRole("navigation", { name: "Main" });

  // The nested list is there with no inboxes, with its two links.
  await expect(sidebar.getByRole("link", { name: "New inbox" })).toBeVisible();
  await expect(sidebar.getByRole("link", { name: "Manage inboxes" })).toBeVisible();

  // Create one from a template: it opens its own page and joins the sidebar.
  await sidebar.getByRole("link", { name: "New inbox" }).click();
  await expect(page.getByRole("heading", { name: "What should this inbox catch?" })).toBeVisible();
  await page.getByRole("button", { name: /^Needs my review/ }).click();
  await expect(page).toHaveURL(/page=inbox-edit&inbox=[0-9a-f]{32}/);
  await expect(
    page.getByText("Created and added to the sidebar. Adjust anything below."),
  ).toBeVisible();
  await expect(page.getByLabel("Name")).toHaveValue("Needs my review");
  await expect(page.getByLabel("Name")).toBeFocused();
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  const inboxLink = sidebar.getByRole("link", { name: /^Needs my review/ });
  await expect(inboxLink).toBeVisible();

  // The preview holds what the inbox would show now.
  const count = page.locator(".inbox-preview-count");
  await expect(count).not.toHaveText("0");
  const reviews = Number(await count.textContent());
  await expect(page.locator(".inbox-preview-row")).toHaveCount(reviews);

  // Changing a part of the sentence saves, and the preview says what changed.
  const what = page.locator(".inbox-part").first();
  await expect(what).toHaveText("Reviews I’m asked for");
  await what.click();
  await page
    .getByRole("group", { name: "What it holds" })
    .getByRole("button", { name: /^Anything waiting on me/ })
    .click();
  await expect(page.locator(".inbox-diff")).toBeVisible();
  await expect(page.locator(".inbox-diff")).toContainText("added");
  await expect(page.getByText("Saved", { exact: true })).toBeVisible();
  await expect(page.locator(".inbox-preview-row.gone")).toHaveCount(0);
  const everything = Number(await count.textContent());
  expect(everything).toBeGreaterThanOrEqual(reviews);
  // The queue the sidebar link opens holds what the preview showed.
  await page.reload();
  await expect(page.getByLabel("Name")).toHaveValue("Needs my review");
  await expect(page.locator(".inbox-part").first()).toHaveText("Anything waiting on me");

  // Counting it in the Inbox badge: with it on, its pull requests add to the
  // number; leaving it out takes away those that are in no other inbox.
  const badge = page.getByRole("switch", { name: "Count it in the Inbox badge" });
  await expect(badge).toBeChecked();
  await expect.poll(() => inboxBadge(page)).toBeGreaterThan(0);
  await page.locator(".inbox-part").first().click();
  await page
    .getByRole("group", { name: "What it holds" })
    .getByRole("button", { name: /^Reviews I’m asked for/ })
    .click();
  await expect(page.locator(".inbox-part").first()).toHaveText("Reviews I’m asked for");
  const withBadge = await inboxBadge(page);
  // Pull requests of the inbox are in no other inbox, so leaving them out
  // lowers the badge by as many as the inbox holds.
  await badge.uncheck();
  await expect(page.getByText("Left out of the badge, so it never nags.")).toBeVisible();
  await expect.poll(() => inboxBadge(page)).toBe(withBadge - reviews);
  await badge.check();
  await expect.poll(() => inboxBadge(page)).toBe(withBadge);

  // Manage lists it, with its badge choice in its menu.
  await sidebar.getByRole("link", { name: "Manage inboxes" }).click();
  await expect(page.getByRole("heading", { name: "Inboxes", exact: true })).toBeVisible();
  await page.getByRole("listitem").filter({ hasText: "Needs my review" }).hover();
  await page.getByRole("button", { name: "Actions for Needs my review", exact: true }).click();
  await expect(
    page.getByRole("menuitemcheckbox", { name: "Count in the Inbox badge" }),
  ).toHaveAttribute("aria-checked", "true");
  await page.keyboard.press("Escape");

  // Delete it from the sidebar, then Undo: it comes back where it was.
  await inboxLink.hover();
  await page.getByRole("button", { name: "More for Needs my review" }).click();
  await page.getByRole("button", { name: "Delete inbox" }).click();
  await expect(
    page.getByText("Deleted Needs my review. Its pull requests stay in Inbox."),
  ).toBeVisible();
  await expect(inboxLink).toHaveCount(0);
  await page.getByRole("status").getByRole("button", { name: "Undo" }).click();
  await expect(inboxLink).toBeVisible();
  // It was saved by the daemon, not just drawn.
  await page.reload();
  await expect(sidebar.getByRole("link", { name: /^Needs my review/ })).toBeVisible();

  // Leave the fixture as the other specs expect it.
  await sidebar.getByRole("link", { name: /^Needs my review/ }).click();
  await page.getByRole("link", { name: "Edit", exact: true }).click();
  await page.getByRole("button", { name: "Delete inbox" }).click();
  await expect(sidebar.getByRole("link", { name: /^Needs my review/ })).toHaveCount(0);
  await expect(sidebar.getByRole("link", { name: /^Inbox/ })).toBeFocused();
  expect(errors).toEqual([]);
});

// At the most inboxes a person can have, the sidebar's pages and inboxes
// scroll within it: Settings stays in view, and the last inbox's menu opens
// whole, not clipped by the scrolling list.

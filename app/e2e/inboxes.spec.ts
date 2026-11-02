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

  // New inbox and Manage inboxes are in Inbox's right-click menu.
  await expect(sidebar.getByRole("link", { name: "New inbox" })).toHaveCount(0);
  const inboxMenu = async (item: string) => {
    await sidebar.getByRole("link", { name: /^Inbox/ }).click({ button: "right" });
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
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\?inbox=[0-9a-f]{32}/);
  await expect(inboxLink).toBeVisible();
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
  await expect(query).toHaveValue("");

  // Counting it in the Inbox badge: with it on, its pull requests add to the
  // number; leaving it out takes away those that are in no other inbox.
  const badge = page.getByRole("switch", { name: "Count it in the Inbox badge" });
  await expect(badge).toBeChecked();
  await expect.poll(() => inboxBadge(page)).toBeGreaterThan(0);
  await query.fill("waiting=agent");
  await query.blur();
  await expect(page.locator(".inbox-preview-count")).toHaveText(String(reviews));
  const withBadge = await inboxBadge(page);
  // Pull requests of the inbox are in no other inbox, so leaving them out
  // lowers the badge by as many as the inbox holds.
  await badge.uncheck();
  await expect(page.getByText("Left out of the badge, so it never nags.")).toBeVisible();
  await expect.poll(() => inboxBadge(page)).toBe(withBadge - reviews);
  await badge.check();
  await expect.poll(() => inboxBadge(page)).toBe(withBadge);

  // Manage lists it, with its badge choice in its menu.
  await inboxMenu("Manage inboxes…");
  await expect(page.getByRole("heading", { name: "Inboxes", exact: true })).toBeVisible();
  await page.getByRole("listitem").filter({ hasText: "For an agent" }).hover();
  await page.getByRole("button", { name: "Actions for For an agent", exact: true }).click();
  await expect(
    page.getByRole("menuitemcheckbox", { name: "Count in the Inbox badge" }),
  ).toHaveAttribute("aria-checked", "true");
  await page.keyboard.press("Escape");

  // Delete it from the sidebar, then Undo: it comes back where it was.
  await inboxLink.hover();
  await page.getByRole("button", { name: "More for For an agent" }).click();
  const deleted = page.waitForResponse(
    (response) =>
      response.request().method() === "PUT" &&
      response.url().endsWith("/api/inboxes") &&
      response.request().postDataJSON().inboxes.length === 0,
  );
  await page.locator(".local-rail-menu").getByRole("button", { name: "Delete inbox" }).click();
  // It asks first.
  await expect(page.getByRole("dialog")).toContainText("Only this view goes.");
  await page.getByRole("dialog").getByRole("button", { name: "Delete inbox" }).click();
  expect((await deleted).ok()).toBe(true);
  await expect(
    page.getByText("Deleted For an agent. Its pull requests stay in Inbox."),
  ).toBeVisible();
  await expect(inboxLink).toHaveCount(0);
  const restored = page.waitForResponse(
    (response) =>
      response.request().method() === "PUT" &&
      response.url().endsWith("/api/inboxes") &&
      response.request().postDataJSON().inboxes.length === 1,
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
  await expect(sidebar.getByRole("link", { name: /^Inbox/ })).toBeFocused();
  expect(errors).toEqual([]);
});

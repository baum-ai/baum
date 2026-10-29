import { expect, test } from "@playwright/test";
import { deleteAllInboxes, launchURL } from "./inbox-helpers";

// Pictures of the inbox pages for a pull request, light and dark. They are
// taken only when asked for (INBOX_SCREENSHOTS=1) and written to
// test-results/inbox-screens, which is not committed.
test.skip(!process.env.INBOX_SCREENSHOTS, "screenshots are taken on request");

// The inboxes made here are not left for the specs that follow.
test.afterEach(async ({ page }) => {
  await deleteAllInboxes(page);
});

test("pictures of the inbox pages", async ({ page }) => {
  test.slow();
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto(await launchURL());
  await expect(page.locator("button.upnext-row").first()).toBeVisible({ timeout: 15000 });
  const sidebar = page.getByRole("navigation", { name: "Main" });

  // Four inboxes from templates; the last is left with its badge off.
  for (const name of [/^Needs my review/, /^Quick merges/, /^For an agent/, /^Personal/]) {
    await sidebar.getByRole("link", { name: "New inbox" }).click();
    await page.getByRole("button", { name }).click();
    await expect(page).toHaveURL(/page=inbox-edit/);
  }
  const shot = (name: string, scheme: string) =>
    page.screenshot({
      path: `test-results/inbox-screens/${name}-${scheme}.png`,
      animations: "disabled",
    });

  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    // The sidebar with its nested inboxes and a row's menu.
    await page.goto(await launchURL());
    await expect(page.locator("button.upnext-row").first()).toBeVisible({ timeout: 15000 });
    const row = sidebar.getByRole("link", { name: /^Needs my review/ });
    await row.hover();
    await page.getByRole("button", { name: "More for Needs my review" }).click();
    await shot("1-sidebar-menu", scheme);
    await page.keyboard.press("Escape");

    // An inbox's queue.
    await row.click();
    await expect(page.locator(".upnext-list-title h1")).toHaveText("Needs my review");
    await shot("2-queue", scheme);

    // New inbox.
    await sidebar.getByRole("link", { name: "New inbox" }).click();
    await expect(
      page.getByRole("heading", { name: "What should this inbox catch?" }),
    ).toBeVisible();
    await shot("3-new", scheme);

    // The edit page, with a part of the sentence open.
    await page.goto(
      new URL(
        (await sidebar.getByRole("link", { name: /^Quick merges/ }).getAttribute("href"))!,
        page.url(),
      ).href,
    );
    await page.getByRole("link", { name: "Edit", exact: true }).click();
    await expect(page.getByLabel("Name")).toHaveValue("Quick merges");
    await page.locator(".inbox-part").nth(1).click();
    await shot("4-edit", scheme);
    await page.locator(".inbox-part").nth(0).click();
    await page
      .getByRole("group", { name: "What it holds" })
      .getByRole("button", { name: /^Anything waiting on me/ })
      .click();
    await expect(page.locator(".inbox-diff")).toBeVisible();
    await shot("4-edit-diff", scheme);

    // Manage.
    await sidebar.getByRole("link", { name: "Manage inboxes" }).click();
    await expect(page.getByRole("heading", { name: "Inboxes", exact: true })).toBeVisible();
    await shot("5-manage", scheme);

    // Put the inbox back as it was for the other scheme.
    await page.getByRole("link", { name: "Edit Quick merges" }).click();
    await page.locator(".inbox-part").nth(0).click();
    await page
      .getByRole("group", { name: "What it holds" })
      .getByRole("button", { name: /^Things ready to merge/ })
      .click();
  }
});

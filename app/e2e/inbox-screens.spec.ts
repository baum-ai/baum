import { expect, test } from "@playwright/test";
import { resetInboxes, launchURL } from "./inbox-helpers";

// Pictures of the inbox pages for a pull request, light and dark. They are
// taken only when asked for (INBOX_SCREENSHOTS=1) and written to
// test-results/inbox-screens, which is not committed.
test.skip(!process.env.INBOX_SCREENSHOTS, "screenshots are taken on request");

// The inboxes made here are not left for the specs that follow.
test.afterEach(async ({ page }) => {
  await resetInboxes(page);
});

test("pictures of the inbox pages", async ({ page }) => {
  test.slow();
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto(await launchURL());
  await expect(page.locator("button.upnext-row").first()).toBeVisible({ timeout: 15000 });
  const sidebar = page.getByRole("navigation", { name: "Main" });

  // Four inboxes from templates; the last is left with its badge off.
  for (const name of [/^Custom/, /^Quick merges/, /^For an agent/, /^Personal/]) {
    await sidebar.locator(".local-rail-heading").click({ button: "right" });
    await page.getByRole("menuitem", { name: "New inbox…" }).click();
    await page.getByRole("button", { name }).click();
    await expect(page).toHaveURL(/page=inbox-edit/);
    await page.getByRole("button", { name: "Save", exact: true }).click();
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
    const row = sidebar.getByRole("link", { name: /^Custom/ });
    await row.hover();
    await page.getByRole("button", { name: "More for Custom" }).click();
    await shot("1-sidebar-menu", scheme);
    await page.keyboard.press("Escape");

    // An inbox's queue.
    await row.click();
    await expect(page.locator(".upnext-list-title h1")).toHaveText("Custom");
    await shot("2-queue", scheme);
    // Renaming in place: the filter and refresh stay right of the field.
    await page.locator(".upnext-list-title h1 button").click();
    const name = page.getByRole("textbox", { name: "Inbox name" });
    await expect(name).toBeFocused();
    const field = (await name.boundingBox())!;
    const filter = (await page.locator(".upnext-list-title > .filter-menu").boundingBox())!;
    expect(field.x + field.width).toBeLessThanOrEqual(filter.x);
    await shot("2-rename", scheme);
    await page.keyboard.press("Escape");

    // New inbox.
    await sidebar.locator(".local-rail-heading").click({ button: "right" });
    await page.getByRole("menuitem", { name: "New inbox…" }).click();
    await expect(
      page.getByRole("heading", { name: "What should this inbox catch?" }),
    ).toBeVisible();
    await shot("3-new", scheme);

    // The edit page, with its query.
    await page.goto(
      new URL(
        (await sidebar.getByRole("link", { name: /^Quick merges/ }).getAttribute("href"))!,
        page.url(),
      ).href,
    );
    await page.locator(".upnext-list-title h1").click({ button: "right" });
    await page.getByRole("menuitem", { name: "Edit…" }).click();
    await expect(page.getByLabel("Name")).toHaveValue("Quick merges");
    await shot("4-edit", scheme);
    const query = page.locator(".inbox-query textarea");
    await query.fill("");
    await query.blur();
    await expect(page.locator(".inbox-diff")).toBeVisible();
    await shot("4-edit-diff", scheme);

    // Manage.
    await sidebar.locator(".local-rail-heading").click({ button: "right" });
    await page.getByRole("menuitem", { name: "Manage inboxes…" }).click();
    await expect(page.getByRole("heading", { name: "Inboxes", exact: true })).toBeVisible();
    await shot("5-manage", scheme);

    // Put the inbox back as it was for the other scheme.
    await page.getByRole("link", { name: "Edit Quick merges" }).click();
    await query.fill("waiting=merge");
    await query.blur();
  }
});
